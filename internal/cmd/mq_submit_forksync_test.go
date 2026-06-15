package cmd

import (
	"errors"
	"strings"
	"testing"
)

// fakeSubmitGitOps is a programmable stub for submitGitOps used to exercise
// detectStaleForkSyncAtSubmit without a real git repo. It records calls so
// tests can assert the fetch-then-ancestry ordering and fail-open behavior.
type fakeSubmitGitOps struct {
	fetchErr error

	// isAncestor[ancestor+"->"+descendant] = (bool, err).
	isAncestor map[string]struct {
		ok  bool
		err error
	}

	calls []string
}

func (f *fakeSubmitGitOps) FetchRemoteBranch(remote, branch string) error {
	f.calls = append(f.calls, "FetchRemoteBranch("+remote+"/"+branch+")")
	return f.fetchErr
}

func (f *fakeSubmitGitOps) IsAncestor(ancestor, descendant string) (bool, error) {
	key := ancestor + "->" + descendant
	f.calls = append(f.calls, "IsAncestor("+key+")")
	if r, ok := f.isAncestor[key]; ok {
		return r.ok, r.err
	}
	return false, nil
}

// TestDetectStaleForkSyncAtSubmit_Current covers the healthy case: the fork-sync
// branch contains the current origin/<target> tip, so it is NOT stale.
func TestDetectStaleForkSyncAtSubmit_Current(t *testing.T) {
	g := &fakeSubmitGitOps{
		isAncestor: map[string]struct {
			ok  bool
			err error
		}{
			"origin/main->deadbeef": {ok: true},
		},
	}
	stale, reason, err := detectStaleForkSyncAtSubmit(g, "main", "deadbeef")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale {
		t.Errorf("expected stale=false when origin/main is contained in branch, got true (reason=%q)", reason)
	}
	// Must fetch before probing ancestry so the comparison is against the live tip.
	if len(g.calls) < 2 || g.calls[0] != "FetchRemoteBranch(origin/main)" {
		t.Errorf("expected fetch before ancestry probe, got calls=%v", g.calls)
	}
}

// TestDetectStaleForkSyncAtSubmit_Stale covers the regression case (gc-hdi17t):
// origin/main has moved ahead of the branch's snapshot, so the branch does NOT
// contain the current tip and must be flagged stale.
func TestDetectStaleForkSyncAtSubmit_Stale(t *testing.T) {
	g := &fakeSubmitGitOps{
		isAncestor: map[string]struct {
			ok  bool
			err error
		}{
			"origin/main->staletip": {ok: false},
		},
	}
	stale, reason, err := detectStaleForkSyncAtSubmit(g, "main", "staletip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Fatalf("expected stale=true when origin/main is not contained in branch")
	}
	if !strings.Contains(reason, "stale snapshot") {
		t.Errorf("expected reason to explain staleness, got %q", reason)
	}
}

// TestDetectStaleForkSyncAtSubmit_FailOpenOnFetchError verifies the fail-open
// stance: a fetch failure must NOT flag the branch stale (the refinery
// merge-gate re-checks before the merge lands). It also must not probe
// ancestry against a possibly-stale local ref.
func TestDetectStaleForkSyncAtSubmit_FailOpenOnFetchError(t *testing.T) {
	g := &fakeSubmitGitOps{fetchErr: errors.New("network down")}
	stale, _, err := detectStaleForkSyncAtSubmit(g, "main", "tip")
	if err == nil {
		t.Errorf("expected the fetch error to be surfaced to the caller")
	}
	if stale {
		t.Errorf("expected stale=false on fetch error (fail-open), got true")
	}
	for _, c := range g.calls {
		if strings.HasPrefix(c, "IsAncestor") {
			t.Errorf("must not probe ancestry after a fetch failure, got calls=%v", g.calls)
		}
	}
}

// TestDetectStaleForkSyncAtSubmit_FailOpenOnAncestryError verifies that an
// ancestry-probe error fails open (stale=false) rather than blocking a submit.
func TestDetectStaleForkSyncAtSubmit_FailOpenOnAncestryError(t *testing.T) {
	g := &fakeSubmitGitOps{
		isAncestor: map[string]struct {
			ok  bool
			err error
		}{
			"origin/main->tip": {err: errors.New("bad object")},
		},
	}
	stale, _, err := detectStaleForkSyncAtSubmit(g, "main", "tip")
	if err == nil {
		t.Errorf("expected the ancestry error to be surfaced to the caller")
	}
	if stale {
		t.Errorf("expected stale=false on ancestry error (fail-open), got true")
	}
}

// TestDetectStaleForkSyncAtSubmit_EmptyInputs covers the guard for missing
// target/tip — neither should trigger a fetch or flag stale.
func TestDetectStaleForkSyncAtSubmit_EmptyInputs(t *testing.T) {
	for _, tc := range []struct{ target, tip string }{
		{"", "tip"},
		{"main", ""},
	} {
		g := &fakeSubmitGitOps{}
		stale, _, err := detectStaleForkSyncAtSubmit(g, tc.target, tc.tip)
		if err != nil {
			t.Errorf("target=%q tip=%q: unexpected error: %v", tc.target, tc.tip, err)
		}
		if stale {
			t.Errorf("target=%q tip=%q: expected stale=false for empty input", tc.target, tc.tip)
		}
		if len(g.calls) != 0 {
			t.Errorf("target=%q tip=%q: expected no git calls for empty input, got %v", tc.target, tc.tip, g.calls)
		}
	}
}

// TestIsForkSyncBranchGatesStaleCheck documents which branch names the
// submit-time stale check applies to. Only fork-sync branches are gated — a
// normal feature branch is expected to be behind main and must never be
// flagged (it is rebased forward by the merge).
func TestIsForkSyncBranchGatesStaleCheck(t *testing.T) {
	forkSync := []string{
		"sync/upstream-main-gu-t6zhb",
		"sync/fork-main",
		"upstream-sync/gastown_upstream/gu-sync-att-1234",
		"polecat/quartz/fork-sync-gu-3e3x9",
		"rebase-upstream-main",
	}
	for _, b := range forkSync {
		if !isForkSyncBranch(b) {
			t.Errorf("expected %q to be classified as fork-sync (would be gated)", b)
		}
	}

	regular := []string{
		"polecat/quartz/gu-3e3x9",
		"fix/dolt-max-connections-1000",
		"feature/new-thing",
		"main",
	}
	for _, b := range regular {
		if isForkSyncBranch(b) {
			t.Errorf("expected %q NOT to be classified as fork-sync (must not be gated)", b)
		}
	}
}

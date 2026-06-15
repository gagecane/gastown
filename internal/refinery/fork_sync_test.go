package refinery

import (
	"errors"
	"testing"
)

// fakeGitOps is a programmable stub for gitForkSyncOps used by unit tests.
// Each field maps a (method, arg-tuple) to the desired return values so
// cases can exercise the decision matrix without a real git repo.
type fakeGitOps struct {
	// refExists[ref] = (bool, err). Missing entry = not found, no error.
	refExists map[string]fakeRefExistsResult

	// isAncestor[ancestor+"->"+descendant] = (bool, err).
	isAncestor map[string]fakeIsAncestorResult

	// calls records the sequence of calls made (for ordering assertions).
	calls []string
}

type fakeRefExistsResult struct {
	ok  bool
	err error
}

type fakeIsAncestorResult struct {
	ok  bool
	err error
}

func (f *fakeGitOps) RefExists(ref string) (bool, error) {
	f.calls = append(f.calls, "RefExists("+ref+")")
	if r, ok := f.refExists[ref]; ok {
		return r.ok, r.err
	}
	return false, nil
}

func (f *fakeGitOps) IsAncestor(ancestor, descendant string) (bool, error) {
	key := ancestor + "->" + descendant
	f.calls = append(f.calls, "IsAncestor("+key+")")
	if r, ok := f.isAncestor[key]; ok {
		return r.ok, r.err
	}
	return false, nil
}

// TestPreserveForkSyncTopology_NoUpstreamRemote covers the common non-fork
// case: repos without an `upstream` remote must not trigger the preservation
// path. This is the hot path for every regular MR.
func TestPreserveForkSyncTopology_NoUpstreamRemote(t *testing.T) {
	g := &fakeGitOps{
		// upstream/main does NOT exist — this is a plain (non-fork) repo.
	}
	decision, err := preserveForkSyncTopology(g, "polecat/branch", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Preserve {
		t.Errorf("expected Preserve=false when upstream remote is absent, got true (reason=%q)", decision.Reason)
	}
	if decision.Reason == "" {
		t.Errorf("expected a non-empty Reason for observability")
	}
	// Must not descend into ancestor checks once the upstream ref is known absent.
	for _, c := range g.calls {
		if c != "RefExists(upstream/main)" {
			t.Errorf("unexpected call after ref-missing short-circuit: %s", c)
		}
	}
}

// TestPreserveForkSyncTopology_ForkSyncBranch is the core positive case:
// the polecat branch has integrated upstream/main (via a merge commit) but
// origin/main has not. Refinery MUST preserve topology.
func TestPreserveForkSyncTopology_ForkSyncBranch(t *testing.T) {
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/main->polecat/fork-sync": {ok: true},  // branch has upstream
			"upstream/main->origin/main":       {ok: false}, // target does not
		},
	}
	decision, err := preserveForkSyncTopology(g, "polecat/fork-sync", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !decision.Preserve {
		t.Fatalf("expected Preserve=true for fork-sync branch, got false (reason=%q)", decision.Reason)
	}
	if decision.UpstreamRef != "upstream/main" {
		t.Errorf("expected UpstreamRef=upstream/main, got %q", decision.UpstreamRef)
	}
}

// TestPreserveForkSyncTopology_BranchHasNoUpstream covers a regular polecat
// branch in a fork repo that did NOT merge upstream. Must squash as usual.
func TestPreserveForkSyncTopology_BranchHasNoUpstream(t *testing.T) {
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/main->polecat/regular": {ok: false},
			// origin/main ancestor check never runs — we short-circuit first.
		},
	}
	decision, err := preserveForkSyncTopology(g, "polecat/regular", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Preserve {
		t.Errorf("expected Preserve=false for non-fork-sync branch, got true (reason=%q)", decision.Reason)
	}
}

// TestPreserveForkSyncTopology_TargetAlreadyCaughtUp covers the case where
// origin/main already has upstream/main as ancestor (a previous fork-sync
// landed successfully with preservation). In this case even if the branch
// re-merged upstream, there's nothing new to preserve — squash is fine.
func TestPreserveForkSyncTopology_TargetAlreadyCaughtUp(t *testing.T) {
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/main->polecat/x":   {ok: true},
			"upstream/main->origin/main": {ok: true}, // already integrated
		},
	}
	decision, err := preserveForkSyncTopology(g, "polecat/x", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Preserve {
		t.Errorf("expected Preserve=false when target already has upstream ancestor, got true (reason=%q)", decision.Reason)
	}
}

// TestPreserveForkSyncTopology_RefExistsError: unexpected git failure when
// probing for the upstream ref. Must return the error and Preserve=false so
// the caller can log and fall back to the safe squash path.
func TestPreserveForkSyncTopology_RefExistsError(t *testing.T) {
	bang := errors.New("disk I/O error")
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {err: bang},
		},
	}
	decision, err := preserveForkSyncTopology(g, "polecat/x", "main")
	if !errors.Is(err, bang) {
		t.Fatalf("expected error %v to be returned, got %v", bang, err)
	}
	if decision.Preserve {
		t.Errorf("expected Preserve=false on git error, got true")
	}
}

// TestPreserveForkSyncTopology_IsAncestorError_Branch: git failure when
// checking if branch has upstream. Same fail-safe behavior — return the
// error with Preserve=false.
func TestPreserveForkSyncTopology_IsAncestorError_Branch(t *testing.T) {
	bang := errors.New("broken pack")
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/main->polecat/x": {err: bang},
		},
	}
	decision, err := preserveForkSyncTopology(g, "polecat/x", "main")
	if !errors.Is(err, bang) {
		t.Fatalf("expected error %v, got %v", bang, err)
	}
	if decision.Preserve {
		t.Errorf("expected Preserve=false on IsAncestor error, got true")
	}
}

// TestPreserveForkSyncTopology_IsAncestorError_Target: git failure when
// checking if target has upstream (the second ancestor probe).
func TestPreserveForkSyncTopology_IsAncestorError_Target(t *testing.T) {
	bang := errors.New("ref missing")
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/main->polecat/x":   {ok: true},
			"upstream/main->origin/main": {err: bang},
		},
	}
	decision, err := preserveForkSyncTopology(g, "polecat/x", "main")
	if !errors.Is(err, bang) {
		t.Fatalf("expected error %v, got %v", bang, err)
	}
	if decision.Preserve {
		t.Errorf("expected Preserve=false on target IsAncestor error, got true")
	}
}

// TestPreserveForkSyncTopology_EmptyInputs guards against caller bugs that
// pass empty strings. Must not touch git and must not crash.
func TestPreserveForkSyncTopology_EmptyInputs(t *testing.T) {
	for _, tc := range []struct {
		name, branch, target string
	}{
		{"empty branch", "", "main"},
		{"empty target", "polecat/x", ""},
		{"both empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeGitOps{}
			decision, err := preserveForkSyncTopology(g, tc.branch, tc.target)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision.Preserve {
				t.Errorf("expected Preserve=false on empty inputs, got true")
			}
			if len(g.calls) != 0 {
				t.Errorf("expected no git calls on empty inputs, got %v", g.calls)
			}
		})
	}
}

// TestPreserveForkSyncTopology_NilOps: defensive check that a nil git ops
// returns an error rather than panicking. Real callers should never trigger
// this but a dropped wire-up in a test fixture would.
func TestPreserveForkSyncTopology_NilOps(t *testing.T) {
	decision, err := preserveForkSyncTopology(nil, "polecat/x", "main")
	if err == nil {
		t.Fatal("expected error for nil git ops, got nil")
	}
	if decision.Preserve {
		t.Error("expected Preserve=false for nil git ops")
	}
}

// TestPreserveForkSyncTopology_CustomTarget covers fork-sync to a branch
// other than "main" (e.g., long-lived release branches). The helper must
// not hard-code "main" anywhere.
func TestPreserveForkSyncTopology_CustomTarget(t *testing.T) {
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/release-1.0": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/release-1.0->polecat/sync":       {ok: true},
			"upstream/release-1.0->origin/release-1.0": {ok: false},
		},
	}
	decision, err := preserveForkSyncTopology(g, "polecat/sync", "release-1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !decision.Preserve {
		t.Errorf("expected Preserve=true for release-branch fork-sync, got false (reason=%q)", decision.Reason)
	}
	if decision.UpstreamRef != "upstream/release-1.0" {
		t.Errorf("expected UpstreamRef=upstream/release-1.0, got %q", decision.UpstreamRef)
	}
}

// TestDetectStaleForkSync_StaleBranch is the core positive case (gc-hdi17t,
// gc-ailhrp): a fork-sync branch that integrated upstream/main but whose base
// does NOT contain the current origin/main tip — it was generated off a stale
// snapshot and would revert commits that landed since. Must be flagged stale.
func TestDetectStaleForkSync_StaleBranch(t *testing.T) {
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/main->polecat/fork-sync": {ok: true},  // is a fork-sync branch
			"origin/main->polecat/fork-sync":   {ok: false}, // but does NOT contain current main
		},
	}
	decision, err := detectStaleForkSync(g, "polecat/fork-sync", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !decision.Stale {
		t.Fatalf("expected Stale=true for stale fork-sync, got false (reason=%q)", decision.Reason)
	}
	if decision.Reason == "" {
		t.Errorf("expected a non-empty Reason for observability")
	}
}

// TestDetectStaleForkSync_CurrentBranch: a fork-sync branch whose base DOES
// contain the current origin/main tip is fresh — merging it adds upstream
// without reverting anything. Must NOT be flagged stale.
func TestDetectStaleForkSync_CurrentBranch(t *testing.T) {
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/main->polecat/fork-sync": {ok: true}, // is a fork-sync branch
			"origin/main->polecat/fork-sync":   {ok: true}, // contains current main — fresh
		},
	}
	decision, err := detectStaleForkSync(g, "polecat/fork-sync", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Stale {
		t.Errorf("expected Stale=false for a current fork-sync, got true (reason=%q)", decision.Reason)
	}
}

// TestDetectStaleForkSync_NotForkSyncBranch: a plain feature branch (did not
// integrate upstream) is out of scope. It is allowed to be behind origin/main
// — the merge fast-forwards it — so the guard must NOT fire and must not even
// probe origin/main (short-circuit after the upstream-ancestor check). Firing
// here would wedge every normal MR.
func TestDetectStaleForkSync_NotForkSyncBranch(t *testing.T) {
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/main->polecat/regular": {ok: false}, // not a fork-sync branch
		},
	}
	decision, err := detectStaleForkSync(g, "polecat/regular", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Stale {
		t.Errorf("expected Stale=false for a non-fork-sync branch, got true (reason=%q)", decision.Reason)
	}
	// Must short-circuit before probing origin/main ancestry.
	for _, c := range g.calls {
		if c == "IsAncestor(origin/main->polecat/regular)" {
			t.Errorf("guard probed origin/main for a non-fork-sync branch (should short-circuit): calls=%v", g.calls)
		}
	}
}

// TestDetectStaleForkSync_NoUpstreamRemote: a non-fork repo (no upstream
// remote) has no fork-sync semantics. Guard must not fire, and must not
// descend into ancestor checks.
func TestDetectStaleForkSync_NoUpstreamRemote(t *testing.T) {
	g := &fakeGitOps{
		// upstream/main absent.
	}
	decision, err := detectStaleForkSync(g, "polecat/branch", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Stale {
		t.Errorf("expected Stale=false when upstream remote absent, got true (reason=%q)", decision.Reason)
	}
	for _, c := range g.calls {
		if c != "RefExists(upstream/main)" {
			t.Errorf("unexpected call after ref-missing short-circuit: %s", c)
		}
	}
}

// TestDetectStaleForkSync_RefExistsError: an unexpected git failure probing
// the upstream ref must fail OPEN (Stale=false + error returned) so a transient
// git hiccup never wedges a legitimate fork-sync.
func TestDetectStaleForkSync_RefExistsError(t *testing.T) {
	bang := errors.New("disk I/O error")
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {err: bang},
		},
	}
	decision, err := detectStaleForkSync(g, "polecat/x", "main")
	if !errors.Is(err, bang) {
		t.Fatalf("expected error %v to be returned, got %v", bang, err)
	}
	if decision.Stale {
		t.Errorf("expected Stale=false on git error (fail open), got true")
	}
}

// TestDetectStaleForkSync_IsAncestorError_Target: a git failure on the
// origin/main ancestry probe must also fail open.
func TestDetectStaleForkSync_IsAncestorError_Target(t *testing.T) {
	bang := errors.New("ref missing")
	g := &fakeGitOps{
		refExists: map[string]fakeRefExistsResult{
			"upstream/main": {ok: true},
		},
		isAncestor: map[string]fakeIsAncestorResult{
			"upstream/main->polecat/x": {ok: true},
			"origin/main->polecat/x":   {err: bang},
		},
	}
	decision, err := detectStaleForkSync(g, "polecat/x", "main")
	if !errors.Is(err, bang) {
		t.Fatalf("expected error %v, got %v", bang, err)
	}
	if decision.Stale {
		t.Errorf("expected Stale=false on target IsAncestor error (fail open), got true")
	}
}

// TestDetectStaleForkSync_EmptyInputs / NilOps: defensive guards mirroring
// the preserveForkSyncTopology contract — never touch git, never panic.
func TestDetectStaleForkSync_EmptyInputs(t *testing.T) {
	for _, tc := range []struct {
		name, branch, target string
	}{
		{"empty branch", "", "main"},
		{"empty target", "polecat/x", ""},
		{"both empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeGitOps{}
			decision, err := detectStaleForkSync(g, tc.branch, tc.target)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision.Stale {
				t.Errorf("expected Stale=false on empty inputs, got true")
			}
			if len(g.calls) != 0 {
				t.Errorf("expected no git calls on empty inputs, got %v", g.calls)
			}
		})
	}
}

func TestDetectStaleForkSync_NilOps(t *testing.T) {
	decision, err := detectStaleForkSync(nil, "polecat/x", "main")
	if err == nil {
		t.Fatal("expected error for nil git ops, got nil")
	}
	if decision.Stale {
		t.Error("expected Stale=false for nil git ops")
	}
}

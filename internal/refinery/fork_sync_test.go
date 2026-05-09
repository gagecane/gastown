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
			"upstream/release-1.0->polecat/sync":        {ok: true},
			"upstream/release-1.0->origin/release-1.0":  {ok: false},
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

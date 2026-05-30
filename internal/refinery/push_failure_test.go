package refinery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installRejectAllHook writes a pre-receive hook to the bare repo that rejects
// every push. This simulates the non-fast-forward rejection a sibling MR
// triggers during convoy fan-out (gu-wj3f) without needing a second client to
// race a real ff-push.
//
// The hook is intentionally cross-platform-shell-portable: bare repos on
// Windows runners use msys-style hook execution, so a `#!/bin/sh` script with
// `exit 1` works the same way as on Linux.
func installRejectAllHook(t *testing.T, bareRepoPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pre-receive hook simulation skipped on windows (msys hook quirks)")
	}
	hookPath := filepath.Join(bareRepoPath, "hooks", "pre-receive")
	body := "#!/bin/sh\n" +
		"echo 'gu-wj3f-test: simulated non-ff rejection (sibling landed first)' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(hookPath, []byte(body), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}
}

// TestProcessSingleMR_PushRejected_PreservesBranchAndMR is the regression test
// for gu-wj3f. Before the fix, processSingleMR routed push failures into the
// generic `else` clause that only set result.Error — HandleMRInfoFailure was
// never called for a non-ff rejection, the MR bead was not transitioned, the
// polecat was never nudged, and (worse, in the patrol-shell counterpart) the
// post-merge cleanup ran anyway because the formula chained git push and
// post-merge without checking the exit code.
//
// What this test pins down:
//
//  1. doMerge returns ProcessResult{Success:false, PushFailed:true} when
//     `git push` is rejected — distinct from Conflict / TestsFailed.
//  2. HandleMRInfoSuccess is NOT invoked (post-merge cleanup must not run on a
//     rejected push). We verify this indirectly by checking the engineer
//     output for the success-path "Released merge slot" / "Closed MR bead"
//     markers — those require a successful merge and must be absent here.
//  3. The branch tip is not deleted from origin and the local squash commit
//     was rolled back via ResetHard (origin/main unchanged).
func TestProcessSingleMR_PushRejected_PreservesBranchAndMR(t *testing.T) {
	workDir, g, cleanup := testGitRepo(t)
	defer cleanup()

	// The bare repo created by testGitRepo lives at <tmp>/origin.git.
	bareRepo := filepath.Join(filepath.Dir(workDir), "origin.git")
	installRejectAllHook(t, bareRepo)

	createFeatureBranch(t, workDir, "polecat/chrome/gu-wj3f-test", "wj3f.txt", "convoy fan-out\n")
	preMain := run(t, workDir, "git", "rev-parse", "origin/main")

	e := newTestEngineer(t, workDir, g)
	mr := &MRInfo{
		ID:          "gt-mr-wj3f",
		Branch:      "polecat/chrome/gu-wj3f-test",
		Target:      "main",
		SourceIssue: "gu-wj3f",
		Worker:      "polecats/chrome",
	}

	// Step 1: doMerge directly — this is the layer that classifies push
	// failure. The return value is the contract the patrol depends on.
	result := e.doMerge(context.Background(), mr.Branch, mr.Target, mr.SourceIssue)
	if result.Success {
		t.Fatal("expected push to fail under reject-all hook, got Success=true")
	}
	if !result.PushFailed {
		t.Errorf("expected PushFailed=true, got %+v", result)
	}
	if result.Conflict || result.TestsFailed || result.BranchNotFound {
		t.Errorf("push rejection must not be classified as conflict/tests/branch-missing: %+v", result)
	}

	// Step 2: origin/main must be unchanged — local squash was rolled back.
	postMain := run(t, workDir, "git", "rev-parse", "origin/main")
	if preMain != postMain {
		t.Errorf("origin/main moved despite rejected push: %s -> %s", preMain, postMain)
	}

	// Step 3: source branch still exists locally (no teardown happened).
	branches := run(t, workDir, "git", "branch", "--list", mr.Branch)
	if !strings.Contains(branches, mr.Branch) {
		t.Errorf("expected source branch %q to be preserved, got: %s", mr.Branch, branches)
	}

	// Step 4: drive the higher-level batch path (single-MR shortcut). This
	// is the actual call site whose missing routing was the bug. The buffer
	// captures everything HandleMRInfoSuccess would have logged on a real
	// merge (e.g., "Closed MR bead", "Released merge slot") — none of those
	// strings should appear, and the failure path's "MR preserved" marker
	// should.
	e.output = &bytes.Buffer{}
	br := e.processSingleMR(context.Background(), mr, "main")
	if len(br.Merged) != 0 {
		t.Errorf("expected 0 merged on push rejection, got %d", len(br.Merged))
	}
	// Push failures route through HandleMRInfoFailure (matching the
	// BranchNotFound / NoMerge / NeedsApproval pattern): result.Error stays
	// nil because the failure is surfaced as a polecat nudge / log marker
	// rather than a hard ProcessBatch error. The MR is left in the queue for
	// retry. We pin both the not-merged result and the absence of cleanup
	// markers below.
	out := e.output.(*bytes.Buffer).String()
	for _, marker := range []string{"Closed MR bead", "Closed source issue", "Deleted local branch", "Deleted remote branch"} {
		if strings.Contains(out, marker) {
			t.Errorf("post-merge cleanup ran on rejected push (saw %q): %s", marker, out)
		}
	}
	if !strings.Contains(out, "MR preserved for rebase+retry") {
		t.Errorf("expected MR-preserved log marker on push rejection, got: %s", out)
	}
}

// TestFastForwardBatch_PushRejected_NoPostMergeCleanup is the batch-path
// counterpart to TestProcessSingleMR_PushRejected_PreservesBranchAndMR. It
// verifies that when a multi-MR batch's combined push is rejected (e.g., a
// sibling refinery worker pushed to main between rebase and push), every MR
// in the stack is preserved — none of them get their beads closed or their
// branches deleted. Pre-fix, fastForwardBatch silently fell through to result
// without invoking HandleMRInfoFailure, leaving the queue stuck.
func TestFastForwardBatch_PushRejected_NoPostMergeCleanup(t *testing.T) {
	workDir, g, cleanup := testGitRepo(t)
	defer cleanup()

	bareRepo := filepath.Join(filepath.Dir(workDir), "origin.git")
	installRejectAllHook(t, bareRepo)

	createFeatureBranch(t, workDir, "polecat/chrome/gu-wj3f-a", "a.txt", "a\n")
	createFeatureBranch(t, workDir, "polecat/chrome/gu-wj3f-b", "b.txt", "b\n")
	preMain := run(t, workDir, "git", "rev-parse", "origin/main")

	e := newTestEngineer(t, workDir, g)
	batch := []*MRInfo{
		{ID: "gt-mr-wj3f-a", Branch: "polecat/chrome/gu-wj3f-a", Target: "main", SourceIssue: "gu-wj3f-a", Worker: "polecats/chrome"},
		{ID: "gt-mr-wj3f-b", Branch: "polecat/chrome/gu-wj3f-b", Target: "main", SourceIssue: "gu-wj3f-b", Worker: "polecats/chrome"},
	}

	// Build the stack first so the engineer has the squash commits applied
	// locally — fastForwardBatch only handles the push step.
	stacked, conflicts, err := e.BuildRebaseStack(context.Background(), batch, "main")
	if err != nil {
		t.Fatalf("BuildRebaseStack: %v", err)
	}
	if len(stacked) != 2 || len(conflicts) != 0 {
		t.Fatalf("expected 2 stacked / 0 conflicts, got %d / %d", len(stacked), len(conflicts))
	}

	br := e.fastForwardBatch(context.Background(), stacked, "main", &BatchResult{})
	if br.Error == nil {
		t.Fatal("expected non-nil error from fastForwardBatch on rejected push")
	}
	if len(br.Merged) != 0 {
		t.Errorf("expected 0 merged on push rejection, got %d: %v", len(br.Merged), stackedIDs(br.Merged))
	}

	// origin/main must be unchanged.
	postMain := run(t, workDir, "git", "rev-parse", "origin/main")
	if preMain != postMain {
		t.Errorf("origin/main moved despite rejected push: %s -> %s", preMain, postMain)
	}

	// No success-path side effects.
	out := e.output.(*bytes.Buffer).String()
	for _, marker := range []string{"Closed MR bead", "Deleted local branch", "Deleted remote branch"} {
		if strings.Contains(out, marker) {
			t.Errorf("post-merge cleanup ran for batch on rejected push (saw %q): %s", marker, out)
		}
	}
}

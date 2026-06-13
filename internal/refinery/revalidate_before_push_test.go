package refinery

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// gu-tw7fa: the refinery must re-validate the MR bead immediately before the
// push. A gate can stall for many minutes (stale flock, host-load deferral),
// and during that window an owner can retract the MR (close the bead) or
// re-classify the work human-review-only (merge_strategy=local / no_merge).
// Without the re-read, the refinery pushes a retracted, human-review-only MR
// onto the protected branch — the talon_cdk mis-merge (gc-wisp-ifnp).
//
// These tests inject the re-validation hook so the abort path is exercised
// without a live beads/Dolt server. The downstream git effects (no push to
// origin/main, local squash rolled back) are verified against a real bare repo.

// TestDoMerge_RetractedDuringGate_AbortsPush pins the core contract: when the
// pre-push re-validation reports an abort reason, doMerge must NOT push to
// origin/<target>, must roll back the local squash commit, and must return
// ProcessResult{Retracted:true} — distinct from Conflict / TestsFailed /
// PushFailed.
func TestDoMerge_RetractedDuringGate_AbortsPush(t *testing.T) {
	workDir, g, cleanup := testGitRepo(t)
	defer cleanup()

	createFeatureBranch(t, workDir, "polecat/ghoul/gu-tw7fa-test", "tw7fa.txt", "retracted work\n")
	preMain := run(t, workDir, "git", "rev-parse", "origin/main")

	e := newTestEngineer(t, workDir, g)
	// Simulate the owner retracting the MR while the gate ran: the re-validation
	// returns a non-empty abort reason.
	e.revalidateBeforePush = func(branch, sourceIssue string) string {
		return "MR gt-mr-tw7fa was retracted (bead closed) during the gate — refusing to push"
	}

	result := e.doMerge(context.Background(), "polecat/ghoul/gu-tw7fa-test", "main", "gu-tw7fa")
	if result.Success {
		t.Fatal("expected abort, got Success=true")
	}
	if !result.Retracted {
		t.Errorf("expected Retracted=true, got %+v", result)
	}
	if result.Conflict || result.TestsFailed || result.PushFailed || result.BranchNotFound {
		t.Errorf("retraction must not be classified as conflict/tests/push/branch-missing: %+v", result)
	}

	// origin/main must be unchanged — nothing was pushed.
	postMain := run(t, workDir, "git", "rev-parse", "origin/main")
	if preMain != postMain {
		t.Errorf("origin/main moved despite retracted MR: %s -> %s", preMain, postMain)
	}

	// Local HEAD on the checked-out target must have been reset to origin/main
	// (the squash commit was rolled back).
	localMain := run(t, workDir, "git", "rev-parse", "HEAD")
	if localMain != preMain {
		t.Errorf("local main not reset after abort: HEAD=%s want %s", localMain, preMain)
	}
}

// TestProcessSingleMR_Retracted_NoCleanupNoNudge verifies the higher-level
// dispatch: a retracted MR is dequeued silently. Post-merge cleanup (close
// beads, delete branch) must NOT run, and the worker must NOT be nudged to
// resubmit (there is nothing to fix). The branch is preserved.
func TestProcessSingleMR_Retracted_NoCleanupNoNudge(t *testing.T) {
	workDir, g, cleanup := testGitRepo(t)
	defer cleanup()

	createFeatureBranch(t, workDir, "polecat/ghoul/gu-tw7fa-b", "b.txt", "b\n")
	preMain := run(t, workDir, "git", "rev-parse", "origin/main")

	e := newTestEngineer(t, workDir, g)
	e.revalidateBeforePush = func(branch, sourceIssue string) string {
		return "MR gt-mr-tw7fa-b is merge_strategy=local (human-review-only) — refusing to auto-push"
	}

	mr := &MRInfo{
		ID:          "gt-mr-tw7fa-b",
		Branch:      "polecat/ghoul/gu-tw7fa-b",
		Target:      "main",
		SourceIssue: "gu-tw7fa-b",
		Worker:      "polecats/ghoul",
	}

	e.output = &bytes.Buffer{}
	br := e.processSingleMR(context.Background(), mr, "main")
	if len(br.Merged) != 0 {
		t.Errorf("expected 0 merged on retracted MR, got %d", len(br.Merged))
	}
	if br.Error != nil {
		t.Errorf("retraction must not surface as a hard batch error, got: %v", br.Error)
	}

	postMain := run(t, workDir, "git", "rev-parse", "origin/main")
	if preMain != postMain {
		t.Errorf("origin/main moved despite retracted MR: %s -> %s", preMain, postMain)
	}

	branches := run(t, workDir, "git", "branch", "--list", mr.Branch)
	if !strings.Contains(branches, mr.Branch) {
		t.Errorf("expected source branch %q preserved, got: %s", mr.Branch, branches)
	}

	out := e.output.(*bytes.Buffer).String()
	for _, marker := range []string{"Closed MR bead", "Closed source issue", "Deleted local branch", "Deleted remote branch", "MERGE_FAILED"} {
		if strings.Contains(out, marker) {
			t.Errorf("retracted MR triggered %q (must dequeue silently): %s", marker, out)
		}
	}
	if !strings.Contains(out, "retracted/human-review-only") {
		t.Errorf("expected retracted-dequeue log marker, got: %s", out)
	}
}

// TestDoMerge_RevalidationPasses_PushProceeds is the negative control: when the
// re-validation hook returns empty (MR still valid), the merge proceeds and
// lands on origin/main as normal. This guards against the guard being too
// aggressive (false-positive aborts wedge the queue).
func TestDoMerge_RevalidationPasses_PushProceeds(t *testing.T) {
	workDir, g, cleanup := testGitRepo(t)
	defer cleanup()

	createFeatureBranch(t, workDir, "polecat/ghoul/gu-tw7fa-ok", "ok.txt", "valid work\n")
	preMain := run(t, workDir, "git", "rev-parse", "origin/main")

	e := newTestEngineer(t, workDir, g)
	revalidated := false
	e.revalidateBeforePush = func(branch, sourceIssue string) string {
		revalidated = true
		return "" // still valid — proceed
	}

	result := e.doMerge(context.Background(), "polecat/ghoul/gu-tw7fa-ok", "main", "gu-tw7fa-ok")
	if !result.Success {
		t.Fatalf("expected merge to succeed, got %+v", result)
	}
	if result.Retracted {
		t.Error("Retracted must be false when re-validation passes")
	}
	if !revalidated {
		t.Error("expected re-validation hook to be invoked before push")
	}

	postMain := run(t, workDir, "git", "rev-parse", "origin/main")
	if preMain == postMain {
		t.Error("origin/main did not advance despite successful merge")
	}
}

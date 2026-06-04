package cmd

// Tests for gs-pd6 phase 1: pushBranchWithFallbacks extracts the runDone push
// ladder (primary push → bare-repo / mayor-clone fallback → SHA-refspec /
// origin-tip recovery → gu-hz3vx / gs-y7g non-fast-forward recovery). The
// individual recovery helpers have isolated tests in done_nonff_recover_test.go;
// these exercise the ORCHESTRATION — that the function routes a clean push to
// success, drives a recoverable non-fast-forward through the recovery branch,
// and returns a non-nil error (without clobbering origin) when no recovery
// applies. The git harness helpers (runGitEnv, setupDivergedOwnBranch,
// attemptPush, trimTrailingWS) live in done_nonff_recover_test.go.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// setupCleanFeatureBranch builds a bare remote + worktree where `main` is pushed
// and a feature branch carries one un-pushed commit. The branch:branch push is a
// clean create (fast-forward), so pushBranchWithFallbacks should succeed on the
// first attempt. Returns (worktreePath, headSHA, branch).
func setupCleanFeatureBranch(t *testing.T, branch string) (string, string, string) {
	t.Helper()
	bare := t.TempDir()
	runGitEnv(t, bare, "init", "--bare", "-b", "main")

	wt := t.TempDir()
	runGitEnv(t, wt, "init", "-b", "main")
	runGitEnv(t, wt, "config", "user.email", "test@example.com")
	runGitEnv(t, wt, "config", "user.name", "Test")
	runGitEnv(t, wt, "remote", "add", "origin", bare)

	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "README.md")
	runGitEnv(t, wt, "commit", "-m", "seed")
	runGitEnv(t, wt, "push", "origin", "main")

	runGitEnv(t, wt, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("the work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "feature.txt")
	runGitEnv(t, wt, "commit", "-m", "feat: the work")
	head := runGitEnv(t, wt, "rev-parse", "HEAD")
	return wt, head, branch
}

// emptyTownRoot returns a temp town root with no .repo.git or mayor/rig under
// the given rig — so the bare-repo and mayor-clone fallbacks are inert and the
// recovery ladder relies on the worktree's own origin.
func emptyTownRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// TestPushBranchWithFallbacks_HappyPathPushesAndReturnsSHA verifies the common
// case: a clean feature branch is pushed on the first attempt, the function
// returns the HEAD SHA with no error, and origin/<branch> now matches HEAD.
func TestPushBranchWithFallbacks_HappyPathPushesAndReturnsSHA(t *testing.T) {
	wt, head, branch := setupCleanFeatureBranch(t, "polecat/test/clean--abc")
	g := git.NewGit(wt)

	sha, err := pushBranchWithFallbacks(g, emptyTownRoot(t), "rig", branch, "main")
	if err != nil {
		t.Fatalf("happy-path push returned error: %v", err)
	}
	if sha != head {
		t.Fatalf("returned SHA = %s, want HEAD %s", sha, head)
	}
	tip, tipErr := g.RemoteBranchTip("origin", branch)
	if tipErr != nil {
		t.Fatalf("RemoteBranchTip: %v", tipErr)
	}
	if trimTrailingWS(tip) != head {
		t.Fatalf("origin tip = %s, want pushed HEAD %s", tip, head)
	}
}

// TestPushBranchWithFallbacks_NonFFSameTreeRecovers verifies the orchestration
// reaches the gu-hz3vx recovery branch: a non-fast-forward rejection where local
// HEAD has the same tree as origin's tip is recovered (origin force-updated to
// local HEAD) and the function returns no error.
func TestPushBranchWithFallbacks_NonFFSameTreeRecovers(t *testing.T) {
	wt, localHEAD, originTip, branch := setupDivergedOwnBranch(t, "polecat/test/ladder-same--abc", true)
	if localHEAD == originTip {
		t.Fatalf("test setup broken: local HEAD must differ from origin tip")
	}
	g := git.NewGit(wt)

	sha, err := pushBranchWithFallbacks(g, emptyTownRoot(t), "rig", branch, "main")
	if err != nil {
		t.Fatalf("expected non-FF same-tree to recover, got error: %v", err)
	}
	if sha != localHEAD {
		t.Fatalf("returned SHA = %s, want local HEAD %s", sha, localHEAD)
	}
	tip, tipErr := g.RemoteBranchTip("origin", branch)
	if tipErr != nil {
		t.Fatalf("RemoteBranchTip: %v", tipErr)
	}
	if trimTrailingWS(tip) != localHEAD {
		t.Fatalf("origin tip = %s, want force-updated to %s", tip, localHEAD)
	}
}

// TestPushBranchWithFallbacks_UnrecoverableReturnsError verifies the failure
// route: a non-fast-forward rejection where local HEAD bundles unrelated content
// (different tree, divergent patch-id) exhausts every recovery path. The function
// must return a non-nil error and leave origin's tip UNCHANGED so the caller
// strands the work loudly rather than clobbering the already-pushed commit.
func TestPushBranchWithFallbacks_UnrecoverableReturnsError(t *testing.T) {
	wt, _, originTip, branch := setupDivergedOwnBranch(t, "polecat/test/ladder-diff--abc", false)
	g := git.NewGit(wt)

	_, err := pushBranchWithFallbacks(g, emptyTownRoot(t), "rig", branch, "main")
	if err == nil {
		t.Fatalf("expected unrecoverable non-FF to return an error, got nil")
	}
	tip, tipErr := g.RemoteBranchTip("origin", branch)
	if tipErr != nil {
		t.Fatalf("RemoteBranchTip: %v", tipErr)
	}
	if trimTrailingWS(tip) != originTip {
		t.Fatalf("origin tip = %s, want unchanged %s (no clobber on unrecoverable failure)", tip, originTip)
	}
}

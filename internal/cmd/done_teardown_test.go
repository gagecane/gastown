package cmd

// Phase-isolated tests for teardownAfterDone (gs-a5v, gs-pd6 phase 3).
//
// teardownAfterDone is the completion tail of runDone — the code reached via
// the notifyWitness label and every `goto notifyWitness` site. The extraction
// is behavior-preserving; these tests lock in the one consequential, git-only
// side effect that body owns: when the role is a polecat and the worktree is
// clean, it syncs the worktree to the default branch and deletes the old
// feature branch (the persistent-polecat DONE→IDLE transition). The
// surrounding bead/nudge calls are best-effort and are neutralized here
// (empty agent identity → early returns; GT_TEST_NUDGE_LOG → file-logged
// witness nudge; config disables self-terminate).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// setupTeardownWorktree builds a bare origin + a worktree clone on a polecat
// feature branch (one commit ahead of main, already pushed). Returns the
// worktree path and the feature branch name.
func setupTeardownWorktree(t *testing.T, branch string) string {
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

	// Feature branch with work, pushed and currently checked out — the state a
	// polecat is in when it calls gt done.
	runGitEnv(t, wt, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("the work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "feature.txt")
	runGitEnv(t, wt, "commit", "-m", "feat: the work")
	runGitEnv(t, wt, "push", "origin", branch)

	return wt
}

// writeNoSelfTerminateConfig writes a town settings file that disables the
// polecat self-terminate path so teardownAfterDone does not spawn a detached
// tmux kill during the test.
func writeNoSelfTerminateConfig(t *testing.T, townRoot string) {
	t.Helper()
	settingsDir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"operational":{"daemon":{"polecat_self_terminate":false}}}`
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTeardownAfterDone_SyncsWorktreeAndDeletesBranch verifies the DONE→IDLE
// worktree sync: a clean polecat completion checks out the default branch and
// deletes the old feature branch. This is the highest-value behavior the
// notifyWitness tail owns, and the one most sensitive to the goto→return
// state-threading the extraction performed.
func TestTeardownAfterDone_SyncsWorktreeAndDeletesBranch(t *testing.T) {
	branch := "polecat/test/teardown--abc123"
	wt := setupTeardownWorktree(t, branch)
	townRoot := t.TempDir()
	writeNoSelfTerminateConfig(t, townRoot)

	// Role = polecat so the sync block runs; no rig/polecat identity so the
	// agent-bead lookups early-return without touching Dolt.
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))

	g := git.NewGit(wt)

	teardownAfterDone(teardownParams{
		g:             g,
		cwd:           wt,
		townRoot:      townRoot,
		rigName:       "testrig",
		sender:        "testrig/polecat",
		polecatName:   "test",
		branch:        branch,
		defaultBranch: "main",
		issueID:       "",
		agentBeadID:   "",
		exitType:      ExitCompleted,
		mrID:          "",
		mrFailed:      false,
		pushFailed:    false,
		cwdAvailable:  true,
	})

	// Contract 1: worktree synced to the default branch.
	if cur, err := g.CurrentBranch(); err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	} else if cur != "main" {
		t.Fatalf("expected worktree on main after sync, got %q", cur)
	}

	// Contract 2: the old feature branch was deleted.
	if exists, err := g.BranchExists(branch); err != nil {
		t.Fatalf("BranchExists: %v", err)
	} else if exists {
		t.Fatalf("expected feature branch %q to be deleted after sync", branch)
	}
}

// TestTeardownAfterDone_PreservesBranchOnPushFailure verifies the recovery
// guard: when push or MR failed, teardownAfterDone must NOT sync the worktree
// or delete the branch, so the unrecovered work stays on the feature branch.
func TestTeardownAfterDone_PreservesBranchOnPushFailure(t *testing.T) {
	branch := "polecat/test/teardown--def456"
	wt := setupTeardownWorktree(t, branch)
	townRoot := t.TempDir()
	writeNoSelfTerminateConfig(t, townRoot)

	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))

	g := git.NewGit(wt)

	teardownAfterDone(teardownParams{
		g:             g,
		cwd:           wt,
		townRoot:      townRoot,
		rigName:       "testrig",
		sender:        "testrig/polecat",
		polecatName:   "test",
		branch:        branch,
		defaultBranch: "main",
		issueID:       "",
		agentBeadID:   "",
		exitType:      ExitCompleted,
		mrID:          "",
		mrFailed:      false,
		pushFailed:    true, // push failed → work must be preserved
		cwdAvailable:  true,
	})

	// The worktree must stay on the feature branch with the branch intact.
	if cur, err := g.CurrentBranch(); err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	} else if cur != branch {
		t.Fatalf("expected worktree to stay on %q after push failure, got %q", branch, cur)
	}
	if exists, err := g.BranchExists(branch); err != nil {
		t.Fatalf("BranchExists: %v", err)
	} else if !exists {
		t.Fatalf("expected feature branch %q to be preserved after push failure", branch)
	}
}

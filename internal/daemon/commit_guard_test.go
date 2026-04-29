package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun is a test helper that runs git with deterministic author env.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s) failed: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupOriginAndWorktree creates a bare "origin" repo with one commit on main,
// then clones it as a worktree. Returns (bareOrigin, worktreeDir).
func setupOriginAndWorktree(t *testing.T) (string, string) {
	t.Helper()
	tmpDir := t.TempDir()

	bare := filepath.Join(tmpDir, "origin.git")
	gitRun(t, "", "init", "--bare", "-b", "main", bare)

	// Seed the bare repo with one commit on main.
	seed := filepath.Join(tmpDir, "seed")
	gitRun(t, "", "init", "-b", "main", seed)
	gitRun(t, seed, "commit", "--allow-empty", "-m", "initial")
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "origin", "main")

	// Clone as the working worktree.
	worktree := filepath.Join(tmpDir, "worktree")
	gitRun(t, "", "clone", bare, worktree)

	return bare, worktree
}

// TestGuardDaemonCommit_RefusesOnPushedBranch verifies the guard refuses when
// the worktree is on a branch that exists at origin. This is the primary
// defensive case from the bead.
func TestGuardDaemonCommit_RefusesOnPushedBranch(t *testing.T) {
	_, worktree := setupOriginAndWorktree(t)

	// Worktree is on main (cloned from origin/main) — main exists at origin.
	err := guardDaemonCommit(worktree)
	if err == nil {
		t.Fatal("expected guardDaemonCommit to refuse on branch pushed to origin, got nil")
	}
	if !strings.Contains(err.Error(), "exists at origin") {
		t.Errorf("expected error mentioning 'exists at origin', got: %v", err)
	}
}

// TestGuardDaemonCommit_AllowsLocalOnlyBranch verifies the guard permits
// commits on a branch that exists only locally — the common polecat
// pre-`gt done` case.
func TestGuardDaemonCommit_AllowsLocalOnlyBranch(t *testing.T) {
	_, worktree := setupOriginAndWorktree(t)

	// Create a local-only branch.
	gitRun(t, worktree, "checkout", "-b", "polecat/rust/gu-local")

	if err := guardDaemonCommit(worktree); err != nil {
		t.Errorf("expected guardDaemonCommit to permit local-only branch, got: %v", err)
	}
}

// TestGuardDaemonCommit_AllowsDetachedHEAD verifies the guard permits
// commits when HEAD is detached (no named branch to corrupt).
func TestGuardDaemonCommit_AllowsDetachedHEAD(t *testing.T) {
	_, worktree := setupOriginAndWorktree(t)

	// Detach HEAD at the current commit.
	gitRun(t, worktree, "checkout", "--detach", "HEAD")

	if err := guardDaemonCommit(worktree); err != nil {
		t.Errorf("expected guardDaemonCommit to permit detached HEAD, got: %v", err)
	}
}

// TestGuardDaemonCommit_FailsClosedWithoutOrigin verifies the guard refuses
// when there's no origin remote — we can't verify the branch is safe, so we
// fail closed. A daemon silently committing because we couldn't check is
// worse than a daemon refusing to commit.
func TestGuardDaemonCommit_FailsClosedWithoutOrigin(t *testing.T) {
	tmpDir := t.TempDir()
	repo := filepath.Join(tmpDir, "repo")
	gitRun(t, "", "init", "-b", "main", repo)
	gitRun(t, repo, "commit", "--allow-empty", "-m", "initial")

	// No origin remote — branchExistsAtOrigin must fail, guard must refuse.
	err := guardDaemonCommit(repo)
	if err == nil {
		t.Fatal("expected guardDaemonCommit to fail closed when origin is missing, got nil")
	}
	if !strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("expected error mentioning 'cannot verify', got: %v", err)
	}
}

// TestBranchExistsAtOrigin_Present verifies the ls-remote path for an
// existing remote ref.
func TestBranchExistsAtOrigin_Present(t *testing.T) {
	_, worktree := setupOriginAndWorktree(t)

	exists, err := branchExistsAtOrigin(worktree, "main")
	if err != nil {
		t.Fatalf("branchExistsAtOrigin returned error: %v", err)
	}
	if !exists {
		t.Error("expected main to exist at origin")
	}
}

// TestBranchExistsAtOrigin_Absent verifies the ls-remote path for a ref
// that isn't on the remote (git ls-remote --exit-code returns 2).
func TestBranchExistsAtOrigin_Absent(t *testing.T) {
	_, worktree := setupOriginAndWorktree(t)

	exists, err := branchExistsAtOrigin(worktree, "nonexistent-branch")
	if err != nil {
		t.Fatalf("branchExistsAtOrigin returned error: %v", err)
	}
	if exists {
		t.Error("expected nonexistent-branch NOT to exist at origin")
	}
}

// TestBranchExistsAtOrigin_EmptyBranch verifies that the empty-branch shortcut
// returns (false, nil) without invoking git.
func TestBranchExistsAtOrigin_EmptyBranch(t *testing.T) {
	_, worktree := setupOriginAndWorktree(t)

	exists, err := branchExistsAtOrigin(worktree, "")
	if err != nil {
		t.Errorf("expected empty branch to return nil error, got: %v", err)
	}
	if exists {
		t.Error("expected empty branch to return false")
	}
}

// TestCurrentBranch_OnMain verifies the branch helper returns the named branch.
func TestCurrentBranch_OnMain(t *testing.T) {
	_, worktree := setupOriginAndWorktree(t)

	branch, err := currentBranch(worktree)
	if err != nil {
		t.Fatalf("currentBranch returned error: %v", err)
	}
	if branch != "main" {
		t.Errorf("expected 'main', got %q", branch)
	}
}

// TestCurrentBranch_Detached verifies the branch helper returns "" for
// detached HEAD.
func TestCurrentBranch_Detached(t *testing.T) {
	_, worktree := setupOriginAndWorktree(t)
	gitRun(t, worktree, "checkout", "--detach", "HEAD")

	branch, err := currentBranch(worktree)
	if err != nil {
		t.Fatalf("currentBranch returned error: %v", err)
	}
	if branch != "" {
		t.Errorf("expected empty branch for detached HEAD, got %q", branch)
	}
}

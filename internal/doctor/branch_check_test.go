package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWorktreeConflict(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantPath string
	}{
		{
			name:     "older git: already checked out at",
			output:   "fatal: 'main' is already checked out at '/home/user/gt/rig/.repo.git'",
			wantPath: "/home/user/gt/rig/.repo.git",
		},
		{
			name:     "newer git: already used by worktree at",
			output:   "fatal: 'main' is already used by worktree at '/home/user/gt/rig/.repo.git'",
			wantPath: "/home/user/gt/rig/.repo.git",
		},
		{
			name:     "with trailing newline",
			output:   "fatal: 'main' is already checked out at '/tmp/bare.git'\n",
			wantPath: "/tmp/bare.git",
		},
		{
			name:     "different branch name",
			output:   "fatal: 'develop' is already checked out at '/some/path/worktree'",
			wantPath: "/some/path/worktree",
		},
		{
			name:     "not a worktree conflict",
			output:   "error: pathspec 'main' did not match any file(s) known to git",
			wantPath: "",
		},
		{
			name:     "empty output",
			output:   "",
			wantPath: "",
		},
		{
			name:     "partial match no closing quote",
			output:   "fatal: 'main' is already checked out at '/broken",
			wantPath: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWorktreeConflict(tt.output)
			if got != tt.wantPath {
				t.Errorf("parseWorktreeConflict(%q) = %q, want %q", tt.output, got, tt.wantPath)
			}
		})
	}
}

// initBareWithCommit creates a bare repo with an initial commit on the main branch.
func initBareWithCommit(t *testing.T, bareRepo string) {
	t.Helper()

	// Create a temporary regular repo, commit, then push to the bare repo
	tmpInit := bareRepo + "-init"
	runGit(t, "", "init", "-b", "main", tmpInit)
	runGit(t, tmpInit, "commit", "--allow-empty", "-m", "initial commit")
	runGit(t, tmpInit, "remote", "add", "bare", bareRepo)
	runGit(t, tmpInit, "push", "bare", "main")
	os.RemoveAll(tmpInit)
}

// TestCheckoutWithWorktreeRetry_BareRepoConflict sets up a real bare repo with
// a worktree and verifies the retry path works. Note: whether git actually
// blocks the checkout depends on git version (some versions allow checkout of
// a branch that's only referenced by a bare repo's HEAD). This test verifies
// the checkout succeeds regardless.
func TestCheckoutWithWorktreeRetry_BareRepoConflict(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a bare repo (simulating .repo.git)
	bareRepo := filepath.Join(tmpDir, "rig", ".repo.git")
	runGit(t, "", "init", "--bare", "-b", "main", bareRepo)
	initBareWithCommit(t, bareRepo)

	// Bare repo now has main branch. Ensure HEAD points to it.
	runGit(t, bareRepo, "symbolic-ref", "HEAD", "refs/heads/main")

	// Create a worktree (simulating refinery/rig) on a different branch
	worktreeDir := filepath.Join(tmpDir, "rig", "refinery", "rig")
	runGit(t, bareRepo, "worktree", "add", "-b", "integration/test", worktreeDir)

	// Attempt to switch the worktree to main. Whether this triggers the
	// retry path depends on git version, but either way it must succeed.
	check := NewBranchCheck()
	err := check.checkoutWithWorktreeRetry(worktreeDir, "main")
	if err != nil {
		t.Fatalf("checkoutWithWorktreeRetry should succeed, got: %v", err)
	}

	// Verify the worktree is now on main
	branch := getCurrentBranchHelper(t, worktreeDir)
	if branch != "main" {
		t.Errorf("expected worktree to be on 'main', got %q", branch)
	}
}

// TestCheckoutWithWorktreeRetry_NonBareRepoConflict verifies that conflicts
// with non-bare repos produce a clear error instead of silently failing.
func TestCheckoutWithWorktreeRetry_NonBareRepoConflict(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a regular (non-bare) repo with main branch
	mainRepo := filepath.Join(tmpDir, "main-clone")
	runGit(t, "", "init", "-b", "main", mainRepo)
	runGit(t, mainRepo, "commit", "--allow-empty", "-m", "initial")

	// Create a worktree from the regular repo
	worktreeDir := filepath.Join(tmpDir, "worktree")
	runGit(t, mainRepo, "worktree", "add", "-b", "feature", worktreeDir)

	check := NewBranchCheck()
	err := check.checkoutWithWorktreeRetry(worktreeDir, "main")
	if err == nil {
		t.Fatal("expected error for non-bare repo conflict, got nil")
	}

	if !strings.Contains(err.Error(), "not a bare repo") {
		t.Errorf("expected error to mention 'not a bare repo', got: %v", err)
	}
}

// TestCheckoutWithWorktreeRetry_NormalCheckout verifies normal checkout
// (no worktree conflict) still works.
func TestCheckoutWithWorktreeRetry_NormalCheckout(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a regular repo with two branches
	repo := filepath.Join(tmpDir, "repo")
	runGit(t, "", "init", "-b", "main", repo)
	runGit(t, repo, "commit", "--allow-empty", "-m", "initial")
	runGit(t, repo, "checkout", "-b", "feature")

	check := NewBranchCheck()
	err := check.checkoutWithWorktreeRetry(repo, "main")
	if err != nil {
		t.Fatalf("expected normal checkout to succeed, got: %v", err)
	}

	branch := getCurrentBranchHelper(t, repo)
	if branch != "main" {
		t.Errorf("expected repo to be on 'main', got %q", branch)
	}
}

// TestCheckoutWithWorktreeRetry_BranchNotFound verifies clear error for missing branch.
func TestCheckoutWithWorktreeRetry_BranchNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	repo := filepath.Join(tmpDir, "repo")
	runGit(t, "", "init", "-b", "main", repo)
	runGit(t, repo, "commit", "--allow-empty", "-m", "initial")

	check := NewBranchCheck()
	err := check.checkoutWithWorktreeRetry(repo, "nonexistent-branch")
	if err == nil {
		t.Fatal("expected error for nonexistent branch, got nil")
	}

	if !strings.Contains(err.Error(), "git checkout nonexistent-branch failed") {
		t.Errorf("expected error about failed checkout, got: %v", err)
	}
}

// runGit is a test helper that runs git commands and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
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

// getCurrentBranchHelper returns the current branch for a directory.
func getCurrentBranchHelper(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to get branch for %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// setupRigWithDefaultBranch creates a town/rig/refinery/rig worktree layout on
// a given default branch. If configDefault is non-empty, it's written into the
// rig's config.json as default_branch. The bare repo's origin/HEAD is pointed
// at defaultBranch so that git-side detection also returns defaultBranch.
//
// Returns the townRoot and the path to the refinery worktree.
func setupRigWithDefaultBranch(t *testing.T, defaultBranch, configDefault string) (string, string) {
	t.Helper()
	townRoot := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write config.json. If configDefault is empty, omit default_branch so the
	// check falls through to git-based detection.
	configBytes := []byte(`{"name":"testrig"}`)
	if configDefault != "" {
		configBytes = []byte(`{"name":"testrig","default_branch":"` + configDefault + `"}`)
	}
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), configBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a bare repo acting as origin and seed it with one commit on defaultBranch.
	bareRepo := filepath.Join(rigDir, ".repo.git")
	runGit(t, "", "init", "--bare", "-b", defaultBranch, bareRepo)
	tmpInit := bareRepo + "-init"
	runGit(t, "", "init", "-b", defaultBranch, tmpInit)
	runGit(t, tmpInit, "commit", "--allow-empty", "-m", "initial commit")
	runGit(t, tmpInit, "remote", "add", "bare", bareRepo)
	runGit(t, tmpInit, "push", "bare", defaultBranch)
	os.RemoveAll(tmpInit)

	// Bare repo's HEAD points at defaultBranch.
	runGit(t, bareRepo, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)

	// Create the refinery/rig worktree tracking origin/defaultBranch so that
	// origin/HEAD is populated correctly for git-side detection.
	refineryRig := filepath.Join(rigDir, "refinery", "rig")
	if err := os.MkdirAll(filepath.Dir(refineryRig), 0755); err != nil {
		t.Fatal(err)
	}
	// Clone locally so origin/HEAD is set to defaultBranch automatically.
	runGit(t, "", "clone", "--branch", defaultBranch, bareRepo, refineryRig)

	return townRoot, refineryRig
}

// TestIsExpectedBranch_RigWithMainlineConfig verifies that a rig whose
// config.json sets default_branch=mainline is happy on "mainline". This is
// the primary fix case from the bug report.
func TestIsExpectedBranch_RigWithMainlineConfig(t *testing.T) {
	townRoot, refineryRig := setupRigWithDefaultBranch(t, "mainline", "mainline")

	check := NewBranchCheck()
	if !check.isExpectedBranch(townRoot, refineryRig, "mainline") {
		t.Error("rig with default_branch=mainline on mainline should be expected")
	}
	// Regression: a worker on "main" inside a mainline-default rig is drift
	// (the old code masked this with the hardcoded main/master short-circuit).
	if check.isExpectedBranch(townRoot, refineryRig, "main") {
		t.Error("rig with default_branch=mainline on main should NOT be expected (that's drift)")
	}
}

// TestIsExpectedBranch_GitFallback_Mainline verifies that when config.json
// lacks default_branch, the check falls back to git's origin/HEAD and accepts
// "mainline" as expected.
func TestIsExpectedBranch_GitFallback_Mainline(t *testing.T) {
	townRoot, refineryRig := setupRigWithDefaultBranch(t, "mainline", "")

	check := NewBranchCheck()
	if !check.isExpectedBranch(townRoot, refineryRig, "mainline") {
		t.Error("rig with no config default_branch but origin/HEAD=mainline should accept mainline")
	}
}

// TestIsExpectedBranch_GitFallback_MainRejectsMainline verifies that when the
// remote default is main (via origin/HEAD) and config.json is unset, a worktree
// on "mainline" is flagged as drift.
func TestIsExpectedBranch_GitFallback_MainRejectsMainline(t *testing.T) {
	townRoot, refineryRig := setupRigWithDefaultBranch(t, "main", "")

	check := NewBranchCheck()
	if check.isExpectedBranch(townRoot, refineryRig, "mainline") {
		t.Error("rig with origin/HEAD=main should NOT accept mainline")
	}
	if !check.isExpectedBranch(townRoot, refineryRig, "main") {
		t.Error("rig with origin/HEAD=main should accept main")
	}
}

// TestIsExpectedBranch_DetachedAtExpected verifies that detached HEAD at
// origin/<expected> is still considered expected.
func TestIsExpectedBranch_DetachedAtExpected(t *testing.T) {
	townRoot, refineryRig := setupRigWithDefaultBranch(t, "mainline", "mainline")

	// Detach HEAD at origin/mainline.
	runGit(t, refineryRig, "checkout", "--detach", "origin/mainline")

	check := NewBranchCheck()
	// Empty branch string signals detached HEAD to isExpectedBranch.
	if !check.isExpectedBranch(townRoot, refineryRig, "") {
		t.Error("detached HEAD at origin/mainline should be expected")
	}
}

// TestDetectDefaultBranchFromGit verifies the git detection helper resolves
// origin/HEAD to the remote default branch name.
func TestDetectDefaultBranchFromGit(t *testing.T) {
	_, refineryRig := setupRigWithDefaultBranch(t, "mainline", "")

	got := detectDefaultBranchFromGit(refineryRig)
	if got != "mainline" {
		t.Errorf("detectDefaultBranchFromGit = %q, want %q", got, "mainline")
	}
}

// TestExpectedBranch_ConfigBeatsGit verifies config.json takes precedence over
// git detection — even if origin/HEAD differs from config, config wins.
func TestExpectedBranch_ConfigBeatsGit(t *testing.T) {
	// Remote default is main, but config.json declares develop.
	townRoot, refineryRig := setupRigWithDefaultBranch(t, "main", "develop")

	check := NewBranchCheck()
	got := check.expectedBranch(townRoot, refineryRig)
	if got != "develop" {
		t.Errorf("expectedBranch = %q, want %q (config should beat git)", got, "develop")
	}
}

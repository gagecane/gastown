package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindOrphanPolecatBranches verifies that polecat worktrees with unmerged
// branches are detected and reported (GH #1024).
func TestFindOrphanPolecatBranches(t *testing.T) {
	// Create a fake rig with a polecat worktree that has unmerged commits.
	rigDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(rigDir, "polecats")

	// Create a bare "origin" repo to serve as a remote
	originDir := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(originDir, 0755); err != nil {
		t.Fatal(err)
	}
	run(t, originDir, "git", "init", "--bare")

	// Create the polecat worktree with an initial commit on main (legacy flat layout)
	polecatName := "alpha"
	worktreePath := filepath.Join(polecatsDir, polecatName)
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatal(err)
	}

	run(t, worktreePath, "git", "init")
	run(t, worktreePath, "git", "remote", "add", "origin", originDir)

	// Create initial commit on main
	writeFile(t, filepath.Join(worktreePath, "README.md"), "# test\n")
	run(t, worktreePath, "git", "add", ".")
	run(t, worktreePath, "git", "commit", "-m", "initial commit")
	run(t, worktreePath, "git", "branch", "-M", "main")
	run(t, worktreePath, "git", "push", "-u", "origin", "main")

	// Create a polecat branch with an extra commit
	run(t, worktreePath, "git", "checkout", "-b", "polecat/alpha-work")
	writeFile(t, filepath.Join(worktreePath, "feature.go"), "package feature\n")
	run(t, worktreePath, "git", "add", ".")
	run(t, worktreePath, "git", "commit", "-m", "feat: add feature")

	// Scan for orphan branches
	branches, skipped, err := findOrphanPolecatBranches(rigDir, rigName, "main")
	if err != nil {
		t.Fatalf("findOrphanPolecatBranches: %v", err)
	}
	if len(skipped) > 0 {
		t.Errorf("unexpected skipped polecats: %v", skipped)
	}

	if len(branches) != 1 {
		t.Fatalf("expected 1 orphan branch, got %d", len(branches))
	}

	b := branches[0]
	if b.Polecat != polecatName {
		t.Errorf("polecat = %q, want %q", b.Polecat, polecatName)
	}
	if b.Branch != "polecat/alpha-work" {
		t.Errorf("branch = %q, want %q", b.Branch, "polecat/alpha-work")
	}
	if b.AheadCount != 1 {
		t.Errorf("ahead count = %d, want 1", b.AheadCount)
	}
	if b.LatestSubject != "feat: add feature" {
		t.Errorf("latest subject = %q, want %q", b.LatestSubject, "feat: add feature")
	}
	if b.HasUncommitted {
		t.Error("expected no uncommitted changes")
	}
	if b.WorktreePath != worktreePath {
		t.Errorf("worktree path = %q, want %q", b.WorktreePath, worktreePath)
	}
}

// TestFindOrphanPolecatBranches_NewStructure verifies that the new-structure
// layout (polecats/<name>/<rigname>/) is correctly detected.
func TestFindOrphanPolecatBranches_NewStructure(t *testing.T) {
	rigDir := t.TempDir()
	rigName := "myrig"
	polecatsDir := filepath.Join(rigDir, "polecats")

	// Create a bare "origin" repo
	originDir := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(originDir, 0755); err != nil {
		t.Fatal(err)
	}
	run(t, originDir, "git", "init", "--bare")

	// New structure: polecats/<name>/<rigname>/
	polecatName := "charlie"
	worktreePath := filepath.Join(polecatsDir, polecatName, rigName)
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatal(err)
	}

	run(t, worktreePath, "git", "init")
	run(t, worktreePath, "git", "remote", "add", "origin", originDir)

	writeFile(t, filepath.Join(worktreePath, "README.md"), "# test\n")
	run(t, worktreePath, "git", "add", ".")
	run(t, worktreePath, "git", "commit", "-m", "initial commit")
	run(t, worktreePath, "git", "branch", "-M", "main")
	run(t, worktreePath, "git", "push", "-u", "origin", "main")

	run(t, worktreePath, "git", "checkout", "-b", "polecat/charlie-work")
	writeFile(t, filepath.Join(worktreePath, "new.go"), "package new\n")
	run(t, worktreePath, "git", "add", ".")
	run(t, worktreePath, "git", "commit", "-m", "feat: new structure work")

	branches, skipped, err := findOrphanPolecatBranches(rigDir, rigName, "main")
	if err != nil {
		t.Fatalf("findOrphanPolecatBranches: %v", err)
	}
	if len(skipped) > 0 {
		t.Errorf("unexpected skipped polecats: %v", skipped)
	}

	if len(branches) != 1 {
		t.Fatalf("expected 1 orphan branch, got %d", len(branches))
	}

	b := branches[0]
	if b.Polecat != polecatName {
		t.Errorf("polecat = %q, want %q", b.Polecat, polecatName)
	}
	if b.WorktreePath != worktreePath {
		t.Errorf("worktree path = %q, want %q", b.WorktreePath, worktreePath)
	}
	if b.AheadCount != 1 {
		t.Errorf("ahead count = %d, want 1", b.AheadCount)
	}
}

// TestFindOrphanPolecatBranches_CustomDefaultBranch verifies that a non-main
// default branch is respected.
func TestFindOrphanPolecatBranches_CustomDefaultBranch(t *testing.T) {
	rigDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(rigDir, "polecats")

	originDir := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(originDir, 0755); err != nil {
		t.Fatal(err)
	}
	run(t, originDir, "git", "init", "--bare")

	polecatName := "delta"
	worktreePath := filepath.Join(polecatsDir, polecatName)
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatal(err)
	}

	run(t, worktreePath, "git", "init")
	run(t, worktreePath, "git", "remote", "add", "origin", originDir)
	writeFile(t, filepath.Join(worktreePath, "README.md"), "# test\n")
	run(t, worktreePath, "git", "add", ".")
	run(t, worktreePath, "git", "commit", "-m", "initial commit")
	// Use "develop" as the default branch
	run(t, worktreePath, "git", "branch", "-M", "develop")
	run(t, worktreePath, "git", "push", "-u", "origin", "develop")

	run(t, worktreePath, "git", "checkout", "-b", "feature/delta-work")
	writeFile(t, filepath.Join(worktreePath, "feature.go"), "package feature\n")
	run(t, worktreePath, "git", "add", ".")
	run(t, worktreePath, "git", "commit", "-m", "feat: custom branch work")

	// Scan with defaultBranch="develop"
	branches, _, err := findOrphanPolecatBranches(rigDir, rigName, "develop")
	if err != nil {
		t.Fatalf("findOrphanPolecatBranches: %v", err)
	}
	if len(branches) != 1 {
		t.Fatalf("expected 1 orphan branch, got %d", len(branches))
	}

	// Scan with defaultBranch="main" should fail to count (no main branch),
	// and the polecat should be skipped
	branches2, skipped, err := findOrphanPolecatBranches(rigDir, rigName, "main")
	if err != nil {
		t.Fatalf("findOrphanPolecatBranches: %v", err)
	}
	if len(branches2) != 0 {
		t.Errorf("expected 0 branches when scanning with wrong default branch, got %d", len(branches2))
	}
	if len(skipped) != 1 {
		t.Errorf("expected 1 skipped polecat (wrong base branch), got %d", len(skipped))
	}
}

// TestFindOrphanPolecatBranches_OnMain verifies that polecats on main are not
// reported as orphans.
func TestFindOrphanPolecatBranches_OnMain(t *testing.T) {
	rigDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(rigDir, "polecats")
	worktreePath := filepath.Join(polecatsDir, "bravo")
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatal(err)
	}

	run(t, worktreePath, "git", "init")
	writeFile(t, filepath.Join(worktreePath, "README.md"), "# test\n")
	run(t, worktreePath, "git", "add", ".")
	run(t, worktreePath, "git", "commit", "-m", "initial commit")
	run(t, worktreePath, "git", "branch", "-M", "main")

	branches, _, err := findOrphanPolecatBranches(rigDir, rigName, "main")
	if err != nil {
		t.Fatalf("findOrphanPolecatBranches: %v", err)
	}
	if len(branches) != 0 {
		t.Errorf("expected 0 orphan branches for polecat on main, got %d", len(branches))
	}
}

// TestFindOrphanPolecatBranches_NoPolecatsDir verifies graceful handling when
// there is no polecats directory.
func TestFindOrphanPolecatBranches_NoPolecatsDir(t *testing.T) {
	rigDir := t.TempDir()
	branches, skipped, err := findOrphanPolecatBranches(rigDir, "testrig", "main")
	if err != nil {
		t.Fatalf("expected nil error for missing polecats dir, got: %v", err)
	}
	if len(branches) != 0 {
		t.Errorf("expected 0 branches, got %d", len(branches))
	}
	if len(skipped) != 0 {
		t.Errorf("expected 0 skipped, got %d", len(skipped))
	}
}

// TestFindOrphanPolecatBranches_SkippedErrorIncludesStderr verifies that when
// rev-list fails (e.g., because the configured default branch does not exist
// locally or on origin), the skipped error message surfaces git's stderr
// rather than just a bare "exit status 128".
//
// Regression test for gt-vja7w / gu-al8g: previously the scanner swallowed
// stderr, so operators had no diagnostic to act on.
func TestFindOrphanPolecatBranches_SkippedErrorIncludesStderr(t *testing.T) {
	rigDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(rigDir, "polecats")

	// Set up a polecat on a feature branch, with NO default branch (neither
	// "main" nor "origin/main") present in the worktree. This forces both
	// rev-list attempts in findOrphanPolecatBranches to fail.
	polecatName := "echo"
	worktreePath := filepath.Join(polecatsDir, polecatName)
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatal(err)
	}

	run(t, worktreePath, "git", "init")
	writeFile(t, filepath.Join(worktreePath, "README.md"), "# test\n")
	run(t, worktreePath, "git", "add", ".")
	run(t, worktreePath, "git", "commit", "-m", "initial commit")
	// Rename only to a feature branch — no "main" ref exists anywhere.
	run(t, worktreePath, "git", "branch", "-M", "feature/echo-work")

	// Scan with defaultBranch="main" — both origin/main and main are missing,
	// so the polecat must be reported as skipped with a useful diagnostic.
	branches, skipped, err := findOrphanPolecatBranches(rigDir, rigName, "main")
	if err != nil {
		t.Fatalf("findOrphanPolecatBranches: %v", err)
	}
	if len(branches) != 0 {
		t.Errorf("expected 0 branches, got %d", len(branches))
	}
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skipped polecat, got %d", len(skipped))
	}

	got := skipped[0].Err
	// The error must mention rev-list (so operators know which command failed)
	// and must include git's actual diagnostic so they can fix the underlying
	// issue (missing ref, misconfigured default branch, etc.).
	if !strings.Contains(got, "rev-list failed") {
		t.Errorf("expected error to contain %q, got: %q", "rev-list failed", got)
	}
	// git's stderr for an unknown revision typically says "unknown revision"
	// or "ambiguous argument". Either is acceptable — we just need stderr to
	// be surfaced, not swallowed.
	if !strings.Contains(got, "unknown revision") && !strings.Contains(got, "ambiguous argument") {
		t.Errorf("expected error to surface git stderr (unknown revision / ambiguous argument), got: %q", got)
	}
}

// --- helpers ---

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

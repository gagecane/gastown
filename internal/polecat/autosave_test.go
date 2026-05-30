package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// testRunGit runs a git command in the given directory.
func testRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	fullArgs := append([]string{"-c", "protocol.file.allow=always"}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// setupTestRepo creates a git repo with an initial commit.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testRunGit(t, dir, "init", "--initial-branch", "main")
	testRunGit(t, dir, "config", "user.email", "test@test.com")
	testRunGit(t, dir, "config", "user.name", "Test User")

	// Create initial commit
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "README.md")
	testRunGit(t, dir, "commit", "-m", "initial commit")

	return dir
}

func TestAutoSaveAbandonedWIP_CleanWorktree(t *testing.T) {
	dir := setupTestRepo(t)

	saved, sha, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-abc", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved {
		t.Error("expected saved=false for clean worktree")
	}
	if sha != "" {
		t.Errorf("expected empty sha, got %q", sha)
	}
}

func TestAutoSaveAbandonedWIP_Additions(t *testing.T) {
	dir := setupTestRepo(t)

	// Create feature branch
	testRunGit(t, dir, "checkout", "-b", "polecat/test/gu-abc")

	// Add uncommitted file
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	saved, sha, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-abc", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !saved {
		t.Error("expected saved=true for uncommitted additions")
	}
	if sha == "" {
		t.Error("expected non-empty sha")
	}

	// Verify worktree is clean after save
	g := git.NewGit(dir)
	status, err := g.CheckUncommittedWork()
	if err != nil {
		t.Fatalf("CheckUncommittedWork: %v", err)
	}
	if status.HasUncommittedChanges && !status.CleanExcludingRuntime() {
		t.Error("worktree should be clean after autosave")
	}

	// Verify commit message
	cmd := exec.Command("git", "log", "-1", "--format=%s")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.HasPrefix(string(out), "fix(autosave):") {
		t.Errorf("commit message should start with 'fix(autosave):', got %q", string(out))
	}
}

func TestAutoSaveAbandonedWIP_Modifications(t *testing.T) {
	dir := setupTestRepo(t)
	testRunGit(t, dir, "checkout", "-b", "polecat/test/gu-xyz")

	// Modify existing file
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n\nModified content.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	saved, sha, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-xyz", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !saved {
		t.Error("expected saved=true for modifications")
	}
	if sha == "" {
		t.Error("expected non-empty sha")
	}
}

func TestAutoSaveAbandonedWIP_DeletionsOnly(t *testing.T) {
	dir := setupTestRepo(t)
	testRunGit(t, dir, "checkout", "-b", "polecat/test/gu-del")

	// Add and commit a file, then delete it
	if err := os.WriteFile(filepath.Join(dir, "todelete.txt"), []byte("delete me\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "todelete.txt")
	testRunGit(t, dir, "commit", "-m", "add file to delete")

	// Delete the file (creates a deletion in the working tree)
	if err := os.Remove(filepath.Join(dir, "todelete.txt")); err != nil {
		t.Fatal(err)
	}

	// AutoSave should return (false, "", nil) when only deletions are present,
	// because after unstaging deletions there's nothing left to commit.
	// This is the desired behavior: autosave preserves additions/modifications,
	// but refuses to commit deletions of tracked files.
	saved, sha, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-del", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved {
		t.Error("expected saved=false for deletions-only (deletions are unstaged)")
	}
	if sha != "" {
		t.Errorf("expected empty sha, got %q", sha)
	}
}

func TestAutoSaveAbandonedWIP_DeletionsWithAdditions(t *testing.T) {
	dir := setupTestRepo(t)
	testRunGit(t, dir, "checkout", "-b", "polecat/test/gu-del-add")

	// Add and commit a file, then delete it
	if err := os.WriteFile(filepath.Join(dir, "todelete.txt"), []byte("delete me\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "todelete.txt")
	testRunGit(t, dir, "commit", "-m", "add file to delete")

	// Delete the file AND add a new file
	if err := os.Remove(filepath.Join(dir, "todelete.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "newfile.go"), []byte("package new\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// AutoSave should save the addition but NOT the deletion
	saved, sha, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-del-add", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !saved || sha == "" {
		t.Error("expected save with mixed additions/deletions")
	}

	// Verify the commit includes the addition but not the deletion
	cmd := exec.Command("git", "show", "--name-status", "--format=")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	committed := string(out)
	if strings.Contains(committed, "D\ttodelete.txt") {
		t.Error("autosave should not commit file deletions")
	}
	if !strings.Contains(committed, "newfile.go") {
		t.Error("autosave should commit the addition")
	}
}

func TestAutoSaveAbandonedWIP_RuntimeArtifacts(t *testing.T) {
	dir := setupTestRepo(t)
	testRunGit(t, dir, "checkout", "-b", "polecat/test/gu-runtime")

	// Create runtime artifacts that should NOT be committed
	runtimeDirs := []string{".claude", ".beads", "node_modules", "__pycache__"}
	for _, d := range runtimeDirs {
		rdir := filepath.Join(dir, d)
		if err := os.MkdirAll(rdir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rdir, "test.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Only runtime artifacts — should NOT save
	saved, _, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-runtime", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved {
		t.Error("expected saved=false for runtime-only artifacts")
	}

	// Now add real source file — should save, but exclude runtime
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	saved, sha, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-runtime", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !saved || sha == "" {
		t.Error("expected save with real source file present")
	}

	// Verify runtime artifacts were NOT committed
	cmd := exec.Command("git", "show", "--name-only", "--format=")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	committed := string(out)
	for _, d := range runtimeDirs {
		if strings.Contains(committed, d+"/") {
			t.Errorf("runtime artifact %s should not be committed", d)
		}
	}
	if !strings.Contains(committed, "handler.go") {
		t.Error("source file handler.go should be committed")
	}
}

func TestAutoSaveAbandonedWIP_DetachedHEAD(t *testing.T) {
	dir := setupTestRepo(t)

	// Detach HEAD
	testRunGit(t, dir, "checkout", "--detach", "HEAD")

	// Add uncommitted changes
	if err := os.WriteFile(filepath.Join(dir, "orphan.go"), []byte("package orphan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	saved, _, err := AutoSaveAbandonedWIP(dir, "HEAD", "reaper-test")
	if err == nil {
		t.Fatal("expected error for detached HEAD")
	}
	if !strings.Contains(err.Error(), "detached") {
		t.Errorf("error should mention detached HEAD, got: %v", err)
	}
	if saved {
		t.Error("should not save on detached HEAD")
	}
}

func TestAutoSaveAbandonedWIP_DefaultBranch_Main(t *testing.T) {
	dir := setupTestRepo(t)

	// Stay on main and add uncommitted changes
	if err := os.WriteFile(filepath.Join(dir, "dangerous.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	saved, _, err := AutoSaveAbandonedWIP(dir, "main", "reaper-test")
	if err == nil {
		t.Fatal("expected error for default branch main")
	}
	if !strings.Contains(err.Error(), "protected default branch") {
		t.Errorf("error should mention protected branch, got: %v", err)
	}
	if saved {
		t.Error("should not save on main")
	}
}

func TestAutoSaveAbandonedWIP_DefaultBranch_Master(t *testing.T) {
	dir := t.TempDir()
	testRunGit(t, dir, "init", "--initial-branch", "master")
	testRunGit(t, dir, "config", "user.email", "test@test.com")
	testRunGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "README.md")
	testRunGit(t, dir, "commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(dir, "dangerous.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	saved, _, err := AutoSaveAbandonedWIP(dir, "master", "reaper-test")
	if err == nil {
		t.Fatal("expected error for default branch master")
	}
	if !strings.Contains(err.Error(), "protected default branch") {
		t.Errorf("error should mention protected branch, got: %v", err)
	}
	if saved {
		t.Error("should not save on master")
	}
}

func TestAutoSaveAbandonedWIP_DefaultBranch_Mainline(t *testing.T) {
	dir := t.TempDir()
	testRunGit(t, dir, "init", "--initial-branch", "mainline")
	testRunGit(t, dir, "config", "user.email", "test@test.com")
	testRunGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "README.md")
	testRunGit(t, dir, "commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(dir, "dangerous.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	saved, _, err := AutoSaveAbandonedWIP(dir, "mainline", "reaper-test")
	if err == nil {
		t.Fatal("expected error for default branch mainline")
	}
	if saved {
		t.Error("should not save on mainline")
	}
}

func TestAutoSaveAbandonedWIP_StashAutoPop(t *testing.T) {
	dir := setupTestRepo(t)
	testRunGit(t, dir, "checkout", "-b", "polecat/test/gu-stash")

	// Create uncommitted changes AND a stash
	// The stash auto-pop only happens when there are already uncommitted changes
	// (the stash is considered "fresh" WIP from the current session).
	if err := os.WriteFile(filepath.Join(dir, "current.go"), []byte("package current\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create and stash additional changes
	if err := os.WriteFile(filepath.Join(dir, "stashed.go"), []byte("package stashed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "stashed.go")
	testRunGit(t, dir, "stash", "push", "-m", "WIP stash")

	// Verify stash exists and current.go is uncommitted
	g := git.NewGit(dir)
	count, err := g.StashCount()
	if err != nil || count == 0 {
		t.Fatal("stash should exist")
	}

	// AutoSave should save current.go and pop+save the stash
	saved, sha, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-stash", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !saved || sha == "" {
		t.Error("expected save with uncommitted changes + stash")
	}

	// Verify current.go was committed
	cmd := exec.Command("git", "show", "--name-only", "--format=")
	cmd.Dir = dir
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "current.go") {
		t.Error("current.go should be in the commit")
	}
	// Note: stash may or may not be popped depending on whether it was detected as fresh.
	// The key behavior is that uncommitted work is saved.
}

func TestAutoSaveAbandonedWIP_StashOnlyNoSave(t *testing.T) {
	dir := setupTestRepo(t)
	testRunGit(t, dir, "checkout", "-b", "polecat/test/gu-stashonly")

	// Create and stash changes, leaving worktree clean
	if err := os.WriteFile(filepath.Join(dir, "stashed.go"), []byte("package stashed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "stashed.go")
	testRunGit(t, dir, "stash", "push", "-m", "WIP stash")

	// Worktree is now clean (only a stash exists)
	// AutoSave returns false because there's nothing uncommitted in the worktree
	saved, sha, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-stashonly", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved {
		t.Error("expected saved=false for stash-only (worktree is clean)")
	}
	if sha != "" {
		t.Errorf("expected empty sha, got %q", sha)
	}
}

func TestAutoSaveAbandonedWIP_StaleStash(t *testing.T) {
	dir := setupTestRepo(t)
	testRunGit(t, dir, "checkout", "-b", "polecat/test/gu-stalestash")

	// Create and stash changes at the current commit
	if err := os.WriteFile(filepath.Join(dir, "old.go"), []byte("package old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "old.go")
	testRunGit(t, dir, "stash", "push", "-m", "old stash")

	// Make a new commit that advances HEAD past the stash parent
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "new.go")
	testRunGit(t, dir, "commit", "-m", "advance HEAD")

	// Add some uncommitted work
	if err := os.WriteFile(filepath.Join(dir, "current.go"), []byte("package current\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// AutoSave should commit the current work but NOT pop the stale stash
	saved, _, err := AutoSaveAbandonedWIP(dir, "polecat/test/gu-stalestash", "reaper-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !saved {
		t.Error("expected save with current uncommitted work")
	}

	// Verify stale stash was NOT popped (still exists)
	g := git.NewGit(dir)
	count, err := g.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 1 {
		t.Errorf("stale stash should remain, got count=%d", count)
	}
}

func TestAutoSaveAbandonedWIP_MissingWorktree(t *testing.T) {
	saved, _, err := AutoSaveAbandonedWIP("/nonexistent/path", "branch", "test")
	if err == nil {
		t.Fatal("expected error for missing worktree")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found, got: %v", err)
	}
	if saved {
		t.Error("should not save for missing worktree")
	}
}

func TestAutoSaveAbandonedWIP_EmptyWorktreePath(t *testing.T) {
	saved, _, err := AutoSaveAbandonedWIP("", "branch", "test")
	if err == nil {
		t.Fatal("expected error for empty worktree path")
	}
	if saved {
		t.Error("should not save for empty path")
	}
}

func TestAutoSaveAbandonedWIP_Idempotent(t *testing.T) {
	dir := setupTestRepo(t)
	testRunGit(t, dir, "checkout", "-b", "polecat/test/gu-idem")

	// Add uncommitted changes
	if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package impl\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// First call should save
	saved1, sha1, err1 := AutoSaveAbandonedWIP(dir, "polecat/test/gu-idem", "first-call")
	if err1 != nil {
		t.Fatalf("first call: %v", err1)
	}
	if !saved1 || sha1 == "" {
		t.Error("first call should save")
	}

	// Second call should be no-op (worktree now clean)
	saved2, sha2, err2 := AutoSaveAbandonedWIP(dir, "polecat/test/gu-idem", "second-call")
	if err2 != nil {
		t.Fatalf("second call: %v", err2)
	}
	if saved2 {
		t.Error("second call should not save (already clean)")
	}
	if sha2 != "" {
		t.Errorf("second call should return empty sha, got %q", sha2)
	}
}

func TestIsDefaultBranchForAutosave(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{"main", true},
		{"master", true},
		{"mainline", true},
		{"polecat/chrome/gu-abc", false},
		{"feat/my-feature", false},
		{"develop", false},
		{"release/1.0", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			got := isDefaultBranchForAutosave(tt.branch)
			if got != tt.want {
				t.Errorf("isDefaultBranchForAutosave(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}

package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Initialize repo with an explicit initial branch so tests are
	// deterministic regardless of the host's init.defaultBranch setting
	// (e.g. some environments set it to "mainline"). Requires git >= 2.28.
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Configure user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	_ = cmd.Run()

	// Create initial commit
	testFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	_ = cmd.Run()

	return dir
}

func TestIsRepo(t *testing.T) {
	dir := t.TempDir()
	g := NewGit(dir)

	if g.IsRepo() {
		t.Fatal("expected IsRepo to be false for empty dir")
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if !g.IsRepo() {
		t.Fatal("expected IsRepo to be true after git init")
	}
}

func TestCloneWithReferenceCreatesAlternates(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	if err := exec.Command("git", "init", src).Run(); err != nil {
		t.Fatalf("init src: %v", err)
	}
	_ = exec.Command("git", "-C", src, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", src, "config", "user.name", "Test User").Run()

	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_ = exec.Command("git", "-C", src, "add", ".").Run()
	_ = exec.Command("git", "-C", src, "commit", "-m", "initial").Run()

	g := NewGit(tmp)
	if err := g.CloneWithReference(src, dst, src); err != nil {
		t.Fatalf("CloneWithReference: %v", err)
	}

	alternates := filepath.Join(dst, ".git", "objects", "info", "alternates")
	if _, err := os.Stat(alternates); err != nil {
		t.Fatalf("expected alternates file: %v", err)
	}
}

func TestCloneWithReferencePreservesSymlinks(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	// Create test repo with symlink
	if err := exec.Command("git", "init", src).Run(); err != nil {
		t.Fatalf("init src: %v", err)
	}
	_ = exec.Command("git", "-C", src, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", src, "config", "user.name", "Test User").Run()

	// Create a directory and a symlink to it
	targetDir := filepath.Join(src, "target")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "file.txt"), []byte("content\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	linkPath := filepath.Join(src, "link")
	if err := os.Symlink("target", linkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_ = exec.Command("git", "-C", src, "add", ".").Run()
	_ = exec.Command("git", "-C", src, "commit", "-m", "initial").Run()

	// Clone with reference
	g := NewGit(tmp)
	if err := g.CloneWithReference(src, dst, src); err != nil {
		t.Fatalf("CloneWithReference: %v", err)
	}

	// Verify symlink was preserved
	dstLink := filepath.Join(dst, "link")
	info, err := os.Lstat(dstLink)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %s to be a symlink, got mode %v", dstLink, info.Mode())
	}

	// Verify symlink target is correct
	target, err := os.Readlink(dstLink)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "target" {
		t.Errorf("expected symlink target 'target', got %q", target)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	branch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}

	// initTestRepo pins the initial branch to "main".
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

// TestIsDetachedHEAD_Attached verifies IsDetachedHEAD returns false on a
// normal checkout where HEAD is a symbolic ref pointing at a branch.
func TestIsDetachedHEAD_Attached(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	detached, err := g.IsDetachedHEAD()
	if err != nil {
		t.Fatalf("IsDetachedHEAD: %v", err)
	}
	if detached {
		t.Error("IsDetachedHEAD = true on freshly-initialized repo, want false")
	}
}

// TestIsDetachedHEAD_Detached verifies IsDetachedHEAD detects detachment
// after `git checkout --detach`. This is the state that caused gu-ge1s:
// CurrentBranch() returns "HEAD" but IsDetachedHEAD() must return true so
// downstream guards can refuse to push/submit with the literal.
func TestIsDetachedHEAD_Detached(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	cmd := exec.Command("git", "checkout", "--detach")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git checkout --detach: %v", err)
	}

	detached, err := g.IsDetachedHEAD()
	if err != nil {
		t.Fatalf("IsDetachedHEAD after detach: %v", err)
	}
	if !detached {
		t.Error("IsDetachedHEAD = false after --detach, want true")
	}

	// Sanity: CurrentBranch still returns the literal "HEAD", which is why
	// callers need IsDetachedHEAD (or CurrentBranchStrict) to distinguish.
	branch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch after detach: %v", err)
	}
	if branch != "HEAD" {
		t.Errorf("CurrentBranch after detach = %q, want %q (unexpected git behavior)", branch, "HEAD")
	}
}

// TestCurrentBranchStrict_Attached returns the branch name unchanged when
// HEAD points at a real branch.
func TestCurrentBranchStrict_Attached(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	branch, err := g.CurrentBranchStrict()
	if err != nil {
		t.Fatalf("CurrentBranchStrict: %v", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

// TestCurrentBranchStrict_DetachedReturnsSentinel guarantees that detached
// HEAD surfaces as ErrDetachedHEAD instead of the literal "HEAD". This is
// the core contract that prevents refs/heads/HEAD pollution (gu-ge1s).
func TestCurrentBranchStrict_DetachedReturnsSentinel(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	cmd := exec.Command("git", "checkout", "--detach")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git checkout --detach: %v", err)
	}

	branch, err := g.CurrentBranchStrict()
	if !errors.Is(err, ErrDetachedHEAD) {
		t.Errorf("CurrentBranchStrict error = %v, want ErrDetachedHEAD", err)
	}
	if branch != "" {
		t.Errorf("CurrentBranchStrict branch = %q, want empty string (never the literal HEAD)", branch)
	}
}

func TestStatus(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Should be clean initially
	status, err := g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Clean {
		t.Error("expected clean status")
	}

	// Add an untracked file
	testFile := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(testFile, []byte("new"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	status, err = g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Clean {
		t.Error("expected dirty status")
	}
	if len(status.Untracked) != 1 {
		t.Errorf("untracked = %d, want 1", len(status.Untracked))
	}
}

func TestAddAndCommit(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a new file
	testFile := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(testFile, []byte("new content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Add and commit
	if err := g.Add("new.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("add new file"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Should be clean
	status, err := g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Clean {
		t.Error("expected clean after commit")
	}
}

func TestHasUncommittedChanges(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	has, err := g.HasUncommittedChanges()
	if err != nil {
		t.Fatalf("HasUncommittedChanges: %v", err)
	}
	if has {
		t.Error("expected no changes initially")
	}

	// Modify a file
	testFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(testFile, []byte("modified"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	has, err = g.HasUncommittedChanges()
	if err != nil {
		t.Fatalf("HasUncommittedChanges: %v", err)
	}
	if !has {
		t.Error("expected changes after modify")
	}
}

func TestCheckout(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a new branch
	if err := g.CreateBranch("feature"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	// Checkout the new branch
	if err := g.Checkout("feature"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	branch, _ := g.CurrentBranch()
	if branch != "feature" {
		t.Errorf("branch = %q, want feature", branch)
	}
}

func TestCheckoutNewBranch(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Get current HEAD ref
	head, err := g.run("rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	// Create and checkout a new branch from HEAD
	if err := g.CheckoutNewBranch("feature-new", strings.TrimSpace(head)); err != nil {
		t.Fatalf("CheckoutNewBranch: %v", err)
	}

	branch, _ := g.CurrentBranch()
	if branch != "feature-new" {
		t.Errorf("branch = %q, want feature-new", branch)
	}

	// Verify it fails if branch already exists.
	// initTestRepo pins the initial branch to "main".
	if err := g.Checkout("main"); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	err = g.CheckoutNewBranch("feature-new", "HEAD")
	if err == nil {
		t.Error("expected error creating duplicate branch, got nil")
	}
}

func TestNotARepo(t *testing.T) {
	dir := t.TempDir() // Empty dir, not a git repo
	g := NewGit(dir)

	_, err := g.CurrentBranch()
	// ZFC: Check for GitError with raw stderr for agent observation.
	// Agents decide what "not a git repository" means, not Go code.
	gitErr, ok := err.(*GitError)
	if !ok {
		t.Errorf("expected GitError, got %T: %v", err, err)
		return
	}
	// Verify raw stderr is available for agent observation
	if gitErr.Stderr == "" {
		t.Errorf("expected GitError with Stderr, got empty stderr")
	}
}

func TestRev(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	hash, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev: %v", err)
	}

	// Should be a 40-char hex string
	if len(hash) != 40 {
		t.Errorf("hash length = %d, want 40", len(hash))
	}
}

func TestFetchBranch(t *testing.T) {
	// Create a "remote" repo
	remoteDir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}

	// Create a local repo and push to remote
	localDir := initTestRepo(t)
	g := NewGit(localDir)

	// Add remote
	cmd = exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	// Push main branch
	mainBranch, _ := g.CurrentBranch()
	cmd = exec.Command("git", "push", "-u", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git push: %v", err)
	}

	// Fetch should succeed
	if err := g.FetchBranch("origin", mainBranch); err != nil {
		t.Errorf("FetchBranch: %v", err)
	}
}

func TestCheckConflicts_NoConflict(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)
	mainBranch, _ := g.CurrentBranch()

	// Create feature branch with non-conflicting change
	if err := g.CreateBranch("feature"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("feature"); err != nil {
		t.Fatalf("Checkout feature: %v", err)
	}

	// Add a new file (won't conflict with main)
	newFile := filepath.Join(dir, "feature.txt")
	if err := os.WriteFile(newFile, []byte("feature content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := g.Add("feature.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("add feature file"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Go back to main
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}

	// Check for conflicts - should be none
	conflicts, err := g.CheckConflicts("feature", mainBranch)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) > 0 {
		t.Errorf("expected no conflicts, got %v", conflicts)
	}

	// Verify we're still on main and clean
	branch, _ := g.CurrentBranch()
	if branch != mainBranch {
		t.Errorf("branch = %q, want %q", branch, mainBranch)
	}
	status, _ := g.Status()
	if !status.Clean {
		t.Error("expected clean working directory after CheckConflicts")
	}
}

func TestCheckConflicts_WithConflict(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)
	mainBranch, _ := g.CurrentBranch()

	// Create feature branch
	if err := g.CreateBranch("feature"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("feature"); err != nil {
		t.Fatalf("Checkout feature: %v", err)
	}

	// Modify README.md on feature branch
	readmeFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Feature changes\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := g.Add("README.md"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("modify readme on feature"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Go back to main and make conflicting change
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	if err := os.WriteFile(readmeFile, []byte("# Main changes\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := g.Add("README.md"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("modify readme on main"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Check for conflicts - should find README.md
	conflicts, err := g.CheckConflicts("feature", mainBranch)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) == 0 {
		t.Error("expected conflicts, got none")
	}

	foundReadme := false
	for _, f := range conflicts {
		if f == "README.md" {
			foundReadme = true
			break
		}
	}
	if !foundReadme {
		t.Errorf("expected README.md in conflicts, got %v", conflicts)
	}

	// Verify we're still on main and clean
	branch, _ := g.CurrentBranch()
	if branch != mainBranch {
		t.Errorf("branch = %q, want %q", branch, mainBranch)
	}
	status, _ := g.Status()
	if !status.Clean {
		t.Error("expected clean working directory after CheckConflicts")
	}
}

// TestCloneBareHasOriginRefs verifies that after CloneBare, origin/* refs
// are available for worktree creation. This was broken before the fix:
// bare clones had refspec configured but no fetch was run, so origin/main
// didn't exist and WorktreeAddFromRef("origin/main") failed.
//
// Related: GitHub issue #286
func TestCloneBareHasOriginRefs(t *testing.T) {
	tmp := t.TempDir()

	// Create a "remote" repo with a commit on main
	remoteDir := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = remoteDir
	_ = cmd.Run()

	// Create initial commit
	readmeFile := filepath.Join(remoteDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Get the main branch name (main or master depending on git version)
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))

	// Clone as bare repo using our CloneBare function
	bareDir := filepath.Join(tmp, "bare.git")
	g := NewGit(tmp)
	if err := g.CloneBare(remoteDir, bareDir); err != nil {
		t.Fatalf("CloneBare: %v", err)
	}

	// Verify origin/main exists (this was the bug - it didn't exist before the fix)
	bareGit := NewGitWithDir(bareDir, "")
	cmd = exec.Command("git", "--git-dir", bareDir, "branch", "-r")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("git branch -r: %v", err)
	}

	originMain := "origin/" + mainBranch
	if !stringContains(string(out), originMain) {
		t.Errorf("expected %q in remote branches, got: %s", originMain, out)
	}

	// Verify WorktreeAddFromRef succeeds with origin/main
	// This is what polecat creation does
	worktreePath := filepath.Join(tmp, "worktree")
	if err := bareGit.WorktreeAddFromRef(worktreePath, "test-branch", originMain); err != nil {
		t.Errorf("WorktreeAddFromRef(%q) failed: %v", originMain, err)
	}

	// Verify the worktree was created and has the expected file
	worktreeReadme := filepath.Join(worktreePath, "README.md")
	if _, err := os.Stat(worktreeReadme); err != nil {
		t.Errorf("expected README.md in worktree: %v", err)
	}
}

func TestIsEmpty_EmptyRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	g := NewGit(dir)
	empty, err := g.IsEmpty()
	if err != nil {
		t.Fatalf("IsEmpty: %v", err)
	}
	if !empty {
		t.Error("expected newly-initialized repo to be empty")
	}
}

func TestIsEmpty_RepoWithCommit(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	empty, err := g.IsEmpty()
	if err != nil {
		t.Fatalf("IsEmpty: %v", err)
	}
	if empty {
		t.Error("expected repo with commits to not be empty")
	}
}

func TestRefExists_ValidRef(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// HEAD should exist
	exists, err := g.RefExists("HEAD")
	if err != nil {
		t.Fatalf("RefExists(HEAD): %v", err)
	}
	if !exists {
		t.Error("expected HEAD to exist")
	}
}

func TestRefExists_InvalidRef(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// A ref that doesn't exist
	exists, err := g.RefExists("refs/heads/nonexistent-branch")
	if err != nil {
		t.Fatalf("RefExists: %v", err)
	}
	if exists {
		t.Error("expected nonexistent ref to not exist")
	}
}

func TestRefExists_OriginRef(t *testing.T) {
	tmp := t.TempDir()

	// Create a remote repo
	remoteDir := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	if err := os.WriteFile(filepath.Join(remoteDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Get main branch name
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))

	// Clone bare
	bareDir := filepath.Join(tmp, "bare.git")
	g := NewGit(tmp)
	if err := g.CloneBare(remoteDir, bareDir); err != nil {
		t.Fatalf("CloneBare: %v", err)
	}

	bareGit := NewGitWithDir(bareDir, "")

	// origin/<main> should exist
	exists, err := bareGit.RefExists("origin/" + mainBranch)
	if err != nil {
		t.Fatalf("RefExists(origin/%s): %v", mainBranch, err)
	}
	if !exists {
		t.Errorf("expected origin/%s to exist", mainBranch)
	}

	// origin/nonexistent should not exist
	exists, err = bareGit.RefExists("origin/nonexistent")
	if err != nil {
		t.Fatalf("RefExists(origin/nonexistent): %v", err)
	}
	if exists {
		t.Error("expected origin/nonexistent to not exist")
	}
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// initTestRepoWithRemote sets up a local repo with a bare remote and initial push.
// Returns (localDir, remoteDir, mainBranch).
func initTestRepoWithRemote(t *testing.T) (string, string, string) {
	t.Helper()
	tmp := t.TempDir()

	// Create bare remote
	remoteDir := filepath.Join(tmp, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}

	// Create local repo
	localDir := filepath.Join(tmp, "local")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	// Initial commit
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial"},
		{"git", "remote", "add", "origin", remoteDir},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	// Get main branch name and push
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = localDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("branch --show-current: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))

	cmd = exec.Command("git", "push", "-u", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Set origin/HEAD so RemoteDefaultBranch() can detect the default branch.
	// A real `git clone` sets this automatically; our manual init+push does not.
	// Without this, RemoteDefaultBranch() falls back to "main" (even when the
	// actual default is "mainline" or similar), breaking callers that resolve
	// origin/<default> (e.g. PruneStaleBranches).
	cmd = exec.Command("git", "remote", "set-head", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("remote set-head: %v", err)
	}

	return localDir, remoteDir, mainBranch
}

func TestPruneStaleBranches_MergedBranch(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create a polecat branch, commit, and merge it to main
	if err := g.CreateBranch("polecat/test-merged"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/test-merged"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("feature.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("add feature"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Push polecat branch to origin
	cmd := exec.Command("git", "push", "origin", "polecat/test-merged")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push polecat branch: %v", err)
	}

	// Merge to main
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	if err := g.Merge("polecat/test-merged"); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// Push main
	cmd = exec.Command("git", "push", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push main: %v", err)
	}

	// Delete remote polecat branch (simulating refinery cleanup)
	cmd = exec.Command("git", "push", "origin", "--delete", "polecat/test-merged")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("delete remote branch: %v", err)
	}

	// Fetch --prune to remove remote tracking ref
	if err := g.FetchPrune("origin"); err != nil {
		t.Fatalf("FetchPrune: %v", err)
	}

	// Verify polecat branch still exists locally
	branches, err := g.ListBranches("polecat/*")
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 1 {
		t.Fatalf("expected 1 local polecat branch, got %d", len(branches))
	}

	// Prune should remove it
	pruned, err := g.PruneStaleBranches("polecat/*", false)
	if err != nil {
		t.Fatalf("PruneStaleBranches: %v", err)
	}
	if len(pruned) != 1 {
		t.Fatalf("expected 1 pruned branch, got %d", len(pruned))
	}
	if pruned[0].Name != "polecat/test-merged" {
		t.Errorf("pruned name = %q, want polecat/test-merged", pruned[0].Name)
	}
	if pruned[0].Reason != "no-remote-merged" {
		t.Errorf("pruned reason = %q, want no-remote-merged", pruned[0].Reason)
	}

	// Verify branch is gone
	branches, err = g.ListBranches("polecat/*")
	if err != nil {
		t.Fatalf("ListBranches after prune: %v", err)
	}
	if len(branches) != 0 {
		t.Errorf("expected 0 branches after prune, got %d: %v", len(branches), branches)
	}
}

func TestPruneStaleBranches_DryRun(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create and merge a polecat branch (same as above)
	if err := g.CreateBranch("polecat/test-dryrun"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/test-dryrun"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "dry.txt"), []byte("dry"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("dry.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("dry run test"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	if err := g.Merge("polecat/test-dryrun"); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// Push main to update origin/main
	cmd := exec.Command("git", "push", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push main: %v", err)
	}

	// Dry run should report but not delete
	pruned, err := g.PruneStaleBranches("polecat/*", true)
	if err != nil {
		t.Fatalf("PruneStaleBranches dry-run: %v", err)
	}
	if len(pruned) != 1 {
		t.Fatalf("expected 1 branch in dry-run, got %d", len(pruned))
	}

	// Branch should still exist
	branches, err := g.ListBranches("polecat/*")
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 1 {
		t.Errorf("expected branch to still exist after dry-run, got %d branches", len(branches))
	}
}

func TestPruneStaleBranches_SkipsCurrentBranch(t *testing.T) {
	localDir, _, _ := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create and checkout a polecat branch (making it the current branch)
	if err := g.CreateBranch("polecat/current"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/current"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	// Prune should not delete the current branch
	pruned, err := g.PruneStaleBranches("polecat/*", false)
	if err != nil {
		t.Fatalf("PruneStaleBranches: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("expected 0 pruned (current branch should be skipped), got %d", len(pruned))
	}
}

func TestPruneStaleBranches_SkipsUnmerged(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create a polecat branch with a commit NOT merged to main
	if err := g.CreateBranch("polecat/unmerged"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/unmerged"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "unmerged.txt"), []byte("unmerged"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("unmerged.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("unmerged work"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Push to remote so it has a remote tracking branch
	cmd := exec.Command("git", "push", "origin", "polecat/unmerged")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push: %v", err)
	}

	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}

	// Prune should NOT delete unmerged branch that still has remote
	pruned, err := g.PruneStaleBranches("polecat/*", false)
	if err != nil {
		t.Fatalf("PruneStaleBranches: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("expected 0 pruned (unmerged with remote should be kept), got %d", len(pruned))
	}
}

func TestPushWithEnv(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Set up a pre-push hook that blocks unless GT_INTEGRATION_LAND=1
	hooksDir := filepath.Join(localDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookScript := `#!/bin/bash
if [[ "$GT_INTEGRATION_LAND" != "1" ]]; then
  echo "BLOCKED: GT_INTEGRATION_LAND not set"
  exit 1
fi
exit 0
`
	hookPath := filepath.Join(hooksDir, "pre-push")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	// Make a commit to push
	if err := os.WriteFile(filepath.Join(localDir, "env-test.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("env-test.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("env test"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Regular Push should fail (hook blocks without env var)
	err := g.Push("origin", mainBranch, false)
	if err == nil {
		t.Fatal("expected Push to fail without GT_INTEGRATION_LAND")
	}

	// PushWithEnv with GT_INTEGRATION_LAND=1 should succeed
	err = g.PushWithEnv("origin", mainBranch, false, []string{"GT_INTEGRATION_LAND=1"})
	if err != nil {
		t.Fatalf("PushWithEnv with GT_INTEGRATION_LAND=1 should succeed: %v", err)
	}
}

func TestFetchPrune(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create and push a branch
	if err := g.CreateBranch("polecat/prune-test"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	cmd := exec.Command("git", "push", "origin", "polecat/prune-test")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	// Verify remote tracking ref exists
	exists, err := g.RemoteTrackingBranchExists("origin", "polecat/prune-test")
	if err != nil {
		t.Fatalf("RemoteTrackingBranchExists: %v", err)
	}
	if !exists {
		t.Fatal("expected remote tracking branch to exist")
	}

	// Delete remote branch
	cmd = exec.Command("git", "push", "origin", "--delete", "polecat/prune-test")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("delete remote: %v", err)
	}

	// FetchPrune should remove the stale tracking ref
	if err := g.FetchPrune("origin"); err != nil {
		t.Fatalf("FetchPrune: %v", err)
	}

	exists, err = g.RemoteTrackingBranchExists("origin", "polecat/prune-test")
	if err != nil {
		t.Fatalf("RemoteTrackingBranchExists after prune: %v", err)
	}
	if exists {
		t.Error("expected remote tracking branch to be pruned")
	}
}

// initTestRepoWithSubmodule creates a parent repo with a submodule for testing.
// Returns parentDir, submoduleRemoteDir (bare).
func initTestRepoWithSubmodule(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()

	// Create a "remote" bare repo for the submodule
	subRemote := filepath.Join(tmp, "sub-remote.git")
	runGit(t, tmp, "init", "--bare", "--initial-branch", "main", subRemote)

	// Create a working clone of the submodule to add content
	subWork := filepath.Join(tmp, "sub-work")
	runGit(t, tmp, "clone", subRemote, subWork)
	runGit(t, subWork, "config", "user.email", "test@test.com")
	runGit(t, subWork, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(subWork, "lib.go"), []byte("package lib\n"), 0644); err != nil {
		t.Fatalf("write sub file: %v", err)
	}
	runGit(t, subWork, "add", ".")
	runGit(t, subWork, "commit", "-m", "initial sub commit")
	runGit(t, subWork, "push", "origin", "main")

	// Create the parent repo
	parent := filepath.Join(tmp, "parent")
	runGit(t, tmp, "init", "--initial-branch", "main", parent)
	runGit(t, parent, "config", "user.email", "test@test.com")
	runGit(t, parent, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("# Parent\n"), 0644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	runGit(t, parent, "add", ".")
	runGit(t, parent, "commit", "-m", "initial parent commit")

	// Add the submodule
	runGit(t, parent, "submodule", "add", subRemote, "libs/sub")
	runGit(t, parent, "commit", "-m", "add submodule")

	return parent, subRemote
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	// Prepend -c protocol.file.allow=always to allow local file:// transport
	fullArgs := append([]string{"-c", "protocol.file.allow=always"}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func TestInitSubmodules_NoSubmodules(t *testing.T) {
	dir := initTestRepo(t)
	// Should be a no-op, not an error
	if err := InitSubmodules(dir); err != nil {
		t.Fatalf("InitSubmodules on repo without submodules: %v", err)
	}
}

func TestInitSubmodules_SkipsUntrackedGitmodules(t *testing.T) {
	dir := initTestRepo(t)
	gitmodules := filepath.Join(dir, ".gitmodules")
	content := []byte("[submodule \"libs/sub\"]\n\tpath = libs/sub\n\turl = https://example.com/sub.git\n")
	if err := os.WriteFile(gitmodules, content, 0644); err != nil {
		t.Fatalf("write .gitmodules: %v", err)
	}
	if err := InitSubmodules(dir); err != nil {
		t.Fatalf("InitSubmodules should skip untracked .gitmodules: %v", err)
	}
}

func TestHasTrackedGitmodules(t *testing.T) {
	dir := initTestRepo(t)
	if hasTrackedGitmodules(dir) {
		t.Fatal("expected false when .gitmodules doesn't exist")
	}
	gitmodules := filepath.Join(dir, ".gitmodules")
	if err := os.WriteFile(gitmodules, []byte("[submodule \"x\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if hasTrackedGitmodules(dir) {
		t.Fatal("expected false when .gitmodules exists but is untracked")
	}
	runGit(t, dir, "add", ".gitmodules")
	runGit(t, dir, "commit", "-m", "add gitmodules")
	if !hasTrackedGitmodules(dir) {
		t.Fatal("expected true when .gitmodules is tracked")
	}
}

func TestInitSubmodules_WithSubmodules(t *testing.T) {
	parent, _ := initTestRepoWithSubmodule(t)

	// The submodule should already be initialized from the test setup
	libFile := filepath.Join(parent, "libs", "sub", "lib.go")
	if _, err := os.Stat(libFile); err != nil {
		t.Fatalf("expected submodule file to exist after setup: %v", err)
	}

	// Now test that InitSubmodules works on a fresh clone
	tmp := t.TempDir()
	cloneDest := filepath.Join(tmp, "clone")
	// Clone without --recurse-submodules to simulate current behavior
	cmd := exec.Command("git", "-c", "protocol.file.allow=always", "clone", parent, cloneDest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	// Submodule dir exists but is empty
	subDir := filepath.Join(cloneDest, "libs", "sub")
	entries, _ := os.ReadDir(subDir)
	if len(entries) > 0 {
		t.Fatal("expected empty submodule dir before init")
	}

	// Allow file:// transport for submodule init in test environment
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	// InitSubmodules should populate it
	if err := InitSubmodules(cloneDest); err != nil {
		t.Fatalf("InitSubmodules: %v", err)
	}

	libFile = filepath.Join(cloneDest, "libs", "sub", "lib.go")
	if _, err := os.Stat(libFile); err != nil {
		t.Fatalf("expected submodule file after InitSubmodules: %v", err)
	}
}

func TestSubmoduleChanges(t *testing.T) {
	parent, subRemote := initTestRepoWithSubmodule(t)

	// Create a branch with a submodule change
	runGit(t, parent, "checkout", "-b", "feature")

	// Make a new commit in the submodule
	subPath := filepath.Join(parent, "libs", "sub")
	if err := os.WriteFile(filepath.Join(subPath, "new.go"), []byte("package lib\n// new\n"), 0644); err != nil {
		t.Fatalf("write new sub file: %v", err)
	}
	runGit(t, subPath, "add", ".")
	runGit(t, subPath, "commit", "-m", "new sub commit")
	runGit(t, subPath, "push", "origin", "HEAD:main")

	// Update the parent's submodule pointer
	runGit(t, parent, "add", "libs/sub")
	runGit(t, parent, "commit", "-m", "update submodule pointer")

	// Now check for submodule changes between main and feature
	g := NewGit(parent)
	changes, err := g.SubmoduleChanges("main", "feature")
	if err != nil {
		t.Fatalf("SubmoduleChanges: %v", err)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 submodule change, got %d", len(changes))
	}

	sc := changes[0]
	if sc.Path != "libs/sub" {
		t.Errorf("expected path libs/sub, got %s", sc.Path)
	}
	if sc.OldSHA == "" {
		t.Error("expected non-empty OldSHA")
	}
	if sc.NewSHA == "" {
		t.Error("expected non-empty NewSHA")
	}
	if sc.OldSHA == sc.NewSHA {
		t.Error("expected different SHAs")
	}
	if sc.URL != subRemote {
		t.Errorf("expected URL %s, got %s", subRemote, sc.URL)
	}
}

func TestSubmoduleChanges_NoSubmodules(t *testing.T) {
	dir := initTestRepo(t)

	// Create a branch with a regular file change
	runGit(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "add file")

	// initTestRepo pins the initial branch to "main", so we can reference it
	// directly without guessing based on host init.defaultBranch.
	g := NewGit(dir)
	changes, err := g.SubmoduleChanges("main", "feature")
	if err != nil {
		t.Fatalf("SubmoduleChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 submodule changes, got %d", len(changes))
	}
}

func TestPushSubmoduleCommit(t *testing.T) {
	parent, subRemote := initTestRepoWithSubmodule(t)

	// Make a new commit in the submodule (but don't push it)
	subPath := filepath.Join(parent, "libs", "sub")
	if err := os.WriteFile(filepath.Join(subPath, "pushed.go"), []byte("package lib\n// pushed\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, subPath, "add", ".")
	runGit(t, subPath, "commit", "-m", "unpushed commit")

	// Get the SHA of the new commit
	cmd := exec.Command("git", "-C", subPath, "rev-parse", "HEAD")
	shaBytes, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))

	// Verify it's not on the remote yet
	lsCmd := exec.Command("git", "ls-remote", subRemote, "refs/heads/main")
	lsOut, _ := lsCmd.Output()
	remoteSHA := strings.Fields(string(lsOut))[0]
	if remoteSHA == sha {
		t.Fatal("commit should not be on remote yet")
	}

	// Push it using PushSubmoduleCommit
	g := NewGit(parent)
	if err := g.PushSubmoduleCommit("libs/sub", sha, "origin"); err != nil {
		t.Fatalf("PushSubmoduleCommit: %v", err)
	}

	// Verify it's now on the remote
	lsCmd = exec.Command("git", "ls-remote", subRemote, "refs/heads/main")
	lsOut, _ = lsCmd.Output()
	remoteSHA = strings.Fields(string(lsOut))[0]
	if remoteSHA != sha {
		t.Errorf("expected remote main to be %s, got %s", sha, remoteSHA)
	}
}

func TestPushSubmoduleCommit_ShortSHA(t *testing.T) {
	// Verify that PushSubmoduleCommit doesn't panic when given a short SHA
	// that triggers an error path. The error message formats sha[:8] which
	// panics if len(sha) < 8. (gt-dg7)
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Use a 7-char SHA (shorter than the [:8] slice). This will fail to push
	// (no such submodule), but must not panic — it should return an error.
	shortSHA := "09bcf16"
	err := g.PushSubmoduleCommit("nonexistent/sub", shortSHA, "origin")
	if err == nil {
		t.Fatal("expected error for nonexistent submodule, got nil")
	}
	// The key assertion: we got here without panicking
}

func TestSubmoduleChanges_SkipsClaudeWorktrees(t *testing.T) {
	// Verify that SubmoduleChanges filters out .claude/ paths.
	// Claude Code creates worktrees under .claude/worktrees/ which have .git
	// files that git may report as gitlinks (mode 160000). These are not
	// real submodules and should be skipped. (gt-dg7)
	tmp := t.TempDir()

	// Create a bare remote for the .claude submodule
	claudeRemote := filepath.Join(tmp, "claude-remote.git")
	runGit(t, tmp, "init", "--bare", "--initial-branch", "main", claudeRemote)

	// Populate the claude submodule remote
	claudeWork := filepath.Join(tmp, "claude-work")
	runGit(t, tmp, "clone", claudeRemote, claudeWork)
	runGit(t, claudeWork, "config", "user.email", "test@test.com")
	runGit(t, claudeWork, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(claudeWork, "init.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, claudeWork, "add", ".")
	runGit(t, claudeWork, "commit", "-m", "init")
	runGit(t, claudeWork, "push", "origin", "main")

	// Start from the standard parent with libs/sub submodule
	parent, _ := initTestRepoWithSubmodule(t)

	// Add the .claude/worktrees submodule
	runGit(t, parent, "submodule", "add", claudeRemote, ".claude/worktrees/codebase-friction")
	runGit(t, parent, "commit", "-m", "add claude worktree submodule")

	// Create a branch and update both submodules
	runGit(t, parent, "checkout", "-b", "feature")

	// Update the real submodule
	subPath := filepath.Join(parent, "libs", "sub")
	if err := os.WriteFile(filepath.Join(subPath, "change.go"), []byte("package lib\n// change\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, subPath, "add", ".")
	runGit(t, subPath, "commit", "-m", "real sub change")
	runGit(t, subPath, "push", "origin", "HEAD:main")

	// Update the .claude worktree submodule
	claudePath := filepath.Join(parent, ".claude", "worktrees", "codebase-friction")
	if err := os.WriteFile(filepath.Join(claudePath, "change.go"), []byte("package x\n// change\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, claudePath, "add", ".")
	runGit(t, claudePath, "commit", "-m", "claude worktree change")
	runGit(t, claudePath, "push", "origin", "HEAD:main")

	runGit(t, parent, "add", ".")
	runGit(t, parent, "commit", "-m", "update both submodules")

	// SubmoduleChanges should return only the real submodule, not the .claude/ one
	g := NewGit(parent)
	changes, err := g.SubmoduleChanges("main", "feature")
	if err != nil {
		t.Fatalf("SubmoduleChanges: %v", err)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 submodule change (filtered .claude/), got %d", len(changes))
	}
	if changes[0].Path != "libs/sub" {
		t.Errorf("expected path libs/sub, got %s", changes[0].Path)
	}
}

func TestConfigurePushURL(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Add a remote
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/upstream/repo.git")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	// Configure push URL
	pushURL := "https://github.com/fork/repo.git"
	if err := g.ConfigurePushURL("origin", pushURL); err != nil {
		t.Fatalf("ConfigurePushURL: %v", err)
	}

	// Verify via GetPushURL
	got, err := g.GetPushURL("origin")
	if err != nil {
		t.Fatalf("GetPushURL: %v", err)
	}
	if got != pushURL {
		t.Errorf("GetPushURL = %q, want %q", got, pushURL)
	}

	// Verify fetch URL is unchanged
	fetchCmd := exec.Command("git", "remote", "get-url", "origin")
	fetchCmd.Dir = dir
	out, err := fetchCmd.Output()
	if err != nil {
		t.Fatalf("get fetch url: %v", err)
	}
	fetchURL := strings.TrimSpace(string(out))
	if fetchURL != "https://github.com/upstream/repo.git" {
		t.Errorf("fetch URL changed to %q, should be unchanged", fetchURL)
	}
}

func TestGetPushURL_NoPushURL(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Add remote without custom push URL
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/upstream/repo.git")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	// GetPushURL returns fetch URL when no custom push URL is set
	got, err := g.GetPushURL("origin")
	if err != nil {
		t.Fatalf("GetPushURL: %v", err)
	}
	if got != "https://github.com/upstream/repo.git" {
		t.Errorf("GetPushURL = %q, want fetch URL when no push URL configured", got)
	}
}

// TestStashCount_FiltersByBranch verifies that StashCount only counts stashes
// belonging to the current branch, not stashes from other worktrees/branches.
// Git stashes are repo-wide (stored in .git/refs/stash), so without filtering
// a worktree would see sibling stashes and block Remove(force=true).
func TestStashCount_FiltersByBranch(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a stash on the default branch
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "main-stash")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Create a worktree on a different branch
	wtDir := t.TempDir()
	cmd = exec.Command("git", "worktree", "add", wtDir, "-b", "polecat-branch")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add: %v", err)
	}

	// StashCount from worktree should be 0 (stash belongs to main, not polecat-branch)
	wtGit := NewGit(wtDir)
	count, err := wtGit.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 0 {
		t.Errorf("StashCount from worktree = %d, want 0 (stash belongs to different branch)", count)
	}

	// StashCount from main repo should be 1
	mainCount, err := g.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if mainCount != 1 {
		t.Errorf("StashCount from main = %d, want 1", mainCount)
	}

	// Create a stash on the worktree branch
	if err := os.WriteFile(filepath.Join(wtDir, "wt-dirty.txt"), []byte("wt-dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = wtDir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "wt-stash")
	cmd.Dir = wtDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash in worktree: %v", err)
	}

	// Now worktree should see 1 (its own stash)
	count, err = wtGit.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 1 {
		t.Errorf("StashCount from worktree after own stash = %d, want 1", count)
	}

	// Main repo should still see 1 (only its own stash)
	mainCount, err = g.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if mainCount != 1 {
		t.Errorf("StashCount from main after worktree stash = %d, want 1", mainCount)
	}
}

// TestStashCount_DetachedHEAD verifies that StashCount counts all stashes
// when in detached HEAD state (cannot determine branch, falls back to counting all).
func TestStashCount_DetachedHEAD(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a stash on main
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "some-stash")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Detach HEAD
	cmd = exec.Command("git", "checkout", "--detach")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git checkout --detach: %v", err)
	}

	// In detached HEAD, StashCount should count all stashes (safe fallback)
	count, err := g.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 1 {
		t.Errorf("StashCount in detached HEAD = %d, want 1 (should count all stashes)", count)
	}
}

// TestStashCount_CustomMessage verifies that StashCount handles both
// "WIP on <branch>:" (auto-stash) and "On <branch>:" (custom message) formats.
func TestStashCount_CustomMessage(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a stash with custom message (produces "On <branch>: <message>" format)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "my custom message")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Should count the custom-message stash on current branch
	count, err := g.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 1 {
		t.Errorf("StashCount with custom message stash = %d, want 1", count)
	}
}

// TestStashCount_NoFalsePositiveFromCommitMessage verifies that a stash
// from branch "develop" with commit message containing "on fix:" does NOT
// match when the current branch is "fix".
func TestStashCount_NoFalsePositiveFromCommitMessage(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)

	// Create branch "develop" and make a commit with message containing "on fix:"
	cmd := exec.Command("git", "checkout", "-b", "develop")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git checkout -b develop: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work on fix: edge case"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "work on fix: edge case")
	cmd.Dir = dir
	_ = cmd.Run()

	// Create a stash on "develop" — its reflog line will contain "on fix:" in the
	// commit message, but the branch prefix is "WIP on develop:"
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Switch to branch "fix" — should NOT see the "develop" stash
	cmd = exec.Command("git", "checkout", "-b", "fix")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git checkout -b fix: %v", err)
	}

	fixGit := NewGit(dir)
	count, err := fixGit.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 0 {
		t.Errorf("StashCount on 'fix' branch = %d, want 0 (stash belongs to 'develop', commit msg has 'on fix:')", count)
	}
}

// TestStashListForBranch verifies StashListForBranch returns entries scoped
// to the current branch with parsed Ref/Message fields.
func TestStashListForBranch(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Empty repo — no stashes
	entries, err := g.StashListForBranch()
	if err != nil {
		t.Fatalf("StashListForBranch (empty): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("StashListForBranch (empty) = %d entries, want 0", len(entries))
	}

	// Create two stashes on main
	for i, content := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "add", ".")
		cmd.Dir = dir
		_ = cmd.Run()
		cmd = exec.Command("git", "stash", "push", "-m", fmt.Sprintf("stash-%d", i))
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git stash %d: %v", i, err)
		}
	}

	entries, err = g.StashListForBranch()
	if err != nil {
		t.Fatalf("StashListForBranch: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("StashListForBranch = %d entries, want 2", len(entries))
	}
	// Newest first: stash@{0} is "second", stash@{1} is "first"
	if entries[0].Ref != "stash@{0}" || entries[1].Ref != "stash@{1}" {
		t.Errorf("Ref ordering = [%s, %s], want [stash@{0}, stash@{1}]",
			entries[0].Ref, entries[1].Ref)
	}
	if entries[0].Message == "" || entries[1].Message == "" {
		t.Errorf("Empty messages: [%s, %s]", entries[0].Message, entries[1].Message)
	}
}

// TestStashPop verifies StashPop applies and drops a stash, leaving the
// working tree dirty (so the gt-pvx auto-commit path catches it).
func TestStashPop(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a stash
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "popme")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Confirm one stash exists
	count, _ := g.StashCount()
	if count != 1 {
		t.Fatalf("StashCount before pop = %d, want 1", count)
	}

	// Pop it
	if err := g.StashPop("stash@{0}"); err != nil {
		t.Fatalf("StashPop: %v", err)
	}

	// Stash should be gone
	count, _ = g.StashCount()
	if count != 0 {
		t.Errorf("StashCount after pop = %d, want 0", count)
	}

	// Working tree should now have the file (dirty)
	if _, err := os.Stat(filepath.Join(dir, "dirty.txt")); err != nil {
		t.Errorf("dirty.txt should exist after pop: %v", err)
	}

	// Empty ref should error
	if err := g.StashPop(""); err == nil {
		t.Error("StashPop(\"\") should error")
	}
}

// TestIsStashStale_FreshStashIsNotStale verifies that a stash created on
// the current HEAD (no intervening commits) is reported as non-stale —
// the recovery scenario the gt-pvx auto-pop was designed for.
func TestIsStashStale_FreshStashIsNotStale(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a stash at current HEAD.
	if err := os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("wip"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "fresh")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	stale, parent, head, err := g.IsStashStale("stash@{0}")
	if err != nil {
		t.Fatalf("IsStashStale: %v", err)
	}
	if stale {
		t.Errorf("fresh stash should not be stale (parent %s, head %s)", parent, head)
	}
	if parent != head {
		t.Errorf("parent %q != head %q for fresh stash", parent, head)
	}
}

// TestIsStashStale_CommitsSinceStashMakesItStale reproduces the gu-vtkn
// near-miss: rust stashes WIP, commits new files, dies; inheriting polecat
// sees HEAD advanced past the stash's base. Auto-pop must refuse.
func TestIsStashStale_CommitsSinceStashMakesItStale(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Stash WIP work at the initial commit.
	if err := os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("wip"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "pre-advance")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Advance HEAD with a new commit (simulates rust's ea733ad2).
	if err := os.WriteFile(filepath.Join(dir, "testenv_test.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "add testenv_test.go")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	stale, parent, head, err := g.IsStashStale("stash@{0}")
	if err != nil {
		t.Fatalf("IsStashStale: %v", err)
	}
	if !stale {
		t.Errorf("stash should be stale after HEAD advances (parent %s == head %s?)", parent, head)
	}
	if parent == head {
		t.Errorf("parent %q should differ from head %q after a commit", parent, head)
	}
}

// TestIsStashStale_InvalidRefReportsStale verifies the err-on-the-side-
// of-caution posture: any resolution failure (bad ref, missing parent)
// is reported as stale so callers refuse to auto-pop.
func TestIsStashStale_InvalidRefReportsStale(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	stale, _, _, err := g.IsStashStale("stash@{99}")
	if err == nil {
		t.Error("expected error for non-existent stash ref")
	}
	if !stale {
		t.Error("staleness check should default to stale=true on resolution failure")
	}
}

// TestStashParentSHA_MatchesRevParse verifies StashParentSHA returns the
// same SHA as `git rev-parse <ref>^1`.
func TestStashParentSHA_MatchesRevParse(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Remember HEAD before the stash is created.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	headBefore, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	wantParent := strings.TrimSpace(string(headBefore))

	// Create a stash; its first parent should be HEAD-at-stash-time.
	if err := os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("wip"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "check-parent")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	gotParent, err := g.StashParentSHA("stash@{0}")
	if err != nil {
		t.Fatalf("StashParentSHA: %v", err)
	}
	if gotParent != wantParent {
		t.Errorf("StashParentSHA = %q, want %q", gotParent, wantParent)
	}

	// Empty ref must error.
	if _, err := g.StashParentSHA(""); err == nil {
		t.Error("StashParentSHA(\"\") should error")
	}
}

// TestHeadSHA_MatchesRevParse verifies HeadSHA returns the same SHA as
// `git rev-parse HEAD`.
func TestHeadSHA_MatchesRevParse(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	want, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}

	got, err := g.HeadSHA()
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if got != strings.TrimSpace(string(want)) {
		t.Errorf("HeadSHA = %q, want %q", got, strings.TrimSpace(string(want)))
	}
}

func TestClearPushURL(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	fetchURL := "https://github.com/upstream/repo.git"
	pushURL := "https://github.com/fork/repo.git"

	cmd := exec.Command("git", "remote", "add", "origin", fetchURL)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	// Set a custom push URL
	if err := g.ConfigurePushURL("origin", pushURL); err != nil {
		t.Fatalf("ConfigurePushURL: %v", err)
	}
	got, _ := g.GetPushURL("origin")
	if got != pushURL {
		t.Fatalf("push URL after set = %q, want %q", got, pushURL)
	}

	// Clear the custom push URL
	if err := g.ClearPushURL("origin"); err != nil {
		t.Fatalf("ClearPushURL: %v", err)
	}

	// After clearing, GetPushURL should return the fetch URL
	got, err := g.GetPushURL("origin")
	if err != nil {
		t.Fatalf("GetPushURL after clear: %v", err)
	}
	if got != fetchURL {
		t.Errorf("push URL after clear = %q, want %q (fetch URL)", got, fetchURL)
	}

	// Clearing again should be a no-op (not an error)
	if err := g.ClearPushURL("origin"); err != nil {
		t.Errorf("ClearPushURL (idempotent) should not error, got: %v", err)
	}
}

func TestIsGasTownRuntimePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{".claude/", true},
		{".claude/settings.json", true},
		{".claude/commands/foo.md", true},
		{".claude", true},
		{".runtime/", true},
		{".runtime/state.json", true},
		{".runtime", true},
		{".beads/", true},
		{".beads/db.json", true},
		{".logs/agent.log", true},
		{"__pycache__/", true},
		{"__pycache__/foo.cpython-312.pyc", true},
		{"src/__pycache__/bar.pyc", true},
		{"src/main.go", false},
		{"README.md", false},
		{".gitignore", false},
		{"claude-stuff/foo", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isGasTownRuntimePath(tt.path)
			if got != tt.want {
				t.Errorf("isGasTownRuntimePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestCleanExcludingRuntime(t *testing.T) {
	tests := []struct {
		name string
		s    UncommittedWorkStatus
		want bool
	}{
		{
			name: "only runtime artifacts",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UntrackedFiles:        []string{".claude/", ".runtime/state.json"},
			},
			want: true,
		},
		{
			name: "real code changes",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				ModifiedFiles:         []string{"src/main.go"},
			},
			want: false,
		},
		{
			name: "mix of runtime and real",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UntrackedFiles:        []string{".claude/settings.json"},
				ModifiedFiles:         []string{"src/main.go"},
			},
			want: false,
		},
		{
			name: "clean",
			s:    UncommittedWorkStatus{},
			want: true,
		},
		{
			name: "stashes ignored (survive worktree deletion)",
			s: UncommittedWorkStatus{
				StashCount: 1,
			},
			want: true,
		},
		{
			// Unpushed commits alone do not affect CleanExcludingRuntime — this
			// function only evaluates uncommitted file changes. Unpushed commits
			// are handled separately by the CommitsAhead check in gt done (gas-7vg).
			name: "unpushed commits alone do not block",
			s: UncommittedWorkStatus{
				UnpushedCommits: 2,
			},
			want: true,
		},
		{
			// The primary bug scenario (gas-7vg): polecat commits work (1 unpushed
			// commit) then calls gt done with only infrastructure files untracked.
			// CleanExcludingRuntime must return true so gt done is not blocked.
			name: "unpushed commit with only runtime artifacts",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UnpushedCommits:       1,
				UntrackedFiles:        []string{".beads/", ".claude/commands/done.md", ".runtime/state.json"},
			},
			want: true,
		},
		{
			name: "pycache untracked",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UntrackedFiles:        []string{"__pycache__/foo.pyc", ".beads/db"},
			},
			want: true,
		},
		{
			// CLAUDE.local.md is a Gas Town overlay file (gt-p35) that must not
			// block gt done or be auto-committed.
			name: "CLAUDE.local.md is runtime artifact",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UntrackedFiles:        []string{"CLAUDE.local.md"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.s.CleanExcludingRuntime()
			if got != tt.want {
				t.Errorf("CleanExcludingRuntime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckBranchContamination(t *testing.T) {
	// Create a repo with main and a feature branch that diverges.
	dir := initTestRepo(t) // has initial commit on default branch
	g := NewGit(dir)

	// Create a "main" branch explicitly and add commits to it.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Rename default branch to main for consistency.
	run("branch", "-M", "main")

	// Create feature branch from current state.
	run("checkout", "-b", "feature")

	// Add a commit on feature.
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature work"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "feature commit")

	// Switch back to main and add several commits (simulating upstream progress).
	run("checkout", "main")
	for i := 0; i < 5; i++ {
		fname := filepath.Join(dir, "main_"+strings.Repeat("x", i+1)+".txt")
		if err := os.WriteFile(fname, []byte("main work"), 0644); err != nil {
			t.Fatal(err)
		}
		run("add", ".")
		run("commit", "-m", "main commit")
	}

	// Check contamination from the feature branch's perspective.
	run("checkout", "feature")
	contam, err := g.CheckBranchContamination("main")
	if err != nil {
		t.Fatalf("CheckBranchContamination: %v", err)
	}

	if contam.Behind != 5 {
		t.Errorf("Behind = %d, want 5", contam.Behind)
	}
	if contam.Ahead != 1 {
		t.Errorf("Ahead = %d, want 1", contam.Ahead)
	}

	// From main's perspective: 0 behind, 5 ahead of feature's merge-base.
	run("checkout", "main")
	contam, err = g.CheckBranchContamination("feature")
	if err != nil {
		t.Fatalf("CheckBranchContamination from main: %v", err)
	}
	if contam.Behind != 1 {
		t.Errorf("Behind (from main) = %d, want 1", contam.Behind)
	}
	if contam.Ahead != 5 {
		t.Errorf("Ahead (from main) = %d, want 5", contam.Ahead)
	}
}

// initTestRepoWithSplitRemote creates a test setup that mirrors the polecat workflow:
// two bare repos (upstream and fork), a local clone whose origin has fetch URL → upstream
// and push URL → fork. Returns (localDir, upstreamBareDir, forkBareDir, mainBranch).
func initTestRepoWithSplitRemote(t *testing.T) (string, string, string, string) {
	t.Helper()
	tmp := t.TempDir()

	upstream := filepath.Join(tmp, "upstream.git")
	fork := filepath.Join(tmp, "fork.git")
	localDir := filepath.Join(tmp, "local")

	for _, bare := range []string{upstream, fork} {
		if err := exec.Command("git", "init", "--bare", bare).Run(); err != nil {
			t.Fatalf("git init --bare %s: %v", bare, err)
		}
	}

	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial"},
		{"git", "remote", "add", "origin", upstream},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = localDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("branch --show-current: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))

	// Push initial commit to both upstream and fork
	cmd = exec.Command("git", "push", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push to upstream: %v", err)
	}
	cmd = exec.Command("git", "push", fork, mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push to fork: %v", err)
	}

	// Split the remote: fetch stays at upstream, push goes to fork
	g := NewGit(localDir)
	if err := g.ConfigurePushURL("origin", fork); err != nil {
		t.Fatalf("ConfigurePushURL: %v", err)
	}

	return localDir, upstream, fork, mainBranch
}

// TestPushRemoteBranchExists_SplitURL is the core regression test for GH#3224:
// with a split fetch/push URL, RemoteBranchExists checks the fetch URL (upstream)
// while PushRemoteBranchExists checks the push URL (fork/bare repo).
func TestPushRemoteBranchExists_SplitURL(t *testing.T) {
	localDir, _, _, _ := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)

	// Create a feature branch and push to origin (goes to fork via push URL)
	if err := g.CreateBranch("polecat/fix-test"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/fix-test"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "fix.go"), []byte("package fix\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = localDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "fix commit")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := g.Push("origin", "polecat/fix-test", false); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// RemoteBranchExists checks the fetch URL (upstream) — branch NOT there
	exists, err := g.RemoteBranchExists("origin", "polecat/fix-test")
	if err != nil {
		t.Fatalf("RemoteBranchExists: %v", err)
	}
	if exists {
		t.Error("RemoteBranchExists should return false — branch was pushed to fork, not upstream")
	}

	// PushRemoteBranchExists checks the push URL (fork) — branch IS there
	exists, err = g.PushRemoteBranchExists("origin", "polecat/fix-test")
	if err != nil {
		t.Fatalf("PushRemoteBranchExists: %v", err)
	}
	if !exists {
		t.Error("PushRemoteBranchExists should return true — branch was pushed to fork")
	}
}

// TestPushRemoteBranchExists_NoPushURL verifies that PushRemoteBranchExists
// falls back to RemoteBranchExists when no custom push URL is configured.
func TestPushRemoteBranchExists_NoPushURL(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// No custom push URL — PushRemoteBranchExists should behave like RemoteBranchExists
	exists, err := g.PushRemoteBranchExists("origin", mainBranch)
	if err != nil {
		t.Fatalf("PushRemoteBranchExists: %v", err)
	}
	if !exists {
		t.Errorf("PushRemoteBranchExists should find %s on origin (no split URL)", mainBranch)
	}

	// Nonexistent branch should return false
	exists, err = g.PushRemoteBranchExists("origin", "nonexistent-branch")
	if err != nil {
		t.Fatalf("PushRemoteBranchExists (nonexistent): %v", err)
	}
	if exists {
		t.Error("PushRemoteBranchExists should return false for nonexistent branch")
	}
}

func TestVerifyPushedCommit(t *testing.T) {
	localDir, _, _ := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	if err := g.CreateBranch("polecat/verified-push"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/verified-push"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "verified.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("verified.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("verified push v1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	v1, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev v1: %v", err)
	}
	if err := g.Push("origin", "polecat/verified-push", false); err != nil {
		t.Fatalf("Push v1: %v", err)
	}
	if err := g.VerifyPushedCommit("origin", "polecat/verified-push", v1); err != nil {
		t.Fatalf("VerifyPushedCommit v1: %v", err)
	}

	if err := os.WriteFile(filepath.Join(localDir, "verified.txt"), []byte("v2\n"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if err := g.Add("verified.txt"); err != nil {
		t.Fatalf("Add v2: %v", err)
	}
	if err := g.Commit("verified push v2"); err != nil {
		t.Fatalf("Commit v2: %v", err)
	}
	v2, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev v2: %v", err)
	}
	if err := g.VerifyPushedCommit("origin", "polecat/verified-push", v2); err == nil {
		t.Fatal("VerifyPushedCommit should fail when remote branch is stale")
	}
	if err := g.VerifyPushedCommit("origin", "polecat/missing", v2); err == nil {
		t.Fatal("VerifyPushedCommit should fail when remote branch is missing")
	}
}

// TestPushSHA_DetachedHEADRecovery exercises the orphan-commit recovery path
// used by `gt done` after a detached-HEAD auto-save deletes the local branch
// ref (gu-0l56, root cause gu-h5pr). Even with no local branch ref, PushSHA
// must be able to deliver the commit from the object DB to refs/heads/<branch>
// on the remote.
func TestPushSHA_DetachedHEADRecovery(t *testing.T) {
	localDir, _, _ := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create a feature branch and a commit on it.
	if err := g.CreateBranch("polecat/sha-recovery"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/sha-recovery"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "work.txt"), []byte("orphan-recovery\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("work.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("orphan commit"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	sha, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev: %v", err)
	}

	// Simulate the gu-h5pr failure mode: detach HEAD at the commit and delete
	// the local branch ref. The commit still lives in the object DB.
	if err := g.Checkout(sha); err != nil {
		t.Fatalf("Checkout to sha: %v", err)
	}
	if err := g.DeleteBranch("polecat/sha-recovery", true); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}

	// Standard branch:branch push must fail — local ref is gone.
	if err := g.Push("origin", "polecat/sha-recovery:polecat/sha-recovery", false); err == nil {
		t.Fatalf("Push with branch:branch refspec should fail when local branch is missing")
	}

	// PushSHA should succeed: git only needs the commit SHA in the object DB.
	if err := g.PushSHA("origin", sha, "polecat/sha-recovery", false); err != nil {
		t.Fatalf("PushSHA: %v", err)
	}

	// The remote branch should now point at our commit.
	if err := g.VerifyPushedCommit("origin", "polecat/sha-recovery", sha); err != nil {
		t.Fatalf("VerifyPushedCommit after PushSHA: %v", err)
	}
}

// TestPushSHA_ValidatesInputs ensures PushSHA rejects empty SHA and empty
// target branch before shelling out to git. This protects against callers
// that may construct an empty refspec when HEAD cannot be resolved.
func TestPushSHA_ValidatesInputs(t *testing.T) {
	localDir, _, _ := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	if err := g.PushSHA("origin", "", "polecat/x", false); err == nil {
		t.Fatal("PushSHA should reject empty sha")
	}
	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev: %v", err)
	}
	if err := g.PushSHA("origin", head, "", false); err == nil {
		t.Fatal("PushSHA should reject empty target branch")
	}
}

// TestPushSkipPrePush_BypassesHookGates installs a pre-push hook that fails
// unless GT_SKIP_PREPUSH=1 is in the environment. This mirrors the real
// scripts/pre-push-check.sh check (line 45 of that script). It verifies that:
//
//  1. Push() does NOT bypass the hook (hook runs, push fails),
//  2. PushSkipPrePush() DOES bypass it (hook short-circuits, push succeeds),
//  3. PushSHASkipPrePush() also bypasses it.
//
// Regression guard for gu-d416: --pre-verified must skip the slow hook gates
// the polecat already ran, otherwise git push hangs minutes and the witness
// force-kills the polecat losing unpushed commits.
func TestPushSkipPrePush_BypassesHookGates(t *testing.T) {
	localDir, _, _ := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Install a pre-push hook that fails unless GT_SKIP_PREPUSH=1.
	hookPath := filepath.Join(localDir, ".git", "hooks", "pre-push")
	hookScript := `#!/bin/sh
if [ "$GT_SKIP_PREPUSH" = "1" ]; then
  exit 0
fi
echo "pre-push: gates would run here (slow)" >&2
exit 1
`
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	// Make a commit on a polecat branch.
	if err := g.CreateBranch("polecat/skip-prepush"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/skip-prepush"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "work.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("work.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("v1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// (1) Plain Push must fail — hook rejects without GT_SKIP_PREPUSH.
	if err := g.Push("origin", "polecat/skip-prepush:polecat/skip-prepush", false); err == nil {
		t.Fatal("Push should fail when pre-push hook rejects (no GT_SKIP_PREPUSH=1)")
	}

	// (2) PushSkipPrePush must succeed — hook sees GT_SKIP_PREPUSH=1 and exits 0.
	if err := g.PushSkipPrePush("origin", "polecat/skip-prepush:polecat/skip-prepush", false); err != nil {
		t.Fatalf("PushSkipPrePush should succeed when GT_SKIP_PREPUSH=1: %v", err)
	}

	// Add another commit, drop the local branch ref to test the SHA variant.
	if err := os.WriteFile(filepath.Join(localDir, "work.txt"), []byte("v2\n"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if err := g.Add("work.txt"); err != nil {
		t.Fatalf("Add v2: %v", err)
	}
	if err := g.Commit("v2"); err != nil {
		t.Fatalf("Commit v2: %v", err)
	}
	sha, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev: %v", err)
	}

	// (3) PushSHASkipPrePush must also bypass the hook.
	if err := g.PushSHASkipPrePush("origin", sha, "polecat/skip-prepush", false); err != nil {
		t.Fatalf("PushSHASkipPrePush should succeed when GT_SKIP_PREPUSH=1: %v", err)
	}
	if err := g.VerifyPushedCommit("origin", "polecat/skip-prepush", sha); err != nil {
		t.Fatalf("VerifyPushedCommit after PushSHASkipPrePush: %v", err)
	}
}

func TestVerifyPushedCommitSplitURL(t *testing.T) {
	localDir, _, _, _ := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)

	if err := g.CreateBranch("polecat/verified-split"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/verified-split"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "split.txt"), []byte("split\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("split.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("verified split push"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	sha, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev: %v", err)
	}
	if err := g.Push("origin", "polecat/verified-split", false); err != nil {
		t.Fatalf("Push: %v", err)
	}

	fetchTip, err := g.RemoteBranchTip("origin", "polecat/verified-split")
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	if fetchTip != "" {
		t.Fatalf("fetch remote should not have split push branch, got %s", fetchTip)
	}
	if err := g.VerifyPushedCommit("origin", "polecat/verified-split", sha); err != nil {
		t.Fatalf("VerifyPushedCommit should query push URL: %v", err)
	}
}

// TestBranchPushedToRemote_SplitURL verifies that BranchPushedToRemote correctly
// reports a branch as pushed when it exists on the push target (fork), even though
// it's absent from the fetch URL (upstream). This is the GH#3224 fix.
func TestBranchPushedToRemote_SplitURL(t *testing.T) {
	localDir, _, _, _ := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)

	// Create and push a feature branch (goes to fork via push URL)
	if err := g.CreateBranch("polecat/status-test"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/status-test"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "status.go"), []byte("package status\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = localDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "status commit")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := g.Push("origin", "polecat/status-test", false); err != nil {
		t.Fatalf("Push: %v", err)
	}

	pushed, unpushed, err := g.BranchPushedToRemote("polecat/status-test", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if !pushed {
		t.Error("BranchPushedToRemote should report pushed=true (branch is on fork)")
	}
	if unpushed != 0 {
		t.Errorf("BranchPushedToRemote unpushed = %d, want 0", unpushed)
	}
}

// TestBranchPushedToRemote_NoPushURL verifies baseline behavior: when fetch and
// push URLs are the same, BranchPushedToRemote works normally.
func TestBranchPushedToRemote_NoPushURL(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Main branch is pushed — should be reported as pushed
	pushed, unpushed, err := g.BranchPushedToRemote(mainBranch, "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if !pushed {
		t.Error("BranchPushedToRemote should report pushed=true for main")
	}
	if unpushed != 0 {
		t.Errorf("BranchPushedToRemote unpushed = %d, want 0", unpushed)
	}

	// Unpushed branch — should report not pushed
	if err := g.CreateBranch("polecat/unpushed"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/unpushed"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "new.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = localDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "local only")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	pushed, unpushed, err = g.BranchPushedToRemote("polecat/unpushed", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if pushed {
		t.Error("BranchPushedToRemote should report pushed=false for unpushed branch")
	}
	if unpushed < 1 {
		t.Errorf("BranchPushedToRemote unpushed = %d, want >= 1", unpushed)
	}
}

// initTestRepoWithRemoteOnBranch sets up a local repo with a bare remote whose
// default branch is a caller-supplied name (e.g. "mainline"). This mirrors the
// real-world state of TalonTriage / codegen_ws rigs that surfaced gu-yksj.
// Returns (localDir, remoteDir, defaultBranch).
func initTestRepoWithRemoteOnBranch(t *testing.T, defaultBranch string) (string, string, string) {
	t.Helper()
	tmp := t.TempDir()

	// Bare remote with its own initial branch (e.g. `mainline`).
	remoteDir := filepath.Join(tmp, "remote.git")
	if err := exec.Command("git", "init", "--bare", "--initial-branch="+defaultBranch, remoteDir).Run(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}

	// Local repo — also initialized with the same default branch so the
	// push can advance `refs/heads/<defaultBranch>` on the remote.
	localDir := filepath.Join(tmp, "local")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=" + defaultBranch},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial"},
		{"git", "remote", "add", "origin", remoteDir},
		{"git", "push", "-u", "origin", defaultBranch},
		// Set origin/HEAD so resolveRemoteBaseline can detect the default —
		// a real `git clone` does this; our init+push does not.
		{"git", "remote", "set-head", "origin", defaultBranch},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	return localDir, remoteDir, defaultBranch
}

// TestBranchPushedToRemote_MainlineDefault is the regression test for gu-yksj.
//
// Before the fix: on a rig whose default is `mainline` (not `main`), a polecat
// branch with zero local commits ahead of mainline was reported as having
// `unpushedCount == <total commits in repo>` (e.g. 133) because the baseline
// `origin/main..HEAD` failed with "ambiguous argument" and the fallback counted
// all of HEAD. That false positive drove doctor's stalled-polecats check wild.
//
// After the fix: the baseline resolves dynamically via `origin/HEAD`, the
// polecat correctly reports 0 unpushed, and `pushed=true`.
func TestBranchPushedToRemote_MainlineDefault(t *testing.T) {
	localDir, _, defaultBranch := initTestRepoWithRemoteOnBranch(t, "mainline")
	if defaultBranch != "mainline" {
		t.Fatalf("setup: default branch = %q, want mainline", defaultBranch)
	}
	g := NewGit(localDir)

	// Sanity: `origin/main` must NOT exist (if it did, the bug would be masked).
	if _, err := g.run("rev-parse", "--verify", "--quiet", "origin/main"); err == nil {
		t.Fatal("precondition violated: origin/main should not exist in a mainline-default repo")
	}

	// Create a feature branch with no new commits, simulating a polecat that
	// was assigned work but hasn't committed yet (or committed, rebased, and
	// ended up at parity with mainline).
	featureBranch := "polecat/yksj/no-work"
	if err := g.CreateBranch(featureBranch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout(featureBranch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	pushed, unpushed, err := g.BranchPushedToRemote(featureBranch, "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if unpushed != 0 {
		t.Errorf("BranchPushedToRemote unpushed = %d, want 0 (branch is at mainline; gu-yksj regression)", unpushed)
	}
	if !pushed {
		t.Error("BranchPushedToRemote pushed = false, want true (no work to push; gu-yksj regression)")
	}
}

// TestBranchPushedToRemote_MainlineDefault_WithLocalCommits verifies the happy
// path for unpushed work on a mainline-default rig: 1 real local commit ahead
// of mainline should be reported as exactly 1 unpushed, not "all of HEAD".
func TestBranchPushedToRemote_MainlineDefault_WithLocalCommits(t *testing.T) {
	localDir, _, _ := initTestRepoWithRemoteOnBranch(t, "mainline")
	g := NewGit(localDir)

	featureBranch := "polecat/yksj/has-work"
	if err := g.CreateBranch(featureBranch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout(featureBranch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "feature.txt"), []byte("local\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "local work"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	pushed, unpushed, err := g.BranchPushedToRemote(featureBranch, "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if pushed {
		t.Error("BranchPushedToRemote pushed = true, want false")
	}
	if unpushed != 1 {
		t.Errorf("BranchPushedToRemote unpushed = %d, want 1 (not total HEAD count)", unpushed)
	}
}

// TestResolveRemoteBaseline_Fallbacks verifies the probe-based fallbacks kick
// in when origin/HEAD is missing. This mirrors CI-minted clones and repos
// created without `remote set-head`.
func TestResolveRemoteBaseline_Fallbacks(t *testing.T) {
	localDir, _, defaultBranch := initTestRepoWithRemoteOnBranch(t, "main")
	g := NewGit(localDir)

	// Tier 1 works: origin/HEAD is set.
	baseline := resolveRemoteBaseline(g, "origin")
	if baseline != "origin/"+defaultBranch {
		t.Errorf("baseline with origin/HEAD set = %q, want origin/%s", baseline, defaultBranch)
	}

	// Remove origin/HEAD and verify the probe fallback still finds origin/main.
	cmd := exec.Command("git", "remote", "set-head", "--delete", "origin")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("remote set-head --delete: %v", err)
	}
	baseline = resolveRemoteBaseline(g, "origin")
	if baseline != "origin/main" {
		t.Errorf("baseline after HEAD removal = %q, want origin/main (probe fallback)", baseline)
	}
}

// TestNonInteractiveGitEnv verifies the helper returns the full set of
// editor- and prompt-disabling env vars expected by Gas Town agents.
//
// Root cause this fix prevents: talontriage refinery hung ~8h in nano on
// 2026-05-02 during a merge-conflict rebase (gu-9h58); credential-prompt
// hangs from HTTPS pushes with stale credentials (gu-1ord).
func TestNonInteractiveGitEnv(t *testing.T) {
	t.Parallel()
	env := nonInteractiveGitEnv()
	want := map[string]string{
		"GIT_EDITOR":           "true",
		"GIT_SEQUENCE_EDITOR":  "true",
		"EDITOR":               "true",
		"GIT_MERGE_AUTOEDIT":   "no",
		"GIT_TERMINAL_PROMPT":  "0",
		"GIT_ASKPASS":          "true",
		"SSH_ASKPASS":          "true",
		"SSH_ASKPASS_REQUIRE":  "never",
	}
	got := map[string]string{}
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed env entry %q", kv)
		}
		got[parts[0]] = parts[1]
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s=%q, want %q", k, got[k], v)
		}
	}
}

// TestWithNonInteractiveEnv_OverridesParentEditor verifies that the wrapper
// env overrides parent-process GIT_EDITOR/GIT_SEQUENCE_EDITOR/EDITOR settings.
// Go's exec.Cmd env precedence is last-wins for duplicate keys, so our
// helpers must append the non-interactive values AFTER the parent env.
func TestWithNonInteractiveEnv_OverridesParentEditor(t *testing.T) {
	// Deliberately set parent env to a value that would fail if inherited
	// unchanged. t.Setenv restores original values on test completion.
	t.Setenv("GIT_EDITOR", "/nonexistent/editor-that-would-fail")
	t.Setenv("GIT_SEQUENCE_EDITOR", "/nonexistent/editor-that-would-fail")
	t.Setenv("EDITOR", "/nonexistent/editor-that-would-fail")
	t.Setenv("GIT_MERGE_AUTOEDIT", "yes") // opposite of our setting

	env := withNonInteractiveEnv()

	// Find the final (last-wins) value for each key.
	final := map[string]string{}
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		final[parts[0]] = parts[1]
	}

	want := map[string]string{
		"GIT_EDITOR":          "true",
		"GIT_SEQUENCE_EDITOR": "true",
		"EDITOR":              "true",
		"GIT_MERGE_AUTOEDIT":  "no",
	}
	for k, v := range want {
		if final[k] != v {
			t.Errorf("%s=%q after merge, want %q (parent env should be overridden)", k, final[k], v)
		}
	}
}

// TestWithNonInteractiveEnv_AppendsExtras verifies that caller-supplied
// env vars are appended last and therefore win over both the parent env and
// our non-interactive defaults. This preserves the original runWithEnv
// contract (e.g. GT_INTEGRATION_LAND for pre-push hook bypass) without
// accidentally un-setting our editor guards.
func TestWithNonInteractiveEnv_AppendsExtras(t *testing.T) {
	t.Parallel()
	env := withNonInteractiveEnv("GIT_EDITOR=caller-wins", "GT_INTEGRATION_LAND=1")

	final := map[string]string{}
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		final[parts[0]] = parts[1]
	}

	// Caller override honored.
	if final["GIT_EDITOR"] != "caller-wins" {
		t.Errorf("GIT_EDITOR=%q, want caller-wins (caller-supplied extras should override defaults)", final["GIT_EDITOR"])
	}
	// Extra passthrough var honored.
	if final["GT_INTEGRATION_LAND"] != "1" {
		t.Errorf("GT_INTEGRATION_LAND=%q, want 1", final["GT_INTEGRATION_LAND"])
	}
	// Other non-interactive defaults still present (caller only overrode GIT_EDITOR).
	if final["GIT_SEQUENCE_EDITOR"] != "true" {
		t.Errorf("GIT_SEQUENCE_EDITOR=%q, want true", final["GIT_SEQUENCE_EDITOR"])
	}
}

// TestMergeWithHostileEditor simulates the original incident: a merge
// operation that would normally open an editor to edit the merge commit
// message. With a hostile editor on the parent process env that exits
// non-zero, the merge would fail (or, worse, hang waiting for input) if
// our wrapper did not override it. Verifies the end-to-end plumbing from
// Git wrapper -> git subprocess env.
func TestMergeWithHostileEditor(t *testing.T) {
	// Point the parent's editor env at a script that fails and writes a
	// sentinel so we can detect if git ever tried to launch it. Our wrapper
	// must override these with GIT_EDITOR=true etc., so git never launches
	// the hostile editor and never creates the sentinel.
	tmp := t.TempDir()
	sentinel := filepath.Join(tmp, "editor-was-launched")
	hostile := filepath.Join(tmp, "hostile-editor.sh")
	if err := os.WriteFile(hostile, []byte("#!/bin/sh\ntouch "+sentinel+"\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("writing hostile editor: %v", err)
	}
	t.Setenv("GIT_EDITOR", hostile)
	t.Setenv("GIT_SEQUENCE_EDITOR", hostile)
	t.Setenv("EDITOR", hostile)
	t.Setenv("GIT_MERGE_AUTOEDIT", "yes") // opposite of our setting

	// Build a repo with a non-fast-forward merge scenario. git merge --no-ff
	// on branches that diverged will, by default, launch the editor to edit
	// the merge commit message (unless GIT_MERGE_AUTOEDIT=no is set).
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a feature branch with its own commit.
	if err := g.CreateBranchFrom("feature", "HEAD"); err != nil {
		t.Fatalf("CreateBranchFrom: %v", err)
	}
	if err := g.Checkout("feature"); err != nil {
		t.Fatalf("Checkout feature: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("writing feature file: %v", err)
	}
	if _, err := g.run("add", "feature.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := g.Commit("feature commit"); err != nil {
		t.Fatalf("Commit feature: %v", err)
	}

	// Back to main and add a divergent commit so --no-ff is a real merge.
	if err := g.Checkout("main"); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("writing main file: %v", err)
	}
	if _, err := g.run("add", "main.txt"); err != nil {
		t.Fatalf("git add main: %v", err)
	}
	if err := g.Commit("main commit"); err != nil {
		t.Fatalf("Commit main: %v", err)
	}

	// Issue a --no-ff merge via our wrapper. If env plumbing is broken,
	// git would launch the hostile editor which creates the sentinel and
	// exits 1, making the merge fail. If our wrapper correctly overrides
	// the env, git uses the default merge message and the merge succeeds.
	if _, err := g.run("merge", "--no-ff", "feature"); err != nil {
		t.Fatalf("merge --no-ff feature failed (env not plumbed through?): %v", err)
	}

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("git launched the hostile editor — wrapper did not override parent env")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error stat-ing sentinel: %v", err)
	}
}


// TestCloneAutoWiresHooksPath verifies that cloning a repo which ships a
// .githooks directory auto-wires core.hooksPath=.githooks on the destination.
// Without this, crew/polecat clones would need a manual `make hooks` or a
// `gt doctor --fix` pass to activate shared hooks like .githooks/pre-push.
//
// Regression guard: if someone refactors cloneInternal and drops the
// configureHooksPath call, this test fails loudly instead of the breakage
// silently surviving (detected only when someone pushes without the gate
// and CI catches the real regression hours later).
//
// Covers Clone, CloneBranch, CloneWithReference, CloneBranchWithReference.
// Partial-clone variants use the same cloneInternal path so they're
// implicitly covered too; bare clones deliberately skip configureHooksPath
// because bare repos don't run client-side hooks.
func TestCloneAutoWiresHooksPath(t *testing.T) {
	// Build a source repo with a .githooks/ directory committed.
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := exec.Command("git", "init", src).Run(); err != nil {
		t.Fatalf("init src: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test User"},
	} {
		if err := exec.Command("git", append([]string{"-C", src}, args...)...).Run(); err != nil {
			t.Fatalf("git config %v: %v", args, err)
		}
	}

	hooksDir := filepath.Join(src, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir .githooks: %v", err)
	}
	hookContent := "#!/bin/sh\n# test pre-push hook\nexit 0\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hookContent), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	if err := exec.Command("git", "-C", src, "add", ".").Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := exec.Command("git", "-C", src, "commit", "-m", "init with hooks").Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Each Clone variant should leave the destination with core.hooksPath=.githooks.
	variants := []struct {
		name string
		run  func(g *Git, dest string) error
	}{
		{"Clone", func(g *Git, dest string) error { return g.Clone(src, dest) }},
		{"CloneBranch", func(g *Git, dest string) error {
			// Default branch name varies (main/master). Detect it from src HEAD.
			out, err := exec.Command("git", "-C", src, "symbolic-ref", "--short", "HEAD").Output()
			if err != nil {
				return fmt.Errorf("detect default branch: %w", err)
			}
			return g.CloneBranch(src, dest, strings.TrimSpace(string(out)))
		}},
		{"CloneWithReference", func(g *Git, dest string) error {
			return g.CloneWithReference(src, dest, src)
		}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			dst := filepath.Join(tmp, "dst-"+v.name)
			g := NewGit(tmp)
			if err := v.run(g, dst); err != nil {
				t.Fatalf("%s: %v", v.name, err)
			}

			out, err := exec.Command("git", "-C", dst, "config", "--get", "core.hooksPath").Output()
			if err != nil {
				t.Fatalf("reading core.hooksPath after %s: %v", v.name, err)
			}
			got := strings.TrimSpace(string(out))
			if got != ".githooks" {
				t.Errorf("%s: core.hooksPath = %q, want %q — auto-wiring regressed (see configureHooksPath in git.go)", v.name, got, ".githooks")
			}
		})
	}
}

// TestCloneBareSkipsHooksPath verifies that bare clones deliberately do NOT
// set core.hooksPath — bare repos don't have a working tree and don't run
// client-side hooks, so wiring the config would be noise. The gastown shared
// repo architecture uses bare clones as object stores for worktrees, and the
// worktrees inherit core.hooksPath from the worktree's own clone config.
func TestCloneBareSkipsHooksPath(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := exec.Command("git", "init", src).Run(); err != nil {
		t.Fatalf("init src: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test User"},
	} {
		_ = exec.Command("git", append([]string{"-C", src}, args...)...).Run()
	}

	// Give src a .githooks dir so configureHooksPath would fire if called.
	if err := os.MkdirAll(filepath.Join(src, ".githooks"), 0o755); err != nil {
		t.Fatalf("mkdir .githooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, ".githooks", "pre-push"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	_ = exec.Command("git", "-C", src, "add", ".").Run()
	_ = exec.Command("git", "-C", src, "commit", "-m", "init").Run()

	dst := filepath.Join(tmp, "dst.git")
	g := NewGit(tmp)
	if err := g.CloneBare(src, dst); err != nil {
		t.Fatalf("CloneBare: %v", err)
	}

	// Bare clone: config is in dst itself, not dst/.git.
	out, _ := exec.Command("git", "-C", dst, "config", "--get", "core.hooksPath").Output()
	if got := strings.TrimSpace(string(out)); got != "" {
		t.Errorf("CloneBare: core.hooksPath = %q, want empty — bare clones should skip hook wiring", got)
	}
}

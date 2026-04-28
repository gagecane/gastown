package witness

import (
	"os"
	"path/filepath"
	"testing"
)

// withStubbedWorktreeList replaces gitWorktreePathsRunner for the duration of a test.
// Tests that call this MUST NOT use t.Parallel() because the stub is a
// package-level variable and concurrent stubbing would produce flakes.
func withStubbedWorktreeList(t *testing.T, stub func(repoPath string) ([]string, error)) {
	t.Helper()
	original := gitWorktreePathsRunner
	gitWorktreePathsRunner = stub
	t.Cleanup(func() { gitWorktreePathsRunner = original })
}

func TestIsRegisteredPolecatWorktree_EmptyInputs(t *testing.T) {
	t.Parallel()
	if isRegisteredPolecatWorktree("", "rig", "nux") {
		t.Error("expected false for empty townRoot")
	}
	if isRegisteredPolecatWorktree("/tmp", "", "nux") {
		t.Error("expected false for empty rigName")
	}
	if isRegisteredPolecatWorktree("/tmp", "rig", "") {
		t.Error("expected false for empty polecatName")
	}
}

func TestIsRegisteredPolecatWorktree_NoGitError(t *testing.T) {
	withStubbedWorktreeList(t, func(repoPath string) ([]string, error) {
		return nil, os.ErrNotExist
	})

	if isRegisteredPolecatWorktree("/tmp/town", "rig", "nux") {
		t.Error("expected false when git returns an error")
	}
}

func TestIsRegisteredPolecatWorktree_NoMatch(t *testing.T) {
	withStubbedWorktreeList(t, func(repoPath string) ([]string, error) {
		return []string{
			"/tmp/town/rig/mayor/rig",
			"/tmp/town/rig/polecats/other/rig",
		}, nil
	})

	if isRegisteredPolecatWorktree("/tmp/town", "rig", "nux") {
		t.Error("expected false when no worktree matches the polecat path")
	}
}

func TestIsRegisteredPolecatWorktree_MatchesNestedPath(t *testing.T) {
	townRoot := "/tmp/town"
	rigName := "rig"
	polecatName := "nux"

	expected := filepath.Join(townRoot, rigName, "polecats", polecatName, rigName)
	withStubbedWorktreeList(t, func(repoPath string) ([]string, error) {
		return []string{expected}, nil
	})

	if !isRegisteredPolecatWorktree(townRoot, rigName, polecatName) {
		t.Errorf("expected true for registered nested worktree path %q", expected)
	}
}

func TestIsRegisteredPolecatWorktree_MatchesFlatPath(t *testing.T) {
	townRoot := "/tmp/town"
	rigName := "rig"
	polecatName := "nux"

	expected := filepath.Join(townRoot, rigName, "polecats", polecatName)
	withStubbedWorktreeList(t, func(repoPath string) ([]string, error) {
		return []string{expected}, nil
	})

	if !isRegisteredPolecatWorktree(townRoot, rigName, polecatName) {
		t.Errorf("expected true for registered flat worktree path %q", expected)
	}
}

// TestIsRegisteredPolecatWorktree_MatchesViaRealpath is the regression test
// for gu-eno2. The on-disk filesystem contains a symlink that makes the
// canonical path different from the path git records. Without realpath
// resolution, the naive string comparison would miss the match and the
// polecat would be (incorrectly) flagged as orphaned.
func TestIsRegisteredPolecatWorktree_MatchesViaRealpath(t *testing.T) {
	// Create two real directories under the test tempdir:
	//   real/rig/polecats/nux/rig    ← actual worktree location on disk
	//   link/rig                     ← symlink into real/rig
	tmp := t.TempDir()
	realRoot := filepath.Join(tmp, "real")
	realRig := filepath.Join(realRoot, "rig")
	polecatWorktree := filepath.Join(realRig, "polecats", "nux", "rig")
	if err := os.MkdirAll(polecatWorktree, 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}

	linkRoot := filepath.Join(tmp, "link")
	if err := os.MkdirAll(linkRoot, 0o755); err != nil {
		t.Fatalf("mkdir link root: %v", err)
	}
	linkRigPath := filepath.Join(linkRoot, "rig")
	if err := os.Symlink(realRig, linkRigPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	// Simulate git reporting the realpath under the real/ prefix while the
	// witness queries with the link/ prefix (different filesystem views).
	withStubbedWorktreeList(t, func(repoPath string) ([]string, error) {
		return []string{polecatWorktree}, nil
	})

	if !isRegisteredPolecatWorktree(linkRoot, "rig", "nux") {
		t.Errorf("expected true when registered path matches candidate via symlink resolution")
	}
}

// TestIsRegisteredPolecatWorktree_MatchesCandidateSymlink covers the
// inverse case: git records a path that traverses a symlink, but the
// witness's candidate path is the real path. Both sides should be
// resolved for comparison.
func TestIsRegisteredPolecatWorktree_MatchesCandidateSymlink(t *testing.T) {
	tmp := t.TempDir()
	realRig := filepath.Join(tmp, "real", "rig")
	polecatWorktree := filepath.Join(realRig, "polecats", "nux", "rig")
	if err := os.MkdirAll(polecatWorktree, 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	linkRoot := filepath.Join(tmp, "link")
	if err := os.MkdirAll(linkRoot, 0o755); err != nil {
		t.Fatalf("mkdir link root: %v", err)
	}
	linkRigPath := filepath.Join(linkRoot, "rig")
	if err := os.Symlink(realRig, linkRigPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	linkedWorktree := filepath.Join(linkRigPath, "polecats", "nux", "rig")

	// Git reports the symlinked path; the witness's candidate resolves to
	// the real path. realpath should canonicalize both sides to the same
	// directory so the match succeeds.
	withStubbedWorktreeList(t, func(repoPath string) ([]string, error) {
		return []string{linkedWorktree}, nil
	})

	realTown := filepath.Join(tmp, "real")
	if !isRegisteredPolecatWorktree(realTown, "rig", "nux") {
		t.Errorf("expected true when candidate resolves to git-reported symlink target")
	}
}

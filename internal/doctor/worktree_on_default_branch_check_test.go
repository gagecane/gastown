package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWorktreeOnDefaultBranchCheck(t *testing.T) {
	check := NewWorktreeOnDefaultBranchCheck()
	if check.Name() != "worktree-on-default-branch" {
		t.Errorf("expected name 'worktree-on-default-branch', got %q", check.Name())
	}
	if check.CanFix() {
		t.Error("expected CanFix to return false (detection-only)")
	}
}

func TestWorktreeOnDefaultBranchCheck_NoRigs(t *testing.T) {
	tmpDir := t.TempDir()
	check := NewWorktreeOnDefaultBranchCheck()
	ctx := &CheckContext{TownRoot: tmpDir}
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for empty town, got %v: %s", result.Status, result.Message)
	}
}

// setupRigWithBareRepoAndRefinery creates a town/rig layout with a bare repo
// and a refinery/rig worktree on defaultBranch. Returns townRoot, rigDir,
// bareRepo path. The rig's config.json sets default_branch=defaultBranch.
func setupRigWithBareRepoAndRefinery(t *testing.T, defaultBranch string) (string, string, string) {
	t.Helper()
	townRoot := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	configBytes := []byte(`{"name":"testrig","default_branch":"` + defaultBranch + `"}`)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), configBytes, 0644); err != nil {
		t.Fatal(err)
	}

	bareRepo := filepath.Join(rigDir, ".repo.git")
	runGit(t, "", "init", "--bare", "-b", defaultBranch, bareRepo)
	tmpInit := bareRepo + "-init"
	runGit(t, "", "init", "-b", defaultBranch, tmpInit)
	runGit(t, tmpInit, "commit", "--allow-empty", "-m", "initial")
	runGit(t, tmpInit, "remote", "add", "bare", bareRepo)
	runGit(t, tmpInit, "push", "bare", defaultBranch)
	os.RemoveAll(tmpInit)
	runGit(t, bareRepo, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)

	// Create refinery/rig worktree on the default branch (allowed/expected).
	refineryRig := filepath.Join(rigDir, "refinery", "rig")
	if err := os.MkdirAll(filepath.Dir(refineryRig), 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, bareRepo, "worktree", "add", refineryRig, defaultBranch)

	return townRoot, rigDir, bareRepo
}

func TestWorktreeOnDefaultBranchCheck_OnlyRefinery_OK(t *testing.T) {
	townRoot, _, _ := setupRigWithBareRepoAndRefinery(t, "main")
	check := NewWorktreeOnDefaultBranchCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when only refinery/rig is on default branch, got %v: %s\nDetails: %v",
			result.Status, result.Message, result.Details)
	}
}

func TestWorktreeOnDefaultBranchCheck_CrewOnMainline_Warning(t *testing.T) {
	townRoot, rigDir, bareRepo := setupRigWithBareRepoAndRefinery(t, "mainline")

	// Create a crew worktree ALSO checked out to mainline — the bug we're
	// preventing. Using --force because git won't normally let two worktrees
	// share a branch (that's the underlying git guard we're enforcing from
	// the other direction).
	crewWorktree := filepath.Join(rigDir, "crew", "someone")
	if err := os.MkdirAll(filepath.Dir(crewWorktree), 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, bareRepo, "worktree", "add", "--force", crewWorktree, "mainline")

	check := NewWorktreeOnDefaultBranchCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when crew worktree on mainline, got %v: %s",
			result.Status, result.Message)
	}
	foundCrew := false
	for _, d := range result.Details {
		if strings.Contains(d, "crew") && strings.Contains(d, "mainline") {
			foundCrew = true
			break
		}
	}
	if !foundCrew {
		t.Errorf("expected warning to mention crew worktree on mainline, got details: %v", result.Details)
	}
}

func TestWorktreeOnDefaultBranchCheck_DeaconDogOnMainline_Warning(t *testing.T) {
	// Simulates the exact incident described in gu-f35z: a deacon dog's
	// worktree ends up on mainline, blocking polecat pushes.
	townRoot, rigDir, bareRepo := setupRigWithBareRepoAndRefinery(t, "mainline")

	// The original incident path shape was /local/home/.../deacon/dogs/alpha/<rig>.
	// In practice gt puts deacon outside the rig (town/deacon/dogs/<dog>/<rig>),
	// but the check is rig-local: any worktree registered against this rig's
	// bare repo and sitting on mainline outside refinery/rig should be flagged.
	// We simulate with a sibling path that shares the rig's bare repo.
	dogWorktree := filepath.Join(rigDir, ".test-dog", "testrig")
	if err := os.MkdirAll(filepath.Dir(dogWorktree), 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, bareRepo, "worktree", "add", "--force", dogWorktree, "mainline")

	check := NewWorktreeOnDefaultBranchCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})

	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning for dog-style worktree on mainline, got %v: %s",
			result.Status, result.Message)
	}
	found := false
	for _, d := range result.Details {
		if strings.Contains(d, dogWorktree) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning to include %s in details, got: %v", dogWorktree, result.Details)
	}
	if result.FixHint == "" {
		t.Errorf("expected a FixHint describing remediation")
	}
}

// TestWorktreeOnDefaultBranchCheck_SymlinkedTownRoot_OK reproduces gu-hyfbg:
// when the town root is reached through a symlinked path (e.g. /home/<user>
// -> /local/home/<user>), `git worktree list` reports the REAL path while the
// allowlist is built from the symlinked ctx.TownRoot. A plain filepath.Clean
// comparison fails and the refinery/rig worktree is falsely flagged. With
// EvalSymlinks-based resolution the refinery is correctly excluded.
func TestWorktreeOnDefaultBranchCheck_SymlinkedTownRoot_OK(t *testing.T) {
	// realTownRoot is the on-disk path that git will report; symlinkedTownRoot
	// is the path we hand the check (mirrors $HOME being symlinked).
	realParent := t.TempDir()
	realTownRoot := filepath.Join(realParent, "real-town")
	if err := os.MkdirAll(realTownRoot, 0755); err != nil {
		t.Fatal(err)
	}
	symlinkedTownRoot := filepath.Join(realParent, "linked-town")
	if err := os.Symlink(realTownRoot, symlinkedTownRoot); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	// Build the rig + refinery worktree under the REAL path (as git sees it).
	defaultBranch := "main"
	rigName := "testrig"
	rigDir := filepath.Join(realTownRoot, rigName)
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	configBytes := []byte(`{"name":"testrig","default_branch":"` + defaultBranch + `"}`)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), configBytes, 0644); err != nil {
		t.Fatal(err)
	}
	bareRepo := filepath.Join(rigDir, ".repo.git")
	runGit(t, "", "init", "--bare", "-b", defaultBranch, bareRepo)
	tmpInit := bareRepo + "-init"
	runGit(t, "", "init", "-b", defaultBranch, tmpInit)
	runGit(t, tmpInit, "commit", "--allow-empty", "-m", "initial")
	runGit(t, tmpInit, "remote", "add", "bare", bareRepo)
	runGit(t, tmpInit, "push", "bare", defaultBranch)
	os.RemoveAll(tmpInit)
	runGit(t, bareRepo, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)
	refineryRig := filepath.Join(rigDir, "refinery", "rig")
	if err := os.MkdirAll(filepath.Dir(refineryRig), 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, bareRepo, "worktree", "add", refineryRig, defaultBranch)

	// Run the check with the SYMLINKED town root (as gt derives from $HOME).
	check := NewWorktreeOnDefaultBranchCheck()
	result := check.Run(&CheckContext{TownRoot: symlinkedTownRoot})
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for refinery/rig under symlinked town root, got %v: %s\nDetails: %v",
			result.Status, result.Message, result.Details)
	}
}

func TestIsAllowedDefaultBranchWorktree(t *testing.T) {
	rigPath := "/gt/myrig"
	tests := []struct {
		name string
		wt   string
		want bool
	}{
		{"refinery rig", "/gt/myrig/refinery/rig", true},
		{"mayor rig", "/gt/myrig/mayor/rig", true},
		{"refinery rig trailing slash", "/gt/myrig/refinery/rig/", true},
		{"crew worktree", "/gt/myrig/crew/someone", false},
		{"dog worktree", "/gt/myrig/.test-dog/myrig", false},
		{"ad-hoc worktree", "/gt/myrig/tmp/junk", false},
		{"empty", "", false},
		{"different rig", "/gt/other-rig/refinery/rig", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAllowedDefaultBranchWorktree(rigPath, tt.wt)
			if got != tt.want {
				t.Errorf("isAllowedDefaultBranchWorktree(%q, %q) = %v, want %v",
					rigPath, tt.wt, got, tt.want)
			}
		})
	}
}

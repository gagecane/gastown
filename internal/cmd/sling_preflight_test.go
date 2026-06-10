package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupPreflightRig creates a town root with one rig directory containing an
// optional config.json and an optional bare repo (.repo.git) seeded with the
// given origin/<branch> remote-tracking refs. Returns the town root.
func setupPreflightRig(t *testing.T, rigName string, writeConfig bool, defaultBranch string, remoteBranches []string) string {
	t.Helper()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	if writeConfig {
		cfg := `{"type":"rig","version":1,"name":"` + rigName + `"`
		if defaultBranch != "" {
			cfg += `,"default_branch":"` + defaultBranch + `"`
		}
		cfg += "}\n"
		if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
	}

	if remoteBranches != nil {
		bareRepoPath := filepath.Join(rigPath, ".repo.git")
		runGitPF(t, "", "init", "--bare", bareRepoPath)

		// Build a real commit in a scratch work repo, then plant it under each
		// requested refs/remotes/origin/<branch> in the bare repo. This makes
		// RefExists("refs/remotes/origin/<branch>") return true exactly for the
		// seeded branches.
		work := t.TempDir()
		runGitPF(t, work, "init")
		runGitPF(t, work, "config", "user.email", "test@test.com")
		runGitPF(t, work, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("hi"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		runGitPF(t, work, "add", ".")
		runGitPF(t, work, "commit", "-m", "init")

		// Push HEAD into each refs/remotes/origin/<branch> in the bare repo. A
		// push transfers the objects too, so RefExists(refs/remotes/origin/<b>)
		// resolves true exactly for the seeded branches.
		for _, b := range remoteBranches {
			runGitPF(t, work, "push", bareRepoPath, "HEAD:refs/remotes/origin/"+b)
		}
	}

	return townRoot
}

func runGitPF(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestPreflightRigSpawn_MissingConfig(t *testing.T) {
	townRoot := setupPreflightRig(t, "myrig", false, "", nil)

	err := preflightRigSpawn(townRoot, "myrig", "")
	if err == nil {
		t.Fatal("expected error for missing config.json, got nil")
	}
	if !strings.Contains(err.Error(), "missing config.json") {
		t.Errorf("error should mention missing config.json, got: %v", err)
	}
	if !strings.Contains(err.Error(), "gt doctor --fix") {
		t.Errorf("error should suggest gt doctor --fix, got: %v", err)
	}
}

func TestPreflightRigSpawn_BadDefaultBranch(t *testing.T) {
	// config sets default_branch=mainline, but the bare repo only has main.
	townRoot := setupPreflightRig(t, "myrig", true, "mainline", []string{"main", "develop"})

	err := preflightRigSpawn(townRoot, "myrig", "")
	if err == nil {
		t.Fatal("expected error for bad default_branch, got nil")
	}
	if !strings.Contains(err.Error(), "mainline") {
		t.Errorf("error should mention the bad branch, got: %v", err)
	}
	// Suggestions should list actual branches.
	if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "develop") {
		t.Errorf("error should suggest available branches (main, develop), got: %v", err)
	}
}

func TestPreflightRigSpawn_GoodDefaultBranch(t *testing.T) {
	townRoot := setupPreflightRig(t, "myrig", true, "main", []string{"main"})

	if err := preflightRigSpawn(townRoot, "myrig", ""); err != nil {
		t.Errorf("expected no error for valid default_branch, got: %v", err)
	}
}

func TestPreflightRigSpawn_ExplicitBaseBranchValidated(t *testing.T) {
	// config default_branch=main exists, but an explicit base branch that does
	// not exist must still be rejected (relay/--base-branch path).
	townRoot := setupPreflightRig(t, "myrig", true, "main", []string{"main"})

	err := preflightRigSpawn(townRoot, "myrig", "origin/release/v9")
	if err == nil {
		t.Fatal("expected error for nonexistent explicit base branch, got nil")
	}
	if !strings.Contains(err.Error(), "release/v9") {
		t.Errorf("error should mention the explicit base branch, got: %v", err)
	}
}

func TestPreflightRigSpawn_ExplicitBaseBranchOriginPrefixStripped(t *testing.T) {
	townRoot := setupPreflightRig(t, "myrig", true, "main", []string{"main", "develop"})

	// "origin/develop" must normalize to validate refs/remotes/origin/develop.
	if err := preflightRigSpawn(townRoot, "myrig", "origin/develop"); err != nil {
		t.Errorf("expected origin/develop to validate, got: %v", err)
	}
}

func TestPreflightRigSpawn_NoBareRepoSkipsBranchCheck(t *testing.T) {
	// config present but no bare repo yet (never-cloned rig): branch validation
	// is skipped and the spawn path's own error is the backstop.
	townRoot := setupPreflightRig(t, "myrig", true, "mainline", nil)

	if err := preflightRigSpawn(townRoot, "myrig", ""); err != nil {
		t.Errorf("expected no error when bare repo is absent, got: %v", err)
	}
}

func TestPreflightRigSpawn_EmptyRigNameNoop(t *testing.T) {
	if err := preflightRigSpawn(t.TempDir(), "", ""); err != nil {
		t.Errorf("expected no-op for empty rig name, got: %v", err)
	}
}

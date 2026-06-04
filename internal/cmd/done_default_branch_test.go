package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// writeRigConfigWithDefault writes a rig config.json with the given
// default_branch (omitted when empty) under <townRoot>/<rigName>.
func writeRigConfigWithDefault(t *testing.T, townRoot, rigName, defaultBranch string) {
	t.Helper()
	rigDir := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]interface{}{"type": "rig", "version": 1, "name": rigName}
	if defaultBranch != "" {
		cfg["default_branch"] = defaultBranch
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// initRepoWithDefaultBranch creates a local+bare repo whose default branch is
// named branch, and sets origin/HEAD so RemoteDefaultBranch() can detect it.
func initRepoWithDefaultBranch(t *testing.T, branch string) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	local := filepath.Join(root, "local")
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	run(remote, "init", "--bare", "--initial-branch", branch)
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	run(local, "init", "--initial-branch", branch)
	run(local, "config", "user.email", "test@test.com")
	run(local, "config", "user.name", "Test User")
	run(local, "commit", "--allow-empty", "-m", "init")
	run(local, "remote", "add", "origin", remote)
	run(local, "push", "-u", "origin", branch)
	run(local, "remote", "set-head", "origin", branch)
	return local
}

// TestResolveRigDefaultBranch_RigConfigWins verifies the rig config's
// default_branch is the source of truth — a "mainline"-default repo resolves to
// "mainline", not the hardcoded "main" (gu-wcb37 regression).
func TestResolveRigDefaultBranch_RigConfigWins(t *testing.T) {
	townRoot := t.TempDir()
	writeRigConfigWithDefault(t, townRoot, "casc_webapp", "mainline")

	// Git default is "main" — proves the rig config (mainline) takes priority
	// and the resolver does not silently prefer the git/hardcoded "main".
	local := initRepoWithDefaultBranch(t, "main")
	g := git.NewGit(local)

	if got := resolveRigDefaultBranch(townRoot, "casc_webapp", g); got != "mainline" {
		t.Errorf("resolveRigDefaultBranch = %q, want %q", got, "mainline")
	}
}

// TestResolveRigDefaultBranch_GitFallback verifies that when the rig config is
// missing or has an empty default_branch, the resolver falls back to the repo's
// actual default (origin/HEAD) rather than the hardcoded "main" (gu-wcb37).
func TestResolveRigDefaultBranch_GitFallback(t *testing.T) {
	townRoot := t.TempDir() // no rig config written → LoadRigConfig fails

	local := initRepoWithDefaultBranch(t, "mainline")
	g := git.NewGit(local)

	if got := resolveRigDefaultBranch(townRoot, "casc_webapp", g); got != "mainline" {
		t.Errorf("resolveRigDefaultBranch with no rig config = %q, want git default %q", got, "mainline")
	}
}

// TestResolveRigDefaultBranch_EmptyDefaultBranchUsesGit verifies that a rig
// config present but with an empty default_branch still falls through to the
// git default rather than the hardcoded "main".
func TestResolveRigDefaultBranch_EmptyDefaultBranchUsesGit(t *testing.T) {
	townRoot := t.TempDir()
	writeRigConfigWithDefault(t, townRoot, "casc_webapp", "") // empty default_branch

	local := initRepoWithDefaultBranch(t, "mainline")
	g := git.NewGit(local)

	if got := resolveRigDefaultBranch(townRoot, "casc_webapp", g); got != "mainline" {
		t.Errorf("resolveRigDefaultBranch with empty default_branch = %q, want git default %q", got, "mainline")
	}
}

// TestResolveRigDefaultBranch_FinalFallback verifies "main" is returned only as
// the last resort: no rig config and git cannot answer (nil Git).
func TestResolveRigDefaultBranch_FinalFallback(t *testing.T) {
	townRoot := t.TempDir() // no rig config

	if got := resolveRigDefaultBranch(townRoot, "casc_webapp", nil); got != "main" {
		t.Errorf("resolveRigDefaultBranch with no config and nil git = %q, want %q", got, "main")
	}
}

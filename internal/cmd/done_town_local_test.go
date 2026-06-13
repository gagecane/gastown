package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// initLocalOnlyRepo creates a git repo with a single commit and NO remote —
// the shape of the town source-of-truth repo (gs-7kjh / hq-ux3c3).
func initLocalOnlyRepo(t *testing.T, branch string) string {
	t.Helper()
	local := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = local
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch", branch)
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test User")
	run("commit", "--allow-empty", "-m", "init")
	return local
}

// TestRepoHasNoRemote_LocalOnly verifies a remote-less repo (the town
// source-of-truth) is detected as no-remote so town-tier work routes to the
// local-commit path instead of a doomed push/MR (gs-7kjh).
func TestRepoHasNoRemote_LocalOnly(t *testing.T) {
	local := initLocalOnlyRepo(t, "main")
	g := git.NewGit(local)

	if !repoHasNoRemote(g) {
		remotes, err := g.Remotes()
		t.Fatalf("repoHasNoRemote = false for a remote-less repo; want true (remotes=%v err=%v)", remotes, err)
	}
}

// TestRepoHasNoRemote_WithRemote verifies a repo that has an origin remote (the
// normal rig clone) is NOT treated as no-remote, so rig work keeps its MR path.
func TestRepoHasNoRemote_WithRemote(t *testing.T) {
	local := initRepoWithDefaultBranch(t, "main") // adds an origin remote
	g := git.NewGit(local)

	if repoHasNoRemote(g) {
		t.Fatalf("repoHasNoRemote = true for a repo with origin; want false")
	}
}

// TestRunAutoCommitSafetyNet_CommitsToDefaultBranchWhenNoRemote verifies the
// gs-7kjh durability fix: on a no-remote repo the safety net commits uncommitted
// deliverable changes to the default branch instead of refusing (which left
// town-tier formula edits uncommitted, the hq-ux3c3 durability gap).
func TestRunAutoCommitSafetyNet_CommitsToDefaultBranchWhenNoRemote(t *testing.T) {
	local := initLocalOnlyRepo(t, "main")
	// Stage an uncommitted deliverable change.
	if err := os.WriteFile(filepath.Join(local, "formula.toml"), []byte("name = \"mol-lia-pr-work\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := git.NewGit(local)

	// Mirror runDone's state: status detected as uncommitted, on the default branch.
	prevStatus := doneCleanupStatus
	doneCleanupStatus = "uncommitted"
	defer func() { doneCleanupStatus = prevStatus }()

	if err := runAutoCommitSafetyNet(g, local, "main", "main", true); err != nil {
		t.Fatalf("runAutoCommitSafetyNet returned error: %v", err)
	}

	// The change must now be committed (no remote → committing to main is the
	// intended town-repo-work path), and the status promoted off "uncommitted".
	ws, err := g.CheckUncommittedWork()
	if err != nil {
		t.Fatalf("CheckUncommittedWork: %v", err)
	}
	if ws.HasUncommittedChanges && !ws.CleanExcludingRuntime() {
		t.Fatalf("expected deliverable committed by safety net; still uncommitted: %s", ws.String())
	}
	if doneCleanupStatus == "uncommitted" {
		t.Fatalf("expected doneCleanupStatus promoted off \"uncommitted\" after auto-commit")
	}
}

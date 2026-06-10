package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/wisp"
)

// gitRunCustomer runs a git command in dir, failing the test on error.
func gitRunCustomer(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupCustomerRig builds a shared object store on `main`, a bare origin, and a
// linked worktree on a polecat branch — then marks the rig customer_repo=true so
// preservation must NEVER push gastown-internal branches to origin (gs-8p5r).
// The rig path is nested under root so the wisp config write stays inside the
// unique temp dir. Returns (manager, store, worktree, origin).
func setupCustomerRig(t *testing.T) (m *Manager, store, wt, origin string) {
	t.Helper()
	root := t.TempDir()

	origin = filepath.Join(root, "origin.git")
	gitRunCustomer(t, root, "init", "-q", "--bare", origin)

	store = filepath.Join(root, "store")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRunCustomer(t, store, "init", "-q", "-b", "main")
	gitRunCustomer(t, store, "config", "commit.gpgsign", "false")
	gitRunCustomer(t, store, "remote", "add", "origin", origin)
	if err := os.WriteFile(filepath.Join(store, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunCustomer(t, store, "add", "README.md")
	gitRunCustomer(t, store, "commit", "-q", "-m", "init")
	gitRunCustomer(t, store, "push", "-q", "origin", "main")

	wt = filepath.Join(root, "wt")
	gitRunCustomer(t, store, "worktree", "add", "-q", "-b", "polecat/thunder/lb-9999", wt, "main")

	// Nest the rig path so wisp's townRoot (filepath.Dir(rigPath)) is inside root.
	townRoot := filepath.Join(root, "town")
	rigPath := filepath.Join(townRoot, "rig")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := wisp.NewConfig(townRoot, "rig").Set("customer_repo", true); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "rig", Path: rigPath}
	m = NewManager(r, git.NewGit(root), nil)
	if m.originPreservationAllowed() {
		t.Fatal("precondition: customer_repo=true should disable origin preservation push")
	}
	return m, store, wt, origin
}

// TestPreserveUnpushedHeadCustomerRepo proves gs-8p5r: on a customer-repo rig the
// unpushed HEAD is still anchored locally (no work lost) but is NEVER pushed to
// origin, so polecat/preserved branch names don't leak into the customer's repo.
func TestPreserveUnpushedHeadCustomerRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	m, store, wt, origin := setupCustomerRig(t)

	// A detached-HEAD-style commit that exists only in the worktree.
	if err := os.WriteFile(filepath.Join(wt, "proto.txt"), []byte("prototype\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunCustomer(t, wt, "add", "proto.txt")
	gitRunCustomer(t, wt, "commit", "-q", "-m", "customer-rig prototype work")
	want := gitRunCustomer(t, wt, "rev-parse", "HEAD")

	m.preserveUnpushedHead("furiosa", wt, git.NewGit(store))

	// Local anchor still holds the work (no data loss).
	if got := gitRunCustomer(t, store, "rev-parse", "refs/preserved/furiosa/"+want[:12]); got != want {
		t.Errorf("local anchor = %s, want %s", got, want)
	}
	// CRITICAL: nothing pushed to the customer origin.
	if out := gitRunCustomer(t, origin, "for-each-ref", "refs/heads/preserved/"); out != "" {
		t.Errorf("customer origin must have NO preserved branches; got:\n%s", out)
	}
}

// TestPreserveAndClearBranchStashesCustomerRepo proves gs-8p5r for the stash
// path: the inherited stash is anchored locally and dropped (so has_stash stops
// tripping) WITHOUT pushing a preserved branch to the customer origin.
func TestPreserveAndClearBranchStashesCustomerRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	m, store, wt, origin := setupCustomerRig(t)

	if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("prior occupant WIP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunCustomer(t, wt, "add", "wip.txt")
	gitRunCustomer(t, wt, "stash", "push", "-m", "thunder WIP")

	wtGit := git.NewGit(wt)
	stashSHA := gitRunCustomer(t, wt, "rev-parse", "stash@{0}")

	cleared := m.preserveAndClearBranchStashes("thunder", wt, git.NewGit(store))
	if cleared != 1 {
		t.Fatalf("preserveAndClearBranchStashes cleared %d, want 1", cleared)
	}

	// Stash dropped from the shared reflog → has_stash no longer trips.
	if count, _ := wtGit.StashCount(); count != 0 {
		t.Errorf("StashCount after clear = %d, want 0", count)
	}
	// Local anchor still holds the work.
	short := stashSHA[:12]
	if got := gitRunCustomer(t, store, "rev-parse", "refs/preserved/thunder/stash-"+short); got != stashSHA {
		t.Errorf("local anchor = %s, want %s", got, stashSHA)
	}
	// CRITICAL: nothing pushed to the customer origin.
	if out := gitRunCustomer(t, origin, "for-each-ref", "refs/heads/preserved/"); out != "" {
		t.Errorf("customer origin must have NO preserved branches; got:\n%s", out)
	}
}

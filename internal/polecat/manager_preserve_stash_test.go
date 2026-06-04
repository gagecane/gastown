package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// TestPreserveAndClearBranchStashes proves the gu-1vtw0 worktree-reuse invariant:
// a stash belonging to the worktree's current branch is anchored to a durable
// preserved ref (bare repo + origin) AND dropped from the shared reflog, so the
// has_stash recovery predicate no longer trips and the next occupant cannot
// silently inherit the prior occupant's WIP — with zero data loss.
func TestPreserveAndClearBranchStashes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	gitRun := func(t *testing.T, dir string, args ...string) string {
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

	// setup builds a shared object store on `main`, a bare origin, and a linked
	// worktree checked out on a polecat branch. Returns (manager, store, wt, origin).
	setup := func(t *testing.T) (m *Manager, store, wt, origin string) {
		t.Helper()
		root := t.TempDir()
		origin = filepath.Join(root, "origin.git")
		gitRun(t, root, "init", "-q", "--bare", origin)

		store = filepath.Join(root, "store")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, store, "init", "-q", "-b", "main")
		gitRun(t, store, "config", "commit.gpgsign", "false")
		gitRun(t, store, "remote", "add", "origin", origin)
		if err := os.WriteFile(filepath.Join(store, "README.md"), []byte("hi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, store, "add", "README.md")
		gitRun(t, store, "commit", "-q", "-m", "init")
		gitRun(t, store, "push", "-q", "origin", "main")

		wt = filepath.Join(root, "wt")
		// Create the linked worktree directly on a polecat branch — this is the
		// "prior occupant". -b avoids fighting `store` for the `main` checkout.
		gitRun(t, store, "worktree", "add", "-q", "-b", "polecat/thunder/gu-10nch", wt, "main")

		r := &rig.Rig{Name: "rig", Path: root}
		return NewManager(r, git.NewGit(root), nil), store, wt, origin
	}

	t.Run("branch stash is preserved to bare repo AND origin, then dropped", func(t *testing.T) {
		m, store, wt, origin := setup(t)

		// Prior occupant stashes uncommitted WIP, then dies.
		if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("prior occupant WIP\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, wt, "add", "wip.txt")
		gitRun(t, wt, "stash", "push", "-m", "thunder WIP")

		wtGit := git.NewGit(wt)
		stashSHA := strings.TrimSpace(gitRun(t, wt, "rev-parse", "stash@{0}"))
		if count, _ := wtGit.StashCount(); count != 1 {
			t.Fatalf("precondition: StashCount = %d, want 1", count)
		}

		cleared := m.preserveAndClearBranchStashes("thunder", wt, git.NewGit(store))
		if cleared != 1 {
			t.Fatalf("preserveAndClearBranchStashes cleared %d, want 1", cleared)
		}

		// Stash gone from the shared reflog → has_stash no longer trips.
		if count, _ := wtGit.StashCount(); count != 0 {
			t.Errorf("StashCount after clear = %d, want 0", count)
		}

		// Local anchor in the bare repo.
		short := stashSHA[:12]
		if got := gitRun(t, store, "rev-parse", "refs/preserved/thunder/stash-"+short); got != stashSHA {
			t.Errorf("local anchor = %s, want %s", got, stashSHA)
		}
		// Durable origin preservation ref.
		originRef := "refs/heads/preserved/thunder/stash-" + short
		if got := gitRun(t, origin, "rev-parse", originRef); got != stashSHA {
			t.Errorf("origin preservation ref %s = %s, want %s", originRef, got, stashSHA)
		}

		// Working tree must be untouched by the preserve (drop, not pop).
		if _, err := os.Stat(filepath.Join(wt, "wip.txt")); !os.IsNotExist(err) {
			t.Errorf("wip.txt should NOT be in working tree after preserve-and-clear")
		}
	})

	t.Run("clean worktree with no stash is a no-op", func(t *testing.T) {
		m, store, wt, _ := setup(t)
		if cleared := m.preserveAndClearBranchStashes("thunder", wt, git.NewGit(store)); cleared != 0 {
			t.Errorf("cleared = %d on clean worktree, want 0", cleared)
		}
		if out := gitRun(t, store, "for-each-ref", "refs/preserved/"); out != "" {
			t.Errorf("no preserved ref expected for clean worktree; got:\n%s", out)
		}
	})
}

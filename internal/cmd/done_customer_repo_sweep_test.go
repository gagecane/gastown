package cmd

// Tests for sweepCustomerRepoLeakedBranches (gs-7s52).
//
// On customer_repo=true rigs, origin is the customer's real remote, so every
// polecat/<agent>/<bead> branch pushed to open a PR leaks gastown-internal
// names into the customer's repo. At gt-done teardown the sweep removes THIS
// polecat's own branches from origin once their work has landed — but never an
// open-PR / unlanded branch (the PR flow stays intact) and never a peer
// polecat's branch (self-scoped, matching the proxy push-gate).
//
// gh is unavailable for a local-path origin, so FindMergedPRCommit/HasOpenPR
// fail-fast; these tests exercise the git-cherry landing signal, which is the
// squash/rebase-safe fallback that must hold without any VCS provider.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/wisp"
)

// setupSweepRig builds a bare origin and a worktree on main, then returns the
// worktree path. Caller seeds the polecat branches it needs.
func setupSweepRig(t *testing.T) (wt, bare string) {
	t.Helper()
	bare = t.TempDir()
	runGitEnv(t, bare, "init", "--bare", "-b", "main")

	wt = t.TempDir()
	runGitEnv(t, wt, "init", "-b", "main")
	runGitEnv(t, wt, "config", "user.email", "test@example.com")
	runGitEnv(t, wt, "config", "user.name", "Test")
	runGitEnv(t, wt, "remote", "add", "origin", bare)

	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "README.md")
	runGitEnv(t, wt, "commit", "-m", "seed")
	runGitEnv(t, wt, "push", "origin", "main")
	return wt, bare
}

// pushLandedBranch creates a branch off main, pushes it, then fast-forwards main
// over it and pushes main — so the branch tip is already an ancestor of
// origin/main (its work has landed).
func pushLandedBranch(t *testing.T, wt, branch, file string) {
	t.Helper()
	runGitEnv(t, wt, "checkout", "-b", branch, "main")
	if err := os.WriteFile(filepath.Join(wt, file), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", file)
	runGitEnv(t, wt, "commit", "-m", "feat: "+file)
	runGitEnv(t, wt, "push", "origin", branch)
	runGitEnv(t, wt, "checkout", "main")
	runGitEnv(t, wt, "merge", "--ff-only", branch)
	runGitEnv(t, wt, "push", "origin", "main")
}

// pushUnlandedBranch creates a branch with a unique commit not on main and
// pushes it, leaving main behind — its work has NOT landed.
func pushUnlandedBranch(t *testing.T, wt, branch, file string) {
	t.Helper()
	runGitEnv(t, wt, "checkout", "-b", branch, "main")
	if err := os.WriteFile(filepath.Join(wt, file), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", file)
	runGitEnv(t, wt, "commit", "-m", "wip: "+file)
	runGitEnv(t, wt, "push", "origin", branch)
	runGitEnv(t, wt, "checkout", "main")
}

func remoteHasBranch(t *testing.T, wt, branch string) bool {
	t.Helper()
	out := runGitEnv(t, wt, "ls-remote", "--heads", "origin", branch)
	return strings.Contains(out, "refs/heads/"+branch)
}

func setCustomerRepo(t *testing.T, townRoot, rigName string, v bool) {
	t.Helper()
	if err := wisp.NewConfig(townRoot, rigName).Set("customer_repo", v); err != nil {
		t.Fatal(err)
	}
}

// TestSweepCustomerRepoLeakedBranches_DeletesOnlyOwnLanded proves the core
// contract: on a customer_repo rig the sweep deletes the polecat's own LANDED
// branch from origin, while leaving (a) its own unlanded branch, (b) the branch
// it is currently completing, and (c) a peer polecat's landed branch.
func TestSweepCustomerRepoLeakedBranches_DeletesOnlyOwnLanded(t *testing.T) {
	wt, _ := setupSweepRig(t)
	townRoot := t.TempDir()
	rigName := "testrig"
	setCustomerRepo(t, townRoot, rigName, true)

	ownLanded := "polecat/slit/gs-1--aaa"
	ownUnlanded := "polecat/slit/gs-2--bbb"
	peerLanded := "polecat/dust/gs-3--ccc"
	current := "polecat/slit/gs-cur--ddd"

	pushLandedBranch(t, wt, ownLanded, "a.txt")
	pushUnlandedBranch(t, wt, ownUnlanded, "b.txt")
	pushLandedBranch(t, wt, peerLanded, "c.txt")
	pushUnlandedBranch(t, wt, current, "d.txt")

	sweepCustomerRepoLeakedBranches(teardownParams{
		g:             git.NewGit(wt),
		townRoot:      townRoot,
		rigName:       rigName,
		polecatName:   "slit",
		branch:        current,
		defaultBranch: "main",
		cwdAvailable:  true,
	})

	if remoteHasBranch(t, wt, ownLanded) {
		t.Errorf("own landed branch %q should have been deleted from customer origin", ownLanded)
	}
	if !remoteHasBranch(t, wt, ownUnlanded) {
		t.Errorf("own unlanded branch %q must NOT be deleted (PR may still be open)", ownUnlanded)
	}
	if !remoteHasBranch(t, wt, peerLanded) {
		t.Errorf("peer polecat branch %q must NOT be touched (self-scoped)", peerLanded)
	}
	if !remoteHasBranch(t, wt, current) {
		t.Errorf("branch being completed %q must NOT be deleted", current)
	}
}

// TestSweepCustomerRepoLeakedBranches_GatedOffByDefault proves the sweep is a
// no-op unless customer_repo=true — Gas Town's own rigs keep every branch on
// origin (their box-loss / audit reliance is unchanged).
func TestSweepCustomerRepoLeakedBranches_GatedOffByDefault(t *testing.T) {
	wt, _ := setupSweepRig(t)
	townRoot := t.TempDir()
	rigName := "testrig"
	// customer_repo not set → defaults to false.

	ownLanded := "polecat/slit/gs-1--aaa"
	pushLandedBranch(t, wt, ownLanded, "a.txt")

	sweepCustomerRepoLeakedBranches(teardownParams{
		g:             git.NewGit(wt),
		townRoot:      townRoot,
		rigName:       rigName,
		polecatName:   "slit",
		branch:        "polecat/slit/gs-cur--ddd",
		defaultBranch: "main",
		cwdAvailable:  true,
	})

	if !remoteHasBranch(t, wt, ownLanded) {
		t.Errorf("non-customer rig must not delete %q from origin (gate off)", ownLanded)
	}
}

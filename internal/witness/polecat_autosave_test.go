// Regression tests for polecatHasAutoSaveCommits (gu-1ufs3).
//
// The original implementation contained a `return false, nil` *inside* the
// remotes loop, so only remotes[0] was ever inspected. On a multi-remote rig
// (origin + upstream), if the auto-save marker was only visible against a
// non-first remote's range, the function returned false. classifyPolecatMergeState
// then mislabeled the polecat MergeCheckMerged instead of MergeCheckAutoSave and
// the zombie-restart path nuked unpushed WIP.
package witness

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// TestPolecatHasAutoSaveCommits_MarkerOnlyOnSecondRemote builds a repo with two
// remotes where the auto-save marker is visible only against remotes[1]'s range,
// and asserts the function still detects it.
func TestPolecatHasAutoSaveCommits_MarkerOnlyOnSecondRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	work := root + "/work"
	bareOrigin := root + "/origin.git"
	bareUpstream := root + "/upstream.git"

	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}

	// Bare remotes.
	mustGit(root, "init", "--bare", "-b", "main", bareOrigin)
	mustGit(root, "init", "--bare", "-b", "main", bareUpstream)

	// Working repo with a seed commit.
	mustGit(root, "init", "-b", "main", work)
	mustGit(work, "config", "user.email", "test@test")
	mustGit(work, "config", "user.name", "Test")
	mustGit(work, "commit", "--allow-empty", "-m", "seed")
	mustGit(work, "remote", "add", "origin", bareOrigin)
	mustGit(work, "remote", "add", "upstream", bareUpstream)

	// upstream/main = seed (does NOT contain the auto-save commit).
	mustGit(work, "push", "upstream", "main")

	// Auto-save commit on top of seed.
	mustGit(work, "commit", "--allow-empty", "-m", "gt-pvx: auto-save uncommitted changes")

	// origin/main = seed + auto-save (so origin/main..HEAD is empty → no marker
	// visible against remotes[0]).
	mustGit(work, "push", "origin", "main")

	// Refresh remote-tracking refs.
	mustGit(work, "fetch", "origin")
	mustGit(work, "fetch", "upstream")

	g := git.NewGit(work)

	// origin first: origin/main..HEAD is empty (no marker). The fix must keep
	// scanning to upstream, whose range still contains the auto-save commit.
	got, err := polecatHasAutoSaveCommits(g, []string{"origin", "upstream"}, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("expected auto-save marker detected against remotes[1], got false " +
			"(premature return inside the remotes loop?)")
	}
}

// TestPolecatHasAutoSaveCommits_NoMarkerAnyRemote confirms a clean "no marker"
// result (queried successfully, marker absent) returns (false, nil).
func TestPolecatHasAutoSaveCommits_NoMarkerAnyRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	work := root + "/work"
	bareOrigin := root + "/origin.git"

	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}

	mustGit(root, "init", "--bare", "-b", "main", bareOrigin)
	mustGit(root, "init", "-b", "main", work)
	mustGit(work, "config", "user.email", "test@test")
	mustGit(work, "config", "user.name", "Test")
	mustGit(work, "commit", "--allow-empty", "-m", "seed")
	mustGit(work, "remote", "add", "origin", bareOrigin)
	mustGit(work, "push", "origin", "main")
	mustGit(work, "commit", "--allow-empty", "-m", "ordinary feature work")
	mustGit(work, "fetch", "origin")

	g := git.NewGit(work)
	got, err := polecatHasAutoSaveCommits(g, []string{"origin"}, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("expected no auto-save marker, got true")
	}
}

// TestPolecatHasAutoSaveCommits_NoQueryableRemote confirms the function returns
// an error when no remote range could be queried (every LogOneline failed).
func TestPolecatHasAutoSaveCommits_NoQueryableRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	work := root + "/work"

	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}

	mustGit(root, "init", "-b", "main", work)
	mustGit(work, "config", "user.email", "test@test")
	mustGit(work, "config", "user.name", "Test")
	mustGit(work, "commit", "--allow-empty", "-m", "seed")

	g := git.NewGit(work)
	// No such remote-tracking ref → every LogOneline errors → queried stays false.
	got, err := polecatHasAutoSaveCommits(g, []string{"nope"}, "main")
	if err == nil {
		t.Fatal("expected error when no remote was queryable, got nil")
	}
	if got {
		t.Fatal("expected false when no remote was queryable, got true")
	}
}

package cmd

// Tests for done_phases.go (gu-y7ouk). These exist primarily to demonstrate
// that the extraction made the helpers reachable from a unit test — runDone
// itself is too entangled with workspace / beads / session globals to reach
// without a heavy harness, but the extracted phases are pure functions over
// a *git.Git, so we can drive them on a real bare+worktree pair.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// initRepoForPhases mirrors setupBareWithBranch (done_stranded_push_test.go)
// but returns just the worktree on a feature branch, with a clean working
// tree. Tests can then dirty the tree to drive the helpers.
func initRepoForPhases(t *testing.T) (string, string) {
	t.Helper()
	bare := t.TempDir()
	for _, args := range [][]string{{"init", "--bare", "-b", "main"}} {
		c := exec.Command("git", args...)
		c.Dir = bare
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v in bare: %v\n%s", args, err, out)
		}
	}

	wt := t.TempDir()
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	runIn := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = gitEnv
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	runIn(wt, "init", "-b", "main")
	runIn(wt, "config", "user.email", "test@example.com")
	runIn(wt, "config", "user.name", "Test")
	runIn(wt, "remote", "add", "origin", bare)

	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(wt, "add", "README.md")
	runIn(wt, "commit", "-m", "seed")
	runIn(wt, "push", "origin", "main")

	branch := "polecat/gu-y7ouk-phases"
	runIn(wt, "checkout", "-b", branch)
	return wt, branch
}

// TestDetectCleanupStatus_NoCwd verifies the worktree-deleted fallback path.
func TestDetectCleanupStatus_NoCwd(t *testing.T) {
	got := detectCleanupStatus(nil, "polecat/whatever", false)
	if got != "unknown" {
		t.Errorf("detectCleanupStatus(cwdAvailable=false) = %q, want %q", got, "unknown")
	}
}

// TestDetectCleanupStatus_Clean verifies that a clean, pushed branch reports
// "clean" — the polecat-self-clean signal the witness uses to decide it's
// safe to nuke the sandbox immediately rather than asking the agent to push.
func TestDetectCleanupStatus_Clean(t *testing.T) {
	wt, branch := initRepoForPhases(t)
	g := git.NewGit(wt)

	// Push branch to origin so BranchPushedToRemote sees no unpushed commits.
	c := exec.Command("git", "push", "origin", branch)
	c.Dir = wt
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}

	got := detectCleanupStatus(g, branch, true)
	if got != "clean" {
		t.Errorf("detectCleanupStatus(clean+pushed) = %q, want %q", got, "clean")
	}
}

// TestDetectCleanupStatus_Uncommitted verifies that a dirty working tree
// reports "uncommitted" — the signal that gates the gt-pvx safety-net commit
// in runDone.
func TestDetectCleanupStatus_Uncommitted(t *testing.T) {
	wt, branch := initRepoForPhases(t)
	g := git.NewGit(wt)

	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectCleanupStatus(g, branch, true)
	if got != "uncommitted" {
		t.Errorf("detectCleanupStatus(dirty) = %q, want %q", got, "uncommitted")
	}
}

// TestDetectCleanupStatus_Unpushed verifies that a clean branch with commits
// not yet on origin reports "unpushed" — the signal that gt done must push
// before notifying witness.
func TestDetectCleanupStatus_Unpushed(t *testing.T) {
	wt, branch := initRepoForPhases(t)
	g := git.NewGit(wt)

	// Commit something so the branch has content not on origin.
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "feature.txt"},
		{"commit", "-m", "feat: phases test"},
	} {
		c := exec.Command("git", args...)
		c.Dir = wt
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@e.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@e.com",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	got := detectCleanupStatus(g, branch, true)
	if got != "unpushed" {
		t.Errorf("detectCleanupStatus(committed-not-pushed) = %q, want %q", got, "unpushed")
	}
}

// TestRunStashAutoPop_Passthrough verifies that runStashAutoPop is a no-op
// for non-stash statuses — runDone calls it unconditionally, so this is the
// hot path for nearly every polecat session.
func TestRunStashAutoPop_Passthrough(t *testing.T) {
	wt, _ := initRepoForPhases(t)
	g := git.NewGit(wt)

	for _, in := range []string{"", "clean", "uncommitted", "unpushed", "unknown"} {
		got := runStashAutoPop(g, in)
		if got != in {
			t.Errorf("runStashAutoPop(%q) = %q, want %q (non-stash status must pass through unchanged)", in, got, in)
		}
	}
}

// TestRunStashAutoPop_NoStashes verifies the "status=stash but list is
// empty" race path. This shouldn't happen in normal flow (the upstream
// detect-status check sets stash only when there's content), but if a
// concurrent process drops the stash between detect and pop, we should
// return the input unchanged rather than spuriously rewriting status.
func TestRunStashAutoPop_NoStashes(t *testing.T) {
	wt, _ := initRepoForPhases(t)
	g := git.NewGit(wt)

	got := runStashAutoPop(g, "stash")
	if got != "stash" {
		t.Errorf("runStashAutoPop(stash, no-entries) = %q, want %q", got, "stash")
	}
}

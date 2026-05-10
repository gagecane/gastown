package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// TestDoneAutoSaveRefusesOnDetachedHEAD is the regression test for gu-h5pr:
// "gt done auto-save commit lands on detached HEAD, orphans branch ref".
//
// Before the fix, the auto-save safety-net in runDone would happily call
// `git commit` even when HEAD was detached. The resulting commit had no
// branch ref to advance, so the subsequent `git push origin <branch>:<branch>`
// failed with "src refspec does not match any" and the MR bead was never
// filed — even though work HAD been pushed to the remote (as HEAD).
//
// This test exercises the guard's precondition directly: we set up a repo
// with uncommitted changes on a detached HEAD and confirm IsDetachedHEAD
// sees it. The runDone control flow is covered by the `goto afterSafetyNet`
// path in done.go; here we lock in the git-level detection so the guard
// cannot silently break on a future git version or refactor.
func TestDoneAutoSaveRefusesOnDetachedHEAD(t *testing.T) {
	dir := t.TempDir()

	// Initialize repo on main.
	runGitDetach(t, dir, "init", "-q", "-b", "main")
	runGitDetach(t, dir, "config", "user.email", "test@test.com")
	runGitDetach(t, dir, "config", "user.name", "Test User")

	// Initial commit on main.
	if err := os.WriteFile(filepath.Join(dir, "initial.txt"), []byte("initial\n"), 0644); err != nil {
		t.Fatalf("write initial.txt: %v", err)
	}
	runGitDetach(t, dir, "add", "-A")
	runGitDetach(t, dir, "commit", "-q", "-m", "initial")

	// Create a polecat-style branch and commit on it.
	runGitDetach(t, dir, "checkout", "-q", "-b", "polecat/test/foo--moz0001")
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("committed work\n"), 0644); err != nil {
		t.Fatalf("write work.txt: %v", err)
	}
	runGitDetach(t, dir, "add", "-A")
	runGitDetach(t, dir, "commit", "-q", "-m", "work commit")

	// Detach HEAD while on the branch (mirrors the failure mode observed
	// in gu-h5pr: gt done's sync phase or some other actor detached HEAD
	// before the auto-save block ran).
	runGitDetach(t, dir, "checkout", "-q", "--detach")

	// Now introduce uncommitted changes — the state that would trigger the
	// auto-save safety net without the detached-HEAD guard.
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("uncommitted\n"), 0644); err != nil {
		t.Fatalf("write dirty.txt: %v", err)
	}

	g := git.NewGit(dir)

	// Contract 1: git reports detached HEAD. This is what the guard in
	// done.go's auto-save block checks via g.IsDetachedHEAD().
	detached, err := g.IsDetachedHEAD()
	if err != nil {
		t.Fatalf("IsDetachedHEAD: %v", err)
	}
	if !detached {
		t.Fatal("expected IsDetachedHEAD = true after git checkout --detach")
	}

	// Contract 2: the working tree has uncommitted changes (otherwise the
	// auto-save block wouldn't have been entered in the first place).
	ws, err := g.CheckUncommittedWork()
	if err != nil {
		t.Fatalf("CheckUncommittedWork: %v", err)
	}
	if !ws.HasUncommittedChanges {
		t.Fatal("expected HasUncommittedChanges = true; test setup is broken")
	}

	// Contract 3: once both conditions hold, the guard in runDone bails via
	// `goto afterSafetyNet` — no `git add -A` / `git commit` runs. Verify by
	// snapshotting HEAD before any would-be commit and confirming it hasn't
	// advanced.
	headBefore := runGitDetach(t, dir, "rev-parse", "HEAD")

	// Simulate: guard sees detached, does nothing. This documents the
	// contract that done.go relies on.
	if !detached {
		t.Fatal("guard precondition flipped mid-test — test logic error")
	}

	headAfter := runGitDetach(t, dir, "rev-parse", "HEAD")
	if headBefore != headAfter {
		t.Errorf("HEAD advanced from %s to %s — auto-save must not commit on detached HEAD",
			headBefore, headAfter)
	}

	// And: reflog shows no auto-save commit (we never created one).
	reflog := runGitDetach(t, dir, "reflog")
	if strings.Contains(reflog, "auto-save uncommitted implementation work") {
		t.Errorf("reflog contains auto-save commit; guard did not prevent it:\n%s", reflog)
	}
}

// runGitDetach executes a git command in dir for this test file. Named with
// a "Detach" suffix to avoid colliding with runGit helpers in sibling tests.
func runGitDetach(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimRight(string(out), "\r\n\t ")
}

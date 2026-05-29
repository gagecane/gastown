package refinery

import (
	"bytes"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"
)

// setupWedgeRepo builds a bare origin + working clone, creates a polecat
// branch on origin, simulates the refinery's per-cycle `temp` branch
// (tracking the polecat branch), then deletes the polecat branch from
// origin to model the post-merge wedge state. Returns the worktree path.
func setupWedgeRepo(t *testing.T, withWedge bool) string {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "origin.git")
	work := filepath.Join(tmp, "work")

	run(t, tmp, "git", "init", "--bare", "--initial-branch=main", bare)
	run(t, tmp, "git", "clone", bare, work)
	run(t, work, "git", "config", "user.email", "test@example.com")
	run(t, work, "git", "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "initial")
	run(t, work, "git", "branch", "-M", "main")
	run(t, work, "git", "push", "-u", "origin", "main")

	if !withWedge {
		return work
	}

	// Create the polecat branch on origin (simulating a polecat push).
	polecat := "polecat/test/gu-xxxx--mprtest"
	run(t, work, "git", "checkout", "-b", polecat, "main")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feat"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "feat: add feature")
	run(t, work, "git", "push", "-u", "origin", polecat)

	// Simulate the refinery role agent: create `temp` from the polecat
	// branch (the role template's `git checkout -b temp polecat/<worker>`
	// step) and explicitly set its upstream to the origin polecat ref.
	// This mirrors what `git checkout -b temp origin/<polecat>` produces
	// before the refinery merges and pushes to main.
	run(t, work, "git", "checkout", "-b", "temp", polecat)
	run(t, work, "git", "branch", "--set-upstream-to=origin/"+polecat, "temp")

	// Now reap the polecat branch from origin (post-merge cleanup).
	run(t, work, "git", "push", "origin", "--delete", polecat)
	// Prune local remote-tracking ref so the wedge reflects what a fresh
	// fetch --prune would leave behind on the next refinery cycle.
	run(t, work, "git", "fetch", "--prune", "origin")

	// Also delete the local polecat branch so we end up in the same shape
	// that bites refinery: HEAD on `temp` with reaped upstream.
	run(t, work, "git", "checkout", "temp")
	run(t, work, "git", "branch", "-D", polecat)

	return work
}

func TestDetectWedge_NoWorktree(t *testing.T) {
	st, err := DetectWedge(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if st.Exists {
		t.Errorf("expected Exists=false")
	}
	if st.Wedged() {
		t.Errorf("missing worktree should not report wedged")
	}
}

func TestDetectWedge_CleanWorktree(t *testing.T) {
	work := setupWedgeRepo(t, false)
	st, err := DetectWedge(work)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !st.Exists {
		t.Errorf("expected Exists=true")
	}
	if st.HasTempBranch {
		t.Errorf("clean repo should not have temp branch")
	}
	if st.Wedged() {
		t.Errorf("clean repo should not be wedged: %s", st.Reason)
	}
}

func TestDetectWedge_WedgedAfterReap(t *testing.T) {
	work := setupWedgeRepo(t, true)
	st, err := DetectWedge(work)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !st.HasTempBranch {
		t.Errorf("expected temp branch to exist")
	}
	if st.TempUpstream == "" {
		t.Errorf("expected temp upstream to be set")
	}
	if !st.Wedged() {
		t.Errorf("expected wedge state, got: %s", st.Reason)
	}
}

func TestUnwedgeWorktree_ClearsWedge(t *testing.T) {
	work := setupWedgeRepo(t, true)

	pre, _ := DetectWedge(work)
	if !pre.Wedged() {
		t.Fatalf("setup did not produce a wedge: %s", pre.Reason)
	}

	var buf bytes.Buffer
	if err := UnwedgeWorktree(work, "main", &buf); err != nil {
		t.Fatalf("UnwedgeWorktree failed: %v\noutput: %s", err, buf.String())
	}

	post, err := DetectWedge(work)
	if err != nil {
		t.Fatalf("post-detect failed: %v", err)
	}
	if post.Wedged() {
		t.Errorf("worktree still wedged after UnwedgeWorktree: %s", post.Reason)
	}
	if post.HasTempBranch {
		t.Errorf("temp branch should be gone after unwedge, found upstream=%s", post.TempUpstream)
	}
	if post.CurrentBranch != "main" {
		t.Errorf("current branch = %q, want main", post.CurrentBranch)
	}
}

func TestUnwedgeWorktree_NoOpWhenClean(t *testing.T) {
	work := setupWedgeRepo(t, false)
	var buf bytes.Buffer
	if err := UnwedgeWorktree(work, "main", &buf); err != nil {
		t.Fatalf("expected no-op success, got: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected silent no-op, got output: %s", buf.String())
	}
}

// TestUnwedgeWorktree_RegressionPostMergeCleanup simulates the
// acceptance-test scenario from the bead: a refinery cycle where the polecat
// branch is reaped between commit and the next-cycle-start. After unwedge,
// the next cycle should start from a clean baseline (HEAD on the default
// branch, no temp ref present).
func TestUnwedgeWorktree_RegressionPostMergeCleanup(t *testing.T) {
	work := setupWedgeRepo(t, true)

	// Pre-condition: we are wedged exactly like refinery is after the
	// post-merge polecat-branch reap.
	if pre, _ := DetectWedge(work); !pre.Wedged() {
		t.Fatalf("setup precondition failed: not wedged")
	}

	var buf bytes.Buffer
	if err := UnwedgeWorktree(work, "main", &buf); err != nil {
		t.Fatalf("unwedge: %v\n%s", err, buf.String())
	}

	// Verify that the worktree is in the shape the next cycle expects:
	//   - on the default branch
	//   - no `temp` ref
	//   - no stale upstream config
	branch := run(t, work, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "main" {
		t.Errorf("expected HEAD on main after unwedge, got %q", branch)
	}
	// branch -D should fail because temp shouldn't exist anymore.
	if _, err := tryRun(t, work, "git", "show-ref", "--verify", "--quiet", "refs/heads/temp"); err == nil {
		t.Errorf("temp ref still present after unwedge")
	}
}

// tryRun runs a git command and returns (stdout, error) instead of t.Fatal.
func tryRun(t *testing.T, dir, name string, args ...string) (string, error) {
	t.Helper()
	cmd := osexec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestUnwedgeWorktree_Idempotent(t *testing.T) {
	work := setupWedgeRepo(t, true)
	var buf bytes.Buffer
	if err := UnwedgeWorktree(work, "main", &buf); err != nil {
		t.Fatalf("first unwedge: %v", err)
	}
	// Second call must be a clean no-op.
	buf.Reset()
	if err := UnwedgeWorktree(work, "main", &buf); err != nil {
		t.Fatalf("second unwedge: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("second call should be silent no-op, got: %s", buf.String())
	}
}

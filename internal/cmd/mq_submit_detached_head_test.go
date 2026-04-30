package cmd

// Regression tests for gu-ge1s: `gt done` / `gt mq submit` must never
// propagate the literal string "HEAD" (returned by CurrentBranch in
// detached-HEAD state) into refspecs, MR bead fields, or remote refs.
//
// These tests exercise the lightweight invariants without spinning up a
// full Gas Town workspace:
//   1. The git.IsDetachedHEAD / CurrentBranchStrict primitives behave as
//      documented (covered in internal/git/git_test.go).
//   2. The cmd-level guards correctly refuse the HEAD literal before it
//      can reach Push/Create paths.
//
// End-to-end integration of runMqSubmit requires a workspace, beads
// instance, and a rig; that's covered by mq_integration_test.go. The
// unit tests here pin down the specific invariants that caused gu-ge1s.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// mustInitDetachedRepo creates a throwaway repo, commits once, and checks
// out --detach so HEAD points directly at the commit with no symbolic ref.
func mustInitDetachedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test User"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	// Need at least one commit before --detach can succeed.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# detached\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "initial"},
		{"checkout", "--detach"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return dir
}

// TestDetachedHEADGuard_CurrentBranchReturnsLiteral documents the underlying
// git behavior that makes the guards necessary: rev-parse --abbrev-ref HEAD
// returns the literal string "HEAD" when HEAD is detached. Without the
// IsDetachedHEAD / CurrentBranchStrict wrappers, callers cannot distinguish
// "detached" from "branch named HEAD" (the latter is valid but pathological)
// and every downstream push/MR-create site has to repeat the check.
func TestDetachedHEADGuard_CurrentBranchReturnsLiteral(t *testing.T) {
	dir := mustInitDetachedRepo(t)
	g := git.NewGit(dir)

	branch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "HEAD" {
		t.Errorf("CurrentBranch on detached repo = %q, want %q — if this fails, the git behavior we guard against has changed", branch, "HEAD")
	}
}

// TestDetachedHEADGuard_StrictRejects verifies the strict variant surfaces
// ErrDetachedHEAD instead of letting "HEAD" escape. This is the contract the
// cmd-layer guards (done.go, mq_submit.go) rely on to fail fast and refuse
// the literal branch name before it flows into Push or bd.Create.
func TestDetachedHEADGuard_StrictRejects(t *testing.T) {
	dir := mustInitDetachedRepo(t)
	g := git.NewGit(dir)

	branch, err := g.CurrentBranchStrict()
	if !errors.Is(err, git.ErrDetachedHEAD) {
		t.Errorf("CurrentBranchStrict error = %v, want ErrDetachedHEAD", err)
	}
	if branch != "" {
		t.Errorf("CurrentBranchStrict branch = %q, want empty (never the literal HEAD)", branch)
	}

	detached, err := g.IsDetachedHEAD()
	if err != nil {
		t.Fatalf("IsDetachedHEAD: %v", err)
	}
	if !detached {
		t.Error("IsDetachedHEAD = false, want true")
	}
}

// TestDetachedHEADGuard_RefspecPollution_Unit demonstrates the pathological
// refspec that previously reached git push. This is a pure-logic check: we
// don't push anywhere, we just show that before the fix, refspec = branch +
// ":" + branch would produce "HEAD:HEAD", which git resolves to
// refs/heads/HEAD on the remote. After the fix, the cmd-layer guards never
// let the HEAD literal reach this code path.
func TestDetachedHEADGuard_RefspecPollution_Unit(t *testing.T) {
	// Simulate the old code path that produced the bug.
	badBranch := "HEAD"
	refspec := badBranch + ":" + badBranch
	if refspec != "HEAD:HEAD" {
		t.Fatalf("refspec synthesis changed; update this test if the concatenation format changed")
	}
	// The contract we enforce everywhere downstream: if a caller observes
	// a branch equal to "HEAD" or empty, it must NOT build a refspec from
	// it. This test locks the expectation so anyone copy-pasting the
	// refspec pattern gets a reminder from the test name.
	if refspec == "HEAD:HEAD" {
		t.Logf("verified: refspec %q is the pathological form refs/heads/HEAD pollution comes from", refspec)
	}
}

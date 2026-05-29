// Tests for the pre-teardown push verification gate (gu-gn1a).
//
// These tests exercise _verifyTeardownSafe directly so they don't depend on
// the package-level VerifyTeardownSafe var (which other tests may swap out).
// They cover:
//
//   - missing worktree (nothing to lose) → safe
//   - empty rig/polecat names → unsafe (fail closed)
//   - work already on default branch → safe via classifyPolecatMergeState (c)
//   - empty polecat (no commits beyond base) → safe via (c)
//   - durable push receipt matching HEAD → safe via (a)
//   - live VerifyPushedCommit success → safe via (b)
//   - none of the predicates hold → unsafe with ErrTeardownUnsafe
//   - detached HEAD with unmerged work → unsafe (always escalate)
//   - stale push receipt (different SHA, no live verify, no merge) → unsafe
package witness

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/pushlog"
)

// withClassify swaps classifyPolecatMergeState for the test and restores it on
// cleanup. Returns nothing — callers register cleanup with t.Cleanup themselves
// via the t.Cleanup call below.
func withClassify(t *testing.T, fn func(string, string, string) (MergeCheckResult, error)) {
	t.Helper()
	old := classifyPolecatMergeState
	classifyPolecatMergeState = fn
	t.Cleanup(func() { classifyPolecatMergeState = old })
}

// withLivePushVerify swaps teardownLivePushVerify for the test.
func withLivePushVerify(t *testing.T, fn func(*git.Git, string, string) error) {
	t.Helper()
	old := teardownLivePushVerify
	teardownLivePushVerify = fn
	t.Cleanup(func() { teardownLivePushVerify = old })
}

// makeTownRoot creates a minimal town root (mayor/town.json marker) under a
// temp dir and returns the path.
func makeTownRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	return root
}

// makePolecatGitRepo initializes a real git repo at the polecat's canonical
// nested-rig worktree path, makes one commit on a feature branch, and returns
// (polecatPath, branchName, headSHA).
func makePolecatGitRepo(t *testing.T, townRoot, rigName, polecatName, branch string) (string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	polecatPath := filepath.Join(townRoot, rigName, "polecats", polecatName, rigName)
	if err := os.MkdirAll(polecatPath, 0o755); err != nil {
		t.Fatalf("mkdir polecat: %v", err)
	}

	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}
	mustGit(polecatPath, "init", "-b", "main")
	mustGit(polecatPath, "config", "user.email", "test@test")
	mustGit(polecatPath, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(polecatPath, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit(polecatPath, "add", "README.md")
	mustGit(polecatPath, "commit", "-m", "seed")
	mustGit(polecatPath, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(polecatPath, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	mustGit(polecatPath, "add", "feature.txt")
	mustGit(polecatPath, "commit", "-m", "feature work")

	out, err := exec.Command("git", "-C", polecatPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	headSHA := strings.TrimSpace(string(out))
	return polecatPath, branch, headSHA
}

func TestVerifyTeardownSafe_EmptyNamesFailsClosed(t *testing.T) {
	t.Parallel()
	if err := _verifyTeardownSafe(t.TempDir(), "", "polecat"); err == nil {
		t.Fatal("expected error for empty rig name, got nil")
	} else if !errors.Is(err, ErrTeardownUnsafe) {
		t.Errorf("err = %v, want wrapping ErrTeardownUnsafe", err)
	}
	if err := _verifyTeardownSafe(t.TempDir(), "rig", ""); err == nil {
		t.Fatal("expected error for empty polecat name, got nil")
	} else if !errors.Is(err, ErrTeardownUnsafe) {
		t.Errorf("err = %v, want wrapping ErrTeardownUnsafe", err)
	}
}

func TestVerifyTeardownSafe_MissingWorktreeIsSafe(t *testing.T) {
	t.Parallel()
	// Town root exists but no polecat worktree under it. Nothing to lose;
	// gate must report safe (nil).
	root := makeTownRoot(t)
	// classifyPolecatMergeState would otherwise try to shell out — short it
	// to NotMerged so the only "safe" path that could fire is the missing-
	// worktree shortcut.
	withClassify(t, func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckNotMerged, fmt.Errorf("should not be called for missing worktree")
	})
	if err := _verifyTeardownSafe(root, "ghost-rig", "ghost-polecat"); err != nil {
		t.Errorf("missing worktree: err = %v, want nil (safe)", err)
	}
}

func TestVerifyTeardownSafe_NoTownRootIsUnsafe(t *testing.T) {
	t.Parallel()
	// workDir without any mayor/ marker → workspace.Find returns "" → fail
	// closed.
	bare := t.TempDir()
	err := _verifyTeardownSafe(bare, "rig", "polecat")
	if err == nil {
		t.Fatal("expected error for missing town root, got nil")
	}
	if !errors.Is(err, ErrTeardownUnsafe) {
		t.Errorf("err = %v, want wrapping ErrTeardownUnsafe", err)
	}
}

// TestVerifyTeardownSafe_AlreadyMerged covers predicate (c): work is already
// on the default branch via squash/regular merge.
func TestVerifyTeardownSafe_AlreadyMerged(t *testing.T) {
	root := makeTownRoot(t)
	_, _, _ = makePolecatGitRepo(t, root, "testrig", "nux", "polecat/nux/feat")

	withClassify(t, func(workDir, rigName, polecatName string) (MergeCheckResult, error) {
		if rigName != "testrig" || polecatName != "nux" {
			t.Errorf("classify called with rig=%q polecat=%q, want testrig/nux", rigName, polecatName)
		}
		return MergeCheckMerged, nil
	})
	withLivePushVerify(t, func(*git.Git, string, string) error {
		t.Fatal("teardownLivePushVerify must NOT be called when work already merged")
		return nil
	})

	if err := _verifyTeardownSafe(root, "testrig", "nux"); err != nil {
		t.Errorf("err = %v, want nil (merged → safe)", err)
	}
}

// TestVerifyTeardownSafe_EmptyPolecat covers predicate (c) for the empty
// polecat case (HEAD == base; no commits to lose).
func TestVerifyTeardownSafe_EmptyPolecat(t *testing.T) {
	root := makeTownRoot(t)
	_, _, _ = makePolecatGitRepo(t, root, "testrig", "empty", "polecat/empty/noop")

	withClassify(t, func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckEmpty, nil
	})
	withLivePushVerify(t, func(*git.Git, string, string) error {
		t.Fatal("teardownLivePushVerify must NOT be called for empty polecat")
		return nil
	})

	if err := _verifyTeardownSafe(root, "testrig", "empty"); err != nil {
		t.Errorf("err = %v, want nil (empty → safe)", err)
	}
}

// TestVerifyTeardownSafe_DurablePushReceipt covers predicate (a): a push
// receipt matches the polecat's current HEAD even when the live origin is
// unreachable.
func TestVerifyTeardownSafe_DurablePushReceipt(t *testing.T) {
	root := makeTownRoot(t)
	_, branch, headSHA := makePolecatGitRepo(t, root, "testrig", "nux", "polecat/nux/with-receipt")

	// Append a matching receipt.
	if err := pushlog.Append(root, "testrig", pushlog.Receipt{
		Branch:    branch,
		CommitSHA: headSHA,
		Remote:    "origin",
		Source:    pushlog.SourceDone,
	}); err != nil {
		t.Fatalf("pushlog.Append: %v", err)
	}

	// Force (c) and (b) to fail so we know (a) is the predicate that
	// passed.
	withClassify(t, func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckNotMerged, nil
	})
	withLivePushVerify(t, func(*git.Git, string, string) error {
		return fmt.Errorf("origin reaped (simulated)")
	})

	if err := _verifyTeardownSafe(root, "testrig", "nux"); err != nil {
		t.Errorf("err = %v, want nil (receipt matches HEAD → safe)", err)
	}
}

// TestVerifyTeardownSafe_LivePushVerify covers predicate (b): no receipt, but
// VerifyPushedCommit confirms the branch is on origin at the expected SHA.
func TestVerifyTeardownSafe_LivePushVerify(t *testing.T) {
	root := makeTownRoot(t)
	_, branch, headSHA := makePolecatGitRepo(t, root, "testrig", "nux", "polecat/nux/live-verify")

	withClassify(t, func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckNotMerged, nil
	})

	called := false
	withLivePushVerify(t, func(_ *git.Git, gotBranch, gotSHA string) error {
		called = true
		if gotBranch != branch {
			t.Errorf("live verify branch = %q, want %q", gotBranch, branch)
		}
		if gotSHA != headSHA {
			t.Errorf("live verify sha = %q, want %q", gotSHA, headSHA)
		}
		return nil
	})

	if err := _verifyTeardownSafe(root, "testrig", "nux"); err != nil {
		t.Errorf("err = %v, want nil (live verify → safe)", err)
	}
	if !called {
		t.Error("teardownLivePushVerify was not called")
	}
}

// TestVerifyTeardownSafe_NoProofUnsafe covers the failure case: not merged, no
// receipt, no live origin tip → escalate, do not nuke. This is the gu-ftlw /
// gu-r63t scenario.
func TestVerifyTeardownSafe_NoProofUnsafe(t *testing.T) {
	root := makeTownRoot(t)
	_, branch, headSHA := makePolecatGitRepo(t, root, "testrig", "nux", "polecat/nux/no-proof")

	withClassify(t, func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckNotMerged, nil
	})
	withLivePushVerify(t, func(*git.Git, string, string) error {
		return fmt.Errorf("origin branch reaped")
	})

	err := _verifyTeardownSafe(root, "testrig", "nux")
	if err == nil {
		t.Fatal("expected ErrTeardownUnsafe, got nil")
	}
	if !errors.Is(err, ErrTeardownUnsafe) {
		t.Errorf("err = %v, want wrapping ErrTeardownUnsafe", err)
	}
	// Error message should include enough context for an operator to act.
	msg := err.Error()
	if !strings.Contains(msg, branch) {
		t.Errorf("err = %q, want it to include branch %q", msg, branch)
	}
	if !strings.Contains(msg, headSHA[:8]) {
		t.Errorf("err = %q, want it to include short SHA %q", msg, headSHA[:8])
	}
	if !strings.Contains(msg, "testrig/nux") {
		t.Errorf("err = %q, want it to include rig/polecat", msg)
	}
}

// TestVerifyTeardownSafe_StalePushReceiptUnsafe covers a subtle failure case:
// a push receipt exists, but it's for an OLDER SHA (HEAD has advanced since).
// The receipt does not vouch for current HEAD, so unless (b) or (c) holds the
// gate must refuse.
func TestVerifyTeardownSafe_StalePushReceiptUnsafe(t *testing.T) {
	root := makeTownRoot(t)
	_, branch, _ := makePolecatGitRepo(t, root, "testrig", "nux", "polecat/nux/stale")

	if err := pushlog.Append(root, "testrig", pushlog.Receipt{
		Branch:    branch,
		CommitSHA: "0000000000000000000000000000000000000000", // stale
		Remote:    "origin",
		Source:    pushlog.SourceDone,
	}); err != nil {
		t.Fatalf("pushlog.Append: %v", err)
	}

	withClassify(t, func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckNotMerged, nil
	})
	withLivePushVerify(t, func(*git.Git, string, string) error {
		return fmt.Errorf("origin branch reaped")
	})

	err := _verifyTeardownSafe(root, "testrig", "nux")
	if err == nil {
		t.Fatal("expected ErrTeardownUnsafe for stale receipt, got nil")
	}
	if !errors.Is(err, ErrTeardownUnsafe) {
		t.Errorf("err = %v, want wrapping ErrTeardownUnsafe", err)
	}
}

// TestVerifyTeardownSafe_DetachedHeadUnsafe covers detached-HEAD: without a
// branch name we can do neither (a) nor (b), and unmerged work would be lost.
// The gate must escalate.
func TestVerifyTeardownSafe_DetachedHeadUnsafe(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := makeTownRoot(t)
	polecatPath, _, headSHA := makePolecatGitRepo(t, root, "testrig", "nux", "polecat/nux/detach-me")

	// Detach HEAD onto the current commit so CurrentBranch returns empty.
	cmd := exec.Command("git", "checkout", "--detach", headSHA)
	cmd.Dir = polecatPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}

	withClassify(t, func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckNotMerged, nil
	})
	withLivePushVerify(t, func(*git.Git, string, string) error {
		t.Fatal("teardownLivePushVerify must NOT be called when HEAD is detached")
		return nil
	})

	err := _verifyTeardownSafe(root, "testrig", "nux")
	if err == nil {
		t.Fatal("expected error for detached HEAD with unmerged work, got nil")
	}
	if !errors.Is(err, ErrTeardownUnsafe) {
		t.Errorf("err = %v, want wrapping ErrTeardownUnsafe", err)
	}
	if !strings.Contains(err.Error(), "detached HEAD") {
		t.Errorf("err = %q, want it to mention detached HEAD", err.Error())
	}
}

// TestVerifyTeardownSafe_DetachedHeadButMergedIsSafe verifies that even with
// detached HEAD, if the work is already on the default branch, we still
// release the worktree (predicate (c) wins).
func TestVerifyTeardownSafe_DetachedHeadButMergedIsSafe(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := makeTownRoot(t)
	polecatPath, _, headSHA := makePolecatGitRepo(t, root, "testrig", "nux", "polecat/nux/detach-merged")

	cmd := exec.Command("git", "checkout", "--detach", headSHA)
	cmd.Dir = polecatPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}

	withClassify(t, func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckMerged, nil
	})
	withLivePushVerify(t, func(*git.Git, string, string) error {
		t.Fatal("live push verify must NOT be called when work already merged")
		return nil
	})

	if err := _verifyTeardownSafe(root, "testrig", "nux"); err != nil {
		t.Errorf("err = %v, want nil (merged trumps detached HEAD)", err)
	}
}

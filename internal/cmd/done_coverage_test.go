package cmd

// Coverage for the gt done recovery / edge-case helpers flagged by gu-8u7e3:
// runDone is the most-churned function in the codebase and recovery/edge-case
// bugs keep landing in its push-failure and stranded-MR helpers. The phase
// extraction (gu-nid89.12.1) already split runDone into named, individually
// testable functions; this file closes the test-coverage gap on the ones that
// were still at 0% — recordPushFailure / recordPushReceipt (the durable
// forensic log every strand investigation reads), verifyPushedCommitWithBareFallback
// (the post-push verify recovery path), and the pure status/duration helpers.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/pushlog"
)

// --- parseCleanupStatus ------------------------------------------------------

func TestParseCleanupStatus(t *testing.T) {
	cases := []struct {
		in   string
		want polecat.CleanupStatus
	}{
		{"clean", polecat.CleanupClean},
		{"CLEAN", polecat.CleanupClean},
		{"uncommitted", polecat.CleanupUncommitted},
		{"has_uncommitted", polecat.CleanupUncommitted},
		{"stash", polecat.CleanupStash},
		{"has_stash", polecat.CleanupStash},
		{"unpushed", polecat.CleanupUnpushed},
		{"has_unpushed", polecat.CleanupUnpushed},
		{"", polecat.CleanupUnknown},
		{"garbage", polecat.CleanupUnknown},
	}
	for _, tc := range cases {
		if got := parseCleanupStatus(tc.in); got != tc.want {
			t.Errorf("parseCleanupStatus(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- sessionDurationMs -------------------------------------------------------

func TestSessionDurationMs(t *testing.T) {
	t.Run("missing env returns 0", func(t *testing.T) {
		t.Setenv("GT_SESSION_START", "")
		if got := sessionDurationMs(); got != 0 {
			t.Errorf("missing GT_SESSION_START: got %v, want 0", got)
		}
	})

	t.Run("unparseable env returns 0", func(t *testing.T) {
		t.Setenv("GT_SESSION_START", "not-a-number")
		if got := sessionDurationMs(); got != 0 {
			t.Errorf("unparseable GT_SESSION_START: got %v, want 0", got)
		}
	})

	t.Run("non-positive timestamp returns 0", func(t *testing.T) {
		t.Setenv("GT_SESSION_START", "0")
		if got := sessionDurationMs(); got != 0 {
			t.Errorf("zero GT_SESSION_START: got %v, want 0", got)
		}
	})

	t.Run("future timestamp returns 0", func(t *testing.T) {
		future := time.Now().Add(time.Hour).Unix()
		t.Setenv("GT_SESSION_START", itoa(future))
		if got := sessionDurationMs(); got != 0 {
			t.Errorf("future GT_SESSION_START: got %v, want 0 (negative elapsed)", got)
		}
	})

	t.Run("past timestamp returns positive elapsed", func(t *testing.T) {
		past := time.Now().Add(-2 * time.Second).Unix()
		t.Setenv("GT_SESSION_START", itoa(past))
		got := sessionDurationMs()
		if got <= 0 {
			t.Errorf("past GT_SESSION_START: got %v, want > 0", got)
		}
		// ~2s elapsed; allow generous slack for slow CI.
		if got < 1000 || got > 60000 {
			t.Errorf("past GT_SESSION_START: got %v ms, want roughly 2000ms", got)
		}
	})
}

func itoa(n int64) string {
	// Local helper to avoid importing strconv just for the tests.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// --- recordPushFailure -------------------------------------------------------

func TestRecordPushFailure(t *testing.T) {
	t.Run("writes failure record to rig runtime log", func(t *testing.T) {
		townRoot := t.TempDir()
		rigName := "testrig"

		recordPushFailure(townRoot, rigName, "polecat/x/gu-abc--1", "deadbeef",
			pushlog.SourceDone, pushlog.StagePush, "x", "gu-abc",
			errors.New("connection reset by peer"))

		failures, err := pushlog.ReadFailures(townRoot, rigName)
		if err != nil {
			t.Fatalf("ReadFailures: %v", err)
		}
		if len(failures) != 1 {
			t.Fatalf("got %d failures, want 1", len(failures))
		}
		f := failures[0]
		if f.Branch != "polecat/x/gu-abc--1" {
			t.Errorf("Branch = %q, want polecat/x/gu-abc--1", f.Branch)
		}
		if f.CommitSHA != "deadbeef" {
			t.Errorf("CommitSHA = %q, want deadbeef", f.CommitSHA)
		}
		if f.Stage != pushlog.StagePush {
			t.Errorf("Stage = %q, want %q", f.Stage, pushlog.StagePush)
		}
		if f.Error != "connection reset by peer" {
			t.Errorf("Error = %q, want connection reset by peer", f.Error)
		}
		if f.IssueID != "gu-abc" {
			t.Errorf("IssueID = %q, want gu-abc", f.IssueID)
		}
	})

	t.Run("nil pushErr records empty error string", func(t *testing.T) {
		townRoot := t.TempDir()
		recordPushFailure(townRoot, "rig", "branch", "sha",
			pushlog.SourceDone, pushlog.StageVerify, "w", "i", nil)
		failures, err := pushlog.ReadFailures(townRoot, "rig")
		if err != nil {
			t.Fatalf("ReadFailures: %v", err)
		}
		if len(failures) != 1 || failures[0].Error != "" {
			t.Fatalf("got %+v, want one record with empty Error", failures)
		}
	})

	t.Run("missing required args is a no-op", func(t *testing.T) {
		townRoot := t.TempDir()
		// Empty branch: guard must short-circuit before writing.
		recordPushFailure(townRoot, "rig", "", "sha",
			pushlog.SourceDone, pushlog.StagePush, "w", "i", errors.New("boom"))
		failures, err := pushlog.ReadFailures(townRoot, "rig")
		if err != nil {
			t.Fatalf("ReadFailures: %v", err)
		}
		if len(failures) != 0 {
			t.Fatalf("got %d failures, want 0 (guard should skip)", len(failures))
		}

		// Empty townRoot: nothing to write to.
		recordPushFailure("", "rig", "branch", "sha",
			pushlog.SourceDone, pushlog.StagePush, "w", "i", errors.New("boom"))
	})
}

// --- recordPushReceipt -------------------------------------------------------

func TestRecordPushReceipt(t *testing.T) {
	t.Run("writes receipt to rig runtime log", func(t *testing.T) {
		townRoot := t.TempDir()
		rigName := "testrig"

		// g==nil is tolerated (PushURL just stays empty).
		recordPushReceipt(nil, townRoot, rigName, "polecat/x/gu-abc--1", "cafef00d",
			pushlog.SourceDone, "x", "gu-abc")

		receipts, err := pushlog.Read(townRoot, rigName)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(receipts) != 1 {
			t.Fatalf("got %d receipts, want 1", len(receipts))
		}
		r := receipts[0]
		if r.Branch != "polecat/x/gu-abc--1" || r.CommitSHA != "cafef00d" {
			t.Errorf("receipt = %+v, want branch+sha set", r)
		}
		if r.IssueID != "gu-abc" {
			t.Errorf("IssueID = %q, want gu-abc", r.IssueID)
		}
	})

	t.Run("missing required args is a no-op", func(t *testing.T) {
		townRoot := t.TempDir()
		recordPushReceipt(nil, townRoot, "rig", "", "sha", pushlog.SourceDone, "w", "i")
		receipts, err := pushlog.Read(townRoot, "rig")
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(receipts) != 0 {
			t.Fatalf("got %d receipts, want 0 (guard should skip)", len(receipts))
		}
	})
}

// --- verifyPushedCommitWithBareFallback --------------------------------------

// TestVerifyPushedCommitWithBareFallback exercises the gt done post-push verify
// recovery path: when the normal origin verify fails, gt done consults the rig's
// local bare clone (.repo.git) as a fallback before concluding the push was lost.
func TestVerifyPushedCommitWithBareFallback(t *testing.T) {
	t.Run("origin verify success returns nil without consulting bare", func(t *testing.T) {
		bare, wt, branch, sha := setupPushedBranch(t)
		_ = bare
		g := git.NewGit(wt)
		if err := verifyPushedCommitWithBareFallback(g, t.TempDir(), "rig", branch, sha); err != nil {
			t.Fatalf("expected nil (origin has the commit), got %v", err)
		}
	})

	t.Run("bare fallback confirms commit when origin verify fails", func(t *testing.T) {
		bare, wt, branch, sha := setupPushedBranch(t)

		// Build a rig layout where <townRoot>/<rig>/.repo.git IS the bare repo
		// that has the branch. Point the worktree's origin at an EMPTY remote so
		// the normal origin verify fails and the bare fallback must rescue it.
		townRoot := t.TempDir()
		rigName := "rig"
		repoGit := filepath.Join(townRoot, rigName, ".repo.git")
		if err := os.MkdirAll(filepath.Dir(repoGit), 0o755); err != nil {
			t.Fatal(err)
		}
		// Symlink the existing bare repo into place as .repo.git.
		if err := os.Symlink(bare, repoGit); err != nil {
			t.Fatalf("symlink bare repo: %v", err)
		}

		// Repoint origin to an empty bare repo so VerifyPushedCommit("origin")
		// cannot find the commit.
		emptyRemote := t.TempDir()
		runGitEnv(t, emptyRemote, "init", "--bare", "-b", "main")
		runGitEnv(t, wt, "remote", "set-url", "origin", emptyRemote)

		g := git.NewGit(wt)
		if err := verifyPushedCommitWithBareFallback(g, townRoot, rigName, branch, sha); err != nil {
			t.Fatalf("expected bare fallback to confirm commit, got %v", err)
		}
	})

	t.Run("returns verify error when bare repo absent", func(t *testing.T) {
		_, wt, branch, sha := setupPushedBranch(t)

		emptyRemote := t.TempDir()
		runGitEnv(t, emptyRemote, "init", "--bare", "-b", "main")
		runGitEnv(t, wt, "remote", "set-url", "origin", emptyRemote)

		g := git.NewGit(wt)
		// townRoot has no <rig>/.repo.git, so the fallback can't help.
		err := verifyPushedCommitWithBareFallback(g, t.TempDir(), "rig", branch, sha)
		if err == nil {
			t.Fatal("expected verify error (no origin commit, no bare fallback), got nil")
		}
	})
}

// setupPushedBranch creates a bare remote and a worktree that has pushed a
// feature branch to it. Returns (bareRepoPath, worktreePath, branch, headSHA).
func setupPushedBranch(t *testing.T) (string, string, string, string) {
	t.Helper()
	bare := t.TempDir()
	runGitEnv(t, bare, "init", "--bare", "-b", "main")

	wt := t.TempDir()
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

	branch := "polecat/test/gu-cov--1"
	runGitEnv(t, wt, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "feature.txt")
	runGitEnv(t, wt, "commit", "-m", "feat: work")
	runGitEnv(t, wt, "push", "origin", branch)
	head := runGitEnv(t, wt, "rev-parse", "HEAD")

	return bare, wt, branch, head
}

package git

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeGitShim installs a fake `git` executable on PATH that forks a child
// process which inherits stdout/stderr and sleeps far longer than any test
// timeout, then blocks the leader too. This reproduces the gc-utizk7 gt-done
// deadlock: git push forks a transport child (ssh / git-remote-https) that
// inherits the write end of the os.Pipe Go allocates for a bytes.Buffer
// stdout/stderr. On context timeout the default CommandContext cancel SIGKILLs
// only the git leader; the backgrounded child reparents to PID 1, keeps the
// pipe write end open, and the stdlib copy goroutine — hence Wait() — blocks
// until the child finally exits (here: 60s). The fix (util.SetProcessGroup +
// WaitDelay) kills the whole process group on timeout so the call returns
// promptly.
func writeGitShim(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "git")
	// The backgrounded `sleep 60 &` inherits fd1/fd2 (the inherited pipe) and
	// outlives the leader; the leader's own `sleep 60` keeps the context busy
	// until the deadline fires. PATH is re-exported inside so `sleep` resolves
	// even though the parent PATH is overridden to find this shim first.
	script := "#!/bin/sh\n" +
		"export PATH=/usr/bin:/bin:$PATH\n" +
		"sleep 60 &\n" +
		"sleep 60\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// assertReturnsBeforeChildExit runs fn (a timed git helper invocation with a
// short timeout) and fails if it does not return well before the shim child's
// 60s sleep — i.e. if it deadlocked on the inherited pipe.
func assertReturnsBeforeChildExit(t *testing.T, fn func() error) {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		_ = fn()
		close(done)
	}()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("timed git helper took %v — pipe deadlock not bounded", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed git helper did not return: deadlocked reading an inherited pipe held open by a reparented child (gc-utizk7 regression)")
	}
}

func TestRunWithTimeout_DoesNotDeadlockOnReparentedChild(t *testing.T) {
	writeGitShim(t)
	g := NewGit(t.TempDir())
	assertReturnsBeforeChildExit(t, func() error {
		_, err := g.runWithTimeout(500*time.Millisecond, "push", "origin", "main")
		return err
	})
}

func TestRunWithEnvAndTimeout_DoesNotDeadlockOnReparentedChild(t *testing.T) {
	writeGitShim(t)
	g := NewGit(t.TempDir())
	assertReturnsBeforeChildExit(t, func() error {
		_, err := g.runWithEnvAndTimeout(
			[]string{"push", "origin", "main"},
			prePushSkipEnv,
			500*time.Millisecond,
		)
		return err
	})
}

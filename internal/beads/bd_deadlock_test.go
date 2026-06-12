package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRun_DoesNotDeadlockOnReparentedChild reproduces the gu-szlis gt-done
// wedge: when the Dolt data plane bounces (circuit breaker), the bd subprocess
// wedges holding a stale pre-bounce connection, and any child it forks inherits
// the write end of the os.Pipe Go creates for run()'s bytes.Buffer
// stdout/stderr. The pre-fix run() used exec.CommandContext +
// SetDetachedProcessGroup (Setpgid but NO Cancel hook): on the subprocess
// timeout, CommandContext SIGKILLs ONLY the bd leader; the backgrounded child
// reparents to PID 1, keeps the inherited pipe write end open, and cmd.Run()
// blocks FOREVER in futex_wait_queue (the fd7/fd9 read-pipe-no-writer signature
// with zero children). This is the same os/exec inherited-pipe class fixed for
// the pre-merge gates (6d9cbc2b) and the git push helpers (a0a7bdb9).
//
// The stub bd backgrounds a long sleep that inherits the pipe and outlives the
// timeout, then the leader also sleeps past the deadline. With the fix
// (util.SetProcessGroup + WaitDelay) the whole process group is SIGKILLed on
// timeout, so run() returns promptly with a (timeout) error instead of hanging.
func TestRun_DoesNotDeadlockOnReparentedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group SIGKILL semantics are POSIX-specific")
	}

	stubDir := t.TempDir()
	// Background a sleep that inherits the stdout/stderr pipe and outlives the
	// timeout, then block the leader past the context deadline. This mirrors
	// bd wedged on a stale Dolt connection while a child holds the pipe open.
	stubScript := "#!/bin/sh\n" +
		"export PATH=/usr/bin:/bin:$PATH\n" +
		"sleep 30 &\n" +
		"sleep 30\n"
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Shrink the subprocess timeout to keep the test fast (the env override is
	// integer seconds; 1s is the minimum).
	t.Setenv("GT_BD_TIMEOUT_SEC", "1")

	b := New(t.TempDir())

	done := make(chan struct{})
	start := time.Now()
	go func() {
		// "show" is a non-list command, so the read-throttle flock path is
		// skipped and we exercise the bare subprocess timeout directly.
		_, _ = b.run("show", "xx-deadlock")
		close(done)
	}()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 15*time.Second {
			t.Fatalf("run() took %v — bd subprocess pipe deadlock not bounded", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run() did not return: deadlocked reading an inherited pipe held open by a reparented child (gu-szlis regression)")
	}
}

// TestResolveBdSubprocessTimeout_HonorsOverride documents that the subprocess
// timeout — the deadline whose enforcement gu-szlis makes effective — is
// configurable, and that an invalid override falls back to the default.
func TestResolveBdSubprocessTimeout_HonorsOverride(t *testing.T) {
	t.Setenv("GT_BD_TIMEOUT_SEC", "5")
	if got := resolveBdSubprocessTimeout(); got != 5*time.Second {
		t.Errorf("expected 5s from override, got %v", got)
	}

	t.Setenv("GT_BD_TIMEOUT_SEC", "not-a-number")
	if got := resolveBdSubprocessTimeout(); got != bdSubprocessTimeout {
		t.Errorf("invalid override should fall back to default %v, got %v", bdSubprocessTimeout, got)
	}
}

// TestRun_TimeoutErrorMentionsBd is a lightweight guard that the timeout path
// surfaces a bd-tagged error to the caller (fail-fast) rather than swallowing
// it. Uses the same forking stub but only asserts the returned error.
func TestRun_TimeoutErrorMentionsBd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group SIGKILL semantics are POSIX-specific")
	}

	stubDir := t.TempDir()
	stubScript := "#!/bin/sh\nexport PATH=/usr/bin:/bin:$PATH\nsleep 30 &\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(stubDir, "bd"), []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_BD_TIMEOUT_SEC", "1")

	b := New(t.TempDir())
	_, err := b.run("show", "xx-deadlock")
	if err == nil {
		t.Fatal("expected a timeout error from the wedged bd subprocess, got nil")
	}
	if !strings.Contains(err.Error(), "bd") {
		t.Errorf("timeout error should be bd-tagged for the caller, got: %v", err)
	}
}

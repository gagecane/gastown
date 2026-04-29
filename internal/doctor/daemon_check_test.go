package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestNewDaemonCheck(t *testing.T) {
	check := NewDaemonCheck()

	if check.Name() != "daemon" {
		t.Errorf("expected name 'daemon', got %q", check.Name())
	}
	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}
}

// TestDaemonCheck_Fix_NoStart verifies the --no-start short-circuit is unchanged.
func TestDaemonCheck_Fix_NoStart(t *testing.T) {
	tmpDir := t.TempDir()
	check := NewDaemonCheck()
	ctx := &CheckContext{TownRoot: tmpDir, NoStart: true}

	err := check.Fix(ctx)
	if !errors.Is(err, ErrSkippedNoStart) {
		t.Errorf("expected ErrSkippedNoStart, got %v", err)
	}
}

// TestDaemonCheck_Fix_SkipsWhenDaemonAlreadyRunning verifies the running-guard:
// when a daemon is already running (held flock on daemon.lock), Fix must return
// nil WITHOUT spawning a child process. This prevents the spawn-loop failure
// mode where buggy callers invoking Fix repeatedly would fork, fail on the
// pidfile lock, and bloat daemon.log.
//
// We detect "no spawn happened" via elapsed time: a real spawn path sleeps
// 300ms after cmd.Start(). The guarded path returns within a few milliseconds.
func TestDaemonCheck_Fix_SkipsWhenDaemonAlreadyRunning(t *testing.T) {
	tmpDir := t.TempDir()
	lockDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lockPath := filepath.Join(lockDir, "daemon.lock")

	// Simulate a running daemon by holding the flock. daemon.IsRunning
	// treats a held lock as the authoritative "daemon is running" signal.
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if !locked {
		t.Fatal("expected to acquire lock")
	}
	defer func() { _ = lock.Unlock() }()

	check := NewDaemonCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Call Fix twice to mirror the spawn-loop scenario from the bug report.
	for i := 0; i < 2; i++ {
		start := time.Now()
		if err := check.Fix(ctx); err != nil {
			t.Fatalf("call %d: Fix returned unexpected error: %v", i+1, err)
		}
		elapsed := time.Since(start)

		// If the guard is missing, Fix would call cmd.Start() followed by
		// time.Sleep(300ms). The guarded path has no sleep. Use a generous
		// threshold (150ms) that clearly distinguishes the two paths without
		// being flaky on slow CI.
		if elapsed >= 150*time.Millisecond {
			t.Errorf("call %d: Fix took %v, expected <150ms — running-guard did not short-circuit (did it spawn a child?)", i+1, elapsed)
		}
	}

	// Belt-and-suspenders: the running-guard path must not create a daemon.pid
	// file. If a real spawn had happened, the child process would write one.
	pidFile := filepath.Join(tmpDir, "daemon", "daemon.pid")
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("daemon.pid exists after Fix, but no spawn should have occurred")
	}
}

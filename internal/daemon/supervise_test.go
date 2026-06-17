package daemon

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/gofrs/flock"
)

// TestClassifySignal pins the signal→action mapping the supervisor loop relies
// on. The startup sequence registers daemonSignals(); each must resolve to the
// right branch so a handoff (SIGUSR1) and a clear-backoff (SIGUSR2) are not
// mistaken for shutdown (SIGINT/SIGTERM).
func TestClassifySignal(t *testing.T) {
	cases := []struct {
		name string
		sig  os.Signal
		want signalAction
	}{
		{"SIGUSR1 is lifecycle", syscall.SIGUSR1, signalLifecycle},
		{"SIGUSR2 is reload-restart", syscall.SIGUSR2, signalReloadRestart},
		{"SIGTERM is shutdown", syscall.SIGTERM, signalShutdown},
		{"SIGINT is shutdown", syscall.SIGINT, signalShutdown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySignal(tc.sig); got != tc.want {
				t.Errorf("classifySignal(%v) = %d, want %d", tc.sig, got, tc.want)
			}
		})
	}
}

// TestRunIfActive_RunsWhenNoShutdown verifies the patrol guard invokes its
// callback when no town shutdown is in progress (the steady-state path every
// periodic patrol case takes).
func TestRunIfActive_RunsWhenNoShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	called := false
	d.runIfActive(func() { called = true })

	if !called {
		t.Error("expected runIfActive to invoke fn when no shutdown is in progress")
	}
}

// TestRunIfActive_SkipsDuringShutdown verifies the patrol guard suppresses its
// callback while `gt down` holds the shutdown lock, so the daemon does not
// fight shutdown by doing recovery work.
func TestRunIfActive_SkipsDuringShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "daemon"), 0755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	// Acquire and hold the shutdown lock to simulate an in-progress shutdown.
	lockPath := filepath.Join(tmpDir, "daemon", "shutdown.lock")
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil || !locked {
		t.Fatalf("failed to acquire shutdown lock for test: locked=%v err=%v", locked, err)
	}
	defer func() { _ = lock.Unlock() }()

	called := false
	d.runIfActive(func() { called = true })

	if called {
		t.Error("expected runIfActive to skip fn while shutdown is in progress")
	}
}

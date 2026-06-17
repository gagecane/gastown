package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
)

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

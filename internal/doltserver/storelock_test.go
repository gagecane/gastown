package doltserver

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// TestStoreLockPath_ProductionPort: the canonical port 3307 gets the bare name.
func TestStoreLockPath_ProductionPort(t *testing.T) {
	got := StoreLockPath("/town", DefaultPort)
	want := filepath.Join("/town", "daemon", "dolt-store.lock")
	if got != want {
		t.Errorf("StoreLockPath(3307) = %q, want %q", got, want)
	}
}

// TestStoreLockPath_OtherPort: a non-canonical port is suffixed so multi-server
// installs stay isolated. MUST match plugins/dolt-backup/run.sh store_lock_path.
func TestStoreLockPath_OtherPort(t *testing.T) {
	got := StoreLockPath("/town", 4407)
	want := filepath.Join("/town", "daemon", "dolt-store.lock.4407")
	if got != want {
		t.Errorf("StoreLockPath(4407) = %q, want %q", got, want)
	}
}

// TestWithStoreLock_RunsAndReleases: fn runs while the lock is held, the lock is
// released afterward (a second WithStoreLock acquires immediately), and fn's
// error is returned unchanged.
func TestWithStoreLock_RunsAndReleases(t *testing.T) {
	town := t.TempDir()
	ran := false
	err := WithStoreLock(town, DefaultPort, 5*time.Second, nil, func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatal("fn was not run")
	}

	// Lock must be released — a fresh acquire should succeed without waiting.
	fl := flock.New(StoreLockPath(town, DefaultPort))
	locked, lerr := fl.TryLock()
	if lerr != nil || !locked {
		t.Fatalf("lock not released after WithStoreLock: locked=%v err=%v", locked, lerr)
	}
	_ = fl.Unlock()
}

// TestWithStoreLock_PropagatesFnError: fn's error is returned verbatim.
func TestWithStoreLock_PropagatesFnError(t *testing.T) {
	town := t.TempDir()
	sentinel := os.ErrClosed
	err := WithStoreLock(town, DefaultPort, 5*time.Second, nil, func() error {
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("WithStoreLock did not propagate fn error: got %v, want %v", err, sentinel)
	}
}

// TestWithStoreLock_FailOpenOnContention: when another holder keeps the lock past
// the wait window, WithStoreLock must STILL run fn (fail-open) so a Dolt restart
// is never blocked by an in-flight backup that overruns.
func TestWithStoreLock_FailOpenOnContention(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Hold the lock for the duration of the test from a separate flock handle.
	holder := flock.New(StoreLockPath(town, DefaultPort))
	locked, err := holder.TryLock()
	if err != nil || !locked {
		t.Fatalf("test setup: could not pre-acquire lock: locked=%v err=%v", locked, err)
	}
	defer func() { _ = holder.Unlock() }()

	var logged bool
	var mu sync.Mutex
	logf := func(string, ...any) {
		mu.Lock()
		logged = true
		mu.Unlock()
	}

	ran := false
	start := time.Now()
	// Short wait so the test is fast; contention is guaranteed by the holder.
	err = WithStoreLock(town, DefaultPort, 150*time.Millisecond, logf, func() error {
		ran = true
		return nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("fail-open path returned error: %v", err)
	}
	if !ran {
		t.Fatal("fail-open: fn must still run when the lock cannot be acquired")
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("expected to wait the full window before failing open, waited %v", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if !logged {
		t.Error("expected a warning to be logged on contention timeout")
	}
}

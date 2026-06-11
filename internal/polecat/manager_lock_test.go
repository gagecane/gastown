package polecat

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/steveyegge/gastown/internal/rig"
)

// TestLockPolecatTimesOutWhenHeld verifies that lockPolecat fails fast with a
// clear error instead of blocking indefinitely when the polecat lock is already
// held by another process (gu-ay53c — bounding the acquire prevents futex
// pile-up of hung callers).
func TestLockPolecatTimesOutWhenHeld(t *testing.T) {
	rigPath := t.TempDir()
	mgr := &Manager{rig: &rig.Rig{Name: "testrig", Path: rigPath}}

	// Simulate a wedged prior operation still holding the polecat lock.
	lockPath := filepath.Join(rigPath, ".runtime", "locks", "polecat-furiosa.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		t.Fatal(err)
	}
	held := flock.New(lockPath)
	if locked, err := held.TryLock(); err != nil || !locked {
		t.Fatalf("failed to pre-acquire lock: locked=%v err=%v", locked, err)
	}
	defer func() { _ = held.Unlock() }()

	// Shrink the timeout so the test doesn't wait the full production budget.
	restore := polecatLockTimeout
	polecatLockTimeout = 200 * time.Millisecond
	defer func() { polecatLockTimeout = restore }()

	start := time.Now()
	fl, err := mgr.lockPolecat("furiosa")
	elapsed := time.Since(start)

	if err == nil {
		_ = fl.Unlock()
		t.Fatal("expected timeout error acquiring contended polecat lock, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("lockPolecat blocked %s — should have failed fast within the timeout", elapsed)
	}
}

// TestLockPolecatSucceedsWhenFree verifies the happy path: an uncontended lock
// is acquired and can be released.
func TestLockPolecatSucceedsWhenFree(t *testing.T) {
	rigPath := t.TempDir()
	mgr := &Manager{rig: &rig.Rig{Name: "testrig", Path: rigPath}}

	fl, err := mgr.lockPolecat("nux")
	if err != nil {
		t.Fatalf("expected to acquire free polecat lock, got error: %v", err)
	}
	if err := fl.Unlock(); err != nil {
		t.Fatalf("unlocking polecat lock: %v", err)
	}
}

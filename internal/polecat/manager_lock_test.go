package polecat

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// TestAcquireBounded_Uncontended verifies that acquiring a free lock succeeds
// immediately and returns a held lock.
func TestAcquireBounded_Uncontended(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "test.lock")

	fl, err := acquireBounded(lockPath, "test lock")
	if err != nil {
		t.Fatalf("acquireBounded on free lock: unexpected error: %v", err)
	}
	defer fl.Unlock()

	if !fl.Locked() {
		t.Fatal("expected lock to be held after acquireBounded")
	}
}

// TestAcquireBounded_Contended verifies that when the lock is already held,
// acquireBounded fails fast with a "lock held" error rather than blocking
// indefinitely (the futex pile-up this change prevents).
func TestAcquireBounded_Contended(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "test.lock")

	// Hold the lock from a separate flock handle on the same path.
	holder := flock.New(lockPath)
	locked, err := holder.TryLock()
	if err != nil {
		t.Fatalf("holder TryLock: %v", err)
	}
	if !locked {
		t.Fatal("holder failed to acquire free lock")
	}
	defer holder.Unlock()

	// Use a short timeout so the test does not wait the full 30s.
	orig := lockAcquireTimeout
	lockAcquireTimeout = 200 * time.Millisecond
	defer func() { lockAcquireTimeout = orig }()

	start := time.Now()
	fl, err := acquireBounded(lockPath, "test lock")
	elapsed := time.Since(start)

	if err == nil {
		fl.Unlock()
		t.Fatal("expected error acquiring contended lock, got nil")
	}
	if !strings.Contains(err.Error(), "lock held") {
		t.Fatalf("expected 'lock held' error, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("acquireBounded blocked too long (%s); should fail fast", elapsed)
	}
}

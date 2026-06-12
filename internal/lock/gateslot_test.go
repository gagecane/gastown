package lock

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestGateSlotDir(t *testing.T) {
	if got := GateSlotDir(""); got != "" {
		t.Errorf("GateSlotDir(\"\") = %q, want empty", got)
	}
	want := filepath.Join("/town", ".runtime", "locks", "gate-slots")
	if got := GateSlotDir("/town"); got != want {
		t.Errorf("GateSlotDir(/town) = %q, want %q", got, want)
	}
}

func TestResolveGateConcurrency(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(GateSlotEnvVar, "")
		if got := ResolveGateConcurrency(); got != DefaultGateConcurrency {
			t.Errorf("ResolveGateConcurrency() = %d, want default %d", got, DefaultGateConcurrency)
		}
	})
	t.Run("honors positive override", func(t *testing.T) {
		t.Setenv(GateSlotEnvVar, "5")
		if got := ResolveGateConcurrency(); got != 5 {
			t.Errorf("ResolveGateConcurrency() = %d, want 5", got)
		}
	})
	t.Run("ignores non-positive and garbage", func(t *testing.T) {
		for _, v := range []string{"0", "-3", "abc"} {
			t.Setenv(GateSlotEnvVar, v)
			if got := ResolveGateConcurrency(); got != DefaultGateConcurrency {
				t.Errorf("ResolveGateConcurrency(%q) = %d, want default %d", v, got, DefaultGateConcurrency)
			}
		}
	})
}

func TestAcquireGateSlot_EmptyTownRoot(t *testing.T) {
	if release := AcquireGateSlot("", time.Second); release != nil {
		release()
		t.Fatal("AcquireGateSlot with empty town root should return nil (proceed unthrottled)")
	}
}

// TestAcquireGateSlot_SharesPoolWithSemaphore proves a slot taken via the
// high-level helper contends on the SAME flock files a raw NewFlockSemaphore
// over GateSlotDir uses. This is the interop guarantee that lets the refinery
// gate, the polecat gt-done path, and the bash pre-push hook share one cap.
func TestAcquireGateSlot_SharesPoolWithSemaphore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("advisory flock is a no-op on Windows")
	}
	t.Setenv(GateSlotEnvVar, "1") // single slot so contention is observable
	townRoot := t.TempDir()

	release := AcquireGateSlot(townRoot, time.Second)
	if release == nil {
		t.Fatal("first AcquireGateSlot should succeed")
	}

	// A separate semaphore instance pointed at the same canonical dir must now
	// fail to acquire — the helper is holding the only slot.
	sem := NewFlockSemaphore(GateSlotDir(townRoot), ResolveGateConcurrency())
	if _, err := sem.Acquire(100 * time.Millisecond); err == nil {
		t.Fatal("second acquire on the shared pool should time out while the helper holds the only slot")
	}

	// Release frees the shared slot for the next acquirer.
	release()
	r2, err := sem.Acquire(time.Second)
	if err != nil {
		t.Fatalf("acquire after release should succeed: %v", err)
	}
	r2()
}

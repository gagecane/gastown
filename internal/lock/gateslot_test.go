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

func TestResolveGateReserve(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(GateReserveEnvVar, "")
		if got := ResolveGateReserve(2); got != DefaultGateRefineryReserve {
			t.Errorf("ResolveGateReserve(2) = %d, want default %d", got, DefaultGateRefineryReserve)
		}
	})
	t.Run("honors override", func(t *testing.T) {
		t.Setenv(GateReserveEnvVar, "2")
		if got := ResolveGateReserve(4); got != 2 {
			t.Errorf("ResolveGateReserve(4) = %d, want 2", got)
		}
	})
	t.Run("clamps to n-1 so a shared slot always remains", func(t *testing.T) {
		t.Setenv(GateReserveEnvVar, "5")
		if got := ResolveGateReserve(2); got != 1 {
			t.Errorf("ResolveGateReserve(2) with reserve=5 = %d, want clamp to 1", got)
		}
	})
	t.Run("ignores garbage and negatives", func(t *testing.T) {
		for _, v := range []string{"-1", "abc"} {
			t.Setenv(GateReserveEnvVar, v)
			if got := ResolveGateReserve(3); got != DefaultGateRefineryReserve {
				t.Errorf("ResolveGateReserve(3, %q) = %d, want default %d", v, got, DefaultGateRefineryReserve)
			}
		}
	})
	t.Run("single-slot pool reserves nothing", func(t *testing.T) {
		t.Setenv(GateReserveEnvVar, "1")
		if got := ResolveGateReserve(1); got != 0 {
			t.Errorf("ResolveGateReserve(1) = %d, want 0 (no slot to reserve)", got)
		}
	})
}

func TestSlotOrder(t *testing.T) {
	// n=3, reserve=1: shared = [0,1], reserved tail = {2}.
	shared := sharedSlotOrder(3, 1)
	if want := []int{0, 1}; !equalInts(shared, want) {
		t.Errorf("sharedSlotOrder(3,1) = %v, want %v", shared, want)
	}
	// Refinery prefers the reserved tail first, then shared as overflow.
	refinery := refinerySlotOrder(3, 1)
	if want := []int{2, 0, 1}; !equalInts(refinery, want) {
		t.Errorf("refinerySlotOrder(3,1) = %v, want %v", refinery, want)
	}
	// reserve=0: shared spans all slots, refinery has the same set (no tail).
	if want := []int{0, 1}; !equalInts(sharedSlotOrder(2, 0), want) {
		t.Errorf("sharedSlotOrder(2,0) = %v, want %v", sharedSlotOrder(2, 0), want)
	}
	if want := []int{0, 1}; !equalInts(refinerySlotOrder(2, 0), want) {
		t.Errorf("refinerySlotOrder(2,0) = %v, want %v", refinerySlotOrder(2, 0), want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAcquireGateSlot_PolecatsCannotStarveRefinery is the regression test for
// gu-428u3: with the default reserve, polecat pre-submit acquirers (the shared
// pool) saturating every slot they CAN take must still leave the refinery's
// reserved slot free, so AcquireGateSlotPriority always makes progress.
func TestAcquireGateSlot_PolecatsCannotStarveRefinery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("advisory flock is a no-op on Windows")
	}
	t.Setenv(GateSlotEnvVar, "2")    // 2 total slots
	t.Setenv(GateReserveEnvVar, "1") // 1 reserved for refinery → 1 shared
	townRoot := t.TempDir()

	// A polecat takes the only shared slot.
	polecat := AcquireGateSlot(townRoot, time.Second)
	if polecat == nil {
		t.Fatal("polecat should acquire the shared slot")
	}

	// A SECOND polecat must NOT find a slot — the only other slot is reserved.
	if more := AcquireGateSlot(townRoot, 100*time.Millisecond); more != nil {
		more()
		t.Fatal("second polecat should be blocked: its only slot is reserved for the refinery")
	}

	// The refinery, however, takes its reserved slot immediately despite the
	// polecat saturating the shared pool. This is the anti-starvation guarantee.
	refinery := AcquireGateSlotPriority(townRoot, time.Second)
	if refinery == nil {
		t.Fatal("refinery must acquire its reserved slot even when polecats saturate the shared pool")
	}

	refinery()
	polecat()
}

// TestAcquireGateSlot_RefineryOverflowsToShared proves the refinery falls back
// to shared slots when its reserved tail is already held (e.g. two refinery
// gates), still bounded by total concurrency.
func TestAcquireGateSlot_RefineryOverflowsToShared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("advisory flock is a no-op on Windows")
	}
	t.Setenv(GateSlotEnvVar, "2")
	t.Setenv(GateReserveEnvVar, "1")
	townRoot := t.TempDir()

	r1 := AcquireGateSlotPriority(townRoot, time.Second) // takes reserved slot
	if r1 == nil {
		t.Fatal("first refinery acquire should succeed")
	}
	r2 := AcquireGateSlotPriority(townRoot, time.Second) // overflows to shared
	if r2 == nil {
		t.Fatal("second refinery acquire should overflow to the shared slot")
	}
	// Now both slots are held — a polecat finds nothing (total cap honored).
	if p := AcquireGateSlot(townRoot, 100*time.Millisecond); p != nil {
		p()
		t.Fatal("polecat should be blocked: refinery overflow holds both slots")
	}
	r1()
	r2()
}

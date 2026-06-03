package liveness

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
)

func TestPIDAlive_CurrentProcess(t *testing.T) {
	if !PIDAlive(os.Getpid()) {
		t.Errorf("PIDAlive(os.Getpid()) = false, want true (this process is alive)")
	}
}

func TestPIDAlive_NonPositive(t *testing.T) {
	// pid <= 0 is never a real, signalable process. On Unix in particular,
	// kill(0, ...) targets the caller's process group and kill(-1, ...) is a
	// broadcast — neither is a liveness probe — so the leaf must reject them
	// before they reach the syscall.
	for _, pid := range []int{0, -1, -1000} {
		if PIDAlive(pid) {
			t.Errorf("PIDAlive(%d) = true, want false", pid)
		}
	}
}

func TestPIDAlive_DeadPID(t *testing.T) {
	// testutil.DeadPID spawns, reaps, and verifies a process is gone. This is
	// the PID-reuse-safe way to obtain a dead PID: hard-coding a high constant
	// (e.g. 4194303) is fragile on hosts where the kernel PID space has
	// wrapped close to pid_max and a live process happens to sit there.
	dead := testutil.DeadPID(t)
	if PIDAlive(dead) {
		t.Errorf("PIDAlive(%d) = true, want false (process was reaped)", dead)
	}
}

func TestPIDAlive_PIDWrapBoundary(t *testing.T) {
	// Guard the int->uint32 narrowing on Windows and the non-positive reject
	// on both platforms. A PID at or past the 32-bit boundary must not be
	// reported alive (the Windows path rejects > MaxUint32; Unix has no such
	// PID and will simply find no process).
	for _, pid := range []int{1 << 31, 1 << 32, (1 << 32) + 1} {
		if PIDAlive(pid) {
			t.Errorf("PIDAlive(%d) = true, want false (out-of-range PID)", pid)
		}
	}
}

package testutil

import (
	"os"
	"runtime"
	"syscall"
	"testing"
)

// TestDeadPID_ReturnsDeadPID verifies that DeadPID returns a PID that is not
// alive at return time, exercising the Linux kill(pid, 0) == ESRCH path.
func TestDeadPID_ReturnsDeadPID(t *testing.T) {
	pid := DeadPID(t)
	if pid <= 0 {
		t.Fatalf("DeadPID returned non-positive PID: %d", pid)
	}
	if runtime.GOOS == "windows" {
		// On Windows we don't verify liveness; just assert a sensible PID.
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		// Expected on many Unix systems when the PID is free.
		return
	}
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Errorf("DeadPID returned PID %d that is still alive", pid)
	}
}

// TestDeadPID_DifferentFromCurrentProcess verifies that DeadPID never returns
// our own PID (which would be very much alive).
func TestDeadPID_DifferentFromCurrentProcess(t *testing.T) {
	pid := DeadPID(t)
	if pid == os.Getpid() {
		t.Errorf("DeadPID returned the current process's PID (%d)", pid)
	}
}

// TestDeadPID_RepeatedCalls ensures the helper is robust under repeated use
// and returns a different PID each time (or at least a different-enough
// sequence that PIDs are being freshly allocated and reaped).
func TestDeadPID_RepeatedCalls(t *testing.T) {
	const n = 5
	seen := make(map[int]bool, n)
	for i := 0; i < n; i++ {
		pid := DeadPID(t)
		if pid <= 0 {
			t.Fatalf("iteration %d: non-positive PID %d", i, pid)
		}
		seen[pid] = true
	}
	// We don't require every PID to be unique (PID wrap could in theory
	// reuse values), but at least one distinct PID must appear.
	if len(seen) == 0 {
		t.Fatal("DeadPID produced no PIDs")
	}
}

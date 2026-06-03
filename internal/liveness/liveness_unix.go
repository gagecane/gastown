//go:build !windows

package liveness

import (
	"errors"
	"math"
	"os"
	"syscall"
)

// PIDAlive reports whether a process with the given PID is alive.
//
// On Unix it uses signal 0 ("kill -0"): the kernel performs the
// existence/permission check without delivering a signal. A nil error means
// the process exists and we may signal it. ESRCH means the PID is gone — note
// the Go runtime translates that errno into os.ErrProcessDone, so both are
// checked. Any other error (notably EPERM) means the process exists but we
// lack permission to signal it — treated as alive so we never reap state
// owned by a live process we simply can't probe.
//
// PIDs above math.MaxInt32 are rejected: pid_t is a signed 32-bit int on
// Linux, so a larger value would wrap negative and kill(2) would target a
// process group instead of probing a single process.
func PIDAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxInt32 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		// On Unix FindProcess never fails, but be defensive.
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return false
		}
		// EPERM and other errors: the process exists but we can't signal it.
		return true
	}
	return true
}

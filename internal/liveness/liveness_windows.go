//go:build windows

package liveness

import (
	"math"

	"golang.org/x/sys/windows"
)

// PIDAlive reports whether a process with the given PID is alive.
//
// On Windows there is no Signal(0); opening a handle with
// PROCESS_QUERY_LIMITED_INFORMATION is sufficient to prove the process exists.
// ERROR_ACCESS_DENIED means the process exists but is inaccessible to the
// current user — treated as alive, mirroring the Unix EPERM handling.
func PIDAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	_ = windows.CloseHandle(handle)
	return true
}

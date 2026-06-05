//go:build windows

package nudge

import (
	"os"
	"syscall"
)

// detachedProcAttr returns SysProcAttr for Windows.
// CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW detaches the child from the
// parent's console group without flashing a visible console window.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: 0x00000200 | 0x08000000, // CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW
	}
}

// terminateProcess kills the process on Windows (no graceful SIGTERM).
func terminateProcess(proc *os.Process) error {
	return proc.Kill()
}

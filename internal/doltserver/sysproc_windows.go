//go:build windows

package doltserver

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup detaches the child process on Windows using
// CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW so that it survives
// the parent's exit without flashing a visible console window.
func setProcessGroup(cmd *exec.Cmd) {
	const CREATE_NO_WINDOW = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW,
	}
}

// gracefulTerminate on Windows has no SIGTERM equivalent — Kill() is the only option.
func gracefulTerminate(p *os.Process) error {
	return p.Kill()
}

//go:build windows

package lock

import (
	"os/exec"
	"syscall"
)

// setProcessGroup detaches the child on Windows, suppressing console window flash.
func setProcessGroup(cmd *exec.Cmd) {
	const CREATE_NO_WINDOW = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW,
	}
}

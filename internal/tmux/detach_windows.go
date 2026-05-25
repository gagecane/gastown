//go:build windows

package tmux

import (
	"os/exec"
	"syscall"
)

// detachCmd configures a command to run as a fully detached process that
// survives the parent's exit. On Windows this creates a new process group
// and suppresses console window creation.
func detachCmd(cmd *exec.Cmd) {
	const CREATE_NO_WINDOW = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW,
	}
}

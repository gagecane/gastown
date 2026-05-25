//go:build !windows

package tmux

import (
	"os/exec"
	"syscall"
)

// detachCmd configures a command to run as a fully detached process that
// survives the parent's exit. On Unix this creates a new session via setsid.
func detachCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}

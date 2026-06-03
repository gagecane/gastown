//go:build !windows

package lock

import (
	"os/exec"
	"syscall"
)

// setProcessGroup detaches the child into its own process group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

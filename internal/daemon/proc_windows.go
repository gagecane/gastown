//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// setSysProcAttr sets platform-specific process attributes.
// On Windows, detach the child into a new process group and suppress
// console-window creation so background subprocesses don't flash a
// visible window (the daemon itself runs with CREATE_NO_WINDOW).
func setSysProcAttr(cmd *exec.Cmd) {
	const CREATE_NO_WINDOW = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW,
	}
}

// sendTermSignal sends a termination signal.
// On Windows, there's no SIGTERM - we use Kill() directly.
func sendTermSignal(p *os.Process) error {
	return p.Kill()
}

// sendKillSignal sends a kill signal.
// On Windows, Kill() is the only option.
func sendKillSignal(p *os.Process) error {
	return p.Kill()
}

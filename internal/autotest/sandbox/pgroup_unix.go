//go:build unix

package sandbox

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup arranges for cmd to run in its own process
// group so killProcessGroup can reap children spawned by the
// subprocess (e.g. the test binary spawned by `go test`). On the
// Linux netns path, ApplyOffline already sets SysProcAttr; we
// merge the Setpgid flag rather than overwrite, so the namespace
// flags survive.
//
// Setpgid=true asks the kernel to put the child in its own
// process group whose pgid equals its pid. killProcessGroup then
// signals the negative-pid (the entire group).
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGKILL to cmd's process group. Falls
// back to killing the immediate process if the process-group
// signal fails (e.g. the child has already exited and its pgid is
// gone). Errors are intentionally swallowed: this is a best-effort
// reap on the timeout path, and the caller already knows the run
// failed.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// Negative pid signals the whole group. SIGKILL is
	// non-catchable so the child cannot ignore it.
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return
	}
	// Fall back to direct kill — happens when Setpgid didn't take
	// effect or the child already exited.
	_ = cmd.Process.Kill()
}

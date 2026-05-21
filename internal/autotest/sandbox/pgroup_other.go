//go:build !unix

package sandbox

import "os/exec"

// configureProcessGroup is a no-op on platforms that don't expose
// POSIX process groups via syscall.SysProcAttr.Setpgid (notably
// plan9). On Windows, exec.Cmd uses Job Objects under the hood
// when SysProcAttr.CreationFlags includes CREATE_NEW_PROCESS_GROUP,
// but the synthesis pins the substrate to Linux so we leave the
// non-unix path as a no-op rather than introduce a Windows
// dependency we never exercise.
func configureProcessGroup(cmd *exec.Cmd) {
	_ = cmd
}

// killProcessGroup falls back to killing the immediate child
// process on platforms without process-group semantics.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

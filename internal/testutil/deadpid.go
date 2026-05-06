package testutil

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
)

// DeadPID returns a PID that maps to a process which has been spawned, exited,
// and fully reaped. By the time this function returns, the process is no
// longer alive and the PID is free.
//
// This is the preferred way for tests to obtain a "dead" PID. It replaces the
// unsafe pattern of hard-coding a high PID number (e.g. 4194303) or
// `syscall.Getpid() - N` and hoping nothing is using it, which fails on hosts
// where the kernel's PID space has wrapped close to pid_max or where a live
// process happens to sit at the guessed PID.
//
// Because the kernel may reassign the returned PID to a fresh process at any
// moment after we reap the child, this helper verifies the PID is actually
// dead before returning it, and retries up to 10 times if the PID is reused
// faster than expected. On the Linux kernels where the original bug was
// observed, PID allocation is sequential with wrap, so a freshly-reaped PID
// is very unlikely to be reassigned before the caller's assertion runs.
//
// On Windows, the "is alive" check is not performed — DeadPID returns the PID
// immediately after the child is reaped. Tests that rely on POSIX kill(pid, 0)
// semantics are already Unix-specific in practice.
func DeadPID(t testing.TB) int {
	t.Helper()

	const maxAttempts = 10
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		cmd := newQuickExitCmd()
		if err := cmd.Run(); err != nil {
			lastErr = err
			continue
		}
		if cmd.Process == nil {
			lastErr = errNoProcess
			continue
		}
		pid := cmd.Process.Pid
		if !deadPIDIsAlive(pid) {
			return pid
		}
	}

	if lastErr != nil {
		t.Fatalf("testutil.DeadPID: could not obtain dead PID after %d attempts: %v", maxAttempts, lastErr)
	} else {
		t.Fatalf("testutil.DeadPID: could not obtain dead PID after %d attempts (all reused too fast)", maxAttempts)
	}
	return 0
}

// newQuickExitCmd returns a command that exits immediately with status 0.
// The command is chosen to exist on stock systems without depending on tools
// that may not be on every $PATH (for example, `go` or `true` by shell builtin
// alone).
func newQuickExitCmd() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd.exe", "/c", "exit")
	}
	return exec.Command("/bin/sh", "-c", "exit 0")
}

// deadPIDIsAlive reports whether the given PID maps to a live process. On
// Unix it uses kill(pid, 0); ESRCH (no such process) means the PID is free.
// On Windows it unconditionally returns false — we trust the kernel to have
// freed the PID by the time Run() returned.
func deadPIDIsAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// errNoProcess is returned from DeadPID when cmd.Run succeeded but
// cmd.Process is nil (should never happen in practice, but guards against
// panicking on a surprising runtime state).
var errNoProcess = &runErrStr{msg: "exec.Cmd.Process is nil after Run"}

type runErrStr struct{ msg string }

func (e *runErrStr) Error() string { return e.msg }

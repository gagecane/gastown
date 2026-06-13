//go:build unix

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// liveDaemonBinaryVerdict reads the running daemon's own binary via
// /proc/<pid>/exe and compares it to the file on disk at that path, so the
// restart_pending dog can tell whether a requested daemon restart has ALREADY
// taken effect (gs-4n7i class 3). On non-Linux unix (e.g. darwin) there is no
// /proc/<pid>/exe, so readlink fails and the verdict is left undetermined — the
// caller falls back to the commit-based forward check.
func liveDaemonBinaryVerdict() liveBinaryVerdict {
	exePath := fmt.Sprintf("/proc/%d/exe", os.Getpid())
	link, err := os.Readlink(exePath)
	probe := procExeProbe{linkOK: err == nil, link: link}
	if err == nil {
		// Stat the magic /proc symlink: procfs resolves it to the inode the
		// process is actually executing, even if the file was deleted on disk.
		if fi, statErr := os.Stat(exePath); statErr == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				probe.runningDev = uint64(st.Dev)
				probe.runningIno = uint64(st.Ino)
			}
		}
		// Stat the link's target PATH: after an atomic-rename upgrade this holds
		// the NEW inode, so a mismatch with the running inode means stale.
		if fi, statErr := os.Stat(link); statErr == nil {
			probe.onDiskOK = true
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				probe.onDiskDev = uint64(st.Dev)
				probe.onDiskIno = uint64(st.Ino)
			}
		}
	}
	return decideLiveBinary(probe)
}

// setSysProcAttr sets platform-specific process attributes.
// On Unix, we detach from the process group so the server survives daemon restart.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// sendTermSignal sends SIGTERM for graceful shutdown.
func sendTermSignal(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}

// sendKillSignal sends SIGKILL for forced termination.
func sendKillSignal(p *os.Process) error {
	return p.Signal(syscall.SIGKILL)
}

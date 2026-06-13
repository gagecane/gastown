//go:build linux

package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// liveDaemonBinaryVerdict reads the running daemon's own binary via
// /proc/<pid>/exe and compares it to the file on disk at that path, so the
// restart_pending dog can tell whether a requested daemon restart has ALREADY
// taken effect (gs-4n7i class 3). Linux-only: /proc/<pid>/exe does not exist on
// darwin/windows, where the verdict is left undetermined and the caller falls
// back to the commit-based forward check.
func liveDaemonBinaryVerdict() liveBinaryVerdict {
	exePath := fmt.Sprintf("/proc/%d/exe", os.Getpid())
	link, err := os.Readlink(exePath)
	probe := procExeProbe{linkOK: err == nil, link: link}
	if err == nil {
		// Stat the magic /proc symlink: procfs resolves it to the inode the
		// process is actually executing, even if the file was deleted on disk.
		if fi, statErr := os.Stat(exePath); statErr == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				probe.runningDev = st.Dev
				probe.runningIno = st.Ino
			}
		}
		// Stat the link's target PATH: after an atomic-rename upgrade this holds
		// the NEW inode, so a mismatch with the running inode means stale.
		if fi, statErr := os.Stat(link); statErr == nil {
			probe.onDiskOK = true
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				probe.onDiskDev = st.Dev
				probe.onDiskIno = st.Ino
			}
		}
	}
	return decideLiveBinary(probe)
}

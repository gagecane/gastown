//go:build !linux

package daemon

// liveDaemonBinaryVerdict is undetermined off Linux: there is no /proc/<pid>/exe
// to compare the running image against the on-disk binary, so the restart_pending
// dog falls back to the commit-based forward check (gs-4n7i class 3).
func liveDaemonBinaryVerdict() liveBinaryVerdict {
	return liveBinaryVerdict{determined: false, detail: "no /proc/<pid>/exe on this platform"}
}

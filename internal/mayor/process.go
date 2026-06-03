package mayor

import "github.com/steveyegge/gastown/internal/liveness"

// acpProcessAlive reports whether the ACP proxy process with the given PID is
// still running. Delegates to the shared internal/liveness leaf.
func acpProcessAlive(pid int) bool {
	return liveness.PIDAlive(pid)
}

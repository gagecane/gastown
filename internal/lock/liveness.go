package lock

import "github.com/steveyegge/gastown/internal/liveness"

// processExists reports whether a process with the given PID exists and is
// alive. Delegates to the shared internal/liveness leaf.
func processExists(pid int) bool {
	return liveness.PIDAlive(pid)
}

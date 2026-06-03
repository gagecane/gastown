package nudge

import "github.com/steveyegge/gastown/internal/liveness"

// pollerProcessAlive reports whether the poller process with the given PID is
// still running. Delegates to the shared internal/liveness leaf.
func pollerProcessAlive(pid int) bool {
	return liveness.PIDAlive(pid)
}

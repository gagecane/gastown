package cmd

import "github.com/steveyegge/gastown/internal/liveness"

// processAlive reports whether a process with the given PID is alive.
// Delegates to the shared internal/liveness leaf.
func processAlive(pid int) bool {
	return liveness.PIDAlive(pid)
}

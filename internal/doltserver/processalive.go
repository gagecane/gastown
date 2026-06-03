package doltserver

import "github.com/steveyegge/gastown/internal/liveness"

// processIsAlive reports whether a process with the given PID is still
// running. Delegates to the shared internal/liveness leaf.
func processIsAlive(pid int) bool {
	return liveness.PIDAlive(pid)
}

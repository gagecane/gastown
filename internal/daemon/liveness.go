package daemon

import (
	"os"

	"github.com/steveyegge/gastown/internal/liveness"
)

// isProcessAlive reports whether the given process is still running.
// Adapter over the shared internal/liveness leaf, keyed on the process PID.
func isProcessAlive(p *os.Process) bool {
	if p == nil {
		return false
	}
	return liveness.PIDAlive(p.Pid)
}

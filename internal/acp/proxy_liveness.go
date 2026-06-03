package acp

import "github.com/steveyegge/gastown/internal/liveness"

// isProcessAlive checks if the agent process is still running. Delegates the
// PID probe to the shared internal/liveness leaf after guarding the unstarted
// case (no command, or command not yet spawned).
func (p *Proxy) isProcessAlive() bool {
	if p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	return liveness.PIDAlive(p.cmd.Process.Pid)
}

package daemon

import (

	"github.com/steveyegge/gastown/internal/mayor"
	"github.com/steveyegge/gastown/internal/tmux"
)

// ensureMayorRunning ensures the Mayor is running.
// Uses mayor.Manager for consistent startup behavior.
// If the tmux session exists but the agent is dead (zombie), the daemon
// stops the zombie session and starts a fresh one.
func (d *Daemon) ensureMayorRunning() {
	mgr := mayor.NewManager(d.config.TownRoot)

	if err := mgr.Start(""); err != nil {
		if err == mayor.ErrAlreadyRunning {
			// Session exists — verify agent is actually alive.
			// During handoffs the agent is briefly undetectable, so we
			// only restart if the session has been a zombie for multiple
			// consecutive patrol cycles (debounce).
			if !d.isMayorAgentAlive(mgr) {
				d.mayorZombieCount++
				if d.mayorZombieCount >= 3 {
					d.logger.Printf("Mayor zombie detected (%d cycles), restarting", d.mayorZombieCount)
					if stopErr := mgr.Stop(); stopErr != nil && stopErr != mayor.ErrNotRunning {
						d.logger.Printf("Error stopping zombie Mayor: %v", stopErr)
						return
					}
					d.mayorZombieCount = 0
					if startErr := mgr.Start(""); startErr != nil {
						d.logger.Printf("Error restarting Mayor after zombie cleanup: %v", startErr)
						return
					}
					d.logger.Println("Mayor restarted after zombie cleanup")
				} else {
					d.logger.Printf("Mayor agent not detected (cycle %d/3), waiting before restart", d.mayorZombieCount)
				}
			} else {
				d.mayorZombieCount = 0
			}
			return
		}
		d.logger.Printf("Error starting Mayor: %v", err)
		return
	}

	d.mayorZombieCount = 0
	d.logger.Println("Mayor started successfully")
}
// isMayorAgentAlive checks if the Mayor's agent process is running in tmux.
func (d *Daemon) isMayorAgentAlive(mgr *mayor.Manager) bool {
	t := tmux.NewTmux()
	return t.IsAgentAlive(mgr.SessionName())
}

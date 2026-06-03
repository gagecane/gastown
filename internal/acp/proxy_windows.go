//go:build windows

package acp

import (
	"os"

	"github.com/steveyegge/gastown/internal/util"
)

// signalsToHandle returns the signals that Forward() should listen for.
// On Windows, only os.Interrupt is available (CTRL+C).
func signalsToHandle() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// setupProcessGroup configures the command to suppress the transient console
// window that Windows creates for console-subsystem children spawned from a
// GUI/no-console parent (e.g. the daemon).
func (p *Proxy) setupProcessGroup() {
	util.SetDetachedProcessGroup(p.cmd)
}

// terminateProcess kills the agent process.
// On Windows, we use Process.Kill() as there's no graceful SIGTERM equivalent.
func (p *Proxy) terminateProcess() {
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
}

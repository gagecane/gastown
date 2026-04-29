package acp

import (
	"context"
	"os"
	"time"
)

// monitorPIDFile watches the configured PID file and triggers a graceful
// shutdown (cancel notification + Shutdown) when it is removed. Useful for
// external supervisors to request termination without sending a signal.
func (p *Proxy) monitorPIDFile(ctx context.Context) {
	if p.pidFilePath == "" {
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-ticker.C:
			if _, err := os.Stat(p.pidFilePath); os.IsNotExist(err) {
				logEvent(p.townRoot, "acp_shutdown", "PID file removed, initiating graceful shutdown")
				debugLog(p.townRoot, "[Proxy] PID file removed, initiating graceful shutdown")
				_ = p.SendCancelNotification()
				p.Shutdown()
				return
			}
		}
	}
}

// Shutdown performs a one-shot orderly shutdown: marks the proxy as
// shutting down, closes the done channel, cancels the context, closes the
// agent's stdin/stdout pipes, and terminates the agent process. Safe to
// call multiple times; subsequent calls are no-ops.
func (p *Proxy) Shutdown() {
	p.shutdownOnce.Do(func() {
		debugLog(p.townRoot, "[Proxy] Shutdown: initiating graceful shutdown")
		p.isShuttingDown.Store(true)
		p.markDone()

		if p.cancel != nil {
			p.cancel()
		}

		p.stdinMux.Lock()
		if p.agentStdin != nil {
			_ = p.agentStdin.Close()
			p.agentStdin = nil
		}
		p.stdinMux.Unlock()

		if p.agentStdout != nil {
			_ = p.agentStdout.Close()
		}

		// Platform-specific process termination
		p.terminateProcess()
	})
}

// logCrashDiagnostics emits a snapshot of proxy state (process liveness,
// session ID, current mode, heartbeat config, last activity, etc.) to the
// debug log. Called from forwardFromAgent on EOF / read errors to help
// diagnose agent crashes.
func (p *Proxy) logCrashDiagnostics(reason string) {
	// Gather comprehensive crash diagnostics
	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	p.modeMux.RLock()
	currentMode := p.currentModeID
	heartbeatMethod := p.heartbeatMethod
	p.modeMux.RUnlock()

	p.promptMux.Lock()
	activePromptID := p.activePromptID
	p.promptMux.Unlock()

	lastActivity := time.Since(time.Unix(0, p.lastActivity.Load()))
	heartbeatSupported := p.heartbeatSupported.Load()
	isShuttingDown := p.isShuttingDown.Load()

	// Check if process is still alive
	processAlive := p.isProcessAlive()

	debugLog(p.townRoot, "[Proxy] === CRASH DIAGNOSTICS ===")
	debugLog(p.townRoot, "[Proxy] Reason: %s", reason)
	debugLog(p.townRoot, "[Proxy] Process alive: %v, Shutting down: %v", processAlive, isShuttingDown)
	debugLog(p.townRoot, "[Proxy] Agent busy: %v, Active prompt: %s", activePromptID != "", activePromptID)
	debugLog(p.townRoot, "[Proxy] Last activity: %v ago", lastActivity)
	debugLog(p.townRoot, "[Proxy] Session ID: %s", sessionID)
	debugLog(p.townRoot, "[Proxy] Current mode: %s", currentMode)
	debugLog(p.townRoot, "[Proxy] Heartbeat: method=%s, supported=%v", heartbeatMethod, heartbeatSupported)
	debugLog(p.townRoot, "[Proxy] =========================")
}

// markDone closes the done channel exactly once, signalling all goroutines
// to exit. Safe to call multiple times.
func (p *Proxy) markDone() {
	p.doneOnce.Do(func() {
		close(p.done)
	})
}

// agentDone returns a channel that receives the agent process exit error
// (or nil) once the agent process terminates.
func (p *Proxy) agentDone() <-chan error {
	ch := make(chan error, 1)
	go func() {
		err := p.cmd.Wait()
		ch <- err
	}()
	return ch
}

// truncateStr returns s unchanged if it fits within maxLen, or a
// "…"-terminated truncation otherwise. Used in log messages to cap the
// size of attacker-controlled content that may flow through.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

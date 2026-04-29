package acp

import (
	"encoding/json"
	"fmt"
	"time"
)

// runKeepAlive periodically injects a no-op request into the agent session
// to keep it responsive when the UI has gone idle. It prefers
// session/set_mode (re-setting the current mode is a no-op that resets the
// agent's idle timer) and falls back to a custom _ping method. The
// heartbeat is skipped while the agent is busy handling a prompt, while
// shutdown is in progress, or while the proxy is in "propelled" autonomous
// mode. If the agent has been marked busy for an abnormally long time with
// no activity, the busy state is force-cleared and a heartbeat is sent.
func (p *Proxy) runKeepAlive(tickerChan <-chan time.Time) {
	defer p.wg.Done()
	debugLog(p.townRoot, "[Proxy] runKeepAlive: loop started")

	for {
		select {
		case <-p.done:
			debugLog(p.townRoot, "[Proxy] runKeepAlive: done channel closed, exiting loop")
			return
		case <-tickerChan:
			if p.isShuttingDown.Load() {
				debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping (shutting down)")
				return
			}

			if p.Propelled.Load() {
				debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping heartbeat (propelled=true)")
				continue
			}

			// Don't send heartbeat if we're currently in a turn
			p.promptMux.Lock()
			busyID := p.activePromptID
			p.promptMux.Unlock()

			last := p.lastActivity.Load()
			idleTime := time.Since(time.Unix(0, last))

			if busyID != "" {
				// FORCE RECOVERY: If busy but no activity for 60s, clear state and heartbeat
				if idleTime > 60*time.Second {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: busy state stuck (id=%s) for %v, forcing recovery", busyID, idleTime)
					p.promptMux.Lock()
					p.activePromptID = ""
					p.promptMux.Unlock()
				} else {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping heartbeat, agent is busy (id=%s)", busyID)
					continue
				}
			}

			// If idle for more than 45 seconds, send a heartbeat
			if idleTime > 45*time.Second {
				p.sessionMux.RLock()
				sid := p.sessionID
				p.sessionMux.RUnlock()

				if sid == "" {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping heartbeat, no sessionID available")
					continue
				}

				// Check if heartbeat is supported and which method to use
				if !p.heartbeatSupported.Load() {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: heartbeat not supported by agent, skipping")
					continue
				}

				p.modeMux.RLock()
				method := p.heartbeatMethod
				currentMode := p.currentModeID
				p.modeMux.RUnlock()

				id := fmt.Sprintf("gt-inject-keepalive-%d", time.Now().UnixNano())

				var msg *JSONRPCMessage

				// Try session/set_mode with current mode (no-op that resets timer)
				if method == "set_mode" && currentMode != "" {
					params := map[string]any{
						"sessionId": sid,
						"modeId":    currentMode, // Set to current mode = no-op
					}
					paramsBytes, _ := json.Marshal(params)

					msg = &JSONRPCMessage{
						JSONRPC: "2.0",
						Method:  "session/set_mode",
						ID:      id,
						Params:  paramsBytes,
					}
					debugLog(p.townRoot, "[Proxy] runKeepAlive: sending heartbeat (session/set_mode mode=%s, idle=%v)", currentMode, idleTime)
				} else {
					// Fallback: try custom _ping method (ACP allows custom methods prefixed with _)
					msg = &JSONRPCMessage{
						JSONRPC: "2.0",
						Method:  "_ping",
						ID:      id,
						Params:  json.RawMessage("{}"),
					}
					debugLog(p.townRoot, "[Proxy] runKeepAlive: sending heartbeat (_ping, idle=%v)", idleTime)
				}

				if err := p.writeToAgent(msg); err != nil {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: heartbeat failed: %v", err)
				}
			} else {
				debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping heartbeat, idle time (%v) < threshold (45s)", idleTime)
			}
		}
	}
}

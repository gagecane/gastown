package acp

import (
	"encoding/json"
	"fmt"
)

// trackHandshakeResponse advances the handshake state machine when the agent
// replies to initialize/session/new. Returns true once the handshake is
// complete AND a startup prompt has been configured, signalling to the
// caller that injectStartupPrompt should be invoked.
func (p *Proxy) trackHandshakeResponse(msg *JSONRPCMessage) bool {
	if msg.ID == nil || msg.Result == nil {
		return false
	}

	p.handshakeMux.Lock()
	defer p.handshakeMux.Unlock()

	if p.handshakeState == handshakeWaitingForInit {
		p.handshakeState = handshakeWaitingForSessionNew
		return false
	}

	if p.handshakeState == handshakeWaitingForSessionNew && p.sessionID != "" {
		p.handshakeState = handshakeComplete
		return p.getStartupPrompt() != ""
	}

	return false
}

// injectStartupPrompt sends the configured startup prompt to the agent once
// the handshake has completed. The response is handled asynchronously by
// forwardFromAgent / trackPromptResponse, which transitions the startup
// prompt state to complete.
func (p *Proxy) injectStartupPrompt() error {
	prompt := p.getStartupPrompt()
	if prompt == "" {
		p.setStartupPromptState(startupPromptStateIdle)
		return nil
	}

	p.setStartupPromptState(startupPromptStateInjecting)

	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	params := map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": prompt},
		},
	}
	paramsBytes, _ := json.Marshal(params)

	req := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "gastown-startup-prompt",
		Method:  "session/prompt",
		Params:  paramsBytes,
	}

	if err := p.writeToAgent(&req); err != nil {
		p.setStartupPromptState(startupPromptStateFailed)
		return fmt.Errorf("sending startup prompt: %w", err)
	}

	// We no longer block here. The response will be handled by forwardFromAgent
	// and trackPromptResponse will update the startupPromptState to complete
	// when the response is received.
	return nil
}

// extractSessionID records the session ID (and initial mode state for
// heartbeat support) from a successful session/new response.
func (p *Proxy) extractSessionID(msg *JSONRPCMessage) {
	if msg.ID != nil && msg.Result != nil {
		var result SessionNewResult
		if err := json.Unmarshal(msg.Result, &result); err == nil && result.SessionID != "" {
			p.sessionMux.Lock()
			p.sessionID = result.SessionID
			p.sessionMux.Unlock()
			debugLog(p.townRoot, "[Proxy] extractSessionID: extracted sessionID=%s", result.SessionID)

			// Extract mode information for heartbeat support
			if result.Modes != nil && result.Modes.CurrentModeID != "" {
				p.modeMux.Lock()
				p.currentModeID = result.Modes.CurrentModeID
				p.modeMux.Unlock()
				debugLog(p.townRoot, "[Proxy] extractSessionID: extracted currentModeId=%s", result.Modes.CurrentModeID)

				// Enable heartbeat using session/set_mode with current mode
				if p.heartbeatMethod == "" {
					p.heartbeatMethod = "set_mode"
					p.heartbeatSupported.Store(true)
					debugLog(p.townRoot, "[Proxy] extractSessionID: enabling heartbeat via session/set_mode")
				}
			}
		}
	}
}

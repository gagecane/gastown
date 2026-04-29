package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// trackPromptResponse clears the active prompt ID (and optionally the
// propulsion flag) when the agent replies to a prompt whose ID we recorded
// in writeToAgent. This is what lets IsBusy() and runKeepAlive know when a
// turn has finished.
func (p *Proxy) trackPromptResponse(msg *JSONRPCMessage) {
	if msg.ID == nil {
		return
	}

	p.promptMux.Lock()
	defer p.promptMux.Unlock()

	if p.activePromptID == "" {
		return
	}

	var idStr string
	if s, ok := msg.ID.(string); ok {
		idStr = s
	} else {
		idStr = fmt.Sprintf("%v", msg.ID)
	}

	if idStr == p.activePromptID {
		debugLog(p.townRoot, "[Proxy] trackPromptResponse: prompt completed (id=%s)", idStr)
		p.activePromptID = ""

		// Reset propulsion mode when a prompt completes (Turn ends)
		if p.Propelled.Load() {
			debugLog(p.townRoot, "[Proxy] trackPromptResponse: resetting Propelled flag and buffer")
			p.SetPropelled(false)
			p.propulsionBuffer = ""
		}

		if idStr == "gastown-startup-prompt" {
			p.setStartupPromptState(startupPromptStateComplete)
		}
	}
}

// InjectNotificationToUI sends a JSON-RPC notification (no ID) to the UI.
// session/update notifications require a populated session ID; other
// methods may be sent as long as the proxy is not shutting down.
func (p *Proxy) InjectNotificationToUI(method string, params any) error {
	if p.isShuttingDown.Load() {
		return fmt.Errorf("proxy is shutting down")
	}

	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	if method == "session/update" && sessionID == "" {
		return fmt.Errorf("cannot inject session/update: empty sessionID")
	}

	msg := JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
	}

	if sessionID != "" || params != nil {
		paramMap := make(map[string]any)
		if sessionID != "" {
			paramMap["sessionId"] = sessionID
		}
		if params != nil {
			if v, ok := params.(map[string]any); ok {
				for k, val := range v {
					paramMap[k] = val
				}
			} else {
				paramMap["params"] = params
			}
		}
		rawParams, _ := json.Marshal(paramMap)
		msg.Params = rawParams
	}

	debugLog(p.townRoot, "[Proxy] Injecting notification to UI: method=%s sessionId=%s", method, sessionID)
	p.stdoutMux.Lock()
	err := p.uiEncoder.Encode(&msg)
	p.stdoutMux.Unlock()
	return err
}

// InjectPrompt sends a synthetic session/prompt to the agent with a
// gt-inject-prompt-* ID so the response is filtered out of the UI stream.
// Callers should use this for operator-injected prompts (e.g. nudges).
// If the startup prompt is still in flight, waits briefly for readiness
// before rejecting with a "busy" error.
func (p *Proxy) InjectPrompt(prompt string) error {
	if p.isShuttingDown.Load() {
		return fmt.Errorf("proxy is shutting down")
	}

	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	if sessionID == "" {
		return fmt.Errorf("cannot inject prompt: empty sessionID")
	}

	// Check if agent is busy to prevent race conditions.
	// If startup prompt is still in-flight, wait briefly for readiness.
	if p.IsBusy() {
		state := p.getStartupPromptState()
		if state == startupPromptStatePending || state == startupPromptStateInjecting {
			waitCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := p.WaitForReady(waitCtx); err != nil {
				debugLog(p.townRoot, "[Proxy] InjectPrompt: agent still busy after waiting for startup readiness: %v", err)
				return fmt.Errorf("agent is busy processing another prompt")
			}
		} else {
			debugLog(p.townRoot, "[Proxy] InjectPrompt: agent is busy, skipping injection to prevent race condition")
			return fmt.Errorf("agent is busy processing another prompt")
		}
	}

	params := map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": prompt},
		},
	}
	paramsBytes, _ := json.Marshal(params)

	req := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("gt-inject-prompt-%d", time.Now().UnixNano()),
		Method:  "session/prompt",
		Params:  paramsBytes,
	}

	logEvent(p.townRoot, "acp_prompt", fmt.Sprintf("injecting prompt: %s", truncateStr(prompt, 100)))
	debugLog(p.townRoot, "[Proxy] Injecting prompt to agent: sessionId=%s text=%q", sessionID, truncateStr(prompt, 50))
	return p.writeToAgent(&req)
}

// SessionID returns the current session ID, or "" if the session has not
// yet been established.
func (p *Proxy) SessionID() string {
	p.sessionMux.RLock()
	defer p.sessionMux.RUnlock()
	return p.sessionID
}

// WaitForSessionID blocks until the session ID has been observed or the
// context is cancelled.
func (p *Proxy) WaitForSessionID(ctx context.Context) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		p.sessionMux.RLock()
		sid := p.sessionID
		p.sessionMux.RUnlock()

		if sid != "" {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return fmt.Errorf("proxy shutting down")
		case <-ticker.C:
		}
	}
}

// WaitForReady blocks until the session ID is available AND the agent is
// not mid-prompt (with the startup prompt in a terminal state). Returns
// early if the proxy is shutting down or the context is cancelled.
func (p *Proxy) WaitForReady(ctx context.Context) error {
	if err := p.WaitForSessionID(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if p.isShuttingDown.Load() {
			return fmt.Errorf("proxy is shutting down")
		}

		p.promptMux.Lock()
		busy := p.activePromptID != ""
		p.promptMux.Unlock()

		state := p.getStartupPromptState()
		if !busy && (state == startupPromptStateIdle || state == startupPromptStateComplete || state == startupPromptStateFailed) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return fmt.Errorf("proxy shutting down")
		case <-ticker.C:
		}
	}
}

// IsBusy reports whether the agent is currently processing a prompt.
func (p *Proxy) IsBusy() bool {
	p.promptMux.Lock()
	defer p.promptMux.Unlock()
	return p.activePromptID != ""
}

// SendCancelNotification sends a session/cancel notification to the agent.
// Silently no-ops when no session has been established yet.
func (p *Proxy) SendCancelNotification() error {
	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	if sessionID == "" {
		return nil
	}

	debugLog(p.townRoot, "[Proxy] Sending session/cancel notification for session %s", sessionID)
	params := map[string]any{"sessionId": sessionID}
	paramsBytes, _ := json.Marshal(params)

	notification := JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "session/cancel",
		Params:  paramsBytes,
	}

	return p.writeToAgent(&notification)
}

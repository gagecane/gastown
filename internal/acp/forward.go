package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/style"
)

// Forward runs the main proxy loop: spawns goroutines that shuttle messages
// between the UI (stdin/stdout) and the agent subprocess, alongside a
// keepalive ticker and optional PID file monitor. It blocks until the agent
// exits, the done channel is closed, or a signal is received, then performs
// orderly shutdown.
func (p *Proxy) Forward() error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, signalsToHandle()...)
	defer signal.Stop(sigChan)

	defer p.Shutdown()

	errChan := make(chan error, 1)
	p.wg.Add(3)
	go p.forwardToAgent()
	go p.forwardFromAgent()

	keepAliveTicker := time.NewTicker(30 * time.Second)
	defer keepAliveTicker.Stop()
	go p.runKeepAlive(keepAliveTicker.C)

	if p.pidFilePath != "" {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.monitorPIDFile(p.ctx)
		}()
	}

	go func() {
		errChan <- p.cmd.Wait()
	}()

	var exitErr error
	select {
	case <-sigChan:
		debugLog(p.townRoot, "[Proxy] Forward: received signal")
	case <-p.done:
		debugLog(p.townRoot, "[Proxy] Forward: done channel signaled")
	case err := <-errChan:
		exitErr = err
		debugLog(p.townRoot, "[Proxy] Forward: agent process exited: %v", err)
	}

	p.Shutdown()

	doneChan := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(doneChan)
	}()

	select {
	case <-doneChan:
		debugLog(p.townRoot, "[Proxy] Forward: all goroutines exited")
	case <-time.After(200 * time.Millisecond):
		debugLog(p.townRoot, "[Proxy] Forward: wait timeout, proceeding with exit")
	}

	if exitErr != nil {
		logEvent(p.townRoot, "acp_error", fmt.Sprintf("agent exited with error: %v", exitErr))
		debugLog(p.townRoot, "[Proxy] Agent exited with error: %v", exitErr)
		return exitErr
	}
	return nil
}

// writeToAgent encodes a JSON-RPC message to the agent's stdin with locking
// and liveness checks. Returns an error if the proxy is shutting down, the
// agent is not running, or the encode fails. Also records active prompt IDs
// so runKeepAlive can tell when the agent is busy.
func (p *Proxy) writeToAgent(msg any) error {
	method := "unknown"
	var id any
	if m, ok := msg.(*JSONRPCMessage); ok {
		method = m.Method
		id = m.ID
	}

	if p.isShuttingDown.Load() {
		debugLog(p.townRoot, "[Proxy] writeToAgent: dropped write during shutdown (method=%s id=%v)", method, id)
		return fmt.Errorf("proxy is shutting down")
	}

	p.stdinMux.Lock()
	defer p.stdinMux.Unlock()

	if p.isShuttingDown.Load() {
		return fmt.Errorf("proxy is shutting down")
	}

	if p.agentStdin == nil {
		return fmt.Errorf("agent stdin is nil")
	}

	if !p.isProcessAlive() {
		debugLog(p.townRoot, "[Proxy] writeToAgent: failed (process dead) (method=%s id=%v)", method, id)
		return fmt.Errorf("agent process is not running")
	}

	isPrompt := false
	if m, ok := msg.(*JSONRPCMessage); ok && m.Method == "session/prompt" && m.ID != nil {
		isPrompt = true
		p.promptMux.Lock()
		if idStr, ok := m.ID.(string); ok {
			p.activePromptID = idStr
		} else {
			p.activePromptID = fmt.Sprintf("%v", m.ID)
		}
		debugLog(p.townRoot, "[Proxy] writeToAgent: marking busy (id=%s)", p.activePromptID)
		p.promptMux.Unlock()
	}

	p.lastActivity.Store(time.Now().UnixNano())
	debugLog(p.townRoot, "[Proxy] writeToAgent: encoding message (method=%s id=%v)", method, id)

	err := json.NewEncoder(p.agentStdin).Encode(msg)
	if err != nil {
		debugLog(p.townRoot, "[Proxy] writeToAgent: encode failed: %v", err)
		if isPrompt {
			p.promptMux.Lock()
			p.activePromptID = ""
			p.promptMux.Unlock()
		}
		return fmt.Errorf("writing to agent: %w", err)
	}

	return nil
}

// forwardToAgent reads JSON-RPC messages from the UI (stdin) and forwards
// them to the agent. Runs until stdin EOF, a read error, or shutdown.
func (p *Proxy) forwardToAgent() {
	defer p.wg.Done()
	defer func() {
		debugLog(p.townRoot, "[Proxy] forwardToAgent: exiting, triggering Shutdown()")
		p.Shutdown()
	}()

	// Use large buffer to handle large JSON messages from the UI
	reader := bufio.NewReaderSize(p.stdin, 1024*1024)
	receivedInput := false

	for {
		select {
		case <-p.done:
			debugLog(p.townRoot, "[Proxy] forwardToAgent: done channel closed, exiting")
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if !receivedInput && p.handshakeState == handshakeInit {
					logEvent(p.townRoot, "acp_error", "stdin closed before handshake - no ACP client connected")
					debugLog(p.townRoot, "[Proxy] stdin closed before handshake - no ACP client connected?")
				} else {
					logEvent(p.townRoot, "acp_shutdown", "stdin EOF - ACP client disconnected")
					debugLog(p.townRoot, "[Proxy] forwardToAgent: stdin EOF (client disconnected)")
				}
			} else {
				debugLog(p.townRoot, "[Proxy] forwardToAgent: stdin read error: %v", err)
				p.markDone()
			}
			return
		}

		receivedInput = true

		var msg JSONRPCMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		// Log large messages that might cause issues
		if len(line) > 50000 {
			debugLog(p.townRoot, "[Proxy] forwardToAgent: large message received (size=%d, method=%s)", len(line), msg.Method)
		}

		p.trackHandshakeRequest(&msg)

		if err := p.writeToAgent(&msg); err != nil {
			debugLog(p.townRoot, "[Proxy] forwardToAgent: writeToAgent failed: %v", err)
			p.markDone()
			return
		}
	}
}

// trackHandshakeRequest advances the handshake state machine when the
// initialize request arrives from the UI.
func (p *Proxy) trackHandshakeRequest(msg *JSONRPCMessage) {
	if msg.Method == "" {
		return
	}

	p.handshakeMux.Lock()
	defer p.handshakeMux.Unlock()

	if msg.Method == "initialize" && p.handshakeState == handshakeInit {
		debugLog(p.townRoot, "[Proxy] Handshake: initialize request received from UI")
		p.handshakeState = handshakeWaitingForInit
	}
}

// forwardFromAgent reads JSON-RPC messages from the agent and forwards them
// to the UI, while also updating session state, tracking prompt completion,
// detecting propulsion triggers, and filtering out redacted thought chunks
// and responses to internally injected prompts.
func (p *Proxy) forwardFromAgent() {
	defer p.wg.Done()

	// Use large buffer to handle bursts of large JSON messages (e.g. build logs)
	reader := bufio.NewReaderSize(p.agentStdout, 1024*1024)

	for {
		select {
		case <-p.done:
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: done channel closed, exiting")
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				debugLog(p.townRoot, "[Proxy] forwardFromAgent: agent stdout EOF (agent terminated)")
				p.logCrashDiagnostics("agent stdout EOF")
				logEvent(p.townRoot, "acp_shutdown", "agent stdout EOF - agent terminated gracefully")
				p.markDone()
			} else {
				logEvent(p.townRoot, "acp_error", fmt.Sprintf("agent stdout read error: %v", err))
				debugLog(p.townRoot, "[Proxy] forwardFromAgent: agent stdout read error: %v", err)
				p.logCrashDiagnostics(fmt.Sprintf("read error: %v", err))
				p.markDone()
			}
			return
		}

		var msg JSONRPCMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// Check for raw propulsion triggers if not valid JSON
			p.propulsionBuffer += line
			if len(p.propulsionBuffer) > 2000 {
				p.propulsionBuffer = p.propulsionBuffer[len(p.propulsionBuffer)-2000:]
			}

			if isPropulsionTrigger(p.propulsionBuffer) {
				debugLog(p.townRoot, "[Proxy] forwardFromAgent: propulsion trigger detected in raw output")
				p.SetPropelled(true)
				p.propulsionBuffer = "" // Reset after detection
			}
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: failed to parse JSON (size=%d): %v", len(line), err)
			continue
		}

		// Log large messages that might cause issues
		if len(line) > 50000 {
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: large message received (size=%d, method=%s)", len(line), msg.Method)
		}

		p.lastActivity.Store(time.Now().UnixNano())
		p.extractSessionID(&msg)
		shouldInjectPrompt := p.trackHandshakeResponse(&msg)
		p.trackPromptResponse(&msg)

		// Check for propulsion triggers in JSON messages (e.g. session/update)
		if checkPropulsionTrigger(&msg) {
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: propulsion trigger detected in JSON message")
			p.SetPropelled(true)
		}

		// Filter out responses to injected prompts so the UI doesn't get confused
		isInjectedResponse := false
		idStr := ""
		if id, ok := msg.ID.(string); ok && strings.HasPrefix(id, "gt-inject-") {
			isInjectedResponse = true
			idStr = id
		}

		if isInjectedResponse && msg.Error != nil {
			debugLog(p.townRoot, "[Proxy] Injected prompt %v failed: %d %s", msg.ID, msg.Error.Code, msg.Error.Message)

			// If heartbeat method fails, disable heartbeat to avoid repeated failures
			if strings.Contains(idStr, "keepalive") {
				debugLog(p.townRoot, "[Proxy] Heartbeat method failed, disabling heartbeat")
				p.heartbeatSupported.Store(false)
			}
		}

		// Log successful heartbeat responses at debug level
		if isInjectedResponse && msg.Error == nil {
			debugLog(p.townRoot, "[Proxy] Heartbeat successful (id=%v)", msg.ID)
		}

		// Filter out redacted thought chunks - they shouldn't be shown to the UI
		// as they create a confusing "Thinking" state when the agent has finished
		if isRedactedThought(&msg) {
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: filtering out redacted thought chunk")
			continue
		}

		if !isInjectedResponse && !p.Propelled.Load() {
			p.stdoutMux.Lock()
			err = p.uiEncoder.Encode(&msg)
			p.stdoutMux.Unlock()
		}
		if err != nil {
			logEvent(p.townRoot, "acp_error", fmt.Sprintf("failed to forward message to UI: %v", err))
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: failed to forward to UI: %v", err)
			p.markDone()
			return
		}

		if shouldInjectPrompt {
			if err := p.injectStartupPrompt(); err != nil {
				style.PrintWarning("failed to inject startup prompt: %v", err)
			}
		}
	}
}

// forwardAgentStderr copies the agent's stderr to the proxy's stderr (for
// debugging), with truncation and statistics to prevent pipe saturation when
// the agent emits very large lines (e.g. permission ruleset dumps).
func (p *Proxy) forwardAgentStderr() {
	defer p.wg.Done()
	reader := bufio.NewReader(p.agentStderr)

	// Log stderr statistics periodically to detect pipe saturation
	statsTicker := time.NewTicker(30 * time.Second)
	defer statsTicker.Stop()

	for {
		select {
		case <-p.done:
			// Log final statistics on exit
			dropped := p.stderrBytesDropped.Load()
			truncated := p.stderrLinesTruncated.Load()
			if dropped > 0 || truncated > 0 {
				debugLog(p.townRoot, "[Proxy] Stderr statistics: %d lines truncated, %d bytes dropped", truncated, dropped)
			}
			return
		case <-statsTicker.C:
			// Log statistics periodically if there's activity
			dropped := p.stderrBytesDropped.Load()
			truncated := p.stderrLinesTruncated.Load()
			if dropped > 0 || truncated > 0 {
				debugLog(p.townRoot, "[Proxy] Stderr statistics: %d lines truncated, %d bytes dropped", truncated, dropped)
			}
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				debugLog(p.townRoot, "[Agent stderr] read error: %v", err)
			}
			return
		}

		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			continue
		}

		lineLen := len(line)

		// DROP very large lines entirely (likely permission ruleset dumps)
		// These can be 50KB+ and serve no debugging purpose
		if lineLen > 50000 {
			p.stderrBytesDropped.Add(int64(lineLen))
			p.stderrLinesTruncated.Add(1)

			// Only log the first few drops to avoid cascading saturation
			if p.stderrLinesTruncated.Load() <= 3 {
				debugLog(p.townRoot, "[Proxy] Dropping massive stderr line (%d bytes) to prevent pipe saturation", lineLen)
			}
			continue
		}

		// Truncate large lines to prevent pipe saturation
		// Keep more context than debug logs (5000 vs 2000 chars)
		outputLine := line
		if lineLen > 5000 {
			outputLine = line[:5000] + fmt.Sprintf("... (truncated from %d bytes)", lineLen)
			p.stderrLinesTruncated.Add(1)
		}

		// ALWAYS use truncated/tracked version to prevent pipe saturation
		fmt.Fprintln(os.Stderr, outputLine)

		// For debug log, use more aggressive truncation
		debugLine := line
		if lineLen > 2000 {
			debugLine = line[:2000] + "... (truncated)"
		}
		debugLog(p.townRoot, "[Agent] %s", debugLine)
	}
}

// isRedactedThought returns true for session/update messages that carry a
// redacted agent_thought_chunk. These are filtered so the UI does not show
// a phantom "thinking" indicator when the agent has actually finished.
func isRedactedThought(msg *JSONRPCMessage) bool {
	if msg.Method != "session/update" {
		return false
	}

	// Check if Params is empty
	if len(msg.Params) == 0 {
		return false
	}

	// Unmarshal params into a generic map
	var params map[string]interface{}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return false
	}

	update, ok := params["update"].(map[string]interface{})
	if !ok {
		return false
	}

	sessionUpdate, ok := update["sessionUpdate"].(string)
	if !ok || sessionUpdate != "agent_thought_chunk" {
		return false
	}

	content, ok := update["content"].(map[string]interface{})
	if !ok {
		return false
	}

	text, ok := content["text"].(string)
	if !ok {
		return false
	}

	return text == "[REDACTED]"
}

// Package acp implements an Agent Client Protocol (ACP) proxy: a broker
// that sits between a UI process (speaking JSON-RPC over stdio) and an
// agent subprocess (also speaking JSON-RPC over stdio). The proxy handles
// handshake orchestration, session tracking, keepalive heartbeats,
// propulsion-mode filtering, stderr shaping, and graceful shutdown.
//
// The implementation is intentionally split across several files to keep
// each concern manageable:
//
//   - proxy.go       (this file) — Proxy struct, construction, and Start
//   - proxy_types.go — JSON-RPC types, handshake/startup constants
//   - forward.go     — Forward loop, per-direction goroutines, writeToAgent
//   - handshake.go   — Handshake state machine + startup prompt injection
//   - session.go     — Session/prompt tracking, public inject/wait helpers
//   - keepalive.go   — Idle-time heartbeat loop
//   - lifecycle.go   — Shutdown, PID monitor, crash diagnostics
//   - proxy_unix.go / proxy_windows.go — Platform-specific process helpers
//   - propulsion.go  — Propulsion-mode detection and debug/event logging
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy is the core ACP broker: it owns the agent subprocess and the
// JSON-RPC streams on both sides, and exposes methods to start the agent,
// drive the forwarding loop (Forward), inject prompts/notifications, and
// shut everything down.
type Proxy struct {
	cmd                *exec.Cmd
	agentStdin         io.WriteCloser
	agentStdout        io.ReadCloser
	agentStderr        io.ReadCloser
	stdin              io.Reader
	stdout             io.Writer
	sessionID          string
	sessionMux         sync.RWMutex
	done               chan struct{}
	doneOnce           sync.Once
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	handshakeState     handshakeState
	handshakeMux       sync.Mutex
	promptMux          sync.Mutex
	activePromptID     string
	stdinMux           sync.Mutex
	stdoutMux          sync.Mutex
	uiEncoder          *json.Encoder
	startupPrompt      string
	startupPromptState string
	startupPromptMux   sync.RWMutex
	shutdownOnce       sync.Once
	isShuttingDown     atomic.Bool
	lastActivity       atomic.Int64
	pidFilePath        string
	townRoot           string
	// Heartbeat support
	currentModeID      string
	modeMux            sync.RWMutex
	heartbeatMethod    string // "custom_ping", "set_mode", or "disabled"
	heartbeatSupported atomic.Bool
	// Propulsion state
	Propelled        atomic.Bool
	propulsionBuffer string
	// Stderr monitoring for pipe saturation
	stderrBytesDropped   atomic.Int64
	stderrLinesTruncated atomic.Int64
	stderrLastLogTime    atomic.Int64
}

// NewProxy constructs a Proxy wired to os.Stdin/os.Stdout with its
// handshake state machine in the initial state.
func NewProxy() *Proxy {
	debugLog("", "[Proxy] Created new proxy, initial handshakeState=%d", handshakeInit)
	p := &Proxy{
		done:           make(chan struct{}),
		handshakeState: handshakeInit,
		stdin:          os.Stdin,
		stdout:         os.Stdout,
	}
	p.uiEncoder = json.NewEncoder(p.stdout)
	p.lastActivity.Store(time.Now().UnixNano())
	return p
}

// SetTownRoot sets the town root for logging important events to town.log.
func (p *Proxy) SetTownRoot(townRoot string) {
	p.townRoot = townRoot
}

// SetPropelled toggles the proxy's "propelled" flag, which suppresses
// forwarding of non-injected agent responses to the UI while autonomous
// work is in progress.
func (p *Proxy) SetPropelled(propelled bool) {
	p.Propelled.Store(propelled)
}

// setStreams overrides the default os.Stdin/os.Stdout streams (used by
// tests).
func (p *Proxy) setStreams(in io.Reader, out io.Writer) {
	p.stdin = in
	p.stdout = out
	p.uiEncoder = json.NewEncoder(out)
}

// SetPIDFilePath sets the path to the PID file for monitoring. If set, a
// goroutine watches the file and triggers graceful shutdown when it is
// removed.
func (p *Proxy) SetPIDFilePath(path string) {
	p.pidFilePath = path
}

// SetStartupPrompt configures a prompt to be injected once the handshake
// completes. Passing "" clears any configured startup prompt and puts the
// startup-prompt state machine in its idle state.
func (p *Proxy) SetStartupPrompt(prompt string) {
	p.startupPromptMux.Lock()
	p.startupPrompt = prompt
	if prompt == "" {
		p.startupPromptState = startupPromptStateIdle
	} else {
		p.startupPromptState = startupPromptStatePending
	}
	p.startupPromptMux.Unlock()
}

func (p *Proxy) getStartupPrompt() string {
	p.startupPromptMux.RLock()
	defer p.startupPromptMux.RUnlock()
	return p.startupPrompt
}

func (p *Proxy) setStartupPromptState(state string) {
	p.startupPromptMux.Lock()
	p.startupPromptState = state
	p.startupPromptMux.Unlock()
}

func (p *Proxy) getStartupPromptState() string {
	p.startupPromptMux.RLock()
	defer p.startupPromptMux.RUnlock()
	return p.startupPromptState
}

// Start spawns the agent subprocess with the given command, arguments, and
// working directory. Stdin/stdout/stderr pipes are attached, a child
// context is installed (so Shutdown can cancel the process), and a
// goroutine is launched to copy the agent's stderr to the proxy's stderr.
// On failure, any partially-acquired pipes are cleaned up before returning.
func (p *Proxy) Start(ctx context.Context, agentPath string, agentArgs []string, cwd string) error {
	childCtx, cancel := context.WithCancel(ctx)
	p.ctx = childCtx
	p.cancel = cancel

	p.cmd = exec.CommandContext(childCtx, agentPath, agentArgs...)
	p.cmd.Dir = cwd

	// Platform-specific process group setup
	p.setupProcessGroup()

	var err error
	p.agentStdin, err = p.cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("creating stdin pipe: %w", err)
	}

	p.agentStdout, err = p.cmd.StdoutPipe()
	if err != nil {
		cancel()
		p.stdinMux.Lock()
		if p.agentStdin != nil {
			_ = p.agentStdin.Close()
			p.agentStdin = nil
		}
		p.stdinMux.Unlock()
		_ = p.cmd.Wait()
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Capture agent stderr for debugging when GT_ACP_DEBUG=1
	p.agentStderr, err = p.cmd.StderrPipe()
	if err != nil {
		cancel()
		p.stdinMux.Lock()
		if p.agentStdin != nil {
			_ = p.agentStdin.Close()
			p.agentStdin = nil
		}
		p.stdinMux.Unlock()
		_ = p.agentStdout.Close()
		return fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := p.cmd.Start(); err != nil {
		cancel()
		p.stdinMux.Lock()
		if p.agentStdin != nil {
			_ = p.agentStdin.Close()
			p.agentStdin = nil
		}
		p.stdinMux.Unlock()
		return fmt.Errorf("starting agent: %w", err)
	}

	// Start goroutine to capture agent stderr and write to acp.log
	p.wg.Add(1)
	go p.forwardAgentStderr()

	return nil
}

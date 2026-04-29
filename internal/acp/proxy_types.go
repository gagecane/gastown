package acp

import (
	"encoding/json"
	"time"
)

// handshakeState tracks progress through the ACP handshake sequence
// (initialize → session/new → complete). Used by Proxy to decide when
// the startup prompt may be injected.
type handshakeState int

const (
	handshakeInit handshakeState = iota
	handshakeWaitingForInit
	handshakeWaitingForSessionNew
	handshakeComplete
)

// Startup prompt state machine. Used to coordinate between the optional
// startup prompt injection (SetStartupPrompt) and prompt injection calls
// from other goroutines (InjectPrompt, WaitForReady).
const (
	startupPromptStateIdle      = ""
	startupPromptStatePending   = "pending"
	startupPromptStateInjecting = "injecting"
	startupPromptStateComplete  = "complete"
	startupPromptStateFailed    = "failed"
)

// startupPromptTimeout is the maximum time to wait for the agent to respond
// to the startup prompt. If the agent doesn't respond within this time,
// the startup prompt is marked as failed and the proxy continues.
const startupPromptTimeout = 60 * time.Second

// JSONRPCMessage is a minimal JSON-RPC 2.0 envelope used on both the agent
// and UI sides of the proxy. It intentionally keeps Params/Result as
// json.RawMessage so callers can decode into concrete types lazily.
type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// SessionMode represents an available agent mode advertised during session/new.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeState represents the current mode state for a session.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SessionNewResult represents the result of a session/new request.
type SessionNewResult struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"`
}

// Package provider implements the Agent Client Protocol (ACP) integration
// layer for Gas Town. It defines the ACP wire-protocol types (JSON-RPC
// requests/responses, messages, content blocks, tools) and the provider
// abstraction that connects a Gas Town agent to an ACP-speaking client.
//
// The central abstraction is [ACPProvider], an interface for components that
// expose tools and handle messages over ACP. [BaseProvider] supplies the
// shared state machine and tool registry; [LocalProvider] is an in-process
// implementation backed by a tool callback. The remaining helpers convert
// between Gas Town messages and ACP types and marshal protocol values to and
// from JSON.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// ProviderState is the lifecycle state of an [ACPProvider] connection.
type ProviderState string

// Provider lifecycle states, reported via [BaseProvider.GetStatus].
const (
	StateDisconnected ProviderState = "disconnected"
	StateConnecting   ProviderState = "connecting"
	StateReady        ProviderState = "ready"
	StateBusy         ProviderState = "busy"
	StateError        ProviderState = "error"
)

// AgentStatus is a snapshot of a provider's current state and identity.
type AgentStatus struct {
	State     ProviderState `json:"state"`
	SessionID string        `json:"session_id,omitempty"`
	AgentName string        `json:"agent_name,omitempty"`
	Version   string        `json:"version,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// ToolCallback handles a tool invocation, returning its result or an error.
type ToolCallback func(ctx context.Context, name string, args map[string]any) (CallToolResult, error)

// SessionStartCallback is invoked when an ACP session begins, receiving the
// negotiated server info.
type SessionStartCallback func(ctx context.Context, info ServerInfo) error

// ACPProvider is the interface implemented by ACP integration providers. It
// covers the protocol handshake, tool discovery and invocation, message
// creation, status reporting, callback registration, and teardown.
type ACPProvider interface {
	Initialize(ctx context.Context, clientName, clientVersion string) (*InitializeResult, error)
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args map[string]any) (*CallToolResult, error)
	CreateMessage(ctx context.Context, params CreateMessageParams) (*CreateMessageResult, error)
	GetStatus() AgentStatus
	OnToolCall(callback ToolCallback)
	OnSessionStart(callback SessionStartCallback)
	Close() error
}

// ACPProviderConfig configures a new provider: its advertised name, version,
// initialization instructions, and the initial set of tools it exposes.
type ACPProviderConfig struct {
	Name         string
	Version      string
	Instructions string
	Tools        []Tool
}

// BaseProvider provides the shared state machine, tool registry, and callback
// storage common to all [ACPProvider] implementations. It is embedded by
// concrete providers such as [LocalProvider].
type BaseProvider struct {
	mu           sync.RWMutex
	state        ProviderState
	tools        []Tool
	toolCallback ToolCallback
	sessionStart SessionStartCallback
	status       AgentStatus
}

// NewBaseProvider creates a [BaseProvider] in the disconnected state,
// seeded with the tools and identity from config.
func NewBaseProvider(config ACPProviderConfig) *BaseProvider {
	return &BaseProvider{
		state: StateDisconnected,
		tools: config.Tools,
		status: AgentStatus{
			State:     StateDisconnected,
			AgentName: config.Name,
			Version:   config.Version,
		},
	}
}

func (p *BaseProvider) setState(state ProviderState) {
	p.mu.Lock()
	p.state = state
	p.status.State = state
	p.mu.Unlock()
}

// GetStatus returns a snapshot of the provider's current status.
func (p *BaseProvider) GetStatus() AgentStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status
}

// OnToolCall registers the callback invoked when a tool is called.
func (p *BaseProvider) OnToolCall(callback ToolCallback) {
	p.mu.Lock()
	p.toolCallback = callback
	p.mu.Unlock()
}

// OnSessionStart registers the callback invoked when an ACP session begins.
func (p *BaseProvider) OnSessionStart(callback SessionStartCallback) {
	p.mu.Lock()
	p.sessionStart = callback
	p.mu.Unlock()
}

// ListTools returns the tools currently registered with the provider.
func (p *BaseProvider) ListTools(ctx context.Context) ([]Tool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.tools, nil
}

// AddTool appends a tool to the provider's registry.
func (p *BaseProvider) AddTool(tool Tool) {
	p.mu.Lock()
	p.tools = append(p.tools, tool)
	p.mu.Unlock()
}

// RemoveTool removes the tool with the given name from the registry, if present.
func (p *BaseProvider) RemoveTool(name string) {
	p.mu.Lock()
	for i, t := range p.tools {
		if t.Name == name {
			p.tools = append(p.tools[:i], p.tools[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
}

// LocalProvider is an in-process [ACPProvider] that serves tools directly via
// the registered [ToolCallback] without spawning an external agent. It does
// not support sampling, so [LocalProvider.CreateMessage] always errors.
type LocalProvider struct {
	*BaseProvider
	instructions string
}

// NewLocalProvider creates a [LocalProvider] from config.
func NewLocalProvider(config ACPProviderConfig) *LocalProvider {
	return &LocalProvider{
		BaseProvider: NewBaseProvider(config),
		instructions: config.Instructions,
	}
}

// Initialize completes the ACP handshake, marking the provider ready and
// invoking any registered [SessionStartCallback]. It returns the negotiated
// [InitializeResult].
func (p *LocalProvider) Initialize(ctx context.Context, clientName, clientVersion string) (*InitializeResult, error) {
	p.setState(StateReady)
	result := &InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{ListChanged: false},
		},
		ServerInfo: ServerInfo{
			Name:    p.status.AgentName,
			Version: p.status.Version,
		},
		Instructions: p.instructions,
	}
	p.mu.RLock()
	callback := p.sessionStart
	p.mu.RUnlock()
	if callback != nil {
		if err := callback(ctx, result.ServerInfo); err != nil {
			return nil, fmt.Errorf("session start callback: %w", err)
		}
	}
	return result, nil
}

// CallTool dispatches a tool invocation to the registered [ToolCallback].
// Callback errors and a missing callback are reported as error results
// rather than returned as Go errors.
func (p *LocalProvider) CallTool(ctx context.Context, name string, args map[string]any) (*CallToolResult, error) {
	p.mu.RLock()
	callback := p.toolCallback
	p.mu.RUnlock()
	if callback == nil {
		return &CallToolResult{
			Content: []ContentBlock{NewTextContent("no tool callback registered")},
			IsError: true,
		}, nil
	}
	result, err := callback(ctx, name, args)
	if err != nil {
		return &CallToolResult{
			Content: []ContentBlock{NewTextContent(err.Error())},
			IsError: true,
		}, nil
	}
	return &result, nil
}

// CreateMessage is unsupported for a local provider and always returns an error.
func (p *LocalProvider) CreateMessage(ctx context.Context, params CreateMessageParams) (*CreateMessageResult, error) {
	return nil, fmt.Errorf("CreateMessage not supported for local provider")
}

// Close marks the provider disconnected. It always returns nil.
func (p *LocalProvider) Close() error {
	p.setState(StateDisconnected)
	return nil
}

// TranslateGastownMessage converts a Gas Town message (sender, recipient,
// subject, body) into an ACP user [Message], combining subject and body into
// the text content.
func TranslateGastownMessage(from, to, subject, body string) Message {
	var content string
	if subject != "" && body != "" {
		content = fmt.Sprintf("**Subject:** %s\n\n%s", subject, body)
	} else if subject != "" {
		content = subject
	} else if body != "" {
		content = body
	}
	return NewUserMessage(content)
}

// ExtractToolCalls returns the tool-use content blocks of msg as [ToolCallInfo].
func ExtractToolCalls(msg Message) []ToolCallInfo {
	var calls []ToolCallInfo
	for _, block := range msg.Content {
		if block.Type == ContentTypeToolUse {
			calls = append(calls, ToolCallInfo{
				Name:  block.Name,
				Input: block.Input,
			})
		}
	}
	return calls
}

// ToolCallInfo is a tool invocation extracted from a [Message]: the tool name
// and its raw JSON input.
type ToolCallInfo struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ExtractToolResults returns the tool-result content blocks of msg as [ToolResultInfo].
func ExtractToolResults(msg Message) []ToolResultInfo {
	var results []ToolResultInfo
	for _, block := range msg.Content {
		if block.Type == ContentTypeToolResult {
			results = append(results, ToolResultInfo{
				ToolUseID: block.ToolUseID,
				Content:   block.Content,
				IsError:   block.IsError,
			})
		}
	}
	return results
}

// ToolResultInfo is a tool result extracted from a [Message]: the originating
// tool-use ID, the result content, and whether it represents an error.
type ToolResultInfo struct {
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content"`
	IsError   bool   `json:"is_error"`
}

// ExtractTextContent concatenates all text content blocks of msg, separated by
// newlines.
func ExtractTextContent(msg Message) string {
	var text string
	for _, block := range msg.Content {
		if block.Type == ContentTypeText && block.Text != "" {
			if text != "" {
				text += "\n"
			}
			text += block.Text
		}
	}
	return text
}

// MessagesToJSON marshals a slice of messages to JSON.
func MessagesToJSON(msgs []Message) ([]byte, error) {
	return json.Marshal(msgs)
}

// MessagesFromJSON unmarshals a JSON-encoded slice of messages.
func MessagesFromJSON(data []byte) ([]Message, error) {
	var msgs []Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, fmt.Errorf("unmarshal messages: %w", err)
	}
	return msgs, nil
}

// RequestToJSON marshals a JSON-RPC request to JSON.
func RequestToJSON(req JSONRPCRequest) ([]byte, error) {
	return json.Marshal(req)
}

// ResponseToJSON marshals a JSON-RPC response to JSON.
func ResponseToJSON(resp JSONRPCResponse) ([]byte, error) {
	return json.Marshal(resp)
}

// ResponseFromJSON parses a JSON-RPC response from data.
func ResponseFromJSON(data []byte) (*JSONRPCResponse, error) {
	return ParseResponse(data)
}

// RequestFromJSON parses a JSON-RPC request from data.
func RequestFromJSON(data []byte) (*JSONRPCRequest, error) {
	return ParseRequest(data)
}

// NewInitializedNotification builds the ACP "notifications/initialized"
// notification sent after a successful handshake.
func NewInitializedNotification() JSONRPCRequest {
	return JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "notifications/initialized",
	}
}

// IsNotification reports whether req is a JSON-RPC notification, i.e. a request
// with no ID and therefore no expected response.
func IsNotification(req *JSONRPCRequest) bool {
	return req.ID == nil
}

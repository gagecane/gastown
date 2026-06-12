package provider

import (
	"encoding/json"
	"fmt"
	"time"
)

// JSONRPCVersion is the JSON-RPC protocol version used by all ACP messages.
const (
	JSONRPCVersion = "2.0"
)

// Role identifies the author of a [Message].
type Role string

// Message roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// ContentType identifies the kind of a [ContentBlock].
type ContentType string

// Content block types.
const (
	ContentTypeText       ContentType = "text"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
	ContentTypeImage      ContentType = "image"
)

// ToolType identifies the kind of a tool.
type ToolType string

// Tool types.
const (
	ToolTypeFunction ToolType = "function"
)

// JSONRPCRequest is a JSON-RPC 2.0 request or notification. A nil ID denotes a
// notification (see [IsNotification]).
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response. Exactly one of Result or Error
// is set.
type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

// JSONRPCError is the error payload of a [JSONRPCResponse].
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ContentBlock is a single piece of message content. The Type field selects
// which of the remaining fields are meaningful (text, tool use, tool result,
// or image).
type ContentBlock struct {
	Type ContentType `json:"type"`

	Text string `json:"text,omitempty"`

	ToolUseID string `json:"tool_use_id,omitempty"`

	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Content any             `json:"content,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Source  *ImageSource    `json:"source,omitempty"`
}

// ImageSource describes the encoded image data of an image [ContentBlock].
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// Message is a single conversational message: a role and its ordered content
// blocks.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// Tool describes a callable tool: its name, human description, and input schema.
type Tool struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	InputSchema *InputSchema `json:"input_schema,omitempty"`
}

// InputSchema is a JSON Schema for a tool's arguments. Schema keywords beyond
// the explicit fields are preserved in Additional and merged back during
// marshaling.
type InputSchema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
	Required   []string       `json:"required,omitempty"`
	Additional map[string]any `json:"-"`
}

// MarshalJSON marshals the schema, merging any [InputSchema.Additional]
// keywords into the top-level object.
func (s *InputSchema) MarshalJSON() ([]byte, error) {
	type Alias InputSchema
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(s),
	}
	data, err := json.Marshal(aux)
	if err != nil {
		return nil, err
	}
	if len(s.Additional) == 0 {
		return data, nil
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		return nil, err
	}
	for k, v := range s.Additional {
		merged[k] = v
	}
	return json.Marshal(merged)
}

// UnmarshalJSON unmarshals the schema, capturing any keywords beyond the
// explicit fields into [InputSchema.Additional].
func (s *InputSchema) UnmarshalJSON(data []byte) error {
	type Alias InputSchema
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(s),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "type")
	delete(raw, "properties")
	delete(raw, "required")
	if len(raw) > 0 {
		s.Additional = raw
	}
	return nil
}

// InitializeParams are the parameters of an ACP "initialize" request.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocol_version"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"client_info"`
	Meta            map[string]any     `json:"_meta,omitempty"`
}

// ClientCapabilities advertises the features a client supports.
type ClientCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
}

// ToolsCapability advertises support for the tools feature and whether the
// tool list may change at runtime.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability advertises support for the resources feature and whether
// the resource list may change at runtime.
type ResourcesCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ClientInfo identifies the connecting client by name and version.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the result of an ACP "initialize" request.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocol_version"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"server_info"`
	Instructions    string             `json:"instructions,omitempty"`
}

// ServerCapabilities advertises the features a server supports.
type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
}

// ServerInfo identifies the server by name and version.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ListToolsParams are the parameters of a "tools/list" request, including an
// optional pagination cursor.
type ListToolsParams struct {
	Cursor string         `json:"cursor,omitempty"`
	Meta   map[string]any `json:"_meta,omitempty"`
}

// ListToolsResult is the result of a "tools/list" request, with an optional
// cursor for the next page.
type ListToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// CallToolParams are the parameters of a "tools/call" request: the tool name
// and its arguments.
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

// CallToolResult is the result of a "tools/call" request. IsError marks a
// tool-level failure (distinct from a JSON-RPC protocol error).
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// CreateMessageParams are the parameters of a sampling request, asking the
// client to generate a model completion.
type CreateMessageParams struct {
	Messages    []Message      `json:"messages"`
	Model       string         `json:"model,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	Temperature float64        `json:"temperature,omitempty"`
	System      []ContentBlock `json:"system,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

// CreateMessageResult is the generated message returned from a sampling request.
type CreateMessageResult struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
	Model   string         `json:"model,omitempty"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// Usage reports token consumption for a sampling request.
type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

// TextContent is the typed form of a text content block.
type TextContent struct {
	Type ContentType `json:"type"`
	Text string      `json:"text"`
}

// NewTextContent returns a text [ContentBlock] carrying the given text.
func NewTextContent(text string) ContentBlock {
	return ContentBlock{
		Type: ContentTypeText,
		Text: text,
	}
}

// ToolUseContent is the typed form of a tool-use content block.
type ToolUseContent struct {
	Type  ContentType     `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// NewToolUseContent returns a tool-use [ContentBlock] for the named tool,
// marshaling input to JSON. It returns an error if input cannot be marshaled.
func NewToolUseContent(id, name string, input any) (ContentBlock, error) {
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return ContentBlock{}, fmt.Errorf("marshal tool input: %w", err)
	}
	return ContentBlock{
		Type:  ContentTypeToolUse,
		Name:  name,
		Input: inputBytes,
	}, nil
}

// ToolResultContent is the typed form of a tool-result content block.
type ToolResultContent struct {
	Type      ContentType `json:"type"`
	ToolUseID string      `json:"tool_use_id"`
	Content   any         `json:"content"`
	IsError   bool        `json:"is_error,omitempty"`
}

// NewToolResultContent returns a tool-result [ContentBlock] for the given
// tool-use ID, content, and error flag.
func NewToolResultContent(toolUseID string, content any, isError bool) ContentBlock {
	return ContentBlock{
		Type:      ContentTypeToolResult,
		ToolUseID: toolUseID,
		Content:   content,
		IsError:   isError,
	}
}

// NewUserMessage returns a user [Message] with a single text content block.
func NewUserMessage(text string) Message {
	return Message{
		Role:    RoleUser,
		Content: []ContentBlock{NewTextContent(text)},
	}
}

// NewUserMessageWithContent returns a user [Message] with the given content blocks.
func NewUserMessageWithContent(blocks ...ContentBlock) Message {
	return Message{
		Role:    RoleUser,
		Content: blocks,
	}
}

// NewAssistantMessage returns an assistant [Message] with a single text content block.
func NewAssistantMessage(text string) Message {
	return Message{
		Role:    RoleAssistant,
		Content: []ContentBlock{NewTextContent(text)},
	}
}

// NewAssistantMessageWithContent returns an assistant [Message] with the given content blocks.
func NewAssistantMessageWithContent(blocks ...ContentBlock) Message {
	return Message{
		Role:    RoleAssistant,
		Content: blocks,
	}
}

// NewSystemMessage returns a system [Message] with a single text content block.
func NewSystemMessage(text string) Message {
	return Message{
		Role:    RoleSystem,
		Content: []ContentBlock{NewTextContent(text)},
	}
}

// SimpleMessage is a flat, mail-like message used by Gas Town for inter-agent
// communication, convertible to and from an ACP [Message].
type SimpleMessage struct {
	ID        string    `json:"id,omitempty"`
	From      string    `json:"from,omitempty"`
	To        string    `json:"to,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	Body      string    `json:"body"`
	Timestamp time.Time `json:"timestamp,omitempty"`
	Priority  string    `json:"priority,omitempty"`
	Type      string    `json:"type,omitempty"`
}

// TranslateSimpleMessage converts a [SimpleMessage] into an ACP user
// [Message], combining its subject and body into text content.
func TranslateSimpleMessage(sm SimpleMessage) Message {
	var content []ContentBlock
	if sm.Subject != "" && sm.Body != "" {
		content = []ContentBlock{
			NewTextContent(fmt.Sprintf("**%s**\n\n%s", sm.Subject, sm.Body)),
		}
	} else if sm.Body != "" {
		content = []ContentBlock{NewTextContent(sm.Body)}
	} else {
		content = []ContentBlock{NewTextContent("")}
	}
	return Message{
		Role:    RoleUser,
		Content: content,
	}
}

// TranslateMessageToSimple converts an ACP [Message] into a [SimpleMessage],
// concatenating its text content blocks into the body and stamping the current
// time.
func TranslateMessageToSimple(msg Message) SimpleMessage {
	var body string
	for _, block := range msg.Content {
		if block.Type == ContentTypeText && block.Text != "" {
			if body != "" {
				body += "\n"
			}
			body += block.Text
		}
	}
	return SimpleMessage{
		Body:      body,
		Timestamp: time.Now(),
	}
}

// ToolFromDefinition builds a [Tool] from a name, description, and a raw JSON
// Schema map, extracting the "properties" and "required" keywords.
func ToolFromDefinition(name, description string, schema map[string]any) Tool {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		props = make(map[string]any)
	}

	return Tool{
		Name:        name,
		Description: description,
		InputSchema: &InputSchema{
			Type:       "object",
			Properties: props,
			Required:   getStringSlice(schema["required"]),
		},
	}
}

func getStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// NewInitializeRequest builds an ACP "initialize" request advertising the
// given client name and version.
func NewInitializeRequest(id any, clientName, clientVersion string) JSONRPCRequest {
	params := InitializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities: ClientCapabilities{
			Tools: &ToolsCapability{ListChanged: true},
		},
		ClientInfo: ClientInfo{
			Name:    clientName,
			Version: clientVersion,
		},
	}
	paramsBytes, _ := json.Marshal(params)
	return JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  "initialize",
		Params:  paramsBytes,
	}
}

// NewInitializeResponse builds the response to an "initialize" request,
// advertising the given server name, version, and instructions.
func NewInitializeResponse(id any, serverName, serverVersion, instructions string) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result: InitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities: ServerCapabilities{
				Tools: &ToolsCapability{ListChanged: false},
			},
			ServerInfo: ServerInfo{
				Name:    serverName,
				Version: serverVersion,
			},
			Instructions: instructions,
		},
	}
}

// NewListToolsRequest builds a "tools/list" request.
func NewListToolsRequest(id any) JSONRPCRequest {
	return JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  "tools/list",
	}
}

// NewListToolsResponse builds the response to a "tools/list" request carrying
// the given tools.
func NewListToolsResponse(id any, tools []Tool) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result: ListToolsResult{
			Tools: tools,
		},
	}
}

// NewCallToolRequest builds a "tools/call" request for the named tool with the
// given arguments.
func NewCallToolRequest(id any, name string, args map[string]any) JSONRPCRequest {
	paramsBytes, _ := json.Marshal(CallToolParams{
		Name:      name,
		Arguments: args,
	})
	return JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  "tools/call",
		Params:  paramsBytes,
	}
}

// NewCallToolResponse builds the response to a "tools/call" request with the
// given content and error flag.
func NewCallToolResponse(id any, content []ContentBlock, isError bool) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result: CallToolResult{
			Content: content,
			IsError: isError,
		},
	}
}

// NewErrorResponse builds a JSON-RPC error response with the given code,
// message, and optional data.
func NewErrorResponse(id any, code int, message string, data any) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

// Standard JSON-RPC 2.0 error codes.
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// ParseRequest unmarshals and validates a JSON-RPC request, checking the
// protocol version.
func ParseRequest(data []byte) (*JSONRPCRequest, error) {
	var req JSONRPCRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("parse JSON-RPC request: %w", err)
	}
	if req.JSONRPC != JSONRPCVersion {
		return nil, fmt.Errorf("invalid JSON-RPC version: %s", req.JSONRPC)
	}
	return &req, nil
}

// ParseResponse unmarshals and validates a JSON-RPC response, checking the
// protocol version.
func ParseResponse(data []byte) (*JSONRPCResponse, error) {
	var resp JSONRPCResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse JSON-RPC response: %w", err)
	}
	if resp.JSONRPC != JSONRPCVersion {
		return nil, fmt.Errorf("invalid JSON-RPC version: %s", resp.JSONRPC)
	}
	return &resp, nil
}

// ParseParams unmarshals the request's params into v. It is a no-op when the
// request carries no params.
func (r *JSONRPCRequest) ParseParams(v any) error {
	if len(r.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Params, v); err != nil {
		return fmt.Errorf("parse params: %w", err)
	}
	return nil
}

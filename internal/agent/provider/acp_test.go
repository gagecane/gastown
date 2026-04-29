package provider

import (
	"encoding/json"
	"math"
	"testing"
)

// TestNewSystemMessage verifies system-role messages are constructed correctly.
func TestNewSystemMessage(t *testing.T) {
	msg := NewSystemMessage("system prompt")
	if msg.Role != RoleSystem {
		t.Errorf("expected role %s, got %s", RoleSystem, msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Type != ContentTypeText {
		t.Errorf("expected content type %s, got %s", ContentTypeText, msg.Content[0].Type)
	}
	if msg.Content[0].Text != "system prompt" {
		t.Errorf("expected text 'system prompt', got %q", msg.Content[0].Text)
	}
}

// TestNewUserMessageWithContent ensures multi-block user messages are preserved verbatim.
func TestNewUserMessageWithContent(t *testing.T) {
	blocks := []ContentBlock{
		NewTextContent("first"),
		NewTextContent("second"),
	}
	msg := NewUserMessageWithContent(blocks...)
	if msg.Role != RoleUser {
		t.Errorf("expected role %s, got %s", RoleUser, msg.Role)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msg.Content))
	}
	if msg.Content[0].Text != "first" || msg.Content[1].Text != "second" {
		t.Errorf("unexpected content order: %+v", msg.Content)
	}
}

// TestNewUserMessageWithContent_Empty ensures calling with no blocks yields an
// assistant-like empty-content struct rather than panicking.
func TestNewUserMessageWithContent_Empty(t *testing.T) {
	msg := NewUserMessageWithContent()
	if msg.Role != RoleUser {
		t.Errorf("expected role %s, got %s", RoleUser, msg.Role)
	}
	if len(msg.Content) != 0 {
		t.Errorf("expected empty content, got %d blocks", len(msg.Content))
	}
}

// TestNewAssistantMessageWithContent covers the assistant-role variadic constructor.
func TestNewAssistantMessageWithContent(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"x": 1})
	blocks := []ContentBlock{
		NewTextContent("thinking..."),
		{Type: ContentTypeToolUse, Name: "calc", Input: input},
	}
	msg := NewAssistantMessageWithContent(blocks...)
	if msg.Role != RoleAssistant {
		t.Errorf("expected role %s, got %s", RoleAssistant, msg.Role)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msg.Content))
	}
	if msg.Content[1].Type != ContentTypeToolUse {
		t.Errorf("expected second block type %s, got %s", ContentTypeToolUse, msg.Content[1].Type)
	}
}

// TestTranslateSimpleMessage_SubjectAndBody combines subject + body into a
// bolded text block.
func TestTranslateSimpleMessage_SubjectAndBody(t *testing.T) {
	sm := SimpleMessage{Subject: "Hello", Body: "World"}
	msg := TranslateSimpleMessage(sm)
	if msg.Role != RoleUser {
		t.Errorf("expected role %s, got %s", RoleUser, msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	want := "**Hello**\n\nWorld"
	if msg.Content[0].Text != want {
		t.Errorf("expected text %q, got %q", want, msg.Content[0].Text)
	}
}

// TestTranslateSimpleMessage_BodyOnly uses body text without a subject prefix.
func TestTranslateSimpleMessage_BodyOnly(t *testing.T) {
	sm := SimpleMessage{Body: "just body"}
	msg := TranslateSimpleMessage(sm)
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Text != "just body" {
		t.Errorf("expected text 'just body', got %q", msg.Content[0].Text)
	}
}

// TestTranslateSimpleMessage_Empty returns a single empty text block when no
// content is provided (neither subject nor body).
func TestTranslateSimpleMessage_Empty(t *testing.T) {
	sm := SimpleMessage{}
	msg := TranslateSimpleMessage(sm)
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Text != "" {
		t.Errorf("expected empty text, got %q", msg.Content[0].Text)
	}
	if msg.Content[0].Type != ContentTypeText {
		t.Errorf("expected text content type, got %s", msg.Content[0].Type)
	}
}

// TestTranslateSimpleMessage_SubjectOnly: with subject but no body, current
// behavior falls through to the empty text branch (documents behavior).
func TestTranslateSimpleMessage_SubjectOnly(t *testing.T) {
	sm := SimpleMessage{Subject: "only subject"}
	msg := TranslateSimpleMessage(sm)
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	// Per current implementation: subject-only collapses to empty text.
	if msg.Content[0].Text != "" {
		t.Errorf("expected empty text for subject-only input, got %q", msg.Content[0].Text)
	}
}

// TestTranslateMessageToSimple collapses text content blocks into newline-joined
// body text.
func TestTranslateMessageToSimple(t *testing.T) {
	msg := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			NewTextContent("line one"),
			NewTextContent("line two"),
			{Type: ContentTypeToolUse, Name: "tool"}, // non-text — should be skipped
		},
	}
	sm := TranslateMessageToSimple(msg)
	want := "line one\nline two"
	if sm.Body != want {
		t.Errorf("expected body %q, got %q", want, sm.Body)
	}
	if sm.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

// TestTranslateMessageToSimple_EmptyTextSkipped: empty text blocks don't
// introduce stray newlines.
func TestTranslateMessageToSimple_EmptyTextSkipped(t *testing.T) {
	msg := Message{
		Role: RoleUser,
		Content: []ContentBlock{
			NewTextContent(""),
			NewTextContent("real content"),
			NewTextContent(""),
		},
	}
	sm := TranslateMessageToSimple(msg)
	if sm.Body != "real content" {
		t.Errorf("expected body 'real content', got %q", sm.Body)
	}
}

// TestTranslateMessageToSimple_NoTextBlocks yields an empty body.
func TestTranslateMessageToSimple_NoTextBlocks(t *testing.T) {
	msg := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			{Type: ContentTypeToolUse, Name: "tool"},
		},
	}
	sm := TranslateMessageToSimple(msg)
	if sm.Body != "" {
		t.Errorf("expected empty body, got %q", sm.Body)
	}
}

// TestToolFromDefinition_WithSchema constructs a Tool with populated schema.
func TestToolFromDefinition_WithSchema(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []any{"path"},
	}
	tool := ToolFromDefinition("read_file", "reads a file", schema)
	if tool.Name != "read_file" {
		t.Errorf("expected name 'read_file', got %s", tool.Name)
	}
	if tool.Description != "reads a file" {
		t.Errorf("expected description 'reads a file', got %s", tool.Description)
	}
	if tool.InputSchema == nil {
		t.Fatal("expected input schema, got nil")
	}
	if tool.InputSchema.Type != "object" {
		t.Errorf("expected schema type 'object', got %s", tool.InputSchema.Type)
	}
	if _, ok := tool.InputSchema.Properties["path"]; !ok {
		t.Error("expected 'path' property in schema")
	}
	if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "path" {
		t.Errorf("expected required=['path'], got %v", tool.InputSchema.Required)
	}
}

// TestToolFromDefinition_NoProperties handles a schema without a properties key;
// the resulting schema should have an empty (non-nil) properties map.
func TestToolFromDefinition_NoProperties(t *testing.T) {
	tool := ToolFromDefinition("no_args", "takes no args", map[string]any{})
	if tool.InputSchema == nil {
		t.Fatal("expected input schema, got nil")
	}
	if tool.InputSchema.Properties == nil {
		t.Error("expected non-nil empty properties map")
	}
	if len(tool.InputSchema.Properties) != 0 {
		t.Errorf("expected empty properties, got %d entries", len(tool.InputSchema.Properties))
	}
	if tool.InputSchema.Required != nil {
		t.Errorf("expected nil required, got %v", tool.InputSchema.Required)
	}
}

// TestToolFromDefinition_PropertiesWrongType: when "properties" is the wrong
// Go type, the constructor falls back to an empty map rather than panicking.
func TestToolFromDefinition_PropertiesWrongType(t *testing.T) {
	schema := map[string]any{
		"properties": "not a map",
	}
	tool := ToolFromDefinition("broken", "", schema)
	if tool.InputSchema == nil {
		t.Fatal("expected input schema, got nil")
	}
	if tool.InputSchema.Properties == nil {
		t.Error("expected non-nil properties fallback")
	}
	if len(tool.InputSchema.Properties) != 0 {
		t.Errorf("expected empty fallback properties, got %d", len(tool.InputSchema.Properties))
	}
}

// TestToolFromDefinition_RequiredWrongType: non-slice "required" should be
// dropped silently (no panic, empty required list).
func TestToolFromDefinition_RequiredWrongType(t *testing.T) {
	schema := map[string]any{
		"required": "path",
	}
	tool := ToolFromDefinition("bad_required", "", schema)
	if tool.InputSchema.Required != nil {
		t.Errorf("expected nil required for wrong-type input, got %v", tool.InputSchema.Required)
	}
}

// TestToolFromDefinition_RequiredMixedTypes: non-string items inside required
// are filtered out; string items are preserved in order.
func TestToolFromDefinition_RequiredMixedTypes(t *testing.T) {
	schema := map[string]any{
		"required": []any{"a", 42, "b", true, "c"},
	}
	tool := ToolFromDefinition("mixed", "", schema)
	want := []string{"a", "b", "c"}
	got := tool.InputSchema.Required
	if len(got) != len(want) {
		t.Fatalf("expected %d required, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("required[%d]: expected %q, got %q", i, want[i], got[i])
		}
	}
}

// TestNewListToolsRequest checks the shape of a tools/list request.
func TestNewListToolsRequest(t *testing.T) {
	req := NewListToolsRequest(42)
	if req.JSONRPC != JSONRPCVersion {
		t.Errorf("expected jsonrpc %s, got %s", JSONRPCVersion, req.JSONRPC)
	}
	if req.Method != "tools/list" {
		t.Errorf("expected method 'tools/list', got %s", req.Method)
	}
	if req.ID != 42 {
		t.Errorf("expected id 42, got %v", req.ID)
	}
}

// TestNewListToolsResponse wraps a tool slice in a JSON-RPC response payload.
func TestNewListToolsResponse(t *testing.T) {
	tools := []Tool{
		{Name: "a", Description: "alpha"},
		{Name: "b", Description: "beta"},
	}
	resp := NewListToolsResponse("req-1", tools)
	if resp.JSONRPC != JSONRPCVersion {
		t.Errorf("expected jsonrpc %s, got %s", JSONRPCVersion, resp.JSONRPC)
	}
	if resp.ID != "req-1" {
		t.Errorf("expected id 'req-1', got %v", resp.ID)
	}
	result, ok := resp.Result.(ListToolsResult)
	if !ok {
		t.Fatalf("expected ListToolsResult, got %T", resp.Result)
	}
	if len(result.Tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(result.Tools))
	}
}

// TestNewCallToolRequest verifies method name and embedded params.
func TestNewCallToolRequest(t *testing.T) {
	args := map[string]any{"path": "/tmp/foo", "mode": "read"}
	req := NewCallToolRequest(7, "read_file", args)
	if req.Method != "tools/call" {
		t.Errorf("expected method 'tools/call', got %s", req.Method)
	}
	var params CallToolParams
	if err := req.ParseParams(&params); err != nil {
		t.Fatalf("ParseParams failed: %v", err)
	}
	if params.Name != "read_file" {
		t.Errorf("expected name 'read_file', got %s", params.Name)
	}
	if params.Arguments["path"] != "/tmp/foo" {
		t.Errorf("expected path '/tmp/foo', got %v", params.Arguments["path"])
	}
	if params.Arguments["mode"] != "read" {
		t.Errorf("expected mode 'read', got %v", params.Arguments["mode"])
	}
}

// TestNewCallToolRequest_NilArgs ensures nil arguments don't break serialization.
func TestNewCallToolRequest_NilArgs(t *testing.T) {
	req := NewCallToolRequest(1, "noop", nil)
	if req.Method != "tools/call" {
		t.Errorf("expected method 'tools/call', got %s", req.Method)
	}
	var params CallToolParams
	if err := req.ParseParams(&params); err != nil {
		t.Fatalf("ParseParams failed: %v", err)
	}
	if params.Name != "noop" {
		t.Errorf("expected name 'noop', got %s", params.Name)
	}
}

// TestNewCallToolResponse embeds tool-call result content and isError flag.
func TestNewCallToolResponse(t *testing.T) {
	content := []ContentBlock{NewTextContent("ok")}
	resp := NewCallToolResponse(9, content, false)
	if resp.ID != 9 {
		t.Errorf("expected id 9, got %v", resp.ID)
	}
	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatalf("expected CallToolResult, got %T", resp.Result)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.IsError {
		t.Error("expected IsError=false")
	}
}

// TestNewCallToolResponse_Error plumbs the error flag through.
func TestNewCallToolResponse_Error(t *testing.T) {
	resp := NewCallToolResponse(10, []ContentBlock{NewTextContent("boom")}, true)
	result := resp.Result.(CallToolResult)
	if !result.IsError {
		t.Error("expected IsError=true")
	}
}

// TestParseResponse accepts a valid JSON-RPC 2.0 response payload.
func TestParseResponse(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","id":5,"result":{"ok":true}}`)
	resp, err := ParseResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.JSONRPC != JSONRPCVersion {
		t.Errorf("expected jsonrpc %s, got %s", JSONRPCVersion, resp.JSONRPC)
	}
	// JSON unmarshals integers as float64 when the field is `any`.
	if id, ok := resp.ID.(float64); !ok || id != 5 {
		t.Errorf("expected id 5 (float64), got %v (%T)", resp.ID, resp.ID)
	}
}

// TestParseResponse_InvalidVersion rejects non-2.0 responses.
func TestParseResponse_InvalidVersion(t *testing.T) {
	data := []byte(`{"jsonrpc":"1.0","id":1,"result":{}}`)
	if _, err := ParseResponse(data); err == nil {
		t.Error("expected error for invalid jsonrpc version")
	}
}

// TestParseResponse_MalformedJSON surfaces a helpful parse error.
func TestParseResponse_MalformedJSON(t *testing.T) {
	data := []byte(`{not valid json`)
	if _, err := ParseResponse(data); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// TestParseResponse_WithError parses an error response.
func TestParseResponse_WithError(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`)
	resp, err := ParseResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error to be populated")
	}
	if resp.Error.Code != MethodNotFound {
		t.Errorf("expected code %d, got %d", MethodNotFound, resp.Error.Code)
	}
}

// TestParseRequest_MalformedJSON surfaces a helpful parse error.
func TestParseRequest_MalformedJSON(t *testing.T) {
	data := []byte(`{not valid`)
	if _, err := ParseRequest(data); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// TestJSONRPCRequest_ParseParams reads params from a well-formed request.
func TestJSONRPCRequest_ParseParams(t *testing.T) {
	req := NewInitializeRequest(1, "client-x", "9.9.9")
	var params InitializeParams
	if err := req.ParseParams(&params); err != nil {
		t.Fatalf("ParseParams failed: %v", err)
	}
	if params.ClientInfo.Name != "client-x" {
		t.Errorf("expected client name 'client-x', got %s", params.ClientInfo.Name)
	}
	if params.ClientInfo.Version != "9.9.9" {
		t.Errorf("expected client version '9.9.9', got %s", params.ClientInfo.Version)
	}
	if params.ProtocolVersion != "2024-11-05" {
		t.Errorf("expected protocol version 2024-11-05, got %s", params.ProtocolVersion)
	}
}

// TestJSONRPCRequest_ParseParams_Empty is a no-op on requests without params.
func TestJSONRPCRequest_ParseParams_Empty(t *testing.T) {
	req := JSONRPCRequest{JSONRPC: JSONRPCVersion, Method: "ping"}
	var params map[string]any
	if err := req.ParseParams(&params); err != nil {
		t.Errorf("expected no error for empty params, got %v", err)
	}
	if params != nil {
		t.Errorf("expected params to remain nil, got %v", params)
	}
}

// TestJSONRPCRequest_ParseParams_Invalid returns an error on malformed params.
func TestJSONRPCRequest_ParseParams_Invalid(t *testing.T) {
	req := JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "test",
		Params:  json.RawMessage(`{not valid`),
	}
	var params map[string]any
	if err := req.ParseParams(&params); err == nil {
		t.Error("expected error for invalid params")
	}
}

// TestInputSchema_UnmarshalJSON parses a schema with known + additional fields.
func TestInputSchema_UnmarshalJSON(t *testing.T) {
	data := []byte(`{
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"required": ["path"],
		"additionalProperties": false,
		"$schema": "http://json-schema.org/draft-07/schema#"
	}`)
	var schema InputSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("expected type 'object', got %s", schema.Type)
	}
	if _, ok := schema.Properties["path"]; !ok {
		t.Error("expected 'path' in properties")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "path" {
		t.Errorf("expected required=['path'], got %v", schema.Required)
	}
	if schema.Additional == nil {
		t.Fatal("expected additional fields to be captured")
	}
	if got, ok := schema.Additional["additionalProperties"]; !ok || got != false {
		t.Errorf("expected additionalProperties=false in Additional, got %v (ok=%v)", got, ok)
	}
	if _, ok := schema.Additional["$schema"]; !ok {
		t.Error("expected $schema to be captured in Additional")
	}
	// Known fields should not leak into Additional.
	if _, ok := schema.Additional["type"]; ok {
		t.Error("type field should not appear in Additional")
	}
	if _, ok := schema.Additional["properties"]; ok {
		t.Error("properties field should not appear in Additional")
	}
	if _, ok := schema.Additional["required"]; ok {
		t.Error("required field should not appear in Additional")
	}
}

// TestInputSchema_UnmarshalJSON_NoExtras leaves Additional nil when the payload
// contains only well-known fields.
func TestInputSchema_UnmarshalJSON_NoExtras(t *testing.T) {
	data := []byte(`{"type":"object","properties":{},"required":[]}`)
	var schema InputSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema.Additional != nil {
		t.Errorf("expected Additional to be nil, got %v", schema.Additional)
	}
}

// TestInputSchema_UnmarshalJSON_Invalid rejects malformed payloads.
func TestInputSchema_UnmarshalJSON_Invalid(t *testing.T) {
	data := []byte(`not a json object`)
	var schema InputSchema
	if err := json.Unmarshal(data, &schema); err == nil {
		t.Error("expected error for malformed schema JSON")
	}
}

// TestInputSchema_MarshalJSON_NoAdditional skips the merge path when Additional
// is empty (covers the `len == 0` branch in MarshalJSON).
func TestInputSchema_MarshalJSON_NoAdditional(t *testing.T) {
	schema := &InputSchema{
		Type:       "object",
		Properties: map[string]any{"a": map[string]any{"type": "integer"}},
		Required:   []string{"a"},
	}
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed["type"] != "object" {
		t.Errorf("expected type 'object', got %v", parsed["type"])
	}
	if _, ok := parsed["additionalProperties"]; ok {
		t.Error("expected no additionalProperties field")
	}
}

// TestInputSchema_RoundTrip confirms marshal→unmarshal preserves Additional.
func TestInputSchema_RoundTrip(t *testing.T) {
	original := &InputSchema{
		Type:       "object",
		Properties: map[string]any{"x": map[string]any{"type": "number"}},
		Required:   []string{"x"},
		Additional: map[string]any{"additionalProperties": false},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded InputSchema
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Type != "object" {
		t.Errorf("type mismatch: %s", decoded.Type)
	}
	if len(decoded.Required) != 1 || decoded.Required[0] != "x" {
		t.Errorf("required mismatch: %v", decoded.Required)
	}
	if decoded.Additional == nil {
		t.Fatal("expected Additional to survive round trip")
	}
	if decoded.Additional["additionalProperties"] != false {
		t.Errorf("expected additionalProperties=false, got %v", decoded.Additional["additionalProperties"])
	}
}

// TestNewToolUseContent_MarshalFailure exercises the error branch of
// NewToolUseContent by passing a value that cannot be JSON-marshaled.
func TestNewToolUseContent_MarshalFailure(t *testing.T) {
	_, err := NewToolUseContent("id", "tool", math.Inf(1))
	if err == nil {
		t.Error("expected error when marshaling unsupported value (+Inf)")
	}
}

// TestConstants pins the JSON-RPC error codes to their standard values so a
// typo or accidental edit is caught immediately.
func TestConstants(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"ParseError", ParseError, -32700},
		{"InvalidRequest", InvalidRequest, -32600},
		{"MethodNotFound", MethodNotFound, -32601},
		{"InvalidParams", InvalidParams, -32602},
		{"InternalError", InternalError, -32603},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: expected %d, got %d", c.name, c.want, c.got)
		}
	}
	if JSONRPCVersion != "2.0" {
		t.Errorf("expected JSONRPCVersion '2.0', got %s", JSONRPCVersion)
	}
}

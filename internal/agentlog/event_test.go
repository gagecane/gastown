package agentlog

import (
	"testing"
	"time"
)

// TestNewAdapter_ReturnsClaudeCodeByDefault verifies the default behavior
// when no agent type is specified: NewAdapter falls back to ClaudeCode.
func TestNewAdapter_ReturnsClaudeCodeByDefault(t *testing.T) {
	a := NewAdapter("")
	if a == nil {
		t.Fatal("NewAdapter(\"\") returned nil, expected ClaudeCode adapter")
	}
	if _, ok := a.(*ClaudeCodeAdapter); !ok {
		t.Errorf("NewAdapter(\"\") returned %T, want *ClaudeCodeAdapter", a)
	}
	if a.AgentType() != "claudecode" {
		t.Errorf("AgentType() = %q, want %q", a.AgentType(), "claudecode")
	}
}

// TestNewAdapter_ReturnsClaudeCode verifies explicit "claudecode" returns ClaudeCode.
func TestNewAdapter_ReturnsClaudeCode(t *testing.T) {
	a := NewAdapter("claudecode")
	if a == nil {
		t.Fatal("NewAdapter(\"claudecode\") returned nil")
	}
	if _, ok := a.(*ClaudeCodeAdapter); !ok {
		t.Errorf("NewAdapter(\"claudecode\") returned %T, want *ClaudeCodeAdapter", a)
	}
}

// TestNewAdapter_ReturnsOpenCode verifies "opencode" returns the OpenCode adapter.
func TestNewAdapter_ReturnsOpenCode(t *testing.T) {
	a := NewAdapter("opencode")
	if a == nil {
		t.Fatal("NewAdapter(\"opencode\") returned nil")
	}
	if _, ok := a.(*OpenCodeAdapter); !ok {
		t.Errorf("NewAdapter(\"opencode\") returned %T, want *OpenCodeAdapter", a)
	}
	if a.AgentType() != "opencode" {
		t.Errorf("AgentType() = %q, want %q", a.AgentType(), "opencode")
	}
}

// TestNewAdapter_ReturnsNilForUnknown verifies unknown agent types return nil.
func TestNewAdapter_ReturnsNilForUnknown(t *testing.T) {
	unknowns := []string{"kiro", "chatgpt", "unknown", "CLAUDECODE", "claude-code", "open_code", " opencode"}
	for _, name := range unknowns {
		t.Run(name, func(t *testing.T) {
			if a := NewAdapter(name); a != nil {
				t.Errorf("NewAdapter(%q) = %T, want nil", name, a)
			}
		})
	}
}

// TestAgentEvent_ZeroValue verifies the zero value of AgentEvent is usable
// (all numeric token fields are 0, string fields empty, Timestamp is zero).
func TestAgentEvent_ZeroValue(t *testing.T) {
	var ev AgentEvent
	if ev.AgentType != "" {
		t.Errorf("zero AgentEvent.AgentType = %q, want empty", ev.AgentType)
	}
	if ev.SessionID != "" {
		t.Errorf("zero AgentEvent.SessionID = %q, want empty", ev.SessionID)
	}
	if ev.NativeSessionID != "" {
		t.Errorf("zero AgentEvent.NativeSessionID = %q, want empty", ev.NativeSessionID)
	}
	if ev.EventType != "" {
		t.Errorf("zero AgentEvent.EventType = %q, want empty", ev.EventType)
	}
	if ev.Role != "" {
		t.Errorf("zero AgentEvent.Role = %q, want empty", ev.Role)
	}
	if ev.Content != "" {
		t.Errorf("zero AgentEvent.Content = %q, want empty", ev.Content)
	}
	if !ev.Timestamp.IsZero() {
		t.Errorf("zero AgentEvent.Timestamp = %v, want zero", ev.Timestamp)
	}
	if ev.InputTokens != 0 || ev.OutputTokens != 0 || ev.CacheReadTokens != 0 || ev.CacheCreationTokens != 0 {
		t.Errorf("zero AgentEvent token fields should all be 0, got in=%d out=%d cacheRead=%d cacheCreation=%d",
			ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheCreationTokens)
	}
}

// TestAgentEvent_FieldAssignment exercises each exported field to confirm
// assignment works as expected. This is a compile/type-level sanity check
// that future refactors of AgentEvent keep these fields public.
func TestAgentEvent_FieldAssignment(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	ev := AgentEvent{
		AgentType:           "claudecode",
		SessionID:           "hq-mayor",
		NativeSessionID:     "abc-123",
		EventType:           "usage",
		Role:                "assistant",
		Content:             "",
		Timestamp:           now,
		InputTokens:         100,
		OutputTokens:        200,
		CacheReadTokens:     300,
		CacheCreationTokens: 400,
	}

	if ev.AgentType != "claudecode" {
		t.Errorf("AgentType = %q", ev.AgentType)
	}
	if ev.SessionID != "hq-mayor" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}
	if ev.NativeSessionID != "abc-123" {
		t.Errorf("NativeSessionID = %q", ev.NativeSessionID)
	}
	if ev.EventType != "usage" {
		t.Errorf("EventType = %q", ev.EventType)
	}
	if ev.Role != "assistant" {
		t.Errorf("Role = %q", ev.Role)
	}
	if !ev.Timestamp.Equal(now) {
		t.Errorf("Timestamp = %v, want %v", ev.Timestamp, now)
	}
	if ev.InputTokens != 100 || ev.OutputTokens != 200 ||
		ev.CacheReadTokens != 300 || ev.CacheCreationTokens != 400 {
		t.Errorf("token fields: in=%d out=%d cacheRead=%d cacheCreation=%d, want 100/200/300/400",
			ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheCreationTokens)
	}
}

// TestAgentAdapter_InterfaceContract verifies that the concrete adapters
// returned by NewAdapter satisfy the AgentAdapter interface and report
// stable AgentType strings. These strings are part of the telemetry contract
// (downstream consumers filter on them), so a regression here would silently
// break monitoring.
func TestAgentAdapter_InterfaceContract(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"claudecode", "claudecode"},
		{"", "claudecode"},
		{"opencode", "opencode"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			var adapter AgentAdapter = NewAdapter(tc.input)
			if adapter == nil {
				t.Fatalf("NewAdapter(%q) returned nil", tc.input)
			}
			if got := adapter.AgentType(); got != tc.want {
				t.Errorf("AgentType() = %q, want %q", got, tc.want)
			}
		})
	}
}

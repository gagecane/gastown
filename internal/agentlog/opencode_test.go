package agentlog

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestOpenCodeAdapter_AgentType verifies the adapter reports its identifier.
// This string is the telemetry contract and must stay "opencode".
func TestOpenCodeAdapter_AgentType(t *testing.T) {
	a := &OpenCodeAdapter{}
	if got := a.AgentType(); got != "opencode" {
		t.Errorf("AgentType() = %q, want %q", got, "opencode")
	}
}

// TestOpenCodeAdapter_Watch_NotImplemented confirms the placeholder Watch
// returns a nil channel and a descriptive error. This guards against a
// regression where a future partial implementation might forget to return
// an error and instead silently succeed.
func TestOpenCodeAdapter_Watch_NotImplemented(t *testing.T) {
	a := &OpenCodeAdapter{}
	ctx := context.Background()

	ch, err := a.Watch(ctx, "session-1", "/some/work/dir", time.Time{})
	if err == nil {
		t.Fatal("Watch() err = nil, want not-implemented error")
	}
	if ch != nil {
		t.Errorf("Watch() ch = %v, want nil when not implemented", ch)
	}
	// The error must mention "opencode" so operators can identify which
	// adapter is unimplemented when reading logs.
	if !strings.Contains(err.Error(), "opencode") {
		t.Errorf("error message %q should mention 'opencode'", err.Error())
	}
}

// TestOpenCodeAdapter_Watch_IgnoresArgs verifies Watch returns the same
// not-implemented error regardless of argument values (cancelled ctx, zero
// time, empty strings). All paths should fail uniformly until real support
// is added.
func TestOpenCodeAdapter_Watch_IgnoresArgs(t *testing.T) {
	a := &OpenCodeAdapter{}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name    string
		ctx     context.Context
		session string
		workDir string
		since   time.Time
	}{
		{"empty args", context.Background(), "", "", time.Time{}},
		{"cancelled ctx", cancelled, "s", "/w", time.Now()},
		{"future since", context.Background(), "s", "/w", time.Now().Add(24 * time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch, err := a.Watch(tc.ctx, tc.session, tc.workDir, tc.since)
			if err == nil {
				t.Error("Watch() err = nil, want error")
			}
			if ch != nil {
				t.Error("Watch() ch should be nil")
			}
		})
	}
}

// TestOpenCodeAdapter_ImplementsInterface is a compile-time guard: if
// OpenCodeAdapter ever drifts from the AgentAdapter contract this test will
// fail to compile.
func TestOpenCodeAdapter_ImplementsInterface(t *testing.T) {
	var _ AgentAdapter = (*OpenCodeAdapter)(nil)
}

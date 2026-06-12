package cmd

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/nudge"
)

func TestShouldSkipDrainUntilIdle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		hasPromptDetection bool
		waitErr            error
		want               bool
	}{
		{"prompt aware idle", true, nil, false},
		{"prompt aware busy", true, errors.New("timeout"), true},
		{"no prompt detection busy", false, errors.New("timeout"), false},
		{"no prompt detection idle", false, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipDrainUntilIdle(tt.hasPromptDetection, tt.waitErr); got != tt.want {
				t.Errorf("shouldSkipDrainUntilIdle(%v, %v) = %v, want %v", tt.hasPromptDetection, tt.waitErr, got, tt.want)
			}
		})
	}
}

func TestInjectOrRequeue_RequeuesOnInjectionFailure(t *testing.T) {
	townRoot := t.TempDir()
	const session = "test-session"

	drained := []nudge.QueuedNudge{
		{Sender: "alice", Message: "hello", Priority: nudge.PriorityNormal},
		{Sender: "bob", Message: "world", Priority: nudge.PriorityNormal},
	}

	injectOrRequeue(townRoot, session, drained, func(string) error {
		return errors.New("tmux hiccup")
	})

	pending, err := nudge.Pending(townRoot, session)
	if err != nil {
		t.Fatalf("Pending returned error: %v", err)
	}
	if pending != len(drained) {
		t.Errorf("after injection failure, pending = %d, want %d (nudges should be requeued)", pending, len(drained))
	}
}

func TestInjectOrRequeue_NoRequeueOnSuccess(t *testing.T) {
	townRoot := t.TempDir()
	const session = "test-session"

	drained := []nudge.QueuedNudge{
		{Sender: "alice", Message: "hello", Priority: nudge.PriorityNormal},
	}

	var injected string
	injectOrRequeue(townRoot, session, drained, func(msg string) error {
		injected = msg
		return nil
	})

	if injected == "" {
		t.Error("inject was not called with formatted message")
	}

	pending, err := nudge.Pending(townRoot, session)
	if err != nil {
		t.Fatalf("Pending returned error: %v", err)
	}
	if pending != 0 {
		t.Errorf("after successful injection, pending = %d, want 0 (nudges should not be requeued)", pending)
	}
}

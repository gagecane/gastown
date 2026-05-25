package tmux

import (
	"testing"
	"time"
)

func TestDetachedKillSession(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-detach-kill"

	// Clean up any leftover
	_ = tm.KillSession(sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	// Create a session to kill
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Verify it exists
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Fatal("session should exist after creation")
	}

	// Spawn detached kill with 1s delay
	if err := tm.DetachedKillSession(sessionName, 1*time.Second); err != nil {
		t.Fatalf("DetachedKillSession: %v", err)
	}

	// Session should still exist immediately (delay hasn't elapsed)
	has, err = tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession (immediate): %v", err)
	}
	if !has {
		t.Fatal("session should still exist immediately after spawning detached kill")
	}

	// Wait for the detached subprocess to kill it
	time.Sleep(3 * time.Second)

	// Session should be gone
	has, err = tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession (after delay): %v", err)
	}
	if has {
		t.Error("session should have been killed by detached subprocess")
	}
}

func TestDetachedKillSessionWithProcesses(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-detach-killp"

	// Clean up any leftover
	_ = tm.KillSession(sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	// Create a session to kill
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Verify it exists
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Fatal("session should exist after creation")
	}

	// Spawn detached kill with 1s delay
	if err := tm.DetachedKillSessionWithProcesses(sessionName, 1*time.Second); err != nil {
		t.Fatalf("DetachedKillSessionWithProcesses: %v", err)
	}

	// Wait for the detached subprocess to kill it
	time.Sleep(3 * time.Second)

	// Session should be gone
	has, err = tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession (after delay): %v", err)
	}
	if has {
		t.Error("session should have been killed by detached subprocess")
	}
}

func TestDetachedKillSession_InvalidName(t *testing.T) {
	tm := newTestTmux(t)

	// Invalid session names should be rejected
	err := tm.DetachedKillSession("bad;name", 1*time.Second)
	if err == nil {
		t.Error("expected error for invalid session name")
	}

	err = tm.DetachedKillSessionWithProcesses("bad;name", 1*time.Second)
	if err == nil {
		t.Error("expected error for invalid session name with processes")
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		safe  bool // Whether result should be unquoted
	}{
		{"simple-name", true},
		{"with_underscore", true},
		{"ABC123", true},
		{"has space", false},
		{"has;semi", false},
		{"", false},
	}

	for _, tt := range tests {
		result := shellQuote(tt.input)
		if tt.safe && result != tt.input {
			t.Errorf("shellQuote(%q) = %q, expected no quoting", tt.input, result)
		}
		if !tt.safe && result == tt.input {
			t.Errorf("shellQuote(%q) = %q, expected quoting", tt.input, result)
		}
	}
}

package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// enableDetachedKillTrace points the detached-kill subprocess at a per-test
// log file. On poll failure the test dumps the log so the next flake yields
// a `set -x` trace instead of a bare assertion. See gu-4l21 — repeated
// flakes despite a 60s polling deadline left no diagnostic trail because
// the subprocess discarded stderr.
func enableDetachedKillTrace(t *testing.T) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "detached-kill.log")
	t.Setenv(EnvTmuxDetachedKillLog, logPath)
	return logPath
}

// dumpDetachedKillLog reads the captured subprocess trace and logs it via
// t.Log so it appears in test output. Tolerant of a missing file — the
// subprocess may not have started, which is itself the diagnostic.
func dumpDetachedKillLog(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Logf("detached-kill log unreadable (%v); subprocess may not have started", err)
		return
	}
	if len(data) == 0 {
		t.Log("detached-kill log empty — subprocess started but produced no output before deadline")
		return
	}
	t.Logf("detached-kill subprocess trace:\n%s", string(data))
}

func TestDetachedKillSession(t *testing.T) {
	tm := newTestTmux(t)
	logPath := enableDetachedKillTrace(t)
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

	// Poll for the detached subprocess to kill the session. The subprocess
	// sleeps 1s then runs `tmux kill-session`; under CI load the spawn +
	// scheduling slack can exceed a fixed-duration sleep, producing flakes.
	// Use a generous polling deadline instead — we still verify the kill
	// happens, just without racing on a tight timing assumption. 60s is
	// chosen to absorb pathological scheduler latency under full
	// `go test ./...` pressure (gu-zyxl); a real regression where the
	// subprocess never spawns or never issues the kill still fails fast.
	if waitForSessionGone(t, tm, sessionName, 60*time.Second) {
		return
	}
	dumpDetachedKillLog(t, logPath)
	t.Error("session should have been killed by detached subprocess")
}

// waitForSessionGone polls HasSession until the session is absent or the
// deadline elapses. Returns true if the session disappeared.
func waitForSessionGone(t *testing.T, tm *Tmux, name string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		has, err := tm.HasSession(name)
		if err != nil {
			t.Fatalf("HasSession (poll): %v", err)
		}
		if !has {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func TestDetachedKillSessionWithProcesses(t *testing.T) {
	tm := newTestTmux(t)
	logPath := enableDetachedKillTrace(t)
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

	// Poll for the detached subprocess to kill the session (see comment in
	// TestDetachedKillSession — fixed sleeps race under CI load).
	if waitForSessionGone(t, tm, sessionName, 60*time.Second) {
		return
	}
	dumpDetachedKillLog(t, logPath)
	t.Error("session should have been killed by detached subprocess")
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

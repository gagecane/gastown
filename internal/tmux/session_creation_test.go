package tmux

import (
	"strings"
	"testing"
	"time"
)

// Tests for the two-step session creation (new-session + respawn-pane) and
// checkSessionAfterCreate health check introduced to eliminate blank windows.

// TestNewSessionWithCommand_BadBinary verifies that NewSessionWithCommand returns
// an error when the command binary doesn't exist, instead of leaving a dead session.
func TestNewSessionWithCommand_BadBinary(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-badbinary-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	err := tm.NewSessionWithCommand(session, "", "/nonexistent/binary --flag")
	if err == nil {
		// checkSessionAfterCreate should have caught this
		t.Error("NewSessionWithCommand should return error for missing binary")
	}
}

// TestNewSessionWithCommand_EarlyCleanExit verifies that a command which exits
// cleanly (status 0) within the 250ms startup window is treated as a startup
// failure. Gastown callers only ever spawn long-lived agents (kiro-cli,
// claude-code, shells) — a command that exits in <250ms is never what the
// caller wanted. Previously this returned nil (success), causing
// session/lifecycle.go VerifySurvived to later report an opaque "died during
// startup" with no pane output. See gu-hq88 / gt-ltnxs.
func TestNewSessionWithCommand_EarlyCleanExit(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-earlyexit-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	// sh -c 'true' exits with status 0 well under 250ms.
	err := tm.NewSessionWithCommand(session, "", "sh -c 'true'")
	if err == nil {
		t.Fatal("NewSessionWithCommand should return error when command exits early even with status 0")
	}
	if !strings.Contains(err.Error(), "exited early") {
		t.Errorf("expected error to mention 'exited early', got: %v", err)
	}
}

// TestNewSessionWithCommand_EarlyExitCapturesPaneOutput verifies that when a
// command exits early, the error surfaces whatever the process wrote to
// stdout/stderr. This is what makes daemon-spawned dog failures finally
// diagnosable (gu-hq88) — the pane output is captured before the session is
// destroyed, so downstream logs carry the actual stack trace / error message
// rather than an opaque "died during startup".
func TestNewSessionWithCommand_EarlyExitCapturesPaneOutput(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-earlyexit-diag-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	marker := "GUHQ88_STDERR_MARKER"
	// Emit a distinctive marker to stderr, then exit non-zero — mirrors the
	// shape of an agent that crashes on startup with a stack trace.
	err := tm.NewSessionWithCommand(session, "", `sh -c 'echo `+marker+` 1>&2; exit 7'`)
	if err == nil {
		t.Fatal("expected error for early exit, got nil")
	}
	if !strings.Contains(err.Error(), marker) {
		t.Errorf("expected pane output marker %q in error, got: %v", marker, err)
	}
	if !strings.Contains(err.Error(), "--- pane output ---") {
		t.Errorf("expected 'pane output' banner in error, got: %v", err)
	}
}

// TestNewSessionWithCommand_BadWorkDir verifies workDir validation rejects
// non-existent directories before creating the session.
func TestNewSessionWithCommand_BadWorkDir(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-badworkdir-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	err := tm.NewSessionWithCommand(session, "/tmp/gastown-nonexistent-dir-99999", "echo hello")
	if err == nil {
		t.Error("NewSessionWithCommand should return error for non-existent workDir")
	}
}

// TestNewSessionWithCommand_ExecEnvBadBinary verifies the exact gastown polecat
// startup pattern (exec env VAR=val binary) returns an error for missing binaries.
func TestNewSessionWithCommand_ExecEnvBadBinary(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-execenv-bad-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	cmd := `exec env GT_TEST=1 GT_ROLE=test /nonexistent/claude-code --settings /tmp`
	err := tm.NewSessionWithCommand(session, "", cmd)
	if err == nil {
		t.Error("NewSessionWithCommand should return error for exec env with missing binary")
	}
}

// TestNewSessionWithCommand_Success verifies a valid command runs and produces output.
func TestNewSessionWithCommand_Success(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-success-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	err := tm.NewSessionWithCommand(session, "", `sh -c 'echo "SESSION_OK"; sleep 10'`)
	if err != nil {
		t.Fatalf("NewSessionWithCommand failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	output, _ := tm.CapturePane(session, 50)
	if !strings.Contains(output, "SESSION_OK") {
		t.Errorf("expected output to contain SESSION_OK, got: %q", strings.TrimSpace(output))
	}
}

// TestNewSessionWithCommand_ExecEnvSuccess verifies the exec env pattern works
// with a real binary.
func TestNewSessionWithCommand_ExecEnvSuccess(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-execenv-ok-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	cmd := `exec env GT_RIG=testrig GT_POLECAT=testcat sleep 5`
	err := tm.NewSessionWithCommand(session, t.TempDir(), cmd)
	if err != nil {
		t.Fatalf("NewSessionWithCommand failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	paneCmd, _ := tm.GetPaneCommand(session)
	if paneCmd != "sleep" {
		t.Errorf("expected pane command 'sleep' (exec replaced shell), got %q", paneCmd)
	}
}

// TestNewSessionWithCommand_Duplicate verifies duplicate session creation is rejected.
func TestNewSessionWithCommand_Duplicate(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-dup-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	if err := tm.NewSessionWithCommand(session, "", "sleep 10"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := tm.NewSessionWithCommand(session, "", "sleep 10")
	if err == nil {
		t.Error("duplicate session creation should fail")
	}
}

// TestNewSessionWithCommand_Concurrent verifies multiple sessions can be created
// concurrently without errors.
func TestNewSessionWithCommand_Concurrent(t *testing.T) {
	tm := newTestTmux(t)
	n := 5
	base := "gt-test-concurrent-"

	for i := 0; i < n; i++ {
		_ = tm.KillSession(base + string(rune('a'+i)))
	}
	defer func() {
		for i := 0; i < n; i++ {
			_ = tm.KillSession(base + string(rune('a'+i)))
		}
	}()

	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			errs <- tm.NewSessionWithCommand(base+string(rune('a'+idx)), "", "sleep 5")
		}(i)
	}

	var failures int
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			failures++
			t.Logf("concurrent create %d: %v", i, err)
		}
	}
	if failures > 0 {
		t.Errorf("%d/%d concurrent session creations failed", failures, n)
	}
}

// TestWaitForCommand_Timeout verifies WaitForCommand returns an error when the
// pane command remains a shell (agent never started).
//
// Uses `bash --norc --noprofile` rather than bare `bash` so startup doesn't
// load /etc/bash.bashrc or ~/.bashrc. On memory-pressured CI runners, rc-file
// subprocesses (nvm, git prompt helpers, etc.) occasionally get SIGKILL'd by
// the OOM killer, taking bash down with them before the 250ms health check in
// NewSessionWithCommand completes — which the health check then surfaces as
// `command exited early with status ?: bash / Pane is dead (signal 9, …)`.
// The `--norc --noprofile` flags keep pane_current_command == "bash" (so
// WaitForCommand's exclude list still matches) while eliminating the flake.
// See gu-soq5.
func TestWaitForCommand_Timeout(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-waitcmd-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	if err := tm.NewSessionWithCommand(session, "", "bash --norc --noprofile"); err != nil {
		t.Fatalf("session creation: %v", err)
	}

	err := tm.WaitForCommand(session, []string{"bash", "zsh", "sh"}, 500*time.Millisecond)
	if err == nil {
		t.Error("WaitForCommand should timeout when shell is still running")
	}
}

// TestSanitizeNudgeMessage verifies control character stripping.
func TestSanitizeNudgeMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"passthrough", "hello world", "hello world"},
		{"strips ESC", "hello\x1bworld", "helloworld"},
		{"strips CR", "hello\rworld", "helloworld"},
		{"tab to space", "hello\tworld", "hello world"},
		{"preserves newline", "hello\nworld", "hello\nworld"},
		{"preserves unicode", "hello 世界", "hello 世界"},
		{"strips BS", "hello\x08world", "helloworld"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeNudgeMessage(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeNudgeMessage(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestContainsRewindIndicators verifies detection of Claude Code's Rewind menu.
func TestContainsRewindIndicators(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"normal prompt", "❯ hello world", false},
		{"busy indicator", "⏵⏵ Running tool... esc to interrupt", false},
		{"rewind with enter and esc", "Rewind\nPress Enter to select, Esc to go back", true},
		{"rewind case insensitive", "rewind history\nenter to continue\nesc to exit", true},
		{"enter to continue + esc to exit", "Some UI\nEnter to continue\nEsc to exit", true},
		{"enter to accept + esc to cancel", "Enter to accept changes\nEsc to cancel", true},
		{"enter to select + esc to cancel", "Choose a checkpoint:\nEnter to select\nEsc to cancel", true},
		{"only rewind no actions", "Rewind history shown here", false},
		{"only enter no esc", "Enter to continue", false},
		{"only esc no enter", "Esc to exit", false},
		{"conversation mentioning rewind", "User said: please rewind the video\n❯ ", false},
		{"partial match no pair", "Enter to continue\nSome other text", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsRewindIndicators(tt.content)
			if got != tt.want {
				t.Errorf("containsRewindIndicators(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

// TestSendMessageToTarget_Chunking verifies that long messages are chunked.
func TestSendMessageToTarget_Chunking(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-chunk-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	// Use cat to receive input
	if err := tm.NewSessionWithCommand(session, "", "cat"); err != nil {
		t.Fatalf("session creation: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Send a message longer than typical chunk size
	msg := strings.Repeat("A", 600)
	err := tm.sendMessageToTarget(session, msg)
	if err != nil {
		t.Fatalf("sendMessageToTarget: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	output, _ := tm.CapturePane(session, 50)
	// Count A's in output (may be split across lines)
	count := strings.Count(output, "A")
	if count < 500 {
		t.Errorf("expected ~600 A's in output, got %d (message may have been truncated)", count)
	}
}

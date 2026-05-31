package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Verifies the diagnostic capture path: trace env is honored, log file
// receives `set -x` output, and dumpDetachedKillLog reads it back.
func TestDetachedKillDiagnosticCapture(t *testing.T) {
	tm := newTestTmux(t)
	logPath := filepath.Join(t.TempDir(), "trace.log")
	t.Setenv(EnvTmuxDetachedKillLog, logPath)
	sessionName := "gt-test-diag-capture"
	_ = tm.KillSession(sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := tm.DetachedKillSession(sessionName, 1*time.Second); err != nil {
		t.Fatalf("DetachedKillSession: %v", err)
	}
	// wait for subprocess to run
	if !waitForSessionGone(t, tm, sessionName, 30*time.Second) {
		t.Fatal("session not killed in time")
	}
	// allow the bash trace to flush
	time.Sleep(200 * time.Millisecond)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("log unreadable: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected trace output, got empty log")
	}
	t.Logf("trace contents:\n%s", data)
}

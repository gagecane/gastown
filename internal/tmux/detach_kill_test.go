package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// happens, just without racing on a tight timing assumption. 120s is
	// chosen to absorb pathological scheduler latency under full
	// `go test ./...` pressure (gu-zyxl, gu-49zso — observed a 60.07s
	// overrun, just past the old 60s deadline); a real regression where the
	// subprocess never spawns or never issues the kill still fails.
	if waitForSessionGone(t, tm, sessionName, 120*time.Second) {
		return
	}
	dumpDetachedKillLog(t, logPath)
	t.Error("session should have been killed by detached subprocess")
}

// waitForSessionGone polls HasSession until the session is absent or the
// deadline elapses. Returns true if the session disappeared.
//
// The poll interval backs off from 50ms to a 1s cap. Each poll spawns a
// `tmux has-session` subprocess; a fixed tight interval over a multi-second
// wait would spawn hundreds of them, adding to the same `go test ./...`
// contention storm that starves the detached killer in the first place
// (gu-49zso). Backoff keeps the happy path fast — the kill lands ~1s in, so
// the early tight polls still catch it promptly — while throttling spawns
// during a pathologically long wait.
func waitForSessionGone(t *testing.T, tm *Tmux, name string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	interval := 50 * time.Millisecond
	const maxInterval = 1 * time.Second
	for time.Now().Before(deadline) {
		has, err := tm.HasSession(name)
		if err != nil {
			t.Fatalf("HasSession (poll): %v", err)
		}
		if !has {
			return true
		}
		time.Sleep(interval)
		if interval < maxInterval {
			interval *= 2
			if interval > maxInterval {
				interval = maxInterval
			}
		}
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
	if waitForSessionGone(t, tm, sessionName, 120*time.Second) {
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

// TestDetachedKillScriptRetries guards the structural property that makes the
// detached kill self-healing: it must poll has-session and reissue kill-session
// in a loop, not fire the kill exactly once. A fire-once script silently
// regresses to the flake this fix removed (gu-v4r86 and its three predecessors),
// where a single dropped kill leaks the session forever. This test fails fast
// and deterministically if the loop is ever refactored away.
func TestDetachedKillScriptRetries(t *testing.T) {
	script := detachedKillScript("my-sock", "sess", 1)
	for _, want := range []string{"has-session", "kill-session", "for i in", "exit 0"} {
		if !strings.Contains(script, want) {
			t.Errorf("detachedKillScript missing %q; got:\n%s", want, script)
		}
	}
	// The kill must be guarded by a has-session check that exits early, so a
	// kill that already landed does not keep looping.
	if !strings.Contains(script, "has-session -t sess 2>/dev/null || exit 0") {
		t.Errorf("expected has-session early-exit guard; got:\n%s", script)
	}
	// Empty socket name must not emit a -L flag.
	if got := detachedKillScript("", "sess", 1); strings.Contains(got, "-L") {
		t.Errorf("empty socket should omit -L flag; got:\n%s", got)
	}
}

// TestDetachedKillRecoversFromDroppedKill proves the fix end-to-end: when the
// first kill-session transiently fails (the documented under-load failure mode
// — tmux momentarily unresponsive), the detached subprocess reissues the kill
// and the session still disappears. A fake `tmux` on PATH drops the first
// kill-session, then behaves normally. Before the retry loop this leaked the
// session permanently; now it self-heals.
func TestDetachedKillRecoversFromDroppedKill(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not installed")
	}
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("cannot resolve real tmux: %v", err)
	}

	fakeDir := t.TempDir()
	counter := filepath.Join(fakeDir, "kill.count")
	// Fake tmux: for the FIRST kill-session call, record it and exit non-zero
	// (simulate a dropped kill); for everything else, delegate to real tmux.
	wrapper := "#!/bin/bash\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = kill-session ]; then\n" +
		"    if [ ! -f " + counter + " ]; then\n" +
		"      echo dropped > " + counter + "\n" +
		"      exit 1\n" +
		"    fi\n" +
		"  fi\n" +
		"done\n" +
		"exec " + realTmux + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeDir, "tmux"), []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tm := NewTmux()
	logPath := enableDetachedKillTrace(t)
	sessionName := "gt-test-detach-kill-retry"
	defer func() { _ = tm.KillSession(sessionName) }()

	// No pre-cleanup KillSession here on purpose: the fake drops the FIRST
	// kill-session it sees, and that drop must land on the detached
	// subprocess's kill (the path under test), not a pre-cleanup call that
	// would consume the drop and make this pass trivially.
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := tm.DetachedKillSession(sessionName, 1*time.Second); err != nil {
		t.Fatalf("DetachedKillSession: %v", err)
	}

	if !waitForSessionGone(t, tm, sessionName, 30*time.Second) {
		dumpDetachedKillLog(t, logPath)
		t.Fatal("session should have been killed despite the first kill being dropped")
	}
	// Confirm the first kill really was dropped — otherwise the test proves
	// nothing about retry behavior.
	if _, err := os.Stat(counter); err != nil {
		t.Errorf("expected a dropped first kill to be recorded: %v", err)
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

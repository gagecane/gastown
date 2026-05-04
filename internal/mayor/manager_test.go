package mayor

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/workspace"
)

func TestNewManager(t *testing.T) {
	m := NewManager("/tmp/test-town")
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.townRoot != "/tmp/test-town" {
		t.Errorf("townRoot = %q, want %q", m.townRoot, "/tmp/test-town")
	}
}

func TestManager_mayorDir(t *testing.T) {
	m := NewManager("/tmp/test-town")
	got := m.mayorDir()
	want := filepath.Join("/tmp/test-town", "mayor")
	if got != want {
		t.Errorf("mayorDir() = %q, want %q", got, want)
	}
}

func TestSessionName_ReturnsConsistentValue(t *testing.T) {
	name := SessionName()
	if name == "" {
		t.Error("SessionName() returned empty string")
	}
	// Verify idempotent
	if SessionName() != name {
		t.Error("SessionName() returned different values on subsequent calls")
	}
}

func TestManager_SessionName_MatchesPackageFunc(t *testing.T) {
	m := NewManager("/tmp/test-town")
	if m.SessionName() != SessionName() {
		t.Errorf("Manager.SessionName() = %q, SessionName() = %q — should match",
			m.SessionName(), SessionName())
	}
}

func TestManager_Errors(t *testing.T) {
	if ErrNotRunning.Error() != "mayor not running" {
		t.Errorf("ErrNotRunning = %q", ErrNotRunning)
	}
	if ErrAlreadyRunning.Error() != "mayor already running" {
		t.Errorf("ErrAlreadyRunning = %q", ErrAlreadyRunning)
	}
}

func TestGetMayorPrime(t *testing.T) {
	// Create a temporary directory with town.json
	tmpDir, err := os.MkdirTemp("", "mayor-prime-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create mayor directory
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}

	// Create a minimal town.json
	townConfig := &config.TownConfig{
		Name: "test-town",
	}
	townConfigPath := filepath.Join(tmpDir, workspace.PrimaryMarker)
	if err := config.SaveTownConfig(townConfigPath, townConfig); err != nil {
		t.Fatalf("failed to save town config: %v", err)
	}

	// Test GetMayorPrime
	content, err := GetMayorPrime(tmpDir)
	if err != nil {
		t.Fatalf("GetMayorPrime failed: %v", err)
	}

	// Verify content has expected elements
	if !strings.Contains(content, "[prime-rendered-at:") {
		t.Error("GetMayorPrime should contain timestamp marker")
	}
	if !strings.Contains(content, "# Mayor Context") {
		t.Error("GetMayorPrime should render mayor template")
	}
	if !strings.Contains(content, tmpDir) {
		t.Error("GetMayorPrime should contain town root path")
	}
}

func TestGetMayorPrime_InvalidTownRoot(t *testing.T) {
	// Test with non-existent directory - should still return content
	// (town name defaults to "unknown" on error)
	content, err := GetMayorPrime("/nonexistent/path")
	if err != nil {
		t.Fatalf("GetMayorPrime should not fail with invalid town root: %v", err)
	}

	// Should still have the template content
	if !strings.Contains(content, "# Mayor Context") {
		t.Error("GetMayorPrime should render mayor template even with invalid town root")
	}
}

// TestManager_StartTMUX_StartsNudgePoller is a regression test for gu-gviq.
// Mayor's StartTMUX must register a background nudge-poller so queued nudges
// are delivered while the user is AFK (the UserPromptSubmit hook only fires
// on explicit prompt submissions). Every other long-running role already does
// this; mayor was the last holdout (see gu-qpj8 / gu-gviq).
//
// We cannot invoke StartTMUX() directly in a unit test (it requires a live
// tmux server plus an agent binary). Instead we exercise the same code path
// that StartTMUX uses — nudge.StartPoller(m.townRoot, sessionID) — and assert
// that the PID file lands at the path StartTMUX and Stop() will both act on.
// A future refactor that removes nudge.StartPoller from StartTMUX without
// updating this test would leave the path assertion intact but the behaviour
// broken; reviewers should treat any change to this file's imports or
// assertions as a signal to re-verify the StartTMUX wiring.
func TestManager_StartTMUX_StartsNudgePoller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("nudge-poller process group management is not supported on Windows")
	}

	townRoot := t.TempDir()
	m := NewManager(townRoot)
	sessionID := m.SessionName()

	// Sanity: mayor's session name is deterministic and non-empty, otherwise
	// the poller key would be meaningless.
	if sessionID == "" {
		t.Fatal("Manager.SessionName() returned empty string")
	}

	// Exercise the same call StartTMUX makes after session.StartSession().
	pid, err := nudge.StartPoller(townRoot, sessionID)
	if err != nil {
		t.Fatalf("nudge.StartPoller(%q, %q) failed: %v", townRoot, sessionID, err)
	}
	if pid <= 0 {
		t.Fatalf("nudge.StartPoller returned non-positive pid %d", pid)
	}

	// Ensure we always clean up the spawned process, even on early t.Fatal.
	t.Cleanup(func() {
		_ = nudge.StopPoller(townRoot, sessionID)
	})

	// The poller PID file MUST live at the exact path StartTMUX/Stop expect:
	// <townRoot>/.runtime/nudge_poller/<sanitized-session>.pid
	// Mayor's session id contains no "/" today, but we normalize defensively
	// to match pollerPidFile() sanitization rules.
	safeSession := strings.ReplaceAll(sessionID, "/", "_")
	expectedPidPath := filepath.Join(townRoot, ".runtime", "nudge_poller", safeSession+".pid")

	data, err := os.ReadFile(expectedPidPath)
	if err != nil {
		t.Fatalf("expected nudge-poller PID file at %s after StartPoller; got error: %v",
			expectedPidPath, err)
	}

	pidFromFile, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("PID file %s contained non-integer content %q: %v", expectedPidPath, data, err)
	}
	if pidFromFile != pid {
		t.Errorf("PID file pid = %d, StartPoller returned %d — poller tracking is broken",
			pidFromFile, pid)
	}

	// The referenced process should be alive. We use signal-0 semantics:
	// FindProcess always succeeds on Unix, so we probe via Signal(0).
	proc, err := os.FindProcess(pidFromFile)
	if err != nil {
		t.Fatalf("os.FindProcess(%d) failed: %v", pidFromFile, err)
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("poller process (pid %d) is not alive: %v", pidFromFile, err)
	}

	// StopPoller must cleanly remove the PID file — this is the Stop() path.
	if err := nudge.StopPoller(townRoot, sessionID); err != nil {
		t.Fatalf("nudge.StopPoller(%q, %q) failed: %v", townRoot, sessionID, err)
	}
	if _, err := os.Stat(expectedPidPath); !os.IsNotExist(err) {
		t.Errorf("StopPoller should have removed PID file at %s; stat err = %v",
			expectedPidPath, err)
	}
}

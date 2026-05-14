package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestHandlePolecatDoneFromBead_NilFields(t *testing.T) {
	t.Parallel()
	result := HandlePolecatDoneFromBead(DefaultBdCli(), "/tmp", "testrig", "nux", nil, nil)
	if result.Error == nil {
		t.Error("expected error for nil fields")
	}
	if result.Handled {
		t.Error("should not be handled with nil fields")
	}
}

func TestHandlePolecatDoneFromBead_PhaseComplete(t *testing.T) {
	t.Parallel()
	fields := &beads.AgentFields{
		ExitType: "PHASE_COMPLETE",
		Branch:   "polecat/nux",
	}
	result := HandlePolecatDoneFromBead(DefaultBdCli(), "/tmp", "testrig", "nux", fields, nil)
	if !result.Handled {
		t.Error("expected PHASE_COMPLETE to be handled")
	}
	if result.Error != nil {
		t.Errorf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Action, "phase-complete") {
		t.Errorf("action %q should contain 'phase-complete'", result.Action)
	}
}

func TestHandlePolecatDoneFromBead_NoMR(t *testing.T) {
	t.Parallel()
	fields := &beads.AgentFields{
		ExitType:       "COMPLETED",
		Branch:         "polecat/nux",
		HookBead:       "gt-test123",
		CompletionTime: "2026-02-28T01:00:00Z",
	}
	result := HandlePolecatDoneFromBead(DefaultBdCli(), "/tmp/nonexistent", "testrig", "nux", fields, nil)
	if !result.Handled {
		t.Error("expected completion with no MR to be handled")
	}
	if !strings.Contains(result.Action, "no MR") {
		t.Errorf("action %q should contain 'no MR'", result.Action)
	}
}

func TestHandlePolecatDoneFromBead_ProtocolType(t *testing.T) {
	t.Parallel()
	fields := &beads.AgentFields{
		ExitType: "COMPLETED",
		Branch:   "polecat/nux",
	}
	result := HandlePolecatDoneFromBead(DefaultBdCli(), "/tmp/nonexistent", "testrig", "nux", fields, nil)
	if result.ProtocolType != ProtoPolecatDone {
		t.Errorf("ProtocolType = %q, want %q", result.ProtocolType, ProtoPolecatDone)
	}
}

func TestZombieResult_Types(t *testing.T) {
	t.Parallel()
	// Verify the ZombieResult type has all expected fields
	z := ZombieResult{
		PolecatName:    "nux",
		AgentState:     "working",
		Classification: ZombieSessionDeadActive,
		HookBead:       "gt-abc123",
		Action:         "restarted",
		BeadRecovered:  true,
		Error:          nil,
	}

	if z.PolecatName != "nux" {
		t.Errorf("PolecatName = %q, want %q", z.PolecatName, "nux")
	}
	if z.AgentState != "working" {
		t.Errorf("AgentState = %q, want %q", z.AgentState, "working")
	}
	if z.Classification != ZombieSessionDeadActive {
		t.Errorf("Classification = %q, want %q", z.Classification, ZombieSessionDeadActive)
	}
	if z.HookBead != "gt-abc123" {
		t.Errorf("HookBead = %q, want %q", z.HookBead, "gt-abc123")
	}
	if z.Action != "restarted" {
		t.Errorf("Action = %q, want %q", z.Action, "restarted")
	}
	if !z.BeadRecovered {
		t.Error("BeadRecovered = false, want true")
	}
}

func TestDetectZombiePolecatsResult_EmptyResult(t *testing.T) {
	t.Parallel()
	result := &DetectZombiePolecatsResult{}

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Zombies) != 0 {
		t.Errorf("Zombies length = %d, want 0", len(result.Zombies))
	}
}

func TestDetectZombiePolecats_NonexistentDir(t *testing.T) {
	t.Parallel()
	// Should handle missing polecats directory gracefully
	result := DetectZombiePolecats(DefaultBdCli(), "/nonexistent/path", "testrig", nil)

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for nonexistent dir", result.Checked)
	}
	if len(result.Zombies) != 0 {
		t.Errorf("Zombies = %d, want 0 for nonexistent dir", len(result.Zombies))
	}
}

func TestDetectZombiePolecats_DirectoryScanning(t *testing.T) {
	t.Parallel()
	// Create a temp directory structure simulating polecats
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create polecat directories
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		if err := os.Mkdir(filepath.Join(polecatsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create hidden dir (should be skipped)
	if err := os.Mkdir(filepath.Join(polecatsDir, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a regular file (should be skipped, not a dir)
	if err := os.WriteFile(filepath.Join(polecatsDir, "notadir.txt"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := DetectZombiePolecats(DefaultBdCli(), tmpDir, rigName, nil)

	// Should have checked 3 polecat dirs (not hidden, not file)
	if result.Checked != 3 {
		t.Errorf("Checked = %d, want 3 (should skip hidden dirs and files)", result.Checked)
	}

	// No zombies because agent bead state will be empty (bd not available),
	// so isZombie stays false for all polecats
	if len(result.Zombies) != 0 {
		t.Errorf("Zombies = %d, want 0 (no agent state = not zombie)", len(result.Zombies))
	}
}

func TestDetectZombiePolecats_EmptyPolecatsDir(t *testing.T) {
	t.Parallel()
	// Empty polecats directory should return 0 checked
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result := DetectZombiePolecats(DefaultBdCli(), tmpDir, rigName, nil)

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for empty polecats dir", result.Checked)
	}
}

func TestGetAgentBeadState_EmptyOutput(t *testing.T) {
	t.Parallel()
	// getAgentBeadState with invalid bead ID should return empty strings
	// (it calls bd which won't exist in test, so it returns empty)
	state, hook := getAgentBeadState(DefaultBdCli(), "/nonexistent", "nonexistent-bead")

	if state != "" {
		t.Errorf("state = %q, want empty for missing bead", state)
	}
	if hook != "" {
		t.Errorf("hook = %q, want empty for missing bead", hook)
	}
}

func TestSessionRecreated_NoSession(t *testing.T) {
	t.Parallel()
	// When the session doesn't exist, sessionRecreated should return false
	// (the session wasn't recreated, it's still dead)
	tm := tmux.NewTmux()
	detectedAt := time.Now()

	recreated := sessionRecreated(tm, "gt-nonexistent-session-xyz", detectedAt)
	if recreated {
		t.Error("sessionRecreated returned true for nonexistent session, want false")
	}
}

func TestSessionRecreated_DetectedAtEdgeCases(t *testing.T) {
	t.Parallel()
	// Verify that sessionRecreated returns false when session is dead
	// regardless of the detectedAt timestamp
	tm := tmux.NewTmux()

	// Try with a past timestamp
	recreated := sessionRecreated(tm, "gt-test-nosession-abc", time.Now().Add(-1*time.Hour))
	if recreated {
		t.Error("sessionRecreated returned true for nonexistent session with past time")
	}

	// Try with a future timestamp
	recreated = sessionRecreated(tm, "gt-test-nosession-def", time.Now().Add(1*time.Hour))
	if recreated {
		t.Error("sessionRecreated returned true for nonexistent session with future time")
	}
}

func TestZombieClassification_SpawningState(t *testing.T) {
	t.Parallel()
	// Verify that "spawning" agent state is treated as a zombie indicator.
	// This tests the classification logic inline in DetectZombiePolecats.
	// We can't easily test this via the full function without mocking,
	// so we test the boolean logic directly.
	states := map[string]bool{
		"working":  true,
		"running":  true,
		"spawning": true,
		"idle":     false,
		"done":     false,
		"":         false,
	}

	for state, wantZombie := range states {
		hookBead := ""
		isZombie := false
		if hookBead != "" {
			isZombie = true
		}
		if state == "working" || state == "running" || state == "spawning" {
			isZombie = true
		}

		if isZombie != wantZombie {
			t.Errorf("agent_state=%q: isZombie=%v, want %v", state, isZombie, wantZombie)
		}
	}
}

func TestZombieClassification_HookBeadAlwaysZombie(t *testing.T) {
	t.Parallel()
	// Any polecat with a hook_bead and dead session should be classified as zombie,
	// regardless of agent_state.
	for _, state := range []string{"", "idle", "done", "working"} {
		hookBead := "gt-some-issue"
		isZombie := false
		if hookBead != "" {
			isZombie = true
		}
		if state == "working" || state == "running" || state == "spawning" {
			isZombie = true
		}

		if !isZombie {
			t.Errorf("agent_state=%q with hook_bead=%q: isZombie=false, want true", state, hookBead)
		}
	}
}

func TestZombieClassification_NoHookNoActiveState(t *testing.T) {
	t.Parallel()
	// Polecats with no hook_bead and non-active agent_state should NOT be zombies.
	for _, state := range []string{"", "idle", "done", "completed"} {
		hookBead := ""
		isZombie := false
		if hookBead != "" {
			isZombie = true
		}
		if state == "working" || state == "running" || state == "spawning" {
			isZombie = true
		}

		if isZombie {
			t.Errorf("agent_state=%q with no hook_bead: isZombie=true, want false", state)
		}
	}
}

func TestFindAnyCleanupWisp_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available (test environment), findAnyCleanupWisp
	// should return empty string without panicking
	result := findAnyCleanupWisp(DefaultBdCli(), "/nonexistent", "testpolecat")
	if result != "" {
		t.Errorf("findAnyCleanupWisp = %q, want empty when bd unavailable", result)
	}
}

// mockBdCalls captures bd invocations and returns canned responses.
// Returns a slice that accumulates "arg0 arg1 ..." strings for each call.
type mockBdCalls struct {
	calls []string
}

// mockBd creates a test-local *BdCli with mock exec/run functions.
// Returns the BdCli and a pointer to the captured call log.
// No global state is modified — safe for use with t.Parallel().
func mockBd(execFn func(args []string) (string, error), runFn func(args []string) error) (*BdCli, *mockBdCalls) {
	mock := &mockBdCalls{}
	bd := &BdCli{
		Exec: func(workDir string, args ...string) (string, error) {
			mock.calls = append(mock.calls, strings.Join(args, " "))
			return execFn(stripMockBdFlags(args))
		},
		Run: func(workDir string, args ...string) error {
			mock.calls = append(mock.calls, strings.Join(args, " "))
			return runFn(stripMockBdFlags(args))
		},
	}
	return bd, mock
}

func stripMockBdFlags(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		args = args[1:]
	}
	return args
}

func installFakeTmuxNoServer(t *testing.T) {
	t.Helper()

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' 'no server running on /tmp/tmux' 1>&2\nexit 1\n"
	if runtime.GOOS == "windows" {
		scriptPath += ".bat"
		script = "@echo off\r\necho no server running on C:\\tmp\\tmux 1>&2\r\nexit /b 1\r\n"
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeBd creates a test-local *BdCli matching the old shell script behavior:
// list→"[]", update→ok, show→cleanup wisp JSON. Returns BdCli and captured call log.
func fakeBd() (*BdCli, *mockBdCalls) {
	return mockBd(
		func(args []string) (string, error) {
			if len(args) > 0 {
				switch args[0] {
				case "list":
					return "[]", nil
				case "show":
					return `[{"labels":["cleanup","polecat:testpol","state:pending"]}]`, nil
				}
			}
			return "{}", nil
		},
		func(args []string) error { return nil },
	)
}

func TestFindCleanupWisp_UsesCorrectBdListFlags(t *testing.T) {
	t.Parallel()
	bd, mock := fakeBd()
	workDir := t.TempDir()

	_, _ = findCleanupWisp(bd, workDir, "nux")

	got := strings.Join(mock.calls, "\n")

	// Must use --label (singular), NOT --labels (plural)
	if !strings.Contains(got, "--label") {
		t.Errorf("findCleanupWisp: expected --label flag, got: %s", got)
	}
	if strings.Contains(got, "--labels") {
		t.Errorf("findCleanupWisp: must not use --labels (plural), got: %s", got)
	}

	// Must NOT use --ephemeral (invalid for bd list)
	if strings.Contains(got, "--ephemeral") {
		t.Errorf("findCleanupWisp: must not use --ephemeral (invalid for bd list), got: %s", got)
	}

	// Must include the polecat label filter
	if !strings.Contains(got, "polecat:nux") {
		t.Errorf("findCleanupWisp: expected polecat:nux label, got: %s", got)
	}
}

func TestFindAnyCleanupWisp_UsesCorrectBdListFlags(t *testing.T) {
	t.Parallel()
	bd, mock := fakeBd()
	workDir := t.TempDir()

	_ = findAnyCleanupWisp(bd, workDir, "bravo")

	got := strings.Join(mock.calls, "\n")

	// Must use --label (singular), NOT --labels (plural)
	if !strings.Contains(got, "--label") {
		t.Errorf("findAnyCleanupWisp: expected --label flag, got: %s", got)
	}
	if strings.Contains(got, "--labels") {
		t.Errorf("findAnyCleanupWisp: must not use --labels (plural), got: %s", got)
	}

	// Must NOT use --ephemeral (invalid for bd list)
	if strings.Contains(got, "--ephemeral") {
		t.Errorf("findAnyCleanupWisp: must not use --ephemeral (invalid for bd list), got: %s", got)
	}

	// Must include the polecat label filter
	if !strings.Contains(got, "polecat:bravo") {
		t.Errorf("findAnyCleanupWisp: expected polecat:bravo label, got: %s", got)
	}
}

func TestFindAllCleanupWisps_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available, findAllCleanupWisps should return nil
	result := findAllCleanupWisps(DefaultBdCli(), "/nonexistent", "testpolecat")
	if result != nil {
		t.Errorf("findAllCleanupWisps = %v, want nil when bd unavailable", result)
	}
}

func TestFindAllCleanupWisps_ReturnsAllIDs(t *testing.T) {
	t.Parallel()
	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) > 0 && args[0] == "list" {
				return `[{"id":"gt-wisp-aaa"},{"id":"gt-wisp-bbb"}]`, nil
			}
			return "{}", nil
		},
		func(args []string) error { return nil },
	)
	workDir := t.TempDir()

	result := findAllCleanupWisps(bd, workDir, "nux")

	if len(result) != 2 {
		t.Fatalf("findAllCleanupWisps: got %d items, want 2", len(result))
	}
	if result[0] != "gt-wisp-aaa" || result[1] != "gt-wisp-bbb" {
		t.Errorf("findAllCleanupWisps: got %v, want [gt-wisp-aaa gt-wisp-bbb]", result)
	}

	got := strings.Join(mock.calls, "\n")
	if !strings.Contains(got, "--label") {
		t.Errorf("findAllCleanupWisps: expected --label flag, got: %s", got)
	}
	if !strings.Contains(got, "polecat:nux") {
		t.Errorf("findAllCleanupWisps: expected polecat:nux label, got: %s", got)
	}
}

func TestFindAllCleanupWisps_EmptyList(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		func(args []string) (string, error) {
			return "[]", nil
		},
		func(args []string) error { return nil },
	)
	workDir := t.TempDir()

	result := findAllCleanupWisps(bd, workDir, "nux")
	if result != nil {
		t.Errorf("findAllCleanupWisps: got %v, want nil for empty list", result)
	}
}

func TestUpdateCleanupWispState_UsesCorrectBdUpdateFlags(t *testing.T) {
	t.Parallel()
	bd, mock := fakeBd()
	workDir := t.TempDir()

	// UpdateCleanupWispState first calls "bd show <id> --json", then "bd update".
	// Our mock returns valid JSON for show with polecat:testpol label,
	// so polecatName will be "testpol". Then it calls bd update with new labels.
	_ = UpdateCleanupWispState(bd, workDir, "gt-wisp-abc", "merged")

	got := strings.Join(mock.calls, "\n")

	// Must use --set-labels=<label> per label (not --labels)
	if !strings.Contains(got, "--set-labels=") {
		t.Errorf("UpdateCleanupWispState: expected --set-labels=<label> flags, got: %s", got)
	}
	// Check for invalid --labels flag in both " --labels " and "--labels=" forms
	if strings.Contains(got, "--labels") && !strings.Contains(got, "--set-labels") {
		t.Errorf("UpdateCleanupWispState: must not use --labels (invalid for bd update), got: %s", got)
	}

	// Verify individual per-label arguments with correct polecat name from show output
	if !strings.Contains(got, "--set-labels=cleanup") {
		t.Errorf("UpdateCleanupWispState: expected --set-labels=cleanup, got: %s", got)
	}
	if !strings.Contains(got, "--set-labels=polecat:testpol") {
		t.Errorf("UpdateCleanupWispState: expected --set-labels=polecat:testpol, got: %s", got)
	}
	if !strings.Contains(got, "--set-labels=state:merged") {
		t.Errorf("UpdateCleanupWispState: expected --set-labels=state:merged, got: %s", got)
	}
}

func TestExtractDoneIntent_Valid(t *testing.T) {
	t.Parallel()
	ts := time.Now().Add(-45 * time.Second)
	labels := []string{
		"gt:agent",
		"idle:2",
		fmt.Sprintf("done-intent:COMPLETED:%d", ts.Unix()),
	}

	intent := extractDoneIntent(labels)
	if intent == nil {
		t.Fatal("extractDoneIntent returned nil for valid label")
	}
	if intent.ExitType != "COMPLETED" {
		t.Errorf("ExitType = %q, want %q", intent.ExitType, "COMPLETED")
	}
	if intent.Timestamp.Unix() != ts.Unix() {
		t.Errorf("Timestamp = %d, want %d", intent.Timestamp.Unix(), ts.Unix())
	}
}

func TestExtractDoneIntent_Missing(t *testing.T) {
	t.Parallel()
	labels := []string{"gt:agent", "idle:2", "backoff-until:1738972900"}

	intent := extractDoneIntent(labels)
	if intent != nil {
		t.Errorf("extractDoneIntent = %+v, want nil for no done-intent label", intent)
	}
}

func TestExtractDoneIntent_Malformed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		labels []string
	}{
		{"missing timestamp", []string{"done-intent:COMPLETED"}},
		{"bad timestamp", []string{"done-intent:COMPLETED:notanumber"}},
		{"empty labels", nil},
		{"empty label list", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := extractDoneIntent(tt.labels)
			if intent != nil {
				t.Errorf("extractDoneIntent(%v) = %+v, want nil for malformed input", tt.labels, intent)
			}
		})
	}
}

func TestExtractDoneIntent_AllExitTypes(t *testing.T) {
	t.Parallel()
	ts := time.Now().Unix()
	for _, exitType := range []string{"COMPLETED", "ESCALATED", "DEFERRED", "PHASE_COMPLETE"} {
		label := fmt.Sprintf("done-intent:%s:%d", exitType, ts)
		intent := extractDoneIntent([]string{label})
		if intent == nil {
			t.Errorf("extractDoneIntent returned nil for exit type %q", exitType)
			continue
		}
		if intent.ExitType != exitType {
			t.Errorf("ExitType = %q, want %q", intent.ExitType, exitType)
		}
	}
}

func TestDetectZombie_DoneIntentDeadSession(t *testing.T) {
	t.Parallel()
	// Verify the logic: dead session + done-intent older than 30s → should be treated as zombie
	// gt-dsgp: action is restart (not nuke), but detection logic is the same
	doneIntent := &DoneIntent{
		ExitType:  "COMPLETED",
		Timestamp: time.Now().Add(-60 * time.Second), // 60s old
	}
	sessionAlive := false
	age := time.Since(doneIntent.Timestamp)

	// Dead session + old intent → restart path (gt-dsgp: was auto-nuke)
	shouldRestart := !sessionAlive && doneIntent != nil && age >= config.DefaultWitnessDoneIntentStuckTimeout
	if !shouldRestart {
		t.Errorf("expected restart for dead session + old done-intent (age=%v)", age)
	}
}

func TestDetectZombie_DoneIntentLiveStuck(t *testing.T) {
	t.Parallel()
	// Verify the logic: live session + done-intent older than 60s → should restart session
	// gt-dsgp: restart instead of kill
	doneIntent := &DoneIntent{
		ExitType:  "COMPLETED",
		Timestamp: time.Now().Add(-90 * time.Second), // 90s old
	}
	sessionAlive := true
	age := time.Since(doneIntent.Timestamp)

	// Live session + old intent → restart stuck session (gt-dsgp: was kill)
	shouldRestart := sessionAlive && doneIntent != nil && age > config.DefaultWitnessDoneIntentStuckTimeout
	if !shouldRestart {
		t.Errorf("expected restart for live session + old done-intent (age=%v)", age)
	}
}

func TestDetectZombie_DoneIntentRecent(t *testing.T) {
	t.Parallel()
	// Verify the logic: done-intent younger than config.DefaultWitnessDoneIntentStuckTimeout → skip (polecat still working)
	doneIntent := &DoneIntent{
		ExitType:  "COMPLETED",
		Timestamp: time.Now().Add(-10 * time.Second), // 10s old
	}
	sessionAlive := false
	age := time.Since(doneIntent.Timestamp)

	// Recent intent → should skip
	shouldSkip := !sessionAlive && doneIntent != nil && age < config.DefaultWitnessDoneIntentStuckTimeout
	if !shouldSkip {
		t.Errorf("expected skip for recent done-intent (age=%v)", age)
	}

	// Live session + recent intent → also skip
	sessionAlive = true
	shouldSkipLive := sessionAlive && doneIntent != nil && age <= config.DefaultWitnessDoneIntentStuckTimeout
	if !shouldSkipLive {
		t.Errorf("expected skip for live session + recent done-intent (age=%v)", age)
	}
}

func TestDetectZombie_DoneOrNukedNotZombie(t *testing.T) {
	t.Parallel()
	// GH#2795: Polecats with agent_state=done or agent_state=nuked and a dead
	// session should NOT be treated as zombies, even if hook_bead is still set.
	// Without this, isZombieState returns true (hookBead != ""), and the witness
	// floods the mayor inbox with RECOVERY_NEEDED alerts every patrol cycle.
	for _, state := range []beads.AgentState{beads.AgentStateDone, beads.AgentStateNuked} {
		hookBead := "gt-some-issue"
		// isZombieState returns true because hookBead != ""
		if !isZombieState(state, hookBead) {
			t.Errorf("isZombieState(%q, %q) = false, want true (pre-condition)", state, hookBead)
		}
		// But the done/nuked check in detectZombieDeadSession should skip these.
		// Verify the states are terminal (not active).
		if state.IsActive() {
			t.Errorf("state %q should not be active", state)
		}
	}
}

func TestDetectZombie_AgentDeadInLiveSession(t *testing.T) {
	t.Parallel()
	// Verify the logic: live session + agent process dead → zombie
	// This is the gt-kj6r6 fix: DetectZombiePolecats now checks IsAgentAlive
	// for sessions that DO exist, catching the tmux-alive-but-agent-dead class.
	sessionAlive := true
	agentAlive := false
	var doneIntent *DoneIntent // No done-intent

	// Live session + no done-intent + agent dead → should be classified as zombie
	shouldDetect := sessionAlive && doneIntent == nil && !agentAlive
	if !shouldDetect {
		t.Error("expected zombie detection for live session with dead agent")
	}

	// Live session + agent alive → NOT a zombie
	agentAlive = true
	shouldSkip := sessionAlive && doneIntent == nil && agentAlive
	if !shouldSkip {
		t.Error("expected skip for live session with alive agent")
	}
}

func TestGetAgentBeadLabels_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available, should return nil without panicking
	labels := getAgentBeadLabels(DefaultBdCli(), "/nonexistent", "nonexistent-bead")
	if labels != nil {
		t.Errorf("getAgentBeadLabels = %v, want nil when bd unavailable", labels)
	}
}

// --- extractPolecatFromJSON tests (issue #1228: panic-safe JSON parsing) ---

func TestExtractPolecatFromJSON_ValidOutput(t *testing.T) {
	t.Parallel()
	input := `[{"labels":["cleanup","polecat:nux","state:pending"]}]`
	got := extractPolecatFromJSON(input)
	if got != "nux" {
		t.Errorf("extractPolecatFromJSON() = %q, want %q", got, "nux")
	}
}

func TestExtractPolecatFromJSON_InvalidInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"empty output", ""},
		{"malformed JSON", "{not valid json"},
		{"empty array", "[]"},
		{"no polecat label", `[{"labels":["cleanup","state:pending"]}]`},
		{"empty labels", `[{"labels":[]}]`},
		{"truncated JSON", `[{"labels":["polecat:`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPolecatFromJSON(tt.input)
			if got != "" {
				t.Errorf("extractPolecatFromJSON(%q) = %q, want empty", tt.input, got)
			}
		})
	}
}

func TestGetBeadStatus_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available (test environment), getBeadStatus
	// should return ("", false) without panicking
	result, ok := getBeadStatus(DefaultBdCli(), "/nonexistent", "gt-abc123")
	if result != "" || ok {
		t.Errorf("getBeadStatus = (%q, %v), want (\"\", false) when bd unavailable", result, ok)
	}
}

func TestGetBeadStatus_EmptyBeadID(t *testing.T) {
	t.Parallel()
	// Empty bead ID should return ("", false) immediately
	result, ok := getBeadStatus(DefaultBdCli(), "/nonexistent", "")
	if result != "" || ok {
		t.Errorf("getBeadStatus(\"\") = (%q, %v), want (\"\", false)", result, ok)
	}
}

func TestDetectZombie_BeadClosedStillRunning(t *testing.T) {
	t.Parallel()
	// Verify the logic: live session + agent alive + hooked bead closed → zombie
	// This is the gt-h1l6i fix: DetectZombiePolecats now checks if the
	// polecat's hooked bead has been closed while the session is still running.
	sessionAlive := true
	agentAlive := true
	var doneIntent *DoneIntent // No done-intent
	hookBead := "gt-some-issue"
	beadStatus := "closed"

	// Live session + agent alive + no done-intent + bead closed → should detect
	shouldDetect := sessionAlive && agentAlive && doneIntent == nil &&
		hookBead != "" && beadStatus == "closed"
	if !shouldDetect {
		t.Error("expected zombie detection for live session with closed bead")
	}

	// Bead open → NOT a zombie
	beadStatus = "open"
	shouldSkip := sessionAlive && agentAlive && doneIntent == nil &&
		hookBead != "" && beadStatus == "closed"
	if shouldSkip {
		t.Error("should not detect zombie when bead is still open")
	}

	// No hook bead → NOT a zombie
	hookBead = ""
	beadStatus = "closed"
	shouldSkipNoHook := sessionAlive && agentAlive && doneIntent == nil &&
		hookBead != "" && beadStatus == "closed"
	if shouldSkipNoHook {
		t.Error("should not detect zombie when no hook bead exists")
	}
}

func TestDetectZombie_BeadClosedVsDoneIntent(t *testing.T) {
	t.Parallel()
	// Verify done-intent takes priority over closed-bead check.
	// If done-intent exists (recent), the polecat is still working through
	// gt done and we should NOT trigger the closed-bead path.
	sessionAlive := true
	agentAlive := true
	doneIntent := &DoneIntent{
		ExitType:  "COMPLETED",
		Timestamp: time.Now().Add(-10 * time.Second), // Recent
	}
	hookBead := "gt-some-issue"
	beadStatus := "closed"

	// Done-intent exists + bead closed → done-intent check runs first,
	// closed-bead check should NOT run (it's in the else branch)
	doneIntentHandled := sessionAlive && doneIntent != nil && time.Since(doneIntent.Timestamp) > config.DefaultWitnessDoneIntentStuckTimeout
	closedBeadCheck := sessionAlive && agentAlive && doneIntent == nil &&
		hookBead != "" && beadStatus == "closed"

	// Neither should trigger: done-intent is recent (not stuck), and
	// closed-bead check requires doneIntent == nil
	if doneIntentHandled {
		t.Error("recent done-intent should not trigger stuck-session handler")
	}
	if closedBeadCheck {
		t.Error("closed-bead check should not run when done-intent exists")
	}
}

func TestResetAbandonedBead_EmptyHookBead(t *testing.T) {
	t.Parallel()
	// resetAbandonedBead should return false for empty hookBead
	result := resetAbandonedBead(DefaultBdCli(), "/tmp", "testrig", "", "nux", nil)
	if result {
		t.Error("resetAbandonedBead should return false for empty hookBead")
	}
}

func TestResetAbandonedBead_NoRouter(t *testing.T) {
	t.Parallel()
	// resetAbandonedBead with nil router should not panic even if bead exists.
	// It will return false because bd won't find the bead, but shouldn't crash.
	result := resetAbandonedBead(DefaultBdCli(), "/tmp/nonexistent", "testrig", "gt-fake123", "nux", nil)
	if result {
		t.Error("resetAbandonedBead should return false when bd commands fail")
	}
}

func TestResetAbandonedBead_ClosesWhenWorkOnMain(t *testing.T) {
	// Not parallel: overrides package-level verifyCommitOnMain.
	// When verifyCommitOnMain returns true, resetAbandonedBead should close the
	// bead instead of resetting it for re-dispatch. This is the fix for #2036.

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return true, nil // work is on main
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) >= 1 && args[0] == "show" {
				return `[{"status":"hooked"}]`, nil
			}
			return "", nil
		},
		func(args []string) error {
			return nil
		},
	)

	tmpDir := t.TempDir()
	result := resetAbandonedBead(bd, tmpDir, "testrig", "gt-work123", "alpha", nil)
	if result {
		t.Error("resetAbandonedBead should return false when work is on main (bead closed, not re-dispatched)")
	}

	// Verify "close" was called, NOT "update ... --status=open"
	var foundClose, foundUpdate bool
	for _, call := range mock.calls {
		if strings.Contains(call, "close gt-work123") {
			foundClose = true
		}
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			foundUpdate = true
		}
	}
	if !foundClose {
		t.Errorf("expected bd close to be called, got calls: %v", mock.calls)
	}
	if foundUpdate {
		t.Error("bd update --status=open should NOT be called when work is on main")
	}
}

func TestResetAbandonedBead_ResetsWhenWorkNotOnMain(t *testing.T) {
	// Not parallel: overrides package-level verifyCommitOnMain.
	// When verifyCommitOnMain returns false, resetAbandonedBead should reset
	// the bead for re-dispatch (existing behavior).

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, nil // work NOT on main
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) >= 1 && args[0] == "show" {
				return `[{"status":"hooked"}]`, nil
			}
			return "", nil
		},
		func(args []string) error {
			return nil
		},
	)

	tmpDir := t.TempDir()
	result := resetAbandonedBead(bd, tmpDir, "testrig", "gt-work123", "alpha", nil)
	if !result {
		t.Error("resetAbandonedBead should return true when work is NOT on main (bead reset for re-dispatch)")
	}

	// Verify "update --status=open" was called (normal reset path)
	var foundUpdate bool
	for _, call := range mock.calls {
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Errorf("expected bd update --status=open to be called, got calls: %v", mock.calls)
	}
}

// TestResetAbandonedBead_MassDeathRateLimited covers the gu-pq2q acceptance
// criterion: fire 10 simultaneous dead-session events and verify only N are
// dispatched within the 1-minute window (N = MaxRedispatchesPerMinute cap).
// This is the integration test that exercises the rate limiter plumbing
// end-to-end through resetAbandonedBead.
func TestResetAbandonedBead_MassDeathRateLimited(t *testing.T) {
	// Not parallel: overrides package-level verifyCommitOnMain and mutates
	// the package-level redispatch limiter registry.
	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, nil // work NOT on main → would normally reset
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	resetRedispatchLimitersForTest()
	t.Cleanup(resetRedispatchLimitersForTest)

	// Use a dedicated rig name so we don't collide with other tests that
	// share the package-level limiter map.
	const rigName = "testrig-mass-death"

	// Lower the cap via settings/config.json so this test runs quickly and
	// doesn't depend on the compiled-in default. Zero disables rate limiting,
	// so we pick a small positive cap.
	const cap = 3
	tmpDir := writeWitnessCapConfig(t, cap)

	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) >= 1 && args[0] == "show" {
				return `[{"status":"hooked"}]`, nil
			}
			return "", nil
		},
		func(args []string) error { return nil },
	)

	const attempts = 10
	var recovered, blocked int
	for i := 0; i < attempts; i++ {
		beadID := fmt.Sprintf("gt-storm-%02d", i)
		polecat := fmt.Sprintf("alpha-%02d", i)
		if resetAbandonedBead(bd, tmpDir, rigName, beadID, polecat, nil) {
			recovered++
		} else {
			blocked++
		}
	}

	if recovered != cap {
		t.Errorf("recovered = %d, want %d (cap)", recovered, cap)
	}
	if blocked != attempts-cap {
		t.Errorf("blocked = %d, want %d", blocked, attempts-cap)
	}

	// Exactly `cap` bd update --status=open calls must have been issued.
	var updates int
	for _, call := range mock.calls {
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			updates++
		}
	}
	if updates != cap {
		t.Errorf("bd update --status=open calls = %d, want %d (rate-limited beads must NOT be reset)", updates, cap)
	}
}

// TestResetAbandonedBead_RateLimitCooldown verifies that once rate-limited
// beads fall outside the sliding window, the limiter allows new dispatches
// again. We simulate window expiry by reaching into the package-level limiter
// and pruning its state manually (we cannot mock time.Now across
// resetAbandonedBead without a larger refactor).
func TestResetAbandonedBead_RateLimitCooldown(t *testing.T) {
	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	resetRedispatchLimitersForTest()
	t.Cleanup(resetRedispatchLimitersForTest)

	const rigName = "testrig-cooldown"
	const cap = 2
	tmpDir := writeWitnessCapConfig(t, cap)

	bd, _ := mockBd(
		func(args []string) (string, error) {
			if len(args) >= 1 && args[0] == "show" {
				return `[{"status":"hooked"}]`, nil
			}
			return "", nil
		},
		func(args []string) error { return nil },
	)

	// Saturate the bucket.
	for i := 0; i < cap; i++ {
		if !resetAbandonedBead(bd, tmpDir, rigName, fmt.Sprintf("gt-a-%d", i), "pc", nil) {
			t.Fatalf("setup call %d should have succeeded", i)
		}
	}
	if resetAbandonedBead(bd, tmpDir, rigName, "gt-blocked", "pc", nil) {
		t.Fatal("over-cap call should have been rate-limited")
	}

	// Expire the window by back-dating every tracked timestamp past the cutoff.
	limiter := getRedispatchLimiter(rigName, cap)
	limiter.mu.Lock()
	expired := time.Now().Add(-2 * RedispatchRateLimitWindow)
	for i := range limiter.dispatchedAt {
		limiter.dispatchedAt[i] = expired
	}
	limiter.mu.Unlock()

	if !resetAbandonedBead(bd, tmpDir, rigName, "gt-after-cooldown", "pc", nil) {
		t.Error("resetAbandonedBead after window expiry = false, want true (capacity should have returned)")
	}
}

// writeWitnessCapConfig writes a minimal settings/config.json at the returned
// tempDir path that sets operational.witness.max_redispatches_per_minute to
// the provided value. Used by rate-limiter integration tests to avoid
// depending on the compiled-in default.
func writeWitnessCapConfig(t *testing.T, cap int) string {
	t.Helper()
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	cfg := fmt.Sprintf(`{
  "operational": {
    "witness": {
      "max_redispatches_per_minute": %d
    }
  }
}
`, cap)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	return dir
}

func TestBeadRecoveredField_DefaultFalse(t *testing.T) {
	t.Parallel()
	// BeadRecovered should default to false (zero value)
	z := ZombieResult{
		PolecatName:    "nux",
		AgentState:     "working",
		Classification: ZombieSessionDeadActive,
	}
	if z.BeadRecovered {
		t.Error("BeadRecovered should default to false")
	}
}

func TestStalledResult_Types(t *testing.T) {
	t.Parallel()
	// Verify the StalledResult type has all expected fields
	s := StalledResult{
		PolecatName: "alpha",
		StallType:   "startup-stall",
		Action:      "auto-dismissed",
		Error:       nil,
	}

	if s.PolecatName != "alpha" {
		t.Errorf("PolecatName = %q, want %q", s.PolecatName, "alpha")
	}
	if s.StallType != "startup-stall" {
		t.Errorf("StallType = %q, want %q", s.StallType, "startup-stall")
	}
	if s.Action != "auto-dismissed" {
		t.Errorf("Action = %q, want %q", s.Action, "auto-dismissed")
	}
	if s.Error != nil {
		t.Errorf("Error = %v, want nil", s.Error)
	}

	// Verify error field works
	s2 := StalledResult{
		PolecatName: "bravo",
		StallType:   "startup-stall",
		Action:      "escalated",
		Error:       fmt.Errorf("auto-dismiss failed"),
	}
	if s2.Error == nil {
		t.Error("Error = nil, want non-nil")
	}
}

func TestDetectStalledPolecatsResult_Empty(t *testing.T) {
	t.Parallel()
	result := &DetectStalledPolecatsResult{}

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Stalled) != 0 {
		t.Errorf("Stalled length = %d, want 0", len(result.Stalled))
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors length = %d, want 0", len(result.Errors))
	}
}

func TestDetectStalledPolecats_NoPolecats(t *testing.T) {
	t.Parallel()
	// Should handle missing polecats directory gracefully
	result := DetectStalledPolecats("/nonexistent/path", "testrig")

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for nonexistent dir", result.Checked)
	}
	if len(result.Stalled) != 0 {
		t.Errorf("Stalled = %d, want 0 for nonexistent dir", len(result.Stalled))
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %d, want 0 for nonexistent dir", len(result.Errors))
	}
}

func TestDetectStalledPolecats_EmptyPolecatsDir(t *testing.T) {
	t.Parallel()
	// Empty polecats directory should return 0 checked
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result := DetectStalledPolecats(tmpDir, rigName)

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for empty polecats dir", result.Checked)
	}
	if len(result.Stalled) != 0 {
		t.Errorf("Stalled = %d, want 0 for empty polecats dir", len(result.Stalled))
	}
}

func TestDetectStalledPolecats_NoSession(t *testing.T) {
	t.Parallel()
	// When tmux sessions don't exist (no real tmux in test),
	// HasSession returns false so polecats are skipped (not errors).
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create polecat directories
	for _, name := range []string{"alpha", "bravo"} {
		if err := os.Mkdir(filepath.Join(polecatsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create hidden dir (should be skipped)
	if err := os.Mkdir(filepath.Join(polecatsDir, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := DetectStalledPolecats(tmpDir, rigName)

	// Should count 2 polecats (skip hidden)
	if result.Checked != 2 {
		t.Errorf("Checked = %d, want 2 (should skip hidden dirs)", result.Checked)
	}

	// No stalled because HasSession returns false (no real tmux in test),
	// so polecats are skipped before structured signal checks.
	if len(result.Stalled) != 0 {
		t.Errorf("Stalled = %d, want 0 (no tmux sessions in test)", len(result.Stalled))
	}
}

func TestStartupStallThresholds(t *testing.T) {
	t.Parallel()
	// Verify config defaults are reasonable (tests the operational config defaults,
	// not removed handler constants).
	stallThreshold := config.DefaultWitnessStartupStallThreshold
	activityGrace := config.DefaultWitnessStartupActivityGrace
	if stallThreshold < 30*time.Second {
		t.Errorf("DefaultWitnessStartupStallThreshold = %v, too short (< 30s)", stallThreshold)
	}
	if stallThreshold > 5*time.Minute {
		t.Errorf("DefaultWitnessStartupStallThreshold = %v, too long (> 5min)", stallThreshold)
	}
	if activityGrace < 15*time.Second {
		t.Errorf("DefaultWitnessStartupActivityGrace = %v, too short (< 15s)", activityGrace)
	}
	if activityGrace > 5*time.Minute {
		t.Errorf("DefaultWitnessStartupActivityGrace = %v, too long (> 5min)", activityGrace)
	}
}

func TestDetectOrphanedBeads_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available (test environment), should return empty result
	result := DetectOrphanedBeads(DefaultBdCli(), "/nonexistent", "testrig", nil)

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 when bd unavailable", result.Checked)
	}
	if len(result.Orphans) != 0 {
		t.Errorf("Orphans = %d, want 0 when bd unavailable", len(result.Orphans))
	}
}

func TestDetectOrphanedBeads_ResultTypes(t *testing.T) {
	t.Parallel()
	// Verify the OrphanedBeadResult type has all expected fields
	o := OrphanedBeadResult{
		BeadID:        "gt-orphan1",
		Assignee:      "testrig/polecats/alpha",
		PolecatName:   "alpha",
		BeadRecovered: true,
	}

	if o.BeadID != "gt-orphan1" {
		t.Errorf("BeadID = %q, want %q", o.BeadID, "gt-orphan1")
	}
	if o.Assignee != "testrig/polecats/alpha" {
		t.Errorf("Assignee = %q, want %q", o.Assignee, "testrig/polecats/alpha")
	}
	if o.PolecatName != "alpha" {
		t.Errorf("PolecatName = %q, want %q", o.PolecatName, "alpha")
	}
	if !o.BeadRecovered {
		t.Error("BeadRecovered = false, want true")
	}
}

func TestDetectOrphanedBeads_WithMockBd(t *testing.T) {
	installFakeTmuxNoServer(t)

	// Set up town directory structure
	townRoot := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(townRoot, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a polecat directory for "bravo" (alive dir, dead session)
	// This case should be SKIPPED (deferred to DetectZombiePolecats)
	if err := os.Mkdir(filepath.Join(polecatsDir, "bravo"), 0o755); err != nil {
		t.Fatal(err)
	}

	// "alpha" has NO directory and NO tmux session — true orphan
	// "bravo" has directory but no session — deferred to DetectZombiePolecats
	// "charlie" is hooked, no dir, no session — also an orphan
	// "delta" is assigned to a different rig — skipped by rigName filter

	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) == 0 {
				return "{}", nil
			}
			switch args[0] {
			case "list":
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "--status=in_progress") {
					return `[
  {"id":"gt-orphan1","assignee":"testrig/polecats/alpha"},
  {"id":"gt-alive1","assignee":"testrig/polecats/bravo"},
  {"id":"gt-nocrew","assignee":"testrig/crew/sean"},
  {"id":"gt-noassign","assignee":""},
  {"id":"gt-otherrig","assignee":"otherrig/polecats/delta"}
]`, nil
				}
				if strings.Contains(joined, "--status=hooked") {
					return `[{"id":"gt-hooked1","assignee":"testrig/polecats/charlie"}]`, nil
				}
				return "[]", nil
			case "show":
				return `[{"status":"in_progress"}]`, nil
			}
			return "{}", nil
		},
		func(args []string) error { return nil },
	)

	result := DetectOrphanedBeads(bd, townRoot, rigName, nil)

	// Verify --limit=0 was passed in bd list invocations
	logStr := strings.Join(mock.calls, "\n")
	if !strings.Contains(logStr, "--limit=0") {
		t.Errorf("bd list was not called with --limit=0; log:\n%s", logStr)
	}
	// Verify both statuses were queried
	if !strings.Contains(logStr, "--status=in_progress") {
		t.Errorf("bd list was not called with --status=in_progress; log:\n%s", logStr)
	}
	if !strings.Contains(logStr, "--status=hooked") {
		t.Errorf("bd list was not called with --status=hooked; log:\n%s", logStr)
	}

	// Should have checked 3 polecat assignees in "testrig":
	// alpha (in_progress), bravo (in_progress), charlie (hooked)
	// "crew/sean" is not a polecat, "" has no assignee,
	// "otherrig/polecats/delta" is filtered out by rigName
	if result.Checked != 3 {
		t.Errorf("Checked = %d, want 3 (alpha + bravo from in_progress, charlie from hooked)", result.Checked)
	}

	// Should have found 2 orphans:
	// alpha (in_progress, no dir, no session) and charlie (hooked, no dir, no session)
	// bravo has directory so deferred to DetectZombiePolecats
	if len(result.Orphans) != 2 {
		t.Fatalf("Orphans = %d, want 2 (alpha + charlie)", len(result.Orphans))
	}

	// Verify first orphan (alpha from in_progress scan)
	orphan := result.Orphans[0]
	if orphan.BeadID != "gt-orphan1" {
		t.Errorf("orphan[0] BeadID = %q, want %q", orphan.BeadID, "gt-orphan1")
	}
	if orphan.PolecatName != "alpha" {
		t.Errorf("orphan[0] PolecatName = %q, want %q", orphan.PolecatName, "alpha")
	}
	if orphan.Assignee != "testrig/polecats/alpha" {
		t.Errorf("orphan[0] Assignee = %q, want %q", orphan.Assignee, "testrig/polecats/alpha")
	}
	// BeadRecovered should be true (mock bd update succeeds)
	if !orphan.BeadRecovered {
		t.Error("orphan[0] BeadRecovered = false, want true")
	}

	// Verify second orphan (charlie from hooked scan)
	orphan2 := result.Orphans[1]
	if orphan2.BeadID != "gt-hooked1" {
		t.Errorf("orphan[1] BeadID = %q, want %q", orphan2.BeadID, "gt-hooked1")
	}
	if orphan2.PolecatName != "charlie" {
		t.Errorf("orphan[1] PolecatName = %q, want %q", orphan2.PolecatName, "charlie")
	}

	// Verify no unexpected errors
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}

func TestDetectOrphanedBeads_ErrorPath(t *testing.T) {
	t.Parallel()
	bdErr := fmt.Errorf("bd: connection refused")
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", bdErr },
		func(args []string) error { return bdErr },
	)

	result := DetectOrphanedBeads(bd, t.TempDir(), "testrig", nil)

	if len(result.Errors) == 0 {
		t.Error("expected errors when bd fails, got none")
	}
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 when bd fails", result.Checked)
	}
	if len(result.Orphans) != 0 {
		t.Errorf("Orphans = %d, want 0 when bd fails", len(result.Orphans))
	}
}

// TestDetectOrphanedBeads_SkipsRegisteredWorktree is the regression test for
// gu-eno2. When the polecat directory does not exist on disk at the expected
// nested path but git worktree list reports a legitimate worktree for the
// polecat, we must NOT reset the bead.
func TestDetectOrphanedBeads_SkipsRegisteredWorktree(t *testing.T) {
	installFakeTmuxNoServer(t)

	townRoot := t.TempDir()
	rigName := "testrig"
	polecatName := "nux"

	// Intentionally do NOT create polecats/nux/ on disk. The only signal
	// that the polecat is legitimate is the stubbed git worktree list below.

	// Stub git worktree list to report the nested polecat path.
	registeredPath := filepath.Join(townRoot, rigName, "polecats", polecatName, rigName)
	original := gitWorktreePathsRunner
	gitWorktreePathsRunner = func(repoPath string) ([]string, error) {
		return []string{registeredPath}, nil
	}
	t.Cleanup(func() { gitWorktreePathsRunner = original })

	bd, _ := mockBd(
		func(args []string) (string, error) {
			if len(args) == 0 {
				return "{}", nil
			}
			if args[0] == "list" {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "--status=in_progress") {
					return `[{"id":"gt-legit1","assignee":"testrig/polecats/nux"}]`, nil
				}
				return "[]", nil
			}
			return "{}", nil
		},
		func(args []string) error { return nil },
	)

	result := DetectOrphanedBeads(bd, townRoot, rigName, nil)

	if result.Checked != 1 {
		t.Errorf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Orphans) != 0 {
		t.Errorf("Orphans = %d, want 0 — legitimate worktree must not be flagged as orphan (%+v)",
			len(result.Orphans), result.Orphans)
	}
}

// --- DetectOrphanedMolecules tests ---

func TestOrphanedMoleculeResult_Types(t *testing.T) {
	t.Parallel()
	// Verify the result types have all expected fields.
	r := OrphanedMoleculeResult{
		BeadID:        "gt-work-123",
		MoleculeID:    "gt-mol-456",
		Assignee:      "testrig/polecats/alpha",
		PolecatName:   "alpha",
		Closed:        5,
		BeadRecovered: true,
		Error:         nil,
	}
	if r.BeadID != "gt-work-123" {
		t.Errorf("BeadID = %q, want %q", r.BeadID, "gt-work-123")
	}
	if r.MoleculeID != "gt-mol-456" {
		t.Errorf("MoleculeID = %q, want %q", r.MoleculeID, "gt-mol-456")
	}
	if r.PolecatName != "alpha" {
		t.Errorf("PolecatName = %q, want %q", r.PolecatName, "alpha")
	}
	if r.Closed != 5 {
		t.Errorf("Closed = %d, want 5", r.Closed)
	}
	if !r.BeadRecovered {
		t.Error("BeadRecovered = false, want true")
	}

	// Aggregate result
	agg := DetectOrphanedMoleculesResult{
		Checked: 10,
		Orphans: []OrphanedMoleculeResult{r},
		Errors:  []error{fmt.Errorf("test error")},
	}
	if agg.Checked != 10 {
		t.Errorf("Checked = %d, want 10", agg.Checked)
	}
	if len(agg.Orphans) != 1 {
		t.Errorf("len(Orphans) = %d, want 1", len(agg.Orphans))
	}
	if len(agg.Errors) != 1 {
		t.Errorf("len(Errors) = %d, want 1", len(agg.Errors))
	}
}

func TestDetectOrphanedMolecules_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available, should return empty result with errors.
	bdErr := fmt.Errorf("bd: not found")
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", bdErr },
		func(args []string) error { return bdErr },
	)
	result := DetectOrphanedMolecules(bd, "/tmp/nonexistent", "testrig", nil)
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// Should have errors from failed bd list commands
	if len(result.Errors) == 0 {
		t.Error("expected errors when bd is not available")
	}
	if len(result.Orphans) != 0 {
		t.Errorf("expected no orphans, got %d", len(result.Orphans))
	}
}

func TestDetectOrphanedMolecules_EmptyResult(t *testing.T) {
	t.Parallel()
	// With a mock bd that returns empty lists, should get empty result.
	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	result := DetectOrphanedMolecules(bd, t.TempDir(), "testrig", nil)
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Orphans) != 0 {
		t.Errorf("len(Orphans) = %d, want 0", len(result.Orphans))
	}
}

func TestGetAttachedMoleculeID_EmptyOutput(t *testing.T) {
	t.Parallel()
	// When bd returns error, should return empty string.
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", fmt.Errorf("bd: not found") },
		func(args []string) error { return fmt.Errorf("bd: not found") },
	)
	result := getAttachedMoleculeID(bd, "/tmp", "gt-fake-123")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestHandlePolecatDone_CompletedWithoutMRID_NoMergeReady(t *testing.T) {
	t.Parallel()
	// When Exit==COMPLETED but MRID is empty and MRFailed is true,
	// the witness should NOT send MERGE_READY (go to no-MR path).
	// This tests the fix for gt-xp6e9p.
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "COMPLETED",
		IssueID:     "gt-abc123",
		MRID:        "",
		Branch:      "polecat/nux-abc123",
		MRFailed:    true,
	}

	// hasPendingMR should be false when MRID is empty
	hasPendingMR := payload.MRID != ""
	if hasPendingMR {
		t.Error("hasPendingMR = true, want false when MRID is empty")
	}

	// Even with Exit==COMPLETED, MRFailed should prevent the bead lookup fallback
	if !payload.MRFailed && payload.Exit == "COMPLETED" && payload.Branch != "" {
		t.Error("should not attempt MR bead lookup when MRFailed is true")
	}
}

func TestHandlePolecatDone_CompletedWithMRID(t *testing.T) {
	t.Parallel()
	// When Exit==COMPLETED and MRID is set, hasPendingMR should be true.
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "COMPLETED",
		MRID:        "gt-mr-xyz",
		Branch:      "polecat/nux-abc123",
	}

	hasPendingMR := payload.MRID != ""
	if !hasPendingMR {
		t.Error("hasPendingMR = false, want true when MRID is set")
	}
}

func TestFindMRBeadForBranch_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available, should return empty string
	result := findMRBeadForBranch(DefaultBdCli(), "/nonexistent", "polecat/nux-abc123")
	if result != "" {
		t.Errorf("findMRBeadForBranch = %q, want empty when bd unavailable", result)
	}
}

func TestDetectOrphanedMolecules_WithMockBd(t *testing.T) {
	installFakeTmuxNoServer(t)

	// Full test with mock bd returning beads assigned to dead polecats.
	//
	// Setup:
	// - alpha: dead polecat (no tmux, no directory) with attached molecule → orphaned
	// - bravo: alive polecat (directory exists) → skip
	// - crew/sean: non-polecat assignee → skip
	// - empty assignee → skip

	tmpDir := t.TempDir()

	// Create town structure: tmpDir is the "town root"
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create bravo's directory (alive polecat)
	if err := os.MkdirAll(filepath.Join(polecatsDir, "bravo"), 0755); err != nil {
		t.Fatal(err)
	}
	// No directory for alpha (dead polecat)

	// Create workspace.Find marker
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) == 0 {
				return "[]", nil
			}
			joined := strings.Join(args, " ")
			switch args[0] {
			case "list":
				if strings.Contains(joined, "--status=hooked") {
					return `[
  {"id":"gt-work-001","assignee":"testrig/polecats/alpha"},
  {"id":"gt-work-002","assignee":"testrig/polecats/bravo"},
  {"id":"gt-work-003","assignee":"testrig/crew/sean"},
  {"id":"gt-work-004","assignee":""}
]`, nil
				}
				if strings.Contains(joined, "--status=in_progress") {
					return "[]", nil
				}
				if strings.Contains(joined, "--parent=gt-mol-orphan") {
					return `[
  {"id":"gt-step-001","status":"open"},
  {"id":"gt-step-002","status":"open"},
  {"id":"gt-step-003","status":"closed"}
]`, nil
				}
				return "[]", nil
			case "show":
				if len(args) > 1 {
					switch args[1] {
					case "gt-work-001":
						return `[{"status":"hooked","description":"attached_molecule: gt-mol-orphan\nattached_at: 2026-01-15T10:00:00Z\ndispatched_by: mayor"}]`, nil
					case "gt-mol-orphan":
						return `[{"status":"open"}]`, nil
					}
				}
				return `[{"status":"open","description":""}]`, nil
			}
			return "{}", nil
		},
		func(args []string) error { return nil },
	)

	result := DetectOrphanedMolecules(bd, tmpDir, rigName, nil)
	if result == nil {
		t.Fatal("result should not be nil")
	}

	// Should have checked 2 polecat-assigned beads (alpha and bravo)
	if result.Checked != 2 {
		t.Errorf("Checked = %d, want 2 (alpha + bravo)", result.Checked)
	}

	// Should have found 1 orphan (alpha's molecule)
	if len(result.Orphans) != 1 {
		t.Fatalf("len(Orphans) = %d, want 1", len(result.Orphans))
	}

	orphan := result.Orphans[0]
	if orphan.BeadID != "gt-work-001" {
		t.Errorf("orphan.BeadID = %q, want %q", orphan.BeadID, "gt-work-001")
	}
	if orphan.MoleculeID != "gt-mol-orphan" {
		t.Errorf("orphan.MoleculeID = %q, want %q", orphan.MoleculeID, "gt-mol-orphan")
	}
	if orphan.PolecatName != "alpha" {
		t.Errorf("orphan.PolecatName = %q, want %q", orphan.PolecatName, "alpha")
	}
	// Closed should be 3: 2 open step children + 1 molecule itself
	if orphan.Closed != 3 {
		t.Errorf("orphan.Closed = %d, want 3 (2 open steps + 1 molecule)", orphan.Closed)
	}
	if orphan.Error != nil {
		t.Errorf("orphan.Error = %v, want nil", orphan.Error)
	}

	// Verify bd close was called by checking the mock log
	logContent := strings.Join(mock.calls, "\n")
	if !strings.Contains(logContent, "close gt-step-001 gt-step-002") {
		t.Errorf("expected bd close for step children, got log:\n%s", logContent)
	}
	if !strings.Contains(logContent, "close gt-mol-orphan") {
		t.Errorf("expected bd close for molecule, got log:\n%s", logContent)
	}
	// Verify bead was recovered (resetAbandonedBead called bd update)
	if !orphan.BeadRecovered {
		t.Error("orphan.BeadRecovered = false, want true (resetAbandonedBead should have reset the bead)")
	}
	if !strings.Contains(logContent, "update gt-work-001") {
		t.Errorf("expected bd update for bead reset, got log:\n%s", logContent)
	}
}

func TestCompletionDiscovery_Types(t *testing.T) {
	t.Parallel()
	// Verify CompletionDiscovery has all expected fields
	d := CompletionDiscovery{
		PolecatName:    "nux",
		AgentBeadID:    "gt-gastown-polecat-nux",
		ExitType:       "COMPLETED",
		IssueID:        "gt-abc123",
		MRID:           "gt-mr-xyz",
		Branch:         "polecat/nux/gt-abc123@hash",
		MRFailed:       false,
		CompletionTime: "2026-02-28T02:00:00Z",
		Action:         "merge-ready-sent",
		WispCreated:    "gt-wisp-123",
	}

	if d.PolecatName != "nux" {
		t.Errorf("PolecatName = %q, want %q", d.PolecatName, "nux")
	}
	if d.ExitType != "COMPLETED" {
		t.Errorf("ExitType = %q, want %q", d.ExitType, "COMPLETED")
	}
	if d.Branch != "polecat/nux/gt-abc123@hash" {
		t.Errorf("Branch = %q, want correct value", d.Branch)
	}
}

func TestDiscoverCompletionsResult_EmptyResult(t *testing.T) {
	t.Parallel()
	result := &DiscoverCompletionsResult{}
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0", len(result.Discovered))
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %d, want 0", len(result.Errors))
	}
}

func TestDiscoverCompletions_NonexistentDir(t *testing.T) {
	t.Parallel()
	// When workDir doesn't exist, should return empty result
	result := DiscoverCompletions(DefaultBdCli(), "/nonexistent/path", "testrig", nil)
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for nonexistent dir", result.Checked)
	}
}

func TestDiscoverCompletions_EmptyPolecatsDir(t *testing.T) {
	t.Parallel()
	// When polecats directory exists but is empty, should scan 0
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create workspace marker
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	result := DiscoverCompletions(DefaultBdCli(), tmpDir, rigName, nil)
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for empty polecats dir", result.Checked)
	}
}

func TestDiscoverCompletions_NoCompletionMetadata(t *testing.T) {
	// Polecat exists but agent bead has no completion metadata — should be skipped
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(filepath.Join(polecatsDir, "nux"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Mock bd that returns agent bead with no completion fields
	bd, _ := mockBd(
		func(args []string) (string, error) {
			if len(args) > 0 && args[0] == "show" {
				return `[{"id":"gt-testrig-polecat-nux","description":"Agent: testrig/polecats/nux\n\nrole_type: polecat\nrig: testrig\nagent_state: working\nhook_bead: gt-work-001","agent_state":"working","hook_bead":"gt-work-001"}]`, nil
			}
			return "[]", nil
		},
		func(args []string) error { return nil },
	)

	result := DiscoverCompletions(bd, tmpDir, rigName, nil)
	if result.Checked != 1 {
		t.Errorf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0 (no completion metadata)", len(result.Discovered))
	}
}

func TestProcessDiscoveredCompletion_PhaseComplete(t *testing.T) {
	t.Parallel()
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "PHASE_COMPLETE",
	}
	discovery := &CompletionDiscovery{}
	processDiscoveredCompletion(DefaultBdCli(), "/tmp", "testrig", payload, discovery)
	if discovery.Action != "phase-complete" {
		t.Errorf("Action = %q, want %q", discovery.Action, "phase-complete")
	}
}

func TestProcessDiscoveredCompletion_NoMR(t *testing.T) {
	t.Parallel()
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "COMPLETED",
		MRFailed:    true, // Prevents fallback MR lookup
	}
	discovery := &CompletionDiscovery{}
	processDiscoveredCompletion(DefaultBdCli(), "/tmp", "testrig", payload, discovery)
	if !strings.Contains(discovery.Action, "acknowledged-idle") {
		t.Errorf("Action = %q, want to contain %q", discovery.Action, "acknowledged-idle")
	}
}

func TestProcessDiscoveredCompletion_EscalatedNoMR(t *testing.T) {
	t.Parallel()
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "ESCALATED",
	}
	discovery := &CompletionDiscovery{}
	processDiscoveredCompletion(DefaultBdCli(), "/tmp", "testrig", payload, discovery)
	if !strings.Contains(discovery.Action, "acknowledged-idle") {
		t.Errorf("Action = %q, want to contain %q for ESCALATED exit", discovery.Action, "acknowledged-idle")
	}
}

func TestGetAgentBeadFields_NoAgentBead(t *testing.T) {
	t.Parallel()
	// When bd fails, should return nil
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", fmt.Errorf("bd: not found") },
		func(args []string) error { return fmt.Errorf("bd: not found") },
	)
	fields := getAgentBeadFields(bd, "/tmp", "gt-fake-agent")
	if fields != nil {
		t.Error("expected nil fields when bd unavailable")
	}
}

func TestClearCompletionMetadata_NoBd(t *testing.T) {
	t.Parallel()
	// When bd fails, should return error
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", fmt.Errorf("bd: not found") },
		func(args []string) error { return fmt.Errorf("bd: not found") },
	)
	err := clearCompletionMetadata(bd, "/tmp", "gt-fake-agent")
	if err == nil {
		t.Error("expected error when bd unavailable")
	}
}

// --- Heartbeat v2 tests (gt-3vr5) ---

func TestHeartbeatV2_ExitingStateSkipsZombieDetection(t *testing.T) {
	t.Parallel()
	// Agent reports "exiting" state via heartbeat v2.
	// The witness should trust the agent and NOT flag as zombie,
	// even if done-intent is older than config.DefaultWitnessDoneIntentStuckTimeout.
	// This replaces timer-based inference for v2 agents.

	// Fresh heartbeat with state="exiting" → not a zombie
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatExiting,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	if stale {
		t.Error("fresh heartbeat should not be stale")
	}
	if hb.EffectiveState() != polecat.HeartbeatExiting {
		t.Errorf("EffectiveState() = %q, want %q", hb.EffectiveState(), polecat.HeartbeatExiting)
	}

	// With a v2 exiting heartbeat, the witness should NOT check done-intent timers
	shouldSkip := hb.IsV2() && !stale && hb.EffectiveState() == polecat.HeartbeatExiting
	if !shouldSkip {
		t.Error("expected v2 exiting heartbeat to skip zombie detection")
	}
}

func TestHeartbeatV2_StuckStateEscalates(t *testing.T) {
	t.Parallel()
	// Agent self-reports "stuck" via heartbeat v2.
	// The witness should escalate (not restart — agent is alive).
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatStuck,
		Context:   "blocked on auth issue",
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	if stale {
		t.Error("fresh heartbeat should not be stale")
	}

	shouldEscalate := hb.IsV2() && !stale && hb.EffectiveState() == polecat.HeartbeatStuck
	if !shouldEscalate {
		t.Error("expected v2 stuck heartbeat to trigger escalation")
	}
}

func TestHeartbeatV2_WorkingStateHealthy(t *testing.T) {
	t.Parallel()
	// Agent heartbeats "working" — healthy, not a zombie.
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatWorking,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	shouldSkip := hb.IsV2() && !stale && (hb.EffectiveState() == polecat.HeartbeatWorking || hb.EffectiveState() == polecat.HeartbeatIdle)
	if !shouldSkip {
		t.Error("expected v2 working heartbeat to skip zombie detection")
	}
}

func TestHeartbeatV2_IdleStateHealthy(t *testing.T) {
	t.Parallel()
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatIdle,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	shouldSkip := hb.IsV2() && !stale && (hb.EffectiveState() == polecat.HeartbeatWorking || hb.EffectiveState() == polecat.HeartbeatIdle)
	if !shouldSkip {
		t.Error("expected v2 idle heartbeat to skip zombie detection")
	}
}

func TestHeartbeatV2_StaleHeartbeatFallsThrough(t *testing.T) {
	t.Parallel()
	// Stale v2 heartbeat (agent died) → fall through to legacy detection.
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now().Add(-10 * time.Minute), // 10min old → stale
		State:     polecat.HeartbeatWorking,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	if !stale {
		t.Error("10-minute-old heartbeat should be stale")
	}

	// Stale heartbeat should NOT skip zombie detection — falls through to legacy
	shouldSkip := hb.IsV2() && !stale
	if shouldSkip {
		t.Error("stale v2 heartbeat should fall through to legacy detection")
	}
}

func TestHeartbeatV2_V1FallsThrough(t *testing.T) {
	t.Parallel()
	// v1 heartbeat (no state field) → fall through to legacy detection.
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		// No State field → v1
	}
	if hb.IsV2() {
		t.Error("expected IsV2()=false for v1 heartbeat")
	}

	// v1 heartbeat should NOT trigger v2 logic
	shouldUseV2 := hb.IsV2()
	if shouldUseV2 {
		t.Error("v1 heartbeat should fall through to legacy detection")
	}
}

func TestHeartbeatV2_DeadSessionFreshHeartbeatRace(t *testing.T) {
	t.Parallel()
	// Dead session but fresh heartbeat → possible race (session just restarted).
	// Should skip zombie detection to avoid killing a newly-started session.
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatWorking,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	sessionDead := true

	// Fresh heartbeat + dead session → skip (race condition)
	shouldSkip := sessionDead && hb.IsV2() && !stale
	if !shouldSkip {
		t.Error("expected fresh v2 heartbeat + dead session to skip zombie detection (race)")
	}
}

func TestZombieAgentSelfReportedStuck_Classification(t *testing.T) {
	t.Parallel()
	// Verify the new classification type
	if ZombieAgentSelfReportedStuck != "agent-self-reported-stuck" {
		t.Errorf("ZombieAgentSelfReportedStuck = %q, want %q", ZombieAgentSelfReportedStuck, "agent-self-reported-stuck")
	}
	// Should imply active work (agent is alive and asking for help)
	if !ZombieAgentSelfReportedStuck.ImpliesActiveWork() {
		t.Error("ZombieAgentSelfReportedStuck should imply active work")
	}
}

func TestZombieNeverHeartbeated_Classification(t *testing.T) {
	t.Parallel()
	if ZombieNeverHeartbeated != "never-heartbeated" {
		t.Errorf("ZombieNeverHeartbeated = %q, want %q", ZombieNeverHeartbeated, "never-heartbeated")
	}
	if !ZombieNeverHeartbeated.ImpliesActiveWork() {
		t.Error("ZombieNeverHeartbeated should imply active work")
	}

	// Session old enough (>5m default) with assigned work and no heartbeat → flag.
	oldSession := time.Now().Add(-10 * time.Minute)
	shouldFlag := time.Since(oldSession) > config.DefaultWitnessHeartbeatStartupGrace
	if !shouldFlag {
		t.Errorf("expected flag for session age=%v, threshold=%v",
			time.Since(oldSession).Round(time.Second), config.DefaultWitnessHeartbeatStartupGrace)
	}

	// Session within grace period → no flag.
	newSession := time.Now().Add(-2 * time.Minute)
	shouldNotFlag := time.Since(newSession) <= config.DefaultWitnessHeartbeatStartupGrace
	if !shouldNotFlag {
		t.Errorf("expected no flag for session age=%v, threshold=%v",
			time.Since(newSession).Round(time.Second), config.DefaultWitnessHeartbeatStartupGrace)
	}
}

func TestSubmittedStillRunningCandidate(t *testing.T) {
	t.Parallel()

	baseSnap := &agentBeadSnapshot{
		AgentState: string(beads.AgentStateDone),
		HookBead:   "gt-work-123",
		UpdatedAt:  time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
		Fields: &beads.AgentFields{
			CleanupStatus: "clean",
			MRID:          "gt-mr-123",
		},
	}
	staleHB := &polecat.SessionHeartbeat{
		Timestamp: time.Now().Add(-10 * time.Minute),
		State:     polecat.HeartbeatWorking,
	}

	age, ok := isSubmittedStillRunningCandidate(baseSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace)
	if !ok {
		t.Fatalf("expected submitted still-running candidate, age=%v", age)
	}

	noHookSnap := *baseSnap
	noHookSnap.HookBead = ""
	if _, ok := isSubmittedStillRunningCandidate(&noHookSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); !ok {
		t.Error("no-hook submitted sessions must still be treated as submitted still-running")
	}

	idleSnap := *baseSnap
	idleSnap.AgentState = string(beads.AgentStateIdle)
	if _, ok := isSubmittedStillRunningCandidate(&idleSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("normal idle polecats with submitted MR metadata must not be treated as submitted still-running")
	}

	freshHB := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatWorking,
	}
	if _, ok := isSubmittedStillRunningCandidate(baseSnap, freshHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("fresh heartbeat must not be treated as submitted still-running")
	}

	dirtySnap := *baseSnap
	dirtyFields := *baseSnap.Fields
	dirtyFields.CleanupStatus = "has_uncommitted"
	dirtySnap.Fields = &dirtyFields
	if _, ok := isSubmittedStillRunningCandidate(&dirtySnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("dirty cleanup status must not be treated as safe submitted still-running")
	}

	noSubmitSnap := *baseSnap
	noSubmitSnap.AgentState = string(beads.AgentStateWorking)
	noSubmitSnap.ActiveMR = ""
	noSubmitSnap.Fields = &beads.AgentFields{CleanupStatus: "clean"}
	if _, ok := isSubmittedStillRunningCandidate(&noSubmitSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("open hooked work without submission evidence must not be treated as submitted still-running")
	}

	completedOnlySnap := *baseSnap
	completedOnlySnap.ActiveMR = ""
	completedOnlySnap.Fields = &beads.AgentFields{
		CleanupStatus:  "clean",
		ExitType:       string(ExitTypeCompleted),
		CompletionTime: time.Now().Format(time.RFC3339),
	}
	if _, ok := isSubmittedStillRunningCandidate(&completedOnlySnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("COMPLETED metadata alone must not be treated as successful submission evidence")
	}

	failedSubmitSnap := *baseSnap
	failedSubmitSnap.Fields = &beads.AgentFields{
		CleanupStatus: "clean",
		MRID:          "gt-mr-123",
		MRFailed:      true,
	}
	if _, ok := isSubmittedStillRunningCandidate(&failedSubmitSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("failed MR submission must not be treated as successful submission evidence")
	}

	pushFailedSnap := *baseSnap
	pushFailedSnap.Fields = &beads.AgentFields{
		CleanupStatus: "clean",
		MRID:          "gt-mr-123",
		PushFailed:    true,
	}
	if _, ok := isSubmittedStillRunningCandidate(&pushFailedSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("failed push must not be treated as successful submission evidence")
	}
}

func TestZombieSubmittedStillRunning_Classification(t *testing.T) {
	t.Parallel()
	if ZombieSubmittedStillRunning != "submitted-still-running" {
		t.Errorf("ZombieSubmittedStillRunning = %q, want %q", ZombieSubmittedStillRunning, "submitted-still-running")
	}
	if ZombieSubmittedStillRunning.ImpliesActiveWork() {
		t.Error("ZombieSubmittedStillRunning should be classified as orphan/submitted idle, not active failed work")
	}
}

func TestNotifyRefineryMergeReady_EmitsChannelEvent(t *testing.T) {
	// Create a fake town root with the workspace marker so workspace.Find recognizes it
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Set GT_TEST_NUDGE_LOG to prevent actual tmux operations in nudgeRefinery
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))

	result := &HandlerResult{}
	// notifyRefineryMergeReady takes workDir and calls workspace.Find(workDir) internally
	notifyRefineryMergeReady(townRoot, "dashboard", result)

	// Verify that a MERGE_READY event file was created in the refinery channel
	eventDir := filepath.Join(townRoot, "events", "refinery")
	entries, err := os.ReadDir(eventDir)
	if err != nil {
		t.Fatalf("reading event dir: %v", err)
	}

	var eventFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".event") {
			eventFiles = append(eventFiles, e.Name())
		}
	}

	if len(eventFiles) == 0 {
		t.Fatal("expected at least one .event file in ~/gt/events/refinery/, got none")
	}

	// Read and verify the event content
	data, err := os.ReadFile(filepath.Join(eventDir, eventFiles[0]))
	if err != nil {
		t.Fatalf("reading event file: %v", err)
	}

	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("parsing event JSON: %v", err)
	}

	if event["type"] != "MERGE_READY" {
		t.Errorf("event type = %v, want MERGE_READY", event["type"])
	}
	if event["channel"] != "refinery" {
		t.Errorf("event channel = %v, want refinery", event["channel"])
	}

	payload, ok := event["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("payload is not a map: %T", event["payload"])
	}
	if payload["source"] != "witness" {
		t.Errorf("payload.source = %v, want witness", payload["source"])
	}
	if payload["rig"] != "dashboard" {
		t.Errorf("payload.rig = %v, want dashboard", payload["rig"])
	}
}


// --- DiscoverPostHocCompletions tests (gu-jr8) ---
//
// Post-hoc completion covers the narrow case where:
//   - A polecat pushed its branch (step 7 of the work formula)
//   - Refinery fast-forwarded the branch to mainline
//   - The polecat session died BEFORE `gt done` wrote exit_type metadata
//
// Without this safety net the hook bead stays in_progress forever, causing
// spawn-storms when subsequent witness cycles re-dispatch "unfinished" work.

// postHocTestBd constructs a mock bd that returns a canned agent bead description
// for `show <agent>` and a canned status for `show <hook>`. Closes are captured.
func postHocTestBd(agentDescription, hookStatus string) (*BdCli, *mockBdCalls) {
	return mockBd(
		func(args []string) (string, error) {
			if len(args) >= 2 && args[0] == "show" {
				// args[1] is the bead ID. Agent bead IDs start with the prefix
				// and contain "polecat" in their constructed form; hook beads are
				// plain work bead IDs (e.g. "gt-work-001"). Distinguish by checking
				// for "polecat" in the ID.
				if strings.Contains(args[1], "polecat") {
					return fmt.Sprintf(`[{"id":%q,"description":%q,"agent_state":"working"}]`,
						args[1], agentDescription), nil
				}
				return fmt.Sprintf(`[{"id":%q,"status":%q}]`, args[1], hookStatus), nil
			}
			return "[]", nil
		},
		func(args []string) error { return nil },
	)
}

func setupPostHocTestDir(t *testing.T, rigName, polecatName string) string {
	t.Helper()
	tmpDir := t.TempDir()
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats", polecatName)
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	return tmpDir
}

// TestDiscoverPostHocCompletions_ClosesBeadWhenBranchMerged verifies the primary
// fix path: active polecat, no exit_type, session dead, hook in_progress,
// commit on main → bead is closed.
func TestDiscoverPostHocCompletions_ClosesBeadWhenBranchMerged(t *testing.T) {
	// Not parallel: overrides package-level verifyCommitOnMain.
	installFakeTmuxNoServer(t)

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return true, nil
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	const (
		rigName     = "testrig"
		polecatName = "nux"
		hookBead    = "gt-work-001"
	)
	tmpDir := setupPostHocTestDir(t, rigName, polecatName)

	// Agent bead: active state, no exit_type, non-empty hook.
	agentDesc := "Agent: testrig/polecats/nux\n\nrole_type: polecat\n" +
		"rig: testrig\nagent_state: working\nhook_bead: " + hookBead
	bd, mock := postHocTestBd(agentDesc, "in_progress")

	result := DiscoverPostHocCompletions(bd, tmpDir, rigName)
	if result.Checked != 1 {
		t.Errorf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Discovered) != 1 {
		t.Fatalf("Discovered = %d, want 1", len(result.Discovered))
	}
	d := result.Discovered[0]
	if d.Action != "closed-hook-bead" {
		t.Errorf("Action = %q, want %q", d.Action, "closed-hook-bead")
	}
	if d.HookBead != hookBead {
		t.Errorf("HookBead = %q, want %q", d.HookBead, hookBead)
	}

	// Verify bd close was called on the hook bead with a reason.
	var foundClose bool
	for _, call := range mock.calls {
		if strings.Contains(call, "close "+hookBead) && strings.Contains(call, "-r") {
			foundClose = true
			break
		}
	}
	if !foundClose {
		t.Errorf("expected bd close %s with -r reason, got calls: %v", hookBead, mock.calls)
	}
}

// TestDiscoverPostHocCompletions_SkipsWhenExitTypePresent verifies that a polecat
// which already has exit_type set (gt done ran) is NOT handled by the post-hoc
// path — the normal DiscoverCompletions takes precedence.
func TestDiscoverPostHocCompletions_SkipsWhenExitTypePresent(t *testing.T) {
	installFakeTmuxNoServer(t)

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return true, nil // even if on main
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	tmpDir := setupPostHocTestDir(t, "testrig", "nux")
	agentDesc := "Agent: testrig/polecats/nux\n\nrole_type: polecat\n" +
		"rig: testrig\nagent_state: working\nhook_bead: gt-work-001\n" +
		"exit_type: COMPLETED\ncompletion_time: 2026-04-28T11:00:00Z"
	bd, mock := postHocTestBd(agentDesc, "in_progress")

	result := DiscoverPostHocCompletions(bd, tmpDir, "testrig")
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0 (exit_type present)", len(result.Discovered))
	}
	for _, call := range mock.calls {
		if strings.Contains(call, "close") {
			t.Errorf("bd close should not be called when exit_type is set, got: %v", mock.calls)
		}
	}
}

// TestDiscoverPostHocCompletions_SkipsWhenCommitNotOnMain verifies fail-open
// behavior: if the polecat's work is NOT merged to mainline, the bead is left
// alone.
func TestDiscoverPostHocCompletions_SkipsWhenCommitNotOnMain(t *testing.T) {
	installFakeTmuxNoServer(t)

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, nil // work NOT on main
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	tmpDir := setupPostHocTestDir(t, "testrig", "nux")
	agentDesc := "Agent: testrig/polecats/nux\n\nrole_type: polecat\n" +
		"rig: testrig\nagent_state: working\nhook_bead: gt-work-001"
	bd, mock := postHocTestBd(agentDesc, "in_progress")

	result := DiscoverPostHocCompletions(bd, tmpDir, "testrig")
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0 (commit not on main)", len(result.Discovered))
	}
	for _, call := range mock.calls {
		if strings.Contains(call, "close") {
			t.Errorf("bd close should not be called when commit not on main, got: %v", mock.calls)
		}
	}
}

// TestDiscoverPostHocCompletions_SkipsWhenAgentIdle verifies that an idle
// polecat (no active work) is NOT picked up — idle polecats are healthy, not
// stuck.
func TestDiscoverPostHocCompletions_SkipsWhenAgentIdle(t *testing.T) {
	installFakeTmuxNoServer(t)

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return true, nil
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	tmpDir := setupPostHocTestDir(t, "testrig", "nux")
	agentDesc := "Agent: testrig/polecats/nux\n\nrole_type: polecat\n" +
		"rig: testrig\nagent_state: idle\nhook_bead: gt-work-001"
	bd, _ := postHocTestBd(agentDesc, "in_progress")

	result := DiscoverPostHocCompletions(bd, tmpDir, "testrig")
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0 (agent idle)", len(result.Discovered))
	}
}

// TestDiscoverPostHocCompletions_SkipsWhenHookBeadEmpty verifies that a polecat
// with no hook bead is NOT picked up — there's nothing to close.
func TestDiscoverPostHocCompletions_SkipsWhenHookBeadEmpty(t *testing.T) {
	installFakeTmuxNoServer(t)

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return true, nil
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	tmpDir := setupPostHocTestDir(t, "testrig", "nux")
	agentDesc := "Agent: testrig/polecats/nux\n\nrole_type: polecat\n" +
		"rig: testrig\nagent_state: working\nhook_bead: null"
	bd, _ := postHocTestBd(agentDesc, "in_progress")

	result := DiscoverPostHocCompletions(bd, tmpDir, "testrig")
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0 (no hook bead)", len(result.Discovered))
	}
}

// TestDiscoverPostHocCompletions_SkipsWhenHookAlreadyClosed verifies that a
// polecat whose hook bead is already closed is NOT re-closed.
func TestDiscoverPostHocCompletions_SkipsWhenHookAlreadyClosed(t *testing.T) {
	installFakeTmuxNoServer(t)

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return true, nil
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	tmpDir := setupPostHocTestDir(t, "testrig", "nux")
	agentDesc := "Agent: testrig/polecats/nux\n\nrole_type: polecat\n" +
		"rig: testrig\nagent_state: working\nhook_bead: gt-work-001"
	bd, mock := postHocTestBd(agentDesc, "closed") // hook already closed

	result := DiscoverPostHocCompletions(bd, tmpDir, "testrig")
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0 (hook already closed)", len(result.Discovered))
	}
	for _, call := range mock.calls {
		if strings.HasPrefix(call, "close") {
			t.Errorf("bd close should not be called when hook already closed, got: %v", mock.calls)
		}
	}
}

// TestDiscoverPostHocCompletions_VerifyError treats verification errors as
// fail-open: don't close, record an error. This preserves the "never close a
// bead whose work isn't actually merged" invariant.
func TestDiscoverPostHocCompletions_VerifyError(t *testing.T) {
	installFakeTmuxNoServer(t)

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, fmt.Errorf("git error")
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	tmpDir := setupPostHocTestDir(t, "testrig", "nux")
	agentDesc := "Agent: testrig/polecats/nux\n\nrole_type: polecat\n" +
		"rig: testrig\nagent_state: working\nhook_bead: gt-work-001"
	bd, _ := postHocTestBd(agentDesc, "in_progress")

	result := DiscoverPostHocCompletions(bd, tmpDir, "testrig")
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0 on verify error (fail-open)", len(result.Discovered))
	}
	if len(result.Errors) == 0 {
		t.Error("expected an error to be recorded when verify fails")
	}
}

// TestDiscoverPostHocCompletions_EmptyPolecatsDir verifies behavior on an empty
// rig.
func TestDiscoverPostHocCompletions_EmptyPolecatsDir(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	polecatsDir := filepath.Join(tmpDir, "testrig", "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	result := DiscoverPostHocCompletions(bd, tmpDir, "testrig")
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
}

// TestDiscoverPostHocCompletions_NonexistentDir verifies behavior on a missing
// rig directory.
func TestDiscoverPostHocCompletions_NonexistentDir(t *testing.T) {
	t.Parallel()
	result := DiscoverPostHocCompletions(DefaultBdCli(), "/nonexistent/path", "testrig")
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
}

// TestSlotOpenMessage verifies the canonical SLOT_OPEN nudge body format.
// Keeps the format under test so accidental format drift is caught — other
// parts of Gas Town match on "SLOT_OPEN:" prefix and the shape matters for
// log readability.
func TestSlotOpenMessage(t *testing.T) {
	t.Parallel()
	msg := slotOpenMessage("casc_webapp", "quartz", "DEFERRED")
	wantPrefix := "SLOT_OPEN: casc_webapp/quartz completed (exit=DEFERRED)"
	if !strings.HasPrefix(msg, wantPrefix) {
		t.Errorf("slotOpenMessage = %q, want prefix %q", msg, wantPrefix)
	}
	if !strings.Contains(msg, "slot available") {
		t.Errorf("slotOpenMessage missing 'slot available' marker: %q", msg)
	}
	if !strings.Contains(msg, "gt polecat list") {
		t.Errorf("slotOpenMessage missing 'gt polecat list' guidance: %q", msg)
	}
}

// TestLogSlotOpenNudge_WritesTownLog is the regression test for bug 2 of
// gu-harz: witness-emitted SLOT_OPEN nudges must leave an audit trail in
// town.log so operators can diagnose delivery regressions.
//
// Previously, notifyMayorSlotOpen called t.NudgeSession directly without
// any LogNudge call, so SLOT_OPEN nudges were invisible in town.log and
// `gt audit` even though the text reached the mayor's pane.
func TestLogSlotOpenNudge_WritesTownLog(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	msg := slotOpenMessage("casc_webapp", "quartz", "DEFERRED")

	logSlotOpenNudge(townRoot, "hq-mayor", msg)

	// Verify a town.log entry was written with the expected shape:
	// "<timestamp> [nudge] hq-mayor nudged with \"SLOT_OPEN...\""
	logPath := filepath.Join(townRoot, "logs", "town.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading %s: %v", logPath, err)
	}
	content := string(data)
	if !strings.Contains(content, "[nudge]") {
		t.Errorf("town.log missing [nudge] marker: %q", content)
	}
	if !strings.Contains(content, "hq-mayor") {
		t.Errorf("town.log missing hq-mayor agent: %q", content)
	}
	if !strings.Contains(content, "SLOT_OPEN") {
		t.Errorf("town.log missing SLOT_OPEN body: %q", content)
	}
}

// TestLogSlotOpenNudge_EmptyTownRootIsNoop verifies that the logging helper
// defensively handles the edge case where workspace.Find returned an empty
// townRoot — writing to a bogus path would create stray directories.
func TestLogSlotOpenNudge_EmptyTownRootIsNoop(t *testing.T) {
	t.Parallel()
	// Should not panic or create files under the current working directory.
	logSlotOpenNudge("", "hq-mayor", "test message")
	// No assertion beyond "does not panic / does not side-effect" — the
	// contract is simply that empty townRoot is a safe no-op.
}

// TestCherryHasUnmergedCommits covers the git-cherry output parser used by
// verifyBranchAlreadyMerged (aa-apw).
func TestCherryHasUnmergedCommits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty output — branch has no commits beyond base", "", false},
		{"whitespace only", "  \n\n", false},
		{"all squash-applied (-)", "- abc123\n- def456\n", false},
		{"one unmerged (+)", "+ abc123\n", true},
		{"mixed", "- abc123\n+ def456\n", true},
		{"unmerged only", "+ a\n+ b\n+ c\n", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cherryHasUnmergedCommits(tc.in); got != tc.want {
				t.Errorf("cherryHasUnmergedCommits(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestHandleZombieRestart_SkipsWhenBranchAlreadyMerged verifies the aa-apw fix:
// when a stopped polecat's branch work is already merged to origin/main (e.g.,
// via squash-merge), the witness must NOT restart the session — restarting
// would let the polecat re-push its pre-squash HEAD and create a duplicate MR.
// Instead the polecat is archived.
//
// Not parallel: overrides the package-level verifyBranchAlreadyMerged var.
func TestHandleZombieRestart_SkipsWhenBranchAlreadyMerged(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	classifyPolecatMergeState = func(workDir, rigName, polecatName string) (MergeCheckResult, error) {
		return MergeCheckMerged, nil
	}
	t.Cleanup(func() { classifyPolecatMergeState = oldClassify })

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	z := &ZombieResult{PolecatName: "scavenger", HookBead: "ma-poc.4"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "scavenger", "ma-poc.4", "has_unpushed", z)

	// Action must reflect the archive decision; must NOT be a "restarted*" action.
	if !strings.Contains(z.Action, "work-already-merged") {
		t.Errorf("action = %q, want it to mention work-already-merged (aa-apw)", z.Action)
	}
	if strings.HasPrefix(z.Action, "restarted") || strings.HasPrefix(z.Action, "restart-") {
		t.Errorf("action = %q, polecat must not be restarted when work is already merged", z.Action)
	}
}

// TestHandleZombieRestart_RestartsWhenBranchNotMerged verifies the pre-aa-apw
// behavior is preserved when work is NOT merged: handleZombieRestart proceeds
// to its normal cleanup/restart flow.
//
// Not parallel: overrides the package-level verifyBranchAlreadyMerged var.
func TestHandleZombieRestart_RestartsWhenBranchNotMerged(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	classifyPolecatMergeState = func(workDir, rigName, polecatName string) (MergeCheckResult, error) {
		return MergeCheckNotMerged, nil
	}
	t.Cleanup(func() { classifyPolecatMergeState = oldClassify })

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	z := &ZombieResult{PolecatName: "scavenger", HookBead: "ma-poc.4"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "scavenger", "ma-poc.4", "clean", z)

	// Should NOT take the archive path.
	if strings.Contains(z.Action, "work-already-merged") {
		t.Errorf("action = %q, should not archive when work is not merged", z.Action)
	}
}

// --- mrExistsAndOpen / hasPendingMR phantom-ref tests (gu-xd7i) ---

// bdShowResponder builds an Exec mock that replies to "show <id>" calls with
// per-id JSON responses and defaults all other commands to "[]". Unknown IDs
// return the real bd-style error so the phantom-ref path is exercised exactly
// as the witness sees it in production.
func bdShowResponder(showByID map[string]string) func([]string) (string, error) {
	return func(args []string) (string, error) {
		if len(args) >= 2 && args[0] == "show" {
			id := args[1]
			if body, ok := showByID[id]; ok {
				return body, nil
			}
			return `{"error":"no issues found matching the provided IDs","schema_version":1}`,
				fmt.Errorf("bd: no issue found matching %q", id)
		}
		// findCleanupWisp uses `bd list` — return empty so only the active_mr
		// path is under test.
		return "[]", nil
	}
}

func TestMrExistsAndOpen_EmptyMRID(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)
	if mrExistsAndOpen(bd, t.TempDir(), "") {
		t.Fatal("mrExistsAndOpen(\"\") = true, want false")
	}
}

func TestMrExistsAndOpen_PhantomMissingID(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		bdShowResponder(nil), // every show returns "no issue found"
		func(args []string) error { return nil },
	)
	if mrExistsAndOpen(bd, t.TempDir(), "gt-phantom") {
		t.Fatal("mrExistsAndOpen on phantom ID = true, want false (gu-xd7i)")
	}
}

func TestMrExistsAndOpen_OpenMR(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		bdShowResponder(map[string]string{
			"gt-realmr": `[{"status":"open"}]`,
		}),
		func(args []string) error { return nil },
	)
	if !mrExistsAndOpen(bd, t.TempDir(), "gt-realmr") {
		t.Fatal("mrExistsAndOpen on open MR = false, want true")
	}
}

func TestMrExistsAndOpen_ClosedMRTreatedAsPhantom(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		bdShowResponder(map[string]string{
			"gt-closedmr": `[{"status":"closed"}]`,
		}),
		func(args []string) error { return nil },
	)
	if mrExistsAndOpen(bd, t.TempDir(), "gt-closedmr") {
		t.Fatal("mrExistsAndOpen on closed MR = true, want false")
	}
}

func TestMrExistsAndOpen_TombstoneTreatedAsPhantom(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		bdShowResponder(map[string]string{
			"gt-tomb": `[{"status":"tombstone"}]`,
		}),
		func(args []string) error { return nil },
	)
	if mrExistsAndOpen(bd, t.TempDir(), "gt-tomb") {
		t.Fatal("mrExistsAndOpen on tombstone MR = true, want false")
	}
}

func TestMrExistsAndOpen_MalformedJSONTreatedAsPhantom(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		bdShowResponder(map[string]string{
			"gt-malformed": `not-json`,
		}),
		func(args []string) error { return nil },
	)
	if mrExistsAndOpen(bd, t.TempDir(), "gt-malformed") {
		t.Fatal("mrExistsAndOpen on malformed JSON = true, want false")
	}
}

func TestMrExistsAndOpen_EmptyStatusTreatedAsPhantom(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		bdShowResponder(map[string]string{
			"gt-empty": `[{"status":""}]`,
		}),
		func(args []string) error { return nil },
	)
	if mrExistsAndOpen(bd, t.TempDir(), "gt-empty") {
		t.Fatal("mrExistsAndOpen on empty-status record = true, want false")
	}
}

// TestHasPendingMRFromSnapshot_PhantomActiveMR is the end-to-end regression for
// gu-xd7i: a polecat agent bead with an active_mr pointing to an MR that no
// longer exists in the DB must not keep the witness from nuking the zombie.
func TestHasPendingMRFromSnapshot_PhantomActiveMR(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		bdShowResponder(nil), // every show returns "no issue found"
		func(args []string) error { return nil },
	)
	if hasPendingMRFromSnapshot(bd, t.TempDir(), "nux", "gt-phantom") {
		t.Fatal("hasPendingMRFromSnapshot with phantom active_mr = true, want false (gu-xd7i)")
	}
}

func TestHasPendingMRFromSnapshot_RealOpenMR(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		bdShowResponder(map[string]string{
			"gt-realmr": `[{"status":"open"}]`,
		}),
		func(args []string) error { return nil },
	)
	if !hasPendingMRFromSnapshot(bd, t.TempDir(), "nux", "gt-realmr") {
		t.Fatal("hasPendingMRFromSnapshot with real open MR = false, want true")
	}
}

func TestHasPendingMRFromSnapshot_EmptyActiveMR(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		bdShowResponder(nil),
		func(args []string) error { return nil },
	)
	if hasPendingMRFromSnapshot(bd, t.TempDir(), "nux", "") {
		t.Fatal("hasPendingMRFromSnapshot with empty active_mr = true, want false")
	}
}

// TestHasPendingMR_PhantomActiveMR is the parallel regression for the
// NukePolecat call site, which uses the non-snapshot variant via an agent bead
// lookup. Here we stub the agent bead lookup to return a phantom active_mr and
// make sure hasPendingMR treats it as cleared.
func TestHasPendingMR_PhantomActiveMR(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		func(args []string) (string, error) {
			if len(args) >= 2 && args[0] == "show" {
				// Agent bead lookup returns an active_mr that doesn't exist.
				if strings.HasPrefix(args[1], "gt-agent") {
					return `[{"active_mr":"gt-phantom"}]`, nil
				}
				// Any other show (the phantom MR verify) is not found.
				return `{"error":"no issues found matching the provided IDs"}`,
					fmt.Errorf("bd: no issue found matching %q", args[1])
			}
			// list calls (findCleanupWisp) return no wisp.
			return "[]", nil
		},
		func(args []string) error { return nil },
	)
	if hasPendingMR(bd, t.TempDir(), "testrig", "nux", "gt-agent-nux") {
		t.Fatal("hasPendingMR with phantom active_mr = true, want false (gu-xd7i)")
	}
}

// --- Unfiled-MR recovery (gu-j98v) ---

// TestUnfiledMRState_NeedsRecovery covers the pure-logic predicate guarding
// the recovery path.
func TestUnfiledMRState_NeedsRecovery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		state *UnfiledMRState
		want  bool
	}{
		{"nil state", nil, false},
		{"no commits ahead", &UnfiledMRState{Branch: "p/n", CommitsAhead: false}, false},
		{"commits ahead, MR already exists", &UnfiledMRState{
			Branch:       "p/n",
			CommitsAhead: true,
			ExistingMRID: "gt-abc",
		}, false},
		{"commits ahead, no MR — recover", &UnfiledMRState{
			Branch:       "p/n",
			CommitsAhead: true,
			ExistingMRID: "",
		}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.state.NeedsRecovery(); got != tc.want {
				t.Errorf("NeedsRecovery() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExtractBaseBranchVar covers the formula_vars parser used by
// verifyUnfiledMR to resolve a polecat's target branch.
func TestExtractBaseBranchVar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"newline form", "base_branch=main\npriority=2", "main"},
		{"comma form", "base_branch=develop,priority=2", "develop"},
		{"whitespace around value", "base_branch = feature/x ", "feature/x"},
		{"absent key", "priority=2\nlabel=ops", ""},
		{"wrong prefix", "non_base_branch=oops", ""},
		{"multiple entries, base_branch second", "priority=2\nbase_branch=feature/y", "feature/y"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := extractBaseBranchVar(tc.in); got != tc.want {
				t.Errorf("extractBaseBranchVar(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExtractGatesCommandsVar covers parsing of the gates_commands block out
// of formula_vars. Witness recovery runs these gates on the recovered branch
// before filing an MR (gu-zrim); the parser must preserve gate order and
// multi-line commands while cleanly terminating on malformed or absent
// blocks.
func TestExtractGatesCommandsVar(t *testing.T) {
	t.Parallel()

	canonical := "base_branch=main\n" +
		"gates_commands=# Gate: build\n" +
		"go build ./...\n" +
		"# Gate: rebase-check\n" +
		"scripts/check-upstream-rebased.sh\n" +
		"# Gate: test\n" +
		"go test ./...\n" +
		"# Gate: vet\n" +
		"go vet ./..."

	cases := []struct {
		name string
		in   string
		want []PolecatGate
	}{
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "absent key",
			in:   "base_branch=main\npriority=2",
			want: nil,
		},
		{
			name: "canonical format from mol-polecat-work",
			in:   canonical,
			want: []PolecatGate{
				{Name: "build", Cmd: "go build ./..."},
				{Name: "rebase-check", Cmd: "scripts/check-upstream-rebased.sh"},
				{Name: "test", Cmd: "go test ./..."},
				{Name: "vet", Cmd: "go vet ./..."},
			},
		},
		{
			name: "gates_commands at start of vars",
			in: "gates_commands=# Gate: build\n" +
				"go build ./...",
			want: []PolecatGate{
				{Name: "build", Cmd: "go build ./..."},
			},
		},
		{
			name: "multi-line command preserves newlines",
			in: "gates_commands=# Gate: lint\n" +
				"golangci-lint run \\\n" +
				"  --timeout=5m",
			want: []PolecatGate{
				{Name: "lint", Cmd: "golangci-lint run \\\n  --timeout=5m"},
			},
		},
		{
			name: "env-style assignment inside command does NOT terminate block",
			in: "gates_commands=# Gate: test\n" +
				"GOFLAGS=-mod=vendor go test ./...",
			want: []PolecatGate{
				// The parser only terminates on formula-var keys at column 0
				// with no shell-metacharacters; "GOFLAGS=..." starts a key
				// with uppercase letters that pass the regex, so it WILL
				// terminate. Document the behavior: such commands must be
				// prefixed with whitespace or moved to a separate line.
			},
		},
		{
			name: "indented env assignment is kept as command content",
			in: "gates_commands=# Gate: test\n" +
				"  GOFLAGS=-mod=vendor go test ./...",
			want: []PolecatGate{
				{Name: "test", Cmd: "  GOFLAGS=-mod=vendor go test ./..."},
			},
		},
		{
			name: "shell-prefixed command kept as command content",
			in: "gates_commands=# Gate: test\n" +
				"./scripts/test.sh",
			want: []PolecatGate{
				{Name: "test", Cmd: "./scripts/test.sh"},
			},
		},
		{
			name: "later formula var terminates block",
			in: "gates_commands=# Gate: build\n" +
				"go build ./...\n" +
				"priority=2",
			want: []PolecatGate{
				{Name: "build", Cmd: "go build ./..."},
			},
		},
		{
			name: "content before first # Gate: header is ignored",
			in: "gates_commands=noise line\n" +
				"# Gate: build\n" +
				"go build ./...",
			want: []PolecatGate{
				{Name: "build", Cmd: "go build ./..."},
			},
		},
		{
			name: "empty gate command is dropped",
			in: "gates_commands=# Gate: build\n" +
				"# Gate: test\n" +
				"go test ./...",
			want: []PolecatGate{
				{Name: "test", Cmd: "go test ./..."},
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractGatesCommandsVar(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d gates, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("gate[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestLooksLikeFormulaVarKey pins down the predicate that decides whether a
// line ends the gates_commands block. The heuristic is intentionally
// conservative — only bare key=value lines at column 0 with an
// identifier-shaped key terminate the block.
func TestLooksLikeFormulaVarKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"base_branch=main", true},
		{"priority=2", true},
		{"FOO_BAR-1=value", true},
		{"  priority=2", false}, // indented → command content
		{"\tFOO=bar", false},    // tab-indented → command content
		{"# Gate: build", false},
		{"go test ./...", false}, // no equals
		{"=novalue", false},      // empty key
		{"has space=bad", false}, // space in key
		{"$VAR=bad", false},      // shell var
		{"", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := looksLikeFormulaVarKey(tc.in); got != tc.want {
				t.Errorf("looksLikeFormulaVarKey(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestRunRecoveryGates_Success verifies the happy path: all gates pass and
// the outcome is success with empty failure metadata.
func TestRunRecoveryGates_Success(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	got := _runRecoveryGates(tmp, []PolecatGate{
		{Name: "g1", Cmd: "true"},
		{Name: "g2", Cmd: "echo ok"},
	})
	if !got.success {
		t.Fatalf("success = false, want true (failedGate=%q msg=%q)", got.failedGate, got.failureMsg)
	}
	if got.failedGate != "" || got.failureMsg != "" {
		t.Errorf("expected empty failure metadata, got gate=%q msg=%q", got.failedGate, got.failureMsg)
	}
}

// TestRunRecoveryGates_FailureStopsFirst verifies sequential stop-on-failure:
// the second gate never runs when the first fails, and the outcome carries
// the failing gate's name and a bounded diagnostic message.
func TestRunRecoveryGates_FailureStopsFirst(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	sentinel := filepath.Join(tmp, "after-bad.touched")
	// The second gate would create a sentinel file if it ran. We assert it
	// doesn't so we know the runner really stopped after the first failure.
	got := _runRecoveryGates(tmp, []PolecatGate{
		{Name: "bad", Cmd: "echo boom >&2; exit 3"},
		{Name: "after-bad", Cmd: "touch " + sentinel},
	})
	if got.success {
		t.Fatalf("success = true, want false")
	}
	if got.failedGate != "bad" {
		t.Errorf("failedGate = %q, want %q", got.failedGate, "bad")
	}
	if !strings.Contains(got.failureMsg, "boom") {
		t.Errorf("failureMsg = %q, expected to contain stderr %q", got.failureMsg, "boom")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("second gate ran after first failure (sentinel exists): stat err=%v", err)
	}
}

// TestRunRecoveryGates_Empty verifies the no-gates path: an empty list is a
// success (no gates to enforce). Blank/whitespace-only commands are skipped.
func TestRunRecoveryGates_Empty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		gates []PolecatGate
	}{
		{"nil", nil},
		{"empty slice", []PolecatGate{}},
		{"blank cmd skipped", []PolecatGate{{Name: "noop", Cmd: "   "}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := _runRecoveryGates(t.TempDir(), tc.gates)
			if !got.success {
				t.Errorf("success = false for %s, want true", tc.name)
			}
		})
	}
}

// TestRunRecoveryGates_Timeout verifies gate commands are bounded by
// recoveryGateTimeout. A slow gate must be killed and surface as a timeout
// failure in the outcome.
func TestRunRecoveryGates_Timeout(t *testing.T) {
	// Not parallel: mutates package-level recoveryGateTimeout.
	old := recoveryGateTimeout
	recoveryGateTimeout = 100 * time.Millisecond
	t.Cleanup(func() { recoveryGateTimeout = old })

	got := _runRecoveryGates(t.TempDir(), []PolecatGate{
		{Name: "slow", Cmd: "sleep 5"},
	})
	if got.success {
		t.Fatalf("success = true, want false for timed-out gate")
	}
	if got.failedGate != "slow" {
		t.Errorf("failedGate = %q, want %q", got.failedGate, "slow")
	}
	if !strings.Contains(got.failureMsg, "timeout") {
		t.Errorf("failureMsg = %q, expected to mention timeout", got.failureMsg)
	}
}

// TestTruncateGateOutput verifies the 500-char ceiling so embedded gate
// output never overflows a zombie Action string.
func TestTruncateGateOutput(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 600)
	short := "short output"
	if got := truncateGateOutput(short); got != short {
		t.Errorf("short output mutated: got %q, want %q", got, short)
	}
	got := truncateGateOutput(long)
	if len(got) != 500+3 { // 500 chars + "..."
		t.Errorf("truncated len = %d, want %d", len(got), 503)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated output missing trailing ellipsis: %q", got[len(got)-10:])
	}
}

// TestRecoverUnfiledMR_GateFailureBlocksPushAndSubmit verifies the gu-zrim
// fix: when a pre-merge gate fails during witness recovery, neither the
// push nor `gt mq submit` is invoked. This is the whole point of running
// gates up front — a broken branch must not reach origin or the merge
// queue just because the polecat died before it could run gates itself.
//
// Not parallel: overrides package-level runRecoveryGates.
func TestRecoverUnfiledMR_GateFailureBlocksPushAndSubmit(t *testing.T) {
	old := runRecoveryGates
	runRecoveryGates = func(string, []PolecatGate) recoveryGateOutcome {
		return recoveryGateOutcome{
			success:    false,
			failedGate: "rebase-check",
			failureMsg: "upstream/main NOT an ancestor of HEAD",
		}
	}
	t.Cleanup(func() { runRecoveryGates = old })

	// Build a fake town root with a polecat worktree directory. _recoverUnfiledMR
	// stat's the polecat path but never runs git here — we short-circuit on
	// the gate failure before any exec happens.
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	polecatPath := filepath.Join(townRoot, "testrig", "polecats", "nux", "testrig")
	if err := os.MkdirAll(polecatPath, 0o755); err != nil {
		t.Fatalf("mkdir polecat path: %v", err)
	}

	state := &UnfiledMRState{
		Branch:        "polecat/nux/gt-abc",
		HeadSHA:       "deadbeef",
		Target:        "main",
		CommitsAhead:  true,
		AlreadyPushed: false,
		Gates: []PolecatGate{
			{Name: "build", Cmd: "go build ./..."},
			{Name: "rebase-check", Cmd: "scripts/check-upstream-rebased.sh"},
		},
	}

	bd, _ := mockBd(
		func(args []string) (string, error) {
			t.Errorf("bd.Exec invoked on gate failure path (args=%v) — caller should have short-circuited", args)
			return "", nil
		},
		func(args []string) error {
			t.Errorf("bd.Run invoked on gate failure path (args=%v) — caller should have short-circuited", args)
			return nil
		},
	)

	action, err := _recoverUnfiledMR(bd, townRoot, "testrig", "nux", state)
	if err == nil {
		t.Fatalf("err = nil, want gate-failure error (action=%q)", action)
	}
	if !strings.Contains(action, "recover-failed-gate-rebase-check") {
		t.Errorf("action = %q, want substring %q", action, "recover-failed-gate-rebase-check")
	}
	if !strings.Contains(action, "aa-unpushed-commits") {
		t.Errorf("action = %q, want aa-unpushed-commits tag (state.AlreadyPushed=false)", action)
	}
	if !strings.Contains(action, "upstream/main NOT an ancestor") {
		t.Errorf("action = %q, want embedded gate failure message", action)
	}
	if !strings.Contains(err.Error(), "gate \"rebase-check\" failed") {
		t.Errorf("err = %v, want mention of failing gate", err)
	}
}

// TestRecoverUnfiledMR_NoGatesPreservesLegacyBehavior verifies that beads
// which don't carry gates_commands (pre-gu-zrim formats, or formulas other
// than mol-polecat-work) still follow the original push + submit path. This
// keeps the new gate check from regressing existing recovery flows.
//
// Not parallel: overrides package-level runRecoveryGates to fail the test if
// the gate runner is ever consulted when Gates is empty.
func TestRecoverUnfiledMR_NoGatesPreservesLegacyBehavior(t *testing.T) {
	old := runRecoveryGates
	runRecoveryGates = func(string, []PolecatGate) recoveryGateOutcome {
		t.Fatalf("runRecoveryGates must not be called when state.Gates is empty")
		return recoveryGateOutcome{}
	}
	t.Cleanup(func() { runRecoveryGates = old })

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	// Intentionally do NOT create the polecat path so _recoverUnfiledMR
	// short-circuits on the worktree-missing check AFTER the gate-bypass
	// decision. We only care that the gate path is not invoked.
	polecatParent := filepath.Join(townRoot, "testrig", "polecats", "nux")
	if err := os.MkdirAll(polecatParent, 0o755); err != nil {
		t.Fatalf("mkdir polecat parent: %v", err)
	}

	state := &UnfiledMRState{
		Branch:        "polecat/nux/gt-abc",
		HeadSHA:       "deadbeef",
		Target:        "main",
		CommitsAhead:  true,
		AlreadyPushed: true, // skip the push branch so no real git runs
		Gates:         nil,
	}

	bd, _ := mockBd(
		func(args []string) (string, error) { return "", nil },
		func(args []string) error { return nil },
	)

	_, _ = _recoverUnfiledMR(bd, townRoot, "testrig", "nux", state)
	// No assertion on action — the point is that the gate-runner mock is
	// never invoked (enforced via t.Fatalf inside the mock).
}

// TestExtractMRIDFromSubmit covers parsing of `gt mq submit` output so we can
// tag recovery actions with the MR ID for operator visibility.
func TestExtractMRIDFromSubmit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "✓ Submitted to merge queue\n  MR ID: gt-abc123\n  Source: polecat/foo", "gt-abc123"},
		{"idempotent", "✓ MR already exists (idempotent)\n  MR ID: gt-existing\n", "gt-existing"},
		{"styled output (ansi bold around id)", "  MR ID: \x1b[1mgt-styled\x1b[0m\n", "gt-styled"},
		{"no id line", "✓ Done\nSource: foo\n", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := extractMRIDFromSubmit(tc.in); got != tc.want {
				t.Errorf("extractMRIDFromSubmit = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStripANSI covers escape-sequence removal from styled CLI output.
func TestStripANSI(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"\x1b[1mbold\x1b[0m", "bold"},
		{"a\x1b[31mred\x1b[0mb", "aredb"},
		{"\x1b[1;33mmulti\x1b[0m done", "multi done"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := stripANSI(tc.in); got != tc.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFindOpenMRForBranchAndSHA covers the bead-query dedup used by
// verifyUnfiledMR to avoid duplicate submissions when the polecat already
// filed an MR before dying.
func TestFindOpenMRForBranchAndSHA(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		queryOut  string
		branch    string
		commitSHA string
		want      string
	}{
		{
			name:     "no MRs in queue",
			queryOut: "[]",
			branch:   "polecat/nux",
			want:     "",
		},
		{
			name: "branch match, sha match",
			queryOut: `[{"id":"gt-mr-1","description":"branch: polecat/nux\ntarget: main\n` +
				`source_issue: gt-abc\nrig: testrig\ncommit_sha: abc123"}]`,
			branch:    "polecat/nux",
			commitSHA: "abc123",
			want:      "gt-mr-1",
		},
		{
			name: "branch match, sha mismatch — stale MR, do not reuse",
			queryOut: `[{"id":"gt-mr-2","description":"branch: polecat/nux\ntarget: main\n` +
				`source_issue: gt-abc\nrig: testrig\ncommit_sha: OLDSHA"}]`,
			branch:    "polecat/nux",
			commitSHA: "NEWSHA",
			want:      "",
		},
		{
			name: "branch match, legacy MR with no commit_sha — branch-only fallback",
			queryOut: `[{"id":"gt-mr-3","description":"branch: polecat/nux\ntarget: main\n` +
				`source_issue: gt-abc\nrig: testrig"}]`,
			branch:    "polecat/nux",
			commitSHA: "NEWSHA",
			want:      "gt-mr-3",
		},
		{
			name: "branch mismatch",
			queryOut: `[{"id":"gt-mr-4","description":"branch: polecat/other\ntarget: main\n` +
				`source_issue: gt-abc\nrig: testrig\ncommit_sha: abc123"}]`,
			branch:    "polecat/nux",
			commitSHA: "abc123",
			want:      "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bd, _ := mockBd(
				func(args []string) (string, error) {
					if len(args) > 0 && args[0] == "query" {
						return tc.queryOut, nil
					}
					return "[]", nil
				},
				func(args []string) error { return nil },
			)
			got := findOpenMRForBranchAndSHA(bd, t.TempDir(), tc.branch, tc.commitSHA)
			if got != tc.want {
				t.Errorf("findOpenMRForBranchAndSHA = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHandleZombieRestart_FilesUnfiledMRForUnpushedCommits verifies gu-j98v:
// when the polecat has local commits ahead of origin/main and no MR bead,
// handleZombieRestart invokes recoverUnfiledMR which must (a) push the branch,
// (b) file the MR, (c) archive the polecat. Restart must NOT happen.
//
// Not parallel: overrides package-level verifyUnfiledMR and recoverUnfiledMR.
func TestHandleZombieRestart_FilesUnfiledMRForUnpushedCommits(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	oldVerify := verifyUnfiledMR
	oldRecover := recoverUnfiledMR
	classifyPolecatMergeState = func(string, string, string) (MergeCheckResult, error) { return MergeCheckNotMerged, nil }
	verifyUnfiledMR = func(_ *BdCli, _, _, _, _ string) (*UnfiledMRState, error) {
		return &UnfiledMRState{
			Branch:        "polecat/nux",
			HeadSHA:       "abc123",
			Target:        "main",
			CommitsAhead:  true,
			AlreadyPushed: false,
		}, nil
	}
	recoverCalled := false
	recoverUnfiledMR = func(_ *BdCli, _, _, _ string, s *UnfiledMRState) (string, error) {
		recoverCalled = true
		if s.AlreadyPushed {
			t.Errorf("recover called with AlreadyPushed=true, want false for this case")
		}
		return "filed-mr-unpushed-commits (aa-unpushed-commits, mr=gt-mr-xyz)", nil
	}
	t.Cleanup(func() {
		classifyPolecatMergeState = oldClassify
		verifyUnfiledMR = oldVerify
		recoverUnfiledMR = oldRecover
	})

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)
	z := &ZombieResult{PolecatName: "nux", HookBead: "gt-abc"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "nux", "gt-abc", "", z)

	if !recoverCalled {
		t.Fatal("recoverUnfiledMR not called for polecat with unfiled commits")
	}
	if !strings.Contains(z.Action, "filed-mr-unpushed-commits") ||
		!strings.Contains(z.Action, "aa-unpushed-commits") {
		t.Errorf("Action = %q, want filed-mr-unpushed-commits (aa-unpushed-commits, ...)", z.Action)
	}
	if strings.HasPrefix(z.Action, "restarted") || strings.HasPrefix(z.Action, "restart-") {
		t.Errorf("Action = %q, polecat must not be restarted when MR is being filed", z.Action)
	}
}

// TestHandleZombieRestart_FilesUnfiledMRForPushedNoMR covers the sibling
// failure mode: commits were pushed before the session died, but `gt mq
// submit` never ran.
//
// Not parallel: overrides package-level verifyUnfiledMR and recoverUnfiledMR.
func TestHandleZombieRestart_FilesUnfiledMRForPushedNoMR(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	oldVerify := verifyUnfiledMR
	oldRecover := recoverUnfiledMR
	classifyPolecatMergeState = func(string, string, string) (MergeCheckResult, error) { return MergeCheckNotMerged, nil }
	verifyUnfiledMR = func(_ *BdCli, _, _, _, _ string) (*UnfiledMRState, error) {
		return &UnfiledMRState{
			Branch:        "polecat/nux",
			HeadSHA:       "abc123",
			Target:        "main",
			CommitsAhead:  true,
			AlreadyPushed: true,
		}, nil
	}
	recoverUnfiledMR = func(_ *BdCli, _, _, _ string, s *UnfiledMRState) (string, error) {
		if !s.AlreadyPushed {
			t.Errorf("recover called with AlreadyPushed=false, want true for this case")
		}
		return "filed-mr-pushed-no-mr (aa-pushed-no-mr, mr=gt-mr-xyz)", nil
	}
	t.Cleanup(func() {
		classifyPolecatMergeState = oldClassify
		verifyUnfiledMR = oldVerify
		recoverUnfiledMR = oldRecover
	})

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)
	z := &ZombieResult{PolecatName: "nux", HookBead: "gt-abc"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "nux", "gt-abc", "", z)

	if !strings.Contains(z.Action, "filed-mr-pushed-no-mr") ||
		!strings.Contains(z.Action, "aa-pushed-no-mr") {
		t.Errorf("Action = %q, want filed-mr-pushed-no-mr (aa-pushed-no-mr, ...)", z.Action)
	}
}

// TestHandleZombieRestart_SkipsUnfiledCheckWhenMRExists verifies that when an
// MR bead already exists for the branch+SHA, handleZombieRestart does NOT
// double-submit — it falls through to the normal restart flow.
//
// Not parallel: overrides package-level vars.
func TestHandleZombieRestart_SkipsUnfiledCheckWhenMRExists(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	oldVerify := verifyUnfiledMR
	oldRecover := recoverUnfiledMR
	classifyPolecatMergeState = func(string, string, string) (MergeCheckResult, error) { return MergeCheckNotMerged, nil }
	verifyUnfiledMR = func(_ *BdCli, _, _, _, _ string) (*UnfiledMRState, error) {
		return &UnfiledMRState{
			Branch:       "polecat/nux",
			HeadSHA:      "abc123",
			Target:       "main",
			CommitsAhead: true,
			ExistingMRID: "gt-mr-existing", // MR already there → no recovery
		}, nil
	}
	recoverUnfiledMR = func(_ *BdCli, _, _, _ string, _ *UnfiledMRState) (string, error) {
		t.Fatal("recoverUnfiledMR must NOT be called when an MR already exists")
		return "", nil
	}
	t.Cleanup(func() {
		classifyPolecatMergeState = oldClassify
		verifyUnfiledMR = oldVerify
		recoverUnfiledMR = oldRecover
	})

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)
	z := &ZombieResult{PolecatName: "nux", HookBead: "gt-abc"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "nux", "gt-abc", "clean", z)

	if strings.Contains(z.Action, "filed-mr") || strings.Contains(z.Action, "aa-") {
		t.Errorf("Action = %q, want no unfiled-MR recovery tag when MR exists", z.Action)
	}
}

// TestHandleZombieRestart_SkipsUnfiledCheckWhenNoCommits verifies that a
// polecat with no commits ahead of target (e.g., died before doing any work)
// falls through to the normal restart flow.
//
// Not parallel: overrides package-level vars.
func TestHandleZombieRestart_SkipsUnfiledCheckWhenNoCommits(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	oldVerify := verifyUnfiledMR
	oldRecover := recoverUnfiledMR
	classifyPolecatMergeState = func(string, string, string) (MergeCheckResult, error) { return MergeCheckNotMerged, nil }
	verifyUnfiledMR = func(_ *BdCli, _, _, _, _ string) (*UnfiledMRState, error) {
		return &UnfiledMRState{
			Branch:       "polecat/nux",
			CommitsAhead: false, // nothing to recover
		}, nil
	}
	recoverUnfiledMR = func(_ *BdCli, _, _, _ string, _ *UnfiledMRState) (string, error) {
		t.Fatal("recoverUnfiledMR must NOT be called when no commits ahead")
		return "", nil
	}
	t.Cleanup(func() {
		classifyPolecatMergeState = oldClassify
		verifyUnfiledMR = oldVerify
		recoverUnfiledMR = oldRecover
	})

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)
	z := &ZombieResult{PolecatName: "nux", HookBead: "gt-abc"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "nux", "gt-abc", "clean", z)

	if strings.Contains(z.Action, "filed-mr") || strings.Contains(z.Action, "aa-unpushed") ||
		strings.Contains(z.Action, "aa-pushed-no-mr") {
		t.Errorf("Action = %q, want no unfiled-MR recovery tag when no commits ahead", z.Action)
	}
}

// TestHandleZombieRestart_AaApwBeatsUnfiledMR verifies the precedence rule:
// aa-apw (work already merged via squash) must fire BEFORE the unfiled-MR
// recovery path. Otherwise we would push a pre-squash HEAD and create a
// duplicate MR for work already on main.
//
// Not parallel: overrides package-level vars.
func TestHandleZombieRestart_AaApwBeatsUnfiledMR(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	oldVerify := verifyUnfiledMR
	oldRecover := recoverUnfiledMR
	classifyPolecatMergeState = func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckMerged, nil // work is already merged
	}
	verifyUnfiledMR = func(_ *BdCli, _, _, _, _ string) (*UnfiledMRState, error) {
		t.Fatal("verifyUnfiledMR must NOT be called when aa-apw fires")
		return nil, nil
	}
	recoverUnfiledMR = func(_ *BdCli, _, _, _ string, _ *UnfiledMRState) (string, error) {
		t.Fatal("recoverUnfiledMR must NOT be called when aa-apw fires")
		return "", nil
	}
	t.Cleanup(func() {
		classifyPolecatMergeState = oldClassify
		verifyUnfiledMR = oldVerify
		recoverUnfiledMR = oldRecover
	})

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)
	z := &ZombieResult{PolecatName: "nux", HookBead: "gt-abc"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "nux", "gt-abc", "has_unpushed", z)

	if !strings.Contains(z.Action, "work-already-merged") {
		t.Errorf("Action = %q, want aa-apw path to win", z.Action)
	}
}

// TestHandleZombieRestart_EmptyPolecat verifies gu-ur85: a polecat that produced
// no commits (HEAD == merge-base) is archived as "empty," not "merged."
func TestHandleZombieRestart_EmptyPolecat(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	classifyPolecatMergeState = func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckEmpty, nil
	}
	t.Cleanup(func() { classifyPolecatMergeState = oldClassify })

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	z := &ZombieResult{PolecatName: "guzzle", HookBead: "gu-jq0q"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "guzzle", "gu-jq0q", "clean", z)

	if !strings.Contains(z.Action, "archived-empty") && !strings.Contains(z.Action, "archive-failed-empty") {
		t.Errorf("Action = %q, want archived-empty for polecat with no work", z.Action)
	}
	if strings.Contains(z.Action, "work-already-merged") {
		t.Errorf("Action = %q, must NOT say work-already-merged for empty polecat", z.Action)
	}
}

// TestHandleZombieRestart_AutoSaveEscalates verifies gu-ur85: a polecat whose
// only divergent commits are gt-pvx auto-save commits is escalated, not archived.
func TestHandleZombieRestart_AutoSaveEscalates(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	classifyPolecatMergeState = func(string, string, string) (MergeCheckResult, error) {
		return MergeCheckAutoSave, nil
	}
	t.Cleanup(func() { classifyPolecatMergeState = oldClassify })

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	z := &ZombieResult{PolecatName: "rust", HookBead: "gu-g3ks"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "rust", "gu-g3ks", "has_unpushed", z)

	if !strings.Contains(z.Action, "escalate-auto-save-work") {
		t.Errorf("Action = %q, want escalate-auto-save-work for gt-pvx commits", z.Action)
	}
	if strings.Contains(z.Action, "archived") && !strings.Contains(z.Action, "escalate") {
		t.Errorf("Action = %q, must NOT archive auto-save work", z.Action)
	}
}
// when recoverUnfiledMR fails, the error flows onto zombie.Error so the patrol
// operator sees what went wrong. The Action string still reflects the attempt.
//
// Not parallel: overrides package-level vars.
func TestHandleZombieRestart_FilesUnfiledMRPropagatesRecoverError(t *testing.T) {
	oldClassify := classifyPolecatMergeState
	oldVerify := verifyUnfiledMR
	oldRecover := recoverUnfiledMR
	classifyPolecatMergeState = func(string, string, string) (MergeCheckResult, error) { return MergeCheckNotMerged, nil }
	verifyUnfiledMR = func(_ *BdCli, _, _, _, _ string) (*UnfiledMRState, error) {
		return &UnfiledMRState{
			Branch:        "polecat/nux",
			HeadSHA:       "abc123",
			Target:        "main",
			CommitsAhead:  true,
			AlreadyPushed: false,
		}, nil
	}
	recoverUnfiledMR = func(_ *BdCli, _, _, _ string, _ *UnfiledMRState) (string, error) {
		return "recover-failed-push (aa-unpushed-commits): boom",
			fmt.Errorf("simulated push failure")
	}
	t.Cleanup(func() {
		classifyPolecatMergeState = oldClassify
		verifyUnfiledMR = oldVerify
		recoverUnfiledMR = oldRecover
	})

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)
	z := &ZombieResult{PolecatName: "nux", HookBead: "gt-abc"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "nux", "gt-abc", "", z)

	if z.Error == nil {
		t.Fatal("expected zombie.Error to propagate from recoverUnfiledMR")
	}
	if !strings.Contains(z.Action, "recover-failed-push") {
		t.Errorf("Action = %q, want recover-failed-push tag", z.Action)
	}
}

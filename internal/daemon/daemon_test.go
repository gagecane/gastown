package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestDefaultConfig(t *testing.T) {
	townRoot := "/tmp/test-town"
	config := DefaultConfig(townRoot)

	if config.HeartbeatInterval != 5*time.Minute {
		t.Errorf("expected HeartbeatInterval 5m, got %v", config.HeartbeatInterval)
	}
	if config.TownRoot != townRoot {
		t.Errorf("expected TownRoot %q, got %q", townRoot, config.TownRoot)
	}
	if config.LogFile != filepath.Join(townRoot, "daemon", "daemon.log") {
		t.Errorf("expected LogFile in daemon dir, got %q", config.LogFile)
	}
	if config.PidFile != filepath.Join(townRoot, "daemon", "daemon.pid") {
		t.Errorf("expected PidFile in daemon dir, got %q", config.PidFile)
	}
}

func TestStateFile(t *testing.T) {
	townRoot := "/tmp/test-town"
	expected := filepath.Join(townRoot, "daemon", "state.json")
	result := StateFile(townRoot)

	if result != expected {
		t.Errorf("StateFile(%q) = %q, expected %q", townRoot, result, expected)
	}
}

func TestLoadState_NonExistent(t *testing.T) {
	// Create temp dir that doesn't have a state file
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	state, err := LoadState(tmpDir)
	if err != nil {
		t.Errorf("LoadState should not error for missing file, got %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.Running {
		t.Error("expected Running=false for empty state")
	}
	if state.PID != 0 {
		t.Errorf("expected PID=0 for empty state, got %d", state.PID)
	}
}

func TestLoadState_ExistingFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create daemon directory
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a state file
	startTime := time.Now().Truncate(time.Second)
	testState := &State{
		Running:        true,
		PID:            12345,
		StartedAt:      startTime,
		LastHeartbeat:  startTime,
		HeartbeatCount: 42,
	}

	data, err := json.MarshalIndent(testState, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir, "state.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Load and verify
	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}
	if !loaded.Running {
		t.Error("expected Running=true")
	}
	if loaded.PID != 12345 {
		t.Errorf("expected PID=12345, got %d", loaded.PID)
	}
	if loaded.HeartbeatCount != 42 {
		t.Errorf("expected HeartbeatCount=42, got %d", loaded.HeartbeatCount)
	}
}

func TestLoadState_InvalidJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create daemon directory with invalid JSON
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir, "state.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadState(tmpDir)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSaveState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	state := &State{
		Running:        true,
		PID:            9999,
		StartedAt:      time.Now(),
		LastHeartbeat:  time.Now(),
		HeartbeatCount: 100,
	}

	// SaveState should create daemon directory if needed
	if err := SaveState(tmpDir, state); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	// Verify file exists
	stateFile := StateFile(tmpDir)
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("state file should exist: %v", err)
	}

	// Verify contents
	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}
	if loaded.PID != 9999 {
		t.Errorf("expected PID=9999, got %d", loaded.PID)
	}
	if loaded.HeartbeatCount != 100 {
		t.Errorf("expected HeartbeatCount=100, got %d", loaded.HeartbeatCount)
	}
}

func TestSaveLoadState_Roundtrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	original := &State{
		Running:        true,
		PID:            54321,
		StartedAt:      time.Now().Truncate(time.Second),
		LastHeartbeat:  time.Now().Truncate(time.Second),
		HeartbeatCount: 1000,
	}

	if err := SaveState(tmpDir, original); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}

	if loaded.Running != original.Running {
		t.Errorf("Running mismatch: got %v, want %v", loaded.Running, original.Running)
	}
	if loaded.PID != original.PID {
		t.Errorf("PID mismatch: got %d, want %d", loaded.PID, original.PID)
	}
	if loaded.HeartbeatCount != original.HeartbeatCount {
		t.Errorf("HeartbeatCount mismatch: got %d, want %d", loaded.HeartbeatCount, original.HeartbeatCount)
	}
	// Time comparison with truncation to handle JSON serialization
	if !loaded.StartedAt.Truncate(time.Second).Equal(original.StartedAt) {
		t.Errorf("StartedAt mismatch: got %v, want %v", loaded.StartedAt, original.StartedAt)
	}
}

func TestListPolecatWorktrees_SkipsHiddenDirs(t *testing.T) {
	tmpDir := t.TempDir()
	polecatsDir := filepath.Join(tmpDir, "some-rig", "polecats")

	if err := os.MkdirAll(filepath.Join(polecatsDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(polecatsDir, "furiosa"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polecatsDir, "not-a-dir.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		t.Fatalf("listPolecatWorktrees returned error: %v", err)
	}

	if slices.Contains(polecats, ".claude") {
		t.Fatalf("expected hidden dir .claude to be ignored, got %v", polecats)
	}
	if !slices.Contains(polecats, "furiosa") {
		t.Fatalf("expected furiosa to be included, got %v", polecats)
	}
}

// NOTE: TestIsWitnessSession removed - isWitnessSession function was deleted
// as part of ZFC cleanup. Witness poking is now Deacon's responsibility.

func TestLifecycleAction_Constants(t *testing.T) {
	// Verify constants have expected string values
	if ActionCycle != "cycle" {
		t.Errorf("expected ActionCycle='cycle', got %q", ActionCycle)
	}
	if ActionRestart != "restart" {
		t.Errorf("expected ActionRestart='restart', got %q", ActionRestart)
	}
	if ActionShutdown != "shutdown" {
		t.Errorf("expected ActionShutdown='shutdown', got %q", ActionShutdown)
	}
}

func TestLifecycleRequest_Serialization(t *testing.T) {
	request := &LifecycleRequest{
		From:      "mayor",
		Action:    ActionCycle,
		Timestamp: time.Now().Truncate(time.Second),
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var loaded LifecycleRequest
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if loaded.From != request.From {
		t.Errorf("From mismatch: got %q, want %q", loaded.From, request.From)
	}
	if loaded.Action != request.Action {
		t.Errorf("Action mismatch: got %q, want %q", loaded.Action, request.Action)
	}
}

func TestIsShutdownInProgress_NoLockFile(t *testing.T) {
	tmpDir := t.TempDir()

	d := &Daemon{
		config: &Config{TownRoot: tmpDir},
	}

	// No lock file exists - should return false
	if d.isShutdownInProgress() {
		t.Error("expected false when lock file doesn't exist")
	}
}

func TestIsShutdownInProgress_StaleLockFile(t *testing.T) {
	tmpDir := t.TempDir()
	lockDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(lockDir, "shutdown.lock")

	// Create a stale lock file (not actually locked)
	if err := os.WriteFile(lockPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config: &Config{TownRoot: tmpDir},
	}

	// File exists but not locked - should return false
	if d.isShutdownInProgress() {
		t.Error("expected false when lock file exists but is not locked")
	}

	// File should still exist - flock files are never removed to prevent
	// a race where concurrent callers lock different inodes
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("expected lock file to be preserved (flock files should not be removed)")
	}
}

func TestIsShutdownInProgress_ActiveLock(t *testing.T) {
	tmpDir := t.TempDir()
	lockDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(lockDir, "shutdown.lock")

	// Create and hold the lock (simulating active shutdown)
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	if !locked {
		t.Fatal("expected to acquire lock")
	}
	defer func() { _ = lock.Unlock() }()

	d := &Daemon{
		config: &Config{TownRoot: tmpDir},
	}

	// File exists and is locked - should return true
	if !d.isShutdownInProgress() {
		t.Error("expected true when lock file is actively held")
	}

	// File should still exist (we're still holding the lock)
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("lock file should still exist: %v", err)
	}
}

// TestDaemon_StartsManagerAndScanner verifies that the convoy manager (event-driven + stranded scan)
// starts and stops correctly when used as the daemon does.
func TestDaemon_StartsManagerAndScanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	manager := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 1*time.Hour, nil, nil, nil)
	if err := manager.Start(); err != nil {
		t.Fatalf("manager Start: %v", err)
	}
	manager.Stop()
}

// TestDaemon_StopsManagerAndScanner verifies that stopping the convoy manager
// completes without blocking (e.g. context cancellation works).
func TestDaemon_StopsManagerAndScanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	manager := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 1*time.Hour, nil, nil, nil)
	if err := manager.Start(); err != nil {
		t.Fatalf("manager Start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not complete within 5s")
	}
}

// TestIsRunningFromPID_StalePIDReturnsNoError verifies that isRunningFromPID
// returns (false, 0, nil) — not an error — when it finds and removes a stale
// PID file. This is the fix for GH#2107: `gt daemon start` was treating the
// stale cleanup as an error, showing help text instead of starting the daemon.
func TestIsRunningFromPID_StalePIDReturnsNoError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a PID file pointing to a process that doesn't exist.
	// PID 2^22-1 (4194303) is extremely unlikely to be in use.
	stalePID := 4194303
	pidFile := filepath.Join(daemonDir, "daemon.pid")
	if _, err := writePIDFile(pidFile, stalePID); err != nil {
		t.Fatal(err)
	}

	running, pid, err := isRunningFromPID(tmpDir)
	if err != nil {
		t.Errorf("isRunningFromPID should not return error for stale PID, got: %v", err)
	}
	if running {
		t.Error("expected running=false for stale PID")
	}
	if pid != 0 {
		t.Errorf("expected pid=0, got %d", pid)
	}

	// PID file should have been removed
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected stale PID file to be removed")
	}
}

// TestIsRunningFromPID_NoPIDFile verifies clean return when no PID file exists.
func TestIsRunningFromPID_NoPIDFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	running, pid, err := isRunningFromPID(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if running {
		t.Error("expected running=false")
	}
	if pid != 0 {
		t.Errorf("expected pid=0, got %d", pid)
	}
}

// TestIsRunningFromPID_LiveProcess verifies detection of a live process.
func TestIsRunningFromPID_LiveProcess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Use our own PID — guaranteed alive
	pidFile := filepath.Join(daemonDir, "daemon.pid")
	if _, err := writePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatal(err)
	}

	running, pid, err := isRunningFromPID(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !running {
		t.Error("expected running=true for live process")
	}
	if pid != os.Getpid() {
		t.Errorf("expected pid=%d, got %d", os.Getpid(), pid)
	}
}

func TestHasPendingEvents_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	eventDir := filepath.Join(tmpDir, "events", "refinery")
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	if d.hasPendingEvents("refinery") {
		t.Error("expected false for empty event directory")
	}
}

func TestHasPendingEvents_MissingDir(t *testing.T) {
	tmpDir := t.TempDir()

	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	if d.hasPendingEvents("refinery") {
		t.Error("expected false when event directory doesn't exist")
	}
}

func TestHasPendingEvents_WithEventFiles(t *testing.T) {
	tmpDir := t.TempDir()
	eventDir := filepath.Join(tmpDir, "events", "refinery")
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create an event file
	eventFile := filepath.Join(eventDir, "1234567890-1-12345.event")
	if err := os.WriteFile(eventFile, []byte(`{"type":"MQ_SUBMIT"}`), 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	if !d.hasPendingEvents("refinery") {
		t.Error("expected true when .event files exist")
	}
}

func TestHasPendingEvents_IgnoresNonEventFiles(t *testing.T) {
	tmpDir := t.TempDir()
	eventDir := filepath.Join(tmpDir, "events", "refinery")
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a non-event file (e.g., .tmp or .lock)
	if err := os.WriteFile(filepath.Join(eventDir, "temp.lock"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	if d.hasPendingEvents("refinery") {
		t.Error("expected false when only non-.event files exist")
	}
}

// TestIsRigOperational_FailSafeOnDoltUnavailable verifies that when Dolt is
// unavailable and we can't check the rig bead for docked status, we fail-safe
// by assuming the rig is NOT operational. This prevents wasting API credits
// starting witnesses for potentially docked rigs. (Regression test for
// bug where witnesses started for docked rigs during Dolt outage)
func TestIsRigOperational_FailSafeOnDoltUnavailable(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a minimal rig structure without a beads database
	rigName := "testrig"
	rigPath := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Create config.json with a prefix
	configPath := filepath.Join(rigPath, "config.json")
	configJSON := `{"beads": {"prefix": "tr"}}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create mayor/rig/.beads directory but NO Dolt database
	// This simulates Dolt being down or database not accessible
	mayorBeads := filepath.Join(rigPath, "mayor", "rig", ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatal(err)
	}

	// Create town-level .beads with routes.jsonl
	townBeads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := `{"prefix":"tr-","path":"testrig/mayor/rig"}`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create daemon with no Dolt server running
	d := &Daemon{
		config: &Config{
			TownRoot: tmpDir,
		},
		logger: log.New(io.Discard, "", 0), // Suppress log output
	}

	// When Dolt is unavailable, isRigOperational should return false
	// (fail-safe: assume not operational rather than risk starting docked rig)
	operational, reason := d.isRigOperational(rigName)
	if operational {
		t.Error("isRigOperational should return false when Dolt is unavailable (fail-safe)")
	}
	if reason == "" {
		t.Error("isRigOperational should provide a reason when returning false")
	}
	if !strings.Contains(reason, "Dolt unavailable") && !strings.Contains(reason, "cannot verify") {
		t.Errorf("reason should mention Dolt unavailable, got: %q", reason)
	}
}

// TestIsRigOperational_DockedRig verifies that docked rigs are correctly
// identified as not operational.
func TestIsRigOperational_DockedRig(t *testing.T) {
	tmpDir := t.TempDir()

	// Create rig with docked label on rig bead
	rigName := "dockedrig"
	rigPath := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create config.json
	configPath := filepath.Join(rigPath, "config.json")
	configJSON := `{"beads": {"prefix": "dr"}}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create town-level .beads with routes.jsonl
	townBeads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := `{"prefix":"dr-","path":"dockedrig/mayor/rig"}`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config: &Config{
			TownRoot: tmpDir,
		},
		logger: log.New(io.Discard, "", 0),
	}

	// Without a rig bead, should fail-safe to not operational
	operational, reason := d.isRigOperational(rigName)
	if operational {
		t.Error("isRigOperational should return false when rig bead is missing")
	}
	t.Logf("Docked rig check returned: operational=%v, reason=%q", operational, reason)
}

// TestIsRigOperational_NoWispConfig_NoWarningSpam is a regression test for
// gu-66xp: isRigOperational previously printed a warning on every heartbeat
// cycle for every rig without a wisp config file. With 15 rigs × ~8 heartbeats
// per minute, this produced ~260K log lines per day (52% of daemon.log). The
// absence of a wisp config is the normal state for rigs that have never been
// parked, so the warning was never actionable.
//
// This test verifies the warning is NOT emitted when the wisp config is
// missing but the rig is otherwise operational.
func TestIsRigOperational_NoWispConfig_NoWarningSpam(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a rig with no wisp config but a valid rig bead setup.
	// We don't need a live rig bead — we only need to verify the warning path
	// is not hit. The downstream IsRigParkedOrDockedE call may fail
	// (fail-safe), but that's fine — what we care about here is that the
	// "no wisp config" warning is suppressed.
	rigName := "noconfigrig"
	rigPath := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Capture daemon log output
	var logBuf bytes.Buffer
	d := &Daemon{
		config: &Config{
			TownRoot: tmpDir,
		},
		logger: log.New(&logBuf, "", 0),
	}

	// Call isRigOperational multiple times to simulate repeated heartbeats.
	// The pre-fix code would emit the "no wisp config" warning once per call.
	for i := 0; i < 5; i++ {
		_, _ = d.isRigOperational(rigName)
	}

	logs := logBuf.String()
	if strings.Contains(logs, "no wisp config") {
		t.Errorf("isRigOperational should not emit 'no wisp config' warning (gu-66xp regression); log output:\n%s", logs)
	}
	if strings.Contains(logs, "parked state may have been lost") {
		t.Errorf("isRigOperational should not emit 'parked state may have been lost' warning (gu-66xp regression); log output:\n%s", logs)
	}
}

// TestIsRigOperational_MissingRigBead_LogsOnce is a regression test for
// gu-resv: previously, when a rig listed in rigs.json had no corresponding
// identity bead (e.g., `cait-rig-casc_integ`), the daemon logged a
// "failed to check rig X for docked/parked status: issue not found" warning
// on every heartbeat (~8/min × number of missing rigs). The furiosa diagnosis
// in ta-0pk described this as "thousands of futile lookups" burning Dolt
// cycles and polluting daemon.log.
//
// The fix distinguishes "rig identity bead not found" (persistent state,
// log once, treat as operational) from "Dolt unavailable" (transient, keep
// fail-safe behavior). This test covers the log-once behavior and the
// non-blocking semantics.
func TestIsRigOperational_MissingRigBead_LogsOnce(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a rig that's registered in rigs.json but has no identity bead.
	// The bd stub below returns the canonical "no issue found" error,
	// which beads.wrapError translates to beads.ErrNotFound, which
	// rig.checkParkedOrDocked then wraps as rig.ErrRigBeadNotFound.
	rigName := "orphanrig"
	rigPath := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig"), 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(rigPath, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"beads": {"prefix": "or"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigsJSON := `{"version":1,"rigs":{"orphanrig":{"beads":{"prefix":"or"}}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Stub bd so Show() reports "no issue found", exercising the
	// ErrRigBeadNotFound path rather than "no beads database found"
	// (which is correctly treated as a transient failure).
	stubDir := t.TempDir()
	stubScript := `#!/bin/sh
cat >&2 <<'EOT'
Error: no issue found matching "or-rig-orphanrig"
EOT
exit 1
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logBuf bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: tmpDir},
		logger: log.New(&logBuf, "", 0),
	}

	// Simulate 10 heartbeats. Pre-fix, each would emit a "failed to check
	// rig" warning — 10 duplicate lines. Post-fix, exactly one warning
	// should appear.
	for i := 0; i < 10; i++ {
		operational, reason := d.isRigOperational(rigName)
		// A rig without an identity bead cannot carry dock/park labels.
		// Treat as operational so work can proceed.
		if !operational {
			t.Errorf("heartbeat %d: expected operational=true for rig with missing identity bead, got false (reason=%q)", i, reason)
		}
	}

	logs := logBuf.String()
	missCount := strings.Count(logs, "has no identity bead")
	if missCount == 0 {
		t.Errorf("expected exactly one 'has no identity bead' warning, got none; log output:\n%s", logs)
	}
	if missCount > 1 {
		t.Errorf("expected exactly one 'has no identity bead' warning (log-once), got %d; log output:\n%s", missCount, logs)
	}

	// The "Dolt unavailable" fail-safe path should NOT be taken for a
	// missing bead — that path is reserved for transient failures.
	if strings.Contains(logs, "Dolt unavailable") {
		t.Errorf("missing rig bead should not produce 'Dolt unavailable' warning (that's for transient errors); log output:\n%s", logs)
	}
	// The pre-fix warning format must not recur for missing beads.
	if strings.Contains(logs, "issue not found") {
		t.Errorf("missing rig bead should not produce 'issue not found' warning spam; log output:\n%s", logs)
	}
}

// TestIsRigOperational_DoltDown_StillFailsSafe guards against a regression
// where relaxing the missing-bead path accidentally also relaxed the
// transient-Dolt-failure path. A rig whose identity bead cannot be queried
// due to Dolt being unreachable must still be treated as non-operational to
// avoid starting agents on potentially-docked rigs.
func TestIsRigOperational_DoltDown_StillFailsSafe(t *testing.T) {
	tmpDir := t.TempDir()

	rigName := "doltdownrig"
	rigPath := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(`{"beads":{"prefix":"dd"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(`{"version":1,"rigs":{"doltdownrig":{"beads":{"prefix":"dd"}}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Stub bd: simulate Dolt connection failure. NOT a "not found" response.
	stubDir := t.TempDir()
	stubScript := `#!/bin/sh
cat >&2 <<'EOT'
Error: [mysql] read tcp 127.0.0.1:3307: connection refused
EOT
exit 1
`
	if err := os.WriteFile(filepath.Join(stubDir, "bd"), []byte(stubScript), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logBuf bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: tmpDir},
		logger: log.New(&logBuf, "", 0),
	}

	operational, reason := d.isRigOperational(rigName)
	if operational {
		t.Errorf("expected operational=false for transient Dolt failure (fail-safe), got true")
	}
	if !strings.Contains(reason, "cannot verify") && !strings.Contains(reason, "Dolt unavailable") {
		t.Errorf("expected 'cannot verify' or 'Dolt unavailable' in reason, got %q", reason)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "failed to check rig") {
		t.Errorf("expected 'failed to check rig' warning for Dolt failure; log output:\n%s", logs)
	}
}

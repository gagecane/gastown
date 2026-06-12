package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/tmux"
)

// testHandlerDaemon creates a minimal Daemon with a logger for handler tests.
func testHandlerDaemon(t *testing.T, townRoot string) *Daemon {
	t.Helper()
	return &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(os.Stderr, "test: ", log.LstdFlags),
	}
}

// testSetupDogState creates a dog directory with a .dog.json state file.
func testSetupDogState(t *testing.T, townRoot, name string, state dog.State, lastActive time.Time) {
	t.Helper()

	kennelDir := filepath.Join(townRoot, "deacon", "dogs", name)
	if err := os.MkdirAll(kennelDir, 0755); err != nil {
		t.Fatalf("Failed to create kennel dir for %s: %v", name, err)
	}

	ds := &dog.DogState{
		Name:       name,
		State:      state,
		LastActive: lastActive,
		Worktrees:  map[string]string{},
		CreatedAt:  lastActive,
		UpdatedAt:  lastActive,
	}

	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal dog state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kennelDir, ".dog.json"), data, 0644); err != nil {
		t.Fatalf("Failed to write dog state: %v", err)
	}
}

// testDogExists checks if a dog directory exists in the kennel.
func testDogExists(townRoot, name string) bool {
	_, err := os.Stat(filepath.Join(townRoot, "deacon", "dogs", name, ".dog.json"))
	return err == nil
}

// testSetupWorkingDogState creates a working dog with a work assignment.
func testSetupWorkingDogState(t *testing.T, townRoot, name, work string, lastActive time.Time) {
	t.Helper()

	kennelDir := filepath.Join(townRoot, "deacon", "dogs", name)
	if err := os.MkdirAll(kennelDir, 0755); err != nil {
		t.Fatalf("Failed to create kennel dir for %s: %v", name, err)
	}

	ds := &dog.DogState{
		Name:       name,
		State:      dog.StateWorking,
		Work:       work,
		LastActive: lastActive,
		Worktrees:  map[string]string{},
		CreatedAt:  lastActive,
		UpdatedAt:  lastActive,
	}

	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal dog state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kennelDir, ".dog.json"), data, 0644); err != nil {
		t.Fatalf("Failed to write dog state: %v", err)
	}
}

func TestDetectStaleWorkingDogs_ClearsStaleWorkers(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Dog working for 3 hours with no activity — should be cleared.
	testSetupWorkingDogState(t, townRoot, "stale", constants.MolConvoyFeed, time.Now().Add(-3*time.Hour))

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("stale")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("stale dog state = %q, want idle", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("stale dog work = %q, want empty", dg.Work)
	}
}

func TestDetectStaleWorkingDogs_SkipsRecentWorkers(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Dog working for 30 minutes — should NOT be cleared.
	testSetupWorkingDogState(t, townRoot, "active", constants.MolConvoyFeed, time.Now().Add(-30*time.Minute))

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("active")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateWorking {
		t.Errorf("active dog state = %q, want working", dg.State)
	}
	if dg.Work != constants.MolConvoyFeed {
		t.Errorf("active dog work = %q, want %s", dg.Work, constants.MolConvoyFeed)
	}
}

func TestDetectStaleWorkingDogs_SkipsIdleDogs(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Idle dog with old last_active — should NOT be touched by this function.
	testSetupDogState(t, townRoot, "idle-old", dog.StateIdle, time.Now().Add(-5*time.Hour))

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("idle-old")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("idle dog state = %q, want idle", dg.State)
	}
}

func TestDetectStaleWorkingDogs_EmptyKennel(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Should not panic or error with empty kennel.
	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})
}

// testWriteTownPlugin writes a town-level plugin.md with a cooldown gate and
// optional execution timeout so pluginStuckThresholds can discover it.
func testWriteTownPlugin(t *testing.T, townRoot, name, cooldown, execTimeout string) {
	t.Helper()
	dir := filepath.Join(townRoot, "plugins", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create plugin dir for %s: %v", name, err)
	}
	content := "+++\n" +
		"name = \"" + name + "\"\n" +
		"description = \"test plugin\"\n" +
		"version = 1\n" +
		"[gate]\n" +
		"type = \"cooldown\"\n" +
		"duration = \"" + cooldown + "\"\n"
	if execTimeout != "" {
		content += "[execution]\n" +
			"timeout = \"" + execTimeout + "\"\n"
	}
	content += "+++\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.md"), []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write plugin.md for %s: %v", name, err)
	}
}

// TestDetectStaleWorkingDogs_PluginTighterThreshold verifies a dog holding a
// short-cadence plugin slot is reclaimed at the plugin's per-plugin threshold
// (2x interval) rather than the multi-hour blanket timeout. See gu-9jmd3.
func TestDetectStaleWorkingDogs_PluginTighterThreshold(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	// dolt-backup: 15m cooldown -> 30m stuck threshold.
	testWriteTownPlugin(t, townRoot, "dolt-backup", "15m", "5m")

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Dog stuck on dolt-backup for 45m — under the 2h blanket but over the 30m
	// per-plugin threshold, so it MUST be cleared.
	testSetupWorkingDogState(t, townRoot, "delta", "plugin:dolt-backup", time.Now().Add(-45*time.Minute))

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("delta")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("delta state = %q, want idle (should be cleared at per-plugin threshold)", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("delta work = %q, want empty", dg.Work)
	}
}

// TestDetectStaleWorkingDogs_PluginUnderThreshold verifies a dog holding a
// plugin slot is NOT reclaimed before its per-plugin threshold elapses.
func TestDetectStaleWorkingDogs_PluginUnderThreshold(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	// dolt-backup: 15m cooldown -> 30m stuck threshold.
	testWriteTownPlugin(t, townRoot, "dolt-backup", "15m", "5m")

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Dog working on dolt-backup for 20m — under the 30m threshold, keep working.
	testSetupWorkingDogState(t, townRoot, "delta", "plugin:dolt-backup", time.Now().Add(-20*time.Minute))

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("delta")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateWorking {
		t.Errorf("delta state = %q, want working (under per-plugin threshold)", dg.State)
	}
}

// TestDetectStaleWorkingDogs_PluginFallsBackToBlanket verifies that a dog
// holding an unknown/undiscoverable plugin slot still uses the blanket timeout
// (no premature clearing).
func TestDetectStaleWorkingDogs_PluginFallsBackToBlanket(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// No plugin.md on disk for "ghost-plugin"; 45m working < 2h blanket -> keep.
	testSetupWorkingDogState(t, townRoot, "delta", "plugin:ghost-plugin", time.Now().Add(-45*time.Minute))

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("delta")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateWorking {
		t.Errorf("delta state = %q, want working (unknown plugin uses blanket timeout)", dg.State)
	}
}

func TestPluginWorkName(t *testing.T) {
	tests := []struct {
		work     string
		wantName string
		wantOK   bool
	}{
		{"plugin:dolt-backup", "dolt-backup", true},
		{"plugin:auto-dispatch (event-driven, rig=foo)", "auto-dispatch", true},
		{"mol-convoy-feed", "", false},
		{"", "", false},
		{"plugin:", "", false},
	}
	for _, tt := range tests {
		gotName, gotOK := pluginWorkName(tt.work)
		if gotName != tt.wantName || gotOK != tt.wantOK {
			t.Errorf("pluginWorkName(%q) = (%q, %v), want (%q, %v)", tt.work, gotName, gotOK, tt.wantName, tt.wantOK)
		}
	}
}

func TestReapIdleDogs_SkipsWorkingDogs(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create a working dog with old LastActive — should NOT be reaped.
	testSetupDogState(t, townRoot, "worker", dog.StateWorking, time.Now().Add(-5*time.Hour))

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	if !testDogExists(townRoot, "worker") {
		t.Error("working dog should not be removed by reapIdleDogs")
	}
}

func TestReapIdleDogs_SkipsRecentlyActiveDogs(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create idle dogs that were active recently — should NOT be reaped.
	for i := 0; i < 6; i++ {
		name := "recent-" + string(rune('a'+i))
		testSetupDogState(t, townRoot, name, dog.StateIdle, time.Now().Add(-30*time.Minute))
	}

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	// All dogs should still exist.
	dogs, err := mgr.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(dogs) != 6 {
		t.Errorf("expected 6 dogs after reap, got %d", len(dogs))
	}
}

func TestReapIdleDogs_RemovesLongIdleDogsWhenPoolOversized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: requires tmux")
	}
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create 6 idle dogs: 4 recent, 2 long-idle.
	// Pool is 6 > pool max (4), so long-idle dogs should be removed.
	for i := 0; i < 4; i++ {
		name := "recent-" + string(rune('a'+i))
		testSetupDogState(t, townRoot, name, dog.StateIdle, time.Now().Add(-10*time.Minute))
	}
	testSetupDogState(t, townRoot, "old-1", dog.StateIdle, time.Now().Add(-5*time.Hour))
	testSetupDogState(t, townRoot, "old-2", dog.StateIdle, time.Now().Add(-6*time.Hour))

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	// Long-idle dogs should be removed, recent ones kept.
	dogs, err := mgr.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	if len(dogs) > 4 {
		t.Errorf("expected pool trimmed to at most %d, got %d", 4, len(dogs))
	}

	// Verify the old dogs were removed.
	if testDogExists(townRoot, "old-1") {
		t.Error("old-1 should have been removed")
	}
	if testDogExists(townRoot, "old-2") {
		t.Error("old-2 should have been removed")
	}
}

func TestReapIdleDogs_DoesNotRemoveWhenPoolAtMaxSize(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create exactly the pool-max number of idle dogs, all long-idle.
	// Pool is NOT oversized, so none should be removed.
	for i := 0; i < 4; i++ {
		name := "idle-" + string(rune('a'+i))
		testSetupDogState(t, townRoot, name, dog.StateIdle, time.Now().Add(-5*time.Hour))
	}

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	dogs, err := mgr.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(dogs) != 4 {
		t.Errorf("expected %d dogs (pool not oversized), got %d", 4, len(dogs))
	}
}

func TestReapIdleDogs_StopsRemovingAtMaxPoolSize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: requires tmux")
	}
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create 7 idle dogs, all long-idle.
	// Should remove 3 to get down to the pool max (4).
	for i := 0; i < 7; i++ {
		name := "dog-" + string(rune('a'+i))
		testSetupDogState(t, townRoot, name, dog.StateIdle, time.Now().Add(-5*time.Hour))
	}

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	dogs, err := mgr.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(dogs) > 4 {
		t.Errorf("expected pool trimmed to %d, got %d", 4, len(dogs))
	}
}

func TestReapIdleDogs_MixedStates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: requires tmux")
	}
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// 2 working + 3 recent idle + 2 long-idle = 7 total.
	// Pool is oversized (7 > 4). Only long-idle IDLE dogs should be removed.
	// Working dogs are never touched.
	testSetupDogState(t, townRoot, "worker-a", dog.StateWorking, time.Now().Add(-5*time.Hour))
	testSetupDogState(t, townRoot, "worker-b", dog.StateWorking, time.Now().Add(-5*time.Hour))
	testSetupDogState(t, townRoot, "recent-a", dog.StateIdle, time.Now().Add(-10*time.Minute))
	testSetupDogState(t, townRoot, "recent-b", dog.StateIdle, time.Now().Add(-10*time.Minute))
	testSetupDogState(t, townRoot, "recent-c", dog.StateIdle, time.Now().Add(-10*time.Minute))
	testSetupDogState(t, townRoot, "old-a", dog.StateIdle, time.Now().Add(-5*time.Hour))
	testSetupDogState(t, townRoot, "old-b", dog.StateIdle, time.Now().Add(-6*time.Hour))

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	// Working dogs must survive.
	if !testDogExists(townRoot, "worker-a") {
		t.Error("worker-a should not be removed")
	}
	if !testDogExists(townRoot, "worker-b") {
		t.Error("worker-b should not be removed")
	}

	// Long-idle dogs should be removed (pool was 7 > 4).
	if testDogExists(townRoot, "old-a") {
		t.Error("old-a should have been removed")
	}
	if testDogExists(townRoot, "old-b") {
		t.Error("old-b should have been removed")
	}

	// Recent idle dogs should survive.
	if !testDogExists(townRoot, "recent-a") {
		t.Error("recent-a should not be removed")
	}
}

func TestReapIdleDogs_EmptyKennel(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Should not panic or error with empty kennel.
	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})
}

func TestDispatchPlugins_SkipsManualGatePlugin(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	pluginDir := filepath.Join(townRoot, "plugins", "test-manual")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	pluginMD := "+++\nname = \"test-manual\"\ndescription = \"manual gate plugin\"\n\n[gate]\ntype = \"manual\"\n+++\n\n# Instructions\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.md"), []byte(pluginMD), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	testSetupDogState(t, townRoot, "idle-dog", dog.StateIdle, time.Now().Add(-10*time.Minute))

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	d.dispatchPlugins(mgr, sm, rigsConfig)

	dg, err := mgr.Get("idle-dog")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("dog state = %q, want idle (manual-gate plugin must not auto-dispatch)", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("dog work = %q, want empty (manual-gate plugin must not auto-dispatch)", dg.Work)
	}
}

// TestDispatchPlugins_SkipsCronGateEmptySchedule verifies the cron branch's
// guard: a cron-gate plugin with no schedule is skipped (and never reaches the
// recorder), so it can't dispatch. The full cron eligibility matrix — fires
// when due, suppressed when not, in-flight grace — is covered by the bd-free
// pure-function tests TestCronDue / TestCronDue_ImpossibleSchedule in the
// plugin package; this test just pins the handler-level wiring that a cron gate
// is no longer silently dropped by the gate-type guard.
func TestDispatchPlugins_SkipsCronGateEmptySchedule(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	pluginDir := filepath.Join(townRoot, "plugins", "test-cron-noschedule")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	pluginMD := "+++\nname = \"test-cron-noschedule\"\ndescription = \"cron gate without schedule\"\n\n[gate]\ntype = \"cron\"\n+++\n\n# Instructions\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.md"), []byte(pluginMD), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	testSetupDogState(t, townRoot, "idle-dog", dog.StateIdle, time.Now().Add(-10*time.Minute))

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	d.dispatchPlugins(mgr, sm, rigsConfig)

	dg, err := mgr.Get("idle-dog")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("dog state = %q, want idle (cron gate with empty schedule must not dispatch)", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("dog work = %q, want empty (cron gate with empty schedule must not dispatch)", dg.Work)
	}
}

// TestFindDispatchableDog_Method_SkipsBackedOffDogs is the integration-level
// companion to the unit tests in dog_startup_backoff_test.go: it verifies
// that the daemon-aware findDispatchableDog method actually filters out
// dogs whose startup is in backoff. Without this filter the cooldown-gated
// dispatchPlugins pass would keep picking the same failing dog every
// heartbeat (the exact symptom gu-ro75 describes).
func TestFindDispatchableDog_Method_SkipsBackedOffDogs(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "daemon"), 0o755); err != nil {
		t.Fatalf("mkdir daemon dir: %v", err)
	}
	d := testHandlerDaemon(t, townRoot)
	d.restartTracker = NewRestartTracker(townRoot, RestartTrackerConfig{
		InitialBackoff:    5 * time.Second,
		MaxBackoff:        10 * time.Second,
		BackoffMultiplier: 2.0,
		CrashLoopWindow:   1 * time.Minute,
		CrashLoopCount:    5,
		StabilityPeriod:   30 * time.Second,
	})

	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())
	testSetupDogState(t, townRoot, "bravo", dog.StateIdle, time.Now())

	// Mark alpha as freshly-failed so backoff is active.
	d.recordDogStartFailure("alpha")

	mgr := dog.NewManager(townRoot, nil)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	got := d.findDispatchableDog(mgr, sm)
	if got == nil {
		t.Fatal("findDispatchableDog returned nil; expected bravo (alpha backed off)")
	}
	if got.Name != "bravo" {
		t.Errorf("findDispatchableDog = %q, want bravo (alpha should be in backoff)", got.Name)
	}
}

// TestFindDispatchableDog_Method_ReturnsNilWhenAllBackedOff verifies the
// stop-the-world safety: if every idle dog is in startup backoff, the
// daemon returns nil (and the caller defers the plugin) rather than
// forcing a dispatch onto a known-broken dog.
func TestFindDispatchableDog_Method_ReturnsNilWhenAllBackedOff(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "daemon"), 0o755); err != nil {
		t.Fatalf("mkdir daemon dir: %v", err)
	}
	d := testHandlerDaemon(t, townRoot)
	d.restartTracker = NewRestartTracker(townRoot, RestartTrackerConfig{
		InitialBackoff:    5 * time.Second,
		MaxBackoff:        10 * time.Second,
		BackoffMultiplier: 2.0,
		CrashLoopWindow:   1 * time.Minute,
		CrashLoopCount:    5,
		StabilityPeriod:   30 * time.Second,
	})

	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())
	testSetupDogState(t, townRoot, "bravo", dog.StateIdle, time.Now())

	d.recordDogStartFailure("alpha")
	d.recordDogStartFailure("bravo")

	mgr := dog.NewManager(townRoot, nil)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	got := d.findDispatchableDog(mgr, sm)
	if got != nil {
		t.Errorf("findDispatchableDog = %q, want nil (all dogs in backoff)", got.Name)
	}
}

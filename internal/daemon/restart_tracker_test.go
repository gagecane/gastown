package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneStaleAgents_RemovesDeadSessions(t *testing.T) {
	rt := NewRestartTracker(t.TempDir(), DefaultRestartTrackerConfig())

	// Add some agents to the tracker
	rt.RecordRestart("deacon")
	rt.RecordRestart("gt-furiosa")
	rt.RecordRestart("gt-nitro")

	// Only "deacon" has an active session (mapped to "hq-deacon")
	activeSessions := map[string]bool{
		"hq-deacon": true,
	}

	resolveSession := func(agentID string) string {
		switch agentID {
		case "deacon":
			return "hq-deacon"
		default:
			return agentID
		}
	}

	result := rt.PruneStaleAgents(activeSessions, resolveSession)

	if result.Total != 3 {
		t.Errorf("expected total=3, got %d", result.Total)
	}
	if len(result.Kept) != 1 {
		t.Errorf("expected 1 kept, got %d: %v", len(result.Kept), result.Kept)
	}
	if len(result.Pruned) != 2 {
		t.Errorf("expected 2 pruned, got %d: %v", len(result.Pruned), result.Pruned)
	}

	// Verify the pruned agents are gone from state
	if _, exists := rt.state.Agents["gt-furiosa"]; exists {
		t.Error("expected gt-furiosa to be pruned")
	}
	if _, exists := rt.state.Agents["gt-nitro"]; exists {
		t.Error("expected gt-nitro to be pruned")
	}
	if _, exists := rt.state.Agents["deacon"]; !exists {
		t.Error("expected deacon to be kept")
	}
}

func TestPruneStaleAgents_KeepsAllActiveSessions(t *testing.T) {
	rt := NewRestartTracker(t.TempDir(), DefaultRestartTrackerConfig())

	rt.RecordRestart("deacon")
	rt.RecordRestart("gt-furiosa")

	activeSessions := map[string]bool{
		"hq-deacon":  true,
		"gt-furiosa": true,
	}

	resolveSession := func(agentID string) string {
		switch agentID {
		case "deacon":
			return "hq-deacon"
		default:
			return agentID
		}
	}

	result := rt.PruneStaleAgents(activeSessions, resolveSession)

	if len(result.Pruned) != 0 {
		t.Errorf("expected no pruning, got %v", result.Pruned)
	}
	if len(result.Kept) != 2 {
		t.Errorf("expected 2 kept, got %d", len(result.Kept))
	}
}

func TestPruneStaleAgents_EmptyState(t *testing.T) {
	rt := NewRestartTracker(t.TempDir(), DefaultRestartTrackerConfig())

	activeSessions := map[string]bool{"hq-deacon": true}
	resolveSession := func(agentID string) string { return agentID }

	result := rt.PruneStaleAgents(activeSessions, resolveSession)

	if result.Total != 0 {
		t.Errorf("expected total=0, got %d", result.Total)
	}
	if len(result.Pruned) != 0 {
		t.Errorf("expected no pruning, got %v", result.Pruned)
	}
}

func TestPruneStaleState_SavesToDisk(t *testing.T) {
	townRoot := t.TempDir()
	daemonDir := filepath.Join(townRoot, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Set up restart state with two agents
	rt := NewRestartTracker(townRoot, DefaultRestartTrackerConfig())
	rt.RecordRestart("deacon")
	rt.RecordRestart("gt-dead-polecat")
	if err := rt.Save(); err != nil {
		t.Fatal(err)
	}

	// Only deacon is alive
	activeSessions := []string{"hq-deacon"}
	resolveSession := func(agentID string) string {
		if agentID == "deacon" {
			return "hq-deacon"
		}
		return agentID
	}

	result, err := PruneStaleState(townRoot, activeSessions, resolveSession)
	if err != nil {
		t.Fatalf("PruneStaleState: %v", err)
	}

	if len(result.Pruned) != 1 {
		t.Errorf("expected 1 pruned, got %d", len(result.Pruned))
	}

	// Reload state from disk and verify pruning persisted
	rt2 := NewRestartTracker(townRoot, RestartTrackerConfig{})
	if err := rt2.Load(); err != nil {
		t.Fatal(err)
	}

	if _, exists := rt2.state.Agents["gt-dead-polecat"]; exists {
		t.Error("expected gt-dead-polecat to be pruned from disk")
	}
	if _, exists := rt2.state.Agents["deacon"]; !exists {
		t.Error("expected deacon to be preserved on disk")
	}
}

func TestPreviewPruneStaleState_DoesNotModifyDisk(t *testing.T) {
	townRoot := t.TempDir()
	daemonDir := filepath.Join(townRoot, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Set up restart state with a dead agent
	rt := NewRestartTracker(townRoot, DefaultRestartTrackerConfig())
	rt.RecordRestart("gt-dead-polecat")
	if err := rt.Save(); err != nil {
		t.Fatal(err)
	}

	activeSessions := []string{} // No active sessions
	resolveSession := func(agentID string) string { return agentID }

	result, err := PreviewPruneStaleState(townRoot, activeSessions, resolveSession)
	if err != nil {
		t.Fatalf("PreviewPruneStaleState: %v", err)
	}

	if len(result.Pruned) != 1 {
		t.Errorf("expected 1 would-prune, got %d", len(result.Pruned))
	}

	// Verify disk was NOT modified
	rt2 := NewRestartTracker(townRoot, RestartTrackerConfig{})
	if err := rt2.Load(); err != nil {
		t.Fatal(err)
	}
	if _, exists := rt2.state.Agents["gt-dead-polecat"]; !exists {
		t.Error("expected gt-dead-polecat to still exist on disk (dry-run)")
	}
}

func TestRestartTracker_RecordAndCanRestart(t *testing.T) {
	rt := NewRestartTracker(t.TempDir(), DefaultRestartTrackerConfig())

	if !rt.CanRestart("test-agent") {
		t.Error("should be able to restart unknown agent")
	}

	rt.RecordRestart("test-agent")

	// Should be in backoff now
	if rt.CanRestart("test-agent") {
		t.Error("should NOT be able to restart agent in backoff")
	}

	remaining := rt.GetBackoffRemaining("test-agent")
	if remaining <= 0 {
		t.Error("expected positive backoff remaining")
	}
}

func TestRestartTracker_CrashLoop(t *testing.T) {
	cfg := RestartTrackerConfig{
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		CrashLoopWindow:   1 * time.Hour,
		CrashLoopCount:    3,
		StabilityPeriod:   1 * time.Hour,
	}
	rt := NewRestartTracker(t.TempDir(), cfg)

	// Record enough restarts to trigger crash loop
	for i := 0; i < 3; i++ {
		rt.RecordRestart("test-agent")
	}

	if !rt.IsInCrashLoop("test-agent") {
		t.Error("expected agent to be in crash loop after 3 restarts")
	}

	if rt.CanRestart("test-agent") {
		t.Error("should NOT be able to restart agent in crash loop")
	}

	// Clear crash loop
	rt.ClearCrashLoop("test-agent")
	if rt.IsInCrashLoop("test-agent") {
		t.Error("expected crash loop to be cleared")
	}
}

func TestRestartTracker_CrashLoopAutoExpiry(t *testing.T) {
	cfg := RestartTrackerConfig{
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		CrashLoopWindow:   1 * time.Hour,
		CrashLoopCount:    3,
		StabilityPeriod:   50 * time.Millisecond, // Short for testing: expiry = 100ms
	}
	rt := NewRestartTracker(t.TempDir(), cfg)

	// Trigger crash loop
	for i := 0; i < 3; i++ {
		rt.RecordRestart("test-agent")
	}

	if !rt.IsInCrashLoop("test-agent") {
		t.Fatal("expected agent to be in crash loop")
	}
	if rt.CanRestart("test-agent") {
		t.Fatal("should NOT be able to restart agent in crash loop")
	}

	// Wait for crash-loop expiry (2 × StabilityPeriod = 100ms)
	time.Sleep(110 * time.Millisecond)

	// Crash loop should now be considered expired
	if rt.IsInCrashLoop("test-agent") {
		t.Error("expected crash loop to be auto-expired after 2× StabilityPeriod")
	}
	if !rt.CanRestart("test-agent") {
		t.Error("expected CanRestart to be true after crash-loop expiry")
	}
}

func TestRestartTracker_CrashLoopNotExpiredWithinWindow(t *testing.T) {
	cfg := RestartTrackerConfig{
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		CrashLoopWindow:   1 * time.Hour,
		CrashLoopCount:    3,
		StabilityPeriod:   1 * time.Hour, // Long: expiry = 2h — won't expire in test
	}
	rt := NewRestartTracker(t.TempDir(), cfg)

	// Trigger crash loop
	for i := 0; i < 3; i++ {
		rt.RecordRestart("test-agent")
	}

	// Should still be in crash loop (nowhere near 2h expiry)
	if !rt.IsInCrashLoop("test-agent") {
		t.Error("expected agent to still be in crash loop within expiry window")
	}
	if rt.CanRestart("test-agent") {
		t.Error("should NOT be able to restart during active crash loop")
	}
}

func TestRestartTracker_SaveAndLoad(t *testing.T) {
	townRoot := t.TempDir()
	daemonDir := filepath.Join(townRoot, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	rt := NewRestartTracker(townRoot, DefaultRestartTrackerConfig())
	rt.RecordRestart("deacon")

	if err := rt.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load into a new tracker
	rt2 := NewRestartTracker(townRoot, DefaultRestartTrackerConfig())
	if err := rt2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, exists := rt2.state.Agents["deacon"]; !exists {
		t.Error("expected deacon in loaded state")
	}
}

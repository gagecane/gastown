package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/polecat"
)

// writeMergeQueueRig creates a minimal rig directory structure with a
// settings/config.json that toggles merge_queue.enabled. Returns the town
// root the caller can pass to the helpers under test.
func writeMergeQueueRig(t *testing.T, rigName string, mqEnabled bool) string {
	t.Helper()
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, rigName, "settings")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("mkdir rig settings: %v", err)
	}
	cfg := map[string]any{
		"type":           "rig-settings",
		"version":        1,
		"default_branch": "main",
		"merge_queue": map[string]any{
			"enabled": mqEnabled,
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	return townRoot
}

// writeRefineryHeartbeat writes a heartbeat file for the rig's refinery
// session at the given age.
func writeRefineryHeartbeat(t *testing.T, townRoot, rigPrefix string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir heartbeats: %v", err)
	}
	hb := polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-age),
		State:     polecat.HeartbeatWorking,
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	path := filepath.Join(dir, rigPrefix+"-refinery.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
}

func TestIsMergeQueueRig(t *testing.T) {
	tr := writeMergeQueueRig(t, "myrig", true)
	if !isMergeQueueRig(tr, "myrig") {
		t.Errorf("expected isMergeQueueRig=true when merge_queue.enabled=true")
	}

	tr2 := writeMergeQueueRig(t, "myrig", false)
	if isMergeQueueRig(tr2, "myrig") {
		t.Errorf("expected isMergeQueueRig=false when merge_queue.enabled=false")
	}

	if isMergeQueueRig(t.TempDir(), "doesnotexist") {
		t.Errorf("expected isMergeQueueRig=false for missing rig")
	}
}

func TestIsRefineryHeartbeatStale(t *testing.T) {
	tr := writeMergeQueueRig(t, "myrig", true)

	// No file → stale.
	stale, reason := isRefineryHeartbeatStale(tr, "myrig")
	if !stale {
		t.Errorf("expected stale=true when heartbeat file is missing")
	}
	if reason == "" {
		t.Errorf("expected non-empty reason when stale")
	}

	// Fresh heartbeat → not stale.
	writeRefineryHeartbeat(t, tr, "gt", 0)
	stale, _ = isRefineryHeartbeatStale(tr, "myrig")
	if stale {
		t.Errorf("expected stale=false on fresh heartbeat")
	}

	// Old heartbeat → stale.
	writeRefineryHeartbeat(t, tr, "gt", refineryHeartbeatStaleThreshold+time.Minute)
	stale, _ = isRefineryHeartbeatStale(tr, "myrig")
	if !stale {
		t.Errorf("expected stale=true when heartbeat is older than threshold")
	}
}

func TestGuardDirectPushOnMergeQueue(t *testing.T) {
	// Save and restore env across cases.
	prevPolecat := os.Getenv("GT_POLECAT")
	prevAllow := os.Getenv("GT_ALLOW_DIRECT_PUSH")
	prevReason := os.Getenv("GT_SKIP_PREPUSH_REASON")
	t.Cleanup(func() {
		_ = os.Setenv("GT_POLECAT", prevPolecat)
		_ = os.Setenv("GT_ALLOW_DIRECT_PUSH", prevAllow)
		_ = os.Setenv("GT_SKIP_PREPUSH_REASON", prevReason)
	})

	// Non-polecat caller: never blocked.
	_ = os.Unsetenv("GT_POLECAT")
	_ = os.Unsetenv("GT_ALLOW_DIRECT_PUSH")
	_ = os.Unsetenv("GT_SKIP_PREPUSH_REASON")
	tr := writeMergeQueueRig(t, "myrig", true)
	if err := guardDirectPushOnMergeQueue(tr, "myrig", "test"); err != nil {
		t.Errorf("non-polecat should not be blocked: %v", err)
	}

	// Polecat on non-merge-queue rig: not blocked.
	_ = os.Setenv("GT_POLECAT", "thunder")
	tr2 := writeMergeQueueRig(t, "myrig", false)
	if err := guardDirectPushOnMergeQueue(tr2, "myrig", "test"); err != nil {
		t.Errorf("polecat on non-mq rig should not be blocked: %v", err)
	}

	// Polecat on merge-queue rig: blocked.
	if err := guardDirectPushOnMergeQueue(tr, "myrig", "convoy direct"); err == nil {
		t.Errorf("polecat on mq rig should be blocked")
	}

	// Override without reason: still blocked (per gu-zy57 audit requirement).
	_ = os.Setenv("GT_ALLOW_DIRECT_PUSH", "1")
	_ = os.Unsetenv("GT_SKIP_PREPUSH_REASON")
	if err := guardDirectPushOnMergeQueue(tr, "myrig", "convoy direct"); err == nil {
		t.Errorf("override without reason should still be blocked")
	}

	// Override with reason: allowed.
	_ = os.Setenv("GT_SKIP_PREPUSH_REASON", "emergency: refinery down")
	if err := guardDirectPushOnMergeQueue(tr, "myrig", "convoy direct"); err != nil {
		t.Errorf("override with reason should be allowed: %v", err)
	}
}

// TestGuardDirectPushOnMergeQueue_StaleRefinery is the regression test
// called out in gu-8edz acceptance criterion #4: simulate a stale refinery
// heartbeat on a merge-queue rig, verify the guard refuses direct-push.
// (The label/note step is exercised by markAwaitingRefineryRecovery below;
// integrating both into a single end-to-end test against gt done would
// require full bd stub plumbing — out of scope for this regression check.)
func TestGuardDirectPushOnMergeQueue_StaleRefinery(t *testing.T) {
	prev := os.Getenv("GT_POLECAT")
	t.Cleanup(func() { _ = os.Setenv("GT_POLECAT", prev) })
	_ = os.Setenv("GT_POLECAT", "thunder")
	_ = os.Unsetenv("GT_ALLOW_DIRECT_PUSH")
	_ = os.Unsetenv("GT_SKIP_PREPUSH_REASON")

	tr := writeMergeQueueRig(t, "myrig", true)
	// Stale heartbeat (>5min old).
	writeRefineryHeartbeat(t, tr, "gt", refineryHeartbeatStaleThreshold+time.Minute)

	stale, _ := isRefineryHeartbeatStale(tr, "myrig")
	if !stale {
		t.Fatalf("test setup wrong: heartbeat should be stale")
	}
	if err := guardDirectPushOnMergeQueue(tr, "myrig", "convoy direct"); err == nil {
		t.Errorf("expected direct-push refused when refinery is stale on mq rig")
	}
}

func TestDirectPushOverrideAllowed(t *testing.T) {
	prevAllow := os.Getenv("GT_ALLOW_DIRECT_PUSH")
	prevReason := os.Getenv("GT_SKIP_PREPUSH_REASON")
	t.Cleanup(func() {
		_ = os.Setenv("GT_ALLOW_DIRECT_PUSH", prevAllow)
		_ = os.Setenv("GT_SKIP_PREPUSH_REASON", prevReason)
	})

	_ = os.Unsetenv("GT_ALLOW_DIRECT_PUSH")
	if allowed, _ := directPushOverrideAllowed(); allowed {
		t.Errorf("override should require GT_ALLOW_DIRECT_PUSH=1")
	}

	_ = os.Setenv("GT_ALLOW_DIRECT_PUSH", "1")
	_ = os.Unsetenv("GT_SKIP_PREPUSH_REASON")
	if allowed, _ := directPushOverrideAllowed(); allowed {
		t.Errorf("override should require GT_SKIP_PREPUSH_REASON")
	}

	_ = os.Setenv("GT_SKIP_PREPUSH_REASON", "test")
	allowed, reason := directPushOverrideAllowed()
	if !allowed {
		t.Errorf("override should be allowed with both env vars set")
	}
	if reason != "test" {
		t.Errorf("expected reason=test, got %q", reason)
	}
}

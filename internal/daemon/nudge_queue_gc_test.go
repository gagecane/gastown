package daemon

import (
	"testing"
	"time"
)

func TestNudgeQueueGCInterval(t *testing.T) {
	// Default
	if got := nudgeQueueGCInterval(nil); got != defaultNudgeQueueGCInterval {
		t.Errorf("expected default %v, got %v", defaultNudgeQueueGCInterval, got)
	}

	// Custom
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			NudgeQueueGC: &NudgeQueueGCConfig{
				Enabled:     true,
				IntervalStr: "10m",
			},
		},
	}
	if got := nudgeQueueGCInterval(config); got != 10*time.Minute {
		t.Errorf("expected 10m, got %v", got)
	}

	// Invalid falls back to default
	config.Patrols.NudgeQueueGC.IntervalStr = "nope"
	if got := nudgeQueueGCInterval(config); got != defaultNudgeQueueGCInterval {
		t.Errorf("expected default for invalid, got %v", got)
	}
}

// TestNudgeQueueGCDefaultEnabled proves that a town whose daemon.json
// predates this patrol still gets it: IsPatrolEnabled returns true even
// when the patrol stanza is absent. This is what makes the fix backstop
// existing live deployments without requiring a config rewrite.
func TestNudgeQueueGCDefaultEnabled(t *testing.T) {
	if !IsPatrolEnabled(nil, "nudge_queue_gc") {
		t.Error("nudge_queue_gc should default to enabled when config is nil")
	}

	emptyConfig := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !IsPatrolEnabled(emptyConfig, "nudge_queue_gc") {
		t.Error("nudge_queue_gc should default to enabled when stanza is missing")
	}

	disabled := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			NudgeQueueGC: &NudgeQueueGCConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(disabled, "nudge_queue_gc") {
		t.Error("nudge_queue_gc should respect explicit disable")
	}
}

// TestEnsureLifecycleDefaultsAddsNudgeQueueGC verifies the patrol gets
// populated into a config that was created before nudge_queue_gc existed.
func TestEnsureLifecycleDefaultsAddsNudgeQueueGC(t *testing.T) {
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{},
	}

	changed := EnsureLifecycleDefaults(config)
	if !changed {
		t.Fatal("expected changed=true when adding new patrol defaults")
	}
	if config.Patrols.NudgeQueueGC == nil {
		t.Fatal("NudgeQueueGC should be populated after EnsureLifecycleDefaults")
	}
	if !config.Patrols.NudgeQueueGC.Enabled {
		t.Error("NudgeQueueGC should be enabled by default")
	}
	if config.Patrols.NudgeQueueGC.IntervalStr != "5m" {
		t.Errorf("default interval = %q, want 5m", config.Patrols.NudgeQueueGC.IntervalStr)
	}
}

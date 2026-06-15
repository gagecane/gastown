package daemon

import (
	"testing"
	"time"
)

// --- Interval tests ---

func TestEscalateStaleInterval_Default(t *testing.T) {
	if got := escalateStaleInterval(nil); got != defaultEscalateStaleInterval {
		t.Errorf("expected default %v, got %v", defaultEscalateStaleInterval, got)
	}
}

func TestEscalateStaleInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			EscalateStale: &EscalateStaleConfig{Enabled: true, IntervalStr: "2h"},
		},
	}
	if got := escalateStaleInterval(cfg); got != 2*time.Hour {
		t.Errorf("expected 2h, got %v", got)
	}
}

func TestEscalateStaleInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			EscalateStale: &EscalateStaleConfig{Enabled: true, IntervalStr: "nonsense"},
		},
	}
	if got := escalateStaleInterval(cfg); got != defaultEscalateStaleInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

// --- IsPatrolEnabled tests (escalate_stale is DEFAULT-ON) ---

func TestIsPatrolEnabled_EscalateStale_NilConfigDefaultsOn(t *testing.T) {
	if !IsPatrolEnabled(nil, "escalate_stale") {
		t.Error("escalate_stale should default ON with nil config")
	}
}

func TestIsPatrolEnabled_EscalateStale_EmptyPatrolsDefaultsOn(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !IsPatrolEnabled(cfg, "escalate_stale") {
		t.Error("escalate_stale should default ON when not explicitly configured")
	}
}

func TestIsPatrolEnabled_EscalateStale_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			EscalateStale: &EscalateStaleConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "escalate_stale") {
		t.Error("escalate_stale should be disabled when explicitly set false")
	}
}

func TestIsPatrolEnabled_EscalateStale_ExplicitlyEnabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			EscalateStale: &EscalateStaleConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "escalate_stale") {
		t.Error("escalate_stale should be enabled when explicitly set true")
	}
}

// --- Lifecycle defaults ---

func TestEnsureLifecycleDefaults_PopulatesEscalateStale(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	changed := EnsureLifecycleDefaults(cfg)
	if !changed {
		t.Fatal("expected EnsureLifecycleDefaults to report a change")
	}
	if cfg.Patrols.EscalateStale == nil {
		t.Fatal("expected EscalateStale to be populated")
	}
	if !cfg.Patrols.EscalateStale.Enabled {
		t.Error("expected populated EscalateStale to be enabled")
	}
}

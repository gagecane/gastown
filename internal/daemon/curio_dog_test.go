package daemon

import (
	"testing"
	"time"
)

func TestCurioInterval_Default(t *testing.T) {
	if got := curioInterval(nil); got != defaultCurioInterval {
		t.Errorf("curioInterval(nil) = %v, want %v", got, defaultCurioInterval)
	}
}

func TestCurioInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{IntervalStr: "5m"}}}
	if got := curioInterval(cfg); got != 5*time.Minute {
		t.Errorf("curioInterval = %v, want 5m", got)
	}
}

func TestCurioInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{IntervalStr: "garbage"}}}
	if got := curioInterval(cfg); got != defaultCurioInterval {
		t.Errorf("invalid interval should fall back to default, got %v", got)
	}
}

func TestIsPatrolEnabled_Curio_NilConfig(t *testing.T) {
	if IsPatrolEnabled(nil, "curio") {
		t.Error("curio must be disabled (opt-in) when config is nil")
	}
}

func TestIsPatrolEnabled_Curio_EmptyPatrols(t *testing.T) {
	if IsPatrolEnabled(&DaemonPatrolConfig{Patrols: &PatrolsConfig{}}, "curio") {
		t.Error("curio must be disabled when its config block is absent")
	}
}

func TestIsPatrolEnabled_Curio_Enabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{Enabled: true}}}
	if !IsPatrolEnabled(cfg, "curio") {
		t.Error("curio should be enabled when explicitly set")
	}
}

func TestIsPatrolEnabled_Curio_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{Enabled: false}}}
	if IsPatrolEnabled(cfg, "curio") {
		t.Error("curio should be disabled when explicitly set to false")
	}
}

func TestCurioRateThresholds_NilConfigUsesCalibratedDefaults(t *testing.T) {
	got := curioRateThresholds(nil)
	if got["done"] != 1300 || got["mail"] != 900 || got["escalation"] != 150 {
		t.Errorf("nil config must yield calibrated defaults, got done=%d mail=%d escalation=%d",
			got["done"], got["mail"], got["escalation"])
	}
}

func TestCurioRateThresholds_AbsentBlockUsesCalibratedDefaults(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if got := curioRateThresholds(cfg)["done"]; got != 1300 {
		t.Errorf("absent curio block must yield calibrated default done=1300, got %d", got)
	}
}

func TestCurioRateThresholds_OverlaysPartialOverride(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{
		RateThresholds: map[string]int{"done": 2000},
	}}}
	got := curioRateThresholds(cfg)
	if got["done"] != 2000 {
		t.Errorf("override for done must apply, got %d", got["done"])
	}
	// Untouched series keep their calibrated defaults (partial override is safe).
	if got["mail"] != 900 {
		t.Errorf("unlisted series must keep calibrated default mail=900, got %d", got["mail"])
	}
}

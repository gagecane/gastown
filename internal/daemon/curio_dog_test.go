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

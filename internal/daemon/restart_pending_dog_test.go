package daemon

import (
	"strings"
	"testing"
	"time"
)

// --- Interval tests ---

func TestRestartPendingInterval_Default(t *testing.T) {
	if got := restartPendingInterval(nil); got != defaultRestartPendingInterval {
		t.Errorf("expected default %v, got %v", defaultRestartPendingInterval, got)
	}
}

func TestRestartPendingInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			RestartPending: &RestartPendingConfig{Enabled: true, IntervalStr: "2m"},
		},
	}
	if got := restartPendingInterval(cfg); got != 2*time.Minute {
		t.Errorf("expected 2m, got %v", got)
	}
}

func TestRestartPendingInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			RestartPending: &RestartPendingConfig{Enabled: true, IntervalStr: "nonsense"},
		},
	}
	if got := restartPendingInterval(cfg); got != defaultRestartPendingInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

// --- IsPatrolEnabled tests (restart_pending is DEFAULT-ON) ---

func TestIsPatrolEnabled_RestartPending_NilConfigDefaultsOn(t *testing.T) {
	// Unlike opt-in patrols, restart_pending must run out of the box.
	if !IsPatrolEnabled(nil, "restart_pending") {
		t.Error("restart_pending should default ON with nil config")
	}
}

func TestIsPatrolEnabled_RestartPending_EmptyPatrolsDefaultsOn(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !IsPatrolEnabled(cfg, "restart_pending") {
		t.Error("restart_pending should default ON when not explicitly configured")
	}
}

func TestIsPatrolEnabled_RestartPending_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			RestartPending: &RestartPendingConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "restart_pending") {
		t.Error("restart_pending should be disabled when explicitly set false")
	}
}

func TestIsPatrolEnabled_RestartPending_ExplicitlyEnabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			RestartPending: &RestartPendingConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "restart_pending") {
		t.Error("restart_pending should be enabled when explicitly set true")
	}
}

// --- Lifecycle defaults ---

func TestEnsureLifecycleDefaults_PopulatesRestartPending(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	changed := EnsureLifecycleDefaults(cfg)
	if !changed {
		t.Fatal("expected EnsureLifecycleDefaults to report a change")
	}
	if cfg.Patrols.RestartPending == nil {
		t.Fatal("expected RestartPending to be populated")
	}
	if !cfg.Patrols.RestartPending.Enabled {
		t.Error("default RestartPending should be Enabled")
	}
}

func TestDefaultLifecycleConfig_IncludesRestartPending(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	if cfg.Patrols == nil || cfg.Patrols.RestartPending == nil {
		t.Fatal("DefaultLifecycleConfig must include RestartPending")
	}
	if !cfg.Patrols.RestartPending.Enabled {
		t.Error("default RestartPending should be Enabled")
	}
	if cfg.Patrols.RestartPending.IntervalStr == "" {
		t.Error("default RestartPending should set an interval")
	}
}

// --- Escalation message builder ---

func TestBuildRestartEscalationMessage_IncludesStateAndAction(t *testing.T) {
	d := &Daemon{}
	b := restartPendingBead{
		ID:          "gu-test1",
		Title:       "daemon-restart-pending: gt binary upgraded to v1.2.3",
		Description: "rebuild-gt upgraded the on-disk binary. Daemon still on old code.",
	}
	msg := d.buildRestartEscalationMessage(b)

	// Acceptance (b): escalation must carry enough state for an agent to gate.
	for _, want := range []string{
		"gu-test1",                          // the pending bead id
		"upgraded to v1.2.3",                // which binary is pending (from title)
		"OLD in-memory image",               // why it matters
		"gt daemon stop && gt daemon start", // the gated action
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("escalation message missing %q.\nGot:\n%s", want, msg)
		}
	}

	// Acceptance (d): must NOT instruct an autonomous self-restart.
	if strings.Contains(strings.ToLower(msg), "auto-restart") &&
		!strings.Contains(strings.ToLower(msg), "not auto-restart") {
		t.Errorf("message should not advocate auto-restart; got:\n%s", msg)
	}
}

func TestBuildRestartEscalationMessage_FirstLineIsSingleLineTitle(t *testing.T) {
	// d.escalate uses the first line as the bd title, which must be single-line.
	d := &Daemon{}
	msg := d.buildRestartEscalationMessage(restartPendingBead{ID: "gu-x", Title: "t"})
	firstLine := msg
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		firstLine = msg[:idx]
	}
	if firstLine == "" {
		t.Error("first line (used as escalation title) must not be empty")
	}
	if strings.Contains(firstLine, "\n") {
		t.Error("first line must be single-line")
	}
}

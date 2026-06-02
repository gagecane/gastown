package daemon

import (
	"strings"
	"testing"
	"time"
)

// --- Interval tests ---

func TestSchedulerStuckInterval_Default(t *testing.T) {
	if got := schedulerStuckInterval(nil); got != defaultSchedulerStuckInterval {
		t.Errorf("expected default %v, got %v", defaultSchedulerStuckInterval, got)
	}
}

func TestSchedulerStuckInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			SchedulerStuck: &SchedulerStuckConfig{Enabled: true, IntervalStr: "2m"},
		},
	}
	if got := schedulerStuckInterval(cfg); got != 2*time.Minute {
		t.Errorf("expected 2m, got %v", got)
	}
}

func TestSchedulerStuckInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			SchedulerStuck: &SchedulerStuckConfig{Enabled: true, IntervalStr: "nonsense"},
		},
	}
	if got := schedulerStuckInterval(cfg); got != defaultSchedulerStuckInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

// --- IsPatrolEnabled tests (scheduler_stuck is DEFAULT-ON) ---

func TestIsPatrolEnabled_SchedulerStuck_NilConfigDefaultsOn(t *testing.T) {
	if !IsPatrolEnabled(nil, "scheduler_stuck") {
		t.Error("scheduler_stuck should default ON with nil config")
	}
}

func TestIsPatrolEnabled_SchedulerStuck_EmptyPatrolsDefaultsOn(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !IsPatrolEnabled(cfg, "scheduler_stuck") {
		t.Error("scheduler_stuck should default ON when not explicitly configured")
	}
}

func TestIsPatrolEnabled_SchedulerStuck_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			SchedulerStuck: &SchedulerStuckConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "scheduler_stuck") {
		t.Error("scheduler_stuck should be disabled when explicitly set false")
	}
}

func TestIsPatrolEnabled_SchedulerStuck_ExplicitlyEnabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			SchedulerStuck: &SchedulerStuckConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "scheduler_stuck") {
		t.Error("scheduler_stuck should be enabled when explicitly set true")
	}
}

// --- Lifecycle defaults ---

func TestEnsureLifecycleDefaults_PopulatesSchedulerStuck(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	changed := EnsureLifecycleDefaults(cfg)
	if !changed {
		t.Fatal("expected EnsureLifecycleDefaults to report a change")
	}
	if cfg.Patrols.SchedulerStuck == nil {
		t.Fatal("expected SchedulerStuck to be populated")
	}
	if !cfg.Patrols.SchedulerStuck.Enabled {
		t.Error("default SchedulerStuck should be Enabled")
	}
}

func TestDefaultLifecycleConfig_IncludesSchedulerStuck(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	if cfg.Patrols == nil || cfg.Patrols.SchedulerStuck == nil {
		t.Fatal("DefaultLifecycleConfig must include SchedulerStuck")
	}
	if !cfg.Patrols.SchedulerStuck.Enabled {
		t.Error("default SchedulerStuck should be Enabled")
	}
	if cfg.Patrols.SchedulerStuck.IntervalStr == "" {
		t.Error("default SchedulerStuck should set an interval")
	}
}

// --- Stall signature detection ---

// stalledSnapshot returns the canonical "classic stall" shape: pool mode, not
// paused, ready work queued, nothing working, free capacity available.
func stalledSnapshot() schedulerStuckSnapshot {
	s := schedulerStuckSnapshot{QueuedTotal: 15, QueuedReady: 15}
	s.Capacity.Max = 50
	s.Capacity.Working = 0
	s.Capacity.Free = 44
	return s
}

func TestIsStalled_ClassicStallSignature(t *testing.T) {
	if !stalledSnapshot().isStalled() {
		t.Error("ready>0 && working==0 && free>0 (pool mode, not paused) must be stalled")
	}
}

func TestIsStalled_PausedIsNotStalled(t *testing.T) {
	s := stalledSnapshot()
	s.Paused = true
	if s.isStalled() {
		t.Error("a paused scheduler is intentionally idle, not stalled")
	}
}

func TestIsStalled_DirectDispatchIsNotStalled(t *testing.T) {
	s := stalledSnapshot()
	s.Capacity.Max = -1 // direct-dispatch mode: no pool to wedge
	if s.isStalled() {
		t.Error("direct-dispatch mode (max<=0) has no pool and must not be stalled")
	}
}

func TestIsStalled_WorkingMeansDraining(t *testing.T) {
	s := stalledSnapshot()
	s.Capacity.Working = 3 // polecats are running — queue is draining
	if s.isStalled() {
		t.Error("working>0 means the scheduler is dispatching, not stalled")
	}
}

func TestIsStalled_NoReadyWorkIsNotStalled(t *testing.T) {
	s := stalledSnapshot()
	s.QueuedReady = 0 // nothing ready — idle, not wedged
	if s.isStalled() {
		t.Error("queued_ready==0 means there's nothing to dispatch, not stalled")
	}
}

func TestIsStalled_NoFreeCapacityIsNotStalled(t *testing.T) {
	s := stalledSnapshot()
	s.Capacity.Free = 0 // legitimately full — back-pressure, not a wedge
	if s.isStalled() {
		t.Error("free==0 means the pool is full, not stalled")
	}
}

// --- Escalation message builder ---

func TestBuildSchedulerStuckMessage_IncludesStateAndAction(t *testing.T) {
	d := &Daemon{}
	s := stalledSnapshot()
	s.LastDispatchAt = "2026-06-02T07:00:00Z"
	msg := d.buildSchedulerStuckMessage(s, 12*time.Minute)

	// Acceptance: escalation must carry enough state for an agent to diagnose.
	for _, want := range []string{
		"ready=15",                     // queue depth
		"working=0",                    // the smoking gun
		"free=44",                      // available capacity
		"12m0s",                        // sustained duration
		"last_dispatch",                // last-dispatch age for staleness check
		"gt scheduler status --json",   // a concrete diagnostic step
		"gt polecat list --all --json", // capacity-leak diagnosis
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("escalation message missing %q.\nGot:\n%s", want, msg)
		}
	}

	// Must NOT advocate a blind auto-remediation (escalate-not-automate).
	if strings.Contains(strings.ToLower(msg), "auto-remediated") &&
		!strings.Contains(strings.ToLower(msg), "not auto-remediated") {
		t.Errorf("message should not advocate auto-remediation; got:\n%s", msg)
	}
}

func TestBuildSchedulerStuckMessage_FirstLineIsSingleLineTitle(t *testing.T) {
	// d.escalate uses the first line as the bd title, which must be single-line.
	d := &Daemon{}
	msg := d.buildSchedulerStuckMessage(stalledSnapshot(), time.Minute)
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

func TestBuildSchedulerStuckMessage_NeverDispatched(t *testing.T) {
	d := &Daemon{}
	s := stalledSnapshot() // LastDispatchAt empty
	msg := d.buildSchedulerStuckMessage(s, time.Minute)
	if !strings.Contains(msg, "last_dispatch: never") {
		t.Errorf("empty last_dispatch should render as 'never'; got:\n%s", msg)
	}
}

// --- State persistence round-trip ---

func TestSchedulerStuckState_RoundTrip(t *testing.T) {
	path := schedulerStuckStateFile(t.TempDir())
	want := schedulerStuckState{
		FirstDetectedAt: time.Now().UTC().Truncate(time.Second),
		Escalated:       true,
	}
	if err := saveSchedulerStuckState(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadSchedulerStuckState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.FirstDetectedAt.Equal(want.FirstDetectedAt) {
		t.Errorf("FirstDetectedAt: got %v, want %v", got.FirstDetectedAt, want.FirstDetectedAt)
	}
	if got.Escalated != want.Escalated {
		t.Errorf("Escalated: got %v, want %v", got.Escalated, want.Escalated)
	}
}

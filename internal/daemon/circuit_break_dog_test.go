package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/events"
)

// --- Interval tests ---

func TestCircuitBreakInterval_Default(t *testing.T) {
	if got := circuitBreakInterval(nil); got != defaultCircuitBreakInterval {
		t.Errorf("expected default %v, got %v", defaultCircuitBreakInterval, got)
	}
}

func TestCircuitBreakInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CircuitBreak: &CircuitBreakConfig{Enabled: true, IntervalStr: "2m"},
		},
	}
	if got := circuitBreakInterval(cfg); got != 2*time.Minute {
		t.Errorf("expected 2m, got %v", got)
	}
}

func TestCircuitBreakInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CircuitBreak: &CircuitBreakConfig{Enabled: true, IntervalStr: "nonsense"},
		},
	}
	if got := circuitBreakInterval(cfg); got != defaultCircuitBreakInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

// --- IsPatrolEnabled tests (circuit_break is DEFAULT-ON) ---

func TestIsPatrolEnabled_CircuitBreak_NilConfigDefaultsOn(t *testing.T) {
	if !IsPatrolEnabled(nil, "circuit_break") {
		t.Error("circuit_break should default ON with nil config")
	}
}

func TestIsPatrolEnabled_CircuitBreak_EmptyPatrolsDefaultsOn(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !IsPatrolEnabled(cfg, "circuit_break") {
		t.Error("circuit_break should default ON when not explicitly configured")
	}
}

func TestIsPatrolEnabled_CircuitBreak_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CircuitBreak: &CircuitBreakConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "circuit_break") {
		t.Error("circuit_break should be disabled when explicitly set false")
	}
}

func TestIsPatrolEnabled_CircuitBreak_ExplicitlyEnabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CircuitBreak: &CircuitBreakConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "circuit_break") {
		t.Error("circuit_break should be enabled when explicitly set true")
	}
}

// --- Lifecycle defaults ---

func TestEnsureLifecycleDefaults_PopulatesCircuitBreak(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !EnsureLifecycleDefaults(cfg) {
		t.Fatal("expected EnsureLifecycleDefaults to report a change")
	}
	if cfg.Patrols.CircuitBreak == nil {
		t.Fatal("expected CircuitBreak to be populated")
	}
	if !cfg.Patrols.CircuitBreak.Enabled {
		t.Error("default CircuitBreak should be Enabled")
	}
}

func TestDefaultLifecycleConfig_IncludesCircuitBreak(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	if cfg.Patrols == nil || cfg.Patrols.CircuitBreak == nil {
		t.Fatal("DefaultLifecycleConfig must include CircuitBreak")
	}
	if !cfg.Patrols.CircuitBreak.Enabled {
		t.Error("default CircuitBreak should be Enabled")
	}
	if cfg.Patrols.CircuitBreak.IntervalStr == "" {
		t.Error("default CircuitBreak should set an interval")
	}
}

// --- Aggregation ---

func TestAggregateCircuitBreaks_CountsDistinctContexts(t *testing.T) {
	breaks := []events.CircuitBreakRecord{
		{WorkBeadID: "gu-aaa", ContextID: "ctx-1", Timestamp: "2026-06-02T01:00:00Z", TargetRig: "rigA", LastFailure: "early"},
		{WorkBeadID: "gu-aaa", ContextID: "ctx-2", Timestamp: "2026-06-02T02:00:00Z", TargetRig: "rigA", LastFailure: "mid"},
		{WorkBeadID: "gu-aaa", ContextID: "ctx-3", Timestamp: "2026-06-02T03:00:00Z", TargetRig: "rigA", LastFailure: "latest"},
		{WorkBeadID: "gu-bbb", ContextID: "ctx-9", Timestamp: "2026-06-02T01:30:00Z"},
	}
	got := aggregateCircuitBreaks(breaks)
	if len(got) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(got))
	}
	// Sorted count-desc: gu-aaa (3) first.
	if got[0].WorkBeadID != "gu-aaa" || got[0].Count != 3 {
		t.Errorf("expected gu-aaa count=3 first, got %+v", got[0])
	}
	// Most-recent metadata wins.
	if got[0].LastFailure != "latest" {
		t.Errorf("expected most-recent failure 'latest', got %q", got[0].LastFailure)
	}
	if got[0].LastBreakTS != "2026-06-02T03:00:00Z" {
		t.Errorf("expected most-recent ts, got %q", got[0].LastBreakTS)
	}
	if got[1].WorkBeadID != "gu-bbb" || got[1].Count != 1 {
		t.Errorf("expected gu-bbb count=1, got %+v", got[1])
	}
}

func TestAggregateCircuitBreaks_DedupsSameContext(t *testing.T) {
	// The same context logged twice (primary + backstop site) must count once.
	breaks := []events.CircuitBreakRecord{
		{WorkBeadID: "gu-aaa", ContextID: "ctx-1", Timestamp: "2026-06-02T01:00:00Z"},
		{WorkBeadID: "gu-aaa", ContextID: "ctx-1", Timestamp: "2026-06-02T01:00:01Z"},
	}
	got := aggregateCircuitBreaks(breaks)
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("expected single bead count=1 (deduped), got %+v", got)
	}
}

func TestAggregateCircuitBreaks_SkipsEmptyWorkBead(t *testing.T) {
	got := aggregateCircuitBreaks([]events.CircuitBreakRecord{{ContextID: "ctx-1"}})
	if len(got) != 0 {
		t.Errorf("records with empty work_bead_id must be skipped, got %+v", got)
	}
}

func TestAggregateCircuitBreaks_Empty(t *testing.T) {
	if got := aggregateCircuitBreaks(nil); len(got) != 0 {
		t.Errorf("expected empty result, got %+v", got)
	}
}

// --- Escalation message builder ---

func TestBuildCircuitBreakMessage_IncludesStateAndAction(t *testing.T) {
	d := &Daemon{}
	b := brokenBead{
		WorkBeadID:  "gu-r8b0q",
		Count:       4,
		TargetRig:   "gastown_upstream",
		LastFailure: "bead not found: gu-r8b0q",
		LastBreakTS: "2026-06-02T03:00:00Z",
	}
	msg := d.buildCircuitBreakMessage(b)

	for _, want := range []string{
		"gu-r8b0q",                 // the offending bead
		"4 times",                  // break count
		"gastown_upstream",         // target rig
		"bead not found: gu-r8b0q", // last failure for diagnosis
		"bd show gu-r8b0q",         // concrete diagnostic step
		"close it",                 // remediation guidance
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("escalation message missing %q.\nGot:\n%s", want, msg)
		}
	}

	// Escalate-not-automate: must declare it is NOT auto-remediated.
	if !strings.Contains(strings.ToLower(msg), "not auto-remediated") {
		t.Errorf("message must state it is NOT auto-remediated; got:\n%s", msg)
	}
}

func TestBuildCircuitBreakMessage_FirstLineIsSingleLineTitle(t *testing.T) {
	d := &Daemon{}
	msg := d.buildCircuitBreakMessage(brokenBead{WorkBeadID: "gu-x", Count: 3})
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

func TestBuildCircuitBreakMessage_OmitsEmptyOptionalFields(t *testing.T) {
	d := &Daemon{}
	// No target rig / last failure — those lines should be omitted, not blank.
	msg := d.buildCircuitBreakMessage(brokenBead{WorkBeadID: "gu-x", Count: 3})
	if strings.Contains(msg, "target_rig:") {
		t.Errorf("empty target_rig should be omitted; got:\n%s", msg)
	}
	if strings.Contains(msg, "last_failure:") {
		t.Errorf("empty last_failure should be omitted; got:\n%s", msg)
	}
}

// --- Escalation decision core ---

func TestCircuitBreakEscalations_BelowThresholdNoEscalation(t *testing.T) {
	agg := []brokenBead{{WorkBeadID: "gu-a", Count: circuitBreakThreshold - 1}}
	got, _, changed := circuitBreakEscalations(agg, circuitBreakState{EscalatedAtCount: map[string]int{}})
	if len(got) != 0 {
		t.Errorf("below threshold must not escalate, got %+v", got)
	}
	if changed {
		t.Error("no escalation and no prune should report unchanged")
	}
}

func TestCircuitBreakEscalations_FirstCrossEscalates(t *testing.T) {
	agg := []brokenBead{{WorkBeadID: "gu-a", Count: circuitBreakThreshold}}
	got, ns, changed := circuitBreakEscalations(agg, circuitBreakState{EscalatedAtCount: map[string]int{}})
	if len(got) != 1 || got[0].WorkBeadID != "gu-a" {
		t.Fatalf("first threshold cross must escalate, got %+v", got)
	}
	if !changed {
		t.Error("escalation must mark state changed")
	}
	if ns.EscalatedAtCount["gu-a"] != circuitBreakThreshold {
		t.Errorf("state should record escalated count, got %+v", ns.EscalatedAtCount)
	}
}

func TestCircuitBreakEscalations_SteadyStateDoesNotReescalate(t *testing.T) {
	agg := []brokenBead{{WorkBeadID: "gu-a", Count: 5}}
	prior := circuitBreakState{EscalatedAtCount: map[string]int{"gu-a": 5}}
	got, _, changed := circuitBreakEscalations(agg, prior)
	if len(got) != 0 {
		t.Errorf("unchanged count must not re-escalate, got %+v", got)
	}
	if changed {
		t.Error("steady state with same window should be unchanged")
	}
}

func TestCircuitBreakEscalations_GrowingCountReescalates(t *testing.T) {
	agg := []brokenBead{{WorkBeadID: "gu-a", Count: 6}}
	prior := circuitBreakState{EscalatedAtCount: map[string]int{"gu-a": 4}}
	got, ns, changed := circuitBreakEscalations(agg, prior)
	if len(got) != 1 {
		t.Fatalf("a worsening wedge (count grew) must re-escalate, got %+v", got)
	}
	if !changed || ns.EscalatedAtCount["gu-a"] != 6 {
		t.Errorf("state should update to new count, got %+v changed=%v", ns.EscalatedAtCount, changed)
	}
}

func TestCircuitBreakEscalations_PrunesOutOfWindowBeads(t *testing.T) {
	// gu-old escalated previously but is no longer in the window — prune it so
	// a recurrence re-arms.
	agg := []brokenBead{}
	prior := circuitBreakState{EscalatedAtCount: map[string]int{"gu-old": 3}}
	got, ns, changed := circuitBreakEscalations(agg, prior)
	if len(got) != 0 {
		t.Errorf("nothing in window should escalate, got %+v", got)
	}
	if !changed {
		t.Error("pruning an out-of-window entry should mark state changed")
	}
	if _, ok := ns.EscalatedAtCount["gu-old"]; ok {
		t.Errorf("out-of-window bead should be pruned, got %+v", ns.EscalatedAtCount)
	}
}

func TestCircuitBreakEscalations_RecurrenceAfterPruneReescalates(t *testing.T) {
	// gu-a dropped out (pruned), then reappears at threshold — should escalate
	// again because the pruned state no longer remembers it.
	pruned := circuitBreakState{EscalatedAtCount: map[string]int{}}
	agg := []brokenBead{{WorkBeadID: "gu-a", Count: circuitBreakThreshold}}
	got, _, _ := circuitBreakEscalations(agg, pruned)
	if len(got) != 1 {
		t.Errorf("a recurrence after prune must re-escalate, got %+v", got)
	}
}

// --- State persistence round-trip ---

func TestCircuitBreakState_RoundTrip(t *testing.T) {
	path := circuitBreakStateFile(t.TempDir())
	want := circuitBreakState{
		EscalatedAtCount: map[string]int{"gu-aaa": 3, "gu-bbb": 5},
	}
	if err := saveCircuitBreakState(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadCircuitBreakState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.EscalatedAtCount["gu-aaa"] != 3 || got.EscalatedAtCount["gu-bbb"] != 5 {
		t.Errorf("round-trip mismatch: got %+v", got.EscalatedAtCount)
	}
}

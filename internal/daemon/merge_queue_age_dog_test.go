package daemon

import (
	"strings"
	"testing"
	"time"
)

// --- Interval tests ---

func TestMergeQueueAgeInterval_Default(t *testing.T) {
	if got := mergeQueueAgeInterval(nil); got != defaultMergeQueueAgeInterval {
		t.Errorf("expected default %v, got %v", defaultMergeQueueAgeInterval, got)
	}
}

func TestMergeQueueAgeInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MergeQueueAge: &MergeQueueAgeConfig{Enabled: true, IntervalStr: "2m"},
		},
	}
	if got := mergeQueueAgeInterval(cfg); got != 2*time.Minute {
		t.Errorf("expected 2m, got %v", got)
	}
}

func TestMergeQueueAgeInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MergeQueueAge: &MergeQueueAgeConfig{Enabled: true, IntervalStr: "nonsense"},
		},
	}
	if got := mergeQueueAgeInterval(cfg); got != defaultMergeQueueAgeInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

// --- IsPatrolEnabled tests (merge_queue_age is DEFAULT-ON) ---

func TestIsPatrolEnabled_MergeQueueAge_NilConfigDefaultsOn(t *testing.T) {
	if !IsPatrolEnabled(nil, "merge_queue_age") {
		t.Error("merge_queue_age should default ON with nil config")
	}
}

func TestIsPatrolEnabled_MergeQueueAge_EmptyPatrolsDefaultsOn(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !IsPatrolEnabled(cfg, "merge_queue_age") {
		t.Error("merge_queue_age should default ON when not explicitly configured")
	}
}

func TestIsPatrolEnabled_MergeQueueAge_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MergeQueueAge: &MergeQueueAgeConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "merge_queue_age") {
		t.Error("merge_queue_age should be disabled when explicitly set false")
	}
}

func TestIsPatrolEnabled_MergeQueueAge_ExplicitlyEnabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MergeQueueAge: &MergeQueueAgeConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "merge_queue_age") {
		t.Error("merge_queue_age should be enabled when explicitly set true")
	}
}

// --- Lifecycle defaults ---

func TestEnsureLifecycleDefaults_PopulatesMergeQueueAge(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	changed := EnsureLifecycleDefaults(cfg)
	if !changed {
		t.Fatal("expected EnsureLifecycleDefaults to report a change")
	}
	if cfg.Patrols.MergeQueueAge == nil {
		t.Fatal("expected MergeQueueAge to be populated")
	}
	if !cfg.Patrols.MergeQueueAge.Enabled {
		t.Error("expected populated MergeQueueAge to be enabled")
	}
}

func TestDefaultLifecycleConfig_IncludesMergeQueueAge(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	if cfg.Patrols.MergeQueueAge == nil {
		t.Fatal("expected DefaultLifecycleConfig to include MergeQueueAge")
	}
	if !cfg.Patrols.MergeQueueAge.Enabled {
		t.Error("expected default MergeQueueAge to be enabled")
	}
}

// --- evaluateMergeQueueRig decision core ---

const (
	testRig   = "gastown_upstream"
	oldThresh = 60 * time.Minute
	hfThresh  = 30 * time.Minute
	hfMin     = 2
)

// mkEntry builds an mqEntry created `age` before `now` with the given score.
func mkEntry(id string, now time.Time, age time.Duration, score float64) mqEntry {
	return mqEntry{ID: id, CreatedAt: now.Add(-age), Score: score}
}

func hasCondition(escs []mqEscalation, cond string) bool {
	for _, e := range escs {
		if e.Condition == cond {
			return true
		}
	}
	return false
}

func TestEvaluateMergeQueueRig_EmptyQueueClearsState(t *testing.T) {
	now := time.Now().UTC()
	prev := &mergeQueueRigState{HeadMRID: "mr-1", HeadSince: now.Add(-time.Hour), HeadEscalated: true}
	escs, state := evaluateMergeQueueRig(testRig, nil, now, prev, oldThresh, hfThresh, hfMin)
	if len(escs) != 0 {
		t.Errorf("empty queue should not escalate, got %d", len(escs))
	}
	if state != nil {
		t.Errorf("empty queue should clear state (nil), got %+v", state)
	}
}

func TestEvaluateMergeQueueRig_OldestAgingEscalates(t *testing.T) {
	now := time.Now().UTC()
	entries := []mqEntry{
		mkEntry("mr-old", now, 90*time.Minute, 100),
		mkEntry("mr-new", now, 5*time.Minute, 50),
	}
	escs, state := evaluateMergeQueueRig(testRig, entries, now, nil, oldThresh, hfThresh, hfMin)
	if !hasCondition(escs, "oldest") {
		t.Fatal("expected oldest-aging escalation")
	}
	if state.OldestEscalatedID != "mr-old" {
		t.Errorf("expected OldestEscalatedID=mr-old, got %q", state.OldestEscalatedID)
	}
}

func TestEvaluateMergeQueueRig_OldestUnderThresholdNoEscalate(t *testing.T) {
	now := time.Now().UTC()
	entries := []mqEntry{
		mkEntry("mr-1", now, 10*time.Minute, 100),
		mkEntry("mr-2", now, 5*time.Minute, 50),
	}
	escs, _ := evaluateMergeQueueRig(testRig, entries, now, nil, oldThresh, hfThresh, hfMin)
	if hasCondition(escs, "oldest") {
		t.Error("young queue should not escalate oldest")
	}
}

func TestEvaluateMergeQueueRig_OldestDedupsUntilLanded(t *testing.T) {
	now := time.Now().UTC()
	entries := []mqEntry{mkEntry("mr-old", now, 90*time.Minute, 100), mkEntry("mr-b", now, 1*time.Minute, 90)}
	escs1, state1 := evaluateMergeQueueRig(testRig, entries, now, nil, oldThresh, hfThresh, hfMin)
	if !hasCondition(escs1, "oldest") {
		t.Fatal("first pass should escalate oldest")
	}
	// Same oldest still present next tick — must NOT re-escalate.
	escs2, state2 := evaluateMergeQueueRig(testRig, entries, now.Add(5*time.Minute), state1, oldThresh, hfThresh, hfMin)
	if hasCondition(escs2, "oldest") {
		t.Error("same aged oldest should not re-escalate")
	}
	if state2.OldestEscalatedID != "mr-old" {
		t.Errorf("expected OldestEscalatedID to persist, got %q", state2.OldestEscalatedID)
	}
}

func TestEvaluateMergeQueueRig_OldestReArmsAfterLanding(t *testing.T) {
	now := time.Now().UTC()
	entries := []mqEntry{mkEntry("mr-old", now, 90*time.Minute, 100), mkEntry("mr-b", now, 1*time.Minute, 90)}
	_, state1 := evaluateMergeQueueRig(testRig, entries, now, nil, oldThresh, hfThresh, hfMin)

	// mr-old landed; the new oldest (mr-b) is young → re-arm.
	youngOnly := []mqEntry{mkEntry("mr-b", now, 2*time.Minute, 90)}
	_, state2 := evaluateMergeQueueRig(testRig, youngOnly, now.Add(5*time.Minute), state1, oldThresh, hfThresh, hfMin)
	if state2.OldestEscalatedID != "" {
		t.Errorf("expected oldest marker re-armed (empty), got %q", state2.OldestEscalatedID)
	}

	// A new MR ages out later → escalates again.
	now2 := now.Add(2 * time.Hour)
	agedAgain := []mqEntry{mkEntry("mr-b", now2, 90*time.Minute, 90), mkEntry("mr-c", now2, 1*time.Minute, 80)}
	escs3, _ := evaluateMergeQueueRig(testRig, agedAgain, now2, state2, oldThresh, hfThresh, hfMin)
	if !hasCondition(escs3, "oldest") {
		t.Error("expected re-escalation after re-arm")
	}
}

func TestEvaluateMergeQueueRig_HeadFrozenEscalatesAfterThreshold(t *testing.T) {
	now := time.Now().UTC()
	// Two MRs; head = highest score. All young so oldest does not fire.
	entries := []mqEntry{
		mkEntry("mr-head", now, 5*time.Minute, 200),
		mkEntry("mr-2", now, 4*time.Minute, 100),
	}
	// First observation: records head, starts timer, no escalation.
	escs1, state1 := evaluateMergeQueueRig(testRig, entries, now, nil, oldThresh, hfThresh, hfMin)
	if hasCondition(escs1, "head") {
		t.Fatal("first observation of head should not escalate")
	}
	if state1.HeadMRID != "mr-head" {
		t.Fatalf("expected head mr-head, got %q", state1.HeadMRID)
	}
	// Same head, past the frozen threshold → escalate.
	later := now.Add(hfThresh + time.Minute)
	escs2, state2 := evaluateMergeQueueRig(testRig, entries, later, state1, oldThresh, hfThresh, hfMin)
	if !hasCondition(escs2, "head") {
		t.Fatal("expected head-frozen escalation past threshold")
	}
	if !state2.HeadEscalated {
		t.Error("expected HeadEscalated=true")
	}
	// Next tick still frozen → must NOT re-escalate.
	escs3, _ := evaluateMergeQueueRig(testRig, entries, later.Add(5*time.Minute), state2, oldThresh, hfThresh, hfMin)
	if hasCondition(escs3, "head") {
		t.Error("frozen head should not re-escalate after first escalation")
	}
}

func TestEvaluateMergeQueueRig_AdvancingHeadNeverFreezes(t *testing.T) {
	now := time.Now().UTC()
	// Deep queue, but the head advances every cycle (busy, not stuck).
	state := (*mergeQueueRigState)(nil)
	for i := 0; i < 10; i++ {
		tick := now.Add(time.Duration(i) * 10 * time.Minute)
		// Head MR id changes each cycle; depth stays >= min.
		headID := "mr-head-" + string(rune('a'+i))
		entries := []mqEntry{
			mkEntry(headID, tick, 2*time.Minute, 300),
			mkEntry("mr-filler1", tick, 1*time.Minute, 100),
			mkEntry("mr-filler2", tick, 1*time.Minute, 90),
		}
		var escs []mqEscalation
		escs, state = evaluateMergeQueueRig(testRig, entries, tick, state, oldThresh, hfThresh, hfMin)
		if hasCondition(escs, "head") {
			t.Fatalf("advancing head should never escalate (cycle %d)", i)
		}
	}
}

func TestEvaluateMergeQueueRig_FrozenHeadBelowMinDepthNoEscalate(t *testing.T) {
	now := time.Now().UTC()
	// Lone MR frozen at head, but depth=1 < min depth 2 → head check suppressed.
	entries := []mqEntry{mkEntry("mr-lone", now, 5*time.Minute, 200)}
	_, state1 := evaluateMergeQueueRig(testRig, entries, now, nil, oldThresh, hfThresh, hfMin)
	later := now.Add(hfThresh + time.Minute)
	escs, _ := evaluateMergeQueueRig(testRig, entries, later, state1, oldThresh, hfThresh, hfMin)
	if hasCondition(escs, "head") {
		t.Error("lone MR below min depth should not fire head-frozen")
	}
}

func TestEvaluateMergeQueueRig_HeadChangeResetsTimer(t *testing.T) {
	now := time.Now().UTC()
	entries1 := []mqEntry{mkEntry("mr-a", now, 5*time.Minute, 200), mkEntry("mr-b", now, 4*time.Minute, 100)}
	_, state1 := evaluateMergeQueueRig(testRig, entries1, now, nil, oldThresh, hfThresh, hfMin)

	// Head changes to mr-b before threshold elapses; timer must reset.
	mid := now.Add(20 * time.Minute)
	entries2 := []mqEntry{mkEntry("mr-b", now, 24*time.Minute, 300), mkEntry("mr-c", mid, 1*time.Minute, 100)}
	_, state2 := evaluateMergeQueueRig(testRig, entries2, mid, state1, oldThresh, hfThresh, hfMin)
	if state2.HeadMRID != "mr-b" {
		t.Fatalf("expected head to advance to mr-b, got %q", state2.HeadMRID)
	}
	if !state2.HeadSince.Equal(mid) {
		t.Errorf("expected HeadSince reset to %v, got %v", mid, state2.HeadSince)
	}
	if state2.HeadEscalated {
		t.Error("expected HeadEscalated reset to false on head change")
	}
}

// --- Message construction ---

func TestBuildMergeQueueHeadFrozenMessage_FirstLineSingleLine(t *testing.T) {
	now := time.Now().UTC()
	head := mkEntry("mr-head", now, 40*time.Minute, 200)
	oldest := mkEntry("mr-old", now, 50*time.Minute, 100)
	msg := buildMergeQueueHeadFrozenMessage(testRig, head, 5, 35*time.Minute, oldest, now)
	firstLine := msg
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		firstLine = msg[:idx]
	}
	if strings.Contains(firstLine, "\n") {
		t.Error("first line must be a single line (bd title requirement)")
	}
	for _, want := range []string{"mr-head", testRig, "not advancing"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected message to contain %q", want)
		}
	}
}

func TestBuildMergeQueueOldestMessage_IncludesStateAndAction(t *testing.T) {
	now := time.Now().UTC()
	oldest := mkEntry("mr-old", now, 75*time.Minute, 100)
	msg := buildMergeQueueOldestMessage(testRig, oldest, 8, 75*time.Minute)
	for _, want := range []string{"mr-old", testRig, "gt mq list", "gt mq status"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected message to contain %q", want)
		}
	}
}

// --- State round-trip ---

func TestMergeQueueAgeState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/merge-queue-age.json"
	now := time.Now().UTC().Truncate(time.Second)
	in := mergeQueueAgeState{Rigs: map[string]*mergeQueueRigState{
		testRig: {HeadMRID: "mr-1", HeadSince: now, HeadEscalated: true, OldestEscalatedID: "mr-0"},
	}}
	if err := saveMergeQueueAgeState(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := loadMergeQueueAgeState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := out.Rigs[testRig]
	if got == nil || got.HeadMRID != "mr-1" || !got.HeadEscalated || got.OldestEscalatedID != "mr-0" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !got.HeadSince.Equal(now) {
		t.Errorf("HeadSince round-trip mismatch: got %v want %v", got.HeadSince, now)
	}
}

// --- Time parsing ---

func TestParseMRTime(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"2026-06-12T10:00:00Z", true},
		{"2026-06-12T10:00:00+00:00", true},
		{"", false},
		{"not-a-time", false},
	}
	for _, c := range cases {
		_, ok := parseMRTime(c.in)
		if ok != c.ok {
			t.Errorf("parseMRTime(%q): got ok=%v want %v", c.in, ok, c.ok)
		}
	}
}

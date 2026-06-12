package curio

import "testing"

// fired returns the set of rule IDs that produced at least one candidate.
func fired(cands []Candidate) map[string]bool {
	f := map[string]bool{}
	for _, c := range cands {
		f[c.RuleID] = true
	}
	return f
}

// --- Rule (a): bead_merged_not_landed ---

func TestMergedNotLanded_FiresOnOrphanCommit(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, Beads: []BeadRecord{
		{ID: "b1", Rig: "r", CloseReason: "merged", Commit: "x", CommitInMainAncestry: false, FiledBy: "polecat"},
	}}
	cands := mergedNotLandedRule{}.Eval(in)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Target != "b1" || cands[0].Rig != "r" {
		t.Errorf("unexpected candidate target/rig: %+v", cands[0])
	}
}

func TestMergedNotLanded_FiresOnMergedNoCommit(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, Beads: []BeadRecord{
		{ID: "b1", CloseReason: "merged", Commit: "", FiledBy: "polecat"},
	}}
	if len(mergedNotLandedRule{}.Eval(in)) != 1 {
		t.Error("expected fire on merged-with-empty-commit")
	}
}

func TestMergedNotLanded_SilentWhenLanded(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, Beads: []BeadRecord{
		{ID: "b1", CloseReason: "merged", Commit: "x", CommitInMainAncestry: true, FiledBy: "polecat"},
	}}
	if len(mergedNotLandedRule{}.Eval(in)) != 0 {
		t.Error("must not fire when commit is in main ancestry")
	}
}

func TestMergedNotLanded_SilentForNonMerged(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, Beads: []BeadRecord{
		{ID: "b1", CloseReason: "completed", Commit: "", CommitInMainAncestry: false, FiledBy: "polecat"},
	}}
	if len(mergedNotLandedRule{}.Eval(in)) != 0 {
		t.Error("must not fire for non-merged close reasons")
	}
}

func TestMergedNotLanded_LoopBreaker(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, Beads: []BeadRecord{
		{ID: "b1", CloseReason: "merged", Commit: "", FiledBy: "curio"},
	}}
	if len(mergedNotLandedRule{}.Eval(in)) != 0 {
		t.Error("must exclude curio-filed beads (loop-breaker)")
	}
}

// --- Rule (b): kill_signal_near_dolt ---

func TestKillSignal_FiresNearDoltPID(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, LogLines: []LogLine{
		{Source: "reaper", Text: "kill -QUIT dolt pid 1", NearDoltPID: true, FiledBy: "reaper"},
	}}
	if len(killSignalNearDoltRule{}.Eval(in)) != 1 {
		t.Error("expected fire on kill signal near dolt pid")
	}
}

func TestKillSignal_SilentWhenNotNearDolt(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, LogLines: []LogLine{
		{Source: "deacon", Text: "ok", NearDoltPID: false, FiledBy: "deacon"},
	}}
	if len(killSignalNearDoltRule{}.Eval(in)) != 0 {
		t.Error("must not fire when not near a dolt pid")
	}
}

func TestKillSignal_LoopBreaker(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, LogLines: []LogLine{
		{Source: "curio", Text: "kill dolt pid 1", NearDoltPID: true, FiledBy: "curio"},
	}}
	if len(killSignalNearDoltRule{}.Eval(in)) != 0 {
		t.Error("must exclude curio-filed log lines (loop-breaker)")
	}
}

func TestKillSignal_DistinctLinesDistinctCandidates(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, LogLines: []LogLine{
		{Source: "reaper", Text: "kill A", NearDoltPID: true, FiledBy: "reaper"},
		{Source: "reaper", Text: "kill B", NearDoltPID: true, FiledBy: "reaper"},
	}}
	cands := killSignalNearDoltRule{}.Eval(in)
	if len(cands) != 2 || cands[0].Fingerprint == cands[1].Fingerprint {
		t.Errorf("expected 2 distinct candidates, got %d", len(cands))
	}
}

// --- Rule (c): alarm_rate_spike ---

func TestRateSpike_StuckAgentThresholdZero(t *testing.T) {
	// dispatch.stuck_agent stays a deliberate floor at 0 (never emitted in the
	// corpus): any non-zero count is a genuinely novel critical event and fires.
	r := rateSpikeRule{thresholds: rateThresholds}
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "dispatch.stuck_agent", Observed: 1, FiledBy: "deacon"},
	}}
	if len(r.Eval(in)) != 1 {
		t.Error("dispatch.stuck_agent floor must fire on any non-zero count")
	}
}

func TestRateSpike_StuckAgentZeroSilent(t *testing.T) {
	r := rateSpikeRule{thresholds: rateThresholds}
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "dispatch.stuck_agent", Observed: 0, FiledBy: "deacon"},
	}}
	if len(r.Eval(in)) != 0 {
		t.Error("dispatch.stuck_agent at zero must be silent")
	}
}

// calibratedBaselines pairs each calibrated series with the observed p95/max
// from the gc-e2uvyr.2 live baseline. A count at the busy-day high water mark
// must stay QUIET; a count above the calibrated ceiling must FIRE. This is the
// core acceptance criterion: the rate rule no longer fires on normal throughput.
func TestRateSpike_CalibratedThresholds_QuietAtBaseline_FireAboveCeiling(t *testing.T) {
	r := rateSpikeRule{thresholds: rateThresholds}
	cases := []struct {
		series      string
		observedMax int // observed historical max (busy day) — must stay quiet
		threshold   int // calibrated ceiling
	}{
		{"sling", 310, 350},
		{"done", 1183, 1300},
		{"mail", 839, 900},
		{"escalation", 120, 150},
		{"sched_fail", 27, 30},
	}
	for _, c := range cases {
		// At the observed busy-day maximum: must NOT fire.
		quiet := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
			{Series: c.series, Observed: c.observedMax, FiledBy: "gt"},
		}}
		if got := len(r.Eval(quiet)); got != 0 {
			t.Errorf("%s: observed max %d must stay quiet under threshold %d, got %d candidates",
				c.series, c.observedMax, c.threshold, got)
		}
		// At exactly the threshold: must NOT fire (strict >).
		atThr := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
			{Series: c.series, Observed: c.threshold, FiledBy: "gt"},
		}}
		if got := len(r.Eval(atThr)); got != 0 {
			t.Errorf("%s: at-threshold %d must not fire (strict >), got %d candidates",
				c.series, c.threshold, got)
		}
		// One above the threshold: must fire.
		flood := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
			{Series: c.series, Observed: c.threshold + 1, FiledBy: "gt"},
		}}
		cands := r.Eval(flood)
		if len(cands) != 1 || cands[0].Observed != c.threshold+1 {
			t.Errorf("%s: count %d above ceiling %d must fire once, got %+v",
				c.series, c.threshold+1, c.threshold, cands)
		}
	}
}

func TestRateSpike_UnknownSeriesSilent(t *testing.T) {
	r := rateSpikeRule{thresholds: rateThresholds}
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "totally.unknown", Observed: 99999, FiledBy: "x"},
	}}
	if len(r.Eval(in)) != 0 {
		t.Error("unbaselined series must not fire")
	}
}

func TestRateSpike_LoopBreaker(t *testing.T) {
	r := rateSpikeRule{thresholds: rateThresholds}
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "bead.open", Observed: 9999, FiledBy: "curio"},
	}}
	if len(r.Eval(in)) != 0 {
		t.Error("must exclude curio-filed event counts (loop-breaker)")
	}
}

func TestDefaultRateThresholds_ReturnsCalibratedCopy(t *testing.T) {
	got := DefaultRateThresholds()
	want := map[string]int{
		"dispatch.stuck_agent": 0,
		"escalation":           150,
		"sched_fail":           30,
		"sling":                350,
		"done":                 1300,
		"mail":                 900,
		"bead.open":            150,
		"bead.close":           150,
	}
	if len(got) != len(want) {
		t.Fatalf("threshold count = %d, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("threshold %q = %d, want %d", k, got[k], v)
		}
	}
	// Mutating the returned map must not corrupt the package defaults.
	got["done"] = 1
	if rateThresholds["done"] != 1300 {
		t.Error("DefaultRateThresholds must return a copy, not the shared map")
	}
}

// TestDefaultRulesWithThresholds_NilFallsBackToCalibratedDefaults proves the
// config-absent fallback: a nil/empty override map yields the safe calibrated
// ceilings, so a missing daemon.json can never silence or lower the rate rule.
func TestDefaultRulesWithThresholds_NilFallsBackToCalibratedDefaults(t *testing.T) {
	for name, thresholds := range map[string]map[string]int{
		"nil":   nil,
		"empty": {},
	} {
		rules := DefaultRulesWithThresholds(thresholds)
		// done at 1300 (calibrated default) must stay quiet; 1301 must fire.
		quiet := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
			{Series: "done", Observed: 1300, FiledBy: "gt"},
		}}
		fire := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
			{Series: "done", Observed: 1301, FiledBy: "gt"},
		}}
		if got := len(Evaluate(rules, quiet)); got != 0 {
			t.Errorf("%s override: done=1300 must stay quiet under calibrated default, got %d", name, got)
		}
		if got := len(Evaluate(rules, fire)); got != 1 {
			t.Errorf("%s override: done=1301 must fire on calibrated default, got %d", name, got)
		}
	}
}

// TestDefaultRulesWithThresholds_OverrideApplies proves an operator override is
// honored: a lower ceiling on a single series fires where the default would not.
func TestDefaultRulesWithThresholds_OverrideApplies(t *testing.T) {
	rules := DefaultRulesWithThresholds(map[string]int{"done": 500})
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "done", Observed: 600, FiledBy: "gt"}, // below default 1300, above override 500
	}}
	if got := len(Evaluate(rules, in)); got != 1 {
		t.Errorf("override done=500 must fire on observed=600, got %d candidates", got)
	}
}

// --- Rule (d): dead_owner_admission ---

func TestDeadOwner_FiresOnDeadPID(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, Admissions: []AdmissionRecord{
		{ID: "a1", PID: 1, Rig: "r", OwnerAlive: false, FiledBy: "scheduler"},
	}}
	cands := deadOwnerAdmissionRule{}.Eval(in)
	if len(cands) != 1 || cands[0].Target != "a1" {
		t.Errorf("expected 1 candidate for a1, got %+v", cands)
	}
}

func TestDeadOwner_SilentWhenAlive(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, Admissions: []AdmissionRecord{
		{ID: "a1", PID: 1, OwnerAlive: true, FiledBy: "scheduler"},
	}}
	if len(deadOwnerAdmissionRule{}.Eval(in)) != 0 {
		t.Error("must not fire for live-owner reservations")
	}
}

func TestDeadOwner_LoopBreaker(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, Admissions: []AdmissionRecord{
		{ID: "a1", PID: 1, OwnerAlive: false, FiledBy: "curio"},
	}}
	if len(deadOwnerAdmissionRule{}.Eval(in)) != 0 {
		t.Error("must exclude curio-filed admissions (loop-breaker)")
	}
}

// --- Engine ---

func TestEvaluate_DedupByFingerprint(t *testing.T) {
	// Two beads with the same ID would produce the same (ruleID,target) fp.
	in := Input{Window: Window{ID: "w"}, Beads: []BeadRecord{
		{ID: "dup", CloseReason: "merged", Commit: "", FiledBy: "polecat"},
		{ID: "dup", CloseReason: "merged", Commit: "", FiledBy: "polecat"},
	}}
	cands := Evaluate(DefaultRules(), in)
	if len(cands) != 1 {
		t.Errorf("expected dedup to 1 candidate, got %d", len(cands))
	}
}

func TestEvaluate_DeterministicOrder(t *testing.T) {
	in := Input{Window: Window{ID: "w"},
		Beads:      []BeadRecord{{ID: "b", CloseReason: "merged", Commit: "", FiledBy: "p"}},
		Admissions: []AdmissionRecord{{ID: "a", OwnerAlive: false, FiledBy: "s"}},
	}
	first := Evaluate(DefaultRules(), in)
	for i := 0; i < 5; i++ {
		got := Evaluate(DefaultRules(), in)
		if len(got) != len(first) {
			t.Fatal("non-deterministic candidate count")
		}
		for j := range got {
			if got[j].Fingerprint != first[j].Fingerprint {
				t.Fatal("non-deterministic candidate order")
			}
		}
	}
}

func TestEvaluate_EmptyInput(t *testing.T) {
	if len(Evaluate(DefaultRules(), Input{Window: Window{ID: "w"}})) != 0 {
		t.Error("empty input must yield no candidates")
	}
}

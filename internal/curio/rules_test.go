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

func TestRateSpike_RareEventThresholdZero(t *testing.T) {
	r := rateSpikeRule{thresholds: rateThresholds}
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "dispatch.stuck_agent", Observed: 1, FiledBy: "deacon"},
	}}
	if len(r.Eval(in)) != 1 {
		t.Error("rare-event series must fire on any non-zero count")
	}
}

func TestRateSpike_RareEventZeroSilent(t *testing.T) {
	r := rateSpikeRule{thresholds: rateThresholds}
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "escalation", Observed: 0, FiledBy: "various"},
	}}
	if len(r.Eval(in)) != 0 {
		t.Error("rare-event series at zero must be silent")
	}
}

func TestRateSpike_NormalTrafficBelowThreshold(t *testing.T) {
	r := rateSpikeRule{thresholds: rateThresholds}
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "sling", Observed: 300, FiledBy: "scheduler"}, // == threshold, not >
		{Series: "done", Observed: 400, FiledBy: "refinery"},
	}}
	if len(r.Eval(in)) != 0 {
		t.Error("at-threshold traffic must not fire (strict >)")
	}
}

func TestRateSpike_FloodFires(t *testing.T) {
	r := rateSpikeRule{thresholds: rateThresholds}
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "sling", Observed: 301, FiledBy: "scheduler"},
	}}
	cands := r.Eval(in)
	if len(cands) != 1 || cands[0].Observed != 301 {
		t.Errorf("expected flood candidate with observed=301, got %+v", cands)
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

package curio

import (
	"math"
	"testing"
)

func TestEWMADetector_WarmupSuppression(t *testing.T) {
	d := NewEWMADetector()

	// Feed fewer than warmupCycles observations — detector should not fire.
	for i := 0; i < warmupCycles-1; i++ {
		d.Observe([]SeriesCount{{Series: "sling", Observed: 100, FiledBy: "scheduler"}})
	}

	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "sling", Observed: 9999, FiledBy: "scheduler"},
	}}
	cands := d.Eval(in)
	if len(cands) != 0 {
		t.Errorf("expected no candidates during warmup, got %d", len(cands))
	}
}

func TestEWMADetector_FiresOnSpike(t *testing.T) {
	d := NewEWMADetector()

	// Feed stable baseline through warmup.
	for i := 0; i < warmupCycles+5; i++ {
		d.Observe([]SeriesCount{{Series: "sling", Observed: 100, FiledBy: "scheduler"}})
	}

	// Now spike massively.
	d.Observe([]SeriesCount{{Series: "sling", Observed: 1000, FiledBy: "scheduler"}})

	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "sling", Observed: 1000, FiledBy: "scheduler"},
	}}
	cands := d.Eval(in)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate on spike, got %d", len(cands))
	}
	if cands[0].RuleID != "ewma_anomaly" {
		t.Errorf("unexpected rule ID: %s", cands[0].RuleID)
	}
	if cands[0].Series != "sling" {
		t.Errorf("unexpected series: %s", cands[0].Series)
	}
	if cands[0].EWMA == 0 {
		t.Error("expected non-zero EWMA on candidate")
	}
	if cands[0].Deviation == 0 {
		t.Error("expected non-zero Deviation on candidate")
	}
}

func TestEWMADetector_SilentOnNormalTraffic(t *testing.T) {
	d := NewEWMADetector()

	// Feed stable baseline.
	for i := 0; i < warmupCycles+5; i++ {
		d.Observe([]SeriesCount{{Series: "done", Observed: 50, FiledBy: "refinery"}})
	}

	// Observation at same level should not fire.
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "done", Observed: 50, FiledBy: "refinery"},
	}}
	cands := d.Eval(in)
	if len(cands) != 0 {
		t.Errorf("expected no candidates on normal traffic, got %d", len(cands))
	}
}

func TestEWMADetector_SilentOnModerateFluctuation(t *testing.T) {
	d := NewEWMADetector()

	// Feed fluctuating baseline to build deviation.
	observations := []int{100, 110, 90, 105, 95, 100, 108, 92, 103, 97}
	for _, obs := range observations {
		d.Observe([]SeriesCount{{Series: "mail", Observed: obs, FiledBy: "gt"}})
	}

	// A value within normal fluctuation range should not fire. After the
	// above history, ewma≈99 and deviation≈3.6, so threshold≈110. An
	// observation of 108 (within the ±10 range we've been feeding) is below.
	d.Observe([]SeriesCount{{Series: "mail", Observed: 108, FiledBy: "gt"}})
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "mail", Observed: 108, FiledBy: "gt"},
	}}
	cands := d.Eval(in)
	if len(cands) != 0 {
		t.Errorf("expected no candidates on moderate fluctuation, got %d", len(cands))
	}
}

func TestEWMADetector_LoopBreaker(t *testing.T) {
	d := NewEWMADetector()

	for i := 0; i < warmupCycles+5; i++ {
		d.Observe([]SeriesCount{{Series: "sling", Observed: 100, FiledBy: "scheduler"}})
	}
	d.Observe([]SeriesCount{{Series: "sling", Observed: 1000, FiledBy: "curio"}})

	// Curio-filed events should be suppressed.
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "sling", Observed: 1000, FiledBy: "curio"},
	}}
	cands := d.Eval(in)
	if len(cands) != 0 {
		t.Errorf("expected curio-filed events to be suppressed, got %d", len(cands))
	}
}

func TestEWMADetector_CurioSeriesPrefixSuppressed(t *testing.T) {
	d := NewEWMADetector()
	// Even if someone adds a curio.* series to the tracked set, the prefix
	// check should suppress it.
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "curio.internal", Observed: 9999, FiledBy: "gt"},
	}}
	cands := d.Eval(in)
	if len(cands) != 0 {
		t.Errorf("expected curio-prefix series to be suppressed, got %d", len(cands))
	}
}

func TestEWMADetector_UnknownSeriesIgnored(t *testing.T) {
	d := NewEWMADetector()

	for i := 0; i < warmupCycles+5; i++ {
		d.Observe([]SeriesCount{{Series: "unknown.series", Observed: 100, FiledBy: "gt"}})
	}

	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "unknown.series", Observed: 9999, FiledBy: "gt"},
	}}
	cands := d.Eval(in)
	if len(cands) != 0 {
		t.Errorf("expected untracked series to be ignored, got %d", len(cands))
	}
}

func TestEWMADetector_MultipleSeries(t *testing.T) {
	d := NewEWMADetector()

	// Warm up two series at different baselines.
	for i := 0; i < warmupCycles+5; i++ {
		d.Observe([]SeriesCount{
			{Series: "sling", Observed: 100, FiledBy: "scheduler"},
			{Series: "escalation", Observed: 0, FiledBy: "gt"},
		})
	}

	// Spike both.
	d.Observe([]SeriesCount{
		{Series: "sling", Observed: 1000, FiledBy: "scheduler"},
		{Series: "escalation", Observed: 50, FiledBy: "gt"},
	})

	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "sling", Observed: 1000, FiledBy: "scheduler"},
		{Series: "escalation", Observed: 50, FiledBy: "gt"},
	}}
	cands := d.Eval(in)
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates on dual spike, got %d", len(cands))
	}
}

func TestEWMADetector_StateTracking(t *testing.T) {
	d := NewEWMADetector()

	d.Observe([]SeriesCount{{Series: "done", Observed: 100, FiledBy: "refinery"}})

	ewma, deviation, count, ok := d.State("done")
	if !ok {
		t.Fatal("expected state for 'done'")
	}
	if count != 1 {
		t.Errorf("expected count=1, got %d", count)
	}
	// First observation seeds EWMA at the observed value.
	if ewma != 100.0 {
		t.Errorf("expected ewma=100, got %f", ewma)
	}
	if deviation != 0 {
		t.Errorf("expected deviation=0 after first observation, got %f", deviation)
	}

	// Second observation.
	d.Observe([]SeriesCount{{Series: "done", Observed: 200, FiledBy: "refinery"}})
	ewma, deviation, count, ok = d.State("done")
	if !ok || count != 2 {
		t.Fatalf("expected count=2, ok=true; got count=%d, ok=%v", count, ok)
	}
	// EWMA: 0.3*200 + 0.7*100 = 60 + 70 = 130
	expectedEWMA := 0.3*200 + 0.7*100
	if math.Abs(ewma-expectedEWMA) > 0.001 {
		t.Errorf("expected ewma=%.1f, got %.1f", expectedEWMA, ewma)
	}
}

func TestEWMADetector_ZeroObservationsTracked(t *testing.T) {
	d := NewEWMADetector()

	// Series not present in counts = observed 0.
	for i := 0; i < warmupCycles+5; i++ {
		d.Observe([]SeriesCount{}) // all 8 series see 0
	}

	_, _, count, ok := d.State("dispatch.stuck_agent")
	if !ok {
		t.Fatal("expected state for 'dispatch.stuck_agent' even with zero observations")
	}
	if count != warmupCycles+5 {
		t.Errorf("expected count=%d, got %d", warmupCycles+5, count)
	}
}

func TestEWMADetector_MinDeviationFloor(t *testing.T) {
	d := NewEWMADetector()

	// Feed perfectly flat series — deviation goes to 0.
	for i := 0; i < warmupCycles+5; i++ {
		d.Observe([]SeriesCount{{Series: "sched_fail", Observed: 0, FiledBy: "gt"}})
	}

	_, deviation, _, ok := d.State("sched_fail")
	if !ok {
		t.Fatal("expected state")
	}
	if deviation > 0.01 {
		t.Errorf("expected near-zero deviation on flat series, got %f", deviation)
	}

	// A single count should NOT fire because minDeviation floor keeps the
	// threshold at ewma + k*1.0 = 0 + 3*1 = 3, and observed=1 < 3.
	d.Observe([]SeriesCount{{Series: "sched_fail", Observed: 1, FiledBy: "gt"}})
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "sched_fail", Observed: 1, FiledBy: "gt"},
	}}
	cands := d.Eval(in)
	if len(cands) != 0 {
		t.Errorf("expected minDeviation floor to prevent firing on +1, got %d candidates", len(cands))
	}

	// But a large spike (>3) should fire.
	d.Observe([]SeriesCount{{Series: "sched_fail", Observed: 10, FiledBy: "gt"}})
	in = Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "sched_fail", Observed: 10, FiledBy: "gt"},
	}}
	cands = d.Eval(in)
	if len(cands) != 1 {
		t.Errorf("expected spike above minDeviation floor to fire, got %d candidates", len(cands))
	}
}

func TestEWMADetector_IntegratesWithEvaluate(t *testing.T) {
	d := NewEWMADetector()

	// Warm up.
	for i := 0; i < warmupCycles+5; i++ {
		d.Observe([]SeriesCount{{Series: "bead.open", Observed: 50, FiledBy: "gt"}})
	}
	// Spike.
	d.Observe([]SeriesCount{{Series: "bead.open", Observed: 500, FiledBy: "gt"}})

	rules := append(DefaultRules(), d)
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "bead.open", Observed: 500, FiledBy: "gt"},
	}}
	cands := Evaluate(rules, in)

	// Should have candidates from both the fixed rate rule (threshold 150) AND
	// the EWMA detector.
	ruleIDs := fired(cands)
	if !ruleIDs["alarm_rate_spike"] {
		t.Error("expected alarm_rate_spike to fire (fixed threshold 150 < 500)")
	}
	if !ruleIDs["ewma_anomaly"] {
		t.Error("expected ewma_anomaly to fire on spike")
	}
}

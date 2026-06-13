package refinery

import (
	"testing"

	"github.com/steveyegge/gastown/internal/curio"
)

// fixtureCorpus loads the shared replay corpus the daemon and B3 grade against.
// The grade-A conjunct in B7 re-runs exactly this corpus.
func fixtureCorpus(t *testing.T) []curio.Fixture {
	t.Helper()
	fx, err := curio.LoadFixtures("../curio/testdata/replay")
	if err != nil {
		t.Fatalf("loading replay corpus: %v", err)
	}
	if len(fx) == 0 {
		t.Fatal("empty replay corpus")
	}
	return fx
}

// qualifyingInputs returns inputs that satisfy ALL THREE conjuncts: a
// daemon.json-only diff confined to rate_thresholds, a precision<0.80 assertion,
// and an overlay that still grades A (raising a noisy series' ceiling keeps
// anchors firing — B3's safe-tune case).
func qualifyingInputs(t *testing.T) AutoMergeInputs {
	t.Helper()
	const base = `{"patrols":{"curio":{"rate_thresholds":{"done":1300}}}}`
	// Raise the "done" ceiling — a noisy, non-anchor series. Anchors still fire,
	// normal volume stays bounded → grade A.
	const head = `{"patrols":{"curio":{"rate_thresholds":{"done":2000}}}}`
	return AutoMergeInputs{
		Labeled:      true,
		ChangedFiles: []string{CurioThresholdConfigPath},
		BaseConfig:   base,
		HeadConfig:   head,
		Body:         "Tune curio.done: measured precision < 0.80 over the last 30d.",
		Fixtures:     fixtureCorpus(t),
	}
}

// --- Applicability ---------------------------------------------------------

func TestEvaluateAutoMerge_UnlabeledNotApplicable(t *testing.T) {
	in := qualifyingInputs(t)
	in.Labeled = false
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Applicable {
		t.Errorf("unlabeled CR must not be applicable; got %+v", d)
	}
	if d.Eligible {
		t.Errorf("unlabeled CR must never be eligible; got %+v", d)
	}
}

// --- Default OFF (B7 acceptance: policy OFF → all CRs need human) -----------

func TestEvaluateAutoMerge_PolicyOffHoldsForHuman(t *testing.T) {
	in := qualifyingInputs(t) // would qualify if ON
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: false}, in)
	if !d.Applicable {
		t.Errorf("labeled CR must be applicable even when policy is OFF; got %+v", d)
	}
	if d.Eligible {
		t.Errorf("policy OFF must hold for human even when all conjuncts hold; got %+v", d)
	}
}

func TestEvaluateAutoMerge_ZeroConfigIsOff(t *testing.T) {
	// The shipped default: zero-value config = OFF.
	d := EvaluateAutoMerge(AutoMergeConfig{}, qualifyingInputs(t))
	if d.Eligible {
		t.Errorf("zero-value (default) config must be OFF; got %+v", d)
	}
}

// --- Policy ON, all conjuncts hold (B7 acceptance: qualifying CR auto-merges) -

func TestEvaluateAutoMerge_PolicyOnQualifyingIsEligible(t *testing.T) {
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, qualifyingInputs(t))
	if !d.Eligible {
		t.Errorf("policy ON + all conjuncts → eligible; got %+v", d)
	}
}

// --- Conjunct 1: diff scope ------------------------------------------------

func TestEvaluateAutoMerge_SourceChangeNeverEligible(t *testing.T) {
	in := qualifyingInputs(t)
	in.ChangedFiles = []string{CurioThresholdConfigPath, "internal/curio/rules.go"}
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Eligible {
		t.Errorf("a source change in the CR must never be auto-eligible; got %+v", d)
	}
}

func TestEvaluateAutoMerge_FixtureDeletionNeverEligible(t *testing.T) {
	in := qualifyingInputs(t)
	in.ChangedFiles = []string{CurioThresholdConfigPath, "internal/curio/testdata/replay/05_anchor_boot_deacon_flap.json"}
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Eligible {
		t.Errorf("a fixture change/deletion in the CR must never be auto-eligible; got %+v", d)
	}
}

func TestEvaluateAutoMerge_OtherDaemonKeyNeverEligible(t *testing.T) {
	// The CR touches only daemon.json by path, but edits a key OUTSIDE
	// rate_thresholds (a patrol schedule) — must fail the structural scope check.
	in := qualifyingInputs(t)
	in.BaseConfig = `{"patrols":{"curio":{"rate_thresholds":{"done":1300}}},"maintenance":{"hour":3}}`
	in.HeadConfig = `{"patrols":{"curio":{"rate_thresholds":{"done":2000}}},"maintenance":{"hour":5}}`
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Eligible {
		t.Errorf("a daemon.json change beyond rate_thresholds must not be eligible; got %+v", d)
	}
}

func TestEvaluateAutoMerge_RateThresholdsOnlyChangeKeepsScopeOK(t *testing.T) {
	// Same non-threshold content on both sides (only key order differs) must
	// still pass the structural scope check — canonical JSON comparison.
	in := qualifyingInputs(t)
	in.BaseConfig = `{"maintenance":{"hour":3},"patrols":{"curio":{"rate_thresholds":{"done":1300}}}}`
	in.HeadConfig = `{"patrols":{"curio":{"rate_thresholds":{"done":2000}}},"maintenance":{"hour":3}}`
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if !d.Eligible {
		t.Errorf("rate_thresholds-only change (whitespace/order aside) should be eligible; got %+v", d)
	}
}

func TestEvaluateAutoMerge_NoChangedFilesNotEligible(t *testing.T) {
	in := qualifyingInputs(t)
	in.ChangedFiles = nil
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Eligible {
		t.Errorf("empty diff must not be eligible; got %+v", d)
	}
}

// --- Conjunct 2: precision assertion ---------------------------------------

func TestEvaluateAutoMerge_MissingPrecisionAssertionNotEligible(t *testing.T) {
	in := qualifyingInputs(t)
	in.Body = "Tune curio.done thresholds. No precision data here."
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Eligible {
		t.Errorf("a CR body with no precision assertion must not be eligible; got %+v", d)
	}
}

func TestEvaluateAutoMerge_PrecisionAtOrAboveCeilingNotEligible(t *testing.T) {
	in := qualifyingInputs(t)
	in.Body = "Tune curio.done: measured precision 0.85 (still high)."
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Eligible {
		t.Errorf("precision >= 0.80 must not be eligible; got %+v", d)
	}
}

func TestEvaluateAutoMerge_PrecisionAssertionVariants(t *testing.T) {
	cases := []struct {
		body string
		want bool // eligible
	}{
		{"measured precision < 0.80", true},
		{"precision: 0.5 over 30d", true},
		{"the precision is 0.799 here", true},
		{"precision = 0.80", false}, // exactly at ceiling → not below
		{"precision 0.95", false},
		{"no assertion at all", false},
	}
	for _, c := range cases {
		in := qualifyingInputs(t)
		in.Body = c.body
		d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
		if d.Eligible != c.want {
			t.Errorf("body %q: eligible=%v, want %v (reason=%s)", c.body, d.Eligible, c.want, d.Reason)
		}
	}
}

// --- Conjunct 3: replay grade A --------------------------------------------

func TestEvaluateAutoMerge_GradeBelowANotEligible(t *testing.T) {
	in := qualifyingInputs(t)
	// Loosen dispatch.stuck_agent past the alarm-flood anchor's level — proven
	// in B3 to silence anchor gu-70rg → grade < A.
	in.HeadConfig = `{"patrols":{"curio":{"rate_thresholds":{"dispatch.stuck_agent":200}}}}`
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Eligible {
		t.Errorf("an overlay that drops grade below A must not be eligible; got %+v", d)
	}
}

func TestEvaluateAutoMerge_EmptyCorpusNotEligible(t *testing.T) {
	in := qualifyingInputs(t)
	in.Fixtures = nil
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Eligible {
		t.Errorf("empty fixture corpus must not be eligible (cannot verify grade A); got %+v", d)
	}
}

func TestEvaluateAutoMerge_UnparseableHeadConfigNotEligible(t *testing.T) {
	in := qualifyingInputs(t)
	// Passes the path scope check (single file) but base/head won't parse for
	// the structural compare — diff-scope fails first, which is the right HOLD.
	in.BaseConfig = `{"patrols":{"curio":{"rate_thresholds":{"done":1300}}}}`
	in.HeadConfig = `{not valid json`
	d := EvaluateAutoMerge(AutoMergeConfig{Enabled: true}, in)
	if d.Eligible {
		t.Errorf("unparseable daemon.json must not be eligible; got %+v", d)
	}
}

// --- stripRateThresholds canonicalization ----------------------------------

func TestStripRateThresholds_IgnoresThresholdContentAndOrdering(t *testing.T) {
	a, err := stripRateThresholds(`{"x":1,"patrols":{"curio":{"rate_thresholds":{"done":1300}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	b, err := stripRateThresholds(`{"patrols":{"curio":{"rate_thresholds":{"done":9999,"sling":42}}},"x":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("stripped remainders should be equal regardless of threshold content/order:\n a=%s\n b=%s", a, b)
	}
}

func TestStripRateThresholds_EmptyIsNull(t *testing.T) {
	got, err := stripRateThresholds("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "null" {
		t.Errorf("empty config should canonicalize to %q, got %q", "null", got)
	}
}

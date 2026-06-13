package curio

import (
	"os"
	"path/filepath"
	"testing"
)

// normalCandidateBound is the Phase 0 go/no-go threshold: ≤20 candidates/day on
// held-out normal windows. The replay harness is the CI gate enforcing it.
const normalCandidateBound = 20

// TestReplay is the CI gate (gu-6s8ao): content rules MUST fire on every anchor
// incident, and candidate volume on normal windows MUST stay bounded. Run as
// `go test ./internal/curio -run Replay`.
func TestReplay(t *testing.T) {
	fixtures, err := LoadFixtures("testdata/replay")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures found")
	}

	rep := Grade(DefaultRules(), fixtures)

	// Recall: every anchor must fire its expected rules.
	if len(rep.AnchorsHit) == 0 {
		t.Fatal("no anchor windows in corpus")
	}
	for anchor, hit := range rep.AnchorsHit {
		if !hit {
			t.Errorf("anchor %s did NOT fire expected rules; missing=%v", anchor, rep.MissingRules[anchor])
		}
	}

	// Precision proxy: bounded candidate volume on normal windows.
	if rep.NormalCandidates > normalCandidateBound {
		t.Errorf("normal window %q produced %d candidates, exceeds bound %d",
			rep.WorstNormalWindow, rep.NormalCandidates, normalCandidateBound)
	}

	t.Logf("replay: %d anchors all-hit, worst normal window=%q (%d candidates, bound %d)",
		len(rep.AnchorsHit), rep.WorstNormalWindow, rep.NormalCandidates, normalCandidateBound)
}

// TestGradeWithThresholds_LooseningBelowAnchorFails is B3's regression-catch
// case: an overlay that loosens (raises) a threshold past the value an anchor
// incident actually hit makes that anchor stop firing → grade < A. Without the
// overlay-aware grade, this config-only regression would sail through go test.
// The alarm-flood anchor (gu-70rg) fires alarm_rate_spike solely off
// dispatch.stuck_agent=123 (default threshold 0); raising that ceiling to 200
// silences it.
func TestGradeWithThresholds_LooseningBelowAnchorFails(t *testing.T) {
	fixtures, err := LoadFixtures("testdata/replay")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	// Baseline: the calibrated defaults grade A (the corpus is the CI gate).
	if base := GradeWithThresholds(nil, fixtures); !base.GradeA(normalCandidateBound) {
		t.Fatalf("calibrated defaults must grade A; got %+v", base)
	}

	overlay := map[string]int{"dispatch.stuck_agent": 200}
	rep := GradeWithThresholds(overlay, fixtures)
	if rep.GradeA(normalCandidateBound) {
		t.Errorf("loosening dispatch.stuck_agent below the anchor's level must drop grade below A; got %+v", rep)
	}
	if hit := rep.AnchorsHit["gu-70rg"]; hit {
		t.Errorf("anchor gu-70rg must FAIL to fire under the loosened threshold; missing=%v", rep.MissingRules["gu-70rg"])
	}
}

// TestGradeWithThresholds_RaisingNoisyCeilingStaysA is B3's safe-tune case: an
// overlay that only raises the ceiling on a currently-quiet ("noisy") series
// touches no anchor and can only REDUCE normal-window volume, so all anchors keep
// firing and grade stays A. This is the shape of a legitimate threshold tune the
// auto-merge path (B7) must pass.
func TestGradeWithThresholds_RaisingNoisyCeilingStaysA(t *testing.T) {
	fixtures, err := LoadFixtures("testdata/replay")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	prior := GradeWithThresholds(nil, fixtures)
	if !prior.GradeA(normalCandidateBound) {
		t.Fatalf("calibrated defaults must grade A; got %+v", prior)
	}

	// Raise the "done" ceiling well above its near-threshold normal volume (1183).
	overlay := map[string]int{"done": 5000}
	rep := GradeWithThresholds(overlay, fixtures)
	if !rep.GradeA(normalCandidateBound) {
		t.Errorf("raising a quiet series' ceiling must keep grade A; got %+v", rep)
	}
	if rep.NormalCandidates > prior.NormalCandidates {
		t.Errorf("raising a ceiling cannot increase normal-window volume: was %d, now %d",
			prior.NormalCandidates, rep.NormalCandidates)
	}
}

// TestLoadRateThresholdOverlay round-trips a daemon.json projection: a CR
// touching only patrols.curio.rate_thresholds is gradeable in CI by loading the
// overlay from the file and feeding it to GradeWithThresholds.
func TestLoadRateThresholdOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.json")
	const cfg = `{"patrols":{"curio":{"rate_thresholds":{"dispatch.stuck_agent":200,"done":5000}}}}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("writing daemon.json: %v", err)
	}

	overlay, err := LoadRateThresholdOverlay(path)
	if err != nil {
		t.Fatalf("loading overlay: %v", err)
	}
	if overlay["dispatch.stuck_agent"] != 200 || overlay["done"] != 5000 {
		t.Errorf("overlay mis-parsed: %+v", overlay)
	}

	// Missing file → nil overlay, no error (unconfigured town tunes nothing).
	missing, err := LoadRateThresholdOverlay(filepath.Join(dir, "absent.json"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if missing != nil {
		t.Errorf("missing file must yield nil overlay, got %+v", missing)
	}
}

// TestReplay_LoopBreakerWindow specifically asserts the curio-self-events window
// produces ZERO candidates — the loop-breaker must suppress all self-activity.
func TestReplay_LoopBreakerWindow(t *testing.T) {
	fixtures, err := LoadFixtures("testdata/replay")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}
	for _, f := range fixtures {
		if f.Input.Window.ID != "2026-05-22/1d-normal-loopbreak" {
			continue
		}
		cands := Evaluate(DefaultRules(), f.Input)
		if len(cands) != 0 {
			t.Errorf("loop-breaker window produced %d candidates, want 0: %+v", len(cands), cands)
		}
		return
	}
	t.Fatal("loop-breaker fixture not found")
}

// TestReplay_BootDeaconFlapCollapses asserts the Call 1(B) state-hash damper:
// three dead-owner reservations flapping across boot/deacon owners within ONE
// rig collapse to a single candidate (not three).
func TestReplay_BootDeaconFlapCollapses(t *testing.T) {
	fixtures, err := LoadFixtures("testdata/replay")
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}
	for _, f := range fixtures {
		if f.Input.Window.ID != "2026-06-01/1d-boot-deacon-flap" {
			continue
		}
		cands := Evaluate(DefaultRules(), f.Input)
		if len(cands) != 1 {
			t.Errorf("boot<->deacon flap must collapse to 1 candidate, got %d: %+v", len(cands), cands)
		}
		return
	}
	t.Fatal("boot-deacon flap fixture not found")
}

package curio

import "testing"

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

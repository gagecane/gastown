package curio

import "testing"

// cand builds a minimal candidate with a fixed (rule,target) identity.
func cand(rule, target string) Candidate {
	return newCandidate("w", rule, target, "", "s", 1, "summary")
}

func TestReactionTracker_PersistentFindingNeverFreezes(t *testing.T) {
	// A genuine, persistent fault is present every cycle. It flips ONCE (on
	// first appearance) and then never again, so it must never freeze no matter
	// how many cycles it lasts.
	tr := NewReactionTracker()
	c := cand("dead_owner_admission", "rigA")
	for i := 0; i < 10; i++ {
		out := tr.Observe([]Candidate{c})
		if out[0].Frozen {
			t.Fatalf("persistent finding froze on cycle %d (flips=%d)", i, out[0].ReactionCount)
		}
	}
}

func TestReactionTracker_FlappingFindingFreezes(t *testing.T) {
	// A finding that oscillates present/absent every cycle accrues a flip each
	// cycle and freezes once it exceeds the threshold.
	tr := NewReactionTracker()
	c := cand("alarm_rate_spike", "flappy")
	froze := false
	for i := 0; i < freezeFlipThreshold+3; i++ {
		var batch []Candidate
		if i%2 == 0 {
			batch = []Candidate{c} // present on even cycles
		}
		out := tr.Observe(batch)
		if len(out) == 1 && out[0].Frozen {
			froze = true
		}
	}
	if !froze {
		t.Error("a finding flapping every cycle must eventually freeze")
	}
}

func TestReactionTracker_FreezeRequiresStrictlyMoreThanThreshold(t *testing.T) {
	// Drive exactly freezeFlipThreshold flips and assert NOT yet frozen, then one
	// more flip and assert frozen (strict >).
	tr := NewReactionTracker()
	c := cand("alarm_rate_spike", "edge")

	// Cycle 0 present (flip #1). Then alternate absent/present.
	seq := []bool{true, false, true, false, true, false, true}
	var lastFrozen bool
	var lastCount int
	for _, present := range seq {
		var batch []Candidate
		if present {
			batch = []Candidate{c}
		}
		out := tr.Observe(batch)
		if present {
			lastFrozen = out[0].Frozen
			lastCount = out[0].ReactionCount
		}
	}
	// 7-cycle alternating sequence yields > threshold flips within the window.
	if lastCount <= freezeFlipThreshold {
		t.Fatalf("expected flip count > %d, got %d", freezeFlipThreshold, lastCount)
	}
	if !lastFrozen {
		t.Errorf("expected frozen once flips (%d) exceed threshold (%d)", lastCount, freezeFlipThreshold)
	}
}

func TestReactionTracker_AgesOutQuietFindings(t *testing.T) {
	// A finding that appears once and then goes quiet must be pruned from the
	// tracker (no unbounded growth) and not carry a stale freeze.
	tr := NewReactionTracker()
	c := cand("kill_signal_near_dolt", "gone")
	tr.Observe([]Candidate{c})
	// Several quiet cycles.
	for i := 0; i < trackWindow+2; i++ {
		tr.Observe(nil)
	}
	if len(tr.states) != 0 {
		t.Errorf("quiet finding should be pruned, tracker still holds %d keys", len(tr.states))
	}
}

func TestReactionTracker_AnnotatesReactionCount(t *testing.T) {
	tr := NewReactionTracker()
	c := cand("dead_owner_admission", "rigZ")
	out := tr.Observe([]Candidate{c})
	if out[0].ReactionCount != 1 {
		t.Errorf("first appearance should record 1 flip, got %d", out[0].ReactionCount)
	}
}

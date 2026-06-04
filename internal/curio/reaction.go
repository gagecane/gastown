package curio

// Call 1(C): reaction-count backstop.
//
// The state-hash damper (1B) collapses a flap WITHIN a single cycle, but a
// finding that appears, clears, and re-appears across SUCCESSIVE cycles is a
// different pathology: a (target,rule) oscillating faster than the world it
// describes is reacting to itself (or to Curio's own downstream churn) rather
// than to a stable fault. The reaction-count backstop watches presence across
// cycles and FREEZES a finding that flips more than freezeFlipThreshold times
// within the trailing trackWindow cycles.
//
// Build 2a only RECORDS the freeze on the candidate (ReactionCount + Frozen).
// Call 2 (gu-2coqj) consumes Frozen to keep a churning finding out of the
// paging lane. The tracker is daemon-held (cross-cycle) and single-goroutine
// (the curio patrol runs serially), so it needs no locking.

const (
	// freezeFlipThreshold is the flip count that trips a freeze: a finding that
	// changes presence (absent->present or present->absent) STRICTLY MORE than
	// this many times inside trackWindow is "flapping" and gets frozen.
	//
	// The bead scope phrases this as ">3x / 3 cycles". A strict ">3 flips"
	// cannot be observed inside a literal 3-cycle window (3 cycles yield at most
	// 3 flip transitions), so we read "3 cycles" as the OSCILLATION RATE the
	// design wants to catch and size trackWindow wide enough to actually observe
	// "more than 3" flips at that rate. Threshold and window are the two tuning
	// knobs; Call 2 (gu-2coqj) may revisit them once live cadence is measured.
	freezeFlipThreshold = 3
	// trackWindow is how many recent cycles of flip history to retain per key.
	// Sized > freezeFlipThreshold so ">3 flips" is observable.
	trackWindow = 6
)

// reactionState is the per-(rule,target) flip history within the window.
type reactionState struct {
	// present is whether the finding was seen in the most recent cycle.
	present bool
	// flips is the recent presence-change history, newest last, capped at
	// trackWindow. Each true is "presence changed this cycle".
	flips []bool
}

// flipCount sums the flips currently inside the window.
func (s *reactionState) flipCount() int {
	n := 0
	for _, f := range s.flips {
		if f {
			n++
		}
	}
	return n
}

// ReactionTracker remembers, across patrol cycles, how often each (rule,target)
// finding has flipped presence, and freezes the ones that churn. It is held by
// the daemon (one per process) and observed once per cycle.
type ReactionTracker struct {
	states map[string]*reactionState
}

// NewReactionTracker returns an empty tracker ready for the first cycle.
func NewReactionTracker() *ReactionTracker {
	return &ReactionTracker{states: map[string]*reactionState{}}
}

// reactionKey identifies a finding across cycles. It uses Fingerprint
// (rule+target) rather than StateHash so the backstop tracks the SPECIFIC
// finding's churn, independent of the 1B state-collapse.
func reactionKey(c Candidate) string { return c.Fingerprint }

// Observe records one patrol cycle's candidates, updates each finding's flip
// history, ages out findings that did not appear this cycle, and returns the
// candidates annotated with ReactionCount and Frozen. The returned slice is a
// copy-with-annotations; the input is not mutated in place beyond the fields
// this method owns.
//
// A finding present this cycle but absent last cycle (or vice-versa) counts as
// one flip. A finding present in consecutive cycles does NOT flip — a genuine,
// persistent fault accrues zero flips and is never frozen, no matter how long
// it lasts. Only oscillation trips the backstop.
func (t *ReactionTracker) Observe(cands []Candidate) []Candidate {
	if t.states == nil {
		t.states = map[string]*reactionState{}
	}

	presentNow := make(map[string]bool, len(cands))
	for _, c := range cands {
		presentNow[reactionKey(c)] = true
	}

	// Age every tracked key: append this cycle's flip bit (changed vs. last
	// cycle's presence), trim to the window, and prune keys that have gone
	// quiet for a full window (so the map cannot grow unbounded).
	for key, st := range t.states {
		nowPresent := presentNow[key]
		flipped := nowPresent != st.present
		st.present = nowPresent
		st.flips = append(st.flips, flipped)
		if len(st.flips) > trackWindow {
			st.flips = st.flips[len(st.flips)-trackWindow:]
		}
		if !nowPresent && st.flipCount() == 0 {
			delete(t.states, key)
		}
	}

	// Register keys appearing for the first time this cycle as a present->true
	// flip (absent in all prior cycles -> present now).
	for key := range presentNow {
		if _, ok := t.states[key]; !ok {
			t.states[key] = &reactionState{present: true, flips: []bool{true}}
		}
	}

	out := make([]Candidate, len(cands))
	copy(out, cands)
	for i := range out {
		st := t.states[reactionKey(out[i])]
		if st == nil {
			continue
		}
		out[i].ReactionCount = st.flipCount()
		out[i].Frozen = st.flipCount() > freezeFlipThreshold
	}
	return out
}

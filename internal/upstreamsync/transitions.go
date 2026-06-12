// State machine transitions for upstream sync.
//
// The upstream sync state machine is defined in .designs/cv-2s6tq/data.md
// §"State Machine". This file enforces those transitions on top of the
// CAS-safe MutateSyncState helper from state_bead.go: callers describe
// the intended target state and a per-attempt mutate callback, and
// TransitionTo refuses the write if the resulting (current → target)
// pair is not in the validTransitions table.
//
// Why this lives next to state_bead.go: the validity table and the
// CAS retry loop are joined-at-the-hip. A caller using the raw
// MutateSyncState surface to bypass these checks would be writing
// arbitrary states into the bead — an obvious foot-gun the design
// explicitly forbids ("CAS on the state field prevents concurrent
// attempts"). All in-tree callers are required to go through
// TransitionTo so the state machine remains the single source of
// truth for what's allowed.
//
// Design context: .designs/cv-2s6tq/data.md §"State Machine".
package upstreamsync

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
)

// validTransitions enumerates the (from, to) state pairs allowed by
// the design's state machine diagram. Any transition not listed here
// is rejected by TransitionTo with ErrInvalidTransition.
//
// Self-loops (e.g., idle → idle) are rejected by default — callers
// who genuinely want a no-op should use MutateSyncState directly,
// since TransitionTo is for state changes.
//
// The "* → paused" wildcard from the design (line 196) is materialized
// as one row per source state below — explicit beats pseudocode.
// Likewise "paused → idle" is one row.
var validTransitions = map[SyncState]map[SyncState]struct{}{
	StateIdle: {
		StateChecking: {},
		StatePaused:   {},
	},
	StateChecking: {
		StateIdle:      {},
		StateSyncing:   {},
		StateResolving: {},
		StateFailed:    {},
		StatePaused:    {},
	},
	StateSyncing: {
		StateGating: {},
		StateFailed: {},
		StatePaused: {},
	},
	StateResolving: {
		StateGating: {},
		StateFailed: {},
		StatePaused: {},
	},
	StateGating: {
		StatePushing: {},
		StateFailed:  {},
		StatePaused:  {},
	},
	StatePushing: {
		StateIdle:   {},
		StateFailed: {},
		StatePaused: {},
	},
	StateFailed: {
		StateIdle:   {},
		StatePaused: {},
		// A failed attempt may be retried directly: `gt upstream sync`
		// accepts StateFailed as a valid starting point (a prior attempt
		// failed or escalated), and begins the new attempt by transitioning
		// to checking. Without this edge the rig would wedge in failed
		// forever — fork-sync failures are expected and recurring, so
		// retry-after-failure must be a first-class transition.
		StateChecking: {},
	},
	StatePaused: {
		StateIdle: {},
	},
}

// ErrInvalidTransition is returned by TransitionTo when the (from, to)
// state pair is not allowed by the state machine.
type ErrInvalidTransition struct {
	From SyncState
	To   SyncState
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("upstream sync: invalid state transition %s → %s", e.From, e.To)
}

// IsValidTransition reports whether the (from, to) pair is allowed by
// the state machine. Callers can pre-flight a transition without
// touching Dolt (useful for CLI input validation).
func IsValidTransition(from, to SyncState) bool {
	if !from.IsValid() || !to.IsValid() {
		return false
	}
	dests, ok := validTransitions[from]
	if !ok {
		return false
	}
	_, ok = dests[to]
	return ok
}

// TransitionTo performs a CAS-safe state transition on the per-rig
// upstream-sync state bead. It loads the current state, verifies the
// transition is allowed by the state machine, runs the mutate callback
// (which sets State and any related fields), and writes back. The
// retry loop in MutateSyncState handles transient Dolt lock errors.
//
// The mutate callback is called on each retry with a freshly-loaded
// state. It MUST set s.State to target — this function does not set
// it for the caller because related fields (e.g. CurrentAttempt,
// PausedUntil, ConsecutiveFailures) are state-pair specific and
// belong to the caller's policy, not this generic helper.
//
// If mutate forgets to set s.State == target, TransitionTo returns an
// error rather than silently writing whatever the callback left in
// the State field — that's the most common foot-gun for callers
// converting from raw MutateSyncState.
//
// Returns *ErrInvalidTransition if the transition is rejected. The
// initial state validity check happens before mutate is called, so
// the callback is not invoked for invalid transitions.
func TransitionTo(b *beads.Beads, rigPrefix string, target SyncState, mutate func(*SyncStateMetadata) error) error {
	if !target.IsValid() {
		return fmt.Errorf("upstream sync: target state %q is not a recognized SyncState", target)
	}

	return MutateSyncState(b, rigPrefix, func(s *SyncStateMetadata) error {
		from := s.State
		if !IsValidTransition(from, target) {
			return &ErrInvalidTransition{From: from, To: target}
		}
		if mutate != nil {
			if err := mutate(s); err != nil {
				return err
			}
		}
		// Defensive: ensure the callback set s.State as we expect. If
		// the caller forgot, set it for them but error out so the bug
		// is obvious in tests.
		if s.State != target {
			return fmt.Errorf("upstream sync: mutate callback did not set State to %s (got %s)", target, s.State)
		}
		return nil
	})
}

// ValidNextStates returns the set of states reachable from `from` by a
// single TransitionTo call. Returns an empty slice for invalid input.
// Useful for CLI hints ("you can transition this rig to: idle, paused").
func ValidNextStates(from SyncState) []SyncState {
	if !from.IsValid() {
		return nil
	}
	dests, ok := validTransitions[from]
	if !ok {
		return nil
	}
	out := make([]SyncState, 0, len(dests))
	for s := range dests {
		out = append(out, s)
	}
	return out
}

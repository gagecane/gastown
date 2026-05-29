// Circuit breaker for upstream sync.
//
// Phase 4 (gu-g5gh). When N consecutive sync attempts fail in a row,
// the circuit breaker auto-pauses the rig so it stops burning polecat
// slots retrying the same wedged scenario every cooldown tick. The
// security design (cv-2s6tq/security.md §T3) names this as the
// mitigation for "DoS via conflict flooding," and the data design
// (cv-2s6tq/data.md §"Circuit breaker") fixes the default threshold
// at 3 — operators can override via UpstreamSyncConfig.MaxConsecutiveFailures.
//
// The trip predicate is a pure function: ShouldTrip(failures, threshold).
// Wiring into the state machine lives in the call sites (upstream_sync.go
// for manual invocations, deacon patrol for automated cycles) so the
// pure logic stays testable in isolation.
//
// Design context: .designs/cv-2s6tq/security.md §T3,
// .designs/cv-2s6tq/data.md §"Circuit breaker".
package upstreamsync

import (
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

// ShouldTrip reports whether the circuit breaker should fire for a
// rig with `failures` consecutive failures, given a configured
// `threshold`. A non-positive threshold is treated as "use the default"
// (config.DefaultUpstreamSyncMaxConsecutiveFailures).
//
// The predicate fires on equality (failures >= threshold) — the Nth
// failure is the one that trips, not the (N+1)th. This matches the
// security design: 3 consecutive failures pauses, not 4.
func ShouldTrip(failures, threshold int) bool {
	if threshold <= 0 {
		threshold = config.DefaultUpstreamSyncMaxConsecutiveFailures
	}
	return failures >= threshold
}

// CircuitBreakerReason is the canonical pause-reason string written to
// the state bead when the breaker auto-pauses a rig. Stable so audit
// queries (`gt upstream history --auto-paused`) can grep for it.
func CircuitBreakerReason(failures int) string {
	return fmt.Sprintf("circuit-breaker: %d consecutive failures (auto-paused)", failures)
}

// TripIfNeeded inspects the rig's state and, if ShouldTrip fires,
// transitions Failed→Paused with the canonical reason and clears any
// in-progress attempt. Idempotent: a no-op if the breaker shouldn't
// trip or if the state is already paused.
//
// Returns:
//   - tripped: true if the breaker fired and the state was transitioned
//   - err: any error from loading state, computing the threshold, or
//     performing the transition
//
// Callers should invoke this after appending a failed attempt — the
// counter increment must already be persisted on the state bead so the
// breaker reads the post-increment value.
func TripIfNeeded(b *beads.Beads, rigPrefix string, cfg *config.UpstreamSyncConfig) (tripped bool, err error) {
	state, err := LoadSyncState(b, rigPrefix)
	if err != nil {
		return false, fmt.Errorf("loading state for circuit-breaker check: %w", err)
	}

	// Already paused — nothing to do; preserves operator-set pause reason.
	if state.State == StatePaused {
		return false, nil
	}

	threshold := cfg.GetMaxConsecutiveFailures()
	if !ShouldTrip(state.ConsecutiveFailures, threshold) {
		return false, nil
	}

	// Only transition from Failed (the standard post-attempt state).
	// If the breaker observes a non-Failed, non-Paused state at this
	// point something else is racing — bail rather than pave over.
	if state.State != StateFailed {
		return false, nil
	}

	failures := state.ConsecutiveFailures
	err = TransitionTo(b, rigPrefix, StatePaused, func(s *SyncStateMetadata) error {
		s.State = StatePaused
		s.PauseReason = CircuitBreakerReason(failures)
		// PausedUntil is intentionally empty — circuit-breaker pauses
		// are indefinite, requiring `gt upstream resume` to clear.
		s.PausedUntil = ""
		// Clear any stale current attempt; defense in depth — Failed
		// already nilled it but we don't trust the previous writer.
		s.CurrentAttempt = nil
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("circuit-breaker pause: %w", err)
	}
	return true, nil
}

// CircuitBreakerEvent is the structured record returned by
// `gt upstream history` when the breaker has tripped. It is not
// persisted as a separate bead — the audit trail lives in the state
// bead's PauseReason + the corresponding failed attempt entry.
type CircuitBreakerEvent struct {
	// Failures is the consecutive-failure count at trip time.
	Failures int

	// TrippedAt is the RFC3339 timestamp of the trip.
	TrippedAt string

	// LastFailedAttemptID is the SyncAttempt.ID that caused the trip.
	LastFailedAttemptID string

	// LastFailedOutcome is the Outcome string of that attempt.
	LastFailedOutcome string
}

// MostRecentTrip walks the attempt history and returns the most recent
// trip event, or nil if the breaker has never fired. The walk is bounded
// by the state bead's history cap so it is O(N) on a fixed-size slice.
func MostRecentTrip(state SyncStateMetadata, threshold int) *CircuitBreakerEvent {
	if threshold <= 0 {
		threshold = config.DefaultUpstreamSyncMaxConsecutiveFailures
	}
	if state.ConsecutiveFailures < threshold {
		// Counter has been reset since the last trip (success or
		// operator resume). No active trip to report.
		// Falls through to history scan only if history is needed —
		// for now the live-state question dominates.
		return nil
	}

	// Walk attempts newest→oldest; the most recent failure that pushed
	// the counter to/over the threshold is the trip event.
	for i := len(state.Attempts) - 1; i >= 0; i-- {
		a := state.Attempts[i]
		if a.Outcome == "" || a.Outcome == "success" || a.Outcome == "skipped" {
			continue
		}
		return &CircuitBreakerEvent{
			Failures:            state.ConsecutiveFailures,
			TrippedAt:           a.CompletedAt,
			LastFailedAttemptID: a.ID,
			LastFailedOutcome:   a.Outcome,
		}
	}
	return nil
}

// IsAutoPaused reports whether the rig is currently paused by the
// circuit breaker (vs. an operator-initiated pause). The discriminator
// is the canonical pause-reason prefix; operator pauses use a different
// shape ("<reason> (by <actor>)"). This lets resume CLIs and audit
// dashboards differentiate the two cases without a separate flag.
func IsAutoPaused(state SyncStateMetadata) bool {
	if state.State != StatePaused {
		return false
	}
	return state.PauseReason != "" &&
		// CircuitBreakerReason starts with this stable prefix.
		hasPrefix(state.PauseReason, "circuit-breaker:")
}

// hasPrefix avoids importing strings just for this one call site —
// keeps the package's import surface lean and parallels the itoa
// helper in complexity.go.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// nowFn is the indirection point for tests that need to pin time.
// Production reads time.Now via this var; tests override it during
// setup. Mirrors upstreamNowFn in upstream_pause.go but private to
// this package so tests don't fight over the global.
var nowFn = func() time.Time { return time.Now() }

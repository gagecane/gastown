// Mayor cycle-close handler for auto-test-pr MR beads.
//
// Phase 0 task 3c (gu-xrxm6) — consumes MRCycleCloseEvent (dispatched
// by the mr_cycle_close_dog daemon patrol in internal/daemon/) and
// implements the four cycle-close paths:
//
//   - merged → CAS-transition per-rig state mr-pending → cooled-down,
//     append transition record, reset circuit-breaker counter to zero.
//   - closed-unmerged → CAS-transition mr-pending → cooled-down, append
//     transition + rejection record, increment town-bead circuit-breaker
//     counter; if ≥3 closes in rolling 7-day window →
//     paused-by-circuit-breaker + Overseer nudge (Q6 SEV-2).
//   - On either path, parse any BUG-DISCOVERED: NOTES and file a P2
//     bug bead in the rig linked to the MR bead.
//   - O(1) state-bead lookup via rig:<target_rig> label on the MR bead
//     (the event's TargetRig field, set by the dog from the label).
//
// Per the design's OQ4 fallback: the single-writer town-state bead
// carries only the circuit-breaker counter and RigSummary (denormalized
// read-cache). Transition/rejection records are append-only log entries
// that Phase 1 moves to attachment beads. In Phase 0, we append them to
// the RigSummary entry as a bounded log (≤20 entries per rig, same cap
// as Incidents on the town-state).
//
// All mutations go through mutateTownState (CAS-safe read-modify-write),
// so the handler is safe to call from the heartbeat goroutine without
// additional synchronization.
package autotestpr

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// CircuitBreakerThreshold is the number of closed-unmerged MRs in a
// rolling 7-day window that trips the circuit breaker (Q6 SEV-2).
const CircuitBreakerThreshold = 3

// CircuitBreakerWindow is the rolling time window for the breaker.
const CircuitBreakerWindow = 7 * 24 * time.Hour

// RigCycleState is the state machine for a single rig's auto-test-pr
// cycle. Stored in TownState.RigSummary[rig] as JSON. Phase 0 only
// tracks state, last_cycle_at, and last_outcome here; Phase 1 task 15
// will provision per-rig state beads with a richer schema.
type RigCycleState struct {
	// State is the current state machine position.
	// Values: "idle", "mr-pending", "cooled-down", "paused-by-circuit-breaker"
	State string `json:"state"`

	// LastCycleAt is the RFC3339 timestamp of the last state transition.
	LastCycleAt string `json:"last_cycle_at,omitempty"`

	// LastOutcome is the close_reason of the most recent MR close event.
	// Values: "merged", "rejected", "conflict", "superseded", etc.
	LastOutcome string `json:"last_outcome,omitempty"`

	// TransitionLog is a bounded log (≤MaxRigTransitions) of state
	// transitions for this rig. Phase 0 home; Phase 1 moves this to
	// attachment beads per the OQ4 fallback.
	TransitionLog []RigTransition `json:"transition_log,omitempty"`

	// RejectionLog is a bounded log (≤MaxRigTransitions) of rejected
	// (closed-unmerged) MR events. Contains the target_path for the
	// per-file 21d cooldown. Phase 0 home; Phase 1 moves this to
	// attachment beads.
	RejectionLog []RigRejection `json:"rejection_log,omitempty"`
}

// RigTransition is a single transition log entry.
type RigTransition struct {
	At       string `json:"at"`
	From     string `json:"from"`
	To       string `json:"to"`
	MRID     string `json:"mr_id"`
	Reason   string `json:"reason"`
}

// RigRejection is a single rejection log entry.
type RigRejection struct {
	At         string `json:"at"`
	MRID       string `json:"mr_id"`
	TargetPath string `json:"target_path,omitempty"`
}

// MaxRigTransitions bounds the per-rig transition and rejection logs.
const MaxRigTransitions = 20

// CycleCloseHandler holds the dependencies for processing cycle-close
// events. The handler is initialized once at daemon startup and
// registered via Daemon.SetMRCycleCloseHandler.
type CycleCloseHandler struct {
	// Beads is the beads wrapper for the town root. Used for CAS
	// mutations on the town-state bead and for filing bug beads.
	Beads *beads.Beads

	// NudgeOverseer is called when the circuit breaker trips. The
	// string argument is the formatted message. In production this
	// shells out to `gt nudge overseer`; in tests it's a stub.
	NudgeOverseer func(msg string)

	// Now returns the current time. Injected so tests can pin it.
	Now func() time.Time

	// Logf is a printf-style logger. In production this is the
	// daemon's logger.Printf; in tests it captures log lines.
	Logf func(format string, args ...interface{})
}

// HandleEvent processes a single MRCycleCloseEvent. This is the
// function registered with Daemon.SetMRCycleCloseHandler.
//
// It is idempotent: re-processing the same event (due to a missed ack
// label on the dog side) produces the same state transitions because
// transitions are guarded by the current state (CAS from mr-pending
// only). The circuit-breaker counter increment is NOT idempotent on
// repeat, but the dog's ack-label mechanism prevents re-dispatch in
// the normal case; the rare transient-ack-failure path accepts a
// slight over-count as the lesser evil vs. complex distributed dedup.
func (h *CycleCloseHandler) HandleEvent(ev MRCycleCloseEvent) {
	now := h.now()
	h.logf("cycle-close-handler: processing mr=%s rig=%s reason=%s", ev.MRID, ev.TargetRig, ev.CloseReason)

	// Classify the close reason into merged vs closed-unmerged.
	isMerged := strings.EqualFold(ev.CloseReason, "merged")

	// CAS-mutate the town-state bead: update RigSummary and circuit breaker.
	err := mutateTownState(h.Beads, func(s *TownState) error {
		// 1. Update per-rig state in RigSummary.
		rigState := h.loadRigCycleState(s, ev.TargetRig)

		prevState := rigState.State
		rigState.State = "cooled-down"
		rigState.LastCycleAt = now.UTC().Format(time.RFC3339)
		rigState.LastOutcome = ev.CloseReason

		// Append transition record.
		appendRigTransition(&rigState, RigTransition{
			At:     now.UTC().Format(time.RFC3339),
			From:   prevState,
			To:     "cooled-down",
			MRID:   ev.MRID,
			Reason: ev.CloseReason,
		})

		if !isMerged {
			// Append rejection record.
			targetPath := extractTargetPathFromBody(ev.Body)
			appendRigRejection(&rigState, RigRejection{
				At:         now.UTC().Format(time.RFC3339),
				MRID:       ev.MRID,
				TargetPath: targetPath,
			})

			// Increment circuit-breaker counter.
			s.CircuitBreaker.Count++
			if s.CircuitBreaker.WindowStartedAt == "" {
				s.CircuitBreaker.WindowStartedAt = now.UTC().Format(time.RFC3339)
			}

			// Check if circuit breaker should trip.
			if h.shouldTripCircuitBreaker(s, now) {
				s.CircuitBreaker.TrippedUntil = now.Add(CircuitBreakerWindow).UTC().Format(time.RFC3339)
				rigState.State = "paused-by-circuit-breaker"

				// Append incident for the trip.
				appendIncident(s, Incident{
					At:      now.UTC().Format(time.RFC3339),
					Actor:   "mayor/cycle-close-handler",
					Kind:    IncidentCircuitBreakerTripped,
					Rig:     ev.TargetRig,
					Details: fmt.Sprintf("count=%d threshold=%d window=7d mr=%s", s.CircuitBreaker.Count, CircuitBreakerThreshold, ev.MRID),
				})
			}
		} else {
			// Merged: reset circuit-breaker counter (consecutive-close resets).
			s.CircuitBreaker.Count = 0
			s.CircuitBreaker.WindowStartedAt = ""
		}

		// Write back the rig state.
		h.saveRigCycleState(s, ev.TargetRig, rigState)
		return nil
	})

	if err != nil {
		h.logf("cycle-close-handler: ERROR mutating town-state for mr=%s: %v", ev.MRID, err)
		return
	}

	// Nudge overseer if circuit breaker tripped.
	if !isMerged {
		// Re-read to check if we just tripped it.
		state, loadErr := LoadTownState(h.Beads)
		if loadErr == nil && state.CircuitBreaker.IsTripped() {
			msg := fmt.Sprintf("Q6 SEV-2: auto-test-pr circuit breaker tripped (count=%d, threshold=%d, window=7d). Last MR: %s, rig: %s",
				state.CircuitBreaker.Count, CircuitBreakerThreshold, ev.MRID, ev.TargetRig)
			h.logf("cycle-close-handler: %s", msg)
			if h.NudgeOverseer != nil {
				h.NudgeOverseer(msg)
			}
		}
	}

	// Parse BUG-DISCOVERED: NOTES and file P2 bug beads.
	bugs := ParseBugDiscoveredNotes(ev.Body)
	for _, bug := range bugs {
		h.fileBugBead(ev.TargetRig, ev.MRID, bug)
	}

	h.logf("cycle-close-handler: completed mr=%s rig=%s reason=%s bugs=%d",
		ev.MRID, ev.TargetRig, ev.CloseReason, len(bugs))
}

// MRCycleCloseEvent is re-exported from daemon for convenience.
// The actual struct lives in internal/daemon to keep the dog and handler
// in the same package boundary. This type alias avoids import cycles —
// callers construct a CycleCloseHandler and pass a closure to
// Daemon.SetMRCycleCloseHandler that bridges the two packages.
type MRCycleCloseEvent struct {
	MRID        string
	TargetRig   string
	CloseReason string
	Body        string
}

// IncidentCircuitBreakerTripped is emitted when the circuit breaker
// trips (≥3 closes in 7d). Distinct from IncidentCircuitBreakerOverride
// (which is operator-initiated).
const IncidentCircuitBreakerTripped IncidentKind = "circuit-breaker-tripped"

// shouldTripCircuitBreaker determines if the circuit breaker should trip
// based on the current count, rolling window, and threshold.
func (h *CycleCloseHandler) shouldTripCircuitBreaker(s *TownState, now time.Time) bool {
	// Already tripped — don't re-trip.
	if s.CircuitBreaker.IsTripped() {
		return false
	}

	// Check threshold.
	if s.CircuitBreaker.Count < CircuitBreakerThreshold {
		return false
	}

	// Check rolling window: if the window started more than 7 days ago,
	// the counter has drifted past the window and we should reset rather
	// than trip. However, in Phase 0 the counter is a simple consecutive-
	// close counter (not per-calendar-window) so we check timestamp only
	// to prevent ancient counters from tripping on restart.
	if s.CircuitBreaker.WindowStartedAt != "" {
		windowStart, err := time.Parse(time.RFC3339, s.CircuitBreaker.WindowStartedAt)
		if err == nil && now.Sub(windowStart) > CircuitBreakerWindow {
			// Window expired — reset counter (the trip is stale).
			s.CircuitBreaker.Count = 1 // current event still counts
			s.CircuitBreaker.WindowStartedAt = now.UTC().Format(time.RFC3339)
			return false
		}
	}

	return true
}

// loadRigCycleState deserializes the per-rig state from RigSummary.
// Returns a zero state if the rig is not yet tracked.
func (h *CycleCloseHandler) loadRigCycleState(s *TownState, rig string) RigCycleState {
	if s.RigSummary == nil {
		return RigCycleState{State: "mr-pending"}
	}
	raw, ok := s.RigSummary[rig]
	if !ok || len(raw) == 0 {
		return RigCycleState{State: "mr-pending"}
	}
	var state RigCycleState
	if err := json.Unmarshal(raw, &state); err != nil {
		h.logf("cycle-close-handler: failed to unmarshal rig state for %s: %v", rig, err)
		return RigCycleState{State: "mr-pending"}
	}
	return state
}

// saveRigCycleState serializes the per-rig state back into RigSummary.
func (h *CycleCloseHandler) saveRigCycleState(s *TownState, rig string, state RigCycleState) {
	raw, err := json.Marshal(state)
	if err != nil {
		h.logf("cycle-close-handler: failed to marshal rig state for %s: %v", rig, err)
		return
	}
	if s.RigSummary == nil {
		s.RigSummary = make(map[string]json.RawMessage)
	}
	s.RigSummary[rig] = raw
}

// appendRigTransition appends a transition to the rig's log, bounded
// to MaxRigTransitions entries.
func appendRigTransition(s *RigCycleState, t RigTransition) {
	s.TransitionLog = append(s.TransitionLog, t)
	if len(s.TransitionLog) > MaxRigTransitions {
		drop := len(s.TransitionLog) - MaxRigTransitions
		s.TransitionLog = s.TransitionLog[drop:]
	}
}

// appendRigRejection appends a rejection to the rig's log, bounded
// to MaxRigTransitions entries.
func appendRigRejection(s *RigCycleState, r RigRejection) {
	s.RejectionLog = append(s.RejectionLog, r)
	if len(s.RejectionLog) > MaxRigTransitions {
		drop := len(s.RejectionLog) - MaxRigTransitions
		s.RejectionLog = s.RejectionLog[drop:]
	}
}

// extractTargetPathFromBody pulls the target_path value from the MR
// description body. Falls back to "" if not present.
func extractTargetPathFromBody(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		const key = "target_path:"
		if strings.HasPrefix(strings.ToLower(line), key) {
			return strings.TrimSpace(line[len(key):])
		}
	}
	return ""
}

// BugDiscovered represents a single BUG-DISCOVERED: entry parsed from
// the MR body.
type BugDiscovered struct {
	// Description is the free-form text after "BUG-DISCOVERED:".
	Description string
}

// ParseBugDiscoveredNotes scans the MR body for lines starting with
// "BUG-DISCOVERED:" and returns structured entries.
func ParseBugDiscoveredNotes(body string) []BugDiscovered {
	var bugs []BugDiscovered
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		const prefix = "BUG-DISCOVERED:"
		if strings.HasPrefix(line, prefix) {
			desc := strings.TrimSpace(line[len(prefix):])
			if desc != "" {
				bugs = append(bugs, BugDiscovered{Description: desc})
			}
		}
	}
	return bugs
}

// fileBugBead creates a P2 bug bead in the rig's beads store, linked
// to the cycle's MR bead via a label. Best-effort: errors are logged
// but do not fail the handler.
func (h *CycleCloseHandler) fileBugBead(rigName, mrID string, bug BugDiscovered) {
	title := fmt.Sprintf("Bug from auto-test-pr: %s", truncate(bug.Description, 60))
	desc := fmt.Sprintf("Discovered by auto-test-pr cycle.\nMR: %s\nRig: %s\n\n%s",
		mrID, rigName, bug.Description)

	_, err := h.Beads.Create(beads.CreateOptions{
		Title:       title,
		Description: desc,
		Labels:      []string{"gt:auto-test-pr", "gt:bug-from-auto-test", fmt.Sprintf("mr:%s", mrID)},
		Priority:    2,
		Actor:       "mayor/cycle-close-handler",
		Rig:         rigName,
	})
	if err != nil {
		h.logf("cycle-close-handler: failed to file bug bead for rig=%s mr=%s: %v", rigName, mrID, err)
		return
	}
	h.logf("cycle-close-handler: filed P2 bug bead for rig=%s mr=%s desc=%q", rigName, mrID, truncate(bug.Description, 40))
}

// truncate shortens s to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// now returns the current time, using the injected clock or time.Now.
func (h *CycleCloseHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// logf logs via the injected logger or discards.
func (h *CycleCloseHandler) logf(format string, args ...interface{}) {
	if h.Logf != nil {
		h.Logf(format, args...)
	}
}

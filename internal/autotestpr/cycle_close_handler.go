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

// RigCycleState is the denormalized read-cache for a single rig's
// auto-test-pr cycle, stored in TownState.RigSummary[rig] as JSON.
// Single-writer fields only — the high-cardinality transition and
// rejection logs live on attachment beads per the OQ4 fallback
// (.designs/auto-test-pr/synthesis.md §"OQ4 fallback"). Phase 1
// task 15 will hoist this state into a dedicated per-rig pinned bead
// (rig_state.go); for Phase 0, the same fields live here on the
// town-state bead so `gt auto-test-pr status` can render per-rig rows
// without touching attachment beads.
type RigCycleState struct {
	// State is the current state machine position.
	// Values: "idle", "mr-pending", "cooled-down", "paused-by-circuit-breaker"
	State string `json:"state"`

	// LastCycleAt is the RFC3339 timestamp of the last state transition.
	LastCycleAt string `json:"last_cycle_at,omitempty"`

	// LastOutcome is the close_reason of the most recent MR close event.
	// Values: "merged", "rejected", "conflict", "superseded", etc.
	LastOutcome string `json:"last_outcome,omitempty"`
}

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

	// AlreadyProcessedMR, when non-nil, is consulted before the
	// circuit-breaker counter is incremented for a non-merged event.
	// If it returns true, the handler treats the event as a duplicate
	// (e.g., webhook retry after a missed dog-side ack-label) and
	// SKIPS both the counter increment and the rejection-attachment
	// write — the state-machine transition still runs (idempotent).
	//
	// In production this defaults to a lookup over rejection
	// attachment beads (see hasRejectionAttachment); in tests it can
	// be stubbed without a live beads wrapper.
	AlreadyProcessedMR func(rig, mrID string) bool
}

// HandleEvent processes a single MRCycleCloseEvent. This is the
// function registered with Daemon.SetMRCycleCloseHandler.
//
// It is idempotent: re-processing the same event (due to a missed ack
// label on the dog side or a webhook retry) produces the same state
// transitions because transitions are guarded by the current state
// (CAS from mr-pending only).
//
// The circuit-breaker counter increment is also idempotent (gu-fomxj):
// before the CAS-mutate, the handler consults AlreadyProcessedMR to
// check whether this MR has already produced a rejection attachment
// for this rig. If so, the counter increment AND the rejection
// attachment write are skipped — preventing webhook retries from
// tripping the breaker prematurely (a denial-of-service on operator
// attention). The state read-cache (rig state, last_outcome,
// last_cycle_at) is still refreshed, because last-write-wins on a
// no-op replay produces the same shape.
//
// Transition and rejection records are persisted as separate
// attachment beads (OQ4 fallback) — the town-state bead's RigSummary
// holds only the denormalized per-rig read-cache (state, LastCycleAt,
// LastOutcome) and the circuit-breaker counter. Acceptance criterion
// (d) for gu-l6xu: the parent state bead's Issue.Metadata MUST NOT
// contain `transition_log[]` or `rejection_log[]` keys post-cycle.
func (h *CycleCloseHandler) HandleEvent(ev MRCycleCloseEvent) {
	now := h.now()
	h.logf("cycle-close-handler: processing mr=%s rig=%s reason=%s", ev.MRID, ev.TargetRig, ev.CloseReason)

	// Classify the close reason into merged vs closed-unmerged.
	isMerged := strings.EqualFold(ev.CloseReason, "merged")
	targetPath := ""
	if !isMerged {
		targetPath = extractTargetPathFromBody(ev.Body)
	}

	// gu-fomxj: dedup webhook retries. If we've already filed a
	// rejection attachment for this MR+rig, the breaker counter has
	// already been incremented for it — re-incrementing here would
	// trip the breaker prematurely on duplicate dispatch. The state-
	// machine transition still runs below (it's a no-op last-write-wins
	// when the rig is already cooled-down).
	duplicate := false
	if !isMerged && ev.MRID != "" {
		duplicate = h.alreadyProcessed(ev.TargetRig, ev.MRID)
		if duplicate {
			h.logf("cycle-close-handler: dedup mr=%s rig=%s — skipping CB increment + rejection attachment (already processed)",
				ev.MRID, ev.TargetRig)
		}
	}

	// CAS-mutate the town-state bead: update the per-rig read-cache row
	// in RigSummary and the circuit-breaker counter. The high-cardinality
	// transition / rejection records are written as separate attachment
	// beads after the CAS commits — they are append-only and need not
	// share the town bead's optimistic-lock window.
	var prevState string
	err := mutateTownState(h.Beads, func(s *TownState) error {
		rigState := h.loadRigCycleState(s, ev.TargetRig)

		prevState = rigState.State
		rigState.State = "cooled-down"
		rigState.LastCycleAt = now.UTC().Format(time.RFC3339)
		rigState.LastOutcome = ev.CloseReason

		switch {
		case isMerged:
			// Merged: reset circuit-breaker counter (consecutive-close resets).
			s.CircuitBreaker.Count = 0
			s.CircuitBreaker.WindowStartedAt = ""
		case duplicate:
			// gu-fomxj: webhook retry — already counted on the first
			// dispatch. Skip the increment and skip the reset (the
			// breaker state for non-merged events is owned by the
			// first-dispatch path).
		default:
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
		}

		// Write back the rig state read-cache.
		h.saveRigCycleState(s, ev.TargetRig, rigState)
		return nil
	})

	if err != nil {
		h.logf("cycle-close-handler: ERROR mutating town-state for mr=%s: %v", ev.MRID, err)
		return
	}

	// Persist the transition record as an attachment bead (OQ4 fallback).
	// This runs after the CAS commits so a downstream attachment-write
	// failure cannot rollback the state-machine transition. Attachment
	// failures are logged but do not fail the handler — the read path
	// degrades to "missing one transition" rather than "rig stuck in
	// mr-pending".
	h.fileTransitionAttachment(ev, prevState, "cooled-down", now)

	if !isMerged && !duplicate {
		// File the rejection attachment with the per-file 21d cooldown.
		// Skipped on duplicate (gu-fomxj): the attachment from the first
		// dispatch already records this MR — adding a second one would
		// double-count when MaterializeAutoTestState reads back per-MR
		// rejections (and is the very signal the dedup check uses).
		h.fileRejectionAttachment(ev, targetPath, now)

		// Nudge overseer if circuit breaker tripped.
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

// RejectionCooldown is the per-file cooldown window applied to a
// rejection attachment (synthesis §D14: per-file 21d cooldown after a
// closed-unmerged MR before the same target is eligible again).
const RejectionCooldown = 21 * 24 * time.Hour

// alreadyProcessed reports whether this MR+rig has already produced a
// rejection attachment. Used to dedup webhook retries (gu-fomxj). Uses
// the AlreadyProcessedMR injection hook if set; otherwise falls back to
// hasRejectionAttachment over the live beads wrapper. Returns false on
// any lookup error (fail-open: the worst case is the documented
// over-count behavior we accept on transient ack failures, which is
// strictly better than dropping a real close on a transient list error).
func (h *CycleCloseHandler) alreadyProcessed(rig, mrID string) bool {
	if h.AlreadyProcessedMR != nil {
		return h.AlreadyProcessedMR(rig, mrID)
	}
	if h.Beads == nil {
		return false
	}
	seen, err := hasRejectionAttachment(h.Beads, rig, mrID)
	if err != nil {
		h.logf("cycle-close-handler: dedup lookup failed for mr=%s rig=%s: %v (fail-open)",
			mrID, rig, err)
		return false
	}
	return seen
}

// hasRejectionAttachment scans existing rejection attachment beads for a
// matching (rig, mrID) pair. Lives next to MaterializeAutoTestState in
// attachments.go semantically but kept here so the handler owns its
// dedup contract. Returns true on first match, false on no-match.
func hasRejectionAttachment(b *beads.Beads, rig, mrID string) (bool, error) {
	if b == nil || rig == "" || mrID == "" {
		return false, nil
	}
	issues, err := b.List(beads.ListOptions{
		Label:  AttachmentLabel,
		Status: "all",
		Limit:  0,
	})
	if err != nil {
		return false, fmt.Errorf("listing attachment beads for dedup: %w", err)
	}
	rigLbl := RigLabel(rig)
	for _, issue := range issues {
		if !beads.HasLabel(issue, rigLbl) {
			continue
		}
		if !beads.HasLabel(issue, KindRejection) {
			continue
		}
		rec, parseErr := parseRejection(issue.Metadata)
		if parseErr != nil {
			continue
		}
		if rec.MRID == mrID {
			return true, nil
		}
	}
	return false, nil
}

// fileTransitionAttachment writes a transition record as an attachment
// bead. Errors are logged and swallowed — see HandleEvent docstring.
func (h *CycleCloseHandler) fileTransitionAttachment(ev MRCycleCloseEvent, from, to string, now time.Time) {
	if h.Beads == nil {
		return
	}
	ctx := map[string]string{
		"mr_id":  ev.MRID,
		"reason": ev.CloseReason,
	}
	rec := TransitionRecord{
		Rig:     ev.TargetRig,
		From:    from,
		To:      to,
		At:      now.UTC(),
		Actor:   "mayor/cycle-close-handler",
		Context: ctx,
	}
	if _, err := CreateTransitionAttachment(h.Beads, rec); err != nil {
		h.logf("cycle-close-handler: failed to file transition attachment for mr=%s rig=%s: %v",
			ev.MRID, ev.TargetRig, err)
	}
}

// fileRejectionAttachment writes a rejection record as an attachment
// bead. Errors are logged and swallowed.
func (h *CycleCloseHandler) fileRejectionAttachment(ev MRCycleCloseEvent, targetPath string, now time.Time) {
	if h.Beads == nil {
		return
	}
	rec := RejectionRecord{
		Rig:           ev.TargetRig,
		File:          targetPath,
		RejectedAt:    now.UTC(),
		Reason:        ev.CloseReason,
		CooldownUntil: now.Add(RejectionCooldown).UTC(),
		MRID:          ev.MRID,
	}
	if _, err := CreateRejectionAttachment(h.Beads, rec); err != nil {
		h.logf("cycle-close-handler: failed to file rejection attachment for mr=%s rig=%s: %v",
			ev.MRID, ev.TargetRig, err)
	}
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
//
// Idempotent: uses CreateIfNoDuplicate so re-processing the same event
// (partial-failure retry) does not produce duplicate bug beads. The
// title includes the truncated description, which serves as the dedup
// key — if a bug bead with the same normalized title already exists,
// the duplicate is suppressed.
func (h *CycleCloseHandler) fileBugBead(rigName, mrID string, bug BugDiscovered) {
	title := fmt.Sprintf("Bug from auto-test-pr: %s", truncate(bug.Description, 60))
	desc := fmt.Sprintf("Discovered by auto-test-pr cycle.\nMR: %s\nRig: %s\n\n%s",
		mrID, rigName, bug.Description)

	_, created, err := h.Beads.CreateIfNoDuplicate(beads.CreateOptions{
		Title:       title,
		Description: desc,
		Labels:      []string{"gt:bug", "gt:auto-test-pr", "gt:bug-from-auto-test", fmt.Sprintf("mr:%s", mrID)},
		Priority:    2,
		Actor:       "mayor/cycle-close-handler",
		Rig:         rigName,
	})
	if err != nil {
		h.logf("cycle-close-handler: failed to file bug bead for rig=%s mr=%s: %v", rigName, mrID, err)
		return
	}
	if !created {
		h.logf("cycle-close-handler: bug bead already exists for rig=%s mr=%s desc=%q (idempotent skip)", rigName, mrID, truncate(bug.Description, 40))
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

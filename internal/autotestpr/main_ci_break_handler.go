// Mayor SEV-1 handler for main-CI-break events on auto-test-pr-opted-in
// rigs.
//
// Phase 0 task 11 (gu-36voy) — D16 SEV-1 auto-revert chain. Consumes
// MainCIBreakEvent dispatched by the main_ci_break_dog daemon patrol
// (internal/daemon/main_ci_break_dog.go) and runs the four-step SEV-1
// response from synthesis §D16:
//
//	(a) file a revert MR in the rig (best-effort; logged on failure).
//	(b) CAS-transition the rig state to `paused-by-circuit-breaker`,
//	    set `circuit_breaker.tripped_until = now + 7d`, and increment
//	    the town-wide circuit-breaker counter.
//	(c) append an Incident to the town-state's audit log naming the
//	    breaking commit, MR bead, and escalation.
//	(d) nudge the Overseer with the SEV-1 payload.
//
// "Re-shape required" note from gu-36voy: the original synthesis spoke
// of Mayor as a programmatic event consumer; the realistic owner is a
// daemon dog (main_ci_break_dog, gu-15c8 / gu-grkl). This handler is
// the autotestpr-package half — the daemon-package half wires it via
// SetMainCIBreakHandler in main_ci_break_handler_wire.go.
//
// Architectural pattern: identical to CycleCloseHandler — dependency-
// injected callbacks (nudge, revert filing), pinned clock for tests,
// CAS-safe town-state mutation, idempotent re-processing.
package autotestpr

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// MainCIBreakEvent is the autotestpr-package shape of the daemon's
// MainCIBreakEvent. The wiring layer translates the daemon's struct
// into this one to avoid an autotestpr → daemon import cycle (mirrors
// the MRCycleCloseEvent / cycle_close_handler_wire pattern).
type MainCIBreakEvent struct {
	// RigName is the rig whose main branch broke.
	RigName string

	// CommitSHA is the breaking commit on origin/<default_branch>.
	CommitSHA string

	// PreviousSHA is the last passing commit (or "unknown" if first run).
	PreviousSHA string

	// MRBeadID is the MR bead the breaking commit landed via — already
	// confirmed by the dog to carry the gt:auto-test-pr label.
	MRBeadID string

	// EscalationID is the source main_branch_test escalation bead ID.
	EscalationID string

	// Body is the full escalation description (informational; the
	// handler does not parse it further).
	Body string
}

// IncidentMainCIBreak is appended to the town-state audit log when the
// SEV-1 handler fires. Distinct from IncidentCircuitBreakerTripped
// (which is the cycle-close-driven path) because the trigger is an
// upstream main-CI-break, not three closes-in-7d.
const IncidentMainCIBreak IncidentKind = "main-ci-break-sev1"

// MainCIBreakRevertFiler is the callback used by the SEV-1 handler to
// file the auto-revert task in the rig's bead store. The implementation
// shells out to `bd create` (or the in-process beadsdk equivalent);
// returning an error is best-effort logged but does NOT abort the
// remaining SEV-1 chain (state-machine pause + CB increment + Overseer
// nudge are higher-priority recovery actions than the revert MR
// itself, which an operator can file manually if this fails).
//
// rigName / mrBeadID / commitSHA / previousSHA are passed through so
// the implementation can compose a deterministic bead title and
// description that reference the originating MR + commit pair.
type MainCIBreakRevertFiler func(rigName, mrBeadID, commitSHA, previousSHA, escalationID string) error

// MainCIBreakHandler holds the dependencies for processing main-CI-break
// events. Initialized once at daemon startup (after beads stores are
// open) and registered via Daemon.SetMainCIBreakHandler.
type MainCIBreakHandler struct {
	// Beads is the beads wrapper for the town root. Used for CAS
	// mutations on the town-state bead.
	Beads *beads.Beads

	// FileRevert files a revert MR/task. Optional — when nil, the
	// handler skips the revert step but still runs the rest of the
	// SEV-1 chain (state pause, CB increment, Overseer nudge).
	FileRevert MainCIBreakRevertFiler

	// NudgeOverseer is called with the SEV-1 payload after the state
	// mutation commits. In production this shells out to
	// `gt nudge overseer`; in tests it captures the message.
	NudgeOverseer func(msg string)

	// Now returns the current time. Injected so tests can pin it.
	Now func() time.Time

	// Logf is a printf-style logger. In production this is the daemon's
	// logger.Printf; in tests it captures log lines.
	Logf func(format string, args ...interface{})
}

// HandleEvent runs the D16 SEV-1 chain for a single main-CI-break event.
// Idempotent on re-process: the CAS update to `paused-by-circuit-breaker`
// is a no-op once already in that state (state-guarded transition). The
// CB counter increment and Incident append are NOT idempotent on repeat —
// the dog's ack-label mechanism (acked-by-ci-break-dog) prevents
// re-dispatch in the normal case; the rare transient-ack-failure path
// accepts a slight over-count as the lesser evil vs. complex distributed
// dedup, mirroring CycleCloseHandler's design.
//
// The handler intentionally runs the state mutation FIRST so that even
// if the revert filing or Overseer nudge later fails, the rig is
// already paused — the point of D16 is to halt further auto-test-pr
// activity on the broken rig, not to guarantee the revert MR lands
// (operators can file a revert manually; they cannot easily re-create a
// missed `paused-by-circuit-breaker` row).
func (h *MainCIBreakHandler) HandleEvent(ev MainCIBreakEvent) {
	now := h.now()
	h.logf("main-ci-break-handler: processing rig=%s commit=%s mr=%s escalation=%s",
		ev.RigName, shortSHA(ev.CommitSHA), ev.MRBeadID, ev.EscalationID)

	// (b)+(c) Run the state mutation as a single CAS so the rig pause,
	// CB counter increment, and audit-log entry land atomically. If we
	// raced between them, an operator inspecting `gt auto-test-pr
	// status` mid-update could see a paused rig with no incident
	// recorded — confusing during a SEV-1 fire-drill.
	var alreadyTripped bool
	err := mutateTownState(h.Beads, func(s *TownState) error {
		alreadyTripped = s.CircuitBreaker.IsTripped()

		rigState := h.loadRigCycleState(s, ev.RigName)
		// State guard: only transition out of non-paused states. If the
		// rig is already in `paused-by-circuit-breaker`, leave the state
		// untouched but still record the fresh incident — operators want
		// to see "this rig broke twice during the cooldown" in the audit
		// log even when the state itself doesn't move.
		prevState := rigState.State
		if prevState != "paused-by-circuit-breaker" {
			rigState.State = "paused-by-circuit-breaker"
			rigState.LastCycleAt = now.UTC().Format(time.RFC3339)
			rigState.LastOutcome = "main-ci-break"
		}

		// Always set TrippedUntil to now+7d on a SEV-1 — this is the
		// canonical "circuit breaker tripped" signal even if the
		// counter-driven trip path hasn't fired yet. Using the larger
		// of (existing TrippedUntil, now+7d) so a successful SEV-1 can
		// extend an in-flight cooldown but never shorten it.
		newUntil := now.Add(CircuitBreakerWindow)
		extendCircuitBreakerUntil(s, newUntil)

		// Increment the town-wide consecutive-close counter so the
		// `gt auto-test-pr status` table surfaces a non-zero count
		// during the SEV-1 — operators expect parity with the cycle-
		// close-driven path. WindowStartedAt is preserved if non-empty.
		s.CircuitBreaker.Count++
		if s.CircuitBreaker.WindowStartedAt == "" {
			s.CircuitBreaker.WindowStartedAt = now.UTC().Format(time.RFC3339)
		}

		appendIncident(s, Incident{
			At:    now.UTC().Format(time.RFC3339),
			Actor: "mayor/main-ci-break-handler",
			Kind:  IncidentMainCIBreak,
			Rig:   ev.RigName,
			Details: fmt.Sprintf("commit=%s previous=%s mr=%s escalation=%s",
				shortSHA(ev.CommitSHA), shortSHA(ev.PreviousSHA), ev.MRBeadID, ev.EscalationID),
		})

		h.saveRigCycleState(s, ev.RigName, rigState)
		return nil
	})

	if err != nil {
		h.logf("main-ci-break-handler: ERROR mutating town-state for rig=%s commit=%s: %v",
			ev.RigName, shortSHA(ev.CommitSHA), err)
		return
	}

	// (a) File the revert task. Best-effort: failures are logged but do
	// NOT roll back the state mutation. Synthesis §D16 (a) names this
	// the "revert MR via the existing revert-MR formula"; in Phase 0
	// the existing flow is a rig-level task bead that an operator picks
	// up — there is no Mayor-driven git revert today (the Phase 1
	// revert-MR formula lands later). Filing the task is what the
	// operator-facing runbook (Phase 0 task 12) reads off of.
	if h.FileRevert != nil {
		if rerr := h.FileRevert(ev.RigName, ev.MRBeadID, ev.CommitSHA, ev.PreviousSHA, ev.EscalationID); rerr != nil {
			h.logf("main-ci-break-handler: failed to file revert task for rig=%s mr=%s: %v",
				ev.RigName, ev.MRBeadID, rerr)
		}
	}

	// (d) Nudge the Overseer with the SEV-1 payload. This is the human
	// notification surface — synthesis §D16 names "Overseer" as the
	// human-routing target; today that maps to a `gt nudge overseer`
	// call on the wiring side. Best-effort: nudge failures are logged
	// but the state has already been recorded.
	if h.NudgeOverseer != nil {
		msg := h.formatSEV1Payload(ev, alreadyTripped)
		h.NudgeOverseer(msg)
	}

	h.logf("main-ci-break-handler: completed rig=%s commit=%s mr=%s already_tripped=%v",
		ev.RigName, shortSHA(ev.CommitSHA), ev.MRBeadID, alreadyTripped)
}

// formatSEV1Payload composes the human-readable Overseer nudge body.
// Includes commit / previous-commit / MR / escalation IDs so the
// operator can jump straight from the nudge into `bd show <id>` or
// `git log` without re-deriving anything.
func (h *MainCIBreakHandler) formatSEV1Payload(ev MainCIBreakEvent, alreadyTripped bool) string {
	cb := "newly-tripped"
	if alreadyTripped {
		cb = "extended-cooldown"
	}
	return fmt.Sprintf(
		"D16 SEV-1: auto-test-pr broke main on rig=%s. commit=%s previous=%s mr=%s escalation=%s circuit_breaker=%s. "+
			"State: paused-by-circuit-breaker (7d cooldown). Manual recovery: gt auto-test-pr resume --rig=%s --override-circuit-breaker.",
		ev.RigName,
		shortSHA(ev.CommitSHA),
		shortSHA(ev.PreviousSHA),
		ev.MRBeadID,
		ev.EscalationID,
		cb,
		ev.RigName,
	)
}

// loadRigCycleState mirrors CycleCloseHandler.loadRigCycleState — same
// RigSummary read path, same default. Duplicated rather than shared
// because the two handlers may evolve independently (e.g., the cycle-
// close path's `mr-pending` default is a side-effect of its caller's
// state-machine context, which doesn't apply here).
func (h *MainCIBreakHandler) loadRigCycleState(s *TownState, rig string) RigCycleState {
	if s.RigSummary == nil {
		return RigCycleState{}
	}
	raw, ok := s.RigSummary[rig]
	if !ok || len(raw) == 0 {
		return RigCycleState{}
	}
	var state RigCycleState
	if err := json.Unmarshal(raw, &state); err != nil {
		h.logf("main-ci-break-handler: failed to unmarshal rig state for %s: %v", rig, err)
		return RigCycleState{}
	}
	return state
}

// saveRigCycleState mirrors CycleCloseHandler.saveRigCycleState.
func (h *MainCIBreakHandler) saveRigCycleState(s *TownState, rig string, state RigCycleState) {
	raw, err := json.Marshal(state)
	if err != nil {
		h.logf("main-ci-break-handler: failed to marshal rig state for %s: %v", rig, err)
		return
	}
	if s.RigSummary == nil {
		s.RigSummary = map[string]json.RawMessage{}
	}
	s.RigSummary[rig] = raw
}

// extendCircuitBreakerUntil sets s.CircuitBreaker.TrippedUntil to the
// later of (current TrippedUntil, until). Stops a SEV-1 from
// accidentally shortening a longer cooldown set by an earlier event;
// guarantees that operator override is the only path to clear the
// state earlier.
func extendCircuitBreakerUntil(s *TownState, until time.Time) {
	target := until.UTC().Format(time.RFC3339)
	if s.CircuitBreaker.TrippedUntil == "" {
		s.CircuitBreaker.TrippedUntil = target
		return
	}
	existing, err := time.Parse(time.RFC3339, s.CircuitBreaker.TrippedUntil)
	if err != nil || until.After(existing) {
		s.CircuitBreaker.TrippedUntil = target
	}
}

// shortSHA truncates a 40-char SHA to 12 chars for log/payload
// readability. Tolerates the "unknown" sentinel (PreviousSHA on a
// first run) and shorter inputs by returning the input verbatim.
func shortSHA(sha string) string {
	if sha == "" || sha == "unknown" {
		return sha
	}
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

// now returns the current time, using the injected clock or time.Now.
func (h *MainCIBreakHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// logf logs via the injected logger or discards.
func (h *MainCIBreakHandler) logf(format string, args ...interface{}) {
	if h.Logf != nil {
		h.Logf(format, args...)
	}
}

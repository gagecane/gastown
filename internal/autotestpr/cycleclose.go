// Mayor cycle-close handler (Phase 0 task 3c, gu-xrxm6).
//
// Consumes classified MR-cycle-close events from the substrate dog
// (mr_cycle_close_dog, gu-h1fn) and implements the four cycle-close paths
// described in .designs/auto-test-pr/synthesis.md §"Phase 0 task 3c":
//
//  1. merged: CAS the rig's <rig>-auto-test-state bead mr-pending →
//     cooled-down and append a transition attachment bead.
//  2. closed-unmerged: CAS to cooled-down, append both a transition
//     attachment AND a rejection attachment, increment the town-bead
//     circuit-breaker counter, and — if the rig has ≥3 closes in any
//     rolling 7-day window — CAS to paused-by-circuit-breaker and notify
//     Overseer (Q6 SEV-2).
//  3. either path: parse BUG-DISCOVERED: NOTES out of the MR body and
//     file a P2 bug bead per occurrence, linked to the cycle's MR bead.
//
// Design notes / OQ4 fallback. Per the synthesis doc, the high-cardinality
// transition_log and rejection_log do NOT live in the per-rig pinned
// bead's Issue.Metadata — they live as one immutable attachment bead per
// transition / rejection (created via bd create, naturally CAS-safe). The
// 7-day rolling window for the circuit-breaker is computed by listing
// the rig's transition attachments and folding by their `at` timestamps,
// not by reading a counter off the parent state bead. The town-bead
// circuit-breaker counter on the town pinned bead IS a single-writer
// metadata field (Mayor's cycle-close handler is the sole writer); we
// touch it via the merge-helper, not via blob-RMW.
//
// O(1) state-bead lookup. The handler resolves the per-rig state bead via
// the deterministic ID pattern <rig>-auto-test-state (see data.md §"Pinned
// state bead per rig"). The rig name is supplied by the caller — extracted
// from the MR bead's rig:<target_rig> label by the substrate dog (round 3
// fix #6). No bead-graph walk; no list-by-label query against the rig's
// MR beads.
package autotestpr

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CloseReason is the merge-outcome value the cycle-close handler dispatches
// on. Only the two values that drive state-bead transitions are encoded as
// constants; other strings (e.g., "superseded", "conflict") fall through
// the merged-vs-closed switch as "neither merged nor a known close".
const (
	// CloseReasonMerged is the close_reason value set on the MR bead when
	// the merge queue successfully merged the cycle's MR. Drives the
	// merged-path transition (mr-pending → cooled-down, transition record
	// only).
	CloseReasonMerged = "merged"

	// CloseReasonRejected is the close_reason set when the MR was closed
	// unmerged (manual close, refinery rejection, etc.). Drives the
	// closed-unmerged path: transition + rejection records, town-bead
	// circuit-breaker increment, and the rolling-7-day pause check.
	CloseReasonRejected = "rejected"
)

// Per-rig state bead state values. Wire-only — Phase 0 task 15 owns the
// real RigState type; this handler only ever sets `state` to one of
// `cooled-down` or `paused-by-circuit-breaker`, and only ever transitions
// from `mr-pending`.
const (
	rigStateMRPending             = "mr-pending"
	rigStateCooledDown            = "cooled-down"
	rigStatePausedByCircuitBreaker = "paused-by-circuit-breaker"
)

// Attachment bead label values per .designs/auto-test-pr/synthesis.md
// §"Per-kind metadata payloads". The umbrella discriminator is reused
// across kinds; the kind:* discriminator is exclusive per attachment.
const (
	labelAttachmentUmbrella = "gt:auto-test-pr-attachment"
	labelKindTransition     = "kind:transition"
	labelKindRejection      = "kind:rejection"
	labelAutoTestPRUmbrella = "gt:auto-test-pr"
	labelRigPrefix          = "rig:"
)

// Round 3 fix #2 / Q6 SEV-2 thresholds. Hardcoded constants here rather
// than pulling from the town-state bead because the design fixes both
// values: 3 closes in any 7-day window trips the breaker (synthesis.md
// table at line 476). Per-rig threshold; town-wide threshold is a
// separate (future) concern.
const (
	// circuitBreakerCloseThreshold is the close-count that, when reached
	// in any rolling closeWindowSize period, trips the per-rig breaker.
	circuitBreakerCloseThreshold = 3

	// closeWindowSize is the rolling-window length the breaker measures
	// over. 7 days per Q6 / R1 design.
	closeWindowSize = 7 * 24 * time.Hour

	// rejectionCooldownPerFile is the per-file cooldown the rejection
	// attachment records as `cooldown_until = rejected_at + 21d`. Per
	// data.md §"Pinned state bead", `rejections[].cooldown_until`.
	rejectionCooldownPerFile = 21 * 24 * time.Hour
)

// Transition is the JSON payload of a kind:transition attachment bead
// (Issue.Metadata). Mirrors the schema in .designs/auto-test-pr/
// synthesis.md §"Per-kind metadata payloads" / kind:transition.
type Transition struct {
	SchemaVersion int                    `json:"schema_version"`
	Rig           string                 `json:"rig"`
	From          string                 `json:"from"`
	To            string                 `json:"to"`
	At            string                 `json:"at"`
	Actor         string                 `json:"actor"`
	Context       map[string]interface{} `json:"context,omitempty"`
}

// Rejection is the JSON payload of a kind:rejection attachment bead.
// Mirrors the schema in synthesis.md §"Per-kind metadata payloads" /
// kind:rejection.
type Rejection struct {
	SchemaVersion int    `json:"schema_version"`
	Rig           string `json:"rig"`
	File          string `json:"file"`
	RejectedAt    string `json:"rejected_at"`
	Reason        string `json:"reason"`
	CooldownUntil string `json:"cooldown_until"`
	MRID          string `json:"mr_id"`
}

// CycleCloseEvent is the data the dog hands to the handler. Mirrors the
// daemon.MRCycleCloseEvent shape but lives in this package so the handler
// can be unit-tested without importing internal/daemon. The wiring at
// startup time adapts daemon.MRCycleCloseEvent → autotestpr.CycleCloseEvent.
type CycleCloseEvent struct {
	// MRID is the merge-request bead ID.
	MRID string

	// TargetRig is the rig that owned the auto-test-pr cycle (read from
	// the rig:<target_rig> label by the substrate dog).
	TargetRig string

	// CloseReason is the merge outcome ("merged", "rejected", ...). The
	// handler dispatches on this value.
	CloseReason string

	// Body is the full MR-bead description, parsed for BUG-DISCOVERED:
	// blocks per the bug-discovery protocol (synthesis §"Bug-discovery").
	Body string
}

// BeadsClient is the minimum surface the cycle-close handler needs from
// the beads layer. Defining a narrow interface here lets unit tests
// inject a fake without standing up a real Dolt server. The production
// adapter (NewBeadsClientFromBeads) wraps *beads.Beads.
type BeadsClient interface {
	// ShowMetadata returns the Metadata blob for the given bead ID. If
	// the bead does not exist, it returns ErrBeadNotFound.
	ShowMetadata(id string) (json.RawMessage, error)

	// UpdateMetadata replaces the entire Metadata blob on the given bead.
	// REPLACE semantics; concurrent writers will lose data unless the
	// caller is single-writer. The handler is documented as Mayor-only,
	// so this constraint holds.
	UpdateMetadata(id string, raw json.RawMessage) error

	// CreateAttachment files a new attachment bead with the given title,
	// labels, parent (depends_on edge), and metadata. Returns the new
	// bead's ID. CAS-safe by construction — bd create mints a new ID per
	// call so concurrent callers never clobber.
	CreateAttachment(title string, labels []string, parentID string, metadata json.RawMessage) (string, error)

	// CreateBugBead files a P2 bug bead linked to the given MR ID. Used
	// for the BUG-DISCOVERED: protocol. Returns the new bead's ID.
	CreateBugBead(title, body string, parentID string, labels []string) (string, error)

	// ListTransitionsForRig returns the parsed transition attachments for
	// the given rig (kind:transition + rig:<rig> labels). Used by the
	// rolling-7d circuit-breaker check.
	ListTransitionsForRig(rig string) ([]Transition, error)
}

// ErrBeadNotFound is returned by BeadsClient.ShowMetadata when the bead
// does not exist.
var ErrBeadNotFound = errors.New("bead not found")

// Notifier is the Overseer-notification surface. Production adapter wraps
// `gt nudge` with --immediate to ensure the SEV-2 event lands. Unit tests
// inject a recorder to assert that the breaker-trip path fires it.
type Notifier interface {
	// NotifyOverseer sends a SEV-2 nudge to the Overseer. Returns the
	// underlying notification error if delivery fails; the handler logs
	// but does not fail the close path on a notify failure (the state
	// transition has already landed and is the source of truth).
	NotifyOverseer(subject, body string) error
}

// CycleCloseHandler implements the four cycle-close paths described in
// the package-level docstring. Construct via NewCycleCloseHandler.
type CycleCloseHandler struct {
	beads    BeadsClient
	notifier Notifier
	log      func(format string, args ...interface{})

	// nowFn returns the current time. Tests inject a fixed clock so the
	// rolling-7d window check is deterministic.
	nowFn func() time.Time
}

// CycleCloseHandlerOption tunes a CycleCloseHandler at construction.
type CycleCloseHandlerOption func(*CycleCloseHandler)

// WithNowFunc overrides the clock. Used by tests.
func WithNowFunc(now func() time.Time) CycleCloseHandlerOption {
	return func(h *CycleCloseHandler) { h.nowFn = now }
}

// WithLogger overrides the structured logger. Defaults to a no-op.
func WithLogger(log func(format string, args ...interface{})) CycleCloseHandlerOption {
	return func(h *CycleCloseHandler) { h.log = log }
}

// NewCycleCloseHandler builds the handler with the given dependencies.
// beads and notifier may not be nil. Both options are applied in order.
func NewCycleCloseHandler(beads BeadsClient, notifier Notifier, opts ...CycleCloseHandlerOption) *CycleCloseHandler {
	h := &CycleCloseHandler{
		beads:    beads,
		notifier: notifier,
		log:      func(format string, args ...interface{}) {},
		nowFn:    time.Now,
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// RigStateBeadID is the deterministic ID of the per-rig state bead. The
// caller passes a rig name; the bead at <rig>-auto-test-state is
// authoritative for that rig. Round 3 fix #6: the dog reads
// rig:<target_rig> off the MR bead and the handler uses it here — no
// bead-graph walk.
func RigStateBeadID(rig string) string {
	return rig + "-auto-test-state"
}

// Handle dispatches the cycle-close event through the four design paths.
// Returns an error only on infrastructure failures that prevent any
// progress; recoverable inconsistencies (e.g., a missing transition
// attachment that fails to write while the state CAS succeeds) are
// logged and surfaced individually so the caller can decide whether to
// retry. The handler is idempotent at the substrate-dog level (the dog
// writes a fingerprint label after a successful dispatch); within a
// single Handle call, ordering matters: the state CAS happens first so
// a downstream attachment-write failure cannot make the state look
// "merged" when the rig is actually still in mr-pending.
func (h *CycleCloseHandler) Handle(ev CycleCloseEvent) error {
	if ev.TargetRig == "" {
		return fmt.Errorf("cycle-close: empty target rig (mr=%s)", ev.MRID)
	}
	if ev.CloseReason == "" {
		return fmt.Errorf("cycle-close: empty close_reason (mr=%s)", ev.MRID)
	}

	now := h.nowFn().UTC()
	rig := ev.TargetRig

	// All paths first parse BUG-DISCOVERED: NOTES and file P2 bug beads.
	// We do this BEFORE the state transition so a buggy NOTES parser can
	// be observed even if the state CAS later fails — and so a transient
	// state-CAS failure doesn't prevent us from filing the bug bead the
	// human needs to see. Bug-bead creation is best-effort: errors are
	// logged but do not block the cycle-close on the state machine.
	bugs := ParseBugDiscovered(ev.Body)
	for i, bug := range bugs {
		bugTitle := fmt.Sprintf("%s-bug-from-auto-test #%d: %s", rig, i+1, bug.Summary())
		bugBody := bug.Body(ev.MRID, rig)
		bugLabels := []string{"gt:bug", labelAutoTestPRUmbrella, labelRigPrefix + rig, "bug-discovered"}
		if _, err := h.beads.CreateBugBead(bugTitle, bugBody, ev.MRID, bugLabels); err != nil {
			h.log("cycle-close: %s: failed to file BUG-DISCOVERED bug bead %d: %v",
				ev.MRID, i+1, err)
			// Continue; bug filing is best-effort.
		} else {
			h.log("cycle-close: %s: filed BUG-DISCOVERED bug bead (idx=%d)", ev.MRID, i+1)
		}
	}

	switch ev.CloseReason {
	case CloseReasonMerged:
		return h.handleMerged(ev, rig, now)
	case CloseReasonRejected:
		return h.handleRejected(ev, rig, now)
	default:
		// Per design: only "merged" and "rejected" map to state changes.
		// Other close reasons (e.g., "superseded") are accepted but no
		// state transition is performed. Log so operators have a trail.
		h.log("cycle-close: %s: unhandled close_reason %q — no state change",
			ev.MRID, ev.CloseReason)
		return nil
	}
}

// handleMerged implements the merged path:
//
//  1. CAS-transition rig state mr-pending → cooled-down.
//  2. File one transition attachment.
func (h *CycleCloseHandler) handleMerged(ev CycleCloseEvent, rig string, now time.Time) error {
	stateID := RigStateBeadID(rig)
	if err := h.casRigState(stateID, rig, rigStateMRPending, rigStateCooledDown); err != nil {
		return fmt.Errorf("merged-path CAS rig state: %w", err)
	}
	tr := Transition{
		SchemaVersion: 1,
		Rig:           rig,
		From:          rigStateMRPending,
		To:            rigStateCooledDown,
		At:            now.Format(time.RFC3339),
		Actor:         "refinery",
		Context:       map[string]interface{}{"mr_id": ev.MRID},
	}
	if err := h.fileTransitionAttachment(stateID, rig, tr); err != nil {
		// Log but do not return — the state has already moved. The next
		// dog tick will not retry (ack label was written), so a failure
		// here is observable only in logs. The materializer surfaces
		// inconsistency naturally (state=cooled-down with no recent
		// transition record).
		h.log("cycle-close: %s: state CAS landed but transition attachment failed: %v",
			ev.MRID, err)
	}
	return nil
}

// handleRejected implements the closed-unmerged path:
//
//  1. CAS-transition rig state mr-pending → cooled-down.
//  2. File one transition attachment.
//  3. File one rejection attachment with cooldown_until = now + 21d.
//  4. Increment town-bead circuit-breaker counter.
//  5. If rig has ≥3 closed-unmerged transitions in the rolling 7-day
//     window, CAS-transition rig state cooled-down →
//     paused-by-circuit-breaker and notify Overseer.
func (h *CycleCloseHandler) handleRejected(ev CycleCloseEvent, rig string, now time.Time) error {
	stateID := RigStateBeadID(rig)
	if err := h.casRigState(stateID, rig, rigStateMRPending, rigStateCooledDown); err != nil {
		return fmt.Errorf("closed-unmerged CAS rig state: %w", err)
	}

	tr := Transition{
		SchemaVersion: 1,
		Rig:           rig,
		From:          rigStateMRPending,
		To:            rigStateCooledDown,
		At:            now.Format(time.RFC3339),
		Actor:         "refinery",
		Context:       map[string]interface{}{"mr_id": ev.MRID, "outcome": "rejected"},
	}
	if err := h.fileTransitionAttachment(stateID, rig, tr); err != nil {
		h.log("cycle-close: %s: rejection-path transition attachment failed: %v",
			ev.MRID, err)
	}

	rj := Rejection{
		SchemaVersion: 1,
		Rig:           rig,
		File:          extractTargetPath(ev.Body),
		RejectedAt:    now.Format(time.RFC3339),
		Reason:        "closed-unmerged",
		CooldownUntil: now.Add(rejectionCooldownPerFile).Format(time.RFC3339),
		MRID:          ev.MRID,
	}
	if err := h.fileRejectionAttachment(stateID, rig, rj); err != nil {
		h.log("cycle-close: %s: rejection-path rejection attachment failed: %v",
			ev.MRID, err)
	}

	// Town-bead circuit-breaker counter. Single-writer field on the town
	// pinned bead — the handler is the sole writer for this counter, per
	// the OQ4 spike acceptance #1 (single-writer sequential round-trips
	// are reliable). Failure here is non-fatal for the cycle: log and
	// continue. The town-bead counter is an aggregate display surface;
	// the per-rig 7-day check below uses transition attachments, which
	// is the authoritative path.
	if err := h.incrementTownCircuitBreakerCounter(); err != nil {
		h.log("cycle-close: %s: failed to increment town circuit-breaker counter: %v",
			ev.MRID, err)
	}

	// Per-rig 7-day rolling-window check. We list the rig's transition
	// attachments, count the ones with `outcome=rejected` whose `at`
	// falls within now − closeWindowSize, and trip the breaker if the
	// count meets the threshold. The transition attachment we just wrote
	// IS in the list (it was created above), so this is inclusive of the
	// current event.
	closes, err := h.recentRejectionCount(rig, now)
	if err != nil {
		h.log("cycle-close: %s: rolling-window query failed: %v — skipping breaker check",
			ev.MRID, err)
		return nil
	}
	if closes >= circuitBreakerCloseThreshold {
		// CAS to paused-by-circuit-breaker. The "from" state is
		// cooled-down because we just transitioned to it above.
		if err := h.casRigState(stateID, rig, rigStateCooledDown, rigStatePausedByCircuitBreaker); err != nil {
			h.log("cycle-close: %s: breaker CAS cooled-down → paused-by-circuit-breaker failed: %v",
				ev.MRID, err)
			return nil
		}
		breakerTr := Transition{
			SchemaVersion: 1,
			Rig:           rig,
			From:          rigStateCooledDown,
			To:            rigStatePausedByCircuitBreaker,
			At:            now.Format(time.RFC3339),
			Actor:         "mayor",
			Context: map[string]interface{}{
				"trigger":      "circuit-breaker",
				"closes_in_7d": closes,
				"mr_id":        ev.MRID,
			},
		}
		if err := h.fileTransitionAttachment(stateID, rig, breakerTr); err != nil {
			h.log("cycle-close: %s: breaker-path transition attachment failed: %v",
				ev.MRID, err)
		}

		subject := fmt.Sprintf("[SEV-2] auto-test-pr circuit breaker tripped: %s", rig)
		body := fmt.Sprintf(
			"Rig %s has %d closed-unmerged auto-test-pr MRs in the last 7 days "+
				"(threshold: %d). The rig has been paused. Resume with:\n\n"+
				"  gt auto-test-pr resume --rig=%s --override-circuit-breaker\n\n"+
				"Triggering MR: %s",
			rig, closes, circuitBreakerCloseThreshold, rig, ev.MRID,
		)
		if err := h.notifier.NotifyOverseer(subject, body); err != nil {
			h.log("cycle-close: %s: Overseer notify failed: %v", ev.MRID, err)
		}
	}
	return nil
}

// casRigState reads the per-rig state bead, asserts that its current
// `state` field equals expectedFrom, and writes the bead back with state
// set to to. The "CAS" here is logical: we read-then-write under the
// single-writer Mayor invariant rather than relying on a Dolt-level
// SERIALIZABLE transaction. If two Mayor handlers ever ran concurrently,
// this would be a lost-update — but the design forbids that, so the
// invariant holds.
func (h *CycleCloseHandler) casRigState(stateID, rig, expectedFrom, to string) error {
	raw, err := h.beads.ShowMetadata(stateID)
	if err != nil {
		return fmt.Errorf("read rig-state bead %s: %w", stateID, err)
	}

	state, err := decodeRigState(raw)
	if err != nil {
		return fmt.Errorf("decode rig-state %s: %w", stateID, err)
	}

	if state.State != expectedFrom {
		return fmt.Errorf("rig-state CAS failed for %s: expected from=%q, found %q",
			stateID, expectedFrom, state.State)
	}
	state.State = to
	state.Rig = rig

	out, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal updated rig-state: %w", err)
	}
	if err := h.beads.UpdateMetadata(stateID, out); err != nil {
		return fmt.Errorf("write rig-state bead %s: %w", stateID, err)
	}
	return nil
}

// fileTransitionAttachment writes one kind:transition attachment bead
// linked to the parent state bead. Title format mirrors the design's
// `Issue.Title` example.
func (h *CycleCloseHandler) fileTransitionAttachment(stateID, rig string, tr Transition) error {
	raw, err := json.Marshal(tr)
	if err != nil {
		return fmt.Errorf("marshal transition: %w", err)
	}
	title := fmt.Sprintf("auto-test-pr transition %s: %s→%s @ %s", rig, tr.From, tr.To, tr.At)
	labels := []string{labelAttachmentUmbrella, labelKindTransition, labelRigPrefix + rig, labelAutoTestPRUmbrella}
	if _, err := h.beads.CreateAttachment(title, labels, stateID, raw); err != nil {
		return fmt.Errorf("create transition attachment: %w", err)
	}
	return nil
}

// fileRejectionAttachment writes one kind:rejection attachment bead.
func (h *CycleCloseHandler) fileRejectionAttachment(stateID, rig string, rj Rejection) error {
	raw, err := json.Marshal(rj)
	if err != nil {
		return fmt.Errorf("marshal rejection: %w", err)
	}
	title := fmt.Sprintf("auto-test-pr rejection %s: %s @ %s", rig, rj.File, rj.RejectedAt)
	labels := []string{labelAttachmentUmbrella, labelKindRejection, labelRigPrefix + rig, labelAutoTestPRUmbrella}
	if _, err := h.beads.CreateAttachment(title, labels, stateID, raw); err != nil {
		return fmt.Errorf("create rejection attachment: %w", err)
	}
	return nil
}

// incrementTownCircuitBreakerCounter bumps Count on the
// town-auto-test-pr-state bead. Single-writer field per OQ4 spike — the
// cycle-close handler is the only writer.
func (h *CycleCloseHandler) incrementTownCircuitBreakerCounter() error {
	raw, err := h.beads.ShowMetadata(TownStateBeadID)
	if err != nil {
		// If the town bead isn't provisioned yet, we cannot increment.
		// The bead is owned by Phase 0 task 8 (gu-kn0j8) and provisioned
		// at install time; a missing bead is a setup error, not a cycle
		// failure. Surface but don't block the close path.
		return fmt.Errorf("town-state read: %w", err)
	}
	town, err := UnmarshalTownState(raw)
	if err != nil {
		return fmt.Errorf("town-state decode: %w", err)
	}
	town.CircuitBreaker.Count++
	updated, err := town.MarshalMetadata()
	if err != nil {
		return fmt.Errorf("town-state marshal: %w", err)
	}
	if err := h.beads.UpdateMetadata(TownStateBeadID, updated); err != nil {
		return fmt.Errorf("town-state write: %w", err)
	}
	return nil
}

// recentRejectionCount lists the rig's transition attachments and returns
// the count of rejection-outcome transitions whose `at` timestamp falls
// within (now − closeWindowSize, now]. The just-written transition is
// included (it was created in the same Handle call before this function
// runs), so the count is inclusive of the current event.
//
// We fold by `outcome=rejected` in the transition Context rather than
// listing rejection attachments because the design table at
// synthesis.md:476 is keyed on "3 closes" and a "close" is a transition
// to cooled-down, not a separate rejection record. Counting transitions
// keeps the breaker semantics aligned with the state machine.
func (h *CycleCloseHandler) recentRejectionCount(rig string, now time.Time) (int, error) {
	transitions, err := h.beads.ListTransitionsForRig(rig)
	if err != nil {
		return 0, err
	}
	cutoff := now.Add(-closeWindowSize)
	count := 0
	for _, tr := range transitions {
		if tr.To != rigStateCooledDown {
			continue
		}
		if outcome, _ := tr.Context["outcome"].(string); outcome != "rejected" {
			continue
		}
		at, err := time.Parse(time.RFC3339, tr.At)
		if err != nil {
			continue
		}
		if at.Before(cutoff) {
			continue
		}
		count++
	}
	return count, nil
}

// rigStatePayload is the (subset of the) per-rig state bead's
// Issue.Metadata payload that this handler reads/writes. We unmarshal
// into a json.RawMessage map so writer-side fields the handler does not
// own are preserved on round-trip — Phase 0 task 15 owns the full
// schema and we MUST NOT clobber those fields.
type rigStatePayload struct {
	State string `json:"state"`
	Rig   string `json:"rig,omitempty"`

	// Other is the round-trip bag for fields we don't own (cadence_days,
	// last_cycle_at, current_cycle, etc.). When we marshal back, these
	// fields are emitted alongside our owned ones.
	Other map[string]json.RawMessage `json:"-"`
}

// MarshalJSON serializes the payload, merging Other on top of the typed
// fields. Order: typed fields first, then Other fields (sorted).
func (p rigStatePayload) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(p.Other)+2)
	for k, v := range p.Other {
		out[k] = v
	}
	stateRaw, err := json.Marshal(p.State)
	if err != nil {
		return nil, err
	}
	out["state"] = stateRaw
	if p.Rig != "" {
		rigRaw, err := json.Marshal(p.Rig)
		if err != nil {
			return nil, err
		}
		out["rig"] = rigRaw
	}
	return json.Marshal(out)
}

// decodeRigState parses the per-rig state bead Issue.Metadata into a
// rigStatePayload. Empty / null metadata returns a zero payload with
// state="" — callers that expect a particular from-state will fail the
// CAS naturally.
func decodeRigState(raw json.RawMessage) (rigStatePayload, error) {
	p := rigStatePayload{Other: map[string]json.RawMessage{}}
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return p, nil
	}

	var bag map[string]json.RawMessage
	if err := json.Unmarshal(raw, &bag); err != nil {
		return p, err
	}
	if v, ok := bag["state"]; ok {
		if err := json.Unmarshal(v, &p.State); err != nil {
			return p, fmt.Errorf("state field: %w", err)
		}
		delete(bag, "state")
	}
	if v, ok := bag["rig"]; ok {
		if err := json.Unmarshal(v, &p.Rig); err != nil {
			return p, fmt.Errorf("rig field: %w", err)
		}
		delete(bag, "rig")
	}
	p.Other = bag
	return p, nil
}

// BugDiscovered captures one BUG-DISCOVERED: block from an MR body.
// Format (per synthesis §"Bug-discovery", line 217-227):
//
//	BUG-DISCOVERED: <file>:<line>
//	expected: <value>
//	actual:   <value>
//	test:
//	  <multiline test source>
//
// Free-form lines after the test: header are captured as the test source
// up to the next BUG-DISCOVERED: header or end-of-body.
type BugDiscovered struct {
	// File is the source file the failing assertion points at.
	File string

	// Line is the source line (0 if unparseable; we keep the raw string
	// in Location for the bug bead body).
	Line int

	// Location is the verbatim "<file>:<line>" string from the header.
	Location string

	// Expected is the value the assertion expected.
	Expected string

	// Actual is the value the assertion observed.
	Actual string

	// TestSource is the candidate test source the polecat wrote.
	TestSource string
}

// Summary returns a one-line description suitable for the bug bead title.
func (b BugDiscovered) Summary() string {
	loc := b.Location
	if loc == "" {
		loc = "<unknown location>"
	}
	if b.Expected != "" || b.Actual != "" {
		return fmt.Sprintf("%s: expected %q, got %q", loc, b.Expected, b.Actual)
	}
	return loc
}

// Body renders the bug bead description body, including the MR backlink
// and a copy of the candidate test source the polecat captured.
func (b BugDiscovered) Body(mrID, rig string) string {
	var sb strings.Builder
	sb.WriteString("Auto-test-pr discovered a likely service bug while iterating on test coverage.\n")
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "Rig: %s\n", rig)
	fmt.Fprintf(&sb, "Source MR: %s\n", mrID)
	if b.Location != "" {
		fmt.Fprintf(&sb, "Location: %s\n", b.Location)
	}
	if b.Expected != "" {
		fmt.Fprintf(&sb, "Expected: %s\n", b.Expected)
	}
	if b.Actual != "" {
		fmt.Fprintf(&sb, "Actual: %s\n", b.Actual)
	}
	if b.TestSource != "" {
		sb.WriteString("\nCandidate test source (test was NOT pushed — encoding the buggy behavior\n")
		sb.WriteString("as 'correct' would paper over the real defect):\n\n")
		sb.WriteString(b.TestSource)
	}
	return sb.String()
}

// bugHeaderRE matches the BUG-DISCOVERED: header line. Captures the
// "<file>:<line>" location.
var bugHeaderRE = regexp.MustCompile(`(?m)^BUG-DISCOVERED:\s*(\S+)\s*$`)

// kvRE matches one of the structured key:value lines under a BUG-DISCOVERED
// header (expected, actual, test).
var kvRE = regexp.MustCompile(`(?m)^(expected|actual|test):\s*(.*)$`)

// ParseBugDiscovered scans the MR body for BUG-DISCOVERED: blocks and
// returns one BugDiscovered per occurrence. Blocks are bounded between
// consecutive BUG-DISCOVERED: headers (or EOF). Malformed blocks
// (header with no key:value lines) still produce an entry — the body
// will surface "<unknown location>" / empty fields, which is what the
// human reviewer needs to see to investigate.
func ParseBugDiscovered(body string) []BugDiscovered {
	if !strings.Contains(body, "BUG-DISCOVERED:") {
		return nil
	}
	var bugs []BugDiscovered
	headerMatches := bugHeaderRE.FindAllStringSubmatchIndex(body, -1)
	for i, m := range headerMatches {
		// Block extends from end of this header to start of next header.
		blockEnd := len(body)
		if i+1 < len(headerMatches) {
			blockEnd = headerMatches[i+1][0]
		}
		header := body[m[0]:m[1]]
		block := body[m[1]:blockEnd]

		bug := BugDiscovered{}
		if hm := bugHeaderRE.FindStringSubmatch(header); len(hm) >= 2 {
			bug.Location = hm[1]
			if idx := strings.LastIndex(bug.Location, ":"); idx >= 0 {
				bug.File = bug.Location[:idx]
				if n, err := strconv.Atoi(bug.Location[idx+1:]); err == nil {
					bug.Line = n
				}
			} else {
				bug.File = bug.Location
			}
		}

		// Walk the block line by line. Track whether we're in the test:
		// section (which slurps until the next BUG-DISCOVERED header).
		var testLines []string
		inTest := false
		for _, line := range strings.Split(block, "\n") {
			if matches := kvRE.FindStringSubmatch(line); matches != nil {
				key := matches[1]
				val := strings.TrimSpace(matches[2])
				switch key {
				case "expected":
					bug.Expected = val
					inTest = false
				case "actual":
					bug.Actual = val
					inTest = false
				case "test":
					inTest = true
					if val != "" {
						testLines = append(testLines, val)
					}
				}
				continue
			}
			if inTest {
				testLines = append(testLines, line)
			}
		}
		if len(testLines) > 0 {
			// Trim leading/trailing empty lines so the body renders
			// cleanly without large vertical gaps.
			bug.TestSource = strings.TrimSpace(strings.Join(testLines, "\n"))
		}
		bugs = append(bugs, bug)
	}
	return bugs
}

// extractTargetPath parses the polecat-emitted `target_path:` line out of
// the MR body. The auto-test-pr MR template carries this field per
// synthesis.md §"Per-rig state bead" / `rejections[].file`. Falls back
// to "" if not present — the rejection attachment then records an
// empty `file`, which the reader treats as "no per-file cooldown".
func extractTargetPath(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		const key = "target_path:"
		if strings.HasPrefix(strings.ToLower(line), key) {
			return strings.TrimSpace(line[len(key):])
		}
	}
	return ""
}

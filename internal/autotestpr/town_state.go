// Auto-Test-PR town-state pinned bead model and provisioning helpers.
//
// Phase 0 task 8 (gu-kn0j8) — provision the single town-wide
// `town-auto-test-pr-state` pinned bead with `enabled_rigs: []`. This
// bead is the Mayor-owned, denormalized read-cache for the
// town-wide row of `gt auto-test-pr status` (global pause flag,
// circuit-breaker counter, and the list of opted-in rigs).
//
// Phase 0 task 2b (gu-uez5w) extended the schema with the operator-
// pause surface (RigPauses, GlobalPauseReason, GlobalPausedBy) and
// the bounded audit log (Incidents []Incident, ≤MaxIncidents). These
// are the targets of the `pause`, `resume` (incl.
// `--override-circuit-breaker`), `status`, `show`, and `history` CLI
// verbs. Per the synthesis (line 1175) "no patrol consumes them yet"
// in Phase 0 — entries are written for audit and read back, but do
// not yet drive Mayor behavior.
//
// Schema and design context:
//   - .designs/auto-test-pr/synthesis.md §"Phase 0 task 8"
//   - .designs/auto-test-pr/synthesis.md §"Phase 0 task 2b" (line 1173)
//   - .designs/auto-test-pr/data.md §"Town-wide bead (one, shared)"
//
// Single-writer fields only (per the OQ4 fallback re-shape — the
// concurrency-prone `transition_log` and `rejection_log` fields move
// to attachment beads, not into Issue.Metadata of this pinned bead).
package autotestpr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// TownStateBeadID is the well-known ID of the town-wide auto-test-pr
// pinned bead. Stored in town beads (~/gt/.beads/) under the "town-"
// prefix that the install path registers in routes.jsonl.
//
// The ID is intentionally not under "hq-" because hq- is reserved for
// town-level agent beads (mayor, deacon, dogs). The auto-test-pr state
// bead is data, not an agent identity.
const TownStateBeadID = "town-auto-test-pr-state"

// TownStateLabel is the umbrella label tagged on the town-state bead.
// Mayor reads it via List(Label=TownStateLabel) to find this bead
// without hard-coding the ID.
const TownStateLabel = "gt:auto-test-pr-state"

// TownStateSchemaVersion is the current on-disk schema version of the
// town-state bead's metadata blob. Bump on any backwards-incompatible
// change. Readers tolerate forward-version fields by round-tripping
// through json.RawMessage per the synthesis schema-versioning policy.
const TownStateSchemaVersion = 1

// TownStateTitle is the bead title. Stable so operators can identify
// the bead without dereferencing the ID.
const TownStateTitle = "Auto-Test-PR town state (Mayor-owned)"

// TownStateDescription is the bead description shown in `bd show`.
// Static — it points operators at the design doc rather than carrying
// any state of its own (state lives in Metadata).
const TownStateDescription = `Auto-Test-PR town-wide state bead.

Mayor-owned. Single-writer metadata fields only (schema_version,
global_pause_until, circuit_breaker, enabled_rigs, rig_summary).
High-cardinality logs (transition_log, rejection_log) live on
per-attachment beads, NOT on this bead — see the OQ4 fallback in
.designs/auto-test-pr/synthesis.md §"OQ4 fallback".

Read-only via: gt auto-test-pr status [--format=json|table]
Written by:    Mayor cycle handlers + the gt auto-test-pr enable/
               disable/pause/resume CLI verbs.`

// CircuitBreakerState is the town-wide consecutive-close counter that
// auto-pauses every rig when the rate of unmerged auto-test PRs across
// the town crosses a threshold. Per the design's R26 / D16 trip
// criteria. The Count field is the consecutive-close count that
// readers like `gt auto-test-pr status` surface; WindowStartedAt and
// TrippedUntil are populated by the Mayor cycle-close handler when
// the breaker fires.
type CircuitBreakerState struct {
	// Count is the count of consecutive closed-unmerged MRs town-wide.
	// Resets to zero on the next merged auto-test-pr MR. The status
	// table prints this verbatim.
	Count int `json:"count"`

	// WindowStartedAt is when the current consecutive-close window
	// began (RFC3339 string; empty before the first close). Consumed by
	// the Mayor's window-rolling logic.
	WindowStartedAt string `json:"window_started_at,omitempty"`

	// TrippedUntil is the RFC3339 timestamp at which the town-wide
	// circuit breaker stops auto-pausing rigs. Empty when the breaker
	// has not fired.
	TrippedUntil string `json:"tripped_until,omitempty"`
}

// IsTripped reports whether the town-wide circuit breaker is currently
// in the tripped state. Phase 0 readers (status, show) compare against
// the *presence* of TrippedUntil; we deliberately don't compare against
// time.Now here — the elapsed-time path is owned by Mayor's tick (which
// clears TrippedUntil on operator override or — in Phase 1 — natural
// cooldown). Treating "TrippedUntil non-empty" as "tripped" matches the
// `paused-by-circuit-breaker` state-machine entry described in the
// synthesis state diagram.
func (c CircuitBreakerState) IsTripped() bool {
	return c.TrippedUntil != ""
}

// RigPauseEntry is the per-rig pause record stored on the town-state
// bead in Phase 0 (before per-rig state beads are provisioned in
// Phase 1 task 15). Each entry is the most-recent pause command for
// the rig — pause and resume are last-write-wins on this row, not an
// append-only log. `pause-by-circuit-breaker` (D16) is recorded
// separately on the town-bead's CircuitBreaker — this struct is for
// the operator-driven `gt auto-test-pr pause --rig=<rig>` verb.
type RigPauseEntry struct {
	// PausedUntil is the RFC3339 timestamp at which the operator-set
	// pause expires. Empty means "not paused" (the rig key is absent
	// from RigPauses entirely in that case; we keep the field tagged
	// omitempty for forward-version readers).
	PausedUntil string `json:"paused_until,omitempty"`

	// Reason is the operator-provided free-form string from
	// `--reason=...`. Optional; empty when not given.
	Reason string `json:"reason,omitempty"`

	// PausedBy is the operator address (e.g., "overseer",
	// "mayor/", "gastown_upstream/polecats/radrat") that issued the
	// pause. Resolved from BD_ACTOR / GT_ROLE at command time.
	PausedBy string `json:"paused_by,omitempty"`

	// PausedAt is the RFC3339 timestamp at which the pause was set.
	// Provides the audit trail's "when" alongside PausedBy's "who".
	PausedAt string `json:"paused_at,omitempty"`
}

// IncidentKind enumerates the distinct kinds of audit-log entries we
// record on the town-state bead. New kinds MUST be appended (not
// renumbered) and readers MUST tolerate unknown values by surfacing
// them verbatim — the audit log is operator-readable text.
type IncidentKind string

const (
	// IncidentCircuitBreakerOverride is emitted when an operator runs
	// `gt auto-test-pr resume --override-circuit-breaker` (D16). Per
	// task 2b acceptance: the override produces an audit-log entry
	// naming the operator and timestamp.
	IncidentCircuitBreakerOverride IncidentKind = "circuit-breaker-override"

	// IncidentGlobalPause is emitted when an operator runs
	// `gt auto-test-pr pause --all`.
	IncidentGlobalPause IncidentKind = "global-pause"

	// IncidentGlobalResume is emitted when an operator runs
	// `gt auto-test-pr resume --all`.
	IncidentGlobalResume IncidentKind = "global-resume"

	// IncidentRigPause is emitted when an operator runs
	// `gt auto-test-pr pause --rig=<rig>`.
	IncidentRigPause IncidentKind = "rig-pause"

	// IncidentRigResume is emitted when an operator runs
	// `gt auto-test-pr resume --rig=<rig>`.
	IncidentRigResume IncidentKind = "rig-resume"
)

// Incident is a single audit-log entry on the town-state bead. The
// full log is bounded to ≤MaxIncidents entries — older entries are
// dropped on append to keep the bead's metadata blob small and the
// pinned-bead Show() round-trip fast.
//
// Phase 0 only records operator-driven events here; Mayor-driven
// state-machine transitions live on attachment beads (OQ4 fallback)
// rather than this single-row log.
type Incident struct {
	// At is the RFC3339 timestamp at which the event occurred.
	At string `json:"at"`

	// Actor is the operator address (e.g., "overseer", "mayor/").
	// Resolved from BD_ACTOR / GT_ROLE at command time.
	Actor string `json:"actor"`

	// Kind is one of the IncidentKind constants. Stored as a string
	// so unknown future kinds round-trip safely through readers.
	Kind IncidentKind `json:"kind"`

	// Rig is the rig the event applies to, or empty for town-wide
	// events. For per-rig events this matches the rig's directory
	// name (e.g., "gastown_upstream").
	Rig string `json:"rig,omitempty"`

	// Details is a free-form human-readable line, e.g., the
	// --reason= flag value or "duration=24h" for pauses. Optional.
	Details string `json:"details,omitempty"`
}

// MaxIncidents is the hard cap on the audit-log slice length per
// round 3 fix #3. AppendIncident drops the oldest entries past this
// cap. Twenty is enough to cover an operator-busy day (a SEV-1
// fire-drill might emit ~5-8 entries) without growing Issue.Metadata
// past the OQ4 spike's safe-blob threshold.
const MaxIncidents = 20

// TownState is the JSON-serialized payload of the
// `town-auto-test-pr-state` pinned bead's Issue.Metadata field.
// Single-writer fields only — see file-level docstring.
type TownState struct {
	// SchemaVersion follows TownStateSchemaVersion. Readers tolerate
	// future versions by round-tripping unknown fields, but a writer
	// emitting a stale schema_version is a programming error.
	SchemaVersion int `json:"schema_version"`

	// GlobalPauseUntil is non-empty when an operator has paused all
	// rigs town-wide via `gt auto-test-pr pause --all`. Empty (or
	// absent) means no town-wide pause is in effect.
	GlobalPauseUntil string `json:"global_pause_until,omitempty"`

	// GlobalPauseReason is the operator-provided free-form string from
	// `pause --all --reason=...`. Optional; empty when not given.
	GlobalPauseReason string `json:"global_pause_reason,omitempty"`

	// GlobalPausedBy is the operator address that issued the most-
	// recent town-wide pause. Audit-log mirror; the canonical record
	// is the matching Incident in Incidents[]. Stored here too so
	// `gt auto-test-pr status` can render "paused-by" without walking
	// the audit log.
	GlobalPausedBy string `json:"global_paused_by,omitempty"`

	// CircuitBreaker holds the town-wide consecutive-close counter
	// state. Serialized even when empty so readers always have a
	// well-formed object to dereference.
	CircuitBreaker CircuitBreakerState `json:"circuit_breaker"`

	// EnabledRigs is the denormalized list of rigs that have
	// auto_test_pr.enabled=true in their settings JSON. Maintained by
	// the Phase 0 task 2a `enable`/`disable` CLI verbs (CAS-append /
	// CAS-remove); reconciled against settings-JSON ground truth at
	// the top of every Mayor cycle (Phase 0 task 4).
	//
	// Always non-nil after a successful Provision — empty slice
	// rather than null, so JSON output reads `[]` and downstream
	// consumers don't have to special-case the missing-key path.
	EnabledRigs []string `json:"enabled_rigs"`

	// RigPauses is the per-rig operator-pause table keyed by rig name.
	// Phase 0 home for `gt auto-test-pr pause --rig=<rig>` data —
	// Phase 1 task 15 will migrate this to the per-rig state bead's
	// `paused_until` field, but Phase 0 task 2b's CLI surface needs
	// somewhere to write to and the per-rig state beads do not yet
	// exist. The synthesis (line 1175) explicitly accepts that "no
	// patrol consumes them yet" in Phase 0 — these entries are read
	// back by `status` / `show` but produce no Mayor behavior.
	//
	// Keys absent from the map mean "not paused"; we do NOT keep
	// stale entries with `paused_until` in the past — `resume` deletes
	// the key entirely.
	RigPauses map[string]RigPauseEntry `json:"rig_pauses,omitempty"`

	// Incidents is the bounded audit log (≤MaxIncidents entries).
	// Append-only with FIFO trim on overflow per round 3 fix #3.
	// Operator-driven events only; Mayor-driven state transitions
	// live on attachment beads per the OQ4 fallback.
	Incidents []Incident `json:"incidents,omitempty"`

	// RigSummary is a denormalized read-cache for the per-rig rows of
	// `gt auto-test-pr status`. Keys are rig names; values are
	// JSON objects whose shape evolves with the per-rig state bead
	// (state, last_cycle_at, last_outcome). Stored as RawMessage so
	// the town bead doesn't have to know the per-rig schema yet —
	// Phase 1 task 15 owns the rig-summary writer.
	RigSummary map[string]json.RawMessage `json:"rig_summary,omitempty"`
}

// DefaultTownState returns the zero-valued state used at provisioning
// time: schema=1, no global pause, breaker count zero, empty
// enabled_rigs (initialized as a non-nil empty slice so downstream
// JSON serializes as `[]` not `null`), nil rig_summary.
//
// This is the bead's exact post-task acceptance state per gu-kn0j8:
//
//	gt auto-test-pr status --format=json
//	  → {enabled_rigs:[], paused:false, circuit_breaker:{count:0}}
func DefaultTownState() TownState {
	return TownState{
		SchemaVersion:  TownStateSchemaVersion,
		EnabledRigs:    []string{},
		CircuitBreaker: CircuitBreakerState{Count: 0},
	}
}

// MarshalMetadata serializes a TownState to the canonical JSON byte
// shape expected in Issue.Metadata. Stable field order is guaranteed
// by encoding/json (struct tag order). Returns an error only if a
// programming bug introduces an unencodable field — at that point the
// caller MUST surface the failure rather than silently writing a
// partial payload.
func (s TownState) MarshalMetadata() (json.RawMessage, error) {
	// EnabledRigs is the most-watched field (round 3 fix #4 sync); a
	// nil slice would JSON-encode as `null`, which is a footgun for
	// downstream consumers expecting an iterable. Defensively
	// re-initialize to empty slice on the marshal path.
	if s.EnabledRigs == nil {
		s.EnabledRigs = []string{}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshaling town state: %w", err)
	}
	return raw, nil
}

// UnmarshalTownState parses Issue.Metadata bytes into a TownState.
// Empty/null metadata yields a zero TownState (NOT DefaultTownState
// — callers that want defaults should use DefaultTownState
// explicitly; this function reports what is actually on the bead).
//
// Forward-compatibility: unknown JSON fields are silently dropped per
// encoding/json's default. The schema-versioning policy
// (.designs/auto-test-pr/synthesis.md §"Schema versioning") allows
// readers to tolerate forward-version fields. If callers need to
// preserve unknown fields on round-trip, they should hold the
// json.RawMessage directly and not unmarshal-then-marshal.
func UnmarshalTownState(raw json.RawMessage) (TownState, error) {
	if len(raw) == 0 {
		return TownState{}, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return TownState{}, nil
	}
	var s TownState
	if err := json.Unmarshal(raw, &s); err != nil {
		return TownState{}, fmt.Errorf("unmarshaling town state: %w", err)
	}
	if s.EnabledRigs == nil {
		// Match MarshalMetadata's invariant — readers should always see
		// an iterable enabled_rigs slice.
		s.EnabledRigs = []string{}
	}
	return s, nil
}

// StatusJSON is the shape of `gt auto-test-pr status --format=json`
// for the town-wide row. The top-level is intentionally flat to match
// the synthesis-doc acceptance literal:
//
//	{enabled_rigs:[], paused:false, circuit_breaker:{count:0}}
//
// Per-rig rows arrive in Phase 1 (task 15 + task 2b status verb v2).
type StatusJSON struct {
	EnabledRigs    []string            `json:"enabled_rigs"`
	Paused         bool                `json:"paused"`
	CircuitBreaker CircuitBreakerState `json:"circuit_breaker"`
}

// ToStatusJSON projects a TownState into the externally-visible
// status JSON shape. Paused is derived from GlobalPauseUntil being
// non-empty (the absence of a value means "not paused"; the precise
// semantics of "paused until <past timestamp>" — i.e. a pause whose
// deadline has elapsed — are the responsibility of the writer
// (Phase 0 task 2b's `resume` verb clears the field). Readers MUST
// NOT compare against time.Now here; that would be a clock dependency
// in a tight read path used during incidents.
func (s TownState) ToStatusJSON() StatusJSON {
	rigs := s.EnabledRigs
	if rigs == nil {
		rigs = []string{}
	}
	return StatusJSON{
		EnabledRigs:    rigs,
		Paused:         s.GlobalPauseUntil != "",
		CircuitBreaker: s.CircuitBreaker,
	}
}

// LoadTownState reads the town-state pinned bead and parses its
// Issue.Metadata. Returns ErrTownStateNotProvisioned if the bead does
// not exist — callers (e.g., `gt auto-test-pr status`) should treat
// that as "fall through to provision then re-load" rather than as a
// hard failure.
//
// Errors from the bd subprocess (Dolt down, network, etc.) are
// returned verbatim so the caller can decide whether to escalate.
func LoadTownState(b *beads.Beads) (TownState, error) {
	issue, err := b.Show(TownStateBeadID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return TownState{}, ErrTownStateNotProvisioned
		}
		// bd's CLI surface returns "no issue found" on a 404 without
		// wrapping ErrNotFound when going through the subprocess path
		// (see Show -> showLocal). Match that string defensively.
		if isBeadNotFoundError(err) {
			return TownState{}, ErrTownStateNotProvisioned
		}
		return TownState{}, fmt.Errorf("loading town-state bead: %w", err)
	}
	return UnmarshalTownState(issue.Metadata)
}

// ErrTownStateNotProvisioned is returned by LoadTownState when the
// pinned bead does not exist. Callers should generally treat this as
// recoverable — call EnsureTownStateBead to create the default-shape
// bead idempotently and re-try.
var ErrTownStateNotProvisioned = errors.New("town-auto-test-pr-state pinned bead not provisioned")

// isBeadNotFoundError handles bd CLI subprocess errors that don't
// come back wrapped in beads.ErrNotFound. The CLI emits stderr text
// like `Error fetching <id>: no issue found matching "<id>"` — we
// match the substring rather than parsing exit codes, which the
// subprocess wrapper already discards.
func isBeadNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no issue found")
}

// EnsureTownStateBead provisions the town-state pinned bead if it does
// not already exist. Idempotent: subsequent calls are no-ops once the
// bead is present. Safe to call from `gt install` (initial setup) and
// lazily from CLI verbs that read the bead (recovery from a partial
// install).
//
// Behavior:
//
//  1. Check for an existing bead at TownStateBeadID. If present, the
//     function returns it without modification — callers MUST NOT
//     assume a freshly-defaulted state on second call (existing state
//     is preserved across calls).
//  2. Otherwise, create the bead with TownStateTitle, TownStateLabel,
//     TownStateDescription, and Metadata=DefaultTownState(). Then mark
//     it pinned via Update(Status=StatusPinned) — bd does not allow
//     pinned status at create time.
//
// The two-step create+pin pattern mirrors GetOrCreateHandoffBead in
// internal/beads/handoff.go. If the second step fails, the bead is
// left in `open` status; the next call retries the pin step. This is
// intentionally NOT cleaned up like handoff beads: the bead's content
// is correct the moment Create returns, and an unpinned-but-correct
// bead is far less harmful than a deleted bead under a transient
// failure.
func EnsureTownStateBead(b *beads.Beads) (*beads.Issue, error) {
	if b == nil {
		return nil, fmt.Errorf("EnsureTownStateBead: nil beads wrapper")
	}

	// Check for existing bead first. Show returns ErrNotFound (or the
	// CLI string equivalent) if the bead does not exist yet.
	existing, err := b.Show(TownStateBeadID)
	if err == nil {
		// Already provisioned. Best-effort heal: if the bead exists but
		// is somehow not in pinned status (e.g., an aborted prior run
		// that reached Create but not Update), pin it now. Other state
		// is preserved.
		if existing.Status != beads.StatusPinned {
			pinned := beads.StatusPinned
			if updateErr := b.Update(TownStateBeadID, beads.UpdateOptions{
				Status: &pinned,
			}); updateErr != nil {
				return existing, fmt.Errorf("repinning existing town-state bead: %w", updateErr)
			}
			// Re-fetch so callers see the updated status.
			refetched, refetchErr := b.Show(TownStateBeadID)
			if refetchErr != nil {
				return existing, fmt.Errorf("refetching repinned town-state bead: %w", refetchErr)
			}
			return refetched, nil
		}
		return existing, nil
	}

	if !errors.Is(err, beads.ErrNotFound) && !isBeadNotFoundError(err) {
		// Something other than "not found" — propagate so the caller
		// can decide (Dolt down, etc.).
		return nil, fmt.Errorf("checking town-state bead existence: %w", err)
	}

	// Create the bead with default state.
	defaultState := DefaultTownState()
	rawMeta, err := defaultState.MarshalMetadata()
	if err != nil {
		return nil, fmt.Errorf("marshaling default town state: %w", err)
	}

	issue, err := b.CreateWithID(TownStateBeadID, beads.CreateOptions{
		Title:       TownStateTitle,
		Description: TownStateDescription,
		Labels:      []string{"gt:task", TownStateLabel},
		Priority:    2,
		Metadata:    rawMeta,
		Actor:       "mayor",
	})
	if err != nil {
		return nil, fmt.Errorf("creating town-state bead: %w", err)
	}

	// Pin it. If this fails the bead exists but is not pinned — the
	// next EnsureTownStateBead call will heal that via the
	// existing-but-unpinned branch above.
	pinned := beads.StatusPinned
	if err := b.Update(TownStateBeadID, beads.UpdateOptions{Status: &pinned}); err != nil {
		return issue, fmt.Errorf("pinning town-state bead: %w", err)
	}

	// Re-fetch to surface the post-pin status to callers.
	refetched, err := b.Show(TownStateBeadID)
	if err != nil {
		return issue, fmt.Errorf("refetching pinned town-state bead: %w", err)
	}
	return refetched, nil
}

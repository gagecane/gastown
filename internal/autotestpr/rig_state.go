// Per-rig auto-test-pr state bead model and provisioning.
//
// Phase 0 task 8 (gu-l6xu) — when a rig first opts in, the enable
// verb provisions a `<rig>-auto-test-state` pinned bead with the
// initial state-machine metadata. This is the per-rig complement to
// the town-wide `town-auto-test-pr-state` bead.
//
// Schema and design context:
//   - .designs/auto-test-pr/data.md §"Pinned state bead (one per opted-in rig)"
//   - .designs/auto-test-pr/synthesis.md §"OQ4 fallback"
//
// Single-writer fields only on the state bead per the OQ4 fallback:
// transition_log[] and rejection_log[] live on attachment beads (see
// attachments.go), NOT in Issue.Metadata of the per-rig pinned bead.
package autotestpr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// RigStateBeadIDSuffix is appended to the rig name to form the
// well-known ID of the per-rig auto-test-pr state bead. For example,
// rig "gastown_upstream" → ID "gastown_upstream-auto-test-state".
const RigStateBeadIDSuffix = "-auto-test-state"

// RigStateLabel is the label tagged on per-rig state beads. Mayor
// finds them via List(Label=RigStateLabel).
const RigStateLabel = "gt:auto-test-pr-rig-state"

// RigStateSchemaVersion is the current schema version of the per-rig
// state bead's metadata blob.
const RigStateSchemaVersion = 1

// RigStateBeadID computes the well-known bead ID for a rig's
// auto-test-pr state bead.
func RigStateBeadID(rigName string) string {
	return rigName + RigStateBeadIDSuffix
}

// PerRigCycleState enumerates the valid state-machine values for a
// per-rig auto-test-pr pinned bead. These are the string constants
// the bead's "state" field holds. Per the synthesis (Q7):
// idle | picking | dispatched | mr-pending | mr-revising | cooled-down
//
// Note: the upstream RigCycleState struct (cycle_close_handler.go) is
// the Phase 0 representation stored in TownState.RigSummary. This
// type is for the per-rig *pinned bead* that Phase 1 task 15
// provisions. They converge in Phase 1.
type PerRigCycleState string

const (
	PerRigCycleStateIdle       PerRigCycleState = "idle"
	PerRigCycleStatePicking    PerRigCycleState = "picking"
	PerRigCycleStateDispatched PerRigCycleState = "dispatched"
	PerRigCycleStateMRPending  PerRigCycleState = "mr-pending"
	PerRigCycleStateMRRevising PerRigCycleState = "mr-revising"
	PerRigCycleStateCooledDown PerRigCycleState = "cooled-down"

	// PerRigCycleStatePausedByCircuitBreaker is the terminal pause state
	// entered when the circuit breaker trips (D16). Unlike cooled-down,
	// it does NOT auto-release on cadence — it requires an explicit
	// `gt auto-test-pr resume --override-circuit-breaker` (see D18).
	PerRigCycleStatePausedByCircuitBreaker PerRigCycleState = "paused-by-circuit-breaker"
)

// CurrentCycle holds the in-flight cycle data when the rig is not
// idle. nil/null means "no active cycle" (state == idle or cooled-down).
type CurrentCycle struct {
	// CycleID is the unique identifier for this cycle's tracking bead.
	CycleID string `json:"cycle_id"`

	// StartedAt is the RFC3339 timestamp when the cycle began.
	StartedAt string `json:"started_at"`

	// PolecatBead is the ID of the polecat work bead dispatched for
	// this cycle.
	PolecatBead string `json:"polecat_bead,omitempty"`

	// MRBead is the ID of the merge-request bead filed by the polecat.
	MRBead string `json:"mr_bead,omitempty"`

	// Branch is the git branch name for the auto-test PR.
	Branch string `json:"branch,omitempty"`
}

// RigState is the JSON-serialized payload of the per-rig
// `<rig>-auto-test-state` pinned bead's Issue.Metadata field.
//
// Single-writer fields only. transition_log and rejection_log are
// NOT present — they live on attachment beads per OQ4 fallback.
type RigState struct {
	// SchemaVersion follows RigStateSchemaVersion.
	SchemaVersion int `json:"schema_version"`

	// State is the current state-machine state for this rig.
	State PerRigCycleState `json:"state"`

	// CurrentCycle holds in-flight cycle data when state is not idle.
	// Serialized as null when no cycle is active.
	CurrentCycle *CurrentCycle `json:"current_cycle"`

	// LastCycleAt is the RFC3339 timestamp of the most-recent cycle
	// completion (any terminal state: merged, closed-unmerged, etc.).
	LastCycleAt string `json:"last_cycle_at,omitempty"`

	// LastCycleOutcome is the outcome of the most-recent cycle.
	// Values: "merged", "closed-unmerged", "expired", "canceled".
	LastCycleOutcome string `json:"last_cycle_outcome,omitempty"`

	// PausedUntil is the per-rig pause deadline (RFC3339). Empty means
	// "not paused". Phase 1 will write this directly; Phase 0 stores
	// pauses on the town-state bead's RigPauses map instead.
	PausedUntil string `json:"paused_until,omitempty"`

	// Incidents is the per-rig bounded audit log (≤MaxIncidents).
	// Same semantics as the town-state bead's Incidents field.
	Incidents []Incident `json:"incidents,omitempty"`
}

// DefaultRigState returns the zero-valued state used when provisioning
// a per-rig bead for the first time: idle, no active cycle, no
// history. This is the shape the per-rig bead starts with and what
// the materializer returns for a rig with zero cycles.
func DefaultRigState() RigState {
	return RigState{
		SchemaVersion: RigStateSchemaVersion,
		State:         PerRigCycleStateIdle,
		CurrentCycle:  nil,
	}
}

// MarshalMetadata serializes a RigState to the canonical JSON byte
// shape for Issue.Metadata.
func (s RigState) MarshalMetadata() (json.RawMessage, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshaling rig state: %w", err)
	}
	return raw, nil
}

// UnmarshalRigState parses Issue.Metadata bytes into a RigState.
// Empty/null metadata yields a zero RigState.
func UnmarshalRigState(raw json.RawMessage) (RigState, error) {
	if len(raw) == 0 {
		return RigState{}, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return RigState{}, nil
	}
	var s RigState
	if err := json.Unmarshal(raw, &s); err != nil {
		return RigState{}, fmt.Errorf("unmarshaling rig state: %w", err)
	}
	return s, nil
}

// ErrRigStateNotProvisioned is returned by LoadRigState when the
// per-rig pinned bead does not exist.
var ErrRigStateNotProvisioned = errors.New("per-rig auto-test-state pinned bead not provisioned")

// LoadRigState reads the per-rig state pinned bead and parses its
// metadata. Returns ErrRigStateNotProvisioned if the bead does not
// exist.
func LoadRigState(b *beads.Beads, rigName string) (RigState, error) {
	beadID := RigStateBeadID(rigName)
	issue, err := b.Show(beadID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return RigState{}, ErrRigStateNotProvisioned
		}
		if isBeadNotFoundError(err) {
			return RigState{}, ErrRigStateNotProvisioned
		}
		return RigState{}, fmt.Errorf("loading rig-state bead %s: %w", beadID, err)
	}
	return UnmarshalRigState(issue.Metadata)
}

// EnsureRigStateBead provisions the per-rig state pinned bead if it
// does not already exist. Idempotent. Called from `gt auto-test-pr
// enable` when the rig first opts in.
//
// The bead is created with:
//   - ID: <rigName>-auto-test-state
//   - Title: "Auto-Test-PR rig state (<rigName>)"
//   - Labels: gt:task, gt:auto-test-pr-rig-state, gt:auto-test-pr
//   - Metadata: DefaultRigState()
//   - Status: pinned (via create + pin, same as EnsureTownStateBead)
//   - Actor: mayor
func EnsureRigStateBead(b *beads.Beads, rigName string) (*beads.Issue, error) {
	if b == nil {
		return nil, fmt.Errorf("EnsureRigStateBead: nil beads wrapper")
	}
	if rigName == "" {
		return nil, fmt.Errorf("EnsureRigStateBead: empty rig name")
	}

	beadID := RigStateBeadID(rigName)

	// Check for existing bead.
	existing, err := b.Show(beadID)
	if err == nil {
		// Already provisioned. Heal pin status if needed.
		if existing.Status != beads.StatusPinned {
			pinned := beads.StatusPinned
			if updateErr := b.Update(beadID, beads.UpdateOptions{
				Status: &pinned,
			}); updateErr != nil {
				return existing, fmt.Errorf("repinning existing rig-state bead %s: %w", beadID, updateErr)
			}
			refetched, refetchErr := b.Show(beadID)
			if refetchErr != nil {
				return existing, fmt.Errorf("refetching repinned rig-state bead %s: %w", beadID, refetchErr)
			}
			return refetched, nil
		}
		return existing, nil
	}

	if !errors.Is(err, beads.ErrNotFound) && !isBeadNotFoundError(err) {
		return nil, fmt.Errorf("checking rig-state bead %s existence: %w", beadID, err)
	}

	// Create the bead with default state.
	defaultState := DefaultRigState()
	rawMeta, err := defaultState.MarshalMetadata()
	if err != nil {
		return nil, fmt.Errorf("marshaling default rig state for %s: %w", rigName, err)
	}

	title := fmt.Sprintf("Auto-Test-PR rig state (%s)", rigName)
	desc := fmt.Sprintf(`Auto-Test-PR per-rig state bead for %s.

Mayor-owned. Single-writer metadata fields only (schema_version,
state, current_cycle, last_cycle_at, last_cycle_outcome, paused_until,
incidents). High-cardinality logs (transition_log, rejection_log) live
on attachment beads — see OQ4 fallback in
.designs/auto-test-pr/synthesis.md.`, rigName)

	issue, err := b.CreateWithID(beadID, beads.CreateOptions{
		Title:       title,
		Description: desc,
		Labels:      []string{"gt:task", RigStateLabel, "gt:auto-test-pr"},
		Priority:    2,
		Metadata:    rawMeta,
		Actor:       "mayor",
	})
	if err != nil {
		return nil, fmt.Errorf("creating rig-state bead %s: %w", beadID, err)
	}

	// Pin it.
	pinned := beads.StatusPinned
	if err := b.Update(beadID, beads.UpdateOptions{Status: &pinned}); err != nil {
		return issue, fmt.Errorf("pinning rig-state bead %s: %w", beadID, err)
	}

	// Re-fetch to surface post-pin status.
	refetched, err := b.Show(beadID)
	if err != nil {
		return issue, fmt.Errorf("refetching pinned rig-state bead %s: %w", beadID, err)
	}
	return refetched, nil
}

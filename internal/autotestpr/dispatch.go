// Dispatch envelope construction and per-rig CAS state transitions for
// the auto-test-pr cycle (synthesis §Key Components §1 steps 3-6 and
// §Interface "Dispatch-bead JSON envelope").
//
// Phase 0 task 4 (gu-2n7xi). Two concerns live here:
//
//  1. The dispatch envelope: the JSON contract the Mayor cycle files
//     onto the work bead and the polecat reads at dispatch time. The
//     shape mirrors the existing gu-wisp-* sling-context envelope so
//     the scheduler can carry it without a bespoke path.
//
//  2. CAS state transitions on the per-rig `<rig>-auto-test-state`
//     pinned bead: idle → picking → dispatched. Each transition
//     re-reads the bead, guards on the expected `from` state (so a
//     concurrent tick that already advanced the rig loses the race and
//     skips), writes the new state, and appends a transition
//     attachment bead (OQ4 fallback) for the audit log.
//
// The CAS layer operates against a small RigStateStore interface so the
// transition logic is unit-testable without a live Dolt server.
//
// Design context:
//   - .designs/auto-test-pr/synthesis.md §Interface, §"State machine"
//   - .designs/auto-test-pr/synthesis.md §"OQ4 fallback"
package autotestpr

import (
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// DispatchFormula is the polecat-work variant the cycle dispatches.
const DispatchFormula = "mol-polecat-work-test-improver"

// DispatchEnvelopeVersion is the current envelope schema version.
const DispatchEnvelopeVersion = 1

// DispatchPriorityFloor is the sling priority floor the cycle uses when
// enqueuing auto-test work: the lowest bucket, so auto-test PRs never
// starve human or higher-priority agent work (synthesis step 5, "strict
// priority floor (lowest bucket)"; depends on task 7's mechanism).
const DispatchPriorityFloor = capacity.PriorityFloorLowest

// SizeBudget bounds the diff the polecat may produce (gate 4g). Defaults
// per synthesis: 3 files, 200 added test LOC.
type SizeBudget struct {
	MaxFiles int `json:"max_files"`
	MaxLOC   int `json:"max_loc"`
}

// DefaultSizeBudget returns the synthesis default budget.
func DefaultSizeBudget() SizeBudget {
	return SizeBudget{MaxFiles: 3, MaxLOC: 200}
}

// DispatchTarget is a single file the polecat should add tests for,
// carried in the envelope's args.targets[].
type DispatchTarget struct {
	Path              string            `json:"path"`
	UncoveredBranches []UncoveredBranch `json:"uncovered_branches"`
	CoveragePctBefore float64           `json:"coverage_pct_before"`
}

// DispatchArgs is the args block of the dispatch envelope.
type DispatchArgs struct {
	Mode                 string           `json:"mode"`
	Targets              []DispatchTarget `json:"targets"`
	ConventionsSheetPath string           `json:"conventions_sheet_path"`
	Language             string           `json:"language"`
	SizeBudget           SizeBudget       `json:"size_budget"`
	PRTemplatePath       string           `json:"pr_template_path"`
	Revision             *RevisionArgs    `json:"revision"`
}

// RevisionArgs carries prior-cycle revision context (mode == "revise").
// Nil for fresh "create" dispatches.
type RevisionArgs struct {
	CommentThread string `json:"comment_thread,omitempty"`
	LastCommitSHA string `json:"last_commit_sha,omitempty"`
	Branch        string `json:"branch,omitempty"`
}

// DispatchEnvelope is the full Mayor → polecat dispatch contract
// (synthesis §Interface). Serialized to JSON and carried as the sling
// context's Args.
type DispatchEnvelope struct {
	Version    int          `json:"version"`
	WorkBeadID string       `json:"work_bead_id"`
	TargetRig  string       `json:"target_rig"`
	Formula    string       `json:"formula"`
	Args       DispatchArgs `json:"args"`
	EnqueuedAt string       `json:"enqueued_at"`
}

// BuildDispatchEnvelope assembles a create-mode dispatch envelope for a
// single chosen target. The chosen candidate's uncovered branches are
// ordered by churn-proximity (NG5) before being placed in the envelope.
//
// workBeadID is the cycle work bead the polecat will hook; rig is the
// target rig; lang is the rig's configured language; conventionsPath and
// templatePath come from the rig's auto_test_pr config; now stamps
// EnqueuedAt.
func BuildDispatchEnvelope(
	workBeadID, rig, lang, conventionsPath, templatePath string,
	chosen TargetCandidate,
	budget SizeBudget,
	now time.Time,
) DispatchEnvelope {
	ordered := OrderUncoveredByChurnProximity(chosen.UncoveredBranches, chosen.ChurnRanges)

	return DispatchEnvelope{
		Version:    DispatchEnvelopeVersion,
		WorkBeadID: workBeadID,
		TargetRig:  rig,
		Formula:    DispatchFormula,
		Args: DispatchArgs{
			Mode: "create",
			Targets: []DispatchTarget{{
				Path:              chosen.Path,
				UncoveredBranches: ordered,
				CoveragePctBefore: chosen.CoveragePctBefore,
			}},
			ConventionsSheetPath: conventionsPath,
			Language:             lang,
			SizeBudget:           budget,
			PRTemplatePath:       templatePath,
			Revision:             nil,
		},
		EnqueuedAt: now.UTC().Format(time.RFC3339),
	}
}

// ─── CAS state transitions ───────────────────────────────────────────

// RigStateStore is the minimal surface the CAS transition layer needs.
// The production implementation wraps *beads.Beads (LoadRigState /
// b.Update); tests supply an in-memory fake. Keeping it an interface
// makes the idle→picking→dispatched transition logic unit-testable
// without a live Dolt server.
type RigStateStore interface {
	// LoadRigState reads the current per-rig state.
	LoadRigState(rig string) (RigState, error)

	// SaveRigState writes the per-rig state back. Implementations must
	// return a transient-classified error (isTransientDoltWriteError)
	// when an optimistic-lock conflict occurs so the CAS loop retries.
	SaveRigState(rig string, s RigState) error

	// AppendTransition records a state transition in the audit log
	// (OQ4-fallback attachment bead). Failures here are non-fatal to
	// the transition itself — the caller logs and proceeds.
	AppendTransition(rec TransitionRecord) error
}

// ErrTransitionConflict is returned by CASTransition when the bead is
// not in the expected `from` state — another tick advanced it first.
// Callers treat this as "skip this rig; the other tick owns it".
var ErrTransitionConflict = errors.New("auto-test-pr: rig not in expected state for transition")

// transitionCASMaxAttempts bounds the CAS retry loop. Mirrors the
// town-state mutator budget (enabledRigsCASMaxAttempts).
const transitionCASMaxAttempts = 5

// CASTransition advances a rig from `from` to `to`, retrying on
// transient Dolt write conflicts. On each attempt it re-reads the bead;
// if the current state is not `from`, it returns ErrTransitionConflict
// immediately (a genuine state mismatch is not retryable — the rig has
// moved on). The optional mutate callback runs after the state field is
// set, letting the caller stamp current-cycle data in the same write.
//
// On a committed transition, a transition attachment is appended for the
// audit log. Attachment failures are returned wrapped but the state
// write has already committed — callers should log, not roll back.
func CASTransition(
	store RigStateStore,
	rig string,
	from, to PerRigCycleState,
	actor string,
	now time.Time,
	mutate func(*RigState),
) error {
	if store == nil {
		return fmt.Errorf("CASTransition: nil store")
	}

	var lastErr error
	for attempt := 0; attempt < transitionCASMaxAttempts; attempt++ {
		s, err := store.LoadRigState(rig)
		if err != nil {
			return fmt.Errorf("loading rig state for %s: %w", rig, err)
		}
		if s.State != from {
			return fmt.Errorf("%w: rig %s is %q, want %q", ErrTransitionConflict, rig, s.State, from)
		}

		s.State = to
		if mutate != nil {
			mutate(&s)
		}

		if err := store.SaveRigState(rig, s); err != nil {
			if isTransientDoltWriteError(err) {
				lastErr = err
				time.Sleep(enabledRigsCASBackoff)
				continue
			}
			return fmt.Errorf("saving rig state for %s: %w", rig, err)
		}

		// State write committed — record the transition (non-fatal).
		if appendErr := store.AppendTransition(TransitionRecord{
			SchemaVersion: 1,
			Rig:           rig,
			From:          string(from),
			To:            string(to),
			At:            now.UTC(),
			Actor:         actor,
		}); appendErr != nil {
			return fmt.Errorf("transition committed but attachment failed for %s (%s→%s): %w",
				rig, from, to, appendErr)
		}
		return nil
	}

	return fmt.Errorf("%w: last error: %v", ErrTownStateCASExhausted, lastErr)
}

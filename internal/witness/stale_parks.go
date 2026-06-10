package witness

import (
	"encoding/json"
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// MountainSkippedLabel marks beads parked (status=blocked) by the Mountain-Eater
// after repeated polecat failures. Such parks are deliberate and must NOT be
// auto-unblocked by the stale-park sweep even if their dependencies resolve.
const MountainSkippedLabel = "mountain:skipped"

// StaleParkedBeadResult records a single stale-park recovery.
type StaleParkedBeadResult struct {
	BeadID           string   // bead that was stuck in status=blocked
	ResolvedBlockers []string // closed external blocker dep edges removed
	DetachedMolecule string   // stale (closed) molecule whose bond was removed, if any
	Unblocked        bool     // true if status was flipped blocked->open
	Error            error
}

// DetectStaleParkedBeadsResult holds aggregate results of the stale-park scan.
type DetectStaleParkedBeadsResult struct {
	Checked   int
	Recovered []StaleParkedBeadResult
	Errors    []error
}

// DetectStaleParkedBeads finds beads parked at status=blocked whose blocking
// dependencies have all resolved (closed) and re-evaluates them so they can
// dispatch again. When a blocker closes, beads (`bd`) does NOT automatically
// flip the dependent's status back to open or drop the now-satisfied dependency
// edge, so a parked bead stays BLOCKED forever and never re-enters dispatch —
// even though it is ready (gs-du4h).
//
// For each qualifying bead the witness performs the same recovery an operator
// would do by hand:
//  1. Remove the satisfied (closed) external blocker dep edges, so they can no
//     longer carry a transitive block on the next readiness evaluation.
//  2. Remove the dep bond of a stale (closed) attached molecule, so a
//     re-dispatch doesn't trip the "existing molecule(s)" guard.
//  3. Flip status blocked->open so the scheduler can re-dispatch.
//  4. Best-effort nudge the deacon to re-evaluate the now-ready bead.
//
// Conservatism (HARD gates — ALL must hold before any mutation):
//   - Status is still "blocked" (re-checked from `bd show` to guard TOCTOU).
//   - The bead is NOT labeled mountain:skipped — those parks are a deliberate
//     Mountain-Eater decision, not a dependency park, and must be left alone.
//   - The bead has at least one EXTERNAL blocking dependency (a "blocks" dep
//     other than its own attached molecule, which blocks its base bead by
//     design). A blocked bead with no external blockers (e.g. a manual park) is
//     never auto-unblocked.
//   - Every external blocker is closed. A single still-open blocker leaves the
//     park intact.
func DetectStaleParkedBeads(bd *BdCli, workDir, rigName string) *DetectStaleParkedBeadsResult {
	result := &DetectStaleParkedBeadsResult{}

	output, err := bd.Exec(workDir, "list", "--status=blocked", "--json", "--limit=0")
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("listing blocked beads: %w", err))
		return result
	}
	if output == "" {
		return result
	}

	type blockedBead struct {
		ID string `json:"id"`
	}
	var blocked []blockedBead
	if err := json.Unmarshal([]byte(output), &blocked); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("parsing blocked beads: %w", err))
		return result
	}

	for _, b := range blocked {
		if b.ID == "" {
			continue
		}
		result.Checked++

		rec, acted := recoverStaleParkedBead(bd, workDir, b.ID)
		if !acted {
			continue
		}
		result.Recovered = append(result.Recovered, rec)
		if rec.Error != nil {
			result.Errors = append(result.Errors, rec.Error)
		}
	}

	return result
}

// recoverStaleParkedBead inspects a single blocked bead and, if its park is
// stale (all external blockers resolved), unblocks it. The bool return is true
// only when the bead qualified and a recovery was attempted (success or error);
// false means the bead was left untouched (still genuinely blocked, deliberate
// park, or unreadable).
func recoverStaleParkedBead(bd *BdCli, workDir, beadID string) (StaleParkedBeadResult, bool) {
	rec := StaleParkedBeadResult{BeadID: beadID}

	output, err := bd.Exec(workDir, "show", beadID, "--json")
	if err != nil || output == "" {
		return rec, false
	}
	var issues []beads.Issue
	if err := json.Unmarshal([]byte(output), &issues); err != nil || len(issues) == 0 {
		return rec, false
	}
	issue := issues[0]

	// TOCTOU re-check: only act on beads still parked at status=blocked.
	if issue.Status != string(beads.StatusBlocked) {
		return rec, false
	}
	// Deliberate Mountain-Eater parks are never auto-unblocked.
	if beads.HasLabel(&issue, MountainSkippedLabel) {
		return rec, false
	}

	// Identify the bead's own attached molecule so it isn't mistaken for an
	// external blocker (a molecule blocks its base bead by design).
	var attachedMol string
	if af := beads.ParseAttachmentFields(&issue); af != nil {
		attachedMol = af.AttachedMolecule
	}

	// Partition blocking dependencies into the attached molecule and external
	// blockers. The park is stale only when every external blocker has closed.
	var closedBlockers []string
	openBlocker := false
	hasExternalBlocker := false
	moleculeClosed := false
	for _, dep := range issue.Dependencies {
		if dep.DependencyType != "blocks" {
			continue
		}
		if attachedMol != "" && dep.ID == attachedMol {
			if dep.Status == string(beads.StatusClosed) {
				moleculeClosed = true
			}
			continue
		}
		hasExternalBlocker = true
		if dep.Status == string(beads.StatusClosed) {
			closedBlockers = append(closedBlockers, dep.ID)
		} else {
			openBlocker = true
		}
	}

	if !hasExternalBlocker || openBlocker {
		return rec, false
	}

	// 1. Drop the satisfied (closed) blocker edges.
	for _, dep := range closedBlockers {
		if err := bd.Run(workDir, "dep", "remove", beadID, dep); err != nil {
			rec.Error = fmt.Errorf("removing closed blocker %s from %s: %w", dep, beadID, err)
			return rec, true
		}
		rec.ResolvedBlockers = append(rec.ResolvedBlockers, dep)
	}

	// 2. Drop a stale (closed) attached molecule's dep bond so a re-dispatch
	//    doesn't trip the "existing molecule(s)" guard.
	if attachedMol != "" && moleculeClosed {
		if err := bd.Run(workDir, "dep", "remove", beadID, attachedMol); err != nil {
			rec.Error = fmt.Errorf("removing stale molecule bond %s from %s: %w", attachedMol, beadID, err)
			return rec, true
		}
		rec.DetachedMolecule = attachedMol
	}

	// 3. Flip status blocked->open so the scheduler can re-dispatch.
	if err := bd.Run(workDir, "update", beadID, "--status=open"); err != nil {
		rec.Error = fmt.Errorf("unblocking %s: %w", beadID, err)
		return rec, true
	}
	rec.Unblocked = true

	// 4. Best-effort nudge the deacon to re-evaluate the now-ready bead
	//    promptly rather than waiting for the next dispatch poll. Nudges create
	//    no bead/Dolt commit, so this is the right channel for a routine signal.
	nudgeDeaconUnparked(beadID)

	return rec, true
}

// nudgeDeaconUnparked sends a best-effort tmux nudge to the deacon that a
// previously-parked bead has been unblocked and is ready for dispatch.
func nudgeDeaconUnparked(beadID string) {
	t := tmux.NewTmux()
	msg := fmt.Sprintf("UNPARKED %s — blocker resolved, status reset to open, please re-evaluate for dispatch", beadID)
	_ = t.NudgeSession(session.DeaconSessionName(), msg)
}

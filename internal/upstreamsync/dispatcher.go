// Polecat dispatcher for upstream-sync conflict resolution.
//
// Phase 4 (gu-g5gh). When a merge produces conflicts AND the complexity
// gate (complexity.go) classifies them as resolvable, this file's
// DispatchConflictResolution helper:
//
//  1. Creates a P1 work bead carrying the conflict context (files,
//     restricted paths, branch, upstream/base SHAs, attempt id).
//  2. Files a sling-context bead with formula
//     `mol-polecat-conflict-resolve` so the scheduler picks the work
//     up on the next heartbeat tick.
//  3. Stamps the resolution branch + polecat work bead id onto the
//     state bead's CurrentAttempt so the operator can follow along
//     and resume on session death (cv-2s6tq/data.md §"Current Attempt").
//
// The dispatcher does not transition the state machine itself —
// callers (upstream_sync.go in Phase 4 wiring, deacon patrol later)
// own the StateChecking → StateResolving transition and pass the
// already-mutated state bead's `CurrentAttempt` to this helper.
//
// Why a separate file: the bead-creation surface needs the full
// `internal/beads` import + `internal/scheduler/capacity` import for
// the sling-context shape. Keeping it isolated from cooldown.go and
// transitions.go (which are pure-state) preserves the unit-test
// boundary.
//
// Design context: .designs/cv-2s6tq/api.md §"Conflict Resolution: Polecat Dispatch",
// .designs/cv-2s6tq/security.md §"Polecat dispatch".
package upstreamsync

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// DispatchFormula is the formula attached to the polecat work bead for
// upstream-sync conflict resolution. The existing
// mol-polecat-conflict-resolve molecule already covers the rebase →
// resolve → push flow (formula: internal/formula/formulas/
// mol-polecat-conflict-resolve.formula.toml). Phase 4 reuses it; a
// future Phase 4.5 may swap in a sync-specific molecule that handles
// the push-to-main hand-off differently.
const DispatchFormula = "mol-polecat-conflict-resolve"

// DispatchLabel is the canonical label for upstream-sync conflict
// resolution work beads. Audit patrols and `gt upstream history` filter
// by this label.
const DispatchLabel = "gt:upstream-sync-conflict"

// ConflictDispatchPayload is the structured envelope persisted on the
// work bead's description so the polecat (and audit dashboards) can
// reconstruct the merge context without re-parsing git state.
type ConflictDispatchPayload struct {
	// Version pins the payload shape for forward compatibility. Always 1.
	Version int `json:"version"`

	// Rig is the rig the sync belongs to (mirrors SyncStateMetadata.Rig).
	Rig string `json:"rig"`

	// AttemptID is the SyncAttempt.ID that produced the conflict.
	AttemptID string `json:"attempt_id"`

	// UpstreamRemote is the configured upstream remote (e.g., "upstream").
	UpstreamRemote string `json:"upstream_remote"`

	// UpstreamBranch is the configured upstream branch (e.g., "main").
	UpstreamBranch string `json:"upstream_branch"`

	// UpstreamSHA is the upstream commit being synced to.
	UpstreamSHA string `json:"upstream_sha"`

	// TargetBranch is the local/origin branch the merge targets.
	TargetBranch string `json:"target_branch"`

	// TargetSHA is the target branch's HEAD before the merge.
	TargetSHA string `json:"target_sha"`

	// ResolutionBranch is the temporary branch the polecat should work
	// on (and ultimately push). Convention:
	// `upstream-sync/<rig>/<attempt-id>`.
	ResolutionBranch string `json:"resolution_branch"`

	// ConflictedFiles is the list reported by DetectConflicts.
	ConflictedFiles []string `json:"conflicted_files"`

	// HunkCount is the total number of conflict hunks (0 if unknown).
	HunkCount int `json:"hunk_count,omitempty"`

	// RestrictedPaths is the configured allowlist boundary — the polecat
	// MUST refuse to modify any file matching one of these patterns.
	// Mirrors the security design's "constrained permissions" requirement.
	RestrictedPaths []string `json:"restricted_paths"`

	// Strategy is the merge strategy to use ("merge" or "rebase").
	Strategy string `json:"strategy"`

	// EnqueuedAt is the RFC3339 timestamp when dispatch happened.
	EnqueuedAt string `json:"enqueued_at"`
}

// DispatchInput is the call-site input for DispatchConflictResolution.
// Mirrors the SyncStateMetadata + Conflict report fields the dispatcher
// needs without forcing callers to construct an entire SyncStateMetadata
// just to dispatch (useful for tests).
type DispatchInput struct {
	Rig             string
	AttemptID       string
	UpstreamRemote  string
	UpstreamBranch  string
	UpstreamSHA     string
	TargetBranch    string
	TargetSHA       string
	ConflictedFiles []string
	HunkCount       int
	RestrictedPaths []string
	Strategy        string
	Actor           string
}

// DispatchResult records what was created so callers can stamp it onto
// the state bead's CurrentAttempt.
type DispatchResult struct {
	WorkBeadID       string
	ContextBeadID    string
	ResolutionBranch string
}

// DispatchConflictResolution creates the work + sling-context beads
// that hand a conflict to a polecat.
//
// rigBeads is the *Beads handle pointing at the TARGET RIG's beads
// directory. BOTH the work bead and the sling-context are created here.
//
// Why the work bead must live in the rig DB (gu-pinfi): `gt sling`'s
// pre-dispatch check (cmd/sling_helpers.go) refuses to dispatch a bead
// that is not present in the target rig's beads database. A work bead
// created in the town/hq DB carries an hq-/gc- prefix that the rig DB
// doesn't contain, so sling rejects it ("bead not present in target rig
// beads database") and the fork-sync stalls. Creating the work bead in
// the rig DB gives it the rig's prefix and keeps it co-located with the
// sling-context that references it (so the tracks dep link resolves too).
//
// Returns the IDs of the created beads. Errors at any step abort the
// dispatch — partial creation is acceptable because the work bead is
// labeled and the sling-context references its ID, so a stranded work
// bead is harmless (audit patrol will close it on the next pass).
func DispatchConflictResolution(rigBeads *beads.Beads, in DispatchInput) (DispatchResult, error) {
	if rigBeads == nil {
		return DispatchResult{}, fmt.Errorf("DispatchConflictResolution: nil beads handle")
	}
	if in.Rig == "" || in.AttemptID == "" {
		return DispatchResult{}, fmt.Errorf("DispatchConflictResolution: rig and attempt_id required")
	}
	if len(in.ConflictedFiles) == 0 {
		return DispatchResult{}, fmt.Errorf("DispatchConflictResolution: no conflicted files (caller should handle clean merges before dispatch)")
	}

	resolutionBranch := buildResolutionBranch(in.Rig, in.AttemptID)
	now := nowFn().UTC().Format(time.RFC3339)

	payload := ConflictDispatchPayload{
		Version:          1,
		Rig:              in.Rig,
		AttemptID:        in.AttemptID,
		UpstreamRemote:   in.UpstreamRemote,
		UpstreamBranch:   in.UpstreamBranch,
		UpstreamSHA:      in.UpstreamSHA,
		TargetBranch:     in.TargetBranch,
		TargetSHA:        in.TargetSHA,
		ResolutionBranch: resolutionBranch,
		ConflictedFiles:  in.ConflictedFiles,
		HunkCount:        in.HunkCount,
		RestrictedPaths:  in.RestrictedPaths,
		Strategy:         in.Strategy,
		EnqueuedAt:       now,
	}

	descRaw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return DispatchResult{}, fmt.Errorf("marshaling dispatch payload: %w", err)
	}

	// 1. Create the work bead in the target rig's DB (gu-pinfi). It must
	// share the rig's prefix so `gt sling`'s target-rig presence check
	// passes. P1 because conflict resolution blocks all future syncs for
	// this rig — operators want it picked up first.
	workTitle := buildWorkBeadTitle(in)
	workIssue, err := rigBeads.Create(beads.CreateOptions{
		Title:       workTitle,
		Description: buildWorkBeadDescription(payload, string(descRaw)),
		Labels:      []string{"gt:task", DispatchLabel, "rig:" + in.Rig},
		Priority:    1,
		Type:        "task",
		Actor:       in.Actor,
	})
	if err != nil {
		return DispatchResult{}, fmt.Errorf("creating conflict-resolution work bead: %w", err)
	}

	// 2. File a sling-context bead pointing at the work bead. The
	// scheduler picks this up on its next tick.
	args := slingArgs(payload)
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return DispatchResult{WorkBeadID: workIssue.ID}, fmt.Errorf("marshaling sling args: %w", err)
	}

	ctxFields := &capacity.SlingContextFields{
		Version:      1,
		WorkBeadID:   workIssue.ID,
		TargetRig:    in.Rig,
		Formula:      DispatchFormula,
		Args:         string(argsJSON),
		ResumeBranch: resolutionBranch,
		BaseBranch:   in.TargetBranch,
		Mode:         "upstream-sync-conflict",
		EnqueuedAt:   now,
	}

	ctxBead, err := rigBeads.CreateSlingContext(workTitle, workIssue.ID, ctxFields)
	if err != nil {
		return DispatchResult{WorkBeadID: workIssue.ID, ResolutionBranch: resolutionBranch},
			fmt.Errorf("filing sling-context: %w", err)
	}

	return DispatchResult{
		WorkBeadID:       workIssue.ID,
		ContextBeadID:    ctxBead.ID,
		ResolutionBranch: resolutionBranch,
	}, nil
}

// SlingArgs is the structured envelope persisted on the sling-context
// bead's args field. The polecat's mol-polecat-conflict-resolve formula
// reads it to know which branch + base + conflict context to pull in.
type SlingArgs struct {
	Mode             string   `json:"mode"`
	Rig              string   `json:"rig"`
	AttemptID        string   `json:"attempt_id"`
	ResolutionBranch string   `json:"resolution_branch"`
	BaseBranch       string   `json:"base_branch"`
	UpstreamRemote   string   `json:"upstream_remote"`
	UpstreamBranch   string   `json:"upstream_branch"`
	UpstreamSHA      string   `json:"upstream_sha"`
	ConflictedFiles  []string `json:"conflicted_files"`
	RestrictedPaths  []string `json:"restricted_paths"`
}

func slingArgs(payload ConflictDispatchPayload) SlingArgs {
	return SlingArgs{
		Mode:             "upstream-sync-conflict",
		Rig:              payload.Rig,
		AttemptID:        payload.AttemptID,
		ResolutionBranch: payload.ResolutionBranch,
		BaseBranch:       payload.TargetBranch,
		UpstreamRemote:   payload.UpstreamRemote,
		UpstreamBranch:   payload.UpstreamBranch,
		UpstreamSHA:      payload.UpstreamSHA,
		ConflictedFiles:  payload.ConflictedFiles,
		RestrictedPaths:  payload.RestrictedPaths,
	}
}

// buildResolutionBranch computes the branch name the polecat will
// rebase + push from. Convention from cv-2s6tq/data.md §"Constraints":
//
//	`upstream-sync/<rig>/<attempt-id>`
func buildResolutionBranch(rig, attemptID string) string {
	return fmt.Sprintf("upstream-sync/%s/%s", rig, attemptID)
}

// buildWorkBeadTitle is the human-readable title shown in `bd show` and
// in the polecat's hook. Prefix is fixed so audit patrols can grep.
func buildWorkBeadTitle(in DispatchInput) string {
	if len(in.ConflictedFiles) == 0 {
		return fmt.Sprintf("upstream-sync conflict in %s (%s)", in.Rig, in.AttemptID)
	}
	if len(in.ConflictedFiles) == 1 {
		return fmt.Sprintf("upstream-sync conflict in %s: %s", in.Rig, in.ConflictedFiles[0])
	}
	return fmt.Sprintf("upstream-sync conflict in %s: %s and %d more",
		in.Rig, in.ConflictedFiles[0], len(in.ConflictedFiles)-1)
}

// buildWorkBeadDescription renders the human-readable header plus the
// machine-readable payload. The polecat parses the JSON; humans use the
// header for context.
func buildWorkBeadDescription(p ConflictDispatchPayload, payloadJSON string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Resolve merge conflict between %s/%s and origin/%s for rig %s.\n\n",
		p.UpstreamRemote, p.UpstreamBranch, p.TargetBranch, p.Rig)
	fmt.Fprintf(&sb, "## Metadata\n")
	fmt.Fprintf(&sb, "- Attempt ID: %s\n", p.AttemptID)
	fmt.Fprintf(&sb, "- Resolution branch: %s\n", p.ResolutionBranch)
	fmt.Fprintf(&sb, "- Upstream SHA: %s\n", p.UpstreamSHA)
	fmt.Fprintf(&sb, "- Target SHA (pre-merge): %s\n", p.TargetSHA)
	fmt.Fprintf(&sb, "- Strategy: %s\n", p.Strategy)
	fmt.Fprintf(&sb, "- Conflicted files (%d):\n", len(p.ConflictedFiles))
	for _, f := range p.ConflictedFiles {
		fmt.Fprintf(&sb, "    - %s\n", f)
	}
	if p.HunkCount > 0 {
		fmt.Fprintf(&sb, "- Hunk count: %d\n", p.HunkCount)
	}
	if len(p.RestrictedPaths) > 0 {
		fmt.Fprintf(&sb, "\n## Restricted paths (DO NOT MODIFY)\n")
		for _, r := range p.RestrictedPaths {
			fmt.Fprintf(&sb, "- %s\n", r)
		}
	}
	fmt.Fprintf(&sb, "\n## Instructions\n")
	fmt.Fprintf(&sb, "1. Fetch upstream and check out %s onto %s.\n", p.ResolutionBranch, p.TargetBranch)
	fmt.Fprintf(&sb, "2. Attempt the merge (%s) and resolve only the listed conflicted files.\n", p.Strategy)
	fmt.Fprintf(&sb, "3. Run the configured gate suite. Do NOT modify restricted paths.\n")
	fmt.Fprintf(&sb, "4. Push %s and update the state bead's CurrentAttempt to mark conflict resolution complete.\n", p.ResolutionBranch)
	fmt.Fprintf(&sb, "\n## Payload (JSON)\n```json\n%s\n```\n", payloadJSON)
	return sb.String()
}

// auto-test-pr CLI: revise verb.
//
// Phase 0 task 2c (gu-y9us). The manual fallback from D17 — lets a
// maintainer trigger the revision polecat directly when
// feedback-patrol routing is not yet live (Phase 1).
//
// The CLI:
//   (a) reads the MR bead
//   (b) extracts comment thread + last commit SHA
//   (c) CAS-transitions rig state bead mr-pending → mr-revising
//   (d) files a sling-context bead with args.mode=revise
//   (e) dispatches mol-polecat-work-test-improver
//
// Design context: .designs/auto-test-pr/synthesis.md §D17
// and §"Implementation Plan, Phase 0 task 2c".
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/autotestpr"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/workspace"
)

// CLI flag bindings for `revise`.
var (
	autoTestPRReviseMR        string
	autoTestPRReviseCommentID string
)

// FormulaTestImprover is the formula name dispatched by the revise
// command. Exported so tests can verify the dispatch request without
// hard-coding a string.
const FormulaTestImprover = "mol-polecat-work-test-improver"

var autoTestPRReviseCmd = &cobra.Command{
	Use:   "revise",
	Short: "Trigger a revision polecat for an auto-test-pr MR (D17 manual fallback)",
	Long: `Trigger a revision polecat on an existing auto-test-pr merge request.

This is the Phase-1 manual fallback (D17) for PRD G4's "feedback-driven
revision on the same PR" requirement. When the automated
mol-pr-feedback-patrol is not yet live, a maintainer can invoke this
CLI to dispatch a polecat that reads the reviewer's comments and pushes
a follow-up commit addressing the feedback.

The CLI:
  (a) reads the MR bead (--mr=<id>)
  (b) extracts comment thread + last commit SHA
  (c) CAS-transitions the rig state bead from mr-pending → mr-revising
  (d) files a sling-context bead with args.mode=revise
  (e) dispatches mol-polecat-work-test-improver

--comment-id is optional: when omitted, the polecat picks the most
recent non-resolved comment thread (D19 fallback).

Examples:

  gt auto-test-pr revise --mr=gt-mr-abc12
  gt auto-test-pr revise --mr=gt-mr-abc12 --comment-id=cmt-42`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRRevise,
}

func init() {
	autoTestPRReviseCmd.Flags().StringVar(&autoTestPRReviseMR, "mr", "",
		"MR bead ID to revise (required)")
	autoTestPRReviseCmd.Flags().StringVar(&autoTestPRReviseCommentID, "comment-id", "",
		"Specific comment thread to address (optional; defaults to most-recent non-resolved)")

	autoTestPRCmd.AddCommand(autoTestPRReviseCmd)
}

// ReviseArgs is the structured envelope filed in the sling-context bead's
// Args field as JSON. The dispatched polecat reads this to know which MR
// to revise, what comments to address, and what commit to base its changes
// on.
type ReviseArgs struct {
	Mode      string `json:"mode"`
	MRID      string `json:"mr_id"`
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha"`
	Rig       string `json:"rig"`
	CommentID string `json:"comment_id,omitempty"`
}

// ErrMRNotPending is returned when the MR's rig state is not mr-pending
// and therefore cannot transition to mr-revising.
var ErrMRNotPending = errors.New("rig state is not mr-pending")

// ErrMRNotAutoTestPR is returned when the MR bead does not carry the
// gt:auto-test-pr label required by the revise command.
var ErrMRNotAutoTestPR = errors.New("MR bead is not labeled gt:auto-test-pr")

func runAutoTestPRRevise(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	if autoTestPRReviseMR == "" {
		fmt.Fprintln(stderr, "gt auto-test-pr revise: --mr is required")
		fmt.Fprintln(stderr, "  example: gt auto-test-pr revise --mr=gt-mr-abc12")
		return NewSilentExit(2)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("locating town root: %w", err)
	}

	// (a) Read the MR bead.
	townBeads := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
	mrIssue, err := townBeads.Show(autoTestPRReviseMR)
	if err != nil {
		return fmt.Errorf("reading MR bead %s: %w", autoTestPRReviseMR, err)
	}

	// Validate the MR bead carries the auto-test-pr label.
	if !beads.HasLabel(mrIssue, "gt:auto-test-pr") {
		fmt.Fprintf(stderr, "gt auto-test-pr revise: MR %s does not carry the gt:auto-test-pr label\n", autoTestPRReviseMR)
		return ErrMRNotAutoTestPR
	}

	// (b) Extract comment thread + last commit SHA from the MR bead.
	mrFields := beads.ParseMRFields(mrIssue)
	if mrFields == nil {
		return fmt.Errorf("MR bead %s has no parseable MR fields", autoTestPRReviseMR)
	}
	if mrFields.CommitSHA == "" {
		return fmt.Errorf("MR bead %s has no commit_sha field", autoTestPRReviseMR)
	}
	if mrFields.Branch == "" {
		return fmt.Errorf("MR bead %s has no branch field", autoTestPRReviseMR)
	}

	// Resolve the target rig from the MR bead's labels (rig:<name>).
	targetRig := extractRigFromMRLabels(mrIssue.Labels)
	if targetRig == "" {
		// Fallback: try the MR fields' Rig field.
		if mrFields.Rig != "" {
			targetRig = mrFields.Rig
		} else {
			return fmt.Errorf("MR bead %s has no rig:<target_rig> label and no rig field", autoTestPRReviseMR)
		}
	}

	// (c) CAS-transition rig state bead: mr-pending → mr-revising.
	// Phase 0: the rig state lives in the town-state bead's RigSummary.
	if err := casTransitionRigState(townBeads, targetRig, "mr-pending", "mr-revising"); err != nil {
		if errors.Is(err, ErrMRNotPending) {
			fmt.Fprintf(stderr, "gt auto-test-pr revise: rig %s state is not mr-pending — cannot transition to mr-revising\n", targetRig)
			return NewSilentExit(3)
		}
		return fmt.Errorf("CAS-transitioning rig state: %w", err)
	}
	fmt.Fprintf(stdout, "✓ rig %s state: mr-pending → mr-revising\n", targetRig)

	// (d) File a sling-context bead with args.mode=revise.
	reviseArgs := ReviseArgs{
		Mode:      "revise",
		MRID:      autoTestPRReviseMR,
		Branch:    mrFields.Branch,
		CommitSHA: mrFields.CommitSHA,
		Rig:       targetRig,
		CommentID: autoTestPRReviseCommentID,
	}
	argsJSON, err := json.Marshal(reviseArgs)
	if err != nil {
		return fmt.Errorf("marshaling revise args: %w", err)
	}

	// Resolve the source issue from the MR to use as the work bead for the context.
	workBeadID := mrFields.SourceIssue
	if workBeadID == "" {
		workBeadID = autoTestPRReviseMR // Fallback: use the MR itself as work bead.
	}

	rigBeadsDir := doltserver.FindRigBeadsDir(townRoot, targetRig)
	rigBeads := beads.NewWithBeadsDir(townRoot, rigBeadsDir)

	slingFields := &capacity.SlingContextFields{
		Version:    1,
		WorkBeadID: workBeadID,
		TargetRig:  targetRig,
		Formula:    FormulaTestImprover,
		Args:       string(argsJSON),
		Mode:       "revise",
		EnqueuedAt: nowFn().UTC().Format(time.RFC3339),
	}

	ctxBead, err := rigBeads.CreateSlingContext(
		fmt.Sprintf("revise: %s", mrIssue.Title),
		workBeadID,
		slingFields,
	)
	if err != nil {
		return fmt.Errorf("filing sling-context bead: %w", err)
	}
	fmt.Fprintf(stdout, "✓ sling-context bead: %s (mode=revise, formula=%s)\n", ctxBead.ID, FormulaTestImprover)

	// (e) Dispatch mol-polecat-work-test-improver.
	// The sling-context bead is the dispatch primitive — the scheduler
	// picks it up on its next heartbeat tick. No explicit dispatch call
	// needed in Phase 0: the scheduler's pipeline reads open sling-context
	// beads and dispatches them. Print confirmation.
	fmt.Fprintf(stdout, "✓ dispatched: %s will pick up context %s on next scheduler tick\n",
		FormulaTestImprover, ctxBead.ID)
	fmt.Fprintf(stdout, "\n  MR:         %s\n", autoTestPRReviseMR)
	fmt.Fprintf(stdout, "  Branch:     %s\n", mrFields.Branch)
	fmt.Fprintf(stdout, "  Commit:     %s\n", mrFields.CommitSHA)
	fmt.Fprintf(stdout, "  Rig:        %s\n", targetRig)
	if autoTestPRReviseCommentID != "" {
		fmt.Fprintf(stdout, "  Comment:    %s\n", autoTestPRReviseCommentID)
	} else {
		fmt.Fprintf(stdout, "  Comment:    (most-recent non-resolved — D19 fallback)\n")
	}

	return nil
}

// extractRigFromMRLabels extracts the rig name from labels in the form
// "rig:<name>". Returns "" if no such label is found.
func extractRigFromMRLabels(labels []string) string {
	const prefix = "rig:"
	for _, l := range labels {
		if len(l) > len(prefix) && l[:len(prefix)] == prefix {
			return l[len(prefix):]
		}
	}
	return ""
}

// RigCycleState is the per-rig auto-test-pr state stored in the town-state
// bead's RigSummary field. This is the Phase 0 shape — Phase 1 task 15
// migrates to a per-rig pinned bead.
type RigCycleState struct {
	State        string `json:"state"`
	LastTransAt  string `json:"last_transition_at,omitempty"`
	LastActor    string `json:"last_actor,omitempty"`
	CurrentMRID  string `json:"current_mr_id,omitempty"`
}

// casTransitionRigState performs a CAS (compare-and-swap) transition on
// the rig's cycle state in the town-state bead's RigSummary. The
// transition only succeeds if the current state matches `from`; the state
// is set to `to`.
//
// Phase 0 implementation: the rig state lives in TownState.RigSummary
// as a RigCycleState JSON object keyed by rig name.
func casTransitionRigState(b *beads.Beads, rigName, from, to string) error {
	return autotestpr.MutateTownStateForRevise(b, func(s *autotestpr.TownState) error {
		if s.RigSummary == nil {
			s.RigSummary = map[string]json.RawMessage{}
		}

		var current RigCycleState
		if raw, ok := s.RigSummary[rigName]; ok {
			if err := json.Unmarshal(raw, &current); err != nil {
				return fmt.Errorf("parsing rig %s state: %w", rigName, err)
			}
		}

		if current.State != from {
			return ErrMRNotPending
		}

		current.State = to
		current.LastTransAt = nowFn().UTC().Format(time.RFC3339)
		current.LastActor = resolveOperatorActor()

		raw, err := json.Marshal(current)
		if err != nil {
			return fmt.Errorf("marshaling rig %s state: %w", rigName, err)
		}
		s.RigSummary[rigName] = raw
		return nil
	})
}

// Git-evidence fallback for the post-merge orphan reconcile (gu-hrweu).
//
// The agent-bead-based reconcile (orphan_reconcile.go, gu-7igu8) proves a
// merge by reading the MR wisp bead that the agent bead's `active_mr` points
// at. That proof is fragile across reaper cycles: a competing pass can destroy
// BOTH artifacts the agent-bead reconcile depends on before the reconcile
// cycle that would have used them runs —
//   1. the agent bead's `active_mr` ref (cleared by scrub-active-mr / gu-dhqm), and
//   2. the MR wisp bead itself (deleted by `gt reaper purge`).
// Once the MR wisp is purged AND active_mr is cleared, the orphan signature is
// GONE: the merged work can no longer be proven via beads, so the agent-bead
// reconcile skips it forever and the source issue stays non-terminal — freezing
// any convoy that depends on it (observed: workflow hq-wf-ojzs6 step 1).
//
// The only artifacts that SURVIVE that race are:
//   - the source issue's `awaiting_refinery_merge` label, stamped directly on
//     the source bead by the polecat's `gt done` (MarkAwaitingRefineryMerge) —
//     purge/scrub never touch source-issue labels; and
//   - the merged commit on the target branch, which cites the source bead ID
//     in its message (the polecat's commit trailer). A merged commit is
//     permanent — no scrubber deletes git history.
//
// This pass anchors the reconcile on those two durable artifacts instead of on
// the purgeable beads. For each non-terminal source issue still carrying
// `awaiting_refinery_merge`, it asks a GitMergeProof whether a commit citing
// the bead ID has landed on the target branch. If so, it completes the
// refinery's interrupted reconcile exactly as the agent-bead pass would:
// force-close the source issue and clear the leaked label.
//
// Safety invariants (mirror orphan_reconcile.go):
//   - A close happens ONLY when git PROVES the merge (a citing commit exists on
//     the target branch). When the prover cannot verify (no worktree, git
//     error), the pass fails CLOSED — it leaves the bead open. Absence of proof
//     is never treated as proof.
//   - Already-terminal source issues are skipped (idempotent). The label may
//     leak onto a closed bead, but a closed bead is harmless to dispatch, so we
//     do not touch it here.
//   - Per-bead failures are tolerated: a single failed close or git probe is
//     recorded as an anomaly and the scan continues.
//
// This pass is COMPLEMENTARY to ReconcileMergedOrphans, not a replacement: the
// agent-bead pass remains the fast path while the MR wisp + active_mr still
// exist; this pass catches the survivors after the race has erased them.

package reaper

import (
	"errors"
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// GitMergeProof proves whether a source issue's work landed on its target
// branch via durable git evidence (a commit citing the bead ID), independent
// of any beads that `gt reaper purge` / scrub-active-mr can delete.
type GitMergeProof interface {
	// ProveMerged reports whether the issue's work is provably merged.
	//   proven   — a commit citing the bead ID exists on the target branch.
	//   verified — the check actually ran (a git worktree was found and the
	//              git command succeeded). When verified is false, proven is
	//              meaningless and the caller MUST fail closed (never close the
	//              source issue): absence of a usable worktree or a git error is
	//              not proof the work landed.
	ProveMerged(issue *beads.Issue) (proven bool, verified bool)
}

// GitReconcileOrphansResult summarizes a single
// ReconcileMergedOrphansByGitEvidence run.
type GitReconcileOrphansResult struct {
	// Scanned is the number of awaiting_refinery_merge source issues inspected.
	Scanned int `json:"scanned"`
	// Reconciled is the number of orphaned source issues closed this run.
	Reconciled int `json:"reconciled"`
	// AlreadyTerminal is the number of candidates skipped because the source
	// issue was already closed/tombstone (idempotent / already reconciled).
	AlreadyTerminal int `json:"already_terminal"`
	// NotYetMerged is the number of candidates whose git check ran but found no
	// citing commit on the target branch — the work has not landed yet, so the
	// bead is correctly left open.
	NotYetMerged int `json:"not_yet_merged"`
	// Unverified is the number of candidates whose merge status could not be
	// proven (no worktree / git error). Fail-closed: left open.
	Unverified int `json:"unverified"`
	// DryRun is true when the pass reported what it would close without closing.
	DryRun bool `json:"dry_run,omitempty"`
	// ReconciledEntries records each source issue that was (or would be) closed.
	ReconciledEntries []GitReconcileOrphanEntry `json:"reconciled_entries,omitempty"`
	// Anomalies records any unexpected per-bead failures (best-effort).
	Anomalies []Anomaly `json:"anomalies,omitempty"`
}

// GitReconcileOrphanEntry records a single reconciled (or would-be-reconciled)
// source issue.
type GitReconcileOrphanEntry struct {
	SourceIssue string `json:"source_issue"`
}

// OrphanGitReconcileBeads is the minimal interface
// ReconcileMergedOrphansByGitEvidence needs from a beads client.
type OrphanGitReconcileBeads interface {
	polecat.IssueReader
	// ListIssuesWithLabel returns every issue carrying the given label, at any
	// status, across all rig databases (source issues live in per-rig DBs).
	ListIssuesWithLabel(label string) ([]*beads.Issue, error)
	ForceCloseWithReason(reason string, ids ...string) error
	Update(id string, opts beads.UpdateOptions) error
}

// ReconcileMergedOrphansByGitEvidence scans every source issue still carrying
// the awaiting_refinery_merge label and, for each whose work is provably merged
// to its target branch (via git evidence), completes the refinery's interrupted
// post-merge reconcile by force-closing the source issue and clearing the
// leaked label.
//
// This is the durable-artifact fallback to ReconcileMergedOrphans (gu-hrweu):
// it does not depend on the agent bead's active_mr or the MR wisp bead, both of
// which a competing reaper cycle can destroy before the agent-bead reconcile
// would have used them.
//
// The function tolerates per-bead failures: a single failed close, git probe,
// or label clear is recorded as an anomaly and the scan continues. It returns a
// non-nil error only when the initial label listing itself fails.
func ReconcileMergedOrphansByGitEvidence(bd OrphanGitReconcileBeads, prover GitMergeProof, dryRun bool) (*GitReconcileOrphansResult, error) {
	if bd == nil {
		return nil, fmt.Errorf("reconcile orphans (git): nil beads client")
	}
	if prover == nil {
		return nil, fmt.Errorf("reconcile orphans (git): nil git prover")
	}

	candidates, err := bd.ListIssuesWithLabel(awaitingRefineryMergeLabel)
	if err != nil {
		return nil, fmt.Errorf("reconcile orphans (git): list %s: %w", awaitingRefineryMergeLabel, err)
	}

	result := &GitReconcileOrphansResult{DryRun: dryRun}
	for _, candidate := range candidates {
		if candidate == nil || candidate.ID == "" {
			continue
		}
		result.Scanned++

		// Re-read for a fresh status: the agent-bead reconcile (gu-7igu8) or the
		// refinery itself may have closed the source issue earlier this cycle.
		// A missing bead (purged/reaped) is treated as terminal — nothing to do.
		fresh, err := bd.Show(candidate.ID)
		if err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				result.AlreadyTerminal++
				continue
			}
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "orphan_git_source_show_failed",
				Message: fmt.Sprintf("source_issue=%s: %v", candidate.ID, err),
			})
			continue
		}
		if fresh == nil || beads.IssueStatus(fresh.Status).IsTerminal() {
			result.AlreadyTerminal++
			continue
		}

		proven, verified := prover.ProveMerged(fresh)
		if !verified {
			// Fail closed: no usable worktree or a git error. Leave the bead open.
			result.Unverified++
			continue
		}
		if !proven {
			// Git check ran but the work has not landed on the target branch yet.
			result.NotYetMerged++
			continue
		}

		entry := GitReconcileOrphanEntry{SourceIssue: fresh.ID}

		if dryRun {
			result.Reconciled++
			result.ReconciledEntries = append(result.ReconciledEntries, entry)
			continue
		}

		closeReason := fmt.Sprintf("Merged (git evidence: commit citing %s on target branch; "+
			"post-merge reconcile completed by reaper after MR wisp purged — gu-hrweu)", fresh.ID)
		if err := bd.ForceCloseWithReason(closeReason, fresh.ID); err != nil {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "orphan_git_source_close_failed",
				Message: fmt.Sprintf("source_issue=%s: %v", fresh.ID, err),
			})
			continue
		}

		// Clear the leaked awaiting_refinery_merge label, as the refinery's
		// PostMerge path would have. Best-effort: the close already completed the
		// critical part of the reconcile, so a label-clear failure is non-fatal.
		if err := bd.Update(fresh.ID, beads.UpdateOptions{
			RemoveLabels: []string{awaitingRefineryMergeLabel},
		}); err != nil {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "orphan_git_label_clear_failed",
				Message: fmt.Sprintf("source_issue=%s: %v", fresh.ID, err),
			})
		}

		result.Reconciled++
		result.ReconciledEntries = append(result.ReconciledEntries, entry)
	}

	return result, nil
}

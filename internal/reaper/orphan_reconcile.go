// Post-merge orphan reconcile for the reaper package.
//
// Background (gu-7igu8 root cause): the refinery's post-merge sequence —
// close MR → close source issue → unhook bead → enable polecat reap — is NOT
// atomic. When the refinery is interrupted mid-reconcile (latch/restart, or it
// proceeds to the next MR before finishing) AFTER the MR is closed/merged but
// BEFORE the source issue is closed, the source issue is left non-terminal
// (typically HOOKED on a now-dead polecat). This is pure bookkeeping drift —
// the work is provably on main — but it blocks `gt polecat nuke` (refuses:
// active_mr=closed but source_status=hooked) and leaves a stale HOOKED bead
// that can mislead dispatch.
//
// This scrubber detects that exact signature and completes the reconcile
// itself: for each agent bead whose active_mr points at a PROVEN-MERGED MR
// whose source issue is still non-terminal, it force-closes the source issue
// with a "Merged in <mr>" reason (which transitions it out of HOOKED) and
// clears the leaked awaiting_refinery_merge label. The follow-on active_mr
// scrub (gu-dhqm) then clears the now-danglng active_mr ref in the same cycle,
// since the source is finally terminal.
//
// Why AUTOMATE rather than ESCALATE (contrast restart_pending_dog /
// scheduler_stuck_dog, which escalate): the remediation here is deterministic,
// idempotent, and provably safe. A merged MR is definitive proof the work
// landed on the target branch, so there is no judgment call and no data-loss
// risk — only the refinery's own post-merge close, replayed. The escalate-only
// dogs guard actions with loop/judgment risk (self-restart, scheduler nuke);
// this one does not.
//
// Safety invariants:
//   - ONLY merged MRs trigger a source close. Rejected/superseded/conflict or
//     missing MR beads are skipped — the work did NOT land, so closing the
//     source issue would lose it.
//   - Polecats preserving human WIP (cleanup_status has_uncommitted/has_stash/
//     has_unpushed) are skipped, mirroring the active_mr scrub (gc-eysed).
//   - Idempotent: a source issue that is already terminal produces no orphan,
//     and a missing source issue is skipped.
//
// Routing note: like ScrubStaleActiveMR, the caller passes a town-rooted
// (ForAgentBead) beads client to enumerate agent beads. Source-issue closes
// reference rig-prefixed IDs (e.g. gu-*) which bd's routes.jsonl resolves back
// to the owning rig database even though the client is rooted at town — so the
// force-close lands in the correct rig DB.

package reaper

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// awaitingRefineryMergeLabel is the label a polecat's `gt done` adds to a
// source issue when it submits an MR. The refinery's PostMerge path clears it
// on a clean reconcile; an interrupted reconcile leaks it onto the
// closed-and-cited bead. We clear it as part of completing the reconcile.
// Kept in sync with completion.awaitingRefineryMergeLabel and
// refinery/manager.go's RemoveLabels usage.
const awaitingRefineryMergeLabel = "awaiting_refinery_merge"

// ReconcileOrphansResult summarizes a single ReconcileMergedOrphans run.
type ReconcileOrphansResult struct {
	// Scanned is the number of agent beads inspected.
	Scanned int `json:"scanned"`
	// HadActiveMR is the number of agent beads with a non-empty active_mr field.
	HadActiveMR int `json:"had_active_mr"`
	// Reconciled is the number of orphaned source issues closed this run.
	Reconciled int `json:"reconciled"`
	// PreservedWIP is the number of agent beads skipped because their
	// cleanup_status indicates human-WIP that must be preserved (gc-eysed).
	PreservedWIP int `json:"preserved_wip"`
	// DryRun is true when the scrubber reported what it would close without
	// actually closing anything.
	DryRun bool `json:"dry_run,omitempty"`
	// ReconciledEntries records each orphaned source issue that was (or would
	// be) closed.
	ReconciledEntries []ReconcileOrphanEntry `json:"reconciled_entries,omitempty"`
	// Anomalies records any unexpected per-bead failures (best-effort; the
	// scrubber tolerates them and continues).
	Anomalies []Anomaly `json:"anomalies,omitempty"`
}

// ReconcileOrphanEntry records a single reconciled (or would-be-reconciled)
// orphaned source issue.
type ReconcileOrphanEntry struct {
	AgentBeadID string `json:"agent_bead_id"`
	ActiveMR    string `json:"active_mr"`
	SourceIssue string `json:"source_issue"`
}

// OrphanReconcileBeads is the minimal interface ReconcileMergedOrphans needs
// from a beads client. *beads.Beads satisfies it directly.
type OrphanReconcileBeads interface {
	polecat.IssueReader
	ListAgentBeads() (map[string]*beads.Issue, error)
	ForceCloseWithReason(reason string, ids ...string) error
	Update(id string, opts beads.UpdateOptions) error
}

// ReconcileMergedOrphans scans every agent bead and, for each whose active_mr
// points at a proven-merged MR with a still-non-terminal source issue,
// completes the refinery's interrupted post-merge reconcile by force-closing
// the source issue and clearing its leaked awaiting_refinery_merge label.
//
// Polecats with cleanup_status indicating human WIP are preserved (gc-eysed).
//
// Caller is responsible for passing a Beads client rooted at the town
// (typically beads.New(townRoot).ForAgentBead()) so agent-bead enumeration
// targets the town database; source-issue closes route to the owning rig DB
// via bd's prefix routing.
//
// The function tolerates per-bead failures: a single failed close or label
// clear is recorded as an anomaly and the scan continues. It returns a non-nil
// error only when ListAgentBeads itself fails.
func ReconcileMergedOrphans(bd OrphanReconcileBeads, dryRun bool) (*ReconcileOrphansResult, error) {
	if bd == nil {
		return nil, fmt.Errorf("reconcile orphans: nil beads client")
	}

	agents, err := bd.ListAgentBeads()
	if err != nil {
		return nil, fmt.Errorf("reconcile orphans: list agent beads: %w", err)
	}

	result := &ReconcileOrphansResult{DryRun: dryRun}
	for id, issue := range agents {
		if issue == nil {
			continue
		}
		result.Scanned++

		fields := beads.ParseAgentFields(issue.Description)
		if fields == nil || fields.ActiveMR == "" {
			continue
		}
		result.HadActiveMR++

		if isPolecatPreservingHumanWIP(fields) {
			result.PreservedWIP++
			continue
		}

		assessment := polecat.AssessActiveMR(bd, polecat.ActiveMRInput{
			ActiveMR:        fields.ActiveMR,
			SourceIssueHint: agentSourceIssueHint(fields),
		})

		// Orphan signature: the MR is stale and PROVEN-MERGED (work landed),
		// a source issue is known, but the source issue was never closed by
		// the refinery's interrupted reconcile. A source issue that is already
		// terminal makes assessment.Pending=false with SourceTerminal=true and
		// is correctly skipped here (SourceTerminal == true).
		if !assessment.Stale || !assessment.MRMerged {
			continue
		}
		if assessment.SourceIssue == "" || assessment.SourceTerminal {
			continue
		}

		entry := ReconcileOrphanEntry{
			AgentBeadID: id,
			ActiveMR:    assessment.ActiveMR,
			SourceIssue: assessment.SourceIssue,
		}

		if dryRun {
			result.Reconciled++
			result.ReconciledEntries = append(result.ReconciledEntries, entry)
			continue
		}

		closeReason := fmt.Sprintf("Merged in %s (post-merge reconcile completed by reaper — gu-7igu8)", assessment.ActiveMR)
		if err := bd.ForceCloseWithReason(closeReason, assessment.SourceIssue); err != nil {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type: "orphan_source_close_failed",
				Message: fmt.Sprintf("agent_bead=%s active_mr=%s source_issue=%s: %v",
					id, assessment.ActiveMR, assessment.SourceIssue, err),
			})
			continue
		}

		// Clear the leaked awaiting_refinery_merge label as the refinery's
		// PostMerge path would have. Best-effort: the source close already
		// completed the critical part of the reconcile, so a label-clear
		// failure is a non-fatal anomaly.
		if err := bd.Update(assessment.SourceIssue, beads.UpdateOptions{
			RemoveLabels: []string{awaitingRefineryMergeLabel},
		}); err != nil {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "orphan_label_clear_failed",
				Message: fmt.Sprintf("source_issue=%s: %v", assessment.SourceIssue, err),
			})
		}

		result.Reconciled++
		result.ReconciledEntries = append(result.ReconciledEntries, entry)
	}

	return result, nil
}

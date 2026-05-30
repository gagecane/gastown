// Active-MR scrubber for the reaper package.
//
// Background (gu-dhqm root cause): an agent bead's `active_mr` field is set
// by `gt done` and cleared in exactly one place — the refinery engineer's
// post-merge happy path. Every other lifecycle end (rebase-after-push,
// force-close, sibling-MR landing first, wisp TTL-reap) leaves the field
// dangling, where it eventually combines with `cleanup_status` drift to
// produce permanent `idle-recovery-needed` verdicts that hold scheduler
// slots until a human runs `gt polecat check-recovery --reconcile-cleanup`.
//
// This file adds a periodic scan that re-evaluates every agent bead's
// `active_mr` using the same `polecat.AssessActiveMR` classifier that the
// on-demand recovery path uses, and clears the field when the assessment
// proves the MR and source issue are both terminal.
//
// Style note: unlike the rest of the reaper package, this scrubber does
// NOT operate via direct SQL against per-database connections. Agent beads
// live exclusively in the town database (the `bd` routes layer redirects
// queries with rig prefixes back to town for `issue_type=agent`), so a
// single `bd list --label=gt:agent` covers the entire fleet. We use the
// `beads.Beads` wrapper to stay consistent with every other agent-bead
// callsite (cmd/done.go, cmd/polecat.go, refinery/engineer.go) and to
// inherit the existing prefix-routing safety in `ForAgentBead()`.
//
// Preservation invariant (gc-eysed): polecats whose self-reported
// `cleanup_status` indicates uncommitted work, a stash, or unpushed
// commits keep their `active_mr` reference even when the assessment
// returns Pending=false. The dangling ref provides the audit trail
// ("this WIP was for <mr>") that the human triage path needs. The
// scrubber leaves them alone.

package reaper

import (
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// ScrubActiveMRResult summarizes a single ScrubStaleActiveMR run.
type ScrubActiveMRResult struct {
	// Scanned is the number of agent beads inspected.
	Scanned int `json:"scanned"`
	// HadActiveMR is the number of agent beads with a non-empty active_mr field.
	HadActiveMR int `json:"had_active_mr"`
	// Cleared is the number of agent beads whose active_mr was cleared this run.
	Cleared int `json:"cleared"`
	// PreservedWIP is the number of agent beads skipped because their
	// cleanup_status indicates human-WIP that must be preserved (gc-eysed).
	PreservedWIP int `json:"preserved_wip"`
	// StillPending is the number of agent beads whose active_mr remained
	// pending after assessment (MR or source still terminal=false).
	StillPending int `json:"still_pending"`
	// DryRun is true when the scrubber reported what it would clear without
	// actually clearing anything.
	DryRun bool `json:"dry_run,omitempty"`
	// ClearedEntries records each agent bead whose active_mr was (or would be) cleared.
	ClearedEntries []ScrubActiveMREntry `json:"cleared_entries,omitempty"`
	// Anomalies records any unexpected per-bead failures (best-effort; the
	// scrubber tolerates them and continues).
	Anomalies []Anomaly `json:"anomalies,omitempty"`
}

// ScrubActiveMREntry records a single cleared (or would-be-cleared) agent bead.
type ScrubActiveMREntry struct {
	AgentBeadID string `json:"agent_bead_id"`
	ActiveMR    string `json:"active_mr"`
	MRStatus    string `json:"mr_status"`
	SourceIssue string `json:"source_issue,omitempty"`
}

// ActiveMRScrubBeads is the minimal interface ScrubStaleActiveMR needs from
// a beads client. *beads.Beads satisfies it directly.
type ActiveMRScrubBeads interface {
	polecat.IssueReader
	ListAgentBeads() (map[string]*beads.Issue, error)
	UpdateAgentActiveMR(id string, activeMR string) error
}

// ScrubStaleActiveMR scans every agent bead, runs polecat.AssessActiveMR
// over each non-empty `active_mr`, and clears the field when the assessment
// returns Pending=false. Polecats with cleanup_status indicating human WIP
// (has_stash / has_uncommitted / has_unpushed) are preserved (gc-eysed).
//
// Caller is responsible for passing a Beads client rooted at the town
// (typically from beads.New(townRoot).ForAgentBead()) so that lookups and
// updates target the town database where agent beads live.
//
// The function tolerates per-bead failures: a single failed lookup or
// update is recorded as an anomaly and the scan continues. It returns a
// non-nil error only when ListAgentBeads itself fails.
func ScrubStaleActiveMR(bd ActiveMRScrubBeads, dryRun bool) (*ScrubActiveMRResult, error) {
	if bd == nil {
		return nil, fmt.Errorf("scrub active_mr: nil beads client")
	}

	agents, err := bd.ListAgentBeads()
	if err != nil {
		return nil, fmt.Errorf("scrub active_mr: list agent beads: %w", err)
	}

	result := &ScrubActiveMRResult{DryRun: dryRun}
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
			// No git-state probe at the reaper level — the cleanup_status
			// preservation rule above already covers the WIP case, and the
			// reaper has no worktree access. Assessment falls back to
			// "MR + source both terminal" as the sole release condition.
		})
		if assessment.Pending {
			result.StillPending++
			continue
		}

		entry := ScrubActiveMREntry{
			AgentBeadID: id,
			ActiveMR:    fields.ActiveMR,
			MRStatus:    assessment.MRStatus,
			SourceIssue: assessment.SourceIssue,
		}

		if dryRun {
			result.Cleared++
			result.ClearedEntries = append(result.ClearedEntries, entry)
			continue
		}

		if err := bd.UpdateAgentActiveMR(id, ""); err != nil {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type: "active_mr_clear_failed",
				Message: fmt.Sprintf("agent_bead=%s active_mr=%s: %v",
					id, fields.ActiveMR, err),
			})
			continue
		}
		result.Cleared++
		result.ClearedEntries = append(result.ClearedEntries, entry)
	}

	return result, nil
}

// isPolecatPreservingHumanWIP reports whether an agent bead is preserving
// uncommitted work, a stash, or unpushed commits — in which case its
// active_mr must remain set as an audit trail (gc-eysed). The polecat
// self-reports this via the cleanup_status field on each tick.
//
// The on-demand recovery path expresses the same predicate in terms of
// git-state strings emitted by recoveryGitStateBlocker (has_uncommitted,
// has_stash, has_unpushed). cleanup_status uses the same vocabulary by
// design — see beads.AgentFields.CleanupStatus.
func isPolecatPreservingHumanWIP(f *beads.AgentFields) bool {
	if f == nil {
		return false
	}
	switch f.CleanupStatus {
	case "has_uncommitted", "has_stash", "has_unpushed":
		return true
	default:
		return false
	}
}

// agentSourceIssueHint mirrors cmd.agentSourceIssueHint with the
// currentIssue argument elided (the reaper has no notion of an active
// dispatch). Prefers LastSourceIssue, falling back to HookBead.
//
// Duplicated rather than shared because cmd is a CLI package and pulling
// it into the reaper would create an import cycle (cmd → reaper).
func agentSourceIssueHint(fields *beads.AgentFields) string {
	if fields == nil {
		return ""
	}
	if fields.LastSourceIssue != "" {
		return fields.LastSourceIssue
	}
	return fields.HookBead
}

// ScrubStaleActiveMRWithBackoff is a thin wrapper for callers that want to
// rate-limit retries between failed scrub cycles. It currently delegates to
// ScrubStaleActiveMR; the parameter is reserved for future use.
//
// Provided as an extension point so daemon callers can pass through their
// configured patrol interval without us reaching into runtime config.
func ScrubStaleActiveMRWithBackoff(bd ActiveMRScrubBeads, dryRun bool, _ time.Duration) (*ScrubActiveMRResult, error) {
	return ScrubStaleActiveMR(bd, dryRun)
}

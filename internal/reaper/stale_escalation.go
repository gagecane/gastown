// Stale-escalation terminal-state backstop for the reaper package (gu-tn0xp).
//
// Every escalation is created as an open `gt:escalation` bead and routed to the
// Mayor. Two existing mechanisms touch an escalation's lifecycle, but neither
// guarantees it ever reaches a terminal state:
//
//   - ReapProcessedMail / ReapProcessedWispMail close an escalation only once it
//     has been PROCESSED (carries `acked` / `read` / `delivery:acked`) and is
//     past a short audit TTL. An escalation that is never acked is never swept.
//   - `gt escalate stale` (driven by escalate_stale_dog) only BUMPS SEVERITY of
//     an unacked escalation — low→medium→high→critical — then stops at the cap.
//     It never closes the bead.
//
// So an escalation raised by an automated source whose condition self-heals
// (the source stops re-firing, the dedup signature clears, the incident passes)
// but which nobody ever acks stays status='open' forever. It walks up to
// critical, then sits there indefinitely — polluting `bd ready` / `bd list` /
// reaper open-wisp scans, with a human `gt escalate close` as the only exit.
// The load-bearing "an escalation always reaches a terminal state" guarantee
// silently does not hold.
//
// ReapStaleEscalations closes that gap as the age-based terminal backstop the
// originating bead calls for: it auto-closes open `gt:escalation` beads past a
// LONG TTL (DefaultStaleEscalationTTL, 72h by default) with a terminal
// close_reason. The TTL is deliberately far longer than both the processed
// audit window (1h) and the full re-escalation severity walk (a handful of
// stale_threshold cycles, ~half a day to reach critical): a genuinely important
// escalation has days of repeated re-escalation and surfacing before this sweep
// retires it, so this only catches escalations the system has effectively
// abandoned. It complements — never replaces — re-escalation and the processed
// sweep; the three are disjoint by time and by gate.
//
// The exclusion set mirrors the sibling mail reapers: agent beads
// (issue_type='agent'), preserve labels (gt:standing-orders / gt:keep / gt:role
// / gt:rig), and beads with a live consumer_bead_id are never swept. Like the
// other sweeps it targets status='open'/'in_progress' and never 'hooked'.
//
// As with processed mail, escalations are written to BOTH the version-controlled
// issues table and the dolt-ignored wisps table (the copy the open-wisp alert's
// CountOpenWisps actually counts). ReapStaleEscalations drains the issues copy;
// ReapStaleWispEscalations drains the wisp copy on the same gate.

package reaper

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DefaultStaleEscalationTTL is the default age before an open, never-resolved
// escalation bead is eligible for the terminal-state backstop close.
//
// Rationale: 72h is well past the processed-mail audit window (1h) and the full
// re-escalation severity walk (low→medium→high→critical over a few
// stale_threshold cycles). An escalation that is still open after three days has
// already been re-escalated to its severity cap and surfaced repeatedly; if it
// is STILL unresolved, the originating condition has almost certainly
// self-healed and the bead is stale operational residue, not a live demand for
// attention. The mol-dog-reaper formula can override via the
// stale_escalation_ttl var.
const DefaultStaleEscalationTTL = 72 * time.Hour

// staleEscalationLabel is the type label that marks a bead as an escalation
// subject to this sweep. Mail-copies of an escalation carry gt:message and
// escalation:<id> but NOT gt:escalation, so gating on this label cleanly targets
// only the escalation beads themselves (not their delivered mail-copies, which
// the mail reapers handle).
const staleEscalationLabel = "gt:escalation"

// staleEscalationCloseReason is the terminal disposition recorded on a bead
// closed by this backstop, so an operator inspecting a closed escalation can
// tell it aged out unresolved rather than being acked/closed by a human.
const staleEscalationCloseReason = "stale:auto-closed by reaper (unacked escalation past TTL)"

// staleEscalationPreserveLabels mirror the sibling mail reapers' preserve set:
// beads carrying any of these are never swept regardless of age.
var staleEscalationPreserveLabels = []string{"gt:standing-orders", "gt:keep", "gt:role", "gt:rig"}

// StaleEscalationResult holds the results of a stale-escalation reap. Remain is
// the number of open escalations still subject to the sweep (after the close),
// the operator's signal for how many escalations remain unresolved.
type StaleEscalationResult struct {
	Database      string        `json:"database"`
	Closed        int           `json:"closed"`
	Remain        int           `json:"remain"`
	DryRun        bool          `json:"dry_run,omitempty"`
	ClosedEntries []ClosedEntry `json:"closed_entries,omitempty"`
	Anomalies     []Anomaly     `json:"anomalies,omitempty"`
}

// staleEscalationArgs builds the bound-parameter slice for the stale-escalation
// queries: an optional leading cutoff, then the type label, then the preserve
// labels — matching the placeholder order the count/select queries emit.
func staleEscalationArgs(extra ...interface{}) []interface{} {
	args := make([]interface{}, 0, len(extra)+1+len(staleEscalationPreserveLabels))
	args = append(args, extra...)
	args = append(args, staleEscalationLabel)
	for _, l := range staleEscalationPreserveLabels {
		args = append(args, l)
	}
	return args
}

// staleEscalationIssuesCountQuery builds the COUNT(DISTINCT i.id) query for open
// escalations in the issues table. When withCutoff is true the query takes a
// leading `created_at < ?` placeholder (candidate count); otherwise it omits the
// age filter (total/remain count). Placeholder order: [cutoff?], type-label,
// preserve-labels.
func staleEscalationIssuesCountQuery(withCutoff bool) string {
	ageFilter := ""
	if withCutoff {
		ageFilter = "AND i.created_at < ?\n\t\t"
	}
	return fmt.Sprintf(`
		SELECT COUNT(DISTINCT i.id) FROM issues i
		INNER JOIN labels type_l ON i.id = type_l.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND i.issue_type != 'agent'
		%sAND type_l.label = ?
		AND i.id NOT IN (
			SELECT l2.issue_id FROM labels l2
			WHERE l2.label IN (%s)
		)
		AND %s`,
		ageFilter,
		sqlPlaceholders(len(staleEscalationPreserveLabels)),
		ConsumerAliveClause)
}

// staleEscalationWispCountQuery is the wisps-table counterpart of
// staleEscalationIssuesCountQuery. Placeholder order: [cutoff?], type-label,
// preserve-labels.
func staleEscalationWispCountQuery(withCutoff bool) string {
	ageFilter := ""
	if withCutoff {
		ageFilter = "AND w.created_at < ?\n\t\t"
	}
	return fmt.Sprintf(`
		SELECT COUNT(DISTINCT w.id) FROM wisps w
		INNER JOIN wisp_labels type_l ON w.id = type_l.issue_id
		WHERE w.status IN ('open', 'in_progress')
		AND w.issue_type != 'agent'
		%sAND type_l.label = ?
		AND w.id NOT IN (
			SELECT l2.issue_id FROM wisp_labels l2
			WHERE l2.label IN (%s)
		)
		AND %s`,
		ageFilter,
		sqlPlaceholders(len(staleEscalationPreserveLabels)),
		wispProcessedMailConsumerAliveClause)
}

// ScanStaleEscalations counts open escalation beads in the issues table.
// Returns the total still open and the subset past the TTL (candidates for the
// terminal-state close). Does not modify any data. Returns zero counts (no
// error) when the issues/labels tables are absent.
func ScanStaleEscalations(db *sql.DB, dbName string, ttl time.Duration) (total, candidates int, err error) {
	return scanStaleEscalationsWith(db, ttl, "stale escalations", staleEscalationIssuesCountQuery)
}

// ScanStaleWispEscalations is the wisps-table counterpart of
// ScanStaleEscalations.
func ScanStaleWispEscalations(db *sql.DB, dbName string, ttl time.Duration) (total, candidates int, err error) {
	return scanStaleEscalationsWith(db, ttl, "stale wisp escalations", staleEscalationWispCountQuery)
}

// scanStaleEscalationsWith counts open escalation beads using the supplied
// count-query builder, backing both the issues and wisps scans (they differ only
// in which builder resolves the live-consumer exclusion). Does not modify data.
func scanStaleEscalationsWith(db *sql.DB, ttl time.Duration, noun string, countQuery func(withCutoff bool) string) (total, candidates int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	if err := db.QueryRowContext(ctx, countQuery(false), staleEscalationArgs()...).Scan(&total); err != nil {
		if isTableNotFound(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("count %s total: %w", noun, err)
	}
	if total == 0 {
		return 0, 0, nil
	}

	cutoff := time.Now().UTC().Add(-ttl)
	if err := db.QueryRowContext(ctx, countQuery(true), staleEscalationArgs(cutoff)...).Scan(&candidates); err != nil {
		if isTableNotFound(err) {
			return total, 0, nil
		}
		return total, 0, fmt.Errorf("count %s candidates: %w", noun, err)
	}
	return total, candidates, nil
}

// ReapStaleEscalations closes open `gt:escalation` beads in the issues table
// that are older than the TTL, with the terminal staleEscalationCloseReason.
// This is the age-based terminal-state backstop (gu-tn0xp): it guarantees an
// escalation reaches a closed state even when it was never acked and its source
// condition self-healed.
//
// Excluded from the sweep:
//   - escalations newer than the TTL — still within the re-escalation window
//   - agent heartbeat beads (issue_type='agent')
//   - beads carrying a preserve label (gt:standing-orders, gt:keep, gt:role, gt:rig)
//   - beads with a live consumer_bead_id (ConsumerAliveClause)
//   - already-closed or hooked beads (filtered by the WHERE clause)
func ReapStaleEscalations(db *sql.DB, dbName string, ttl time.Duration, dryRun bool) (*StaleEscalationResult, error) {
	cutoff := time.Now().UTC().Add(-ttl)

	selectQuery := fmt.Sprintf(`
		SELECT DISTINCT i.id, i.title, i.created_at FROM issues i
		INNER JOIN labels type_l ON i.id = type_l.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND i.issue_type != 'agent'
		AND i.created_at < ?
		AND type_l.label = ?
		AND i.id NOT IN (
			SELECT l2.issue_id FROM labels l2
			WHERE l2.label IN (%s)
		)
		AND %s
		LIMIT %d`,
		sqlPlaceholders(len(staleEscalationPreserveLabels)),
		ConsumerAliveClause, DefaultBatchSize)

	return runStaleEscalationReap(db, dbName, dryRun, mailReapConfig{
		selectQuery:    selectQuery,
		selectArgs:     staleEscalationArgs(cutoff),
		updateQueryFmt: "UPDATE issues SET status='closed', closed_at=NOW(), close_reason='" + staleEscalationCloseReason + "' WHERE id IN (%s)",
		noun:           "stale escalations",
		commitMsgFmt:   "reaper: close %d stale escalation beads in %s",
		anomalyMsgFmt:  "dolt commit after stale-escalation reap failed: %v",
		remainQuery:    staleEscalationIssuesCountQuery(false),
		remainArgs:     staleEscalationArgs(),
	})
}

// ReapStaleWispEscalations is the wisps-table counterpart of
// ReapStaleEscalations. It closes the dolt-ignored wisps-table copies of stale
// escalation beads — the copies the open-wisp alert's CountOpenWisps counts — on
// the same gate and exclusions, resolved against the wisp tables.
func ReapStaleWispEscalations(db *sql.DB, dbName string, ttl time.Duration, dryRun bool) (*StaleEscalationResult, error) {
	cutoff := time.Now().UTC().Add(-ttl)

	selectQuery := fmt.Sprintf(`
		SELECT DISTINCT w.id, w.title, w.created_at FROM wisps w
		INNER JOIN wisp_labels type_l ON w.id = type_l.issue_id
		WHERE w.status IN ('open', 'in_progress')
		AND w.issue_type != 'agent'
		AND w.created_at < ?
		AND type_l.label = ?
		AND w.id NOT IN (
			SELECT l2.issue_id FROM wisp_labels l2
			WHERE l2.label IN (%s)
		)
		AND %s
		LIMIT %d`,
		sqlPlaceholders(len(staleEscalationPreserveLabels)),
		wispProcessedMailConsumerAliveClause, DefaultBatchSize)

	return runStaleEscalationReap(db, dbName, dryRun, mailReapConfig{
		selectQuery:    selectQuery,
		selectArgs:     staleEscalationArgs(cutoff),
		updateQueryFmt: "UPDATE wisps SET status='closed', closed_at=NOW(), close_reason='" + staleEscalationCloseReason + "' WHERE id IN (%s)",
		noun:           "stale wisp escalations",
		commitMsgFmt:   "reaper: close %d stale wisp escalation beads in %s",
		anomalyMsgFmt:  "dolt commit after stale-wisp-escalation reap failed: %v",
		remainQuery:    staleEscalationWispCountQuery(false),
		remainArgs:     staleEscalationArgs(),
	})
}

// runStaleEscalationReap is the shared wrapper for ReapStaleEscalations and
// ReapStaleWispEscalations. It runs the shared batch reap loop and assembles a
// *StaleEscalationResult, mirroring runProcessedMailReap.
func runStaleEscalationReap(db *sql.DB, dbName string, dryRun bool, cfg mailReapConfig) (*StaleEscalationResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result := &StaleEscalationResult{Database: dbName, DryRun: dryRun}

	out, err := runMailReapLoop(ctx, db, dbName, dryRun, cfg)
	if out != nil {
		result.Closed = out.Closed
		result.ClosedEntries = out.Entries
		result.Anomalies = out.Anomalies
		result.Remain = out.Remain
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

// Processed-mail lifecycle operations for the reaper package.
//
// Every escalation or mail sent to the mayor (and other roles) creates a
// permanent bead in the `issues` table, labeled `gt:message` (mail) or
// `gt:escalation` (escalation). Processing such a notification adds labels but
// never closes the bead:
//
//   - `gt mail mark-read`  → adds `read` + `delivery:acked` (+ delivery-acked-*)
//   - `gt escalate ack`    → adds `acked`
//
// So fully-processed notifications stay status='open' forever, growing the hq
// DB unboundedly and polluting `bd ready` / `bd list` / reaper scans. The
// existing ReapOpenMail sweep only touches `gt:message` on a blind TTL and
// never sees `gt:escalation`; nothing closes a notification once the human/agent
// has acted on it.
//
// ReapProcessedMail closes the gap: it auto-closes message/escalation beads
// that are PROCESSED (carry a `read`, `delivery:acked`, or `acked` label) and
// older than a short audit-window TTL. The "processed" gate is the key
// difference from ReapOpenMail — an un-acked escalation must stay open so it
// still demands attention; only acted-on notifications are swept here.
//
// See gu-ctspx for the originating bug.

package reaper

import (
	"database/sql"
	"fmt"
	"time"
)

// DefaultProcessedMailTTL is the default age before a PROCESSED (read/acked)
// message or escalation bead is eligible for auto-close by the reaper.
//
// Rationale: once a notification has been read (`read`/`delivery:acked`) or an
// escalation acknowledged (`acked`), the ack IS the resolution — the bead has
// no further coordination value. We keep only a short audit window so an
// operator can still spot recently-processed items in `bd list`, then close
// them. 1h matches the floor the hooked/open-mail TTLs honor (>=1h to avoid
// false positives) while keeping the audit window as short as the bug asks.
//
// The mol-dog-reaper formula can override via the processed_mail_ttl var.
const DefaultProcessedMailTTL = 1 * time.Hour

// processedMailTypeLabels are the labels that mark a bead as a notification
// subject to the processed-mail sweep: mail (gt:message) or escalation
// (gt:escalation).
var processedMailTypeLabels = []string{"gt:message", "gt:escalation"}

// processedMailDoneLabels are the labels that mark a notification bead as
// PROCESSED. `read` + `delivery:acked` are added by `gt mail mark-read`;
// `acked` is added by `gt escalate ack`. Any one of these means the recipient
// has acted on the notification.
var processedMailDoneLabels = []string{"read", "delivery:acked", "acked"}

// ProcessedMailResult holds the results of a processed-mail reap operation.
// Mirrors OpenMailResult but reports the remaining processed-but-open count
// (ProcessedRemain) after the sweep.
type ProcessedMailResult struct {
	Database        string        `json:"database"`
	Closed          int           `json:"closed"`
	ProcessedRemain int           `json:"processed_remain"`
	DryRun          bool          `json:"dry_run,omitempty"`
	ClosedEntries   []ClosedEntry `json:"closed_entries,omitempty"`
	Anomalies       []Anomaly     `json:"anomalies,omitempty"`
}

// ScanProcessedMail counts processed (read/acked) message+escalation beads in
// a database. Returns both the total number of such beads still open and the
// subset that have exceeded the TTL (candidates for auto-close). Does not
// modify any data.
//
// A bead counts as "processed mail" iff:
//   - it is in the `issues` table (not `wisps`)
//   - its status is `open` or `in_progress`
//   - it carries a type label (gt:message or gt:escalation)
//   - it carries a processed label (read, delivery:acked, or acked)
//
// The same exclusions as ReapProcessedMail apply (agent beads, preserve
// labels, live consumer) so the reported counts reflect beads actually subject
// to the sweep.
func ScanProcessedMail(db *sql.DB, dbName string, ttl time.Duration) (total, candidates int, err error) {
	return scanProcessedMailWith(db, ttl, "processed mail", processedMailCountQuery)
}

// processedMailCountQuery builds the COUNT(DISTINCT i.id) query for processed
// mail. When withCutoff is true the query takes a leading `created_at < ?`
// placeholder (for candidate counts); otherwise it omits the age filter (for
// the total count). Placeholder order: [cutoff?], type-labels, done-labels,
// preserve-labels.
func processedMailCountQuery(preserveLabels []string, withCutoff bool) string {
	ageFilter := ""
	if withCutoff {
		ageFilter = "AND i.created_at < ?\n\t\t"
	}
	return fmt.Sprintf(`
		SELECT COUNT(DISTINCT i.id) FROM issues i
		INNER JOIN labels type_l ON i.id = type_l.issue_id
		INNER JOIN labels done_l ON i.id = done_l.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND i.issue_type != 'agent'
		%sAND type_l.label IN (%s)
		AND done_l.label IN (%s)
		AND i.id NOT IN (
			SELECT l2.issue_id FROM labels l2
			WHERE l2.label IN (%s)
		)
		AND %s`,
		ageFilter,
		sqlPlaceholders(len(processedMailTypeLabels)),
		sqlPlaceholders(len(processedMailDoneLabels)),
		sqlPlaceholders(len(preserveLabels)),
		ConsumerAliveClause)
}

// wispProcessedMailConsumerAliveClause mirrors ConsumerAliveClause but resolves
// the consumer bead against the wisps table rather than issues. Processed
// notifications that live in wisps reference any live consumer there; the
// issues-table clause would never match a wisp id, so the wisp sweep needs its
// own clause to honor the same "skip if a live consumer is waiting" guard.
const wispProcessedMailConsumerAliveClause = `
	NOT EXISTS (
		SELECT 1 FROM wisps c
		WHERE c.id = JSON_UNQUOTE(JSON_EXTRACT(w.metadata, '$.consumer_bead_id'))
		AND c.status != 'closed'
	)`

// ScanProcessedWispMail is the wisps-table counterpart of ScanProcessedMail. It
// counts processed (read/acked) message+escalation beads that live in the
// dolt-ignored wisps table — the copies the open-wisp alert's CountOpenWisps
// actually counts. Returns the total still open and the subset past the TTL.
// Does not modify any data.
//
// The same processed gate, agent exclusion, preserve labels, and live-consumer
// exclusion as ReapProcessedWispMail apply, so the reported counts reflect
// beads actually subject to the sweep.
func ScanProcessedWispMail(db *sql.DB, dbName string, ttl time.Duration) (total, candidates int, err error) {
	return scanProcessedMailWith(db, ttl, "processed wisp mail", processedWispMailCountQuery)
}

// processedWispMailCountQuery is the wisps-table counterpart of
// processedMailCountQuery. Placeholder order: [cutoff?], type-labels,
// done-labels, preserve-labels.
func processedWispMailCountQuery(preserveLabels []string, withCutoff bool) string {
	ageFilter := ""
	if withCutoff {
		ageFilter = "AND w.created_at < ?\n\t\t"
	}
	return fmt.Sprintf(`
		SELECT COUNT(DISTINCT w.id) FROM wisps w
		INNER JOIN wisp_labels type_l ON w.id = type_l.issue_id
		INNER JOIN wisp_labels done_l ON w.id = done_l.issue_id
		WHERE w.status IN ('open', 'in_progress')
		AND w.issue_type != 'agent'
		%sAND type_l.label IN (%s)
		AND done_l.label IN (%s)
		AND w.id NOT IN (
			SELECT l2.issue_id FROM wisp_labels l2
			WHERE l2.label IN (%s)
		)
		AND %s`,
		ageFilter,
		sqlPlaceholders(len(processedMailTypeLabels)),
		sqlPlaceholders(len(processedMailDoneLabels)),
		sqlPlaceholders(len(preserveLabels)),
		wispProcessedMailConsumerAliveClause)
}

// ReapProcessedWispMail is the wisps-table counterpart of ReapProcessedMail. It
// closes PROCESSED (read/acked) message and escalation beads that live in the
// dolt-ignored wisps table, older than the TTL, with reason "processed".
//
// This completes gu-ctspx (originating bug gu-2md8k): mail/escalation
// notifications are created in BOTH the version-controlled issues table and
// the dolt-ignored wisps table. ReapProcessedMail drains only the issues
// copies, but the open-wisp alert's CountOpenWisps counts the wisps table — so
// processed notifications stranded in wisps were never swept and kept tripping
// the reaper open-wisp threshold. This function drains the wisp copies on the
// same processed gate.
//
// The exclusion set matches ReapProcessedMail (un-processed beads, agent beads,
// preserve labels, live consumer) but resolves all of them against the wisps
// tables. Like ReapProcessedMail it targets only status='open'/'in_progress'
// and never 'hooked'.
func ReapProcessedWispMail(db *sql.DB, dbName string, ttl time.Duration, dryRun bool) (*ProcessedMailResult, error) {
	cutoff := time.Now().UTC().Add(-ttl)
	preserveLabels := []string{"gt:standing-orders", "gt:keep", "gt:role", "gt:rig"}

	selectQuery := fmt.Sprintf(`
		SELECT DISTINCT w.id, w.title, w.created_at FROM wisps w
		INNER JOIN wisp_labels type_l ON w.id = type_l.issue_id
		INNER JOIN wisp_labels done_l ON w.id = done_l.issue_id
		WHERE w.status IN ('open', 'in_progress')
		AND w.issue_type != 'agent'
		AND w.created_at < ?
		AND type_l.label IN (%s)
		AND done_l.label IN (%s)
		AND w.id NOT IN (
			SELECT l2.issue_id FROM wisp_labels l2
			WHERE l2.label IN (%s)
		)
		AND %s
		LIMIT %d`,
		sqlPlaceholders(len(processedMailTypeLabels)),
		sqlPlaceholders(len(processedMailDoneLabels)),
		sqlPlaceholders(len(preserveLabels)),
		wispProcessedMailConsumerAliveClause, DefaultBatchSize)

	// The wisp tables are dolt-ignored, so DOLT_COMMIT('-Am') stages nothing
	// and hasWorkingSetChanges is normally false here — the shared loop guards
	// the commit so the no-op does not spam dolt.log (gu-leuwr).
	return runProcessedMailReap(db, dbName, dryRun, mailReapConfig{
		selectQuery:    selectQuery,
		selectArgs:     processedMailArgs(preserveLabels, cutoff),
		updateQueryFmt: "UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (%s)",
		noun:           "processed wisp mail",
		commitMsgFmt:   "reaper: close %d processed wisp mail/escalation beads in %s",
		anomalyMsgFmt:  "dolt commit after processed-wisp-mail reap failed: %v",
		// Count remaining processed wisp mail for the report, applying the same
		// exclusions as the select above.
		remainQuery: processedWispMailCountQuery(preserveLabels, false),
		remainArgs:  processedMailArgs(preserveLabels),
	})
}

// ReapProcessedMail closes PROCESSED (read/acked) message and escalation beads
// older than the TTL with reason "processed". Returns the count of closed
// beads and any remaining processed-but-open mail. Safe to call when the
// issues/labels tables are not present on the server — returns zero counts in
// that case.
//
// This addresses gu-ctspx: `gt escalate ack` and `gt mail mark-read` add the
// `acked` / `read` + `delivery:acked` labels but never close the bead, so
// fully-processed notifications accumulate forever. ReapOpenMail only sweeps
// `gt:message` on a blind TTL and never touches `gt:escalation`; this function
// closes the gap by reaping notifications the recipient has actually acted on.
//
// Excluded from the sweep:
//   - un-processed beads (no read/delivery:acked/acked label) — still demand attention
//   - agent heartbeat beads (issue_type='agent')
//   - beads carrying a preserve label (gt:standing-orders, gt:keep, gt:role, gt:rig)
//   - beads with a live consumer_bead_id (ConsumerAliveClause)
//   - already-closed, hooked, or pinned beads (filtered by the WHERE clause)
func ReapProcessedMail(db *sql.DB, dbName string, ttl time.Duration, dryRun bool) (*ProcessedMailResult, error) {
	cutoff := time.Now().UTC().Add(-ttl)
	preserveLabels := []string{"gt:standing-orders", "gt:keep", "gt:role", "gt:rig"}

	selectQuery := fmt.Sprintf(`
		SELECT DISTINCT i.id, i.title, i.created_at FROM issues i
		INNER JOIN labels type_l ON i.id = type_l.issue_id
		INNER JOIN labels done_l ON i.id = done_l.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND i.issue_type != 'agent'
		AND i.created_at < ?
		AND type_l.label IN (%s)
		AND done_l.label IN (%s)
		AND i.id NOT IN (
			SELECT l2.issue_id FROM labels l2
			WHERE l2.label IN (%s)
		)
		AND %s
		LIMIT %d`,
		sqlPlaceholders(len(processedMailTypeLabels)),
		sqlPlaceholders(len(processedMailDoneLabels)),
		sqlPlaceholders(len(preserveLabels)),
		ConsumerAliveClause, DefaultBatchSize)

	return runProcessedMailReap(db, dbName, dryRun, mailReapConfig{
		selectQuery:    selectQuery,
		selectArgs:     processedMailArgs(preserveLabels, cutoff),
		updateQueryFmt: "UPDATE issues SET status='closed', closed_at=NOW() WHERE id IN (%s)",
		noun:           "processed mail",
		commitMsgFmt:   "reaper: close %d processed mail/escalation beads in %s",
		anomalyMsgFmt:  "dolt commit after processed-mail reap failed: %v",
		// Count remaining processed mail for the report, applying the same
		// exclusions as the select above so the "remain" number reflects beads
		// actually subject to the sweep.
		remainQuery: processedMailCountQuery(preserveLabels, false),
		remainArgs:  processedMailArgs(preserveLabels),
	})
}

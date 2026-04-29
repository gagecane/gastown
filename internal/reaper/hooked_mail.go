// Hooked-mail lifecycle operations for the reaper package.
//
// HANDOFF and other mail beads are stored in the `issues` table (NOT the
// `wisps` table) because `gt hook` only scans issues. If a successor never
// consumes the hook (via `gt prime --hook`, which promotes status from
// `hooked` to `in_progress`), the bead stays `hooked` forever and accumulates
// as dead-letter. The main Reap() operation targets wisps and does not see
// these, so they need a dedicated sweep.
//
// See gu-hhqk for the broader GUPP lifecycle discussion.

package reaper

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DefaultHookedMailTTL is the default age before a hooked mail bead is
// considered dead-letter and eligible for auto-close by the reaper.
//
// Rationale: Legitimate handoff consumption happens within seconds to
// minutes of the mail being created (successor session starts, runs
// `gt prime --hook`, which promotes the mail to `in_progress`). 24h
// is two orders of magnitude above that window — anything still
// `hooked` after a day is almost certainly orphaned.
//
// The mol-dog-reaper formula can override via the hooked_mail_ttl var.
const DefaultHookedMailTTL = 24 * time.Hour

// HookedMailResult holds the results of a hooked-mail reap operation.
type HookedMailResult struct {
	Database         string        `json:"database"`
	Closed           int           `json:"closed"`
	HookedRemain     int           `json:"hooked_remain"`
	DryRun           bool          `json:"dry_run,omitempty"`
	ClosedEntries    []ClosedEntry `json:"closed_entries,omitempty"`
	Anomalies        []Anomaly     `json:"anomalies,omitempty"`
}

// ScanHookedMail counts hooked mail beads in a database. Returns both the
// total number of hooked mail beads and the subset that have exceeded the
// TTL (candidates for auto-close). Does not modify any data.
//
// A bead counts as "hooked mail" iff:
//   - it is in the `issues` table (not `wisps`)
//   - its status is `hooked`
//   - it carries the `gt:message` label
//
// Agent heartbeat beads (issue_type='agent') are intentionally long-lived
// and are excluded — they live on the hook by design (see gu-hhqk out-of-scope).
func ScanHookedMail(db *sql.DB, dbName string, ttl time.Duration) (total, candidates int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	// Count total hooked mail beads.
	totalQuery := `
		SELECT COUNT(*) FROM issues i
		INNER JOIN labels l ON i.id = l.issue_id
		WHERE i.status = 'hooked'
		AND i.issue_type != 'agent'
		AND l.label = 'gt:message'`
	if err := db.QueryRowContext(ctx, totalQuery).Scan(&total); err != nil {
		if isTableNotFound(err) {
			return 0, 0, nil // issues/labels not on this server — skip
		}
		return 0, 0, fmt.Errorf("count hooked mail total: %w", err)
	}

	if total == 0 {
		return 0, 0, nil
	}

	// Count candidates past TTL.
	cutoff := time.Now().UTC().Add(-ttl)
	candQuery := `
		SELECT COUNT(*) FROM issues i
		INNER JOIN labels l ON i.id = l.issue_id
		WHERE i.status = 'hooked'
		AND i.issue_type != 'agent'
		AND l.label = 'gt:message'
		AND i.created_at < ?`
	if err := db.QueryRowContext(ctx, candQuery, cutoff).Scan(&candidates); err != nil {
		if isTableNotFound(err) {
			return total, 0, nil
		}
		return total, 0, fmt.Errorf("count hooked mail candidates: %w", err)
	}

	return total, candidates, nil
}

// ReapHookedMail closes hooked mail beads older than the TTL with reason
// "ttl-expired". Returns the count of closed beads and any remaining hooked
// mail. Safe to call when the issues/labels tables are not present on the
// server (e.g., split-brain deployments) — returns zero counts in that case.
//
// Excluded from the sweep:
//   - agent heartbeat beads (issue_type='agent')
//   - pinned beads (status != 'hooked', filtered by the WHERE clause)
//   - standing-orders, keep, role, rig labels (long-lived mail, by convention)
func ReapHookedMail(db *sql.DB, dbName string, ttl time.Duration, dryRun bool) (*HookedMailResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cutoff := time.Now().UTC().Add(-ttl)
	result := &HookedMailResult{Database: dbName, DryRun: dryRun}

	// Preserve-labels that mark beads as long-lived despite being hooked mail.
	// If any future conventions add other "keep" labels, extend this list.
	preserveLabels := []string{"gt:standing-orders", "gt:keep", "gt:role", "gt:rig"}

	// Build the eligibility SELECT. We need the bead title and created_at for
	// the ClosedEntries log. Must NOT match beads carrying a preserve label.
	selectQuery := fmt.Sprintf(`
		SELECT DISTINCT i.id, i.title, i.created_at FROM issues i
		INNER JOIN labels mail_l ON i.id = mail_l.issue_id
		WHERE i.status = 'hooked'
		AND i.issue_type != 'agent'
		AND i.created_at < ?
		AND mail_l.label = 'gt:message'
		AND i.id NOT IN (
			SELECT l2.issue_id FROM labels l2
			WHERE l2.label IN (%s)
		)
		LIMIT %d`, sqlPlaceholders(len(preserveLabels)), DefaultBatchSize)

	args := []interface{}{cutoff}
	for _, lbl := range preserveLabels {
		args = append(args, lbl)
	}

	type candidate struct {
		id        string
		title     string
		createdAt time.Time
	}

	now := time.Now().UTC()
	totalClosed := 0

	// Batch loop: select up to N candidates, close them, repeat until empty.
	for {
		rows, err := db.QueryContext(ctx, selectQuery, args...)
		if err != nil {
			if isTableNotFound(err) {
				return result, nil // issues/labels not on this server
			}
			return nil, fmt.Errorf("select hooked mail batch: %w", err)
		}

		var batch []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.title, &c.createdAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan hooked mail row: %w", err)
			}
			batch = append(batch, c)
		}
		rows.Close()

		if len(batch) == 0 {
			break
		}

		// Record entries (same in dry-run and live).
		for _, c := range batch {
			result.ClosedEntries = append(result.ClosedEntries, ClosedEntry{
				ID:       c.id,
				Title:    c.title,
				AgeDays:  int(now.Sub(c.createdAt).Hours() / 24),
				Database: dbName,
			})
		}

		if dryRun {
			totalClosed += len(batch)
			// In dry-run, do not loop forever on the same rows.
			break
		}

		// Live: batch UPDATE issues to status='closed' with ttl-expired reason.
		placeholders := make([]string, len(batch))
		updateArgs := make([]interface{}, 0, len(batch))
		for i, c := range batch {
			placeholders[i] = "?"
			updateArgs = append(updateArgs, c.id)
		}
		inClause := strings.Join(placeholders, ",")

		updateQuery := fmt.Sprintf(
			"UPDATE issues SET status='closed', closed_at=NOW() WHERE id IN (%s)",
			inClause)

		if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
			return result, fmt.Errorf("disable autocommit: %w", err)
		}

		sqlResult, err := db.ExecContext(ctx, updateQuery, updateArgs...)
		if err != nil {
			_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
			return result, fmt.Errorf("close hooked mail batch: %w", err)
		}
		affected, _ := sqlResult.RowsAffected()
		totalClosed += int(affected)

		// Commit the SQL transaction so DOLT_COMMIT sees the working-set diff.
		if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
			_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
			return result, fmt.Errorf("sql commit: %w", err)
		}
		commitMsg := fmt.Sprintf("reaper: ttl-expired close %d hooked mail in %s", int(affected), dbName)
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe internal values
			if !isNothingToCommit(err) {
				result.Anomalies = append(result.Anomalies, Anomaly{
					Type:    "dolt_commit_failed",
					Message: fmt.Sprintf("dolt commit after hooked-mail reap failed: %v", err),
				})
			}
		}
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")

		// If the batch was smaller than the page size, we're done.
		if len(batch) < DefaultBatchSize {
			break
		}
	}

	result.Closed = totalClosed

	// Count remaining hooked mail for the report.
	remainQuery := `
		SELECT COUNT(*) FROM issues i
		INNER JOIN labels l ON i.id = l.issue_id
		WHERE i.status = 'hooked'
		AND i.issue_type != 'agent'
		AND l.label = 'gt:message'`
	if err := db.QueryRowContext(ctx, remainQuery).Scan(&result.HookedRemain); err != nil {
		if !isTableNotFound(err) {
			return result, fmt.Errorf("count hooked mail remain: %w", err)
		}
	}

	return result, nil
}

// DefaultDeadLetterThreshold is the age at which a hooked mail bead is
// considered "dead-letter" for observability purposes. Mirrors the doctor
// check threshold from gu-hhqk AC#4. Reported via the gastown.hooked_beads.*
// gauges so operators see backlog accumulation before it bites.
//
// Note: this is separate from DefaultHookedMailTTL (24h). The dead-letter
// threshold surfaces backlog early; the TTL governs when the reaper actually
// closes beads.
const DefaultDeadLetterThreshold = 30 * time.Minute

// HookedMailCounts is a read-only snapshot of hooked mail counts for one
// database. Produced by ScanHookedMailCounts and consumed by the metrics
// gauge callback.
type HookedMailCounts struct {
	Database   string // rig / database name
	Total      int    // all hooked mail beads (excluding preserve-labels and agents)
	DeadLetter int    // subset older than the dead-letter threshold
}

// ScanHookedMailCounts returns the hooked-mail counts for a database using
// the same exclusion set as ReapHookedMail and the hooked-dead-letter doctor
// check: excludes issue_type='agent' and any bead labeled
// gt:standing-orders, gt:keep, gt:role, or gt:rig. Does not modify any data.
//
// "Dead-letter" is the subset older than deadLetterThreshold (typically 30
// minutes). Use DefaultDeadLetterThreshold unless overriding for tests.
//
// Returns zero counts (no error) when the issues/labels tables are absent,
// matching ScanHookedMail's split-brain tolerance.
func ScanHookedMailCounts(db *sql.DB, dbName string, deadLetterThreshold time.Duration) (HookedMailCounts, error) {
	result := HookedMailCounts{Database: dbName}

	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	// The exclusion set matches reaper.ReapHookedMail and
	// doctor.HookedDeadLetterCheck to keep all three semantics aligned.
	preserveLabels := []string{"gt:standing-orders", "gt:keep", "gt:role", "gt:rig"}
	preserveArgs := make([]interface{}, len(preserveLabels))
	for i, lbl := range preserveLabels {
		preserveArgs[i] = lbl
	}

	totalQuery := fmt.Sprintf(`
		SELECT COUNT(DISTINCT i.id) FROM issues i
		INNER JOIN labels mail_l ON i.id = mail_l.issue_id
		WHERE i.status = 'hooked'
		AND i.issue_type != 'agent'
		AND mail_l.label = 'gt:message'
		AND i.id NOT IN (
			SELECT l2.issue_id FROM labels l2
			WHERE l2.label IN (%s)
		)`, sqlPlaceholders(len(preserveLabels)))
	if err := db.QueryRowContext(ctx, totalQuery, preserveArgs...).Scan(&result.Total); err != nil {
		if isTableNotFound(err) {
			return result, nil
		}
		return result, fmt.Errorf("count hooked mail total: %w", err)
	}

	if result.Total == 0 {
		return result, nil
	}

	cutoff := time.Now().UTC().Add(-deadLetterThreshold)
	dlArgs := append([]interface{}{cutoff}, preserveArgs...)
	dlQuery := fmt.Sprintf(`
		SELECT COUNT(DISTINCT i.id) FROM issues i
		INNER JOIN labels mail_l ON i.id = mail_l.issue_id
		WHERE i.status = 'hooked'
		AND i.issue_type != 'agent'
		AND mail_l.label = 'gt:message'
		AND i.created_at < ?
		AND i.id NOT IN (
			SELECT l2.issue_id FROM labels l2
			WHERE l2.label IN (%s)
		)`, sqlPlaceholders(len(preserveLabels)))
	if err := db.QueryRowContext(ctx, dlQuery, dlArgs...).Scan(&result.DeadLetter); err != nil {
		if isTableNotFound(err) {
			return result, nil
		}
		return result, fmt.Errorf("count hooked mail dead-letter: %w", err)
	}

	return result, nil
}

// sqlPlaceholders returns a comma-separated list of n "?" placeholders for
// use in an IN (...) clause. Returns "NULL" if n == 0 so the query still
// parses (empty IN lists are not valid SQL).
func sqlPlaceholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

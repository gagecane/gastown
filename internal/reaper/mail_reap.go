// Shared helpers for the mail-family reapers (hooked, open, processed, and
// processed-wisp). These collapse the batch-close loop and the processed-mail
// scan that were near-identical copies across hooked_mail.go and
// processed_mail.go (gu-nid89.12.4).
//
// The SQL itself stays inline in each Reap*/Scan* function: the table, label,
// and status predicates are what distinguish the reapers, and keeping them at
// the call site preserves readability and the source-inspection guards in the
// _test.go files. Only the table-agnostic mechanics live here.

package reaper

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// mailReapCandidate is one bead eligible for a mail-reap close.
type mailReapCandidate struct {
	id        string
	title     string
	createdAt time.Time
}

// mailReapConfig parameterizes the shared batch select→record→update→commit
// loop. Each caller builds its own SQL so the table/label/status predicates
// stay at the call site; the loop only needs the finished queries plus the
// nouns used in log/commit messages.
type mailReapConfig struct {
	// selectQuery selects up to DefaultBatchSize eligible (id, title,
	// created_at) rows; selectArgs are its bound parameters.
	selectQuery string
	selectArgs  []interface{}
	// updateQueryFmt closes a batch; it must contain a single %s for the
	// "?,?,..." IN clause (e.g. "UPDATE issues SET status='closed' ... (%s)").
	updateQueryFmt string
	// noun names the unit in error messages, e.g. "hooked mail".
	noun string
	// commitMsgFmt builds the DOLT_COMMIT message; it takes (affected int,
	// dbName string).
	commitMsgFmt string
	// anomalyMsgFmt builds a dolt_commit_failed anomaly message; it takes the
	// underlying error (%v).
	anomalyMsgFmt string
	// remainQuery counts the beads still subject to the sweep after the loop,
	// applying the same exclusions as selectQuery; remainArgs are its bound
	// parameters. When remainQuery is empty the count is skipped.
	remainQuery string
	remainArgs  []interface{}
}

// mailReapOutcome carries the shared results of a batch reap loop.
type mailReapOutcome struct {
	Closed       int
	Entries      []ClosedEntry
	Anomalies    []Anomaly
	Remain       int
	TableMissing bool
}

// runMailReapLoop runs the batch select→record→update→commit loop shared by the
// mail reapers. Its behavior mirrors the previously-duplicated inline loops:
//
//   - On a read error (select/scan/rows-close) it returns (nil, err); the
//     caller should propagate without a partial result.
//   - When the target tables are absent it returns an outcome with
//     TableMissing=true and a nil error.
//   - On a write error (autocommit/update/commit) it returns the partial
//     outcome plus the error so the caller can surface what was recorded.
//   - On success it returns the full outcome and nil.
func runMailReapLoop(ctx context.Context, db *sql.DB, dbName string, dryRun bool, cfg mailReapConfig) (*mailReapOutcome, error) {
	now := time.Now().UTC()
	out := &mailReapOutcome{}

	// Batch loop: select up to N candidates, close them, repeat until empty.
	for {
		rows, err := db.QueryContext(ctx, cfg.selectQuery, cfg.selectArgs...)
		if err != nil {
			if isTableNotFound(err) {
				out.TableMissing = true
				return out, nil // tables not on this server — skip
			}
			return nil, fmt.Errorf("select %s batch: %w", cfg.noun, err)
		}

		var batch []mailReapCandidate
		for rows.Next() {
			var c mailReapCandidate
			if err := rows.Scan(&c.id, &c.title, &c.createdAt); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan %s row: %w", cfg.noun, err)
			}
			batch = append(batch, c)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close %s rows: %w", cfg.noun, err)
		}

		if len(batch) == 0 {
			break
		}

		// Record entries (same in dry-run and live).
		for _, c := range batch {
			out.Entries = append(out.Entries, ClosedEntry{
				ID:       c.id,
				Title:    c.title,
				AgeDays:  int(now.Sub(c.createdAt).Hours() / 24),
				Database: dbName,
			})
		}

		if dryRun {
			out.Closed += len(batch)
			// In dry-run, do not loop forever on the same rows.
			break
		}

		// Live: batch UPDATE the selected ids to status='closed'.
		placeholders := make([]string, len(batch))
		updateArgs := make([]interface{}, 0, len(batch))
		for i, c := range batch {
			placeholders[i] = "?"
			updateArgs = append(updateArgs, c.id)
		}
		updateQuery := fmt.Sprintf(cfg.updateQueryFmt, strings.Join(placeholders, ","))

		if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
			return out, fmt.Errorf("disable autocommit: %w", err)
		}

		sqlResult, err := db.ExecContext(ctx, updateQuery, updateArgs...)
		if err != nil {
			_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
			return out, fmt.Errorf("close %s batch: %w", cfg.noun, err)
		}
		affected, _ := sqlResult.RowsAffected()
		out.Closed += int(affected)

		// Commit the SQL transaction so DOLT_COMMIT sees the working-set diff.
		if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
			_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
			return out, fmt.Errorf("sql commit: %w", err)
		}
		commitMsg := fmt.Sprintf(cfg.commitMsgFmt, int(affected), dbName)
		// Skip the commit when nothing landed in the working set (e.g. the only
		// mutated tables are dolt-ignored), avoiding a server-side "nothing to
		// commit" warning in dolt.log (gu-leuwr).
		if hasWorkingSetChanges(ctx, db) {
			if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe internal values
				if !isNothingToCommit(err) {
					out.Anomalies = append(out.Anomalies, Anomaly{
						Type:    "dolt_commit_failed",
						Message: fmt.Sprintf(cfg.anomalyMsgFmt, err),
					})
				}
			}
		}
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")

		// If the batch was smaller than the page size, we're done.
		if len(batch) < DefaultBatchSize {
			break
		}
	}

	// Count the beads still subject to the sweep for the report.
	if cfg.remainQuery != "" {
		if err := db.QueryRowContext(ctx, cfg.remainQuery, cfg.remainArgs...).Scan(&out.Remain); err != nil {
			if !isTableNotFound(err) {
				return out, fmt.Errorf("count %s remain: %w", cfg.noun, err)
			}
		}
	}

	return out, nil
}

// issuesMailSelectQuery builds the batch-eligibility SELECT shared by
// ReapHookedMail and ReapOpenMail. The two reapers differ only in their status
// predicate (statusClause, e.g. "i.status = 'hooked'"); everything else — the
// gt:message join, agent exclusion, age filter, and preserve-label NOT IN — is
// identical. The caller passes the live-consumer guard (consumerClause) so the
// exclusion stays visible at each call site. Placeholder order matches the
// caller's args: [cutoff, preserve-labels...].
func issuesMailSelectQuery(statusClause string, preserveCount int, consumerClause string) string {
	return fmt.Sprintf(`
		SELECT DISTINCT i.id, i.title, i.created_at FROM issues i
		INNER JOIN labels mail_l ON i.id = mail_l.issue_id
		WHERE %s
		AND i.issue_type != 'agent'
		AND i.created_at < ?
		AND mail_l.label = 'gt:message'
		AND i.id NOT IN (
			SELECT l2.issue_id FROM labels l2
			WHERE l2.label IN (%s)
		)
		AND %s
		LIMIT %d`, statusClause, sqlPlaceholders(preserveCount), consumerClause, DefaultBatchSize)
}

// issuesMailRemainQuery builds the "remaining" COUNT(*) for the same reapers,
// applying the identical exclusions as issuesMailSelectQuery. Placeholder
// order: [preserve-labels...].
func issuesMailRemainQuery(statusClause string, preserveCount int, consumerClause string) string {
	return fmt.Sprintf(`
		SELECT COUNT(*) FROM issues i
		INNER JOIN labels l ON i.id = l.issue_id
		WHERE %s
		AND i.issue_type != 'agent'
		AND l.label = 'gt:message'
		AND i.id NOT IN (
			SELECT l2.issue_id FROM labels l2
			WHERE l2.label IN (%s)
		)
		AND %s`, statusClause, sqlPlaceholders(preserveCount), consumerClause)
}

// processedMailArgs builds the bound-parameter slice shared by the processed
// mail/escalation queries: an optional leading cutoff, then the type-labels,
// done-labels, and preserve-labels in that order (matching the placeholder
// order the count/select queries emit).
func processedMailArgs(preserveLabels []string, extra ...interface{}) []interface{} {
	args := make([]interface{}, 0, len(extra)+len(processedMailTypeLabels)+len(processedMailDoneLabels)+len(preserveLabels))
	args = append(args, extra...)
	for _, l := range processedMailTypeLabels {
		args = append(args, l)
	}
	for _, l := range processedMailDoneLabels {
		args = append(args, l)
	}
	for _, l := range preserveLabels {
		args = append(args, l)
	}
	return args
}

// runProcessedMailReap is the shared wrapper for ReapProcessedMail and
// ReapProcessedWispMail. Both return a *ProcessedMailResult and differ only in
// the table-specific SQL their callers build into cfg; this collapses the
// identical context setup, result assembly, and table-missing handling. The
// caller builds cfg (including the inline select/remain SQL the
// source-inspection tests pin to each function body).
func runProcessedMailReap(db *sql.DB, dbName string, dryRun bool, cfg mailReapConfig) (*ProcessedMailResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result := &ProcessedMailResult{Database: dbName, DryRun: dryRun}

	out, err := runMailReapLoop(ctx, db, dbName, dryRun, cfg)
	if out != nil {
		result.Closed = out.Closed
		result.ClosedEntries = out.Entries
		result.Anomalies = out.Anomalies
		result.ProcessedRemain = out.Remain
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

// scanProcessedMailWith counts processed (read/acked) message+escalation beads
// using the supplied count-query builder. It backs both ScanProcessedMail
// (issues tables) and ScanProcessedWispMail (wisps tables); the only difference
// between those is which builder resolves the live-consumer exclusion, so the
// argument-binding and total/candidate two-step are shared here. noun is used
// only in error messages. Does not modify any data.
func scanProcessedMailWith(db *sql.DB, ttl time.Duration, noun string, countQuery func(preserveLabels []string, withCutoff bool) string) (total, candidates int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	preserveLabels := []string{"gt:standing-orders", "gt:keep", "gt:role", "gt:rig"}

	totalQuery := countQuery(preserveLabels, false)
	if err := db.QueryRowContext(ctx, totalQuery, processedMailArgs(preserveLabels)...).Scan(&total); err != nil {
		if isTableNotFound(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("count %s total: %w", noun, err)
	}

	if total == 0 {
		return 0, 0, nil
	}

	cutoff := time.Now().UTC().Add(-ttl)
	candQuery := countQuery(preserveLabels, true)
	if err := db.QueryRowContext(ctx, candQuery, processedMailArgs(preserveLabels, cutoff)...).Scan(&candidates); err != nil {
		if isTableNotFound(err) {
			return total, 0, nil
		}
		return total, 0, fmt.Errorf("count %s candidates: %w", noun, err)
	}

	return total, candidates, nil
}

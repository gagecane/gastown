// Package reaper provides wisp and issue cleanup operations for Dolt databases.
//
// These functions are the "callable helper functions" for the Dog-driven
// mol-dog-reaper formula. They execute SQL operations but do not make
// eligibility decisions — the Dog (or daemon orchestrator) decides what
// to reap, purge, and auto-close based on the formula.
package reaper

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// validDBName matches safe database names (alphanumeric, underscore, hyphen).
var validDBName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// DefaultDatabases is the static fallback list of known production databases.
// Used only when SHOW DATABASES fails (server unreachable).
// GH#2385: Removed legacy "gt" and "bd" names — modern towns use "hq" (town
// beads) and rig-specific names. Those databases no longer exist in most
// installations and their presence in the fallback caused phantom DB errors.
var DefaultDatabases = []string{"hq"}

// testPollutionPrefixes are database name prefixes created by tests.
var testPollutionPrefixes = []string{"testdb_", "beads_t", "beads_pt", "doctest_"}

// isNothingToCommit returns true if the error is a Dolt "nothing to commit" error.
func isNothingToCommit(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "nothing to commit")
}

// hasWorkingSetChanges returns true if the current database has any working-set
// changes (staged or unstaged) reported by dolt_status. It guards DOLT_COMMIT('-Am')
// calls so the no-op commit is skipped entirely when the only mutated tables are
// dolt-ignored (wisps, wisp_%, local_metadata, repo_mtimes). Mutating only ignored
// tables leaves dolt_status empty, so the subsequent '-Am' commit would otherwise
// emit a server-side "nothing to commit" warning to dolt.log at high frequency
// (gu-leuwr; mirrors the staged-changes guard added for daemon pushes in gt-zb8).
//
// Uses COUNT(*) (not WHERE staged=1) because '-Am' auto-stages: any working-set
// row means there is something to commit. Fails open (returns true) on query error
// so a commit is still attempted and the error surfaces through the normal path.
func hasWorkingSetChanges(ctx context.Context, db *sql.DB) bool {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&count); err != nil {
		return true
	}
	return count > 0
}

// isTableNotFound returns true if the error indicates a missing table.
// This happens when beads stores its data on a separate Dolt instance from
// the gt Dolt server, so tables like issues/labels/dependencies don't exist
// on the server the reaper connects to.
func isTableNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "table not found") || strings.Contains(msg, "doesn't exist")
}

// DiscoverDatabases queries SHOW DATABASES on the Dolt server and returns
// all production databases, filtering out system databases and test pollution.
// Falls back to DefaultDatabases on any error.
func DiscoverDatabases(host string, port int) []string {
	dsn := fmt.Sprintf("root@tcp(%s:%d)/?parseTime=true&timeout=5s", host, port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return DefaultDatabases
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return DefaultDatabases
	}
	defer func() { _ = rows.Close() }()

	var databases []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if name == "information_schema" || name == "mysql" {
			continue
		}
		lower := strings.ToLower(name)
		skip := false
		for _, prefix := range testPollutionPrefixes {
			if strings.HasPrefix(lower, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		databases = append(databases, name)
	}

	if len(databases) == 0 {
		return DefaultDatabases
	}
	return databases
}

// ScanResult holds the results of scanning a database for reaper candidates.
type ScanResult struct {
	Database        string    `json:"database"`
	ReapCandidates  int       `json:"reap_candidates"`
	PurgeCandidates int       `json:"purge_candidates"`
	MailCandidates  int       `json:"mail_candidates"`
	StaleCandidates int       `json:"stale_candidates"`
	OpenWisps       int       `json:"open_wisps"`
	Anomalies       []Anomaly `json:"anomalies,omitempty"`
}

// ReapResult holds the results of a reap operation.
type ReapResult struct {
	Database   string    `json:"database"`
	Reaped     int       `json:"reaped"`
	OpenRemain int       `json:"open_remain"`
	DryRun     bool      `json:"dry_run,omitempty"`
	Anomalies  []Anomaly `json:"anomalies,omitempty"`
}

// PurgeResult holds the results of a purge operation.
type PurgeResult struct {
	Database    string    `json:"database"`
	WispsPurged int       `json:"wisps_purged"`
	MailPurged  int       `json:"mail_purged"`
	DryRun      bool      `json:"dry_run,omitempty"`
	Anomalies   []Anomaly `json:"anomalies,omitempty"`
}

// ClosedEntry records an individual issue closure with details for logging.
type ClosedEntry struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	AgeDays  int    `json:"age_days"`
	Database string `json:"database"`
}

// AutoCloseResult holds the results of an auto-close operation.
type AutoCloseResult struct {
	Database      string        `json:"database"`
	Closed        int           `json:"closed"`
	ClosedEntries []ClosedEntry `json:"closed_entries,omitempty"`
	DryRun        bool          `json:"dry_run,omitempty"`
	Anomalies     []Anomaly     `json:"anomalies,omitempty"`
}

// Anomaly represents an unexpected condition found during reaper operations.
type Anomaly struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

const (
	// DefaultQueryTimeout is the timeout for individual reaper SQL queries.
	DefaultQueryTimeout = 30 * time.Second
	// DefaultBatchSize is the number of rows per batch DELETE operation.
	DefaultBatchSize = 100
	// DefaultAlertThreshold is the open-wisp count above which callers should
	// surface a warning. Sized above the natural steady-state for the current
	// dog/deacon emit rate (~23 wisps/h × 24h TTL ≈ 550). See hq-57jr8.
	DefaultAlertThreshold = 800
	// EphemeralPurgeAge is the retention for closed *ephemeral* wisps (patrol
	// molecules, plugin-run receipts, sling-context). They carry no audit value
	// once closed, so they are purged far sooner than the standard purge-age for
	// regular wisps. Letting them linger inflates the unindexed bd-show resolver
	// scan over the wisps table — a near-continuous query whose cost is O(rows) —
	// driving residual Dolt CPU after wisp-heavy bursts (gs-7pk).
	EphemeralPurgeAge = 1 * time.Hour
)

// closedWispPurgeWhere selects closed wisps eligible for purge. Its two
// placeholders, in order, are: (1) the standard purge cutoff applied to every
// closed wisp, and (2) the shorter EphemeralPurgeAge cutoff applied only to
// ephemeral wisps. Shared by Scan (candidate count) and purgeClosedWisps
// (digest + delete) so all three stay in lockstep.
const closedWispPurgeWhere = "w.status = 'closed' AND (w.closed_at < ? OR (w.ephemeral = 1 AND w.closed_at < ?))"

// ValidateDBName returns an error if the database name is unsafe.
func ValidateDBName(dbName string) error {
	if !validDBName.MatchString(dbName) {
		return fmt.Errorf("invalid database name: %q", dbName)
	}
	return nil
}

// OpenDB opens a connection to the Dolt server for a given database.
func OpenDB(host string, port int, dbName string, readTimeout, writeTimeout time.Duration) (*sql.DB, error) {
	if err := ValidateDBName(dbName); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=%s&writeTimeout=%s",
		host, port, dbName,
		fmt.Sprintf("%ds", int(readTimeout.Seconds())),
		fmt.Sprintf("%ds", int(writeTimeout.Seconds())))
	return sql.Open("mysql", dsn)
}

// parentExcludeJoin returns a LEFT JOIN clause and WHERE condition that restricts
// results to wisps whose parent molecule is closed, missing, or nonexistent.
//
// This replaces the previous parentCheckWhere() which used 3 correlated EXISTS
// subqueries per row, causing O(n*m) query cost on large wisp tables (gt-jd1z).
// The LEFT JOIN approach runs the subquery once and hash-joins: O(n+m).
//
// Semantics (unchanged from parentCheckWhere):
//   - No parent-child dependency → eligible (orphan wisps)
//   - Parent status is 'closed' → eligible (parent already reaped)
//   - Parent row missing (dangling ref) → eligible (parent already purged)
//
// The inverse is simpler: exclude wisps that have an OPEN parent.
//
// Usage:
//
//	join, where := parentExcludeJoin(dbName)
//	query := fmt.Sprintf("SELECT ... FROM wisps w %s WHERE ... AND %s", dbName, join, where)
func parentExcludeJoin(dbName string) (joinClause, whereCondition string) {
	joinClause = `LEFT JOIN (
		SELECT DISTINCT wd.issue_id
		FROM wisp_dependencies wd
		LEFT JOIN wisps pw ON pw.id = wd.depends_on_issue_id LEFT JOIN issues pi ON pi.id = wd.depends_on_issue_id
		WHERE wd.type = 'parent-child'
		AND (pw.status IN ('open', 'hooked', 'in_progress') OR pi.status IN ('open', 'in_progress'))
	) open_parent ON open_parent.issue_id = w.id`
	whereCondition = "open_parent.issue_id IS NULL"
	return
}

// LabelSlingContext is the label the scheduler attaches to sling-context wisps.
// Duplicated here (rather than imported from internal/scheduler/capacity) to
// keep the reaper package free of a scheduler dependency. Must stay in sync
// with capacity.LabelSlingContext.
const LabelSlingContext = "gt:sling-context"

// liveTrackedContextExcludeJoin returns a LEFT JOIN clause and WHERE condition
// that excludes sling-context wisps whose tracked work bead is still open.
//
// A sling-context wisp is created with `dep add <context> <workbead>
// --type=tracks`, i.e. the context's wisp_dependencies row has issue_id=context
// and depends_on_issue_id=workbead. The work bead lives in the issues table. If the
// reaper closes/purges the context by age while its work bead is still open,
// the scheduler can dispatch against a now-missing context and CloseSlingContext
// fails with "issue not found" — a double-dispatch risk (gu-i0oaq / gu-ycihb).
//
// This mirrors parentExcludeJoin but for type='tracks' edges, and is scoped to
// wisps carrying the gt:sling-context label so unrelated 'tracks' edges (e.g.
// convoys) are not protected. Semantics: exclude a wisp from reaping when it is
// a sling-context AND its tracked work bead is open/hooked/in_progress.
func liveTrackedContextExcludeJoin(dbName string) (joinClause, whereCondition string) {
	joinClause = `LEFT JOIN (
		SELECT DISTINCT wd.issue_id
		FROM wisp_dependencies wd
		INNER JOIN wisp_labels wl ON wl.issue_id = wd.issue_id AND wl.label = '` + LabelSlingContext + `'
		LEFT JOIN wisps tw ON tw.id = wd.depends_on_issue_id LEFT JOIN issues ti ON ti.id = wd.depends_on_issue_id
		WHERE wd.type = 'tracks'
		AND (tw.status IN ('open', 'hooked', 'in_progress') OR ti.status IN ('open', 'in_progress'))
	) live_tracked ON live_tracked.issue_id = w.id`
	whereCondition = "live_tracked.issue_id IS NULL"
	return
}

// HasReaperSchema checks whether the database has the tables required for reaper
// operations (wisps and issues). Returns false (no error) when tables are missing
// — callers use this to skip databases that have incomplete beads schema (e.g.
// partially initialized databases on the central Dolt server).
func HasReaperSchema(db *sql.DB) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name IN ('wisps', 'issues') AND table_schema = DATABASE()").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check reaper schema: %w", err)
	}
	return count >= 2, nil
}

// Scan counts reaper candidates in a database without modifying anything.
func Scan(db *sql.DB, dbName string, maxAge, purgeAge, mailDeleteAge, staleIssueAge time.Duration) (*ScanResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	result := &ScanResult{Database: dbName}
	now := time.Now().UTC()
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	trackedJoin, trackedWhere := liveTrackedContextExcludeJoin(dbName)

	// Count reap candidates: open wisps past max_age with eligible parent status.
	// Must match Reap() eligibility semantics exactly, including the exclusion of
	// agent beads and live-tracked sling-contexts (gu-ycihb), otherwise scan can
	// report candidates that reap will never close.
	// Uses LEFT JOIN anti-pattern instead of correlated EXISTS to avoid O(n*m) cost (gt-jd1z).
	reapQuery := fmt.Sprintf(
		"SELECT COUNT(*) FROM wisps w %s %s WHERE w.status IN ('open', 'hooked', 'in_progress') AND w.created_at < ? AND w.issue_type != 'agent' AND %s AND %s",
		parentJoin, trackedJoin, parentWhere, trackedWhere)
	if err := db.QueryRowContext(ctx, reapQuery, now.Add(-maxAge)).Scan(&result.ReapCandidates); err != nil {
		return nil, fmt.Errorf("count reap candidates: %w", err)
	}

	// Count purge candidates: closed wisps past purge_age (or past the shorter
	// EphemeralPurgeAge horizon if ephemeral).
	// No parent check needed — closed wisps past the delete age are unconditionally purgeable.
	// The parent check (correlated subqueries on wisp_dependencies) was causing O(n*m) query
	// cost with 1800+ closed wisps, leading to CPU spikes and connection timeouts (gt-wvd2).
	// The live-tracked guard, however, is retained: a closed sling-context wisp must not be
	// purged while its tracked work bead is still open/in-flight, or the scheduler loses
	// track of slung work (bead reverts to plain open-ready) and may double-dispatch the
	// still-running polecat (gu-25jx5). It uses the same O(n+m) LEFT JOIN anti-pattern as
	// the reap-path guard, so it does not reintroduce the gt-wvd2 O(n*m) cost. Must stay in
	// lockstep with purgeClosedWisps so scan never reports candidates the purge won't delete.
	purgeQuery := fmt.Sprintf(
		"SELECT COUNT(*) FROM wisps w %s WHERE %s AND %s", trackedJoin, closedWispPurgeWhere, trackedWhere)
	if err := db.QueryRowContext(ctx, purgeQuery, now.Add(-purgeAge), now.Add(-EphemeralPurgeAge)).Scan(&result.PurgeCandidates); err != nil {
		return nil, fmt.Errorf("count purge candidates: %w", err)
	}

	// Count mail candidates.
	// The issues/labels tables may not exist on the gt Dolt server if beads
	// stores its data on a separate Dolt instance. Skip gracefully.
	mailQuery := "SELECT COUNT(*) FROM issues WHERE status = 'closed' AND closed_at < ? AND id IN (SELECT issue_id FROM labels WHERE label = 'gt:message')"
	if err := db.QueryRowContext(ctx, mailQuery, now.Add(-mailDeleteAge)).Scan(&result.MailCandidates); err != nil {
		if !isTableNotFound(err) {
			return nil, fmt.Errorf("count mail candidates: %w", err)
		}
		// issues/labels table not on this server — skip mail count
	}

	// Count stale issue candidates.
	// Same caveat: issues/dependencies tables may live on a separate Dolt instance.
	staleQuery := `
		SELECT COUNT(*) FROM issues i
		WHERE i.status IN ('open', 'in_progress')
		AND i.updated_at < ?
		AND i.priority > 1
		AND i.issue_type != 'epic'
		AND i.id NOT IN (
			SELECT DISTINCT d.issue_id FROM dependencies d
			INNER JOIN issues dep ON d.depends_on_issue_id = dep.id
			WHERE dep.status IN ('open', 'in_progress')
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.depends_on_issue_id FROM dependencies d
			INNER JOIN issues blocker ON d.issue_id = blocker.id
			WHERE blocker.status IN ('open', 'in_progress')
		)`
	if err := db.QueryRowContext(ctx, staleQuery, now.Add(-staleIssueAge)).Scan(&result.StaleCandidates); err != nil {
		if !isTableNotFound(err) {
			return nil, fmt.Errorf("count stale candidates: %w", err)
		}
		// issues/dependencies table not on this server — skip stale count
	}

	// Total open wisps.
	openQuery := "SELECT COUNT(*) FROM wisps WHERE status IN ('open', 'hooked', 'in_progress')"
	if err := db.QueryRowContext(ctx, openQuery).Scan(&result.OpenWisps); err != nil {
		return nil, fmt.Errorf("count open wisps: %w", err)
	}

	// Anomaly detection: dangling parent references.
	danglingQuery := `
		SELECT COUNT(*) FROM wisp_dependencies wd
		LEFT JOIN wisps pw ON pw.id = wd.depends_on_issue_id LEFT JOIN issues pi ON pi.id = wd.depends_on_issue_id
		WHERE wd.type = 'parent-child' AND pw.id IS NULL AND pi.id IS NULL`
	var danglingCount int
	if err := db.QueryRowContext(ctx, danglingQuery).Scan(&danglingCount); err == nil && danglingCount > 0 {
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "dangling_parent_ref",
			Message: fmt.Sprintf("%d wisp(s) have parent dependency records pointing to purged/missing parents", danglingCount),
			Count:   danglingCount,
		})
	}

	return result, nil
}

// Reap closes stale wisps in a database whose parent molecule is already closed.
// UPDATEs are batched to avoid holding a write lock for extended periods on large tables.
func Reap(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ReapResult, error) {
	// Use a longer timeout to accommodate batched processing across large tables.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	trackedJoin, trackedWhere := liveTrackedContextExcludeJoin(dbName)
	// Exclude agent beads (issue_type='agent') from reaping — they have persistent
	// identity and should not be closed by the wisp reaper regardless of age.
	// The trackedWhere clause additionally spares sling-context wisps whose
	// tracked work bead is still open (gu-ycihb) to avoid the dispatch-close
	// double-dispatch race (gu-i0oaq).
	whereClause := fmt.Sprintf(
		"w.status IN ('open', 'hooked', 'in_progress') AND w.created_at < ? AND w.issue_type != 'agent' AND %s AND %s", parentWhere, trackedWhere)

	result := &ReapResult{Database: dbName, DryRun: dryRun}

	if dryRun {
		countQuery := fmt.Sprintf("SELECT COUNT(*) FROM wisps w %s %s WHERE %s", parentJoin, trackedJoin, whereClause)
		if err := db.QueryRowContext(ctx, countQuery, cutoff).Scan(&result.Reaped); err != nil {
			return nil, fmt.Errorf("dry-run count: %w", err)
		}
		openQuery := "SELECT COUNT(*) FROM wisps WHERE status IN ('open', 'hooked', 'in_progress')"
		if err := db.QueryRowContext(ctx, openQuery).Scan(&result.OpenRemain); err != nil {
			return nil, fmt.Errorf("count open: %w", err)
		}
		return result, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	// Batch UPDATE: select IDs in chunks, update each chunk.
	// This avoids holding a write lock on the entire table for minutes.
	// Uses LEFT JOIN anti-pattern instead of correlated EXISTS to avoid O(n*m) cost (gt-jd1z).
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s %s WHERE %s LIMIT %d",
		parentJoin, trackedJoin, whereClause, DefaultBatchSize)

	totalReaped := 0
	for {
		rows, err := db.QueryContext(ctx, idQuery, cutoff)
		if err != nil {
			return nil, fmt.Errorf("select reap batch: %w", err)
		}

		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan wisp id: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close reap rows: %w", err)
		}

		if len(ids) == 0 {
			break
		}

		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		inClause := strings.Join(placeholders, ",")

		updateQuery := fmt.Sprintf(
			"UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (%s)",
			inClause)
		sqlResult, err := db.ExecContext(ctx, updateQuery, args...)
		if err != nil {
			return nil, fmt.Errorf("close stale wisps batch: %w", err)
		}

		affected, _ := sqlResult.RowsAffected()
		totalReaped += int(affected)
	}

	result.Reaped = totalReaped

	if totalReaped > 0 {
		// Flush the SQL transaction to the Dolt working set before DOLT_COMMIT.
		// With autocommit=0, UPDATE changes are in the SQL transaction buffer,
		// not the Dolt working set. DOLT_COMMIT operates on the working set,
		// so without this COMMIT it sees "nothing to commit".
		if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
			return result, fmt.Errorf("sql commit: %w", err)
		}
		commitMsg := fmt.Sprintf("reaper: close %d stale wisps in %s", totalReaped, dbName)
		// Skip the commit when nothing landed in the working set (e.g. the only
		// mutated tables are dolt-ignored), avoiding a server-side "nothing to
		// commit" warning in dolt.log (gu-leuwr).
		if hasWorkingSetChanges(ctx, db) {
			if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
				// "nothing to commit" is expected when the reaper reverts dirty working
				// set changes back to match HEAD. The wisps were set to "open" in the
				// server's in-memory working set without being committed; closing them
				// makes the working set match HEAD again, so DOLT_COMMIT sees no diff.
				if !isNothingToCommit(err) {
					return result, fmt.Errorf("dolt commit: %w", err)
				}
			}
		}
	}

	openQuery := "SELECT COUNT(*) FROM wisps WHERE status IN ('open', 'hooked', 'in_progress')"
	if err := db.QueryRowContext(ctx, openQuery).Scan(&result.OpenRemain); err != nil {
		return result, fmt.Errorf("count open: %w", err)
	}

	return result, nil
}

// Purge deletes old closed wisps and mail from a database.
func Purge(db *sql.DB, dbName string, purgeAge, mailDeleteAge time.Duration, dryRun bool) (*PurgeResult, error) {
	result := &PurgeResult{Database: dbName, DryRun: dryRun}

	// Purge closed wisps.
	purged, anomalies, err := purgeClosedWisps(db, dbName, purgeAge, dryRun)
	if err != nil {
		return nil, fmt.Errorf("purge wisps: %w", err)
	}
	result.WispsPurged = purged
	result.Anomalies = append(result.Anomalies, anomalies...)

	// Purge old mail.
	mailPurged, err := purgeOldMail(db, dbName, mailDeleteAge, dryRun)
	if err != nil {
		return result, fmt.Errorf("purge mail: %w", err)
	}
	result.MailPurged = mailPurged

	return result, nil
}

func purgeClosedWisps(db *sql.DB, dbName string, purgeAge time.Duration, dryRun bool) (int, []Anomaly, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	deleteCutoff := time.Now().UTC().Add(-purgeAge)
	ephemeralCutoff := time.Now().UTC().Add(-EphemeralPurgeAge)
	var anomalies []Anomaly
	trackedJoin, trackedWhere := liveTrackedContextExcludeJoin(dbName)

	// Digest: count by wisp_type.
	// No parent check — closed wisps past the delete age are unconditionally purgeable.
	// The parent check (correlated subqueries on wisp_dependencies) was causing O(n*m)
	// query cost with 1800+ closed wisps, leading to CPU spikes and timeouts (gt-wvd2).
	// The live-tracked guard spares closed sling-context wisps whose work bead is still
	// open (gu-25jx5); it uses the O(n+m) LEFT JOIN anti-pattern so the gt-wvd2 cost does
	// not return. Must match Scan()'s purge count so scan>0/purge=0 drift cannot appear.
	digestQuery := fmt.Sprintf(
		"SELECT COALESCE(w.wisp_type, 'unknown') AS wtype, COUNT(*) AS cnt FROM wisps w %s WHERE %s AND %s GROUP BY wtype",
		trackedJoin, closedWispPurgeWhere, trackedWhere)
	rows, err := db.QueryContext(ctx, digestQuery, deleteCutoff, ephemeralCutoff)
	if err != nil {
		return 0, nil, fmt.Errorf("digest query: %w", err)
	}
	digestTotal := 0
	for rows.Next() {
		var wtype string
		var cnt int
		if err := rows.Scan(&wtype, &cnt); err != nil {
			_ = rows.Close()
			return 0, nil, fmt.Errorf("digest scan: %w", err)
		}
		digestTotal += cnt
	}
	if err := rows.Close(); err != nil {
		return 0, nil, fmt.Errorf("close digest rows: %w", err)
	}

	if digestTotal == 0 {
		return 0, anomalies, nil
	}

	if dryRun {
		return digestTotal, anomalies, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return 0, nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	// Batch delete — status+age filter plus the live-tracked sling-context guard so an
	// in-flight context is never purged while its work bead is open (gu-25jx5). Must stay
	// in lockstep with the digest query above and Scan()'s purge count.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s WHERE %s AND %s LIMIT %d",
		trackedJoin, closedWispPurgeWhere, trackedWhere, DefaultBatchSize)
	auxTables := []string{"wisp_labels", "wisp_comments", "wisp_events", "wisp_dependencies"}

	totalDeleted, err := batchDeleteRows(ctx, db, idQuery, "wisps", auxTables, deleteCutoff, ephemeralCutoff)
	if err != nil {
		return totalDeleted, anomalies, err
	}

	if totalDeleted > 0 {
		// Flush SQL transaction to working set before DOLT_COMMIT.
		if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
			anomalies = append(anomalies, Anomaly{
				Type:    "sql_commit_failed",
				Message: fmt.Sprintf("sql commit after purge failed: %v", err),
			})
			return totalDeleted, anomalies, nil
		}
		commitMsg := fmt.Sprintf("reaper: purge %d closed wisps from %s", totalDeleted, dbName)
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('--allow-empty', '-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			// Non-fatal — log but continue.
			anomalies = append(anomalies, Anomaly{
				Type:    "dolt_commit_failed",
				Message: fmt.Sprintf("dolt commit after purge failed: %v", err),
			})
		}
	}

	return totalDeleted, anomalies, nil
}

func purgeOldMail(db *sql.DB, dbName string, mailDeleteAge time.Duration, dryRun bool) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	mailCutoff := time.Now().UTC().Add(-mailDeleteAge)

	countQuery := fmt.Sprintf(
		"SELECT COUNT(*) FROM `%s`.issues WHERE status = 'closed' AND closed_at < ? AND id IN (SELECT issue_id FROM `%s`.labels WHERE label = 'gt:message')",
		dbName, dbName)
	var count int
	if err := db.QueryRowContext(ctx, countQuery, mailCutoff).Scan(&count); err != nil {
		if isTableNotFound(err) {
			return 0, nil // issues/labels not on this server
		}
		return 0, fmt.Errorf("count mail: %w", err)
	}
	if count == 0 {
		return 0, nil
	}

	if dryRun {
		return count, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return 0, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	idQuery := fmt.Sprintf(
		"SELECT i.id FROM `%s`.issues i INNER JOIN `%s`.labels l ON i.id = l.issue_id WHERE i.status = 'closed' AND i.closed_at < ? AND l.label = 'gt:message' LIMIT %d",
		dbName, dbName, DefaultBatchSize)
	auxTables := []string{"labels", "comments", "events", "dependencies"}

	totalDeleted, err := batchDeleteRows(ctx, db, idQuery, "issues", auxTables, mailCutoff)
	if err != nil {
		return totalDeleted, err
	}

	if totalDeleted > 0 {
		// Flush SQL transaction to working set before DOLT_COMMIT.
		if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
			return totalDeleted, fmt.Errorf("sql commit: %w", err)
		}
		commitMsg := fmt.Sprintf("reaper: purge %d old mail from %s", totalDeleted, dbName)
		// Non-fatal: Dolt commit failure doesn't lose the SQL deletes.
		_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('--allow-empty', '-Am', '%s')", commitMsg)) //nolint:gosec // G201: commitMsg from safe values
	}

	return totalDeleted, nil
}

// AutoClose closes issues that have been open with no updates past staleAge.
// Excludes P0/P1 priority, epics, hooked/pinned issues, standing-order labels,
// agent infra beads (gt:agent — permanent identity reference; closing them
// breaks 'gt agents resolve', see gu-016x1), and issues with active dependencies.
func AutoClose(db *sql.DB, dbName string, staleAge time.Duration, dryRun bool) (*AutoCloseResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	staleCutoff := time.Now().UTC().Add(-staleAge)
	result := &AutoCloseResult{Database: dbName, DryRun: dryRun}

	whereClause := fmt.Sprintf(`
		i.status IN ('open', 'in_progress')
		AND i.updated_at < ?
		AND i.priority > 1
		AND i.issue_type != 'epic'
		AND i.id NOT IN (
			SELECT DISTINCT l.issue_id FROM `+"`%s`"+`.labels l
			WHERE l.label IN ('gt:standing-orders', 'gt:keep', 'gt:role', 'gt:rig', 'gt:agent')
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.issue_id FROM `+"`%s`"+`.dependencies d
			INNER JOIN `+"`%s`"+`.issues dep ON d.depends_on_issue_id = dep.id
			WHERE dep.status IN ('open', 'in_progress')
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.depends_on_issue_id FROM `+"`%s`"+`.dependencies d
			INNER JOIN `+"`%s`"+`.issues blocker ON d.issue_id = blocker.id
			WHERE blocker.status IN ('open', 'in_progress')
		)`, dbName, dbName, dbName, dbName, dbName)

	// Two-step SELECT-then-UPDATE to avoid self-referencing subquery in UPDATE,
	// which is not valid MySQL (Error 1093) and fragile in Dolt (dolthub/dolt#10600).
	selectQuery := fmt.Sprintf("SELECT i.id, i.title, i.updated_at FROM issues i WHERE %s", whereClause)
	rows, err := db.QueryContext(ctx, selectQuery, staleCutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil // issues/dependencies not on this server
		}
		return nil, fmt.Errorf("select stale: %w", err)
	}
	type candidate struct {
		id        string
		title     string
		updatedAt time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.title, &c.updatedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan stale id: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close stale rows: %w", err)
	}

	// Build per-issue closure log entries.
	now := time.Now().UTC()
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
		result.ClosedEntries = append(result.ClosedEntries, ClosedEntry{
			ID:       c.id,
			Title:    c.title,
			AgeDays:  int(now.Sub(c.updatedAt).Hours() / 24),
			Database: dbName,
		})
	}

	if dryRun {
		result.Closed = len(ids)
		return result, nil
	}

	if len(ids) == 0 {
		return result, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE `%s`.issues SET status = 'closed', closed_at = NOW(), close_reason = 'stale:auto-closed by reaper' WHERE id IN (%s)",
		dbName, strings.Join(placeholders, ","))
	if _, err := db.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("auto-close: %w", err)
	}

	result.Closed = len(ids)

	if len(ids) > 0 {
		// Flush SQL transaction to working set before DOLT_COMMIT.
		if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "sql_commit_failed",
				Message: fmt.Sprintf("sql commit after auto-close failed: %v", err),
			})
			return result, nil
		}
		commitMsg := fmt.Sprintf("reaper: auto-close %d stale issues in %s", len(ids), dbName)
		// Skip the commit when nothing landed in the working set (e.g. the only
		// mutated tables are dolt-ignored), avoiding a server-side "nothing to
		// commit" warning in dolt.log (gu-leuwr).
		if hasWorkingSetChanges(ctx, db) {
			if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
				// "nothing to commit" is expected when the updated tables are dolt_ignored.
				if !isNothingToCommit(err) {
					result.Anomalies = append(result.Anomalies, Anomaly{
						Type:    "dolt_commit_failed",
						Message: fmt.Sprintf("dolt commit after auto-close failed: %v", err),
					})
				}
			}
		}
	}

	return result, nil
}

// batchDeleteRows deletes rows from a primary table and its auxiliary tables in batches.
// cutoffArgs are bound to idQuery's placeholders on every batch iteration.
func batchDeleteRows(ctx context.Context, db *sql.DB, idQuery, primaryTable string, auxTables []string, cutoffArgs ...interface{}) (int, error) {
	totalDeleted := 0
	for {
		idRows, err := db.QueryContext(ctx, idQuery, cutoffArgs...)
		if err != nil {
			return totalDeleted, fmt.Errorf("select batch: %w", err)
		}

		var ids []string
		for idRows.Next() {
			var id string
			if err := idRows.Scan(&id); err != nil {
				_ = idRows.Close()
				return totalDeleted, fmt.Errorf("scan id: %w", err)
			}
			ids = append(ids, id)
		}
		if err := idRows.Close(); err != nil {
			return totalDeleted, fmt.Errorf("close batch rows: %w", err)
		}

		if len(ids) == 0 {
			break
		}

		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		inClause := "(" + strings.Join(placeholders, ",") + ")"

		for _, tbl := range auxTables {
			delAux := fmt.Sprintf("DELETE FROM `%s` WHERE issue_id IN %s", tbl, inClause) //nolint:gosec // G201: tbl is internal
			// Non-fatal: best-effort cleanup of auxiliary tables.
			_, _ = db.ExecContext(ctx, delAux, args...)
		}

		// Clean up reverse dependency references to prevent dangling parent refs.
		// Non-fatal: primary delete below is what matters.
		delReverse := fmt.Sprintf("DELETE FROM wisp_dependencies WHERE depends_on_issue_id IN %s", inClause)
		_, _ = db.ExecContext(ctx, delReverse, args...)

		delPrimary := fmt.Sprintf("DELETE FROM `%s` WHERE id IN %s", primaryTable, inClause) //nolint:gosec // G201: primaryTable is internal
		sqlResult, err := db.ExecContext(ctx, delPrimary, args...)
		if err != nil {
			return totalDeleted, fmt.Errorf("delete %s batch: %w", primaryTable, err)
		}
		affected, _ := sqlResult.RowsAffected()
		totalDeleted += int(affected)
	}

	return totalDeleted, nil
}

// ClosePluginReceiptResult holds the results of closing plugin run receipts.
type ClosePluginReceiptResult struct {
	Database  string    `json:"database"`
	Closed    int       `json:"closed"`
	DryRun    bool      `json:"dry_run,omitempty"`
	Anomalies []Anomaly `json:"anomalies,omitempty"`
}

// ClosePluginReceipts closes open wisps labeled "type:plugin-run" that are
// older than maxAge. These are transient run receipts created by deacon dog
// plugins and patrol scripts (RESTART_POLECAT, stuck-agent-dog, dolt-backup,
// mol-dog-*, etc.); they should be closed shortly after creation since they
// exist only for audit/cooldown-gate purposes. The standard reap path uses
// 24h max_age, which lets receipts accumulate past the alert_threshold
// during normal-volume daemon activity (gs-g9k).
//
// Patrol receipts live in the wisps table (not issues), so this function
// queries wisps/wisp_labels. Agent beads (issue_type='agent') are excluded
// for symmetry with Reap(), even though receipts are not expected to use
// that issue_type.
func ClosePluginReceipts(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ClosePluginReceiptResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	result := &ClosePluginReceiptResult{Database: dbName, DryRun: dryRun}

	// Find open wisps with the "type:plugin-run" label older than maxAge.
	selectQuery := `
		SELECT w.id FROM wisps w
		INNER JOIN wisp_labels wl ON w.id = wl.issue_id
		WHERE w.status IN ('open', 'hooked', 'in_progress')
		AND wl.label = 'type:plugin-run'
		AND w.issue_type != 'agent'
		AND w.created_at < ?`

	rows, err := db.QueryContext(ctx, selectQuery, cutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil
		}
		return nil, fmt.Errorf("select plugin receipts: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan plugin receipt id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close plugin receipt rows: %w", err)
	}

	result.Closed = len(ids)
	if len(ids) == 0 || dryRun {
		return result, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	if _, err := db.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("close plugin receipts: %w", err)
	}

	// Flush and commit.
	if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "sql_commit_failed",
			Message: fmt.Sprintf("sql commit after plugin receipt close failed: %v", err),
		})
		return result, nil
	}
	commitMsg := fmt.Sprintf("reaper: close %d plugin receipts in %s", len(ids), dbName)
	// Skip the commit when nothing landed in the working set (e.g. the only
	// mutated tables are dolt-ignored), avoiding a server-side "nothing to
	// commit" warning in dolt.log (gu-leuwr).
	if hasWorkingSetChanges(ctx, db) {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			if !isNothingToCommit(err) {
				result.Anomalies = append(result.Anomalies, Anomaly{
					Type:    "dolt_commit_failed",
					Message: fmt.Sprintf("dolt commit after plugin receipt close failed: %v", err),
				})
			}
		}
	}

	return result, nil
}

// ClosePluginDispatches closes open dispatch mail beads created by the daemon
// when sending plugin instructions to dogs. These beads are labeled "gt:message"
// + "from:daemon" with a title prefix "Plugin:" and are never closed after the
// dog completes. Without this, they accumulate at ~288/day (one per 5-minute
// stuck-agent-dog run) and are only caught by AutoClose after 7 days.
func ClosePluginDispatches(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ClosePluginReceiptResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	result := &ClosePluginReceiptResult{Database: dbName, DryRun: dryRun}

	// Find open issues with both "gt:message" and "from:daemon" labels whose
	// title starts with "Plugin:", older than maxAge.
	selectQuery := fmt.Sprintf(`
		SELECT i.id FROM `+"`%s`"+`.issues i
		INNER JOIN `+"`%s`"+`.labels l1 ON i.id = l1.issue_id
		INNER JOIN `+"`%s`"+`.labels l2 ON i.id = l2.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND l1.label = 'gt:message'
		AND l2.label = 'from:daemon'
		AND i.title LIKE 'Plugin:%%'
		AND i.created_at < ?`, dbName, dbName, dbName)

	rows, err := db.QueryContext(ctx, selectQuery, cutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil
		}
		return nil, fmt.Errorf("select plugin dispatches: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan plugin dispatch id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close plugin dispatch rows: %w", err)
	}

	result.Closed = len(ids)
	if len(ids) == 0 || dryRun {
		return result, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE `%s`.issues SET status = 'closed', closed_at = NOW() WHERE id IN (%s)",
		dbName, strings.Join(placeholders, ","))
	if _, err := db.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("close plugin dispatches: %w", err)
	}

	// Flush and commit.
	if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "sql_commit_failed",
			Message: fmt.Sprintf("sql commit after plugin dispatch close failed: %v", err),
		})
		return result, nil
	}
	commitMsg := fmt.Sprintf("reaper: close %d plugin dispatches in %s", len(ids), dbName)
	// Skip the commit when nothing landed in the working set (e.g. the only
	// mutated tables are dolt-ignored), avoiding a server-side "nothing to
	// commit" warning in dolt.log (gu-leuwr).
	if hasWorkingSetChanges(ctx, db) {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			if !isNothingToCommit(err) {
				result.Anomalies = append(result.Anomalies, Anomaly{
					Type:    "dolt_commit_failed",
					Message: fmt.Sprintf("dolt commit after plugin dispatch close failed: %v", err),
				})
			}
		}
	}

	return result, nil
}

// CloseStaleHookedMols closes wisps that have been in 'hooked' status for
// longer than maxAge. These are dispatch wisps created by gt sling targeting
// the dog group (deacon/dogs) that were never consumed because no dog session
// was running. The title filter "mol-dog-%" scopes cleanup to daemon-dispatch
// mols without touching user-created hooked beads. The standard Reap() path
// also reaps hooked wisps but only after 24h; this shorter TTL prevents
// orphan accumulation between reaper cycles. See: GH#3767.
func CloseStaleHookedMols(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ClosePluginReceiptResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	result := &ClosePluginReceiptResult{Database: dbName, DryRun: dryRun}

	selectQuery := fmt.Sprintf(
		"SELECT id FROM `%s`.wisps WHERE status = 'hooked' AND title LIKE 'mol-dog-%%' AND issue_type != 'agent' AND created_at < ?",
		dbName)

	rows, err := db.QueryContext(ctx, selectQuery, cutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil
		}
		return nil, fmt.Errorf("select stale hooked mols: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan hooked mol id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	result.Closed = len(ids)
	if len(ids) == 0 || dryRun {
		return result, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE `%s`.wisps SET status = 'closed', closed_at = NOW() WHERE id IN (%s)",
		dbName, strings.Join(placeholders, ","))
	if _, err := db.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("close stale hooked mols: %w", err)
	}

	if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "sql_commit_failed",
			Message: fmt.Sprintf("sql commit after hooked mol close failed: %v", err),
		})
		return result, nil
	}
	commitMsg := fmt.Sprintf("reaper: close %d stale hooked mols in %s", len(ids), dbName)
	// Skip the commit when nothing landed in the working set (e.g. the only
	// mutated tables are dolt-ignored), avoiding a server-side "nothing to
	// commit" warning in dolt.log (gu-leuwr).
	if hasWorkingSetChanges(ctx, db) {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			if !isNothingToCommit(err) {
				result.Anomalies = append(result.Anomalies, Anomaly{
					Type:    "dolt_commit_failed",
					Message: fmt.Sprintf("dolt commit after hooked mol close failed: %v", err),
				})
			}
		}
	}

	return result, nil
}

// FormatJSON marshals any value to indented JSON.
func FormatJSON(v interface{}) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(data)
}

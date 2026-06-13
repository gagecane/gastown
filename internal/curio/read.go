package curio

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// recentFPCap bounds how many recent false-positive summaries ReadOutcomeHistory
// returns per rule. A rule with a noisy history (e.g. kill_signal_near_dolt
// during a Dolt incident) can have hundreds of FP rows; the digest only needs a
// handful of recent examples to justify a tune, and the cap keeps the rendered
// artifact (and the embedded untrusted text it carries) bounded.
const recentFPCap = 5

// RuleOutcome is the per-rule precision summary ReadOutcomeHistory derives from
// curio_ledger. Resolved counts only JUDGED rows — outcome in
// {fixed,false_positive,duplicate,deferred}; the design EXCLUDES 'unknown' and
// unreconciled (empty-outcome) rows so a young/unjudgeable filing neither
// inflates nor deflates a rule's measured precision. Precision is the fraction
// of judged rows that were NOT false positives, rounded to two decimals (only
// false_positive decrements precision, per the design's precision formula).
type RuleOutcome struct {
	RuleID            string   `json:"rule_id"`
	Resolved          int      `json:"resolved"`
	FalsePositives    int      `json:"false_positives"`
	Precision         float64  `json:"precision"`
	RecentFPSummaries []string `json:"recent_fp_summaries"`
}

// Reader is the Retrospect lane's READ-ONLY view of the curio_candidate
// sidecar. It is deliberately separate from Store: it issues SELECT statements
// exclusively and NEVER creates tables, inserts, updates, or deletes. The
// write-incapable curio-proposer binary (build 3/6) reads through this type and
// nothing else, so the "Retrospect cannot mutate state" invariant holds at the
// type boundary — Reader simply exposes no write method to call.
//
// Unlike OpenStore, OpenReader does NOT run ensureTables: creating tables is a
// write, and the Retrospect lane must touch zero state. A missing table is a
// read error the caller surfaces, not something Reader silently provisions.
type Reader struct {
	db *sql.DB
}

// OpenReader connects to the gt Dolt server for read-only candidate access.
// host defaults to 127.0.0.1 when empty; dbName is typically "hq". It performs
// no DDL and no writes.
func OpenReader(host string, port int, dbName string) (*Reader, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=10s&readTimeout=30s",
		host, port, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open hq dolt (read-only): %w", err)
	}
	return &Reader{db: db}, nil
}

// Close releases the underlying connection pool.
func (r *Reader) Close() error { return r.db.Close() }

// ReadCandidatesBefore returns candidates whose created_at is STRICTLY before
// cutoff, newest first. Passing the closed-window cursor (now minus a margin)
// as cutoff is what enforces the closed-window invariant: in-flight candidates
// written inside the margin are never read, so Retrospect can never live-tail
// the live Patrol's current cycle.
func (r *Reader) ReadCandidatesBefore(cutoff time.Time) ([]Candidate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := r.db.QueryContext(ctx,
		"SELECT fingerprint, window_id, series, observed, ewma, deviation,"+
			" hypothesis, rule_id, target, rig, summary"+
			" FROM "+candidateTable+
			" WHERE created_at < ?"+
			" ORDER BY created_at DESC",
		cutoff.UTC())
	if err != nil {
		return nil, fmt.Errorf("reading candidates before %s: %w", cutoff.UTC().Format(time.RFC3339), err)
	}
	defer func() { _ = rows.Close() }()

	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(
			&c.Fingerprint, &c.WindowID, &c.Series, &c.Observed, &c.EWMA, &c.Deviation,
			&c.Hypothesis, &c.RuleID, &c.Target, &c.Rig, &c.Summary,
		); err != nil {
			return nil, fmt.Errorf("scanning candidate row: %w", err)
		}
		// StateHash is not persisted (build 2a left the schema unchanged); the
		// read view defaults it to the fingerprint, matching newCandidate.
		c.StateHash = c.Fingerprint
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating candidate rows: %w", err)
	}
	return out, nil
}

// ReadOutcomeHistory returns the per-rule precision summary over curio_ledger.
// It is a pure read (two SELECTs, no DDL/writes) — the one piece of P2 build-4
// scope the Retrospect lane keeps. The precision formula counts only JUDGED rows
// (outcome in {fixed,false_positive,duplicate,deferred}); 'unknown' and
// unreconciled (empty-outcome) rows are excluded so an unjudgeable filing does
// not move a rule's measured precision. Output is sorted by rule_id for
// deterministic rendering.
func (r *Reader) ReadOutcomeHistory() ([]RuleOutcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Aggregate judged rows per rule. The IN-list is the same outcome set the
	// classifier emits minus 'unknown' (and minus the empty unreconciled state),
	// matching the design's "precision EXCLUDES unknown" rule.
	aggRows, err := r.db.QueryContext(ctx,
		"SELECT rule_id,"+
			" SUM(CASE WHEN outcome IN ('fixed','false_positive','duplicate','deferred') THEN 1 ELSE 0 END) AS resolved,"+
			" SUM(CASE WHEN outcome = 'false_positive' THEN 1 ELSE 0 END) AS fps"+
			" FROM "+ledgerTable+
			" GROUP BY rule_id")
	if err != nil {
		return nil, fmt.Errorf("reading outcome aggregates: %w", err)
	}
	byRule := make(map[string]*RuleOutcome)
	for aggRows.Next() {
		var (
			ruleID        string
			resolved, fps int64
		)
		if err := aggRows.Scan(&ruleID, &resolved, &fps); err != nil {
			_ = aggRows.Close()
			return nil, fmt.Errorf("scanning outcome aggregate row: %w", err)
		}
		ro := &RuleOutcome{
			RuleID:         ruleID,
			Resolved:       int(resolved),
			FalsePositives: int(fps),
		}
		// Precision = non-FP judged / judged, rounded to 2dp. A rule with no
		// judged rows has undefined precision; report 0 resolved and precision 0
		// (the renderer shows "n/a" for resolved==0 so 0.0 is never mistaken for
		// a measured zero-precision rule).
		if resolved > 0 {
			ro.Precision = math.Round(float64(resolved-fps)/float64(resolved)*100) / 100
		}
		byRule[ruleID] = ro
	}
	if err := aggRows.Err(); err != nil {
		_ = aggRows.Close()
		return nil, fmt.Errorf("iterating outcome aggregate rows: %w", err)
	}
	_ = aggRows.Close()

	// Recent false-positive summaries per rule, newest first, joined to the
	// candidate sidecar for the human-readable summary. A FP row whose candidate
	// is absent (summary NULL) yields an empty string the renderer treats as
	// "(summary unavailable)".
	fpRows, err := r.db.QueryContext(ctx,
		"SELECT l.rule_id, c.summary"+
			" FROM "+ledgerTable+" l"+
			" LEFT JOIN "+candidateTable+" c ON c.fingerprint = l.fingerprint"+
			" WHERE l.outcome = 'false_positive'"+
			" ORDER BY l.resolved_at DESC, l.bead_id DESC")
	if err != nil {
		return nil, fmt.Errorf("reading recent false-positive summaries: %w", err)
	}
	for fpRows.Next() {
		var (
			ruleID  string
			summary sql.NullString
		)
		if err := fpRows.Scan(&ruleID, &summary); err != nil {
			_ = fpRows.Close()
			return nil, fmt.Errorf("scanning recent FP row: %w", err)
		}
		ro, ok := byRule[ruleID]
		if !ok {
			// A FP row whose rule had no judged aggregate is impossible (an FP IS
			// judged), but guard defensively rather than panic on a map miss.
			continue
		}
		if len(ro.RecentFPSummaries) >= recentFPCap {
			continue
		}
		ro.RecentFPSummaries = append(ro.RecentFPSummaries, summary.String)
	}
	if err := fpRows.Err(); err != nil {
		_ = fpRows.Close()
		return nil, fmt.Errorf("iterating recent FP rows: %w", err)
	}
	_ = fpRows.Close()

	out := make([]RuleOutcome, 0, len(byRule))
	for _, ro := range byRule {
		out = append(out, *ro)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RuleID < out[j].RuleID })
	return out, nil
}

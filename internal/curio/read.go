package curio

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

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

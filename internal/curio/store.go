package curio

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// Storage lives in TOWN HQ Dolt (the "hq" database), NOT rig Dolt — that's
// where the gc- Curio epic lives (eng-review decision 4). Filed beads (later
// phases) still route to the owning rig via beads.GetRigDirForName; only the
// candidate/ledger sidecar tables are HQ-local.

const (
	// candidateTable holds emitted Curio candidates (never beads in Phase 1).
	candidateTable = "curio_candidate"
	// ledgerTable is the precision-ledger sidecar, keyed by bead ID, joined to
	// bead outcome later. Created now so precision is queryable in Phase 2.
	ledgerTable = "curio_ledger"
)

// candidateDDL matches the design's candidate row shape plus dedup/routing
// columns. fingerprint is UNIQUE so re-emitting the same finding is a no-op
// (INSERT IGNORE), giving cross-cycle dedup at the storage layer.
var candidateDDL = `CREATE TABLE ` + candidateTable + ` (
  fingerprint varchar(12) NOT NULL,
  window_id varchar(255) NOT NULL,
  series varchar(255) NOT NULL,
  observed bigint NOT NULL DEFAULT 0,
  ewma double NOT NULL DEFAULT 0,
  deviation double NOT NULL DEFAULT 0,
  hypothesis text,
  rule_id varchar(255) NOT NULL,
  target varchar(512) NOT NULL DEFAULT '',
  rig varchar(255) NOT NULL DEFAULT '',
  summary text NOT NULL DEFAULT '',
  created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (fingerprint),
  KEY idx_curio_candidate_window (window_id),
  KEY idx_curio_candidate_rule (rule_id)
)`

// ledgerDDL is the precision-ledger sidecar. outcome is one of
// {fixed,false-positive,duplicate,deferred}; only false-positive decrements
// precision (design "precision ledger storage" section). Empty until Phase 2.
var ledgerDDL = `CREATE TABLE ` + ledgerTable + ` (
  bead_id varchar(255) NOT NULL,
  fingerprint varchar(12) NOT NULL DEFAULT '',
  rule_id varchar(255) NOT NULL DEFAULT '',
  filed_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  outcome varchar(32) NOT NULL DEFAULT '',
  resolved_at datetime,
  PRIMARY KEY (bead_id),
  KEY idx_curio_ledger_outcome (outcome)
)`

// Store writes candidates to HQ Dolt. It is created with a DSN to the gt Dolt
// server's "hq" database.
type Store struct {
	db     *sql.DB
	dbName string
}

// OpenStore connects to the gt Dolt server and ensures the curio tables exist.
// host defaults to 127.0.0.1 when empty; dbName is typically "hq".
//
// HQ is the busiest Dolt database in the town (heartbeat lock-commit churn, bd
// writes, convoy). A fresh session's first query can race a concurrent
// DOLT_COMMIT mid-flight and fail with "no root value found in session" — a
// TRANSIENT that clears on a fresh connection (gu-iebpz; same class as the
// gu-tqtwt wisp-commit race that a Dolt restart cleared). ensureTables retries
// on that signature so a momentary commit collision no longer fails the whole
// patrol cycle and spams the log until an out-of-band restart.
func OpenStore(host string, port int, dbName string) (*Store, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=10s&readTimeout=30s&writeTimeout=30s",
		host, port, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open hq dolt: %w", err)
	}
	s := &Store{db: db, dbName: dbName}
	if err := s.ensureTablesWithRetry(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// sessionRootRetries bounds the reopen-retry loop for the transient
// "no root value found in session" error. 3 attempts with a short linear
// backoff comfortably outlasts a single concurrent DOLT_COMMIT's commit window
// without holding the patrol cycle open.
const sessionRootRetries = 3

// isSessionRootError reports whether err is Dolt's transient mid-commit session
// race (Error 1105 "no root value found in session"). It is recoverable by
// retrying on a fresh server-side session — distinct from a real schema/DDL
// failure, which must surface.
func isSessionRootError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no root value found in session")
}

// ensureTablesWithRetry calls ensureTables, retrying only the transient
// session-root race. A fresh connection from the pool gets a fresh session, so
// the next attempt typically lands cleanly once the colliding commit completes.
func (s *Store) ensureTablesWithRetry() error {
	return retryOnSessionRoot(s.ensureTables, sessionRootRetries, sleepBackoff)
}

// sleepBackoff is the production backoff between session-root retries: linear
// 250ms × attempt. Injected into retryOnSessionRoot so tests run instantly.
func sleepBackoff(attempt int) { time.Sleep(time.Duration(attempt) * 250 * time.Millisecond) }

// retryOnSessionRoot runs fn up to maxAttempts times, retrying ONLY the
// transient "no root value found in session" error and calling backoff(attempt)
// between tries. Any other error (or success) returns immediately. Extracted
// from ensureTablesWithRetry so the retry policy is unit-testable without a DB.
func retryOnSessionRoot(fn func() error, maxAttempts int, backoff func(attempt int)) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if !isSessionRootError(err) {
			return err
		}
		if attempt < maxAttempts {
			backoff(attempt)
		}
	}
	return err
}

// Close releases the underlying connection pool.
func (s *Store) Close() error { return s.db.Close() }

// ensureTables creates the candidate and ledger tables if absent, then commits
// the DDL to Dolt HEAD so the schema survives a new session.
//
// It commits via commitTables (explicit per-table DOLT_ADD, NOT DOLT_COMMIT('-Am')).
// The '-Am' sweep was the gu-tqtwt anti-pattern: it stages EVERY pending
// working-set change in the shared hq database, entangling curio's schema
// commit with unrelated churn, and the previous code then swallowed any commit
// failure (return nil) — so a table could remain working-set-only forever and
// every later session re-hit "no root value". commitTables stages only the
// curio tables and is keyed off dolt_status, making it self-healing: a table
// left uncommitted by an earlier crashed run is flushed to HEAD on the next
// open, even when this call creates nothing new.
func (s *Store) ensureTables() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, t := range []struct{ name, ddl string }{
		{candidateTable, candidateDDL},
		{ledgerTable, ledgerDDL},
	} {
		exists, err := s.tableExists(ctx, t.name)
		if err != nil {
			return fmt.Errorf("checking %s: %w", t.name, err)
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, t.ddl); err != nil {
			return fmt.Errorf("creating %s: %w", t.name, err)
		}
	}

	return s.commitTables(ctx, "curio: create candidate + ledger sidecar tables",
		candidateTable, ledgerTable)
}

// commitTables stages the named curio tables (only those with pending HEAD->
// WORKING changes) and commits them to Dolt HEAD. It is idempotent and
// self-healing: with nothing pending it is a no-op; if an earlier run created a
// table in the working set but failed to commit, the next open flushes it.
//
// Unlike DOLT_COMMIT('-Am', ...), this stages each curio table by name so the
// commit can never sweep in unrelated hq working-set churn. A commit failure is
// returned (not swallowed) so the caller — and the patrol log — sees that the
// schema is not yet durable rather than silently persisting nothing.
func (s *Store) commitTables(ctx context.Context, msg string, tables ...string) error {
	var pending []string
	for _, t := range tables {
		dirty, err := s.tableDirty(ctx, t)
		if err != nil {
			return fmt.Errorf("checking dirty %s: %w", t, err)
		}
		if dirty {
			pending = append(pending, t)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	// Dolt's staging area is SESSION-scoped: DOLT_ADD marks working-set changes
	// staged on the connection that ran it, and DOLT_COMMIT commits what is
	// staged on the SAME connection. The database/sql pool may hand each
	// ExecContext a different pooled connection, so the stage and commit must be
	// pinned to one sql.Conn or the commit sees nothing staged. (The old '-Am'
	// did stage+commit in a single statement, sidestepping this.)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pinning hq connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	for _, t := range pending {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD(?)", t); err != nil {
			return fmt.Errorf("staging %s: %w", t, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", msg); err != nil {
		return fmt.Errorf("committing curio schema: %w", err)
	}
	return nil
}

// tableDirty reports whether table has uncommitted (HEAD->WORKING) changes in
// dolt_status — i.e. it exists in the working set but is not yet committed to
// HEAD. A table absent from dolt_status is clean (already committed or absent).
func (s *Store) tableDirty(ctx context.Context, table string) (bool, error) {
	var dummy int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM dolt_status WHERE table_name = ? LIMIT 1", table).Scan(&dummy)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) tableExists(ctx context.Context, table string) (bool, error) {
	var dummy int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM information_schema.tables WHERE table_schema = ? AND table_name = ?",
		s.dbName, table).Scan(&dummy)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// FingerprintFiled reports whether a curio_ledger row already exists for the
// given fingerprint. The ledger doubles as the file-once dedup record: once a
// candidate has been filed (a row written at file-time), its fingerprint is in
// the ledger forever, so the filer never re-files the same finding — even after
// the bead is closed (the row persists with its outcome). An absent row means
// the finding has not been filed yet.
func (s *Store) FingerprintFiled(fingerprint string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var dummy int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM "+ledgerTable+" WHERE fingerprint = ? LIMIT 1", fingerprint).Scan(&dummy)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// InsertLedgerRow writes the file-time precision-ledger row for a freshly filed
// bead: (bead_id, fingerprint, rule_id) with outcome=” and filed_at defaulting
// to CURRENT_TIMESTAMP per the DDL. outcome + resolved_at stay empty until the
// post-close reconciler (B0b) fills them. INSERT IGNORE keeps the call
// idempotent on the bead_id primary key — a duplicate file-time insert for the
// same bead is a no-op, never an error.
//
// This is the PRODUCER half of curio_ledger population (B0a). It does not change
// the curio-proposer import graph: the write lives in the daemon-opened HQ
// store, on the write side of the read/write air-gap from cmd/curio-proposer.
func (s *Store) InsertLedgerRow(beadID, fingerprint, ruleID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx,
		"INSERT IGNORE INTO "+ledgerTable+
			" (bead_id, fingerprint, rule_id) VALUES (?, ?, ?)",
		beadID, fingerprint, ruleID); err != nil {
		return fmt.Errorf("inserting ledger row for %s: %w", beadID, err)
	}
	return nil
}

// InsertCandidates writes candidates, deduping by fingerprint (INSERT IGNORE).
// Returns the number of NEW rows inserted (already-seen fingerprints are
// silently skipped — cross-cycle dedup). Phase 1 only writes candidates; it
// NEVER files beads.
func (s *Store) InsertCandidates(cands []Candidate) (int, error) {
	if len(cands) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inserted := 0
	for _, c := range cands {
		res, err := s.db.ExecContext(ctx,
			"INSERT IGNORE INTO "+candidateTable+
				" (fingerprint, window_id, series, observed, ewma, deviation, hypothesis, rule_id, target, rig, summary)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			c.Fingerprint, c.WindowID, c.Series, c.Observed, c.EWMA, c.Deviation,
			c.Hypothesis, c.RuleID, c.Target, c.Rig, c.Summary)
		if err != nil {
			return inserted, fmt.Errorf("inserting candidate %s: %w", c.Fingerprint, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

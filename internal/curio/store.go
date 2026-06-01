package curio

import (
	"context"
	"database/sql"
	"fmt"
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
	if err := s.ensureTables(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() error { return s.db.Close() }

// ensureTables creates the candidate and ledger tables if absent, then commits
// the DDL to Dolt history (mirrors the wisps_migrate pattern).
func (s *Store) ensureTables() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var created bool
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
		created = true
	}

	if created {
		msg := "curio: create candidate + ledger sidecar tables"
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", msg)); err != nil {
			// Non-fatal: tables exist in the working set even without commit.
			return nil //nolint:nilerr // intentional: working-set tables are usable
		}
	}
	return nil
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

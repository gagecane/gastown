package curio

// Call 2 SHADOW-MODE ledger.
//
// The operator-mandated safety gate: Call 2's paging path lands in SHADOW MODE
// first. Every cycle's PageActions are recorded to this append-only ledger (and
// the daemon log) describing what Curio WOULD have paged — but no Overseer page
// is sent until PageForReal is deliberately enabled in a follow-up. This is the
// same candidates-only discipline that kept Phase 1 safe: prove precision and
// cadence on a live ledger before you can wake a human.
//
// The ledger lives in TOWN HQ Dolt alongside the candidate sidecar (store.go),
// NOT under .runtime — unlike the scheduler circuit-break log, a shadow page is
// precisely the durable, queryable signal the operator inspects to decide
// whether to flip PageForReal, so it must survive a daemon restart.

import (
	"context"
	"strings"
	"time"
)

// shadowPageTable records what Call 2 WOULD have paged, for precision/cadence
// review before real paging is enabled.
const shadowPageTable = "curio_shadow_page"

// shadowPageDDL matches the PageAction shape the emitter records. dedup_key is
// indexed (not unique): the ledger is an append-only audit trail of every
// would-page decision, including repeated bumps for the same dedup key, so
// cadence is measurable.
var shadowPageDDL = `CREATE TABLE ` + shadowPageTable + ` (
  id bigint NOT NULL AUTO_INCREMENT,
  window_id varchar(255) NOT NULL,
  kind varchar(32) NOT NULL,
  lane varchar(16) NOT NULL,
  severity varchar(16) NOT NULL,
  dedup_key varchar(255) NOT NULL DEFAULT '',
  summary text NOT NULL,
  proof text,
  occurrences bigint NOT NULL DEFAULT 0,
  clusters bigint NOT NULL DEFAULT 0,
  would_page tinyint NOT NULL DEFAULT 0,
  created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_curio_shadow_page_dedup (dedup_key),
  KEY idx_curio_shadow_page_window (window_id)
)`

// ensureShadowTable creates the shadow-page ledger table if absent. Mirrors
// Store.ensureTables; safe to call repeatedly. Kept separate from ensureTables
// so build 2a's candidate/ledger schema is untouched and this is additive.
func (s *Store) ensureShadowTable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exists, err := s.tableExists(ctx, shadowPageTable)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, shadowPageDDL); err != nil {
		return err
	}
	// Commit the DDL to Dolt history (mirrors ensureTables). Non-fatal on failure:
	// the working-set table is usable even without a commit.
	if _, err := s.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'curio: create shadow-page ledger')"); err != nil {
		return nil //nolint:nilerr // intentional: working-set table is usable
	}
	return nil
}

// RecordShadowPages appends one row per action to the shadow ledger, stamping
// whether the action WOULD have paged for real. Returns the number of rows
// written. A nil/empty slice is a no-op.
//
// wouldPage records the operator's PageForReal intent at decision time, so the
// ledger captures BOTH what was decided and whether the gate was open — making
// the shadow→live transition auditable after the fact.
func (s *Store) RecordShadowPages(windowID string, actions []PageAction, wouldPage bool) (int, error) {
	if len(actions) == 0 {
		return 0, nil
	}
	if err := s.ensureShadowTable(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	written := 0
	for _, a := range actions {
		wp := 0
		if wouldPage {
			wp = 1
		}
		_, err := s.db.ExecContext(ctx,
			"INSERT INTO "+shadowPageTable+
				" (window_id, kind, lane, severity, dedup_key, summary, proof, occurrences, clusters, would_page)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			windowID, a.Kind.String(), a.Lane.String(), a.Severity, a.DedupKey,
			a.Summary, strings.Join(a.Proof, "\n"), a.Occurrences, a.Clusters, wp)
		if err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

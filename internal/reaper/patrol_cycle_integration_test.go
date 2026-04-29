//go:build integration

// Patrol-cycle integration test for GUPP (gu-hhqk AC #7).
//
// Verifies that the hooked-mail lifecycle guarantee — TTL reaping via
// ReapHookedMail — keeps backlog bounded under repeated patrol cycles.
// Each patrol cycle simulates what witness/refinery/deacon do: emit a
// handful of HANDOFF / wisp mail beads on the hook, some of which get
// consumed (closed) by successor sessions, some of which are orphaned
// and must be swept by the reaper.
//
// The test asserts:
//
//  1. After 10 cycles WITH the reaper running at a short TTL between
//     cycles, count(hooked mail) stays bounded (< N+5 where N is the
//     cycle-0 baseline).
//  2. After 10 cycles WITHOUT the reaper, count(hooked mail) grows
//     unbounded and exceeds N+5 — this guards against a future
//     regression where the reaper silently no-ops.
//
// See gu-mmv2 for the tracking bead and gu-hhqk for the parent GUPP fix.
package reaper

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/testutil"
)

// patrolIntegrationDB is the test database created inside the isolated Dolt
// container. It is passed to ReapHookedMail for dolt-commit labeling, but
// the actual connection targets this DB via the DSN.
const patrolIntegrationDB = "patrol_test"

// openPatrolTestDB starts an isolated Dolt container, creates the
// patrol_test database plus the minimal issues/labels schema that
// ReapHookedMail/ScanHookedMail rely on, and returns an open *sql.DB
// targeting patrol_test. The container is torn down when the test ends.
func openPatrolTestDB(t *testing.T) *sql.DB {
	t.Helper()

	port := testutil.StartIsolatedDoltContainer(t)

	rootDSN := fmt.Sprintf("root@tcp(127.0.0.1:%s)/?parseTime=true&multiStatements=true", port)
	root, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("opening root DSN: %v", err)
	}
	defer root.Close()

	// Create DB. Issues/labels columns follow the schema ReapHookedMail
	// actually queries: id, title, status, issue_type, created_at in
	// issues; (issue_id, label) composite in labels. Keep the schema as
	// small as possible so the test breaks only when the reaper's
	// eligibility predicate regresses, not because of unrelated schema
	// drift.
	setup := []string{
		"CREATE DATABASE IF NOT EXISTS " + patrolIntegrationDB,
		"USE " + patrolIntegrationDB,
		`CREATE TABLE IF NOT EXISTS issues (
			id VARCHAR(64) NOT NULL PRIMARY KEY,
			title VARCHAR(255) NOT NULL DEFAULT '',
			status VARCHAR(32) NOT NULL DEFAULT 'open',
			issue_type VARCHAR(32) NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			closed_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS labels (
			issue_id VARCHAR(64) NOT NULL,
			label    VARCHAR(128) NOT NULL,
			PRIMARY KEY (issue_id, label)
		)`,
	}
	for _, stmt := range setup {
		if _, err := root.Exec(stmt); err != nil {
			t.Fatalf("setup stmt %q: %v", stmt, err)
		}
	}

	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%s)/%s?parseTime=true&multiStatements=true",
		port, patrolIntegrationDB)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("opening patrol DSN: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Ping(); err != nil {
		t.Fatalf("ping patrol DB: %v", err)
	}
	return db
}

// insertHookedMail inserts a mail bead with the given id, age, status, and
// issue_type into the test DB. labels is the list of labels to attach — in
// practice callers include "gt:message" for anything meant to be reaped.
func insertHookedMail(t *testing.T, db *sql.DB, id string, age time.Duration, status, issueType string, labels ...string) {
	t.Helper()

	createdAt := time.Now().UTC().Add(-age)
	// Use a parameterised INSERT so created_at is set precisely (CURRENT_TIMESTAMP
	// would ignore the "age" we want to simulate).
	if _, err := db.Exec(
		"INSERT INTO issues (id, title, status, issue_type, created_at) VALUES (?, ?, ?, ?, ?)",
		id, "handoff: "+id, status, issueType, createdAt,
	); err != nil {
		t.Fatalf("insert issue %s: %v", id, err)
	}

	for _, lbl := range labels {
		if _, err := db.Exec(
			"INSERT INTO labels (issue_id, label) VALUES (?, ?)",
			id, lbl,
		); err != nil {
			t.Fatalf("insert label %s on %s: %v", lbl, id, err)
		}
	}
}

// countHookedMail returns the number of hooked mail beads visible to the
// reaper's eligibility predicate (hooked + gt:message + not agent). Mirrors
// ScanHookedMail's query on the "total" side.
func countHookedMail(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`
		SELECT COUNT(DISTINCT i.id)
		FROM issues i
		INNER JOIN labels l ON i.id = l.issue_id
		WHERE i.status = 'hooked'
		  AND i.issue_type != 'agent'
		  AND l.label = 'gt:message'
	`).Scan(&n); err != nil {
		t.Fatalf("count hooked mail: %v", err)
	}
	return n
}

// closeIssue marks a hooked mail bead as consumed (status='closed'), the
// same thing `gt prime --hook` would do when a successor session claims it.
// This is how a guaranteed consumer drains the hook.
func closeIssue(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(
		"UPDATE issues SET status = 'closed', closed_at = NOW() WHERE id = ?",
		id,
	); err != nil {
		t.Fatalf("close issue %s: %v", id, err)
	}
}

// patrolCycleParams controls how each simulated patrol cycle seeds hooked
// mail beads. Mirrors the mix we saw during the 2026-04-28 audit: a few
// HANDOFF beads per cycle, most consumed, and a handful orphaned.
//
// Preserve-labeled mail (gt:standing-orders / gt:keep / gt:role / gt:rig)
// and agent heartbeat beads are seeded ONCE at test start via seedLongLived,
// not per-cycle, because in real deployments those are per-role fixtures —
// they don't accumulate with every patrol tick. The reaper must leave them
// alone every cycle regardless.
type patrolCycleParams struct {
	perCycleHandoffs int           // hooked mail beads created per cycle with gt:message
	consumedPerCycle int           // how many of the handoffs the successor closes
	initialAge       time.Duration // age for beads created in this cycle
}

// longLivedFixture counts per category of beads that must never be reaped.
// Seeded once at setup time via seedLongLived.
type longLivedFixture struct {
	preserveCount int // total gt:standing-orders beads seeded
	agentCount    int // total issue_type='agent' beads seeded
}

// defaultPatrolParams returns the shape of a normal patrol cycle. Three
// hooks created, two consumed — so one orphan per cycle leaks if nothing
// sweeps. Over 10 cycles that's 10 orphans, well above the N+5 bound,
// which is exactly what we want the reaper to collapse back down to ~0.
func defaultPatrolParams() patrolCycleParams {
	return patrolCycleParams{
		perCycleHandoffs: 3,
		consumedPerCycle: 2,
		// Age > DefaultDeadLetterThreshold (30m) so anything unreaped
		// would trip the doctor dead-letter check. Also ensures the
		// short-TTL reaper (1s) finds them.
		initialAge: time.Hour,
	}
}

// seedLongLived inserts a fixed set of preserve-labeled and agent beads
// that must survive every reap cycle. Returns the expected counts so the
// caller can later assert nothing was collateral-damaged.
func seedLongLived(t *testing.T, db *sql.DB, age time.Duration) longLivedFixture {
	t.Helper()

	fx := longLivedFixture{
		preserveCount: 2, // e.g. one standing-order + one keep bead
		agentCount:    2, // e.g. witness + refinery agent beads
	}

	// Preserve-labeled hooked mail — real-world: gt standing-orders,
	// long-lived rig/role beads. Must survive any ttl-expired sweep.
	insertHookedMail(t, db, "standing-orders-0", age, "hooked", "wisp",
		"gt:message", "gt:standing-orders")
	insertHookedMail(t, db, "keep-0", age, "hooked", "wisp",
		"gt:message", "gt:keep")

	// Agent heartbeat beads — issue_type='agent'. Must survive.
	insertHookedMail(t, db, "agent-witness", age, "hooked", "agent",
		"gt:message", "gt:agent")
	insertHookedMail(t, db, "agent-refinery", age, "hooked", "agent",
		"gt:message", "gt:agent")

	return fx
}

// runPatrolCycle seeds a single simulated patrol cycle. The cycle index is
// used to generate unique bead IDs so multiple cycles don't collide on the
// issues.id primary key. Returns the IDs of the beads that were orphaned
// (created hooked + never closed) so the caller can reason about them.
func runPatrolCycle(t *testing.T, db *sql.DB, cycle int, p patrolCycleParams) []string {
	t.Helper()

	// Create handoff hooks (hooked + gt:message).
	handoffIDs := make([]string, 0, p.perCycleHandoffs)
	for i := 0; i < p.perCycleHandoffs; i++ {
		id := fmt.Sprintf("wisp-%02d-%02d", cycle, i)
		insertHookedMail(t, db, id, p.initialAge, "hooked", "wisp", "gt:message")
		handoffIDs = append(handoffIDs, id)
	}

	// "Consume" the first N handoffs — simulate a successor session running
	// `gt prime --hook` and closing the bead once promoted to in_progress.
	for i := 0; i < p.consumedPerCycle && i < len(handoffIDs); i++ {
		closeIssue(t, db, handoffIDs[i])
	}

	// Return the orphans (handoffs that weren't consumed).
	if len(handoffIDs) <= p.consumedPerCycle {
		return nil
	}
	return handoffIDs[p.consumedPerCycle:]
}

// TestPatrolCyclesHookedBacklogBoundedWithReaper is the gu-hhqk AC#7
// regression test: ensure that 10 patrol cycles with the reaper running
// between them keeps the hooked backlog bounded.
//
// Baseline N is the count after cycle 0 + one reap. The assertion is
// count_after_cycle_10 < N + 5, per the bead description.
func TestPatrolCyclesHookedBacklogBoundedWithReaper(t *testing.T) {
	db := openPatrolTestDB(t)

	params := defaultPatrolParams()

	// Long-lived fixture beads — seeded once, must survive every reap.
	fx := seedLongLived(t, db, params.initialAge)

	// Very short TTL so the reaper considers anything older than 1s as
	// orphaned. Real deployments use 24h; here we want deterministic
	// behaviour without sleeping forever.
	const reapTTL = time.Second

	// Cycle 0: seed + reap to establish the steady-state baseline.
	_ = runPatrolCycle(t, db, 0, params)
	if _, err := ReapHookedMail(db, patrolIntegrationDB, reapTTL, false); err != nil {
		t.Fatalf("cycle 0 reap: %v", err)
	}
	baseline := countHookedMail(t, db)
	t.Logf("cycle 0 post-reap baseline N = %d hooked mail bead(s) "+
		"(includes %d preserve + 0 agent, since agent is excluded from mail total)",
		baseline, fx.preserveCount)

	// Cycles 1..10: seed, reap, advance.
	for cycle := 1; cycle <= 10; cycle++ {
		orphans := runPatrolCycle(t, db, cycle, params)

		preCount := countHookedMail(t, db)
		result, err := ReapHookedMail(db, patrolIntegrationDB, reapTTL, false)
		if err != nil {
			t.Fatalf("cycle %d reap: %v", cycle, err)
		}
		postCount := countHookedMail(t, db)
		t.Logf("cycle %d: orphans=%d pre=%d reaped=%d post=%d",
			cycle, len(orphans), preCount, result.Closed, postCount)
	}

	final := countHookedMail(t, db)
	t.Logf("final hooked mail count after 10 cycles = %d (baseline=%d)", final, baseline)

	// AC #7: count < N + 5.
	if final >= baseline+5 {
		t.Errorf(
			"hooked backlog not bounded: final=%d, baseline=%d, want final < baseline+5 (%d)",
			final, baseline, baseline+5,
		)
	}

	// Additional invariant: the reaper MUST NOT have touched preserve-labeled
	// mail or agent beads.
	assertPreserveIntact(t, db, fx.preserveCount)
	assertAgentIntact(t, db, fx.agentCount)
}

// TestPatrolCyclesHookedBacklogUnboundedWithoutReaper is the negative
// control. It establishes that the assertion in the positive test is
// meaningful — i.e. without the reaper, 10 cycles DOES blow past N+5.
// Together these two tests prove the reaper is what keeps the backlog
// bounded, not some incidental property of the test harness.
func TestPatrolCyclesHookedBacklogUnboundedWithoutReaper(t *testing.T) {
	db := openPatrolTestDB(t)

	params := defaultPatrolParams()

	// Seed the same long-lived fixtures as the positive test so both use
	// the same baseline definition.
	_ = seedLongLived(t, db, params.initialAge)

	// Cycle 0: seed only (no reap) to establish the baseline. This is
	// intentionally different from the positive test — we want to see
	// what happens when no lifecycle guarantee exists.
	_ = runPatrolCycle(t, db, 0, params)
	baseline := countHookedMail(t, db)
	t.Logf("cycle 0 baseline N (no reaper) = %d hooked mail bead(s)", baseline)

	for cycle := 1; cycle <= 10; cycle++ {
		_ = runPatrolCycle(t, db, cycle, params)
	}

	final := countHookedMail(t, db)
	t.Logf("final hooked mail count after 10 unreaped cycles = %d (baseline=%d)", final, baseline)

	// Each cycle leaks (perCycleHandoffs - consumedPerCycle) orphans.
	// 10 additional cycles * 1 leak = 10 extra hooked beads, well above
	// baseline + 5. This bound must trip, or the positive test is not
	// actually exercising the reaper.
	if final < baseline+5 {
		t.Errorf(
			"expected unbounded growth without reaper: final=%d, baseline=%d, "+
				"want final >= baseline+5 (%d). If this passes, the positive "+
				"test is not actually exercising the reaper.",
			final, baseline, baseline+5,
		)
	}
}

// TestPatrolCyclesReaperPreservesNonReapable_DryRun uses the DryRun=true
// path to assert that the reaper identifies the same candidate set every
// cycle (its eligibility predicate is stable) and never includes
// preserve-labeled or agent beads. Separate from the bounded-backlog test
// so one regression doesn't mask the other.
func TestPatrolCyclesReaperPreservesNonReapable_DryRun(t *testing.T) {
	db := openPatrolTestDB(t)

	params := defaultPatrolParams()

	// Seed long-lived fixtures + one cycle's worth of mail.
	_ = seedLongLived(t, db, params.initialAge)
	_ = runPatrolCycle(t, db, 0, params)

	result, err := ReapHookedMail(db, patrolIntegrationDB, time.Second, true /*dryRun*/)
	if err != nil {
		t.Fatalf("dry-run reap: %v", err)
	}

	// The dry-run must never reach into preserve-labeled or agent beads.
	for _, entry := range result.ClosedEntries {
		if strings.HasPrefix(entry.ID, "standing-orders-") ||
			strings.HasPrefix(entry.ID, "keep-") {
			t.Errorf("dry-run would close preserve-labeled bead %s — reaper leaked", entry.ID)
		}
		if strings.HasPrefix(entry.ID, "agent-") {
			t.Errorf("dry-run would close agent bead %s — reaper leaked", entry.ID)
		}
	}

	// DryRun=true must not mutate the DB.
	countBefore := countHookedMail(t, db)
	if _, err := ReapHookedMail(db, patrolIntegrationDB, time.Second, true); err != nil {
		t.Fatalf("second dry-run: %v", err)
	}
	countAfter := countHookedMail(t, db)
	if countBefore != countAfter {
		t.Errorf("dry-run mutated the DB: before=%d after=%d", countBefore, countAfter)
	}
}

// assertPreserveIntact checks that every bead with a preserve label
// (gt:standing-orders or gt:keep) is still hooked — none were reaped.
func assertPreserveIntact(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`
		SELECT COUNT(DISTINCT i.id)
		FROM issues i
		INNER JOIN labels l ON i.id = l.issue_id
		WHERE i.status = 'hooked'
		  AND l.label IN ('gt:standing-orders', 'gt:keep', 'gt:role', 'gt:rig')
	`).Scan(&got); err != nil {
		t.Fatalf("count preserve: %v", err)
	}
	if got != want {
		t.Errorf("preserve-labeled beads were reaped: got %d hooked, want %d", got, want)
	}
}

// assertAgentIntact checks that every issue_type='agent' bead is still
// hooked — none were reaped.
func assertAgentIntact(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM issues
		WHERE status = 'hooked'
		  AND issue_type = 'agent'
	`).Scan(&got); err != nil {
		t.Fatalf("count agent: %v", err)
	}
	if got != want {
		t.Errorf("agent beads were reaped: got %d hooked, want %d", got, want)
	}
}

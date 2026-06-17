package daemon

import (
	"database/sql"
	"io"
	"log"
	"strings"
	"testing"
	"time"
)

// newInlineTestDaemon builds a Daemon wired for inline-reaper unit tests: a
// discard logger and a TownRoot we control. Tests that must avoid touching a
// real Dolt server point the (absent) doltServer at the default port and rely on
// HasReaperSchema failing for an unreachable connection.
func newInlineTestDaemon(townRoot string) *Daemon {
	return &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
	}
}

// noopMol returns a dogMol that no-ops on closeStep/failStep (empty rootID),
// so step bookkeeping never reaches bd during a unit test.
func noopMol() *dogMol {
	return &dogMol{steps: map[string]bool{}, logger: log.New(io.Discard, "", 0)}
}

func TestBuildReapSummaryZeroValue(t *testing.T) {
	got := buildReapSummary(reapTotals{}, 0, false)

	// reaped is always emitted; molecule_steps_closed is omitted when zero.
	if !strings.HasPrefix(got, "wisp_reaper: cycle complete — reaped=0") {
		t.Errorf("summary should start with reaped=0, got %q", got)
	}
	if strings.Contains(got, "molecule_steps_closed=") {
		t.Errorf("molecule_steps_closed should be omitted when zero, got %q", got)
	}
	for _, want := range []string{
		"purged=0", "wisp_flushed=0", "mail_purged=0", "plugin_closed=0",
		"dispatch_closed=0", "hooked_closed=0", "auto_closed=0",
		"open=0", "databases=0", "dryRun=false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q, got %q", want, got)
		}
	}
}

func TestBuildReapSummaryPopulated(t *testing.T) {
	totals := reapTotals{
		reaped:            3,
		moleculeSteps:     2,
		open:              7,
		purged:            5,
		mailPurged:        1,
		wispFlushed:       4,
		pluginClosed:      6,
		dispatchClosed:    8,
		hookedClosed:      9,
		autoClosed:        10,
		reconScanned:      11,
		reconReconciled:   12,
		reconPreservedWIP: 13,
		scrubScanned:      14,
		scrubCleared:      15,
		scrubPreservedWIP: 16,
		scrubStillPending: 17,
		fkScanned:         18,
		fkClearedMRID:     19,
		fkClearedHook:     20,
		fkPreservedWIP:    21,
	}
	got := buildReapSummary(totals, 4, true)

	for _, want := range []string{
		"reaped=3", "molecule_steps_closed=2", "purged=5", "wisp_flushed=4",
		"mail_purged=1", "plugin_closed=6", "dispatch_closed=8", "hooked_closed=9",
		"auto_closed=10", "orphan_recon_scanned=11", "orphan_reconciled=12",
		"orphan_recon_preserved=13", "active_mr_scanned=14", "active_mr_cleared=15",
		"active_mr_preserved=16", "active_mr_pending=17", "dangling_fk_scanned=18",
		"dangling_fk_cleared_mr_id=19", "dangling_fk_cleared_hook=20",
		"dangling_fk_preserved=21", "open=7", "databases=4", "dryRun=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q, got %q", want, got)
		}
	}
}

func TestResolveReapDatabasesConfigured(t *testing.T) {
	d := newInlineTestDaemon(t.TempDir())
	config := &WispReaperConfig{Databases: []string{"hq", "gt"}}

	got, ok := d.resolveReapDatabases(config, noopMol())
	if !ok {
		t.Fatal("expected ok=true for an explicit database list")
	}
	if len(got) != 2 || got[0] != "hq" || got[1] != "gt" {
		t.Errorf("expected configured list [hq gt], got %v", got)
	}
}

func TestResolveReapDatabasesDiscoveryFails(t *testing.T) {
	// No configured databases + an unreachable Dolt server → DiscoverDatabases
	// errors and resolveReapDatabases must fail the cycle (ok=false) rather than
	// silently degrade to a phantom single-DB list (gu-7c2if).
	d := newInlineTestDaemon(t.TempDir())
	d.doltServer = &DoltServerManager{config: &DoltServerConfig{Host: "127.0.0.1", Port: 1}}
	config := &WispReaperConfig{}

	got, ok := d.resolveReapDatabases(config, noopMol())
	if ok {
		t.Errorf("expected ok=false when discovery fails, got databases %v", got)
	}
	if got != nil {
		t.Errorf("expected nil database list on discovery failure, got %v", got)
	}
}

func TestMarkStep(t *testing.T) {
	d := newInlineTestDaemon(t.TempDir())
	// markStep on a no-op mol must not panic for either branch.
	d.markStep(noopMol(), "reap", 0, "reap errors")
	d.markStep(noopMol(), "reap", 3, "reap errors")
}

func TestWithReapableDBsSkipsInvalidAndUnreachable(t *testing.T) {
	d := newInlineTestDaemon(t.TempDir())
	called := 0
	fn := func(_ *sql.DB, _ string) error {
		called++
		return nil
	}

	// "drop table" is an invalid DB name (skipped before OpenDB). "hq" opens a
	// connection to an unreachable port, so HasReaperSchema fails and the DB is
	// skipped without calling fn and without counting an error.
	errs := d.withReapableDBs([]string{"drop table", "hq"}, "127.0.0.1", 1, 1*time.Second, fn)
	if called != 0 {
		t.Errorf("fn should not run for invalid/schema-less DBs, ran %d times", called)
	}
	if errs != 0 {
		t.Errorf("schema-less databases must not count as errors, got %d", errs)
	}
}

func TestStepWrappersAllDatabasesSkipped(t *testing.T) {
	// With every database unreachable, each per-step wrapper should leave its
	// totals untouched and not panic. Exercises the wrapper plumbing without a
	// live Dolt server.
	d := newInlineTestDaemon(t.TempDir())
	databases := []string{"hq"}
	var totals reapTotals

	d.reapStep(&totals, databases, "127.0.0.1", 1, time.Hour, false, noopMol())
	d.purgeStep(&totals, databases, "127.0.0.1", 1, time.Hour, false, noopMol())
	d.flushStep(&totals, databases, "127.0.0.1", 1, false, noopMol())
	d.autoCloseStep(&totals, databases, "127.0.0.1", 1, false, noopMol())

	if totals != (reapTotals{}) {
		t.Errorf("totals should be unchanged when all DBs are skipped, got %+v", totals)
	}
}

func TestTownScrubsSkippedWithoutTownRoot(t *testing.T) {
	// An empty TownRoot must short-circuit all three town-only scrubs without
	// touching beads, leaving totals at zero.
	d := newInlineTestDaemon("")
	var totals reapTotals

	d.reconcileMergedOrphansInline(&totals, false)
	d.scrubActiveMRInline(&totals, false)
	d.scrubDanglingFKInline(&totals, false)

	if totals != (reapTotals{}) {
		t.Errorf("town scrubs must leave totals zero without a town root, got %+v", totals)
	}
}

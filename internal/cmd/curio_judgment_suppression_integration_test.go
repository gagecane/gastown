//go:build integration

// End-to-end proof for gc-e2uvyr.4: judgment-lane re-fire suppression.
//
// The NO-GO note for live curio paging claimed that judgment-lane bumps "would
// re-page the Overseer each cycle without suppression." Code review said the
// suppression already exists, spread across three layers:
//
//	L1  internal/curio/paging.go        — a latched judgment breaker re-emits
//	                                       ActionJudgmentBump every cycle with a
//	                                       STABLE DedupKey "curio:judgment:<keys>".
//	L2  internal/daemon/curio_dog_paging.go:pageOverseer — turns each action into
//	                                       `gt escalate --dedup --signature=<key>`.
//	L3  internal/cmd/escalate_impl.go:runEscalate — on --dedup + --signature, an
//	                                       OPEN match bumps occurrence_count on the
//	                                       existing bead instead of minting a new one.
//
// L1 is proven in internal/curio/paging_test.go
// (TestJudgmentLane_StableDedupKeyAcrossManyBumps). L2's argv contract is proven
// in internal/daemon/curio_dog_paging_dedup_test.go. This file proves the JOINED
// L1→L3 claim against a REAL bd/Dolt backend: drive the real PagingEngine across
// many cycles of an identical judgment cluster set, run the REAL runEscalate
// dedup path for every emitted page, and assert the database ends with exactly
// ONE escalation bead whose occurrence_count equals the number of bumps — i.e.
// ONE Overseer page + N occurrence bumps, NOT N pages.
//
// Run with:
//
//	go test -tags=integration -run TestCurioJudgmentReFireSuppression \
//	  -timeout 5m -count=1 -v ./internal/cmd/
package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/curio"
)

var curioSuppressionCounter atomic.Int32

// TestCurioJudgmentReFireSuppression is the keystone end-to-end test: real
// engine → real runEscalate dedup path → real bd/Dolt. A stable judgment cluster
// set re-firing across many cycles MUST collapse to one escalation bead with
// occurrence_count == (cycles after the trip).
func TestCurioJudgmentReFireSuppression(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping judgment-suppression integration test")
	}
	requireDoltServer(t)

	townRoot, bd := setupCurioSuppressionTown(t)

	// Enter the town so runEscalate's workspace.FindFromCwdOrError() resolves
	// here (it is cwd-based first), and clear env fallbacks that could point
	// elsewhere.
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")
	origWD, _ := os.Getwd()
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir town: %v", err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	// --- L1: drive the real engine through a trip + sustained re-fires. ---
	e := curio.NewPagingEngine()
	now := time.Now()
	// A fixed 3-cluster set: trips on cycle 0 (burst >= 3), then re-fires the
	// same set every subsequent cycle (each a bump, never a new trip).
	clusterSet := []curio.Candidate{
		newSuppressionCand("kill_signal_near_dolt"),
		newSuppressionCand("bead_merged_not_landed"),
		newSuppressionCand("await_signal_timeout"),
	}

	const cycles = 6
	var dedupKey string
	trips, pages := 0, 0
	for i := 0; i < cycles; i++ {
		acts := e.Decide(clusterSet, now.Add(time.Duration(i)*time.Minute))
		for _, a := range acts {
			if a.Lane != curio.LaneJudgment {
				continue
			}
			pages++
			if a.Kind == curio.ActionJudgmentTrip {
				trips++
			}
			if dedupKey == "" {
				dedupKey = a.DedupKey
			}
			// --- L2/L3: run the REAL escalate dedup path for this page, exactly
			// as pageOverseer would (same flags). ---
			runOneCurioEscalate(t, a)
		}
	}

	if trips != 1 {
		t.Fatalf("engine should trip EXACTLY ONCE for a stable cluster set, got %d trips", trips)
	}
	if pages < 2 {
		t.Fatalf("expected a trip + >=1 bump across %d cycles, got %d pages", cycles, pages)
	}
	if dedupKey == "" {
		t.Fatal("no DedupKey captured")
	}

	// --- Assert the DB collapsed all pages into ONE bead with N-1 bumps. ---
	open, fields, err := bd.FindRecentEscalationBySignature(dedupKey, 0 /* open-only */)
	if err != nil {
		t.Fatalf("FindRecentEscalationBySignature: %v", err)
	}
	if open == nil {
		t.Fatalf("expected exactly one OPEN escalation for signature %q, found none", dedupKey)
	}

	// The decisive assertion: one bead, occurrence_count == bumps (pages-1).
	wantOccurrences := pages - 1 // first page creates the bead; the rest bump it
	if fields.OccurrenceCount != wantOccurrences {
		t.Errorf("occurrence_count = %d, want %d (one create + %d bumps) — a mismatch means re-fires are NOT being deduped",
			fields.OccurrenceCount, wantOccurrences, wantOccurrences)
	}

	// Belt-and-suspenders: list ALL escalations and confirm only ONE carries
	// this signature. A second bead would prove the dedup failed even if the
	// occurrence count happened to look right.
	all, err := bd.ListEscalations()
	if err != nil {
		t.Fatalf("ListEscalations: %v", err)
	}
	matches := 0
	for _, iss := range all {
		f := beads.ParseEscalationFields(iss.Description)
		if f.Signature == dedupKey {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("found %d escalation beads with signature %q, want exactly 1 — N pages were NOT collapsed into one",
			matches, dedupKey)
	}

	t.Logf("re-fire suppression PROVEN end-to-end: %d judgment pages (1 trip + %d bumps) "+
		"collapsed into 1 escalation bead %s with occurrence_count=%d. "+
		"The Overseer is paged ONCE; subsequent cycles bump the live counter, not re-notify.",
		pages, pages-1, open.ID, fields.OccurrenceCount)
}

// runOneCurioEscalate invokes the REAL runEscalate exactly as the daemon's
// pageOverseer does for a judgment page: --dedup --signature=<DedupKey>
// --fingerprint=<DedupKey>. It saves/restores the package-level escalate flags
// so it doesn't leak state into other tests.
func runOneCurioEscalate(t *testing.T, a curio.PageAction) {
	t.Helper()

	save := struct {
		sev, reason, source, sig, fp string
		dedup, jsonOut, stdin, dry   bool
		window                       time.Duration
	}{
		escalateSeverity, escalateReason, escalateSource, escalateSignature, escalateFingerprint,
		escalateDedup, escalateJSON, escalateStdin, escalateDryRun, escalateDedupWindow,
	}
	defer func() {
		escalateSeverity, escalateReason, escalateSource, escalateSignature, escalateFingerprint =
			save.sev, save.reason, save.source, save.sig, save.fp
		escalateDedup, escalateJSON, escalateStdin, escalateDryRun, escalateDedupWindow =
			save.dedup, save.jsonOut, save.stdin, save.dry, save.window
	}()

	escalateSeverity = a.Severity
	escalateReason = a.Summary
	escalateSource = "daemon:curio"
	escalateSignature = a.DedupKey
	escalateFingerprint = a.DedupKey
	escalateDedup = true
	escalateJSON = false
	escalateStdin = false
	escalateDryRun = false
	escalateDedupWindow = time.Hour // pageOverseer relies on the 1h default

	// Suppress the command's stdout chatter; failures still surface via t.
	_ = captureStdout(t, func() {
		if err := runEscalate(escalateCmd, []string{"curio: " + a.Summary}); err != nil {
			t.Fatalf("runEscalate: %v", err)
		}
	})
}

// setupCurioSuppressionTown stands up a minimal town with an HQ beads DB on the
// shared Dolt test server and an escalation config whose routes are bead-only
// (no mail/email/sms), so runEscalate exercises the dedup→bead path without
// needing a mail router or external contacts. Returns the town root and a
// *beads.Beads bound to the HQ DB for assertions.
func setupCurioSuppressionTown(t *testing.T) (townRoot string, bd *beads.Beads) {
	t.Helper()

	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	configureTestGitIdentity(t, tmpDir)

	n := curioSuppressionCounter.Add(1)
	hqPrefix := fmt.Sprintf("cjs%d", n)
	townRoot = tmpDir

	// mayor/town.json — the workspace marker FindFromCwdOrError() detects.
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	writeJSONFile(t, filepath.Join(mayorDir, "town.json"), &config.TownConfig{
		Type:    "town",
		Name:    "test",
		Version: config.CurrentTownVersion,
	})

	// town-level .beads with a single HQ route.
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{{Prefix: hqPrefix + "-", Path: "."}}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	initBeadsDBForServer(t, townRoot, hqPrefix)

	t.Cleanup(func() {
		port := testDoltPort(t)
		db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/", port))
		if err != nil {
			t.Logf("cleanup: connect failed: %v", err)
			return
		}
		defer db.Close()
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + hqPrefix + "`"); err != nil {
			t.Logf("cleanup: drop %s failed: %v", hqPrefix, err)
		}
		if _, err := db.Exec("CALL dolt_purge_dropped_databases()"); err != nil {
			t.Logf("cleanup: purge failed: %v", err)
		}
	})

	// Bead-only routing so runEscalate doesn't try to send mail/email/sms.
	escCfg := config.NewEscalationConfig()
	escCfg.Routes = map[string][]string{
		config.SeverityLow:      {"bead"},
		config.SeverityMedium:   {"bead"},
		config.SeverityHigh:     {"bead"},
		config.SeverityCritical: {"bead"},
	}
	if err := config.SaveEscalationConfig(config.EscalationConfigPath(townRoot), escCfg); err != nil {
		t.Fatalf("save escalation config: %v", err)
	}

	return townRoot, beads.New(beads.ResolveBeadsDir(townRoot))
}

// newSuppressionCand builds a judgment-lane candidate (no verifier) for a given
// rule/cluster. Non-rate-spike rules are used deliberately: per the bead's
// ENG-REVIEW note, this backstop must hold for the judgment findings that
// legitimately reach the lane (bead_merged_not_landed, kill_signal_near_dolt),
// independent of the .3 rate-spike threshold calibration.
func newSuppressionCand(rule string) curio.Candidate {
	return curio.Candidate{
		RuleID:      rule,
		Fingerprint: rule,
		StateHash:   rule,
		Summary:     "judgment finding: " + rule,
	}
}

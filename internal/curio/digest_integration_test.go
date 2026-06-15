//go:build !windows

package curio

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/fingerprint"
	"github.com/steveyegge/gastown/internal/testutil"
)

// TestDigest_B0ToB1Seam is review Must-Fix #5: a real B0→B1 integration test
// that crosses the seam against an actual Dolt server. It drives the B0 WRITE
// path (InsertCandidates + InsertLedgerRow at file-time, then SetLedgerOutcome
// at post-close) and then the B1 READ path (ReadOutcomeHistory → RenderDigest),
// asserting the precision table is non-empty and numerically correct. A unit
// golden test with a MOCK outcome history is insufficient — without this, the
// B0-writer/B1-reader seam first runs in production.
func TestDigest_B0ToB1Seam(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("bad container port %q: %v", port, err)
	}
	const db = "gt_test"

	// --- B0 WRITE side: open the write-capable Store (ensures tables exist) ---
	store, err := OpenStore("127.0.0.1", p, db)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Seed candidates (the sidecar the FP-summary join reads). Two alarm_rate_spike
	// candidates and one kill_signal candidate.
	cands := []Candidate{
		newCandidate("w1", "alarm_rate_spike", "sling", "", "sling", 450, `series "sling" rate 450 exceeds threshold 350`),
		newCandidate("w1", "alarm_rate_spike", "done", "", "done", 1400, `series "done" rate 1400 exceeds threshold 1300`),
		newCandidate("w1", "kill_signal_near_dolt", "deacon#0", "", "dog.log.kill_signal", 1, "kill/quit signal near Dolt PID in deacon log"),
	}
	if _, err := store.InsertCandidates(cands); err != nil {
		t.Fatalf("InsertCandidates: %v", err)
	}

	// File-time ledger rows (B0a): one per filed bead, keyed to a candidate's
	// fingerprint so the FP-summary LEFT JOIN resolves.
	type filed struct {
		beadID, ruleID, target, outcome string
	}
	filings := []filed{
		// alarm_rate_spike: 3 judged — 2 false positives, 1 fixed → precision 1/3 = 0.33.
		{"b-1", "alarm_rate_spike", "sling", OutcomeFalsePositive},
		{"b-2", "alarm_rate_spike", "done", OutcomeFalsePositive},
		{"b-3", "alarm_rate_spike", "sling", OutcomeFixed},
		// alarm_rate_spike: one UNKNOWN — must be EXCLUDED from resolved/precision.
		{"b-4", "alarm_rate_spike", "done", OutcomeUnknown},
		// kill_signal_near_dolt: 1 judged, fixed → precision 1.00.
		{"b-5", "kill_signal_near_dolt", "deacon#0", OutcomeFixed},
		// kill_signal_near_dolt: one UNRECONCILED (no outcome yet) — excluded.
		{"b-6", "kill_signal_near_dolt", "deacon#0", ""},
	}
	for _, f := range filings {
		fp := fingerprint.Of(f.ruleID, f.target)
		if err := store.InsertLedgerRow(f.beadID, fp, f.ruleID); err != nil {
			t.Fatalf("InsertLedgerRow %s: %v", f.beadID, err)
		}
		// Post-close reconcile (B0b) for the judged ones.
		if f.outcome != "" {
			if _, err := store.SetLedgerOutcome(f.beadID, f.outcome); err != nil {
				t.Fatalf("SetLedgerOutcome %s: %v", f.beadID, err)
			}
		}
	}

	// --- B1 READ side: open the read-only Reader and cross the seam ---
	reader, err := OpenReader("127.0.0.1", p, db)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = reader.Close() }()

	outcomes, err := reader.ReadOutcomeHistory()
	if err != nil {
		t.Fatalf("ReadOutcomeHistory: %v", err)
	}

	byRule := map[string]RuleOutcome{}
	for _, o := range outcomes {
		byRule[o.RuleID] = o
	}

	// alarm_rate_spike: resolved counts only the 3 JUDGED rows (unknown excluded).
	ars := byRule["alarm_rate_spike"]
	if ars.Resolved != 3 {
		t.Errorf("alarm_rate_spike resolved = %d, want 3 (unknown excluded)", ars.Resolved)
	}
	if ars.FalsePositives != 2 {
		t.Errorf("alarm_rate_spike false_positives = %d, want 2", ars.FalsePositives)
	}
	if ars.Precision != 0.33 {
		t.Errorf("alarm_rate_spike precision = %v, want 0.33", ars.Precision)
	}
	if len(ars.RecentFPSummaries) != 2 {
		t.Errorf("alarm_rate_spike recent FP summaries = %d, want 2", len(ars.RecentFPSummaries))
	}

	// kill_signal_near_dolt: 1 judged fixed (unreconciled excluded), precision 1.00.
	ksd := byRule["kill_signal_near_dolt"]
	if ksd.Resolved != 1 {
		t.Errorf("kill_signal_near_dolt resolved = %d, want 1 (unreconciled excluded)", ksd.Resolved)
	}
	if ksd.FalsePositives != 0 {
		t.Errorf("kill_signal_near_dolt false_positives = %d, want 0", ksd.FalsePositives)
	}
	if ksd.Precision != 1.0 {
		t.Errorf("kill_signal_near_dolt precision = %v, want 1.0", ksd.Precision)
	}

	// --- Render the digest from the real read and assert the seam produced a
	// non-empty, numerically-correct precision table. ---
	closedCands, err := reader.ReadCandidatesBefore(time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("ReadCandidatesBefore: %v", err)
	}
	digest := RenderDigest(time.Now().UTC(), closedCands, outcomes)

	if !strings.Contains(digest, "| alarm_rate_spike | 3 | 0.33 | 2 |") {
		t.Errorf("digest missing correct alarm_rate_spike precision row:\n%s", digest)
	}
	if !strings.Contains(digest, "| kill_signal_near_dolt | 1 | 1.00 | 0 |") {
		t.Errorf("digest missing correct kill_signal_near_dolt precision row:\n%s", digest)
	}

	doc := extractDigestJSON(t, digest)
	if doc.RulesWithPrecision != 2 {
		t.Errorf("rules_with_precision = %d, want 2", doc.RulesWithPrecision)
	}
	if len(doc.Rules) == 0 {
		t.Fatal("precision table is empty — the B0→B1 seam produced no rule rows")
	}
}

// TestReadJudgedFingerprints_OnlyJudged is the gu-1whne lever-B read invariant
// against a real Dolt server: ReadJudgedFingerprints returns ONLY fingerprints
// whose ledger row has a judged outcome (fixed/false_positive/duplicate/
// deferred) — never an 'unknown' or unreconciled (empty-outcome) row.
func TestReadJudgedFingerprints_OnlyJudged(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("bad container port %q: %v", port, err)
	}
	const db = "gt_test"

	store, err := OpenStore("127.0.0.1", p, db)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	type filed struct {
		beadID, ruleID, target, outcome string
	}
	filings := []filed{
		{"b-1", "alarm_rate_spike", "sling", OutcomeFixed},           // judged
		{"b-2", "alarm_rate_spike", "done", OutcomeFalsePositive},    // judged
		{"b-3", "kill_signal_near_dolt", "deacon#0", OutcomeUnknown}, // NOT judged
		{"b-4", "dead_owner_admission", "res#9", ""},                 // unreconciled, NOT judged
	}
	wantJudged := map[string]struct{}{}
	for _, f := range filings {
		fp := fingerprint.Of(f.ruleID, f.target)
		if err := store.InsertLedgerRow(f.beadID, fp, f.ruleID); err != nil {
			t.Fatalf("InsertLedgerRow %s: %v", f.beadID, err)
		}
		if f.outcome != "" {
			if _, err := store.SetLedgerOutcome(f.beadID, f.outcome); err != nil {
				t.Fatalf("SetLedgerOutcome %s: %v", f.beadID, err)
			}
		}
		if f.outcome == OutcomeFixed || f.outcome == OutcomeFalsePositive ||
			f.outcome == OutcomeDuplicate || f.outcome == OutcomeDeferred {
			wantJudged[fp] = struct{}{}
		}
	}

	reader, err := OpenReader("127.0.0.1", p, db)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = reader.Close() }()

	got, err := reader.ReadJudgedFingerprints()
	if err != nil {
		t.Fatalf("ReadJudgedFingerprints: %v", err)
	}
	if len(got) != len(wantJudged) {
		t.Fatalf("judged fingerprints = %d, want %d\ngot: %v\nwant: %v", len(got), len(wantJudged), got, wantJudged)
	}
	for fp := range wantJudged {
		if _, ok := got[fp]; !ok {
			t.Errorf("expected judged fingerprint %q missing from result", fp)
		}
	}
	// The unknown/unreconciled fingerprints must be ABSENT.
	for _, f := range []filed{{ruleID: "kill_signal_near_dolt", target: "deacon#0"}, {ruleID: "dead_owner_admission", target: "res#9"}} {
		if _, ok := got[fingerprint.Of(f.ruleID, f.target)]; ok {
			t.Errorf("non-judged fingerprint for (%s,%s) must not be returned", f.ruleID, f.target)
		}
	}
}

// TestReadCandidatesBetween_BoundsWindow is the gu-1whne lever-A read invariant
// against a real Dolt server: ReadCandidatesBetween returns only rows in
// [start, cutoff), excluding both too-old (before start) and in-flight (>=cutoff)
// candidates, while ReadCandidatesBefore (zero start) still returns the full tail.
func TestReadCandidatesBetween_BoundsWindow(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("bad container port %q: %v", port, err)
	}
	const db = "gt_test"

	store, err := OpenStore("127.0.0.1", p, db)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Insert three candidates, then back-date their created_at to control the
	// window membership deterministically (created_at defaults to now on INSERT).
	cands := []Candidate{
		newCandidate("w-old", "alarm_rate_spike", "old", "", "old", 1, "TOO-OLD"),
		newCandidate("w-mid", "alarm_rate_spike", "mid", "", "mid", 2, "IN-WINDOW"),
		newCandidate("w-new", "alarm_rate_spike", "new", "", "new", 3, "IN-FLIGHT"),
	}
	if _, err := store.InsertCandidates(cands); err != nil {
		t.Fatalf("InsertCandidates: %v", err)
	}

	base := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)
	// old: 10 days before base; mid: 1 day before base; new: 1 hour after base.
	stamps := map[string]time.Time{
		fingerprint.Of("alarm_rate_spike", "old"): base.Add(-10 * 24 * time.Hour),
		fingerprint.Of("alarm_rate_spike", "mid"): base.Add(-24 * time.Hour),
		fingerprint.Of("alarm_rate_spike", "new"): base.Add(time.Hour),
	}
	for fp, ts := range stamps {
		if err := backdateCandidate(t, store, fp, ts); err != nil {
			t.Fatalf("backdate %s: %v", fp, err)
		}
	}

	reader, err := OpenReader("127.0.0.1", p, db)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = reader.Close() }()

	// cutoff at base, lookback 72h → window [base-72h, base): only "mid" qualifies.
	cutoff := base
	start := cutoff.Add(-72 * time.Hour)
	got, err := reader.ReadCandidatesBetween(start, cutoff)
	if err != nil {
		t.Fatalf("ReadCandidatesBetween: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "IN-WINDOW" {
		t.Fatalf("bounded window = %+v, want exactly the IN-WINDOW candidate", got)
	}

	// Zero start (ReadCandidatesBefore) keeps the legacy unbounded tail: old + mid
	// (both before cutoff), but still excludes the in-flight "new" (>= cutoff).
	unbounded, err := reader.ReadCandidatesBefore(cutoff)
	if err != nil {
		t.Fatalf("ReadCandidatesBefore: %v", err)
	}
	if len(unbounded) != 2 {
		t.Fatalf("unbounded read = %d candidates, want 2 (old+mid, in-flight excluded)\ngot: %+v", len(unbounded), unbounded)
	}
}

// backdateCandidate sets a candidate row's created_at directly. It is a
// TEST-ONLY write through the write-capable Store's connection (the production
// Reader has no such method), used to make window-membership deterministic.
func backdateCandidate(t *testing.T, store *Store, fingerprint string, ts time.Time) error {
	t.Helper()
	_, err := store.db.Exec(
		"UPDATE "+candidateTable+" SET created_at = ? WHERE fingerprint = ?",
		ts.UTC(), fingerprint)
	return err
}

package daemon

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/curio"
)

// fakeLedger is an in-memory candidateLedger for filing tests. It records every
// InsertLedgerRow call and lets a test seed already-filed fingerprints or inject
// errors.
type fakeLedger struct {
	filedFPs  map[string]bool // fingerprints already in the ledger
	inserts   []ledgerInsert  // every InsertLedgerRow call, in order
	lookupErr error           // returned by FingerprintFiled when set
	insertErr error           // returned by InsertLedgerRow when set
}

type ledgerInsert struct {
	beadID, fingerprint, ruleID string
}

func (f *fakeLedger) FingerprintFiled(fp string) (bool, error) {
	if f.lookupErr != nil {
		return false, f.lookupErr
	}
	return f.filedFPs[fp], nil
}

func (f *fakeLedger) InsertLedgerRow(beadID, fp, ruleID string) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserts = append(f.inserts, ledgerInsert{beadID, fp, ruleID})
	if f.filedFPs == nil {
		f.filedFPs = map[string]bool{}
	}
	f.filedFPs[fp] = true
	return nil
}

// fakeFiler is an in-memory candidateFiler. It hands out sequential bead IDs and
// records the candidates it was asked to file.
type fakeFiler struct {
	filed   []curio.Candidate
	nextID  int
	fileErr error
}

func (f *fakeFiler) FileCurioBead(c curio.Candidate) (string, error) {
	if f.fileErr != nil {
		return "", f.fileErr
	}
	f.filed = append(f.filed, c)
	f.nextID++
	return beadIDForTest(f.nextID), nil
}

func beadIDForTest(n int) string {
	return "gu-test-" + string(rune('0'+n))
}

func cand(ruleID, fingerprint string) curio.Candidate {
	return curio.Candidate{RuleID: ruleID, Fingerprint: fingerprint, Series: "done", Observed: 1500}
}

func discard(string, ...interface{}) {}

// TestFileTunedCandidates_InsertsLedgerRowOnFile is the B0a acceptance test: a
// tuned-rule candidate→bead file MUST fire the file-time ledger insert with the
// correct bead_id / fingerprint / rule_id and outcome left empty (the row's
// outcome column defaults to ”).
func TestFileTunedCandidates_InsertsLedgerRowOnFile(t *testing.T) {
	filer := &fakeFiler{}
	ledger := &fakeLedger{}

	c := cand("alarm_rate_spike", "abc123def456")
	filed, err := fileTunedCandidates([]curio.Candidate{c}, filer, ledger, discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filed != 1 {
		t.Fatalf("filed = %d, want 1", filed)
	}
	if len(filer.filed) != 1 {
		t.Fatalf("filer saw %d candidates, want 1", len(filer.filed))
	}
	if len(ledger.inserts) != 1 {
		t.Fatalf("ledger saw %d inserts, want 1", len(ledger.inserts))
	}
	got := ledger.inserts[0]
	if got.fingerprint != c.Fingerprint || got.ruleID != c.RuleID {
		t.Errorf("ledger insert = %+v, want fingerprint=%s rule=%s", got, c.Fingerprint, c.RuleID)
	}
	if got.beadID == "" {
		t.Error("ledger row written with empty bead_id")
	}
}

// TestFileTunedCandidates_SkipsNonTunedRules proves only tuned rules are filed —
// other rules stay candidates-only (the filing blast radius is the allowlist).
func TestFileTunedCandidates_SkipsNonTunedRules(t *testing.T) {
	filer := &fakeFiler{}
	ledger := &fakeLedger{}

	cands := []curio.Candidate{
		cand("dead_owner_admission", "fp-dead"),
		cand("bead_merged_not_landed", "fp-merged"),
		cand("kill_signal_near_dolt", "fp-kill"),
	}
	filed, err := fileTunedCandidates(cands, filer, ledger, discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filed != 0 {
		t.Errorf("filed = %d, want 0 (no tuned rules)", filed)
	}
	if len(filer.filed) != 0 || len(ledger.inserts) != 0 {
		t.Errorf("non-tuned rule produced a file (%d) or ledger row (%d)", len(filer.filed), len(ledger.inserts))
	}
}

// TestFileTunedCandidates_FileOnceDedup proves a fingerprint already in the
// ledger is not re-filed — the ledger row persists through bead close, so a
// finding is filed at most once.
func TestFileTunedCandidates_FileOnceDedup(t *testing.T) {
	filer := &fakeFiler{}
	ledger := &fakeLedger{filedFPs: map[string]bool{"already-filed": true}}

	cands := []curio.Candidate{
		cand("alarm_rate_spike", "already-filed"),
		cand("alarm_rate_spike", "fresh-one"),
	}
	filed, err := fileTunedCandidates(cands, filer, ledger, discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filed != 1 {
		t.Fatalf("filed = %d, want 1 (only the fresh fingerprint)", filed)
	}
	if len(filer.filed) != 1 || filer.filed[0].Fingerprint != "fresh-one" {
		t.Errorf("expected only fresh-one filed, got %+v", filer.filed)
	}
}

// TestFileTunedCandidates_NoLedgerRowOnFileFailure proves the ledger row is NOT
// written when the bead file itself fails (no orphan ledger rows pointing at a
// bead that was never created).
func TestFileTunedCandidates_NoLedgerRowOnFileFailure(t *testing.T) {
	filer := &fakeFiler{fileErr: errors.New("bd create boom")}
	ledger := &fakeLedger{}

	filed, err := fileTunedCandidates([]curio.Candidate{cand("alarm_rate_spike", "fp")}, filer, ledger, discard)
	if filed != 0 {
		t.Errorf("filed = %d, want 0 on file failure", filed)
	}
	if err == nil {
		t.Error("expected the file error to surface")
	}
	if len(ledger.inserts) != 0 {
		t.Errorf("ledger row written despite file failure: %+v", ledger.inserts)
	}
}

// TestFileTunedCandidates_BestEffortContinuesAfterError proves one bad candidate
// does not abort the cycle: a later good candidate is still filed, and the first
// error is surfaced for observability.
func TestFileTunedCandidates_BestEffortContinuesAfterError(t *testing.T) {
	filer := &fakeFiler{}
	// Lookup fails for the first candidate only by injecting a one-shot error.
	ledger := &errOnceLedger{fakeLedger: fakeLedger{}}

	cands := []curio.Candidate{
		cand("alarm_rate_spike", "first-errors"),
		cand("alarm_rate_spike", "second-ok"),
	}
	filed, err := fileTunedCandidates(cands, filer, ledger, discard)
	if filed != 1 {
		t.Errorf("filed = %d, want 1 (second candidate survives)", filed)
	}
	if err == nil {
		t.Error("expected the first candidate's error to be surfaced")
	}
}

// errOnceLedger returns a lookup error on its first FingerprintFiled call, then
// behaves like its embedded fakeLedger.
type errOnceLedger struct {
	fakeLedger
	calls int
}

func (e *errOnceLedger) FingerprintFiled(fp string) (bool, error) {
	e.calls++
	if e.calls == 1 {
		return false, errors.New("transient ledger lookup error")
	}
	return e.fakeLedger.FingerprintFiled(fp)
}

// TestCurioFileTunedRules_DefaultOff proves the filing gate defaults to OFF
// (candidates-only) for nil / absent / explicitly-false config — the Patrol's
// prior posture is preserved unless the operator deliberately enables filing.
func TestCurioFileTunedRules_DefaultOff(t *testing.T) {
	cases := []struct {
		name string
		cfg  *DaemonPatrolConfig
		want bool
	}{
		{"nil config", nil, false},
		{"absent curio block", &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}, false},
		{"explicitly false", &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{FileTunedRules: false}}}, false},
		{"enabled", &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{FileTunedRules: true}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Daemon{patrolConfig: tc.cfg}
			if got := d.curioFileTunedRules(); got != tc.want {
				t.Errorf("curioFileTunedRules() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsTunedRule(t *testing.T) {
	if !isTunedRule("alarm_rate_spike") {
		t.Error("alarm_rate_spike must be tuned (file-enabled)")
	}
	for _, r := range []string{"dead_owner_admission", "bead_merged_not_landed", "kill_signal_near_dolt", ""} {
		if isTunedRule(r) {
			t.Errorf("%q must not be tuned (file-enabled)", r)
		}
	}
}

package daemon

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/curio"
)

// TestEventPoll_InvokesOnBeadClose is the B0b wiring assertion: a real bead-close
// EVENT flowing through the daemon's canonical close-event stream
// (ConvoyManager.pollStore) must invoke the registered onBeadClose callback with
// the closed issue's ID — proving the reconciler is wired to the event path, not
// merely callable directly (acceptance: "a real bead-close EVENT invokes the
// reconciler"). It also asserts dedup: the callback fires at most once per close.
func TestEventPoll_InvokesOnBeadClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	issue := &beadsdk.Issue{
		ID:        "test-recon1",
		Title:     "Curio bead to close",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "merged in abc123", "test", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	var mu sync.Mutex
	var closes []string
	m := NewConvoyManager(t.TempDir(), func(string, ...interface{}) {}, "gt",
		10*time.Minute, map[string]beadsdk.Storage{"hq": store}, nil, nil)
	m.onBeadClose = func(id string) {
		mu.Lock()
		defer mu.Unlock()
		closes = append(closes, id)
	}
	m.seeded.Store(true)

	// Poll twice: the second poll's cross-cycle dedup must NOT re-fire the
	// callback for the same close.
	m.pollStoresSnapshot(m.stores)
	m.pollStoresSnapshot(m.stores)

	mu.Lock()
	defer mu.Unlock()
	if len(closes) != 1 {
		t.Fatalf("onBeadClose fired %d times (%v), want exactly 1", len(closes), closes)
	}
	if closes[0] != issue.ID {
		t.Errorf("onBeadClose got id %q, want %q", closes[0], issue.ID)
	}
}

// fakeLedgerReconciler is a test double for ledgerReconciler. has controls the
// membership check; setCalls records every SetLedgerOutcome call.
type fakeLedgerReconciler struct {
	has        map[string]bool
	hasErr     error
	setErr     error
	setCalls   []fakeSetCall
	updatedFor map[string]bool // bead_id -> whether SetLedgerOutcome reports an updated row
}

type fakeSetCall struct {
	beadID  string
	outcome string
}

func (f *fakeLedgerReconciler) LedgerHasBead(beadID string) (bool, error) {
	if f.hasErr != nil {
		return false, f.hasErr
	}
	return f.has[beadID], nil
}

func (f *fakeLedgerReconciler) SetLedgerOutcome(beadID, outcome string) (bool, error) {
	f.setCalls = append(f.setCalls, fakeSetCall{beadID: beadID, outcome: outcome})
	if f.setErr != nil {
		return false, f.setErr
	}
	if f.updatedFor != nil {
		return f.updatedFor[beadID], nil
	}
	return true, nil
}

// fakeCloseSignalReader is a test double for closeSignalReader.
type fakeCloseSignalReader struct {
	reason string
	labels []string
	err    error
	calls  int
}

func (f *fakeCloseSignalReader) CloseSignals(beadID string) (string, []string, error) {
	f.calls++
	return f.reason, f.labels, f.err
}

func discardLog(string, ...interface{}) {}

// TestReconcileCurioLedgerClose_NotInLedgerIsNoOp asserts closing a bead that is
// NOT in the ledger does nothing: no signal read, no outcome write, no error.
func TestReconcileCurioLedgerClose_NotInLedgerIsNoOp(t *testing.T) {
	ledger := &fakeLedgerReconciler{has: map[string]bool{}}
	reader := &fakeCloseSignalReader{reason: "merged"}

	reconciled, outcome, err := reconcileCurioLedgerClose("gt-not-curio", ledger, reader, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reconciled {
		t.Errorf("reconciled = true, want false for a non-ledger bead")
	}
	if outcome != "" {
		t.Errorf("outcome = %q, want empty for a non-ledger bead", outcome)
	}
	if len(ledger.setCalls) != 0 {
		t.Errorf("SetLedgerOutcome called %d times, want 0 for a non-ledger bead", len(ledger.setCalls))
	}
	if reader.calls != 0 {
		t.Errorf("CloseSignals called %d times, want 0 (membership check must short-circuit)", reader.calls)
	}
}

// TestReconcileCurioLedgerClose_InLedgerClassifiesAndWrites asserts a close for
// a ledger bead reads its signals, classifies the outcome, and writes it.
func TestReconcileCurioLedgerClose_InLedgerClassifiesAndWrites(t *testing.T) {
	ledger := &fakeLedgerReconciler{has: map[string]bool{"gt-curio1": true}}
	reader := &fakeCloseSignalReader{reason: "merged in abc123"}

	reconciled, outcome, err := reconcileCurioLedgerClose("gt-curio1", ledger, reader, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reconciled {
		t.Errorf("reconciled = false, want true")
	}
	if outcome != curio.OutcomeFixed {
		t.Errorf("outcome = %q, want %q", outcome, curio.OutcomeFixed)
	}
	if len(ledger.setCalls) != 1 {
		t.Fatalf("SetLedgerOutcome called %d times, want 1", len(ledger.setCalls))
	}
	if got := ledger.setCalls[0]; got.beadID != "gt-curio1" || got.outcome != curio.OutcomeFixed {
		t.Errorf("SetLedgerOutcome(%q, %q), want (gt-curio1, %q)", got.beadID, got.outcome, curio.OutcomeFixed)
	}
}

// TestReconcileCurioLedgerClose_AmbiguousWritesUnknown is the Must-Fix #2 guard
// at the reconciler boundary: an ambiguous close reason persists 'unknown', not
// 'fixed'.
func TestReconcileCurioLedgerClose_AmbiguousWritesUnknown(t *testing.T) {
	ledger := &fakeLedgerReconciler{has: map[string]bool{"gt-curio2": true}}
	reader := &fakeCloseSignalReader{reason: "done"}

	_, outcome, err := reconcileCurioLedgerClose("gt-curio2", ledger, reader, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != curio.OutcomeUnknown {
		t.Errorf("outcome = %q, want %q", outcome, curio.OutcomeUnknown)
	}
	if got := ledger.setCalls[0].outcome; got != curio.OutcomeUnknown {
		t.Errorf("persisted outcome = %q, want %q (ambiguous must not be fixed)", got, curio.OutcomeUnknown)
	}
}

// TestReconcileCurioLedgerClose_StructuredLabelWins asserts the structured
// close-label is honored end-to-end through the reconciler.
func TestReconcileCurioLedgerClose_StructuredLabelWins(t *testing.T) {
	ledger := &fakeLedgerReconciler{has: map[string]bool{"gt-curio3": true}}
	reader := &fakeCloseSignalReader{reason: "merged", labels: []string{"curio-outcome:fp"}}

	_, outcome, err := reconcileCurioLedgerClose("gt-curio3", ledger, reader, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != curio.OutcomeFalsePositive {
		t.Errorf("outcome = %q, want %q (label must win over free text)", outcome, curio.OutcomeFalsePositive)
	}
}

// TestReconcileCurioLedgerClose_MembershipErrorSurfaces asserts a membership
// lookup error is returned and stops the reconcile (no signal read, no write).
func TestReconcileCurioLedgerClose_MembershipErrorSurfaces(t *testing.T) {
	wantErr := errors.New("dolt down")
	ledger := &fakeLedgerReconciler{hasErr: wantErr}
	reader := &fakeCloseSignalReader{}

	_, _, err := reconcileCurioLedgerClose("gt-x", ledger, reader, discardLog)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if reader.calls != 0 || len(ledger.setCalls) != 0 {
		t.Errorf("membership error must short-circuit before read/write")
	}
}

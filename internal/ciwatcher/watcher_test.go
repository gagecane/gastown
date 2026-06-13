package ciwatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeFetcher is a static RunFetcher for tests.
type fakeFetcher struct {
	runs []CIRun
	err  error
}

func (f *fakeFetcher) CompletedRuns(_ context.Context, _ string, _ int) ([]CIRun, error) {
	return f.runs, f.err
}

// fakeBeads records mutations and lets tests pre-stub Exists/Reopen errors.
type fakeBeads struct {
	known       map[string]bool // bead IDs that exist
	reopens     []string
	labels      []string // "<id>::<label>"
	notes       []string // "<id>::<note>"
	reopenErr   error
	addLabelErr error
	existsErr   error
	noteErr     error
}

func newFakeBeads(existing ...string) *fakeBeads {
	fb := &fakeBeads{known: map[string]bool{}}
	for _, id := range existing {
		fb.known[id] = true
	}
	return fb
}

func (f *fakeBeads) Reopen(beadID string) error {
	if f.reopenErr != nil {
		return f.reopenErr
	}
	f.reopens = append(f.reopens, beadID)
	return nil
}

func (f *fakeBeads) AddLabel(beadID, label string) error {
	if f.addLabelErr != nil {
		return f.addLabelErr
	}
	f.labels = append(f.labels, beadID+"::"+label)
	return nil
}

func (f *fakeBeads) AppendNote(beadID, note string) error {
	if f.noteErr != nil {
		return f.noteErr
	}
	f.notes = append(f.notes, beadID+"::"+note)
	return nil
}

func (f *fakeBeads) Exists(beadID string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.known[beadID], nil
}

// fakeMailer captures sent mail.
type fakeMailer struct {
	sent []struct{ Subject, Body string }
	err  error
}

func (m *fakeMailer) SendMayor(subject, body string) error {
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, struct{ Subject, Body string }{subject, body})
	return nil
}

// fixedClock returns a deterministic time.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func newWatcher(t *testing.T, town string, runs []CIRun, fetchErr error, fb *fakeBeads, fm *fakeMailer) (*Watcher, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	cfg := Config{TownRoot: town, Rig: "alpha", TargetBranch: "main"}
	clock := fixedClock{t: time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)}
	return NewWatcher(cfg, &fakeFetcher{runs: runs, err: fetchErr}, fb, fm, clock, buf), buf
}

func TestWatcherFreezesOnFailedRun(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	runs := []CIRun{
		{
			ID:                "100",
			HeadSHA:           "deadbeefcafe",
			HeadCommitSubject: "fix(refinery): handle slot timeout (gu-aaa)",
			Conclusion:        ConclusionFailure,
			Branch:            "main",
			URL:               "https://example.test/run/100",
			Workflow:          "build",
		},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.FailuresHandled != 1 {
		t.Errorf("FailuresHandled = %d, want 1", res.FailuresHandled)
	}
	if !res.FreezeWritten {
		t.Errorf("FreezeWritten = false")
	}
	if len(fb.reopens) != 1 || fb.reopens[0] != "gu-aaa" {
		t.Errorf("reopens = %v", fb.reopens)
	}
	wantLabel := "gu-aaa::" + LabelBrokeMainCI
	if len(fb.labels) != 1 || fb.labels[0] != wantLabel {
		t.Errorf("labels = %v, want [%s]", fb.labels, wantLabel)
	}
	if len(fm.sent) != 1 {
		t.Fatalf("expected 1 mail, got %d", len(fm.sent))
	}
	if !strings.Contains(fm.sent[0].Subject, "[HIGH]") || !strings.Contains(fm.sent[0].Subject, "gu-aaa") {
		t.Errorf("subject = %q lacks [HIGH] or bead", fm.sent[0].Subject)
	}
	frozen, err := IsFrozen(town, "alpha")
	if err != nil || !frozen {
		t.Errorf("freeze not written: frozen=%v err=%v", frozen, err)
	}
	ff, _ := ReadFreeze(town, "alpha")
	if ff == nil || ff.BeadID != "gu-aaa" || ff.RunID != "100" {
		t.Errorf("freeze contents wrong: %+v", ff)
	}
}

func TestWatcherIdempotentSeenRuns(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	runs := []CIRun{
		{ID: "100", HeadSHA: "abc", HeadCommitSubject: "fix: thing (gu-aaa)", Conclusion: ConclusionFailure, Branch: "main", URL: "u"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	if _, err := w.Process(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Second run with the same data should be a no-op.
	res2, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res2.RunsProcessed != 0 {
		t.Errorf("second poll RunsProcessed = %d, want 0", res2.RunsProcessed)
	}
	if len(fb.reopens) != 1 {
		t.Errorf("reopens called twice: %v", fb.reopens)
	}
	if len(fm.sent) != 1 {
		t.Errorf("mail sent twice: %v", fm.sent)
	}
}

func TestWatcherClearsFreezeOnSuccess(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	// Pre-existing freeze.
	if err := WriteFreeze(town, FreezeFile{Rig: "alpha", BeadID: "gu-aaa", Reason: "stale freeze"}); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		{ID: "200", HeadSHA: "feedface", HeadCommitSubject: "ok (gu-bbb)", Conclusion: ConclusionSuccess, Branch: "main", URL: "u"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.FreezeCleared {
		t.Errorf("FreezeCleared = false")
	}
	frozen, _ := IsFrozen(town, "alpha")
	if frozen {
		t.Errorf("freeze still present after success")
	}
	if len(fm.sent) != 1 || !strings.Contains(fm.sent[0].Subject, "CLEARED") {
		t.Errorf("expected CLEARED mail, got %v", fm.sent)
	}
}

func TestWatcherFailThenPassWithinSinglePoll(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	// Fetcher returns newest-first: pass arrives after fail in real time.
	runs := []CIRun{
		{ID: "201", HeadSHA: "feedfff", HeadCommitSubject: "ok (gu-bbb)", Conclusion: ConclusionSuccess, Branch: "main", URL: "u201"},
		{ID: "200", HeadSHA: "deadbee", HeadCommitSubject: "fix (gu-aaa)", Conclusion: ConclusionFailure, Branch: "main", URL: "u200"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.FreezeWritten {
		t.Errorf("FreezeWritten = false")
	}
	if !res.FreezeCleared {
		t.Errorf("FreezeCleared = false")
	}
	frozen, _ := IsFrozen(town, "alpha")
	if frozen {
		t.Errorf("freeze should be cleared after pass following fail")
	}
}

func TestWatcherCancelledIsNoOp(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads()
	fm := &fakeMailer{}
	runs := []CIRun{
		{ID: "300", HeadSHA: "x", HeadCommitSubject: "(gu-aaa)", Conclusion: ConclusionCancelled, Branch: "main"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.FailuresHandled != 0 || res.FreezeWritten || res.FreezeCleared {
		t.Errorf("cancelled run should be a no-op, got %+v", res)
	}
	if len(fm.sent) != 0 {
		t.Errorf("no mail expected, got %v", fm.sent)
	}
	frozen, _ := IsFrozen(town, "alpha")
	if frozen {
		t.Errorf("cancelled run should not freeze")
	}
}

func TestWatcherUnknownBeadFreezesWithoutReopen(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads() // no known beads
	fm := &fakeMailer{}
	runs := []CIRun{
		{ID: "400", HeadSHA: "ff00", HeadCommitSubject: "fix (gu-ghost)", Conclusion: ConclusionFailure, Branch: "main", URL: "u"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.FailuresHandled != 1 || !res.FreezeWritten {
		t.Errorf("freeze should still happen: %+v", res)
	}
	if len(fb.reopens) != 0 {
		t.Errorf("should not reopen unknown bead, got %v", fb.reopens)
	}
	if len(fm.sent) != 1 {
		t.Fatalf("expected mail, got %d", len(fm.sent))
	}
	if !strings.Contains(fm.sent[0].Subject, "unknown bead") {
		t.Errorf("expected 'unknown bead' subject, got %q", fm.sent[0].Subject)
	}
	ff, _ := ReadFreeze(town, "alpha")
	if ff == nil || ff.BeadID != "" {
		t.Errorf("freeze should have empty BeadID, got %+v", ff)
	}
}

func TestWatcherNoBeadInSubject(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads()
	fm := &fakeMailer{}
	runs := []CIRun{
		{ID: "500", HeadSHA: "ab", HeadCommitSubject: "WIP no bead", Conclusion: ConclusionFailure, Branch: "main", URL: "u"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.FreezeWritten {
		t.Errorf("freeze must still happen when no bead can be extracted")
	}
	if len(fb.reopens) != 0 {
		t.Errorf("no reopen expected: %v", fb.reopens)
	}
}

func TestWatcherFiltersOtherBranches(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	runs := []CIRun{
		{ID: "600", HeadSHA: "ab", HeadCommitSubject: "(gu-aaa)", Conclusion: ConclusionFailure, Branch: "feature-x", URL: "u"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.RunsProcessed != 0 || res.FreezeWritten {
		t.Errorf("non-main runs should be filtered: %+v", res)
	}
}

func TestWatcherFetchError(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads()
	fm := &fakeMailer{}
	w, _ := newWatcher(t, town, nil, errors.New("boom"), fb, fm)
	if _, err := w.Process(context.Background()); err == nil {
		t.Fatal("expected error on fetch failure")
	}
}

// TestWatcherSkipsUnavailableRuns is the gu-qfhvw regression: a rig whose
// repo does not exist or has Actions disabled (HTTP 404 on the runs endpoint)
// must be reported as a clean skip — Process returns no error so the poll
// plugin records a success receipt instead of failing every cooldown cycle.
func TestWatcherSkipsUnavailableRuns(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads()
	fm := &fakeMailer{}
	fetchErr := fmt.Errorf("%w (repo=owner/missing, stderr: HTTP 404)", ErrRunsUnavailable)
	w, buf := newWatcher(t, town, nil, fetchErr, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("404 should be a clean skip, got error: %v", err)
	}
	if !res.Skipped {
		t.Errorf("expected res.Skipped=true, got %+v", res)
	}
	if res.FreezeWritten || len(fb.reopens) != 0 {
		t.Errorf("a skip must not freeze or reopen: %+v reopens=%v", res, fb.reopens)
	}
	if !strings.Contains(buf.String(), "skipped") {
		t.Errorf("expected a skip log line, got: %q", buf.String())
	}
}

// TestWatcherColdStartSuppressesStaleFailures is the gs-qth regression: on the
// first-ever poll (no seen-runs ledger), historical failures older than the
// cold-start lookback must be recorded as seen but NOT escalated, while a
// genuinely-recent break still escalates promptly.
func TestWatcherColdStartSuppressesStaleFailures(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-old", "gu-new")
	fm := &fakeMailer{}
	now := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)
	runs := []CIRun{
		// Recent failure (10m ago) — must escalate.
		{ID: "910", HeadSHA: "newsha", HeadCommitSubject: "fix (gu-new)", Conclusion: ConclusionFailure, Branch: "main", URL: "u910", CompletedAt: now.Add(-10 * time.Minute)},
		// Stale failures (well beyond the 2h lookback) — must be suppressed.
		{ID: "900", HeadSHA: "oldsha", HeadCommitSubject: "fix (gu-old)", Conclusion: ConclusionFailure, Branch: "main", URL: "u900", CompletedAt: now.Add(-72 * time.Hour)},
		{ID: "899", HeadSHA: "oldsha2", HeadCommitSubject: "fix (gu-old)", Conclusion: ConclusionFailure, Branch: "main", URL: "u899", CompletedAt: now.Add(-100 * time.Hour)},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.ColdStartSuppressed != 2 {
		t.Errorf("ColdStartSuppressed = %d, want 2", res.ColdStartSuppressed)
	}
	if res.FailuresHandled != 1 {
		t.Errorf("FailuresHandled = %d, want 1 (only the recent break)", res.FailuresHandled)
	}
	// Only the recent break should mail the mayor — no flood.
	if len(fm.sent) != 1 {
		t.Fatalf("expected 1 mail (recent break only), got %d: %v", len(fm.sent), fm.sent)
	}
	if !strings.Contains(fm.sent[0].Subject, "gu-new") {
		t.Errorf("mail should be for the recent break, got %q", fm.sent[0].Subject)
	}
	if len(fb.reopens) != 1 || fb.reopens[0] != "gu-new" {
		t.Errorf("only the recent bead should reopen, got %v", fb.reopens)
	}
	// All three runs must be marked seen so a warm re-poll is a no-op.
	seen, _ := LoadSeenRuns(town, "alpha")
	for _, id := range []string{"910", "900", "899"} {
		if !seen.Has(id) {
			t.Errorf("run %s should be marked seen after cold start", id)
		}
	}
	if seen.Fresh() {
		t.Errorf("ledger should no longer be Fresh after a save")
	}
}

// TestWatcherWarmPollEscalatesOldFailure verifies the cold-start cutoff only
// applies on the first poll: once a ledger exists, an unseen failure older
// than the lookback (e.g. one that completed during a daemon downtime gap)
// still escalates.
func TestWatcherWarmPollEscalatesOldFailure(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	now := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)
	// Seed a non-empty ledger so this is a warm start (not Fresh).
	seed, _ := LoadSeenRuns(town, "alpha")
	seed.Mark("seed-run", now.Add(-24*time.Hour))
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		{ID: "950", HeadSHA: "ab", HeadCommitSubject: "fix (gu-aaa)", Conclusion: ConclusionFailure, Branch: "main", URL: "u", CompletedAt: now.Add(-72 * time.Hour)},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.ColdStartSuppressed != 0 {
		t.Errorf("warm poll must not suppress, got ColdStartSuppressed=%d", res.ColdStartSuppressed)
	}
	if res.FailuresHandled != 1 {
		t.Errorf("warm poll should escalate the old unseen failure, got FailuresHandled=%d", res.FailuresHandled)
	}
}

// TestWatcherSupersededFailureSuppressedOnWarmPoll is the gs-218 regression:
// on a warm poll, a failed run that a LATER passing run on main already
// superseded must be recorded as seen but NOT escalated — it is a resolved,
// historical break. Without this guard a wide fetch window (or a rebuilt
// ledger) re-floods the mayor with broke-main-ci for already-closed work.
func TestWatcherSupersededFailureSuppressedOnWarmPoll(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-old")
	fm := &fakeMailer{}
	now := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)
	// Warm start: seed a non-empty ledger so cold-start suppression is off.
	seed, _ := LoadSeenRuns(town, "alpha")
	seed.Mark("seed-run", now.Add(-24*time.Hour))
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		// Newest: main is green again — supersedes the earlier break.
		{ID: "201", HeadSHA: "greensha", Conclusion: ConclusionSuccess, Branch: "main", URL: "u201", CompletedAt: now.Add(-5 * time.Minute)},
		// Older failure that the green run above resolved — must be suppressed.
		{ID: "200", HeadSHA: "redsha", HeadCommitSubject: "fix (gu-old)", Conclusion: ConclusionFailure, Branch: "main", URL: "u200", CompletedAt: now.Add(-30 * time.Minute)},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.SupersededSuppressed != 1 {
		t.Errorf("SupersededSuppressed = %d, want 1", res.SupersededSuppressed)
	}
	if res.FailuresHandled != 0 {
		t.Errorf("FailuresHandled = %d, want 0 (break was superseded)", res.FailuresHandled)
	}
	if len(fm.sent) != 0 {
		t.Errorf("expected no mail for a superseded break, got %d: %v", len(fm.sent), fm.sent)
	}
	if len(fb.reopens) != 0 {
		t.Errorf("expected no bead reopen for a superseded break, got %v", fb.reopens)
	}
	// Both runs must be marked seen so a re-poll stays a no-op.
	seen, _ := LoadSeenRuns(town, "alpha")
	for _, id := range []string{"200", "201"} {
		if !seen.Has(id) {
			t.Errorf("run %s should be marked seen", id)
		}
	}
}

// TestWatcherCurrentBreakEscalatesDespiteEarlierSuccess verifies the superseded
// guard never silences a live regression: when the NEWEST run is a failure
// (no later passing run), it must still escalate even though an older passing
// run sits in the same fetch window.
func TestWatcherCurrentBreakEscalatesDespiteEarlierSuccess(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-new")
	fm := &fakeMailer{}
	now := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)
	seed, _ := LoadSeenRuns(town, "alpha")
	seed.Mark("seed-run", now.Add(-24*time.Hour))
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		// Newest: a fresh break with no later passing run — must escalate.
		{ID: "301", HeadSHA: "redsha", HeadCommitSubject: "feat (gu-new)", Conclusion: ConclusionFailure, Branch: "main", URL: "u301", CompletedAt: now.Add(-5 * time.Minute)},
		// Older passing run — does NOT supersede the newer failure.
		{ID: "300", HeadSHA: "greensha", Conclusion: ConclusionSuccess, Branch: "main", URL: "u300", CompletedAt: now.Add(-30 * time.Minute)},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.SupersededSuppressed != 0 {
		t.Errorf("SupersededSuppressed = %d, want 0", res.SupersededSuppressed)
	}
	if res.FailuresHandled != 1 {
		t.Errorf("FailuresHandled = %d, want 1 (live break must escalate)", res.FailuresHandled)
	}
	if len(fb.reopens) != 1 || fb.reopens[0] != "gu-new" {
		t.Errorf("reopens = %v, want [gu-new]", fb.reopens)
	}
}

// TestWatcherDoesNotBlameWhenMainAlreadyRed is the gs-4n7i class-4 regression:
// when main was ALREADY red (an earlier same-workflow run on main failed), a
// later red commit must still freeze but must NOT be blamed as the culprit —
// no reopen, no broke-main-ci label on the innocent commit's bead. The first
// red commit IS attributed.
func TestWatcherDoesNotBlameWhenMainAlreadyRed(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-first", "gu-second")
	fm := &fakeMailer{}
	now := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)
	seed, _ := LoadSeenRuns(town, "alpha")
	seed.Mark("seed-run", now.Add(-24*time.Hour))
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		// Newest red — main was ALREADY red (run 400 below), so this commit
		// is NOT the culprit and must not be blamed.
		{ID: "401", HeadSHA: "sha2", HeadCommitSubject: "feat (gu-second)", Conclusion: ConclusionFailure, Branch: "main", URL: "u401", Workflow: "CI", Event: "push", CompletedAt: now.Add(-5 * time.Minute)},
		// The FIRST red — broke main; this one IS the culprit.
		{ID: "400", HeadSHA: "sha1", HeadCommitSubject: "feat (gu-first)", Conclusion: ConclusionFailure, Branch: "main", URL: "u400", Workflow: "CI", Event: "push", CompletedAt: now.Add(-30 * time.Minute)},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.AlreadyRedSuppressed != 1 {
		t.Errorf("AlreadyRedSuppressed = %d, want 1", res.AlreadyRedSuppressed)
	}
	// Only the first-failing commit's bead is reopened/blamed.
	if len(fb.reopens) != 1 || fb.reopens[0] != "gu-first" {
		t.Errorf("reopens = %v, want [gu-first] (innocent commit must not be blamed)", fb.reopens)
	}
	if !res.FreezeWritten {
		t.Errorf("FreezeWritten = false; main is red and the queue must stay frozen")
	}
}

// TestWatcherSuppressesScheduledCronFailure is the gu-y94l1 regression: a
// failed scheduled-cron run on main (E2E, Nightly Integration) must NOT
// freeze the queue — those workflows run on a timer against pre-existing
// main state and have no per-MR attribution. Before the fix, every nightly
// cron failure on a pre-existing condition spuriously froze gastown_upstream
// and forced the mayor to manually unfreeze each cycle.
func TestWatcherSuppressesScheduledCronFailure(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	runs := []CIRun{
		{
			ID:                "800",
			HeadSHA:           "abcdef12",
			HeadCommitSubject: "fix: thing (gu-aaa)",
			Conclusion:        ConclusionFailure,
			Branch:            "main",
			URL:               "https://example.test/run/800",
			Workflow:          "Nightly Integration Tests",
			Event:             "schedule",
		},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.NonPushFailureSuppressed != 1 {
		t.Errorf("NonPushFailureSuppressed = %d, want 1", res.NonPushFailureSuppressed)
	}
	if res.FailuresHandled != 0 {
		t.Errorf("FailuresHandled = %d, want 0 (scheduled-cron must not freeze)", res.FailuresHandled)
	}
	if res.FreezeWritten {
		t.Errorf("FreezeWritten = true; scheduled-cron failures must not freeze the queue")
	}
	if len(fm.sent) != 0 {
		t.Errorf("expected no mail for scheduled-cron failure, got %d: %v", len(fm.sent), fm.sent)
	}
	if len(fb.reopens) != 0 {
		t.Errorf("expected no bead reopen for scheduled-cron failure, got %v", fb.reopens)
	}
	frozen, _ := IsFrozen(town, "alpha")
	if frozen {
		t.Errorf("queue should not be frozen by scheduled-cron failure")
	}
	// Run must be marked seen so the next poll doesn't reprocess.
	seen, _ := LoadSeenRuns(town, "alpha")
	if !seen.Has("800") {
		t.Errorf("run 800 should be marked seen even though escalation was suppressed")
	}
}

// TestWatcherSuppressesWorkflowDispatchFailure verifies the suppression covers
// all non-push events, not just schedule. workflow_dispatch (manual reruns),
// issue_comment (auto-label workflows), etc. are also out of scope for
// queue-freeze policy.
func TestWatcherSuppressesWorkflowDispatchFailure(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	runs := []CIRun{
		{ID: "801", HeadSHA: "ab", HeadCommitSubject: "fix (gu-aaa)", Conclusion: ConclusionFailure, Branch: "main", URL: "u", Event: "workflow_dispatch"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.NonPushFailureSuppressed != 1 || res.FreezeWritten {
		t.Errorf("workflow_dispatch failure should be suppressed, got %+v", res)
	}
}

// TestWatcherPushFailureStillFreezes verifies the suppression is scoped: a
// failed push-event run (a real post-merge regression) must still freeze the
// queue exactly as before. This is the canonical case the watcher exists for.
func TestWatcherPushFailureStillFreezes(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	runs := []CIRun{
		{
			ID:                "802",
			HeadSHA:           "deadbeef",
			HeadCommitSubject: "fix(refinery): handle slot timeout (gu-aaa)",
			Conclusion:        ConclusionFailure,
			Branch:            "main",
			URL:               "https://example.test/run/802",
			Workflow:          "CI",
			Event:             "push",
		},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.NonPushFailureSuppressed != 0 {
		t.Errorf("push failure must not be suppressed, got NonPushFailureSuppressed=%d", res.NonPushFailureSuppressed)
	}
	if res.FailuresHandled != 1 || !res.FreezeWritten {
		t.Errorf("push failure must freeze the queue, got %+v", res)
	}
	if len(fb.reopens) != 1 || fb.reopens[0] != "gu-aaa" {
		t.Errorf("push failure should reopen attributed bead, got %v", fb.reopens)
	}
	frozen, _ := IsFrozen(town, "alpha")
	if !frozen {
		t.Errorf("push failure should freeze the queue")
	}
}

// TestWatcherEmptyEventFallsBackToLegacyFreeze verifies backward compatibility:
// a host that does not populate Event (empty string) still gets the legacy
// "freeze on any failure" behavior so non-GitHub backends are not silently
// disabled.
func TestWatcherEmptyEventFallsBackToLegacyFreeze(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	runs := []CIRun{
		// No Event field — legacy fetcher / non-gh host.
		{ID: "803", HeadSHA: "ab", HeadCommitSubject: "fix (gu-aaa)", Conclusion: ConclusionFailure, Branch: "main", URL: "u"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.NonPushFailureSuppressed != 0 {
		t.Errorf("empty Event should not trigger non-push suppression, got %d", res.NonPushFailureSuppressed)
	}
	if res.FailuresHandled != 1 || !res.FreezeWritten {
		t.Errorf("empty-event failure should fall back to legacy freeze, got %+v", res)
	}
}

// TestWatcherScheduledCronSuccessClearsFreeze verifies the success path is
// NOT scoped to push events. A green scheduled-cron run on main genuinely
// indicates main is healthy, so it should clear an existing freeze just like
// a green push run would. This keeps the watcher liberal about clearing —
// the cost of an unwanted clear is much smaller than the cost of an unwanted
// freeze (the scenario this whole fix exists to prevent).
func TestWatcherScheduledCronSuccessClearsFreeze(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	if err := WriteFreeze(town, FreezeFile{Rig: "alpha", BeadID: "gu-aaa", Reason: "stale freeze"}); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		{ID: "900", HeadSHA: "feedface", Conclusion: ConclusionSuccess, Branch: "main", URL: "u", Event: "schedule"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.FreezeCleared {
		t.Errorf("scheduled-cron success on main should still clear an existing freeze")
	}
	frozen, _ := IsFrozen(town, "alpha")
	if frozen {
		t.Errorf("freeze should be cleared")
	}
}

// TestWatcherGreenWorkflowDoesNotMaskRedWorkflow is the gu-t1z17 regression,
// mirroring the live case: interleaved [Windows CI=success, CI=failure] push
// runs on the same branch. The branch-global supersession let the green
// "Windows CI" run mask the red "CI" run, so main breakage landed with no
// freeze and no broke-main-ci bead. Per-workflow scoping must freeze the queue
// and reopen the bead for the persistently-red "CI" workflow.
func TestWatcherGreenWorkflowDoesNotMaskRedWorkflow(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	now := time.Date(2026, 6, 12, 21, 0, 0, 0, time.UTC)
	// Warm start so cold-start suppression is off.
	seed, _ := LoadSeenRuns(town, "alpha")
	seed.Mark("seed-run", now.Add(-24*time.Hour))
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	// Newest-first, interleaved as observed live: each push has a green
	// "Windows CI" and a red "CI" in the same fetch window.
	runs := []CIRun{
		{ID: "104", HeadSHA: "a3b29d49", HeadCommitSubject: "fix (gu-aaa)", Conclusion: ConclusionFailure, Branch: "main", Workflow: "CI", Event: "push", URL: "u104", CompletedAt: now.Add(-1 * time.Minute)},
		{ID: "103", HeadSHA: "a3b29d49", Conclusion: ConclusionSuccess, Branch: "main", Workflow: "Windows CI", Event: "push", URL: "u103", CompletedAt: now.Add(-2 * time.Minute)},
		{ID: "102", HeadSHA: "d6e33a42", HeadCommitSubject: "fix (gu-aaa)", Conclusion: ConclusionFailure, Branch: "main", Workflow: "CI", Event: "push", URL: "u102", CompletedAt: now.Add(-6 * time.Minute)},
		{ID: "101", HeadSHA: "d6e33a42", Conclusion: ConclusionSuccess, Branch: "main", Workflow: "Windows CI", Event: "push", URL: "u101", CompletedAt: now.Add(-7 * time.Minute)},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// The older "CI" failure (102) is superseded by the newer "CI" failure?
	// No — supersession needs a later *success* of the same workflow. There
	// is no "CI" success, so 102 is superseded by nothing; only the newest
	// "CI" failure (104) drives the freeze. 102 still escalates because it is
	// not superseded — but it does not matter for the freeze outcome.
	if res.FailuresHandled < 1 {
		t.Errorf("FailuresHandled = %d, want >= 1 (CI failure must escalate)", res.FailuresHandled)
	}
	if !res.FreezeWritten {
		t.Errorf("FreezeWritten = false; the red CI workflow must freeze the queue")
	}
	if len(fb.reopens) < 1 || fb.reopens[len(fb.reopens)-1] != "gu-aaa" {
		t.Errorf("reopens = %v, want gu-aaa reopened for the CI failure", fb.reopens)
	}
	frozen, _ := IsFrozen(town, "alpha")
	if !frozen {
		t.Errorf("queue must be frozen — green Windows CI must not mask red CI")
	}
	ff, _ := ReadFreeze(town, "alpha")
	if ff == nil || ff.Workflow != "CI" {
		t.Errorf("freeze should record Workflow=CI, got %+v", ff)
	}
}

// TestWatcherCrossWorkflowSuccessDoesNotClearFreeze is the freeze-clear half of
// gu-t1z17: a freeze written for "CI" must NOT be cleared by a green "Windows
// CI" run. Only a green "CI" run resolves a "CI" freeze.
func TestWatcherCrossWorkflowSuccessDoesNotClearFreeze(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	if err := WriteFreeze(town, FreezeFile{Rig: "alpha", BeadID: "gu-aaa", Workflow: "CI", Reason: "broke-main-ci: gu-aaa"}); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		{ID: "210", HeadSHA: "greensha", Conclusion: ConclusionSuccess, Branch: "main", Workflow: "Windows CI", Event: "push", URL: "u210"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.FreezeCleared {
		t.Errorf("FreezeCleared = true; a green Windows CI must not clear a CI freeze")
	}
	frozen, _ := IsFrozen(town, "alpha")
	if !frozen {
		t.Errorf("freeze should remain — the failing CI workflow is still red")
	}
	if len(fm.sent) != 0 {
		t.Errorf("no CLEARED mail expected, got %v", fm.sent)
	}
}

// TestWatcherMatchingWorkflowSuccessClearsFreeze verifies the complement: a
// green run of the SAME workflow that froze the queue clears the freeze.
func TestWatcherMatchingWorkflowSuccessClearsFreeze(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	if err := WriteFreeze(town, FreezeFile{Rig: "alpha", BeadID: "gu-aaa", Workflow: "CI", Reason: "broke-main-ci: gu-aaa"}); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		{ID: "211", HeadSHA: "greensha", Conclusion: ConclusionSuccess, Branch: "main", Workflow: "CI", Event: "push", URL: "u211"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.FreezeCleared {
		t.Errorf("FreezeCleared = false; a green CI run must clear the CI freeze")
	}
	frozen, _ := IsFrozen(town, "alpha")
	if frozen {
		t.Errorf("freeze should be cleared by the matching-workflow success")
	}
}

// TestWatcherEmptyWorkflowFreezeClearsOnAnySuccess verifies backward
// compatibility: a freeze with no recorded workflow (legacy / non-GitHub host)
// still clears on any green run regardless of that run's workflow.
func TestWatcherEmptyWorkflowFreezeClearsOnAnySuccess(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{}
	// Legacy freeze: no Workflow recorded.
	if err := WriteFreeze(town, FreezeFile{Rig: "alpha", BeadID: "gu-aaa", Reason: "stale freeze"}); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		{ID: "212", HeadSHA: "greensha", Conclusion: ConclusionSuccess, Branch: "main", Workflow: "Windows CI", URL: "u212"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.FreezeCleared {
		t.Errorf("FreezeCleared = false; a legacy (no-workflow) freeze should clear on any success")
	}
}

// TestWatcherSameWorkflowSupersessionStillResolves guards gs-218 under the new
// per-workflow scoping: a fail-then-pass sequence WITHIN one workflow must
// still resolve to "no freeze" (the green run supersedes the earlier red).
func TestWatcherSameWorkflowSupersessionStillResolves(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-old")
	fm := &fakeMailer{}
	now := time.Date(2026, 6, 12, 21, 0, 0, 0, time.UTC)
	seed, _ := LoadSeenRuns(town, "alpha")
	seed.Mark("seed-run", now.Add(-24*time.Hour))
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	runs := []CIRun{
		// Newest: same workflow ("CI") green — supersedes the earlier red.
		{ID: "221", HeadSHA: "greensha", Conclusion: ConclusionSuccess, Branch: "main", Workflow: "CI", Event: "push", URL: "u221", CompletedAt: now.Add(-5 * time.Minute)},
		{ID: "220", HeadSHA: "redsha", HeadCommitSubject: "fix (gu-old)", Conclusion: ConclusionFailure, Branch: "main", Workflow: "CI", Event: "push", URL: "u220", CompletedAt: now.Add(-30 * time.Minute)},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.SupersededSuppressed != 1 {
		t.Errorf("SupersededSuppressed = %d, want 1 (same-workflow fail-then-pass)", res.SupersededSuppressed)
	}
	if res.FailuresHandled != 0 {
		t.Errorf("FailuresHandled = %d, want 0 (break superseded within CI)", res.FailuresHandled)
	}
}

func TestWatcherMailFailureKeepsRunUnseen(t *testing.T) {
	town := t.TempDir()
	fb := newFakeBeads("gu-aaa")
	fm := &fakeMailer{err: errors.New("smtp down")}
	runs := []CIRun{
		{ID: "700", HeadSHA: "ab", HeadCommitSubject: "(gu-aaa)", Conclusion: ConclusionFailure, Branch: "main", URL: "u"},
	}
	w, _ := newWatcher(t, town, runs, nil, fb, fm)
	if _, err := w.Process(context.Background()); err == nil {
		t.Fatal("expected error from mailer")
	}
	// Seen-runs ledger MUST NOT contain the run since we failed mid-flight.
	seen, _ := LoadSeenRuns(town, "alpha")
	if seen.Has("700") {
		t.Errorf("run 700 should not be marked seen after mail failure")
	}
}

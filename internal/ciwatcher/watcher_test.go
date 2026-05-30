package ciwatcher

import (
	"bytes"
	"context"
	"errors"
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

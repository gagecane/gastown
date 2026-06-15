package prwatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- fakes -------------------------------------------------------------------

type fakeFetcher struct {
	comments []ReviewComment
	err      error
}

func (f *fakeFetcher) UnresolvedComments(_ context.Context) ([]ReviewComment, error) {
	return f.comments, f.err
}

type fakeDispatcher struct {
	created   []string // "<title>::<label>"
	slung     []string // "<beadID>::<rig>"
	nextID    int
	createErr error
	slingErr  error
}

func (d *fakeDispatcher) CreateBead(title, _ string, label string) (string, error) {
	if d.createErr != nil {
		return "", d.createErr
	}
	d.created = append(d.created, title+"::"+label)
	d.nextID++
	return fmt.Sprintf("gc-test%d", d.nextID), nil
}

func (d *fakeDispatcher) Sling(beadID, rig string) error {
	if d.slingErr != nil {
		return d.slingErr
	}
	d.slung = append(d.slung, beadID+"::"+rig)
	return nil
}

type fakeReplier struct {
	replies []string // "<pr>::<body>"
	err     error
}

func (r *fakeReplier) Reply(prNumber int, body string) error {
	if r.err != nil {
		return r.err
	}
	r.replies = append(r.replies, fmt.Sprintf("%d::%s", prNumber, body))
	return nil
}

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

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func newWatcher(t *testing.T, town string, comments []ReviewComment, fetchErr error) (*Watcher, *fakeDispatcher, *fakeReplier, *fakeMailer) {
	t.Helper()
	fd := &fakeDispatcher{}
	fr := &fakeReplier{}
	fm := &fakeMailer{}
	cfg := Config{TownRoot: town, Rig: "alpha"}
	clock := fixedClock{t: time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)}
	w := NewWatcher(cfg, &fakeFetcher{comments: comments, err: fetchErr}, fd, fr, fm, clock, &bytes.Buffer{})
	return w, fd, fr, fm
}

// --- tests -------------------------------------------------------------------

func TestMechanicalCommentAutoDispatches(t *testing.T) {
	town := t.TempDir()
	comments := []ReviewComment{
		{ID: "c1", PRNumber: 7, Body: "typo: 'recieve' → 'receive'", Path: "a.go", Line: 3, URL: "http://x/c1"},
	}
	w, fd, fr, fm := newWatcher(t, town, comments, nil)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Mechanical != 1 || res.Gated != 0 {
		t.Errorf("mechanical=%d gated=%d, want 1/0", res.Mechanical, res.Gated)
	}
	if len(fd.created) != 1 || !strings.HasSuffix(fd.created[0], "::"+LabelPRComment) {
		t.Errorf("created = %v, want one bead with %s label", fd.created, LabelPRComment)
	}
	if len(fd.slung) != 1 || fd.slung[0] != "gc-test1::alpha" {
		t.Errorf("slung = %v, want [gc-test1::alpha]", fd.slung)
	}
	if len(fm.sent) != 0 {
		t.Errorf("mechanical comment should not mail mayor, sent=%v", fm.sent)
	}
	if len(fr.replies) != 1 || !strings.HasPrefix(fr.replies[0], "7::") {
		t.Errorf("replies = %v, want one ack on PR 7", fr.replies)
	}
}

func TestJudgmentCommentGates(t *testing.T) {
	town := t.TempDir()
	comments := []ReviewComment{
		{ID: "c1", PRNumber: 9, Body: "why is this locked here? consider a different approach", URL: "http://x/c1"},
	}
	w, fd, fr, fm := newWatcher(t, town, comments, nil)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Gated != 1 || res.Mechanical != 0 {
		t.Errorf("gated=%d mechanical=%d, want 1/0", res.Gated, res.Mechanical)
	}
	if len(fd.created) != 1 || !strings.Contains(fd.created[0], LabelNeedsHumanTriage) {
		t.Errorf("created = %v, want bead with %s", fd.created, LabelNeedsHumanTriage)
	}
	if len(fd.slung) != 0 {
		t.Errorf("judgment comment must NOT be slung, slung=%v", fd.slung)
	}
	if len(fm.sent) != 1 || !strings.Contains(fm.sent[0].Subject, "needs triage") {
		t.Errorf("want one mayor mail about triage, got %v", fm.sent)
	}
	if len(fr.replies) != 1 {
		t.Errorf("want one ack reply, got %v", fr.replies)
	}
}

func TestDedupAcrossPolls(t *testing.T) {
	town := t.TempDir()
	comments := []ReviewComment{
		{ID: "c1", PRNumber: 7, Body: "typo here", URL: "http://x/c1"},
	}
	w, fd, _, _ := newWatcher(t, town, comments, nil)
	if _, err := w.Process(context.Background()); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	// Second poll: same comment still unresolved. Must NOT re-action.
	w2, fd2, _, _ := newWatcher(t, town, comments, nil)
	res, err := w2.Process(context.Background())
	if err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if res.CommentsActioned != 0 {
		t.Errorf("poll 2 actioned=%d, want 0 (already actioned)", res.CommentsActioned)
	}
	if len(fd.created) != 1 || len(fd2.created) != 0 {
		t.Errorf("dedup failed: poll1 created=%d poll2 created=%d", len(fd.created), len(fd2.created))
	}
}

func TestColdStartSuppressesOldComments(t *testing.T) {
	town := t.TempDir()
	clockT := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	comments := []ReviewComment{
		{ID: "old", PRNumber: 1, Body: "typo", CreatedAt: clockT.Add(-48 * time.Hour)}, // older than 24h cutoff
		{ID: "new", PRNumber: 2, Body: "typo", CreatedAt: clockT.Add(-1 * time.Hour)},  // within cutoff
	}
	w, fd, _, _ := newWatcher(t, town, comments, nil)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.ColdStartSuppressed != 1 {
		t.Errorf("ColdStartSuppressed = %d, want 1", res.ColdStartSuppressed)
	}
	if res.CommentsActioned != 1 || res.Mechanical != 1 {
		t.Errorf("actioned=%d mechanical=%d, want 1/1 (only the recent comment)", res.CommentsActioned, res.Mechanical)
	}
	if len(fd.created) != 1 {
		t.Errorf("created = %v, want only the recent comment", fd.created)
	}
	// On a warm second poll, the old comment is in the ledger (marked seen) and
	// stays suppressed — no late dispatch.
	w2, fd2, _, _ := newWatcher(t, town, comments, nil)
	res2, err := w2.Process(context.Background())
	if err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if res2.CommentsActioned != 0 || len(fd2.created) != 0 {
		t.Errorf("warm poll re-actioned cold-start-suppressed comment: actioned=%d created=%v", res2.CommentsActioned, fd2.created)
	}
}

func TestSkippedOnUnavailableRepo(t *testing.T) {
	town := t.TempDir()
	w, _, _, _ := newWatcher(t, town, nil, ErrPRsUnavailable)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process should not error on ErrPRsUnavailable: %v", err)
	}
	if !res.Skipped {
		t.Errorf("expected Skipped=true")
	}
}

func TestFetchErrorPropagates(t *testing.T) {
	town := t.TempDir()
	w, _, _, _ := newWatcher(t, town, nil, errors.New("boom"))
	if _, err := w.Process(context.Background()); err == nil {
		t.Errorf("expected fetch error to propagate")
	}
}

func TestPartialFailureDoesNotMarkActioned(t *testing.T) {
	town := t.TempDir()
	comments := []ReviewComment{{ID: "c1", PRNumber: 7, Body: "typo here", URL: "http://x/c1"}}
	w, fd, _, _ := newWatcher(t, town, comments, nil)
	fd.slingErr = errors.New("sling failed")
	if _, err := w.Process(context.Background()); err == nil {
		t.Fatalf("expected sling failure to surface")
	}
	// The comment must NOT be in the ledger — next poll retries.
	a, _ := LoadActioned(town, "alpha")
	if a.Has("c1") {
		t.Errorf("comment marked actioned despite sling failure")
	}
}

func TestEmptyIDSkipped(t *testing.T) {
	town := t.TempDir()
	comments := []ReviewComment{{ID: "", PRNumber: 7, Body: "typo"}}
	w, fd, _, _ := newWatcher(t, town, comments, nil)
	res, err := w.Process(context.Background())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.CommentsActioned != 0 || len(fd.created) != 0 {
		t.Errorf("empty-ID comment should be skipped, actioned=%d created=%v", res.CommentsActioned, fd.created)
	}
}

package autotestpr

import (
	"errors"
	"strings"
	"testing"
)

// TestFormatReviseReply_Standard verifies the standard (automated) reply
// template includes commit SHA, gates, and summary.
func TestFormatReviseReply_Standard(t *testing.T) {
	args := ReviseReplyArgs{
		CommitSHA:   "abc12345def67890",
		GatesPassed: []string{"coverage-delta", "mutant-sanity", "flakiness-rerun", "tautology-linter", "gitleaks"},
		Summary:     "Added branch coverage for handleTimeout edge case",
		CommentID:   "cmt-42",
	}

	reply := FormatReviseReply(args)

	if reply.ThreadID != "cmt-42" {
		t.Errorf("ThreadID = %q, want %q", reply.ThreadID, "cmt-42")
	}

	if !strings.Contains(reply.Body, "🤖 **Auto-Test-PR Revision**") {
		t.Errorf("body missing standard header, got:\n%s", reply.Body)
	}

	// Truncated SHA (first 8 chars).
	if !strings.Contains(reply.Body, "`abc12345`") {
		t.Errorf("body missing truncated commit SHA, got:\n%s", reply.Body)
	}

	if !strings.Contains(reply.Body, "coverage-delta, mutant-sanity, flakiness-rerun, tautology-linter, gitleaks") {
		t.Errorf("body missing gates list, got:\n%s", reply.Body)
	}

	if !strings.Contains(reply.Body, "Added branch coverage for handleTimeout edge case") {
		t.Errorf("body missing summary, got:\n%s", reply.Body)
	}

	// Standard template MUST NOT include the manual-dispatch header.
	if strings.Contains(reply.Body, "Manual Revision") {
		t.Errorf("standard reply should not contain manual revision header, got:\n%s", reply.Body)
	}
}

// TestFormatReviseReply_ManualDispatch verifies the manual dispatch
// template uses the "Manual Revision" header and names the actor.
func TestFormatReviseReply_ManualDispatch(t *testing.T) {
	args := ReviseReplyArgs{
		CommitSHA:      "deadbeef12345678",
		GatesPassed:    []string{"coverage-delta", "tautology-linter"},
		Summary:        "Addressed reviewer feedback on error handling",
		CommentID:      "cmt-99",
		ManualDispatch: true,
		Actor:          "gastown_upstream/polecats/chrome",
	}

	reply := FormatReviseReply(args)

	if reply.ThreadID != "cmt-99" {
		t.Errorf("ThreadID = %q, want %q", reply.ThreadID, "cmt-99")
	}

	if !strings.Contains(reply.Body, "🤖 **Auto-Test-PR Manual Revision**") {
		t.Errorf("body missing manual dispatch header, got:\n%s", reply.Body)
	}

	if !strings.Contains(reply.Body, "gastown_upstream/polecats/chrome") {
		t.Errorf("body missing actor, got:\n%s", reply.Body)
	}

	if !strings.Contains(reply.Body, "`deadbeef`") {
		t.Errorf("body missing truncated commit SHA, got:\n%s", reply.Body)
	}
}

// TestFormatReviseReply_NoGates verifies graceful handling when no
// gates are passed (edge case: early failure path that still emits a
// reply).
func TestFormatReviseReply_NoGates(t *testing.T) {
	args := ReviseReplyArgs{
		CommitSHA: "abc12345",
		Summary:   "Attempted fix",
		CommentID: "cmt-1",
	}

	reply := FormatReviseReply(args)

	if !strings.Contains(reply.Body, "**Gates passed:** (none)") {
		t.Errorf("body should show (none) for empty gates, got:\n%s", reply.Body)
	}
}

// TestFormatReviseReply_EmptySummary verifies the reply omits the
// summary line entirely when summary is empty (no dangling label).
func TestFormatReviseReply_EmptySummary(t *testing.T) {
	args := ReviseReplyArgs{
		CommitSHA:   "abc12345",
		GatesPassed: []string{"gitleaks"},
		CommentID:   "cmt-1",
	}

	reply := FormatReviseReply(args)

	if strings.Contains(reply.Body, "**Summary:**") {
		t.Errorf("body should not contain summary line when empty, got:\n%s", reply.Body)
	}
}

// TestResolveTargetThread_Targeted verifies that when a comment-id is
// provided, it is used directly without consulting the thread list.
func TestResolveTargetThread_Targeted(t *testing.T) {
	threads := []CommentThread{
		{ID: "cmt-1", Resolved: false, CreatedAt: "2026-05-20T10:00:00Z"},
		{ID: "cmt-2", Resolved: false, CreatedAt: "2026-05-21T10:00:00Z"},
	}

	got, err := ResolveTargetThread("cmt-specific", threads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cmt-specific" {
		t.Errorf("ResolveTargetThread = %q, want %q", got, "cmt-specific")
	}
}

// TestResolveTargetThread_Targeted_EmptyThreads verifies that targeted
// resolution works even when the thread list is empty (the explicit
// --comment-id is trusted regardless of the bead's known threads).
func TestResolveTargetThread_Targeted_EmptyThreads(t *testing.T) {
	got, err := ResolveTargetThread("cmt-explicit", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cmt-explicit" {
		t.Errorf("ResolveTargetThread = %q, want %q", got, "cmt-explicit")
	}
}

// TestResolveTargetThread_Fallback_MostRecent verifies that when no
// comment-id is provided, the most recent non-resolved thread is
// selected (resolved threads are skipped).
func TestResolveTargetThread_Fallback_MostRecent(t *testing.T) {
	threads := []CommentThread{
		{ID: "cmt-old", Resolved: false, CreatedAt: "2026-05-19T08:00:00Z"},
		{ID: "cmt-resolved", Resolved: true, CreatedAt: "2026-05-22T12:00:00Z"},
		{ID: "cmt-newest", Resolved: false, CreatedAt: "2026-05-21T15:30:00Z"},
		{ID: "cmt-mid", Resolved: false, CreatedAt: "2026-05-20T10:00:00Z"},
	}

	got, err := ResolveTargetThread("", threads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cmt-newest" {
		t.Errorf("ResolveTargetThread = %q, want %q (most recent non-resolved)", got, "cmt-newest")
	}
}

// TestResolveTargetThread_Fallback_AllResolved verifies that when all
// threads are resolved and no comment-id is given, ErrNoUnresolvedThread
// is returned.
func TestResolveTargetThread_Fallback_AllResolved(t *testing.T) {
	threads := []CommentThread{
		{ID: "cmt-1", Resolved: true, CreatedAt: "2026-05-20T10:00:00Z"},
		{ID: "cmt-2", Resolved: true, CreatedAt: "2026-05-21T10:00:00Z"},
	}

	_, err := ResolveTargetThread("", threads)
	if !errors.Is(err, ErrNoUnresolvedThread) {
		t.Errorf("error = %v, want ErrNoUnresolvedThread", err)
	}
}

// TestResolveTargetThread_Fallback_NoThreads verifies that when no
// threads exist and no comment-id is given, ErrNoUnresolvedThread is
// returned.
func TestResolveTargetThread_Fallback_NoThreads(t *testing.T) {
	_, err := ResolveTargetThread("", nil)
	if !errors.Is(err, ErrNoUnresolvedThread) {
		t.Errorf("error = %v, want ErrNoUnresolvedThread", err)
	}
}

// TestResolveTargetThread_Fallback_SkipsUnparseableTimestamp verifies
// that threads with garbage CreatedAt are ignored by the fallback path
// rather than crashing the resolver.
func TestResolveTargetThread_Fallback_SkipsUnparseableTimestamp(t *testing.T) {
	threads := []CommentThread{
		{ID: "cmt-bad", Resolved: false, CreatedAt: "not-a-timestamp"},
		{ID: "cmt-good", Resolved: false, CreatedAt: "2026-05-20T10:00:00Z"},
	}

	got, err := ResolveTargetThread("", threads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cmt-good" {
		t.Errorf("ResolveTargetThread = %q, want %q (good timestamp wins)", got, "cmt-good")
	}
}

// TestGenerateReviseReplies_Targeted verifies the end-to-end path with
// an explicit comment-id: exactly one reply, targeting that thread,
// containing the commit SHA.
func TestGenerateReviseReplies_Targeted(t *testing.T) {
	args := ReviseReplyArgs{
		CommitSHA:   "abc12345def67890",
		GatesPassed: []string{"coverage-delta", "tautology-linter"},
		Summary:     "Fixed edge case",
		CommentID:   "cmt-42",
	}
	threads := []CommentThread{
		{ID: "cmt-1", Resolved: false, CreatedAt: "2026-05-20T10:00:00Z"},
		{ID: "cmt-42", Resolved: false, CreatedAt: "2026-05-21T10:00:00Z"},
	}

	replies, err := GenerateReviseReplies(args, threads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	if replies[0].ThreadID != "cmt-42" {
		t.Errorf("reply ThreadID = %q, want %q", replies[0].ThreadID, "cmt-42")
	}
	if !strings.Contains(replies[0].Body, "`abc12345`") {
		t.Errorf("reply body missing commit SHA, got:\n%s", replies[0].Body)
	}
}

// TestGenerateReviseReplies_Fallback verifies the end-to-end fallback
// path: no comment-id, manual dispatch, picks the most recent
// non-resolved thread and renders the manual-dispatch banner.
func TestGenerateReviseReplies_Fallback(t *testing.T) {
	args := ReviseReplyArgs{
		CommitSHA:      "feedface12345678",
		GatesPassed:    []string{"gitleaks"},
		Summary:        "Addressed feedback",
		ManualDispatch: true,
		Actor:          "overseer",
	}
	threads := []CommentThread{
		{ID: "cmt-old", Resolved: false, CreatedAt: "2026-05-19T08:00:00Z"},
		{ID: "cmt-resolved", Resolved: true, CreatedAt: "2026-05-25T12:00:00Z"},
		{ID: "cmt-recent", Resolved: false, CreatedAt: "2026-05-22T15:30:00Z"},
	}

	replies, err := GenerateReviseReplies(args, threads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	if replies[0].ThreadID != "cmt-recent" {
		t.Errorf("reply ThreadID = %q, want %q (most recent non-resolved)", replies[0].ThreadID, "cmt-recent")
	}
	if !strings.Contains(replies[0].Body, "Manual Revision") {
		t.Errorf("fallback manual reply missing manual header, got:\n%s", replies[0].Body)
	}
	if !strings.Contains(replies[0].Body, "overseer") {
		t.Errorf("fallback manual reply missing actor, got:\n%s", replies[0].Body)
	}
}

// TestGenerateReviseReplies_NoThreads_NoCommentID verifies that when no
// comment-id is given and no threads exist, the resolver surfaces
// ErrNoUnresolvedThread to the caller.
func TestGenerateReviseReplies_NoThreads_NoCommentID(t *testing.T) {
	args := ReviseReplyArgs{
		CommitSHA: "abc12345",
		Summary:   "Fix",
	}

	_, err := GenerateReviseReplies(args, nil)
	if !errors.Is(err, ErrNoUnresolvedThread) {
		t.Errorf("error = %v, want ErrNoUnresolvedThread", err)
	}
}

// TestTruncateSHA verifies SHA truncation behavior at the 8-char
// boundary, including short / empty inputs.
func TestTruncateSHA(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abc12345def67890", "abc12345"},
		{"short", "short"},
		{"12345678", "12345678"},
		{"123456789", "12345678"},
		{"", ""},
	}

	for _, tt := range tests {
		got := truncateSHA(tt.input)
		if got != tt.want {
			t.Errorf("truncateSHA(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

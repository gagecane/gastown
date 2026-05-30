// Unit tests for the D19 reply support helpers used by
// mol-polecat-work-test-improver mode=revise (Phase 0 task 3b: gu-75jja).
//
// These cover both paths required by the acceptance criteria:
//
//  1. Targeted reply (--comment-id supplied in the dispatch envelope):
//     SelectReplyTargets returns exactly the named thread, never falls
//     back, and surfaces a clear error when the ID is unknown.
//
//  2. Most-recent-thread fallback (--comment-id omitted): the helper
//     picks the newest non-resolved thread by CreatedAt. Resolved
//     threads are skipped. If every thread is resolved (or there are
//     zero threads), the helper signals a manual-only banner.
//
// The render tests pin the banner shape so downstream consumers (the
// formula step and any future bead-comment poster) can rely on the
// byte-for-byte contract.
package autotestpr

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ParseRevisionContext
// ---------------------------------------------------------------------------

func TestParseRevisionContext_Empty(t *testing.T) {
	t.Parallel()

	for _, tc := range []string{"", "   ", "null", "\n\n\t"} {
		rev, err := ParseRevisionContext(tc)
		if err != nil {
			t.Errorf("ParseRevisionContext(%q): unexpected err %v", tc, err)
		}
		if rev != nil {
			t.Errorf("ParseRevisionContext(%q): want nil, got %+v", tc, rev)
		}
	}
}

func TestParseRevisionContext_Malformed(t *testing.T) {
	t.Parallel()

	rev, err := ParseRevisionContext("{not json")
	if err == nil {
		t.Fatalf("expected error, got nil; rev=%+v", rev)
	}
	if !strings.Contains(err.Error(), "parsing args.revision") {
		t.Errorf("error wrap missing prefix: %v", err)
	}
}

func TestParseRevisionContext_FullShape(t *testing.T) {
	t.Parallel()

	raw := `{
		"branch": "auto-test/gastown_upstream/gu-75jja",
		"last_commit_sha": "abc1234567890",
		"comment_id": "cmt-42",
		"comments": [
			{
				"id": "cmt-42",
				"resolved": false,
				"created_at": "2026-05-21T10:30:00Z",
				"path": "internal/refinery/queue.go",
				"line": 58,
				"user": "alice"
			},
			{
				"id": "cmt-99",
				"resolved": true,
				"created_at": "2026-05-20T09:00:00Z",
				"path": "internal/refinery/queue.go",
				"line": 102,
				"user": "bob"
			}
		]
	}`
	rev, err := ParseRevisionContext(raw)
	if err != nil {
		t.Fatalf("ParseRevisionContext: %v", err)
	}
	if rev == nil {
		t.Fatal("ParseRevisionContext returned nil for valid input")
	}

	if rev.Branch != "auto-test/gastown_upstream/gu-75jja" {
		t.Errorf("Branch = %q", rev.Branch)
	}
	if rev.LastCommitSHA != "abc1234567890" {
		t.Errorf("LastCommitSHA = %q", rev.LastCommitSHA)
	}
	if rev.CommentID != "cmt-42" {
		t.Errorf("CommentID = %q", rev.CommentID)
	}
	if len(rev.Comments) != 2 {
		t.Fatalf("Comments length = %d, want 2", len(rev.Comments))
	}
	if rev.Comments[0].User != "alice" {
		t.Errorf("Comments[0].User = %q", rev.Comments[0].User)
	}
	if !rev.Comments[1].Resolved {
		t.Errorf("Comments[1] should be resolved")
	}
}

func TestRevisionContext_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	want := RevisionContext{
		Branch:        "auto-test/foo/bar",
		LastCommitSHA: "deadbeef",
		Comments: []CommentRef{
			{ID: "x", Resolved: false, CreatedAt: "2026-05-21T00:00:00Z", User: "u", Path: "p", Line: 1},
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got RevisionContext
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Branch != want.Branch || got.LastCommitSHA != want.LastCommitSHA {
		t.Errorf("scalar fields lost: got=%+v want=%+v", got, want)
	}
	if len(got.Comments) != 1 || got.Comments[0].ID != "x" {
		t.Errorf("Comments lost: %+v", got.Comments)
	}
}

// ---------------------------------------------------------------------------
// SelectReplyTargets — TARGETED PATH (--comment-id supplied)
// ---------------------------------------------------------------------------

func TestSelectReplyTargets_Targeted_HitsNamedThread(t *testing.T) {
	t.Parallel()

	rev := &RevisionContext{
		Comments: []CommentRef{
			{ID: "cmt-1", CreatedAt: "2026-05-21T01:00:00Z"},
			{ID: "cmt-42", CreatedAt: "2026-05-21T02:00:00Z", User: "alice"},
			{ID: "cmt-99", CreatedAt: "2026-05-21T03:00:00Z"},
		},
	}
	targets, fallback, err := SelectReplyTargets(rev, "cmt-42")
	if err != nil {
		t.Fatalf("SelectReplyTargets: %v", err)
	}
	if fallback {
		t.Errorf("fallback=true on targeted path; should be false")
	}
	if len(targets) != 1 || targets[0].ID != "cmt-42" {
		t.Fatalf("targets = %+v; want single cmt-42", targets)
	}
	if targets[0].User != "alice" {
		t.Errorf("target.User = %q; should preserve input", targets[0].User)
	}
}

func TestSelectReplyTargets_Targeted_PicksTargetEvenIfResolved(t *testing.T) {
	t.Parallel()

	// If a maintainer explicitly names a comment ID, we honor it even
	// if the thread is marked resolved. The fallback path filters
	// resolved; the targeted path does not.
	rev := &RevisionContext{
		Comments: []CommentRef{
			{ID: "cmt-1", Resolved: true, CreatedAt: "2026-05-21T00:00:00Z"},
		},
	}
	targets, fallback, err := SelectReplyTargets(rev, "cmt-1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fallback {
		t.Errorf("fallback should be false on targeted")
	}
	if len(targets) != 1 || targets[0].ID != "cmt-1" {
		t.Errorf("targets = %+v; want single cmt-1", targets)
	}
}

func TestSelectReplyTargets_Targeted_UnknownIDReturnsError(t *testing.T) {
	t.Parallel()

	rev := &RevisionContext{
		Comments: []CommentRef{{ID: "cmt-other"}},
	}
	targets, fallback, err := SelectReplyTargets(rev, "cmt-missing")
	if err == nil {
		t.Fatalf("expected ErrCommentIDNotFound, got nil; targets=%+v", targets)
	}
	if !errors.Is(err, ErrCommentIDNotFound) {
		t.Errorf("error not wrapped via ErrCommentIDNotFound: %v", err)
	}
	if fallback {
		t.Errorf("fallback should be false even on error (target was explicit)")
	}
	if targets != nil {
		t.Errorf("targets should be nil on error, got %+v", targets)
	}
	// Error message should name the comment_id and the size of the list
	// so the maintainer can tell what went wrong.
	if !strings.Contains(err.Error(), "cmt-missing") {
		t.Errorf("error should mention the comment_id, got: %v", err)
	}
	if !strings.Contains(err.Error(), "1-thread list") {
		t.Errorf("error should mention list size, got: %v", err)
	}
}

func TestSelectReplyTargets_Targeted_EmptyCommentList(t *testing.T) {
	t.Parallel()

	// commentID supplied but rev has zero comments — that's a
	// dispatcher bug (the maintainer named a comment that the
	// dispatcher never wrote into the envelope). Surface it.
	rev := &RevisionContext{Comments: nil}
	_, _, err := SelectReplyTargets(rev, "cmt-1")
	if !errors.Is(err, ErrCommentIDNotFound) {
		t.Fatalf("err = %v; want ErrCommentIDNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// SelectReplyTargets — FALLBACK PATH (--comment-id omitted)
// ---------------------------------------------------------------------------

func TestSelectReplyTargets_Fallback_PicksMostRecentNonResolved(t *testing.T) {
	t.Parallel()

	rev := &RevisionContext{
		Comments: []CommentRef{
			{ID: "old", CreatedAt: "2026-05-21T01:00:00Z"},
			{ID: "newest", CreatedAt: "2026-05-21T05:00:00Z"},
			{ID: "middle", CreatedAt: "2026-05-21T03:00:00Z"},
		},
	}
	targets, fallback, err := SelectReplyTargets(rev, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !fallback {
		t.Errorf("fallback should be true on most-recent path")
	}
	if len(targets) != 1 || targets[0].ID != "newest" {
		t.Errorf("targets = %+v; want single 'newest'", targets)
	}
}

func TestSelectReplyTargets_Fallback_SkipsResolvedThreads(t *testing.T) {
	t.Parallel()

	// Newest thread is resolved → must skip and pick the next-newest
	// non-resolved. This is the core D19 contract: only address still-
	// open threads on the manual fallback path so the polecat doesn't
	// re-litigate already-resolved feedback.
	rev := &RevisionContext{
		Comments: []CommentRef{
			{ID: "older-open", CreatedAt: "2026-05-21T01:00:00Z", Resolved: false},
			{ID: "newer-resolved", CreatedAt: "2026-05-21T05:00:00Z", Resolved: true},
			{ID: "middle-open", CreatedAt: "2026-05-21T03:00:00Z", Resolved: false},
		},
	}
	targets, fallback, err := SelectReplyTargets(rev, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !fallback {
		t.Errorf("fallback should be true")
	}
	if len(targets) != 1 || targets[0].ID != "middle-open" {
		t.Errorf("targets = %+v; want single 'middle-open'", targets)
	}
}

func TestSelectReplyTargets_Fallback_AllResolved_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	rev := &RevisionContext{
		Comments: []CommentRef{
			{ID: "a", Resolved: true},
			{ID: "b", Resolved: true},
		},
	}
	targets, fallback, err := SelectReplyTargets(rev, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !fallback {
		t.Errorf("fallback should be true")
	}
	if len(targets) != 0 {
		t.Errorf("targets = %+v; want empty (all threads resolved)", targets)
	}
}

func TestSelectReplyTargets_Fallback_NoComments(t *testing.T) {
	t.Parallel()

	rev := &RevisionContext{Comments: nil}
	targets, fallback, err := SelectReplyTargets(rev, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !fallback {
		t.Errorf("fallback should be true")
	}
	if len(targets) != 0 {
		t.Errorf("targets = %+v; want empty", targets)
	}
}

func TestSelectReplyTargets_NilContext(t *testing.T) {
	t.Parallel()

	targets, fallback, err := SelectReplyTargets(nil, "")
	if err != nil {
		t.Errorf("err = %v; want nil", err)
	}
	if !fallback {
		t.Errorf("fallback should be true on nil context")
	}
	if len(targets) != 0 {
		t.Errorf("targets = %+v; want empty", targets)
	}
}

// ---------------------------------------------------------------------------
// MostRecentNonResolved (CLI preview helper)
// ---------------------------------------------------------------------------

func TestMostRecentNonResolved_Found(t *testing.T) {
	t.Parallel()

	rev := &RevisionContext{
		Comments: []CommentRef{
			{ID: "a", CreatedAt: "2026-05-21T01:00:00Z"},
			{ID: "b", CreatedAt: "2026-05-21T02:00:00Z"},
		},
	}
	got, ok := MostRecentNonResolved(rev)
	if !ok {
		t.Fatal("ok=false; want true")
	}
	if got.ID != "b" {
		t.Errorf("got.ID=%q; want b", got.ID)
	}
}

func TestMostRecentNonResolved_None(t *testing.T) {
	t.Parallel()

	for _, rev := range []*RevisionContext{
		nil,
		{Comments: nil},
		{Comments: []CommentRef{{ID: "a", Resolved: true}}},
	} {
		got, ok := MostRecentNonResolved(rev)
		if ok {
			t.Errorf("ok=true on %+v; want false (got=%+v)", rev, got)
		}
	}
}

// ---------------------------------------------------------------------------
// RenderD19Reply — banner shape
// ---------------------------------------------------------------------------

func TestRenderD19Reply_TargetedFull(t *testing.T) {
	t.Parallel()

	out := RenderD19Reply(D19ReplyArgs{
		CommitSHA:   "abc1234",
		Branch:      "auto-test/gastown_upstream/gu-75jja",
		GatesPassed: []string{"coverage-delta", "mutant-sanity", "flakiness", "tautology", "gitleaks"},
		Summary:     "tighten LeaseExpired assertion",
		Target: CommentRef{
			ID:   "cmt-42",
			User: "alice",
			Path: "internal/refinery/queue.go",
			Line: 58,
		},
		Manual: false,
	})

	wantContains := []string{
		"🤖 Revision dispatched by gt auto-test-pr (D19)",
		"New commit:    abc1234",
		"Branch:        auto-test/gastown_upstream/gu-75jja",
		"Re: comment:   alice on internal/refinery/queue.go:58",
		"Gates passed:",
		"  ✓ coverage-delta",
		"  ✓ gitleaks",
		"Summary: tighten LeaseExpired assertion",
	}
	for _, s := range wantContains {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\nFull output:\n%s", s, out)
		}
	}

	if strings.Contains(out, "Manual fallback") {
		t.Errorf("non-manual reply should NOT mention manual fallback:\n%s", out)
	}
}

func TestRenderD19Reply_ManualFallbackBanner(t *testing.T) {
	t.Parallel()

	// gt auto-test-pr revise without --comment-id AND no comments to
	// fall back to → manual-revision-dispatched-by-<actor> banner.
	out := RenderD19Reply(D19ReplyArgs{
		CommitSHA:   "deadbee",
		Branch:      "auto-test/foo/gu-1",
		GatesPassed: []string{"build", "test"},
		Manual:      true,
		Actor:       "polecat/fury",
	})

	if !strings.Contains(out, "(Manual fallback: dispatched by polecat/fury)") {
		t.Errorf("manual banner missing actor:\n%s", out)
	}
	if strings.Contains(out, "Re: comment:") {
		t.Errorf("manual fallback with no target should omit Re: line:\n%s", out)
	}
}

func TestRenderD19Reply_ManualFallbackDefaultActor(t *testing.T) {
	t.Parallel()

	out := RenderD19Reply(D19ReplyArgs{
		CommitSHA: "x",
		Manual:    true,
		Actor:     "",
	})
	if !strings.Contains(out, "dispatched by operator") {
		t.Errorf("empty actor should default to 'operator':\n%s", out)
	}
}

func TestRenderD19Reply_NoGates(t *testing.T) {
	t.Parallel()

	out := RenderD19Reply(D19ReplyArgs{
		CommitSHA:   "x",
		GatesPassed: nil,
	})
	if !strings.Contains(out, "(no gates configured)") {
		t.Errorf("empty gates should render placeholder:\n%s", out)
	}
}

func TestRenderD19Reply_EmptyCommitSHA(t *testing.T) {
	t.Parallel()

	out := RenderD19Reply(D19ReplyArgs{CommitSHA: ""})
	if !strings.Contains(out, "New commit:    (unknown)") {
		t.Errorf("empty SHA should render '(unknown)':\n%s", out)
	}
}

func TestRenderD19Reply_OmitsBlankBranch(t *testing.T) {
	t.Parallel()

	out := RenderD19Reply(D19ReplyArgs{
		CommitSHA: "abc",
		Branch:    "",
	})
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Branch:") {
			t.Errorf("Branch line should be omitted when branch empty:\n%s", out)
		}
	}
}

func TestRenderD19Reply_OmitsBlankSummary(t *testing.T) {
	t.Parallel()

	out := RenderD19Reply(D19ReplyArgs{CommitSHA: "abc", Summary: "  "})
	if strings.Contains(out, "Summary:") {
		t.Errorf("blank summary should be omitted:\n%s", out)
	}
}

func TestRenderD19Reply_TargetWithMissingFields(t *testing.T) {
	t.Parallel()

	// Target carries Line but no User/Path — banner should still
	// produce a stable line (no nils, no panics).
	out := RenderD19Reply(D19ReplyArgs{
		CommitSHA: "abc",
		Target:    CommentRef{ID: "x", Line: 7},
	})
	if !strings.Contains(out, "Re: comment:   (unknown) on (no path):7") {
		t.Errorf("partial-target banner missing fallback labels:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// ValidateRevisionContext
// ---------------------------------------------------------------------------

func TestValidateRevisionContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rev     *RevisionContext
		wantErr string
	}{
		{
			name:    "nil",
			rev:     nil,
			wantErr: "nil",
		},
		{
			name:    "missing branch",
			rev:     &RevisionContext{LastCommitSHA: "abc"},
			wantErr: "revision.branch is required",
		},
		{
			name:    "missing SHA",
			rev:     &RevisionContext{Branch: "b"},
			wantErr: "revision.last_commit_sha is required",
		},
		{
			name: "ok",
			rev:  &RevisionContext{Branch: "b", LastCommitSHA: "abc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRevisionContext(tt.rev)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("err = %v; want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v; want substring %q", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FormatCreatedAt
// ---------------------------------------------------------------------------

func TestFormatCreatedAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"2026-05-21T10:30:00Z", "2026-05-21T10:30:00Z"},
		{"2026-05-21T10:30:00+02:00", "2026-05-21T08:30:00Z"}, // UTC normalize
		{"not a timestamp", "not a timestamp"},                // pass through
		{"", ""},
	}
	for _, tt := range tests {
		got := FormatCreatedAt(tt.in)
		if got != tt.want {
			t.Errorf("FormatCreatedAt(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

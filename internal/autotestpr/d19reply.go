// D19 reply support for mol-polecat-work-test-improver mode=revise.
//
// PRD scenario S3 ("the comment thread is replied to") and design D19
// require that, after a revision commit lands on an auto-test-pr MR,
// the polecat emits a structured reply on each comment thread it
// addressed. Without this, a maintainer who left a comment has no
// signal that the polecat acted on it (R23 in the risk register).
//
// This module provides the data types and helpers used by the formula
// step: parsing the dispatch envelope's args.revision, selecting which
// comment threads to reply to (targeted vs most-recent fallback), and
// rendering the templated banner.
//
// The actual posting channel (Refinery bead-comment in v1, GitHub
// review-reply in v2) is intentionally out of scope here — this module
// emits text and target IDs; the formula step pipes them through
// whichever transport applies in the current rig configuration.
//
// Design context: .designs/auto-test-pr/synthesis.md §D19 and
// §"Implementation Plan, Phase 0 task 3b".
package autotestpr

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrCommentIDNotFound is returned by SelectReplyTargets when an explicit
// comment_id was supplied in the dispatch envelope but does not match any
// thread in args.revision.comments[]. The formula step treats this as a
// hard fail rather than silently falling back, so that a maintainer who
// requested a specific reply never sees their comment ignored.
var ErrCommentIDNotFound = errors.New("comment_id does not match any thread in args.revision.comments")

// RevisionContext is the parsed shape of the dispatch envelope's
// args.revision field for mode=revise. It carries the prior comment
// thread, the last commit SHA on the branch, and the branch name.
//
// JSON shape (matches the dispatch envelope produced by
// gt auto-test-pr revise and by the future feedback-patrol):
//
//	{
//	  "branch":          "auto-test/<rig>/<bead>",
//	  "last_commit_sha": "<40-char SHA>",
//	  "comment_id":      "cmt-42"          // optional; targeted-reply hint
//	  "comments": [
//	    {
//	      "id":         "cmt-42",
//	      "resolved":   false,
//	      "created_at": "2026-05-21T10:30:00Z",
//	      "path":       "internal/refinery/queue.go",
//	      "line":       58,
//	      "user":       "alice"
//	    },
//	    ...
//	  ]
//	}
type RevisionContext struct {
	Branch        string       `json:"branch"`
	LastCommitSHA string       `json:"last_commit_sha"`
	CommentID     string       `json:"comment_id,omitempty"`
	Comments      []CommentRef `json:"comments,omitempty"`
}

// CommentRef is a single comment-thread descriptor inside
// args.revision.comments[]. The polecat replies on each thread it
// addresses with a templated banner naming the new commit SHA, the
// gates passed, and a one-line summary.
type CommentRef struct {
	ID        string `json:"id"`
	Resolved  bool   `json:"resolved,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	User      string `json:"user,omitempty"`
}

// ParseRevisionContext extracts a RevisionContext from the raw
// args.revision blob (JSON-encoded). An empty or null blob returns
// (nil, nil) so the caller can distinguish "no revision context" from
// "malformed revision context".
func ParseRevisionContext(raw string) (*RevisionContext, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var rev RevisionContext
	if err := json.Unmarshal([]byte(trimmed), &rev); err != nil {
		return nil, fmt.Errorf("parsing args.revision: %w", err)
	}
	return &rev, nil
}

// SelectReplyTargets chooses which comment threads the polecat should
// reply to per the D19 contract. Two paths:
//
//  1. Targeted (commentID != ""): find the thread in rev.Comments whose
//     ID matches; return [that thread], usedFallback=false. If no such
//     thread exists, returns (nil, false, ErrCommentIDNotFound) — the
//     formula treats this as a hard fail.
//
//  2. Fallback (commentID == ""): pick the most-recent non-resolved
//     thread by CreatedAt. If no non-resolved threads exist (or no
//     comments at all), return ([], true, nil) — the manual-revision
//     dispatched-by-<user> banner is still posted but as a free-standing
//     comment by the formula step (channel-dependent).
//
// The function never modifies rev or its slices.
func SelectReplyTargets(rev *RevisionContext, commentID string) ([]CommentRef, bool, error) {
	if rev == nil {
		// No revision context at all — fallback semantics.
		return nil, true, nil
	}

	if commentID != "" {
		for _, c := range rev.Comments {
			if c.ID == commentID {
				return []CommentRef{c}, false, nil
			}
		}
		return nil, false, fmt.Errorf("%w: comment_id=%q not in %d-thread list",
			ErrCommentIDNotFound, commentID, len(rev.Comments))
	}

	// Fallback: most-recent non-resolved by CreatedAt.
	candidates := make([]CommentRef, 0, len(rev.Comments))
	for _, c := range rev.Comments {
		if c.Resolved {
			continue
		}
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		return nil, true, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		// Newer (greater CreatedAt) first.
		return candidates[i].CreatedAt > candidates[j].CreatedAt
	})
	return []CommentRef{candidates[0]}, true, nil
}

// D19ReplyArgs captures the data the formula step assembles after the
// gate suite passes and before pushing.
type D19ReplyArgs struct {
	// CommitSHA is the new revision commit's SHA (full 40-char or short).
	// The banner shows the short form but the caller can pass either.
	CommitSHA string

	// Branch is the source branch the revision commit lives on.
	Branch string

	// GatesPassed names each gate the revision commit cleared, in the
	// order they ran. Empty list is allowed — the banner says
	// "no gates configured" rather than dropping the section.
	GatesPassed []string

	// Summary is a one-line description of what the polecat changed in
	// response to the comment (e.g. "tighten LeaseExpired assertion").
	// Empty string is allowed — the banner skips the summary line.
	Summary string

	// Target is the specific comment being replied to. Path/Line/User
	// are echoed in the banner so the maintainer sees their comment
	// referenced by file:line. The zero value is allowed for the
	// no-comments-at-all manual fallback.
	Target CommentRef

	// Manual is true when the reply is the dispatched-by-<actor>
	// fallback (gt auto-test-pr revise --mr=<id> without --comment-id
	// AND no comments to fall back to, OR the most-recent-thread
	// fallback path). The banner adds "manual revision dispatched by
	// <Actor>" framing.
	Manual bool

	// Actor names the human (or automation) that triggered the revise.
	// Used only when Manual=true. Falls back to "operator" when empty.
	Actor string
}

// RenderD19Reply produces the templated banner that the formula step
// posts on the comment thread (or as a free-standing comment in the
// no-thread fallback). The format is intentionally Markdown-friendly:
//
//	🤖 Revision dispatched by gt auto-test-pr (D19)
//	───────────────────────────────────────────────
//	New commit:    <SHA>
//	Branch:        <branch>
//	Re: comment:   <user> on <path>:<line>     (omitted when target empty)
//
//	Gates passed:
//	  ✓ <gate-1>
//	  ✓ <gate-2>
//	  ...
//
//	Summary: <one-line summary>             (omitted when summary empty)
//
//	(Manual fallback: dispatched by <actor>) (omitted unless Manual=true)
//
// The exact byte shape is asserted by the unit tests; downstream
// transports (bead-comment / GH review-reply) treat the output as an
// opaque body string.
func RenderD19Reply(args D19ReplyArgs) string {
	var sb strings.Builder
	sb.WriteString("🤖 Revision dispatched by gt auto-test-pr (D19)\n")
	sb.WriteString("───────────────────────────────────────────────\n")

	commit := strings.TrimSpace(args.CommitSHA)
	if commit == "" {
		commit = "(unknown)"
	}
	fmt.Fprintf(&sb, "New commit:    %s\n", commit)

	if branch := strings.TrimSpace(args.Branch); branch != "" {
		fmt.Fprintf(&sb, "Branch:        %s\n", branch)
	}

	if !isZeroComment(args.Target) {
		who := args.Target.User
		if who == "" {
			who = "(unknown)"
		}
		path := args.Target.Path
		if path == "" {
			path = "(no path)"
		}
		fmt.Fprintf(&sb, "Re: comment:   %s on %s:%d\n", who, path, args.Target.Line)
	}

	sb.WriteString("\nGates passed:\n")
	if len(args.GatesPassed) == 0 {
		sb.WriteString("  (no gates configured)\n")
	} else {
		for _, g := range args.GatesPassed {
			fmt.Fprintf(&sb, "  ✓ %s\n", g)
		}
	}

	if summary := strings.TrimSpace(args.Summary); summary != "" {
		fmt.Fprintf(&sb, "\nSummary: %s\n", summary)
	}

	if args.Manual {
		actor := strings.TrimSpace(args.Actor)
		if actor == "" {
			actor = "operator"
		}
		fmt.Fprintf(&sb, "\n(Manual fallback: dispatched by %s)\n", actor)
	}

	return sb.String()
}

// isZeroComment reports whether c is the zero value (no path, line, or
// user) — used to decide whether to print the "Re: comment:" line in
// the banner.
func isZeroComment(c CommentRef) bool {
	return c.ID == "" && c.Path == "" && c.Line == 0 && c.User == ""
}

// ValidateRevisionContext returns an error when rev is missing fields
// required by the formula step. mode=revise dispatch envelopes that
// fail this check are rejected by the formula's load-context step; the
// dispatcher (gt auto-test-pr revise) is responsible for filling them
// in. Validating early surfaces dispatcher bugs rather than letting
// them manifest as silent reply-skips.
func ValidateRevisionContext(rev *RevisionContext) error {
	if rev == nil {
		return errors.New("revision context is nil")
	}
	if strings.TrimSpace(rev.Branch) == "" {
		return errors.New("revision.branch is required for mode=revise")
	}
	if strings.TrimSpace(rev.LastCommitSHA) == "" {
		return errors.New("revision.last_commit_sha is required for mode=revise")
	}
	return nil
}

// MostRecentNonResolved is exported so the manual CLI
// (gt auto-test-pr revise --mr=<id> without --comment-id) can preview
// which thread the polecat will reply to before dispatching. Returns
// the zero value and false when no non-resolved threads exist.
func MostRecentNonResolved(rev *RevisionContext) (CommentRef, bool) {
	targets, _, err := SelectReplyTargets(rev, "")
	if err != nil || len(targets) == 0 {
		return CommentRef{}, false
	}
	return targets[0], true
}

// FormatCreatedAt formats an RFC3339 timestamp for display in the
// reply banner. Invalid timestamps round-trip unchanged so the banner
// never panics on dispatcher-side bugs.
func FormatCreatedAt(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339)
}

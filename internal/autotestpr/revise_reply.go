// D19 reply step: emit templated reply on comment threads after a
// revision commit is pushed.
//
// Phase 0 task 3b (gu-epc6). When mol-polecat-work-test-improver runs
// in mode=revise, after the polecat pushes a new commit addressing
// reviewer feedback, it must reply on each originating comment thread
// with a structured banner. This module provides the template rendering
// and thread resolution logic.
//
// The reply is a templated banner that names:
//   (a) the new commit SHA
//   (b) which gates passed
//   (c) a one-line summary of what the polecat changed
//
// Two paths:
//   1. --comment-id provided: reply to that specific thread.
//   2. No --comment-id: pick the most recent non-resolved comment thread
//      on the MR bead and reply there (D19 fallback).
//
// In v1 (Refinery mode), replies are follow-up bead-comments threaded
// against the review-comment bead. v2 (external mode) will use GitHub
// PR review replies.
//
// Design context: .designs/auto-test-pr/synthesis.md §D19
package autotestpr

import (
	"fmt"
	"strings"
	"time"
)

// ReviseReplyArgs holds the inputs needed to generate a D19 reply.
type ReviseReplyArgs struct {
	// CommitSHA is the new commit SHA pushed by the revise polecat.
	CommitSHA string

	// GatesPassed lists the gates that passed (e.g., ["coverage-delta",
	// "mutant-sanity", "flakiness-rerun", "tautology-linter", "gitleaks"]).
	GatesPassed []string

	// Summary is a one-line summary of what the polecat changed in
	// response to the reviewer's comment.
	Summary string

	// CommentID is the specific comment thread ID to reply to. When
	// empty, the fallback path (most-recent non-resolved) is used.
	CommentID string

	// ManualDispatch indicates whether this revise was triggered via
	// `gt auto-test-pr revise` without --comment-id. When true, the
	// reply uses the "manual revision dispatched by <user>" template.
	ManualDispatch bool

	// Actor is the operator/agent who triggered the revision.
	Actor string
}

// CommentThread represents a review comment thread on an MR bead.
// In v1, these are bead-comments stored as child beads with a
// parent_id pointing to the MR bead.
type CommentThread struct {
	// ID is the bead ID of the comment thread.
	ID string

	// Resolved indicates whether the thread has been marked resolved.
	Resolved bool

	// CreatedAt is the RFC3339 timestamp of the comment's creation.
	CreatedAt string

	// Author is the identity of the comment author.
	Author string

	// Body is the comment text.
	Body string
}

// ReviseReply is the formatted reply to post on a comment thread.
type ReviseReply struct {
	// ThreadID is the comment thread this reply targets.
	ThreadID string

	// Body is the templated reply text.
	Body string
}

// FormatReviseReply renders the D19 reply template for a targeted
// comment thread (--comment-id provided or resolved via fallback).
//
// The template is a structured banner:
//
//	🤖 **Auto-Test-PR Revision**
//	- **Commit:** `<sha>`
//	- **Gates passed:** coverage-delta, mutant-sanity, ...
//	- **Summary:** <one-line description>
func FormatReviseReply(args ReviseReplyArgs) ReviseReply {
	var body strings.Builder

	if args.ManualDispatch {
		body.WriteString("🤖 **Auto-Test-PR Manual Revision**\n")
		body.WriteString(fmt.Sprintf("- **Dispatched by:** %s\n", args.Actor))
	} else {
		body.WriteString("🤖 **Auto-Test-PR Revision**\n")
	}

	body.WriteString(fmt.Sprintf("- **Commit:** `%s`\n", truncateSHA(args.CommitSHA)))

	if len(args.GatesPassed) > 0 {
		body.WriteString(fmt.Sprintf("- **Gates passed:** %s\n", strings.Join(args.GatesPassed, ", ")))
	} else {
		body.WriteString("- **Gates passed:** (none)\n")
	}

	if args.Summary != "" {
		body.WriteString(fmt.Sprintf("- **Summary:** %s\n", args.Summary))
	}

	return ReviseReply{
		ThreadID: args.CommentID,
		Body:     body.String(),
	}
}

// ResolveTargetThread determines which comment thread to reply to.
//
// If args.CommentID is non-empty, it is used directly (targeted path).
// Otherwise, the most recent non-resolved thread is selected from the
// provided list (D19 fallback path).
//
// Returns the thread ID to reply to, or an error if no suitable thread
// is found.
func ResolveTargetThread(commentID string, threads []CommentThread) (string, error) {
	if commentID != "" {
		return commentID, nil
	}

	// Fallback: pick the most recent non-resolved comment thread.
	var best *CommentThread
	var bestTime time.Time

	for i := range threads {
		t := &threads[i]
		if t.Resolved {
			continue
		}

		parsed, err := time.Parse(time.RFC3339, t.CreatedAt)
		if err != nil {
			// If we can't parse the time, skip this thread.
			continue
		}

		if best == nil || parsed.After(bestTime) {
			best = t
			bestTime = parsed
		}
	}

	if best == nil {
		return "", ErrNoUnresolvedThread
	}

	return best.ID, nil
}

// ErrNoUnresolvedThread is returned by ResolveTargetThread when no
// non-resolved comment thread is found on the MR bead and no
// --comment-id was provided.
var ErrNoUnresolvedThread = fmt.Errorf("no unresolved comment thread found on MR bead")

// GenerateReviseReplies produces a ReviseReply for each comment thread
// in the args.revision.comments[] list. When CommentID is set, only
// one reply is generated for that specific thread. When CommentID is
// empty, the fallback picks the most recent non-resolved thread.
//
// This is the top-level function called by the formula's D19 reply step.
func GenerateReviseReplies(args ReviseReplyArgs, threads []CommentThread) ([]ReviseReply, error) {
	targetID, err := ResolveTargetThread(args.CommentID, threads)
	if err != nil {
		return nil, err
	}

	// Update the args with the resolved thread ID.
	args.CommentID = targetID
	reply := FormatReviseReply(args)

	return []ReviseReply{reply}, nil
}

// truncateSHA returns the first 8 characters of a SHA for display.
// If the SHA is shorter than 8 chars, it is returned as-is.
func truncateSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

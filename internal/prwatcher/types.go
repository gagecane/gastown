package prwatcher

import (
	"context"
	"time"
)

// ReviewComment is the watcher's host-agnostic view of a single unresolved PR
// review comment. The gh-CLI fetcher maps GitHub's GraphQL review-thread shape
// onto this struct; tests inject fakes.
type ReviewComment struct {
	// ID is the host-assigned comment identifier, stable across polls. For
	// GitHub this is the review-comment node ID (an opaque base64 string). It
	// is the dedup key in the actioned-comments ledger.
	ID string

	// PRNumber is the pull request the comment lives on.
	PRNumber int

	// PRTitle is the PR's title, used for context in beads and mail. May be
	// empty if the host did not report it.
	PRTitle string

	// Author is the GitHub login of the reviewer who wrote the comment.
	Author string

	// Body is the freeform comment text. This is what the triage classifier
	// inspects.
	Body string

	// Path is the file the comment is anchored to (e.g. "internal/foo/bar.go").
	// Empty for PR-level (non-diff) comments.
	Path string

	// Line is the line number in Path the comment is anchored to. Zero when
	// the host did not report a line (e.g. file-level or PR-level comments).
	Line int

	// URL is the human-readable permalink to the comment, used in ack replies,
	// beads, and mail.
	URL string

	// CreatedAt is when the comment was authored (UTC). Used by the cold-start
	// lookback to suppress dispatch of a pre-existing backlog on the first poll.
	CreatedAt time.Time
}

// CommentFetcher abstracts the host-specific "list unresolved review comments
// on open PRs" call. Production uses the gh-CLI client; tests inject a fake.
type CommentFetcher interface {
	// UnresolvedComments returns review comments belonging to unresolved
	// review threads on open PRs in the rig's repo, most recent first. The
	// fetcher MUST filter out comments on resolved threads — those are already
	// handled and re-actioning them would be spurious.
	UnresolvedComments(ctx context.Context) ([]ReviewComment, error)
}

// Dispatcher abstracts turning a triaged comment into Gas Town work. The
// production implementation shells out to bd create + gt sling; tests record
// the calls. Each method is best-effort and idempotent at the watcher level
// (the actioned ledger guarantees a comment is dispatched at most once).
type Dispatcher interface {
	// CreateBead creates a work bead for a review comment and returns its ID.
	// `label` is the lifecycle label that distinguishes mechanical
	// (auto-dispatch) from judgment (needs-human-triage) work.
	CreateBead(title, description, label string) (string, error)

	// Sling assigns an existing bead to the rig so a fresh polecat fixes it.
	// Only called for the mechanical class; judgment-class beads are gated for
	// a human and never auto-slung.
	Sling(beadID, rig string) error
}

// Replier posts an acknowledgement back onto the PR comment thread so the
// reviewer sees the comment was picked up. Production shells out to
// `gh pr comment`; tests record the replies.
type Replier interface {
	// Reply posts `body` as a reply on the PR identified by `prNumber`. The
	// fetcher's comment URL is included in the body so the reviewer can
	// correlate the ack with the originating thread.
	Reply(prNumber int, body string) error
}

// Mailer abstracts mail delivery to the mayor. Judgment-class comments mail the
// mayor at HIGH severity for human confirmation before any code change. We use
// mail (not nudge) because it must survive session death — the mayor may triage
// it on a later patrol.
type Mailer interface {
	// SendMayor delivers a mail to the mayor. Returns an error if delivery
	// fails so the watcher can retry next poll.
	SendMayor(subject, body string) error
}

// Clock returns the current time. Tests inject a deterministic clock.
type Clock interface {
	Now() time.Time
}

// realClock is the production clock.
type realClock struct{}

// Now returns time.Now() in UTC.
func (realClock) Now() time.Time { return time.Now().UTC() }

// SystemClock is the production singleton clock.
var SystemClock Clock = realClock{}

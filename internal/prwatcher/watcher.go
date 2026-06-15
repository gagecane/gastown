package prwatcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// LabelPRComment is added to every bead the watcher creates from a PR review
// comment. Operators can search `bd list --label=pr-review-comment` to see all
// comment-derived work.
const LabelPRComment = "pr-review-comment"

// LabelNeedsHumanTriage is added to judgment-class beads: the comment requires
// human confirmation before any code change, so the bead is NOT auto-slung. A
// human triages `bd list --label=needs-human-triage` and slings what's real.
const LabelNeedsHumanTriage = "needs-human-triage"

// DefaultColdStartLookback bounds dispatch on the first-ever poll for a rig
// (when no actioned ledger exists). Without this bound, a fresh poller treats
// every open review comment as new and floods the rig with beads for a backlog
// of pre-existing comments. Comments created before now-lookback on a cold
// start are recorded as actioned but not dispatched. 24h comfortably covers a
// poller restart gap while keeping a long-standing review backlog out.
const DefaultColdStartLookback = 24 * time.Hour

// Config holds the static configuration for a Watcher. All fields required
// unless noted.
type Config struct {
	// TownRoot is the absolute path to the town root. Used to locate the
	// actioned-comments ledger.
	TownRoot string

	// Rig is the rig whose PRs this watcher polls. The ledger path is per-rig
	// so multiple rigs can run watchers concurrently without colliding. Also
	// the sling target for mechanical-class work.
	Rig string

	// ColdStartLookback bounds dispatch on the first-ever poll for a rig (no
	// actioned ledger). Defaults to DefaultColdStartLookback when zero.
	ColdStartLookback time.Duration
}

// Watcher orchestrates the PR-comment poll loop. Construct with NewWatcher and
// call Process() once per poll cycle.
type Watcher struct {
	cfg        Config
	fetcher    CommentFetcher
	dispatcher Dispatcher
	replier    Replier
	mailer     Mailer
	clock      Clock
	out        io.Writer
}

// NewWatcher constructs a Watcher. fetcher/dispatcher/replier/mailer must be
// non-nil; out may be nil (no logging) and clock may be nil (SystemClock).
func NewWatcher(cfg Config, fetcher CommentFetcher, dispatcher Dispatcher, replier Replier, mailer Mailer, clock Clock, out io.Writer) *Watcher {
	if clock == nil {
		clock = SystemClock
	}
	if cfg.ColdStartLookback == 0 {
		cfg.ColdStartLookback = DefaultColdStartLookback
	}
	return &Watcher{
		cfg:        cfg,
		fetcher:    fetcher,
		dispatcher: dispatcher,
		replier:    replier,
		mailer:     mailer,
		clock:      clock,
		out:        out,
	}
}

// PollResult is the per-call summary, returned for logging and tests.
type PollResult struct {
	// CommentsConsidered is the number of unresolved comments the fetcher
	// returned.
	CommentsConsidered int

	// CommentsActioned is the subset acted on this poll (not already in the
	// ledger and not cold-start-suppressed).
	CommentsActioned int

	// Mechanical counts comments auto-dispatched (bead created + slung).
	Mechanical int

	// Gated counts judgment-class comments (bead created with
	// needs-human-triage + mayor mailed, NOT slung).
	Gated int

	// ColdStartSuppressed counts comments recorded as actioned but NOT
	// dispatched because this was a cold start and the comment predates the
	// lookback cutoff. Always 0 on warm polls.
	ColdStartSuppressed int

	// Skipped is true when the rig has no pollable PRs — the repo does not
	// exist or PRs are unavailable. SkipReason carries the detail.
	Skipped    bool
	SkipReason string
}

// Process fetches unresolved PR review comments, triages each fresh one, and
// dispatches accordingly. Caller invokes it on a schedule; Process does not
// loop.
func (w *Watcher) Process(ctx context.Context) (PollResult, error) {
	res := PollResult{}

	comments, err := w.fetcher.UnresolvedComments(ctx)
	if err != nil {
		// A missing repo (HTTP 404) is a benign, persistent condition. Report
		// it as a clean skip so the plugin records a success receipt instead of
		// failing every cooldown cycle (mirrors ciwatcher gu-qfhvw).
		if errors.Is(err, ErrPRsUnavailable) {
			res.Skipped = true
			res.SkipReason = err.Error()
			w.logf("prwatcher: rig=%s skipped — %v", w.cfg.Rig, err)
			return res, nil
		}
		return res, fmt.Errorf("prwatcher: fetch comments: %w", err)
	}
	res.CommentsConsidered = len(comments)

	actioned, err := LoadActioned(w.cfg.TownRoot, w.cfg.Rig)
	if err != nil {
		return res, fmt.Errorf("prwatcher: load actioned: %w", err)
	}

	coldStart := actioned.Fresh()
	var coldCutoff time.Time
	if coldStart {
		coldCutoff = w.clock.Now().Add(-w.cfg.ColdStartLookback)
		w.logf("prwatcher: cold start (no actioned ledger for rig=%s) — suppressing dispatch for comments created before %s",
			w.cfg.Rig, coldCutoff.Format(time.RFC3339))
	}

	// Process oldest-to-newest so a poll that actions a batch dispatches them
	// in authoring order. The fetcher returns newest-first, so reverse here.
	ordered := make([]ReviewComment, 0, len(comments))
	for i := len(comments) - 1; i >= 0; i-- {
		ordered = append(ordered, comments[i])
	}

	for _, c := range ordered {
		if c.ID == "" {
			// Defensive: a comment with no stable ID can't be deduped, so we
			// must not action it (it would re-fire every poll). Skip and log.
			w.logf("prwatcher: skipping comment with empty ID on PR #%d (cannot dedup)", c.PRNumber)
			continue
		}
		if actioned.Has(c.ID) {
			continue
		}

		// Cold start: suppress dispatch for comments authored before the
		// lookback cutoff. Record as actioned so they never dispatch, but take
		// no action. Comments with no creation timestamp are processed normally
		// (when in doubt we act). The cutoff applies only on the first poll.
		if coldStart && !c.CreatedAt.IsZero() && c.CreatedAt.Before(coldCutoff) {
			w.logf("prwatcher: cold-start suppress comment id=%s pr=#%d created=%s (older than cutoff)",
				shortID(c.ID), c.PRNumber, c.CreatedAt.Format(time.RFC3339))
			res.ColdStartSuppressed++
			actioned.Mark(c.ID, w.clock.Now())
			continue
		}

		class := Triage(c)
		w.logf("prwatcher: actioning comment id=%s pr=#%d path=%s class=%s", shortID(c.ID), c.PRNumber, c.Path, class)

		if err := w.action(c, class); err != nil {
			// Don't mark the comment actioned if we failed — the next poll
			// retries. Return so the operator sees the failure.
			return res, fmt.Errorf("prwatcher: action comment id=%s: %w", c.ID, err)
		}

		res.CommentsActioned++
		switch class {
		case ClassMechanical:
			res.Mechanical++
		case ClassJudgment:
			res.Gated++
		}
		actioned.Mark(c.ID, w.clock.Now())
	}

	if err := actioned.Save(); err != nil {
		return res, fmt.Errorf("prwatcher: save actioned: %w", err)
	}
	return res, nil
}

// action executes the dispatch sequence for a single triaged comment. Each
// side effect is best-effort within a comment; a partial failure returns an
// error so the comment is NOT marked actioned and the next poll retries the
// whole sequence. The sequence is therefore ordered so the most important,
// least-reversible side effect (bead creation) happens first; the ack reply is
// last because a missing ack is the most tolerable partial failure.
func (w *Watcher) action(c ReviewComment, class Class) error {
	title := w.beadTitle(c)
	desc := w.beadDescription(c, class)

	label := LabelNeedsHumanTriage
	if class == ClassMechanical {
		label = LabelPRComment
	}

	beadID, err := w.dispatcher.CreateBead(title, desc, label)
	if err != nil {
		return fmt.Errorf("create bead: %w", err)
	}

	switch class {
	case ClassMechanical:
		// Auto-dispatch: a fresh polecat fixes and re-pushes.
		if err := w.dispatcher.Sling(beadID, w.cfg.Rig); err != nil {
			return fmt.Errorf("sling %s: %w", beadID, err)
		}
	case ClassJudgment:
		// Gate: mail the mayor for human confirmation before any code change.
		subject := fmt.Sprintf("[HIGH] pr-review-comment needs triage: %s (PR #%d)", beadID, c.PRNumber)
		body := w.mayorBody(c, beadID)
		if err := w.mailer.SendMayor(subject, body); err != nil {
			return fmt.Errorf("mail mayor: %w", err)
		}
	}

	// Always ack the reviewer so they see the comment was picked up. This is
	// last and, if it fails, fails the whole action so the next poll retries —
	// but the bead/sling/mail above are idempotent enough that a retry is safe
	// (a duplicate bead is the worst case, bounded by the actioned ledger only
	// advancing on full success).
	if err := w.replier.Reply(c.PRNumber, w.ackBody(c, class, beadID)); err != nil {
		return fmt.Errorf("ack reply: %w", err)
	}

	w.logf("prwatcher: actioned comment id=%s pr=#%d bead=%s class=%s", shortID(c.ID), c.PRNumber, beadID, class)
	return nil
}

// beadTitle renders a concise bead title from the comment.
func (w *Watcher) beadTitle(c ReviewComment) string {
	loc := fmt.Sprintf("PR #%d", c.PRNumber)
	if c.Path != "" {
		loc = fmt.Sprintf("%s:%d in PR #%d", c.Path, c.Line, c.PRNumber)
	}
	summary := firstLine(c.Body)
	if summary == "" {
		summary = "(no comment text)"
	}
	return fmt.Sprintf("PR review comment on %s: %s", loc, truncate(summary, 80))
}

// beadDescription renders the full bead body with the comment context.
func (w *Watcher) beadDescription(c ReviewComment, class Class) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Auto-created from a GitHub PR review comment by %s.\n\n", w.cfg.Rig)
	fmt.Fprintf(&b, "Triage class: %s\n", class)
	fmt.Fprintf(&b, "PR:      #%d %s\n", c.PRNumber, c.PRTitle)
	if c.Author != "" {
		fmt.Fprintf(&b, "Author:  %s\n", c.Author)
	}
	if c.Path != "" {
		fmt.Fprintf(&b, "File:    %s:%d\n", c.Path, c.Line)
	}
	if c.URL != "" {
		fmt.Fprintf(&b, "Comment: %s\n", c.URL)
	}
	fmt.Fprintf(&b, "\n--- comment ---\n%s\n", strings.TrimSpace(c.Body))
	if class == ClassJudgment {
		fmt.Fprintf(&b, "\nThis comment requires human judgment. It was NOT auto-dispatched; "+
			"confirm the intended change before slinging.\n")
	}
	return b.String()
}

// mayorBody renders the judgment-class confirmation mail.
func (w *Watcher) mayorBody(c ReviewComment, beadID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A GitHub PR review comment on %s needs human triage.\n\n", w.cfg.Rig)
	fmt.Fprintf(&b, "Bead:    %s (labeled %s)\n", beadID, LabelNeedsHumanTriage)
	fmt.Fprintf(&b, "PR:      #%d %s\n", c.PRNumber, c.PRTitle)
	if c.Author != "" {
		fmt.Fprintf(&b, "Author:  %s\n", c.Author)
	}
	if c.Path != "" {
		fmt.Fprintf(&b, "File:    %s:%d\n", c.Path, c.Line)
	}
	if c.URL != "" {
		fmt.Fprintf(&b, "Comment: %s\n", c.URL)
	}
	fmt.Fprintf(&b, "\n--- comment ---\n%s\n\n", strings.TrimSpace(c.Body))
	fmt.Fprintf(&b, "The poller did NOT auto-dispatch this — it needs your judgment. "+
		"To action it: confirm the intended change, then `gt sling %s %s`.\n", beadID, w.cfg.Rig)
	return b.String()
}

// ackBody renders the reply posted back on the PR so the reviewer sees the
// comment was picked up.
func (w *Watcher) ackBody(c ReviewComment, class Class, beadID string) string {
	switch class {
	case ClassMechanical:
		return fmt.Sprintf("🤖 Gas Town picked this up as a mechanical change — created bead `%s` and dispatched a worker to fix and re-push. (%s)", beadID, c.URL)
	default:
		return fmt.Sprintf("🤖 Gas Town picked this up — created bead `%s` and flagged it for human triage (needs judgment before a change is made). (%s)", beadID, c.URL)
	}
}

func (w *Watcher) logf(format string, args ...any) {
	if w.out == nil {
		return
	}
	fmt.Fprintf(w.out, format+"\n", args...)
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// shortID returns a short, log-friendly prefix of an opaque comment node ID.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12] + "…"
	}
	return id
}

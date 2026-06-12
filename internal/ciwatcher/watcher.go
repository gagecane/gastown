package ciwatcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// LabelBrokeMainCI is added to every bead the watcher reopens after a
// post-merge CI failure. Operators can search `bd list --label=broke-main-ci`
// to triage outstanding regressions.
const LabelBrokeMainCI = "broke-main-ci"

// EventPush is the GitHub Actions trigger event for a commit landing on a
// branch — the canonical "post-merge" signal. Only push-event failures on the
// target branch represent a regression introduced by a recent merge that
// should freeze the queue. Scheduled-cron failures (E2E, Nightly) run on a
// timer against pre-existing main state and must NOT freeze the queue
// (gu-y94l1). The Event field is populated from gh's --json event for the
// GitHub fetcher; alternative hosts may leave it empty, in which case the
// watcher falls back to its legacy "freeze on any failure" behavior.
const EventPush = "push"

// DefaultRunLimit caps how many recent runs the watcher pulls per poll. Two
// minutes of polling at GitHub Actions' typical post-merge cadence yields
// at most a handful of runs; 50 is comfortable head-room.
const DefaultRunLimit = 50

// DefaultColdStartLookback bounds how far back the watcher will escalate on
// its first-ever poll for a rig (when no seen-runs ledger exists). Without
// this bound, a fresh daemon treats every historical CI failure as new and
// floods the mayor with stale broke-main-ci escalations (gs-qth). Runs that
// completed before now-lookback on a cold start are recorded as seen but not
// escalated. Two hours comfortably covers a daemon restart gap while keeping
// long-resolved history out of the inbox.
const DefaultColdStartLookback = 2 * time.Hour

// Config holds the static configuration for a Watcher. All fields are
// required unless noted otherwise.
type Config struct {
	// TownRoot is the absolute path to the town root. Used to locate the
	// freeze file and the seen-runs ledger.
	TownRoot string

	// Rig is the rig whose merge queue this watcher protects. The freeze
	// file path is per-rig so multiple rigs can run watchers concurrently
	// without colliding.
	Rig string

	// TargetBranch is the branch whose CI status drives the watcher.
	// Typically "main"; allowed to be overridden for forks that protect a
	// different default branch.
	TargetBranch string

	// RunLimit caps how many recent runs the fetcher returns per poll.
	// Defaults to DefaultRunLimit when zero.
	RunLimit int

	// ColdStartLookback bounds escalation on the first-ever poll for a rig
	// (no seen-runs ledger). On a cold start, runs that completed before
	// now-ColdStartLookback are recorded as seen but not escalated, so a
	// fresh daemon does not flood the mayor with stale historical failures.
	// Defaults to DefaultColdStartLookback when zero. Has no effect once a
	// ledger exists (warm polls process every unseen run as before).
	ColdStartLookback time.Duration
}

// Watcher orchestrates the post-merge CI watch loop. Construct with NewWatcher
// and call Process() once per poll cycle.
type Watcher struct {
	cfg     Config
	fetcher RunFetcher
	beads   BeadStore
	mailer  Mailer
	clock   Clock
	out     io.Writer
}

// NewWatcher constructs a Watcher. fetcher/beads/mailer must be non-nil; out
// may be nil (no logging) and clock may be nil (defaults to SystemClock).
func NewWatcher(cfg Config, fetcher RunFetcher, beads BeadStore, mailer Mailer, clock Clock, out io.Writer) *Watcher {
	if clock == nil {
		clock = SystemClock
	}
	if cfg.RunLimit == 0 {
		cfg.RunLimit = DefaultRunLimit
	}
	if cfg.ColdStartLookback == 0 {
		cfg.ColdStartLookback = DefaultColdStartLookback
	}
	if cfg.TargetBranch == "" {
		cfg.TargetBranch = "main"
	}
	return &Watcher{
		cfg:     cfg,
		fetcher: fetcher,
		beads:   beads,
		mailer:  mailer,
		clock:   clock,
		out:     out,
	}
}

// PollResult is the per-call summary, returned for logging and tests.
type PollResult struct {
	// RunsConsidered is the number of completed runs returned by the
	// fetcher.
	RunsConsidered int

	// RunsProcessed is the subset that the watcher acted on (i.e. not
	// filtered for branch and not already in the seen-runs ledger).
	RunsProcessed int

	// FailuresHandled counts runs that triggered the reopen+freeze path.
	FailuresHandled int

	// FreezeCleared is true when the watcher cleared an existing freeze
	// after observing a passing run on the target branch.
	FreezeCleared bool

	// FreezeWritten is true when the watcher wrote a new freeze (or
	// overwrote an existing one with newer metadata).
	FreezeWritten bool

	// ColdStartSuppressed counts runs that were recorded as seen but NOT
	// escalated because this was a cold start (no prior ledger) and the run
	// completed before the cold-start lookback cutoff. Always 0 on warm
	// polls.
	ColdStartSuppressed int

	// SupersededSuppressed counts failed runs that were recorded as seen but
	// NOT escalated because a later passing run on the target branch already
	// superseded the break (the regression was resolved and main advanced).
	// Applies on both cold and warm polls; see gs-218.
	SupersededSuppressed int

	// NonPushFailureSuppressed counts failed runs that were recorded as seen
	// but NOT escalated because the trigger event was not "push" (e.g.
	// scheduled-cron observability runs like E2E, Nightly Integration). These
	// runs do not represent a post-merge regression — they fail against
	// pre-existing main state on a timer — so freezing the queue on them
	// blocks every MR landing until a human unfreezes (gu-y94l1). Empty Event
	// strings still flow through the legacy path.
	NonPushFailureSuppressed int

	// Skipped is true when the rig has no pollable Actions runs — the repo
	// does not exist (e.g. origin points at a fork that was never created) or
	// Actions is disabled. The watcher treats this as a benign no-op rather
	// than a hard error so the plugin doesn't surface a 404 every cooldown
	// cycle (gu-qfhvw). SkipReason carries the detail for the summary line.
	Skipped    bool
	SkipReason string
}

// Process inspects the most recent completed runs on the target branch and
// applies the reopen-and-freeze / clear-freeze policy. Caller is responsible
// for invoking it on a schedule; Process itself does not loop.
func (w *Watcher) Process(ctx context.Context) (PollResult, error) {
	res := PollResult{}

	runs, err := w.fetcher.CompletedRuns(ctx, w.cfg.TargetBranch, w.cfg.RunLimit)
	if err != nil {
		// A missing repo or disabled Actions (HTTP 404) is a benign,
		// persistent condition, not a transient fetch failure. Report it as a
		// clean skip so the poll plugin records a success receipt instead of
		// failing every cooldown cycle (gu-qfhvw).
		if errors.Is(err, ErrRunsUnavailable) {
			res.Skipped = true
			res.SkipReason = err.Error()
			w.logf("ciwatcher: rig=%s skipped — %v", w.cfg.Rig, err)
			return res, nil
		}
		return res, fmt.Errorf("ciwatcher: fetch runs: %w", err)
	}
	res.RunsConsidered = len(runs)

	seen, err := LoadSeenRuns(w.cfg.TownRoot, w.cfg.Rig)
	if err != nil {
		return res, fmt.Errorf("ciwatcher: load seen-runs: %w", err)
	}

	// Cold start: on the first-ever poll for a rig there is no ledger, so
	// every historical run looks new. Bound escalation to a recent window so
	// a fresh (or rebuilt) daemon doesn't re-escalate long-resolved failures
	// across all of CI history (gs-qth).
	coldStart := seen.Fresh()
	var coldCutoff time.Time
	if coldStart {
		coldCutoff = w.clock.Now().Add(-w.cfg.ColdStartLookback)
		w.logf("ciwatcher: cold start (no seen-runs ledger for rig=%s) — suppressing escalation for runs completed before %s",
			w.cfg.Rig, coldCutoff.Format(time.RFC3339))
	}

	// Find the most recent passing run on the target branch, scoped PER
	// WORKFLOW. A failure that completed before its own workflow's latest
	// success has been superseded: that workflow went green again afterwards,
	// so the break is resolved and re-escalating it would just flood the mayor
	// with stale broke-main-ci (gs-218). In the merge-queue model main freezes
	// on a break, so a later green run means the queue advanced past the
	// failing commit. Unlike the cold-start cutoff, this guard applies on warm
	// polls too — so a ledger rebuild or a wide fetch window that re-surfaces
	// an old, already-resolved failure does not re-escalate it.
	//
	// Scoping is per-workflow because a branch carries multiple independent
	// workflows (e.g. "CI" and "Windows CI"). A green "Windows CI" run must NOT
	// supersede a red "CI" run on the same SHA — they validate different
	// things. Using a branch-global latest-success let a passing workflow mask
	// a persistently failing one, landing breakage silently (gu-t1z17). Runs
	// with an empty Workflow name share a single bucket so non-GitHub hosts that
	// don't report a workflow keep the legacy branch-global behavior.
	latestSuccessByWorkflow := make(map[string]time.Time)
	for _, run := range runs {
		if run.Conclusion != ConclusionSuccess {
			continue
		}
		if !strings.EqualFold(run.Branch, w.cfg.TargetBranch) {
			continue
		}
		if run.CompletedAt.IsZero() {
			continue
		}
		if run.CompletedAt.After(latestSuccessByWorkflow[run.Workflow]) {
			latestSuccessByWorkflow[run.Workflow] = run.CompletedAt
		}
	}

	// Process oldest-to-newest so a fail-then-pass sequence in a single
	// poll resolves to "no freeze" (the pass clears the failure's freeze).
	// The fetcher returns newest-first, so reverse here.
	ordered := make([]CIRun, 0, len(runs))
	for i := len(runs) - 1; i >= 0; i-- {
		ordered = append(ordered, runs[i])
	}

	for _, run := range ordered {
		if !strings.EqualFold(run.Branch, w.cfg.TargetBranch) {
			// The fetcher SHOULD only return target-branch runs, but
			// GitHub Actions occasionally surfaces workflow_dispatch runs
			// with no branch attribution. Skip defensively.
			continue
		}
		if seen.Has(run.ID) {
			continue
		}

		// On a cold start, suppress escalation for runs that completed
		// before the lookback cutoff: record them as seen so they never
		// escalate, but take no action. Runs with no completion timestamp
		// are processed normally — when in doubt we'd rather act than
		// silently drop a genuine break. The cutoff only applies on the
		// first poll; subsequent (warm) polls process every unseen run.
		if coldStart && !run.CompletedAt.IsZero() && run.CompletedAt.Before(coldCutoff) {
			w.logf("ciwatcher: cold-start suppress run id=%s sha=%s completed=%s (older than cutoff)",
				run.ID, shortSHA(run.HeadSHA), run.CompletedAt.Format(time.RFC3339))
			res.ColdStartSuppressed++
			seen.Mark(run.ID, w.clock.Now())
			continue
		}

		// Suppress a failed run whose break was already superseded by a later
		// passing run OF THE SAME WORKFLOW on the target branch (gs-218).
		// Record as seen but take no action: no reopen, no mail, no freeze. The
		// current break — the newest failure of its workflow, which by
		// definition has no later success of that workflow — is never
		// suppressed, so a live regression still escalates promptly. Scoping by
		// workflow is what stops a green "Windows CI" from masking a red "CI"
		// (gu-t1z17). Runs with no completion timestamp are processed normally
		// (we'd rather act than silently drop a genuine break).
		if latest, ok := latestSuccessByWorkflow[run.Workflow]; ok &&
			run.Conclusion.IsFailureLike() && !run.CompletedAt.IsZero() &&
			run.CompletedAt.Before(latest) {
			w.logf("ciwatcher: superseded suppress run id=%s sha=%s workflow=%q completed=%s (later passing run at %s)",
				run.ID, shortSHA(run.HeadSHA), run.Workflow, run.CompletedAt.Format(time.RFC3339), latest.Format(time.RFC3339))
			res.SupersededSuppressed++
			seen.Mark(run.ID, w.clock.Now())
			continue
		}

		// Suppress failures from non-push events (gu-y94l1). Scheduled-cron
		// workflows (E2E, Nightly Integration) run on a TIMER against main's
		// existing state, not against a specific MR. When they fail on a
		// pre-existing condition, every cron run spuriously freezes the queue
		// and forces manual unfreeze. The freeze is only meaningful for
		// per-merge gate failures, identified by event="push" on the target
		// branch. Empty Event (host did not report it) flows through the
		// legacy path so non-GitHub backends still get coverage. Successes
		// are NOT scoped — any green run on main, regardless of trigger,
		// indicates main is healthy and can clear a freeze.
		if run.Conclusion.IsFailureLike() && run.Event != "" && run.Event != EventPush {
			w.logf("ciwatcher: non-push failure suppressed run id=%s sha=%s event=%s workflow=%q (queue freeze is push-event only)",
				run.ID, shortSHA(run.HeadSHA), run.Event, run.Workflow)
			res.NonPushFailureSuppressed++
			seen.Mark(run.ID, w.clock.Now())
			continue
		}

		res.RunsProcessed++

		w.logf("ciwatcher: processing run id=%s sha=%s conclusion=%s event=%s", run.ID, shortSHA(run.HeadSHA), run.Conclusion, run.Event)

		switch {
		case run.Conclusion.IsFailureLike():
			if err := w.handleFailure(run); err != nil {
				// Don't mark the run as seen if we failed to act — we
				// want the next poll to retry. Return so the operator
				// sees the failure.
				return res, fmt.Errorf("ciwatcher: handle failure run=%s: %w", run.ID, err)
			}
			res.FailuresHandled++
			res.FreezeWritten = true
		case run.Conclusion == ConclusionSuccess:
			cleared, err := w.handleSuccess(run)
			if err != nil {
				return res, fmt.Errorf("ciwatcher: handle success run=%s: %w", run.ID, err)
			}
			if cleared {
				res.FreezeCleared = true
			}
		default:
			w.logf("ciwatcher: skipping run id=%s with conclusion=%s (no-op)", run.ID, run.Conclusion)
		}

		seen.Mark(run.ID, w.clock.Now())
	}

	if err := seen.Save(); err != nil {
		return res, fmt.Errorf("ciwatcher: save seen-runs: %w", err)
	}
	return res, nil
}

// handleFailure executes the reopen+mail+freeze sequence for a single failed
// run. Each side effect is best-effort: a partial failure is reported to the
// caller so the next poll can retry from a fresh state.
func (w *Watcher) handleFailure(run CIRun) error {
	beadID := ExtractBeadID(run.HeadCommitSubject)
	commitDesc := shortSHA(run.HeadSHA)
	if run.HeadCommitSubject != "" {
		commitDesc = fmt.Sprintf("%s (%s)", shortSHA(run.HeadSHA), run.HeadCommitSubject)
	}

	// Reopen the bead if we could attribute the commit. A missing bead
	// (extracted ID does not resolve) is reported but does not block the
	// freeze — the freeze is the more important side effect.
	if beadID != "" {
		exists, err := w.beads.Exists(beadID)
		if err != nil {
			w.logf("ciwatcher: bead Exists(%s): %v", beadID, err)
		} else if !exists {
			w.logf("ciwatcher: extracted bead %q not found; will mail mayor without reopen", beadID)
			beadID = ""
		}
	}

	if beadID != "" {
		if err := w.beads.Reopen(beadID); err != nil {
			return fmt.Errorf("reopen %s: %w", beadID, err)
		}
		if err := w.beads.AddLabel(beadID, LabelBrokeMainCI); err != nil {
			return fmt.Errorf("label %s: %w", beadID, err)
		}
		note := fmt.Sprintf("broke-main-ci: run=%s url=%s commit=%s", run.ID, run.URL, commitDesc)
		if err := w.beads.AppendNote(beadID, note); err != nil {
			// Note failure is non-fatal — the bead is reopened, mayor
			// will be mailed, freeze written. We log and continue.
			w.logf("ciwatcher: append note %s: %v", beadID, err)
		}
	}

	// Mail mayor. Subject deliberately matches the bead description's
	// "broke-main-ci: <bead-id> — <run-url>" convention.
	subject := w.mayorSubject(beadID, run)
	body := w.mayorBody(beadID, run, commitDesc)
	if err := w.mailer.SendMayor(subject, body); err != nil {
		return fmt.Errorf("mail mayor: %w", err)
	}

	reason := "broke-main-ci"
	if beadID != "" {
		reason = fmt.Sprintf("broke-main-ci: %s", beadID)
	}
	freeze := FreezeFile{
		Rig:       w.cfg.Rig,
		FrozenAt:  w.clock.Now(),
		Reason:    reason,
		BeadID:    beadID,
		Workflow:  run.Workflow,
		CommitSHA: run.HeadSHA,
		RunID:     run.ID,
		RunURL:    run.URL,
	}
	if err := WriteFreeze(w.cfg.TownRoot, freeze); err != nil {
		return fmt.Errorf("write freeze: %w", err)
	}

	w.logf("ciwatcher: froze MQ for rig=%s bead=%s run=%s", w.cfg.Rig, beadID, run.ID)
	return nil
}

// handleSuccess clears an existing freeze when a successful run arrives on the
// target branch. Returns (true, nil) when a freeze was actually present and
// removed; (false, nil) when there was no freeze to clear.
func (w *Watcher) handleSuccess(run CIRun) (bool, error) {
	frozen, err := IsFrozen(w.cfg.TownRoot, w.cfg.Rig)
	if err != nil {
		return false, fmt.Errorf("check freeze: %w", err)
	}
	if !frozen {
		return false, nil
	}
	prior, err := ReadFreeze(w.cfg.TownRoot, w.cfg.Rig)
	if err != nil {
		// Don't fail just because we couldn't decode the prior freeze;
		// still clear it.
		w.logf("ciwatcher: read prior freeze: %v", err)
	}
	// Scope the clear to the freeze's workflow (gu-t1z17). A freeze written for
	// "CI" must NOT be cleared by a green "Windows CI" run — that workflow's
	// failure is still unresolved. We only require a match when BOTH the freeze
	// and the success run carry a workflow name; an empty name on either side
	// falls back to legacy branch-global clearing (a green run of any workflow,
	// or a freeze from a host that didn't record one, clears).
	if prior != nil && prior.Workflow != "" && run.Workflow != "" &&
		!strings.EqualFold(prior.Workflow, run.Workflow) {
		w.logf("ciwatcher: success run id=%s workflow=%q does not match freeze workflow=%q — leaving freeze in place",
			run.ID, run.Workflow, prior.Workflow)
		return false, nil
	}
	if err := ClearFreeze(w.cfg.TownRoot, w.cfg.Rig); err != nil {
		return false, fmt.Errorf("clear freeze: %w", err)
	}
	subject := fmt.Sprintf("broke-main-ci CLEARED: %s", w.cfg.Rig)
	body := fmt.Sprintf("Main is healthy again on %s.\n\nClearing run: %s\nCommit: %s\nRun URL: %s\n",
		w.cfg.Rig, run.ID, shortSHA(run.HeadSHA), run.URL)
	if prior != nil {
		body += fmt.Sprintf("\nPrior freeze: bead=%s run=%s frozen_at=%s\n",
			prior.BeadID, prior.RunID, prior.FrozenAt.Format(time.RFC3339))
	}
	if err := w.mailer.SendMayor(subject, body); err != nil {
		// Mail failure does not roll the clear back — the queue is unfrozen
		// either way. Log and surface the error to the caller.
		return true, fmt.Errorf("mail mayor (cleared): %w", err)
	}
	return true, nil
}

// mayorSubject formats the high-priority subject the bead description calls
// for. `[HIGH]` is the convention recognized by the mail-protocol triage.
func (w *Watcher) mayorSubject(beadID string, run CIRun) string {
	if beadID != "" {
		return fmt.Sprintf("[HIGH] broke-main-ci: %s — %s", beadID, run.URL)
	}
	return fmt.Sprintf("[HIGH] broke-main-ci (unknown bead) — %s", run.URL)
}

// mayorBody renders the operator-facing notification.
func (w *Watcher) mayorBody(beadID string, run CIRun, commitDesc string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Post-merge CI failure on %s/%s.\n\n", w.cfg.Rig, w.cfg.TargetBranch)
	fmt.Fprintf(&b, "Run:    %s\n", run.URL)
	fmt.Fprintf(&b, "Run ID: %s\n", run.ID)
	if run.Workflow != "" {
		fmt.Fprintf(&b, "Workflow: %s\n", run.Workflow)
	}
	fmt.Fprintf(&b, "Commit: %s\n", commitDesc)
	fmt.Fprintf(&b, "Conclusion: %s\n", run.Conclusion)
	if !run.CompletedAt.IsZero() {
		fmt.Fprintf(&b, "Completed: %s\n", run.CompletedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "\n")
	if beadID != "" {
		fmt.Fprintf(&b, "Reopened bead %s with label %q.\n", beadID, LabelBrokeMainCI)
	} else {
		fmt.Fprintf(&b, "Could not attribute commit to a bead — manual triage required.\n")
	}
	fmt.Fprintf(&b, "Merge queue for rig %s is now FROZEN. Refinery will refuse to process MRs until the freeze is cleared (clears automatically on next passing run, or manually via `gt ci-watcher unfreeze`).\n", w.cfg.Rig)
	return b.String()
}

func (w *Watcher) logf(format string, args ...any) {
	if w.out == nil {
		return
	}
	fmt.Fprintf(w.out, format+"\n", args...)
}

// shortSHA returns the first 8 chars of a SHA, or the SHA itself if shorter.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

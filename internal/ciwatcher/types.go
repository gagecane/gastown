package ciwatcher

import (
	"context"
	"time"
)

// Conclusion mirrors GitHub Actions' `conclusion` field on a completed run.
// We accept other host backends in principle (Bitbucket Pipelines, Crux),
// so the enum is host-agnostic; the gh-CLI fetcher maps the host string to
// these values.
type Conclusion string

const (
	// ConclusionSuccess means the run completed and all jobs passed.
	ConclusionSuccess Conclusion = "success"

	// ConclusionFailure means at least one required job failed. This is the
	// trigger condition for the watcher's main code path.
	ConclusionFailure Conclusion = "failure"

	// ConclusionCancelled means the run was cancelled before completion.
	// We treat cancelled as "no signal" — a cancelled run does not freeze
	// the queue. If a polecat repeatedly cancels post-merge runs that's a
	// separate problem worth its own bead.
	ConclusionCancelled Conclusion = "cancelled"

	// ConclusionTimedOut is a GitHub Actions terminal state; we treat it
	// like a failure for queue-freezing purposes.
	ConclusionTimedOut Conclusion = "timed_out"

	// ConclusionStartupFailure indicates the runner failed to start the
	// workflow. Treated as failure (the commit's CI did not pass).
	ConclusionStartupFailure Conclusion = "startup_failure"

	// ConclusionUnknown is the catch-all when the host returns a value we
	// don't recognize. The watcher logs it but does NOT freeze; freezing on
	// unknown states risks DoS-ing the queue when GitHub adds a new field.
	ConclusionUnknown Conclusion = "unknown"
)

// IsFailureLike reports whether the conclusion should trigger the
// reopen+freeze path. Cancelled and unknown deliberately fall through.
func (c Conclusion) IsFailureLike() bool {
	switch c {
	case ConclusionFailure, ConclusionTimedOut, ConclusionStartupFailure:
		return true
	}
	return false
}

// CIRun is the watcher's host-agnostic view of a CI run on the target branch.
type CIRun struct {
	// ID is the host-assigned run identifier as a string. For GitHub Actions
	// this is the numeric run ID (e.g. "12345678901").
	ID string

	// HeadSHA is the commit SHA whose CI this run validated.
	HeadSHA string

	// HeadCommitSubject is the first line of the commit message for HeadSHA.
	// Used to extract the responsible bead. The fetcher is responsible for
	// populating this; if the host does not return a subject inline we fall
	// back to `git log -1 --format=%s <SHA>` in the gh-CLI path.
	HeadCommitSubject string

	// Conclusion is the terminal state of the run.
	Conclusion Conclusion

	// CompletedAt is when the run finished (UTC).
	CompletedAt time.Time

	// URL is the human-readable URL for the run, used in mail bodies.
	URL string

	// Workflow is the workflow name (e.g. "build", "test") for context in
	// notifications. May be empty.
	Workflow string

	// Branch is the branch the run was triggered on. The watcher filters
	// for the configured target branch (typically "main") before invoking
	// Process(); this field is informational.
	Branch string
}

// RunFetcher abstracts the host-specific "list recent completed runs on
// branch" call. Tests inject a fake; production uses the gh-CLI client.
type RunFetcher interface {
	// CompletedRuns returns runs on `branch` whose status is "completed",
	// most recent first. `limit` bounds how many runs to return; the
	// fetcher MUST honor it but MAY return fewer.
	CompletedRuns(ctx context.Context, branch string, limit int) ([]CIRun, error)
}

// BeadStore is the subset of internal/beads we need: reopen and label add.
// The interface keeps the watcher independent of the bd CLI vs. in-process
// store distinction — both implementations satisfy this contract.
type BeadStore interface {
	// Reopen flips a closed bead back to open status. Idempotent: a bead
	// already open returns nil.
	Reopen(beadID string) error

	// AddLabel attaches a label to a bead. Idempotent.
	AddLabel(beadID, label string) error

	// AppendNote appends a note to a bead's notes field. The watcher writes
	// a single line per CI failure summarizing run URL + commit so a human
	// can correlate without paging through the events log.
	AppendNote(beadID, note string) error

	// Exists reports whether a bead with the given ID is known. Used to
	// distinguish "couldn't extract" from "extracted but bead is gone"; the
	// latter is a Mayor-grade anomaly.
	Exists(beadID string) (bool, error)
}

// Mailer abstracts mail/nudge delivery. We use mail (not nudge) for the
// mayor notification because broke-main-ci is an audit-grade event that
// must survive session death — the mayor may need to re-check it next
// patrol.
type Mailer interface {
	// SendMayor delivers a mail to the mayor. Returns an error if delivery
	// fails so the watcher can log + retry next poll.
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

// SystemClock is the production singleton clock. Exposed so callers don't
// need to know the realClock type.
var SystemClock Clock = realClock{}

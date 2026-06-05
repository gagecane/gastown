package sling

import "strings"

// This file classifies `gt sling` failure stderr lines into terminal-vs-transient
// categories so EVERY dispatch producer can react consistently. The convoy
// stranded-feed loop (internal/daemon/convoy_manager.go) originated these
// predicates, but 6+ independent producers shell out to `gt sling` in their own
// re-feed loops (convoy_manager, deacon/redispatch, deacon/feed_stranded,
// convoy/operations, synthesis, formula) plus plugins. When the predicates lived
// private to convoy_manager, each producer reimplemented (or omitted) its own
// skip logic — the structural gap behind the gu-q1wzq re-dispatch storm
// (~6,508 wasted sling-retries/day → Dolt connection storm → data-plane death)
// and its still-open sibling gu-u7rsg. Centralizing them here lets any producer
// distinguish "never retry this" from "transient, back off" identically.
//
// All predicates match on the FIRST LINE of sling's stderr (case-insensitive,
// quoting-tolerant) and are pure — no I/O, safe to call on every scan tick.
//
// NOTE: internal/autotestpr has a separate isBeadNotFoundError(err error) that
// operates on a Go error, not a stderr string. These are intentionally distinct:
// that one classifies bd-subprocess errors; these classify gt-sling stderr.

// IsBeadNotFoundError reports whether a sling stderr line indicates the target
// bead does not exist. Matches the error shapes produced by verifyBeadExists and
// bd show ("bead 'xxx' not found", "bead xxx not found", and the bd-direct
// "no issue found matching 'xxx'" form). (gu-f0gq)
func IsBeadNotFoundError(stderrLine string) bool {
	if stderrLine == "" {
		return false
	}
	s := strings.ToLower(stderrLine)
	// gt sling: "bead 'xxx' not found" / "bead xxx not found"
	if strings.Contains(s, "not found") &&
		(strings.Contains(s, "bead ") || strings.Contains(s, "issue ")) {
		return true
	}
	// bd: "no issue found matching"
	if strings.Contains(s, "no issue found matching") {
		return true
	}
	return false
}

// IsClosedBeadSlingError reports whether a sling stderr line indicates the
// target bead is already closed/tombstoned — i.e. the work completed between the
// stranded scan and this feed (a TOCTOU race). Matches sling's closed-bead guard:
// "bead <id> is closed (work already completed)" / "... is tombstone (work
// already completed)". This is Category A from gu-y6ild: a closed tracked bead
// should trigger a convoy completion check, not a Mayor escalation.
func IsClosedBeadSlingError(stderrLine string) bool {
	if stderrLine == "" {
		return false
	}
	return strings.Contains(strings.ToLower(stderrLine), "work already completed")
}

// IsStructuralNonWorkSlingError reports whether a sling stderr line indicates the
// target bead is a structural non-work item that can never be dispatched as a
// convoy step (Category C from gu-y6ild). These are permanent data-shape
// rejections from sling's guards — re-attempting every scan is futile, and
// escalating to the Mayor every time is pure toil. The bead should be
// auto-untracked from the convoy so it can progress and auto-close.
//
// Matched rejections (sling.go guards):
//   - epic container (epic title / phase:epic label / type=epic)
//   - parent of open children (container, not a leaf work item)
//   - identity/system bead (gt:agent label or polecat/refinery title)
//   - sling-context wrapper (gt:sling-context label)
//   - flag-like garbage title (flag-parsing bug)
//   - polecat-owned bead (self-filed by a polecat)
//
// Deliberately NOT matched (left to escalate as genuinely-ambiguous, needing
// Mayor judgment): mayor-only / no-polecat beads, unroutable targets, and
// capacity-scheduler failures.
func IsStructuralNonWorkSlingError(stderrLine string) bool {
	if stderrLine == "" {
		return false
	}
	s := strings.ToLower(stderrLine)
	switch {
	case strings.Contains(s, "is an epic container"):
		return true
	case strings.Contains(s, "has open children"):
		return true
	case strings.Contains(s, "is an identity/system bead"):
		return true
	case strings.Contains(s, "is a sling-context wrapper"):
		return true
	case strings.Contains(s, "looks like a cli flag"):
		return true
	case strings.Contains(s, "is owned by a polecat"):
		return true
	}
	return false
}

// IsActivelyWorkedSlingError reports whether a sling stderr line indicates the
// target bead is already hooked / in_progress to a LIVE agent — i.e. the work is
// being performed right now, not wedged (gs-2dr). sling refuses with this error
// ONLY after its own dead-agent detection declines to auto-force: a hooked bead
// whose agent's session is gone is auto-re-slung, so reaching this error path
// means the hooked agent is alive. The step is progressing and must NOT be
// escalated as 'cannot dispatch / will never progress'.
//
// Matched sling-guard rejection shapes:
//   - sling.go:       "bead <id> is already hooked to <agent>"
//   - sling.go:       "bead <id> is already in_progress to <agent>"
//   - sling_dispatch: "already hooked (use --force to re-sling)"
//   - sling_dispatch: "already in_progress (use --force to re-sling)"
//
// Deliberately NOT matched: "already pinned" — a pinned bead is an explicit
// do-not-dispatch reference, a structural state the Mayor should still see.
func IsActivelyWorkedSlingError(stderrLine string) bool {
	if stderrLine == "" {
		return false
	}
	s := strings.ToLower(stderrLine)
	return strings.Contains(s, "already hooked") ||
		strings.Contains(s, "already in_progress")
}

// IsDoNotDispatchSlingError reports whether a sling stderr line indicates the
// target bead is a do-not-dispatch / pinned reference tripwire — a permanent live
// safety gate that the scheduler refuses by design and that must stay OPEN
// (sling_dispatch.go / sling_schedule.go: "is a do-not-dispatch / pinned
// reference tripwire ... refusing to schedule"). Unlike an actively-worked bead
// (which is progressing and will close on its own), a tripwire NEVER becomes
// dispatchable — its labels/type are intentional. Re-feeding it every scan is
// pure waste: gu-q1wzq observed ~3,000 such retries in a day (≈383 per tripwire
// bead) as the dominant share of a 6,508-retry convoy storm that spiked host
// load. These must be permanently untracked from the convoy, not backed off.
func IsDoNotDispatchSlingError(stderrLine string) bool {
	if stderrLine == "" {
		return false
	}
	s := strings.ToLower(stderrLine)
	return strings.Contains(s, "do-not-dispatch") ||
		strings.Contains(s, "reference tripwire")
}

// SlingFailureClass categorizes a sling stderr line into a single terminal-vs-
// transient disposition so producers can switch on one value instead of chaining
// predicates (and risk ordering bugs). Ordering matters: closed and not-found are
// checked before the broader structural/tripwire matches.
type SlingFailureClass int

const (
	// SlingFailureUnknown is a genuinely-ambiguous failure (unroutable target,
	// mayor-only assertion, transient infra) that needs human/Mayor judgment.
	// Producers should escalate ONCE, not re-feed every scan.
	SlingFailureUnknown SlingFailureClass = iota
	// SlingFailureNotFound: the bead was deleted/reaped. Drop it (ghost).
	SlingFailureNotFound
	// SlingFailureClosed: the bead closed between scan and feed (TOCTOU). Run a
	// completion check; it drops from the ready set on its own.
	SlingFailureClosed
	// SlingFailureStructuralNonWork: permanent data-shape rejection. Untrack.
	SlingFailureStructuralNonWork
	// SlingFailureDoNotDispatch: permanent safety-gate tripwire. Untrack.
	SlingFailureDoNotDispatch
	// SlingFailureActivelyWorked: hooked/in_progress to a live agent. Suppress
	// escalation and back off; it will close on its own.
	SlingFailureActivelyWorked
)

// ClassifySlingFailure maps a sling stderr line to its SlingFailureClass.
// Checked in priority order so the most specific/terminal disposition wins.
func ClassifySlingFailure(stderrLine string) SlingFailureClass {
	switch {
	case IsBeadNotFoundError(stderrLine):
		return SlingFailureNotFound
	case IsClosedBeadSlingError(stderrLine):
		return SlingFailureClosed
	case IsDoNotDispatchSlingError(stderrLine):
		return SlingFailureDoNotDispatch
	case IsStructuralNonWorkSlingError(stderrLine):
		return SlingFailureStructuralNonWork
	case IsActivelyWorkedSlingError(stderrLine):
		return SlingFailureActivelyWorked
	default:
		return SlingFailureUnknown
	}
}

// IsTerminalSlingFailure reports whether a failure class is permanent — the bead
// will NEVER become dispatchable by retrying, so a producer must stop re-feeding
// it (drop/untrack), not back off. Actively-worked is NOT terminal (it resolves
// on its own); unknown is NOT terminal (needs judgment, escalate once).
func IsTerminalSlingFailure(c SlingFailureClass) bool {
	switch c {
	case SlingFailureNotFound, SlingFailureClosed,
		SlingFailureStructuralNonWork, SlingFailureDoNotDispatch:
		return true
	default:
		return false
	}
}

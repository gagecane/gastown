package sling

import (
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// ContextTTL is the maximum age of a sling context before it's considered
// stale and ignored by the scheduler's scheduled-set scan. This prevents
// orphaned sling contexts (from failed spawns or throttled dispatches) from
// permanently blocking tasks. See GH#2279.
const ContextTTL = 30 * time.Minute

// ContextOlderThan reports whether the context's CreatedAt timestamp is older
// than the given TTL, relative to now. Unparseable or empty timestamps return
// false (fail-closed — don't treat an unknown-age context as stale).
func ContextOlderThan(ctx *beads.Issue, now time.Time, ttl time.Duration) bool {
	if ctx == nil || ctx.CreatedAt == "" {
		return false
	}
	created, err := time.Parse(time.RFC3339, ctx.CreatedAt)
	if err != nil {
		return false
	}
	return now.Sub(created) > ttl
}

// ShouldReattachFormula reports whether an already-scheduled bead's staged
// formula should be replaced in place (gs-am8 GAP 2). Re-attach only when the
// caller passed --force AND is requesting a formula that differs from the one
// currently staged — so a bead stuck on the wrong formula (e.g. a review gate
// staged with the default mol-polecat-work) can be corrected without an
// unschedule/reschedule dance. Without --force, or with the same formula, the
// existing no-op (idempotent) behavior stands.
func ShouldReattachFormula(force bool, requestedFormula string, existing *capacity.SlingContextFields) bool {
	return force && existing != nil && requestedFormula != existing.Formula
}

// IsStaleOrFailedContext reports whether an existing open sling context should
// be treated as expired rather than a healthy in-flight dispatch (gu-rm08l).
// True when the context recorded any transient dispatch failure
// (dispatch_failures>0 — a spawn hiccup, bd-list read throttle, etc.) or when
// it has aged past ContextTTL. Either condition means a re-sling must NOT
// no-op: the parked context is closed and a fresh one created so the work bead
// returns to the dispatchable pool.
//
// This mirrors the scheduler's scheduled-set TTL logic, which already ignores
// such contexts when convoy/epic/stranded scans decide whether to re-sling.
// Without this, the scans re-sling but scheduleBead no-ops on the lingering
// context, parking the bead indefinitely. recordDispatchFailure already
// excludes "already hooked/in_progress" errors, so dispatch_failures>0 implies
// a genuine spawn failure, not active work being performed.
func IsStaleOrFailedContext(ctx *beads.Issue, fields *capacity.SlingContextFields, now time.Time) bool {
	if fields != nil && fields.DispatchFailures > 0 {
		return true
	}
	return ContextOlderThan(ctx, now, ContextTTL)
}

// StaleContextReslingReason returns the close reason for a stale/failed context
// being recycled by a re-sling, distinguishing transient-failure expiry from
// plain TTL expiry for observability.
func StaleContextReslingReason(fields *capacity.SlingContextFields) string {
	if fields != nil && fields.DispatchFailures > 0 {
		return fmt.Sprintf("failed-context-resling (dispatch_failures=%d)", fields.DispatchFailures)
	}
	return "stale-context-resling (ttl-expired)"
}

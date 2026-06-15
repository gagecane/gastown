# PRD: Inbound Sentry Error Ingestion for Gas Town

## Problem Statement

Production errors for the `lia` org — primarily `lia-health-backend` — land in
Sentry, but they don't automatically flow into the Gas Town work pipeline. Today
someone has to notice a Sentry issue, decide it's actionable, and hand-file a
bead. That's slow, lossy, and inconsistent: high-volume or high-severity errors
can sit in Sentry for hours without anyone in the gt workflow owning them.

We want production error signal to become tracked work automatically. A gt daemon
patrol should poll the Sentry API, triage what it finds, and file (or update) a
bead carrying the actionable signal, so the existing dispatch/refinery machinery
can route it like any other work.

**Direction is strictly inbound.** Gas Town *consumes* Sentry. It never emits to
Sentry. This supersedes the prior `sentry-gastown` effort, which was mis-framed
as instrumenting gt itself with the Sentry SDK (outbound). All of that review's
PHI scrub-boundary blockers (BeforeSend, CaptureException, gt.prompt beacon,
flush-before-exit, goroutine-recover, separate-project) are **N/A here** because
we are reading, not emitting.

**For whom:** the gt operators / maintainers who currently triage `lia` production
errors by hand, and the refinery/dispatch pipeline that wants error work as beads.

**Why now:** the prior (outbound) framing was wrong and is being replaced; the
correct inbound framing unblocks a much simpler, lower-risk integration.

## Goals

1. **Poll Sentry on an interval as a gt daemon patrol.** Target the `lia` org,
   primary project `lia-health-backend`, with the design extensible to all `lia`
   projects (project list is config-driven, not hardcoded to one).
2. **Triage and route each issue, then file a bead** carrying the actionable
   signal: title, culprit, level, event count, frequency, first-seen, last-seen,
   and a permalink back to the Sentry issue.
3. **Dedup BEFORE bead creation**, keyed on a stable Sentry identifier (Sentry
   issue id or fingerprint). Recurring / already-filed issues update the existing
   bead rather than creating a duplicate, and the bead stores `sentry_issue_id`
   for future correlation.
4. **Config-driven, no secrets hardcoded.** API token, org/project selection,
   poll interval, and severity-or-volume thresholds all come from config.

Success looks like: an actionable Sentry issue becomes a bead within one poll
interval; a recurring issue updates its existing bead instead of spawning a
duplicate; no API token ever appears in source or logs.

## Non-Goals

- **Outbound instrumentation** — gt emitting its own errors to Sentry. Explicitly
  out of scope; this is the mis-framing being superseded.
- **Auto-dispatching fixes.** v1 is triage-and-file only. No automatic assignment
  of a polecat to fix the error.
- **Re-scrubbing PHI.** See PHI posture below — the upstream app already scrubs.
- **Replacing Sentry alerting.** This complements Sentry's own alerts; it does not
  attempt to be a notification system.

## PHI Posture (Assumption)

The `lia` app already scrubs PHI upstream *before* sending to Sentry, under the
existing BAA. Therefore ingested reports are PHI-safe at the source, and **gastown
needs no scrub boundary of its own.** This is recorded as an assumption to be
revisited *only if* the upstream guarantee changes. (This is why the 12 PHI
blockers from the outbound review do not apply: there is no gt-side emission path
that could leak PHI.)

## User Stories / Scenarios

- **As a gt operator,** when a new error crosses the threshold in
  `lia-health-backend`, I want a bead filed automatically so it enters the work
  pipeline without me watching Sentry.
- **As a gt operator,** when an already-filed error recurs (more events, new
  last-seen), I want its existing bead updated — not a flood of duplicate beads.
- **As the dispatch/refinery pipeline,** I want error beads that look like normal
  work beads (title, severity signal, link to source) so I can route them.
- **As a security/compliance reviewer,** I want assurance that no API token is
  stored in source or printed, and that the PHI assumption is documented.

## Constraints

- **Technical:** Go codebase; the patrol runs inside the existing gt daemon patrol
  framework (same shape as witness/deacon/refinery patrols). Must respect Sentry
  API rate limits and paginate result sets. Gates: `go vet ./...`,
  `golangci-lint run`, `go test ./...`, `go build ./...`.
- **Data plane:** beads live in Dolt; bead create/update must go through the
  normal `bd` path. Dedup lookups must be cheap (indexed on `sentry_issue_id` or
  equivalent) and tolerant of the recurring-poll workload.
- **Secrets:** API token from config/env only, never committed, never logged.
- **Idempotency:** repeated polls of the same Sentry state must not create
  duplicate beads (this is the core dedup requirement).

## Open Questions

1. **Poll cadence & rate limits.** What interval balances freshness against Sentry
   rate limits? How do we handle pagination of large issue lists and 429s
   (backoff)? Should the interval be per-project or global?
2. **Dedup key choice & bead mapping.** Sentry *issue id* vs *fingerprint* — which
   is the stable key? (Issue id is stable per Sentry issue; fingerprint can group
   differently.) Exact mapping from Sentry fields → bead fields, and what an
   "update" mutates (event count? last-seen? reopen if closed?).
3. **Threshold policy.** What makes an issue worth a bead? New-issues-only?
   Events-above-N? Level-at-least-`error`? Some combination? Is the threshold per
   project? What happens to issues below threshold that later cross it?
4. **Landing target & ownership.** Which rig / which Dolt db do these beads land
   in? Who owns/triages them after filing? What assignee/owner do filed beads get
   by default?
5. **State & resumption.** Where does the patrol persist "last polled" / "already
   seen" state so a daemon restart doesn't re-file or miss issues? (Dolt? cursor
   in config? rely solely on dedup-on-create?)
6. **Issue lifecycle.** If a Sentry issue is resolved/ignored in Sentry, should
   the corresponding bead close or be annotated? (Likely v2, but worth flagging.)
7. **Failure handling.** If the Sentry API is down or the token is invalid, how
   does the patrol surface that (escalate? log-and-retry?) without spamming?

## Rough Approach

- Add a new gt daemon **patrol** (e.g. `sentry` / `sentry-ingest`) alongside the
  existing patrols, driven on the same interval mechanism.
- Each cycle: authenticate with the configured token, page through the configured
  org/projects' issues from Sentry's issues API, filtered by the configured
  threshold policy.
- For each qualifying issue, compute the stable dedup key (issue id or
  fingerprint). Look up an existing bead by `sentry_issue_id`:
  - **No match →** create a bead with the actionable signal fields + store
    `sentry_issue_id`.
  - **Match →** update the existing bead's volatile fields (event count, last-seen,
    frequency) rather than creating a new one.
- Persist poll state so restarts are clean and idempotent.
- All config (token, org, project list, interval, thresholds) loaded from the
  existing gt config mechanism; no literals in source.
- v1 stops at "filed/updated" — no auto-dispatch, no Sentry write-back.

*(Draft — breadth over depth. The review phase should pressure-test the dedup key,
the threshold policy, the landing target, and the persisted-state design, which
are the riskiest unknowns.)*

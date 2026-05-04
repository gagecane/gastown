# Plugin Dispatch Transport

**Status:** Accepted (foundation delivered; consumer migration deferred)
**Beads:** gu-zwui (this rig) / gt-to45a (HQ mirror) — follow-up to gt-swirk
**Date:** 2026-05-04

## Context

The gastown daemon dispatches plugins (dolt-backup, code-scout, task-discovery,
compactor-dog, stuck-agent-dog, etc.) to agent recipients by sending a
`mail.TypeTask` message with subject `"Plugin: <name>"`. Three call sites
produce these:

1. `internal/cmd/dog.go` — manual `gt dog dispatch` invocation.
2. `internal/daemon/handler.go` — cooldown-gated heartbeat dispatch.
3. `internal/daemon/auto_dispatch_watcher.go` — event-driven dispatch (reacts
   to `session_death` events).

The mail transport triggers the full mail machinery on delivery:

1. Envelope write to the mail store.
2. `"📬 You have new mail"` nudge to the recipient.
3. Reply-reminder scheduling.
4. Notification-message formatting.
5. Downstream delivery.

None of steps 2–4 are meaningful for plugin dispatches. Plugin recipients
(dogs) do not "read the mail" in any UX sense, cannot reply, and have no use
for a reply reminder. In **gt-swirk** (2026-04-28) this misfit manifested
as a **nudge storm**: phantom "reply via mail" reminders fired on plugin
dispatches were costing deacon cycles ~30% of their context budget.

gt-swirk's MVP fix introduced `isPluginDispatchSubject()` in
`internal/mail/router.go` to suppress reply reminders and swap the
notification phrasing for `"Plugin: "` subjects. That fix is **symptomatic**
— the mail transport still does all of the above work, the filter just hides
the UI fallout. The close reason on gt-swirk noted that the fuller
architectural fix would be to route plugin dispatches through the event feed
instead of mail. This ADR records that architectural direction and the
staging strategy for landing it safely.

## Decision

**Split the transports by intent:**

- **Mail** carries inter-agent *conversation* — messages a human or agent
  will read, reply to, and act on (escalations, questions, handoffs, etc.).
  Mail retains reply reminders, notification nudges, and mailbox semantics.
- **Events** carry *daemon notifications* — informational signals emitted by
  daemon/infrastructure that a consumer may or may not act on. Events are
  audit-first, lightweight, and have no reply semantics.

Plugin dispatches are daemon notifications, not conversation. They belong on
the event feed.

The new event type is:

```
events.TypeDaemonPluginDispatch = "daemon.plugin.dispatch"
```

With payload schema:

```go
DaemonPluginDispatchPayload(plugin, rig, target, trigger string) map[string]interface{}
```

| Field    | Type   | Required | Description                                                |
|----------|--------|----------|------------------------------------------------------------|
| plugin   | string | yes      | Plugin name being dispatched (e.g., "dolt-backup")         |
| target   | string | yes      | Agent recipient address (e.g., "deacon/dogs/alpha")        |
| rig      | string | no       | Rig the plugin is scoped to; omitted for town-level plugins|
| trigger  | string | no       | Dispatch origin: "cooldown" / "event-driven" / "manual"    |

Events are emitted at **audit** visibility (`events.LogAudit`) — they appear
in `~/gt/.events.jsonl` but not the curated user-facing feed. The daemon is
not trying to tell a human anything here; it is telling *future code* and
*operators running queries* that a dispatch happened.

## Staging Strategy

A big-bang transport swap would:

1. Require every consumer (dogs reading `"Plugin: "` mail) to migrate
   atomically, which cannot be coordinated across rigs.
2. Re-open the gt-swirk nudge storm if the mail path is removed before
   `isPluginDispatchSubject()` is no longer reachable.
3. Invent a new durability / replay model under time pressure (mail has
   inbox persistence; the event feed is append-only audit).

Instead we land this in **three phases**:

### Phase 1 — Foundation (this ADR / this PR)

- Introduce `TypeDaemonPluginDispatch` and `DaemonPluginDispatchPayload`.
- Emit the event **additively** at all three dispatch sites, *after* the
  existing mail has been successfully sent and *before* session start.
- Consumers still read mail. `isPluginDispatchSubject()` filter stays in
  place exactly as gt-swirk left it.

Observable contract after Phase 1:

- Every successful plugin dispatch produces one `daemon.plugin.dispatch`
  event in `~/gt/.events.jsonl`.
- No behavioral change for any existing consumer.
- Event emission failures do **not** fail the dispatch (best-effort audit).

### Phase 2 — Consumer migration (follow-up beads)

- Each consumer (dog plugin-mail reader, future witness subscribers, etc.)
  gets its own bead to migrate from mail-listener to event-subscriber.
- Consumers may be migrated independently. Until the last consumer
  migrates, the daemon continues to produce both mail and events.
- This is the phase where durability / replay semantics are designed per
  consumer (dogs currently rely on mail persistence to survive crash +
  restart; the event-feed replacement must address this).

### Phase 3 — Mail removal (follow-up bead)

Only once every known consumer of `"Plugin: "` mail has migrated:

- Drop the mail send from the three dispatch sites.
- Remove `isPluginDispatchSubject()` and its test coverage.
- Remove the `"🔌 %s dispatched from %s."` notification-message branch.
- Audit `grep -rn 'Plugin: ' --include='*.go'` confirms no producer remains.

Skipping either of these gates reopens gt-swirk.

## Non-goals

- **Inbox replay semantics on the event feed.** The event feed is
  append-only and not currently durable across consumer downtime in the same
  way mail inboxes are. Designing a replay model is Phase 2 per-consumer
  work, not this PR.
- **Agent-initiated plugin invocations.** Some subjects in the codebase
  (`"Plugin: %s"` in help text, dry-run output) are not daemon-originated
  dispatches. The event is only emitted from the three daemon/deacon
  dispatch call sites that actually produce dispatches to live agents.
- **Removing the gt-swirk MVP filter.** Listed under Phase 3; premature
  removal reopens the nudge storm.

## Consequences

**Pros:**

- Creates a concrete landing spot that follow-up consumer-migration beads
  can point at.
- Zero behavioral change in Phase 1 — reviewable as pure observability.
- Staged migration is incremental, reversible, and does not require
  cross-rig coordination.
- Future dashboards and operational queries get a stable event type to key
  off (`daemon.plugin.dispatch`) regardless of the underlying transport.

**Cons:**

- Double-writes during Phases 1–2: every dispatch produces both a mail and
  an event until consumers migrate. Cost is low (one JSON line appended to
  `~/gt/.events.jsonl` per dispatch) but non-zero.
- Two places to look when debugging dispatch (`gt mail inbox` *and* the
  event feed) until Phase 3 completes.
- The `trigger` field is advisory, not a formal enum; naming convention is
  enforced by code review, not types.

## References

- gt-swirk — MVP filter fix (closed, nudge storm root cause).
- gu-zwui (this bead) / gt-to45a (HQ mirror) — this architectural
  follow-up.
- `internal/events/events.go` — `TypeDaemonPluginDispatch` constant and
  `DaemonPluginDispatchPayload` helper.
- `internal/mail/router.go` — `isPluginDispatchSubject()` MVP filter to be
  removed in Phase 3.

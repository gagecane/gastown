# Agent reliability: crash-rate & recovery-success KPI (gu-dnkz4, KPI-2)

How reliable are agent sessions? Two derived indicators answer that:

1. **Crash rate** — how often sessions die unexpectedly (crash / kill) versus
   exit cleanly (`gt done`, `gt handoff`).
2. **Recovery success** — how often the `polecat-kiro-wrapper` supervisor brings
   a session to a clean `done` despite the kiro-cli clean-exit-mid-task bug
   (gu-ronb).

This spec lists the signals and the per-rig queries that compute both. It is
the KPI-2 deliverable from the telemetry audit (gu-nid89.14); it ships the
event-symmetry fix (D3) and the aggregation, not a new metric.

## The two planes

Reliability data lives in two separate planes, and the two KPIs read different
ones. Keep them straight:

- **Events plane** — `~/gt/.events.jsonl`, the local append-only audit log
  (`internal/events`). Carries the discrete session-lifecycle events
  (`session_start`, `session_end`, `session_death`). The **crash rate** is
  computed here.
- **OTel / VictoriaMetrics plane** — the exported metrics
  (`internal/telemetry`), including the well-instrumented
  `gastown.polecat.kiro_wrapper.*` family. **Recovery success** is computed
  here.

They are not joined; each KPI is self-contained within its plane.

## D3 fix: clean-exit symmetry

Before gu-dnkz4 the events plane was asymmetric: `session_start` was emitted on
`gt prime`, but there was no clean-close complement. `session_death` was the
only termination event, and it covered **both** crashes/kills (daemon reaper,
doctor, zombie patrol) **and** — by absence of an alternative — was the only
way to reason about a session ending. That left the crash-rate KPI with no
clean denominator: you cannot divide "deaths" by "clean exits" if clean exits
are never recorded.

`TypeSessionEnd` (`"session_end"`) was *defined* in `internal/events/events.go`
but never emitted (audit finding D3). gu-dnkz4 emits it on every clean,
intentional session termination:

| Emitter | Site | `reason` | `caller` |
|---|---|---|---|
| `gt done` (all exit types) | `teardownAfterDone`, `internal/cmd/done.go` | `gt done (exit=COMPLETED\|ESCALATED\|DEFERRED)` | `gt done` |
| `gt handoff` (non-polecat self-handoff) | `runHandoff`, `internal/cmd/handoff.go` | `gt handoff` | `gt handoff` |
| `gt handoff --cycle` | `runHandoffCycle`, `internal/cmd/handoff.go` | `gt handoff --cycle` | `gt handoff` |

Notes on coverage:

- **Polecats handing off** redirect to `gt done --status DEFERRED` (handoff.go),
  so they are counted once via the `gt done` path — no double-count.
- **`gt handoff --auto`** (PreCompact state-save) does *not* cycle the session,
  so it deliberately emits no `session_end` — the session lives on.
- `session_end` is **audit-visibility** (`LogAudit`), not feed-visible: a clean
  exit is mechanical and `done`/`handoff` already carry the feed line. It still
  lands in `.events.jsonl`, which is all the KPI needs.

### `session_end` payload

`events.SessionEndPayload(session, agent, rig, reason, caller)` — mirrors
`SessionDeathPayload` so the two can be queried side by side.

| Field | Description |
|---|---|
| `session` | tmux session name (`GT_SESSION`); empty when unknown |
| `agent` | Gas Town agent identity (e.g. `gastown_upstream/polecats/chrome`) |
| `rig` | rig name for per-rig aggregation; omitted for town-level roles |
| `reason` | how the session ended (e.g. `gt done (exit=COMPLETED)`) |
| `caller` | what initiated the clean exit (`gt done` / `gt handoff`) |

`session_death` (the crash/kill complement) carries `session`, `agent`,
`reason`, `caller` — see `SessionDeathPayload`. It is emitted by the daemon
reaper, doctor orphan/zombie checks, `gt log crash`, and `session.town`.

## Crash rate (events plane)

The witness/deacon control loop already reads `.events.jsonl`, so crash rate is
a lightweight rolling aggregate — no external query backend required. Both
events carry `rig` (death via the `rig/polecat` actor; end via the `rig`
field), so the ratio is computable per rig.

```
crash_rate(rig, window) =
    count(session_death where rig == R, ts in window)
  / count(session_end   where rig == R, ts in window)
```

A clean town trends toward 0. A rising ratio means sessions are dying faster
than they finish — a spawn-storm / instability signal (the gu-ronb class of
bug). Reading it from the file plane keeps it available to the patrol loop even
when the OTel pipeline is down.

For ad-hoc / forensic inspection without a backend:

```bash
# Clean exits vs deaths in the events log, last N lines, by rig.
# session_end is audit-only; both still land in .events.jsonl.
grep '"type":"session_end"'   ~/gt/.events.jsonl | wc -l   # denominator
grep '"type":"session_death"' ~/gt/.events.jsonl | wc -l   # numerator

# Deaths grouped by rig (actor is "<rig>/<polecat>")
grep '"type":"session_death"' ~/gt/.events.jsonl \
  | jq -r '.actor' | cut -d/ -f1 | sort | uniq -c
```

### If/when the events plane is mirrored to OTel

`session_start` / `session.stop` already have OTel counterparts
(`gastown.session.starts.total`, `gastown.session.stops.total` in
`internal/telemetry/recorder.go`). If `session_end` / `session_death` are later
exported as counters labeled by `rig`, the same ratio is a PromQL one-liner:

```promql
sum by (rig) (rate(gastown_session_death_total[1d]))
/
sum by (rig) (rate(gastown_session_end_total[1d]))
```

(Forward-looking — those counters are not emitted today; crash rate is computed
from the events plane above.)

## Recovery success (OTel plane)

Recovery success is fully instrumented already via the kiro-wrapper terminal
state. "Recovered to a clean done" = `state="done"` over all wrapper
invocations:

```promql
sum by (rig) (rate(gastown_polecat_kiro_wrapper_terminal_state_total{state="done"}[1d]))
/
sum by (rig) (rate(gastown_polecat_kiro_wrapper_terminal_state_total[1d]))
```

Should trend close to 1.0; below ~0.95 means the wrapper mitigation is starting
to fail — investigate kiro-cli upstream changes. This is the same signal
documented in [`kiro-wrapper-dashboard.md`](./kiro-wrapper-dashboard.md) (which
uses `invocations_total`; `terminal_state_total` has identical cardinality and
is preferred here because it slices cleanly by `state × rig`).

The inverse half — exhaustion (`state="max_iterations"`) and hard failures
(`state="non_zero_exit"`) — and the alerting thresholds for both live in
`kiro-wrapper-dashboard.md`; they are not duplicated here.

## Putting it together

| KPI | Plane | Numerator | Denominator | Healthy |
|---|---|---|---|---|
| Crash rate | events (`.events.jsonl`) | `session_death` | `session_end` | → 0 |
| Recovery success | OTel | `terminal_state{state=done}` | `terminal_state` (all) | → 1.0 |

Both are sliceable per rig. Crash rate is the leading indicator of session
instability; recovery success measures whether the gu-ronb mitigation is
holding. Read together they separate "the wrapper is catching the bug" (high
recovery success) from "sessions are dying for other reasons" (high crash rate
with healthy recovery success).

## Related documentation

- [`kiro-wrapper-dashboard.md`](./kiro-wrapper-dashboard.md) — recovery /
  exhaustion / duration panels + alert thresholds
- [`dolt-health-dashboard.md`](./dolt-health-dashboard.md) — sibling KPI spec
  (KPI-4 / KPI-7)
- [`otel-data-model.md`](./otel-data-model.md) — event / metric schemas
- [`otel-architecture.md`](./otel-architecture.md) — backend setup
- `reports/audit-2026-06/telemetry-signals.md` — KPI-2 origin (gu-nid89.14)
- gu-dnkz4 — this work (D3 fix + KPI-2 aggregation)

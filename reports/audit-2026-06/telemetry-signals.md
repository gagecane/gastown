# Telemetry Signals Audit — Town Performance Evaluation

**Bead:** gu-nid89.14 (parent epic: gu-nid89 — Whole-Repo Gastown Audit)
**Date:** 2026-06-11
**Scope:** `internal/telemetry/`, `internal/events/`, `internal/agentlog/`, `internal/townlog/`, `internal/feed/`, `internal/daemon/metrics.go`
**Goal:** Map the current telemetry surface, identify KPIs for town health, and propose new signals with implementation notes.

---

## 1. Executive Summary

Gas Town has **two parallel observability planes** that have grown independently:

1. **OTel plane** (`internal/telemetry` + `internal/daemon/metrics.go`) — structured metrics → VictoriaMetrics (`:8428`) and logs → VictoriaLogs (`:9428`). 27 `Record*` emitters + 11 daemon instruments. **Opt-in** (disabled unless `GT_OTEL_*_URL` is set). Rich, but **no in-repo consumer** — every dashboard/alert lives outside the repo, so signal quality is unverifiable from here and drifts silently.

2. **File-log plane** (`events`, `townlog`, `feed`, circuit-break log) — append-only JSONL/text under the town root, consumed in-repo by the feed TUI, witness patrols, the auto-dispatch watcher, and the circuit-break dog. This is where the **operational control loop** actually reads signal today.

**Headline findings:**

- **The KPI layer does not exist.** Raw counters and events are emitted abundantly, but **nothing in-repo computes a single derived performance indicator** — no throughput, time-to-close, crash rate, queue wait time, or recovery-success rate. The data to compute most of them is present (or one field away).
- **Three town-health signals are completely dark:** dispatch wait time / queue depth as a time series, polecat time-to-close, and agent crash-vs-clean-exit rate. These are the highest-value gaps.
- **Dead/asymmetric signals exist:** `RecordPaneOutput`, `events.TypeKill`, and `events.TypeSessionEnd` are defined but never emitted; `session.start`/`session.stop` are emitted but there is no session-duration signal.
- **Documentation has drifted:** `docs/design/otel/otel-architecture.md` still lists `agent.instantiate`, `mol.*`, and `bead.create` as "❌ Roadmap / no Record function exists" — but those `Record*` functions **do exist on this branch** (landed via PR #2199). The roadmap's P1/P2 proposals (refinery queue, witness patrol, scheduler dispatch, `done` enrichment) remain unimplemented and are the right backlog.

---

## 2. Current-State Map

### 2.1 OTel plane — metrics + structured logs

**Init:** `telemetry.Init` (`internal/telemetry/telemetry.go`) is wired in two entry points — `internal/cmd/root.go` (service `gastown`, every `gt` CLI invocation) and `internal/daemon/daemon.go` (service `gastown-daemon`). All daemon roles (mayor, witness, refinery, deacon) run inside the daemon process and are covered. Telemetry is best-effort and **returns a no-op provider when neither `GT_OTEL_METRICS_URL` nor `GT_OTEL_LOGS_URL` is set** (`IsActive()` gates side-effects).

**Emitters (`internal/telemetry/recorder.go`):** 27 `Record*` functions, each emitting a paired OTel log event + metric counter/histogram. Every event carries `run.id` (from ctx or `GT_RUN`) for waterfall correlation.

| Domain | Metric instruments | Notes |
|---|---|---|
| bd CLI | `gastown.bd.calls.total`, `gastown.bd.duration_ms` (hist) | labeled by `subcommand`, `status` |
| Sessions | `gastown.session.starts.total`, `gastown.session.stops.total` | **no duration histogram** |
| Agent lifecycle | `gastown.agent.instantiations.total`, `.events.total`, `.state_changes.total` | `agent.instantiate` is the waterfall root |
| Prompts / panes | `gastown.prompt.sends.total`, `gastown.pane.output.total` | pane emitter is **dead** (§3) |
| Polecat | `gastown.polecat.spawns.total`, `.removes.total` | spawns also emitted by daemon (dup name, §3) |
| Dispatch | `gastown.sling.dispatches.total`, `gastown.done.total`, `gastown.nudge.total` | counters only — no timing |
| Mail | `gastown.mail.operations.total` | labeled by `operation`, `status` |
| Daemon | `gastown.daemon.agent_restarts.total`, `.daemon.heartbeat.total`, `.daemon.restart.total` | two restart counters (recorder + daemon) |
| Molecule | `gastown.mol.cooks/wisps/squashes/burns.total`, `gastown.bead.creates.total` | full formula→wisp pipeline |
| Formula/convoy | `gastown.formula.instantiations.total`, `gastown.convoy.creates.total` | |
| Token usage | `agent.usage` log event (input/output/cache tokens) | **no cost metric** (roadmap P1) |
| kiro-wrapper | `.kiro_wrapper.invocations/iteration_events/terminal_state.total`, `.iterations` (hist), `.recovery_duration_seconds` (hist) | best-instrumented subsystem; labeled by terminal `state` |

**Daemon observable gauges (`internal/daemon/metrics.go`)** — already present and high-value:

- `gastown.dolt.connections`, `.max_connections`, `.query_latency_ms`, `.disk_usage_bytes`, `.healthy` — **Dolt health is fully instrumented.**
- `gastown.hooked_beads.total{db}`, `gastown.hooked_beads.dead_letter{db}` — **per-rig mail backlog / queue depth proxy already exists** (gu-hhqk).

### 2.2 File-log plane — the in-repo control loop

| Source | Producer | In-repo consumers |
|---|---|---|
| `~/gt/.events.jsonl` (audit, `internal/events`) | `events.Log/LogFeed/LogAudit` — 34 event types, ~56 call sites | feed curator, feed TUI (`cmd/feed.go`, `tui/feed`), `seance`, `audit`, auto-dispatch watcher, `witness/refinery_paused`, web fetcher, deacon feed-stranded, `trail`, dolt-snapshots plugin |
| `~/gt/.feed.jsonl` (curated) | `internal/feed/curator.go` (dedupe + aggregate) | **none in-repo** — consumed by external web UI |
| `~/gt/logs/town.log` (human text, `internal/townlog`) | `Logger.Log` — ~12 call sites (session, done, witness, kiro-wrapper) | `cmd/log.go`, `cmd/audit.go` |
| `.runtime/scheduler-circuit-breaks.jsonl` | `events.LogCircuitBreak` (2 sites) | `internal/daemon/circuit_break_dog.go` (escalation patrol, self-pruning) |
| `~/.gt/cmd-usage.jsonl` | `internal/cmd/telemetry.go` (per-command) | `gt metrics` (`cmd/metrics.go`) — frequency/dead-command analysis only |

**Observation:** the file-log plane is what feeds the witness/deacon control loop and the feed UI. The OTel plane is a separate, richer firehose with no in-repo reader. KPIs should be computed where they can act — either as derived OTel metrics (for dashboards/alerts) or as in-repo aggregations the witness can consume directly.

### 2.3 Documentation

- `docs/otel-data-model.md` — full log-record schema (events, attributes, identity hierarchy). Largely accurate.
- `docs/design/otel/otel-architecture.md` — backend-agnostic design, setup, **Implementation Status table (STALE — see §3) + Roadmap (P0 done, P1/P2 open — see §4)**.
- `docs/design/otel/kiro-wrapper-dashboard.md` — the **only** concrete dashboard/alert spec in-repo (PromQL + VictoriaLogs queries for the kiro-wrapper). A good template to replicate for other KPIs.
- No docker-compose service, setup script, Grafana JSON, or alert rule for the Victoria stack is checked in — operators stand it up manually (documented as raw `docker run`).

---

## 3. Defects & Dead Signals (verified)

| # | Finding | Evidence | Impact |
|---|---|---|---|
| D1 | `RecordPaneOutput` defined, **never called** | `grep -rl RecordPaneOutput` → only `recorder.go` | `GT_LOG_PANE_OUTPUT` env var is documented but inert; dead surface |
| D2 | `events.TypeKill` defined, **never emitted** | no `events.Log*` with `TypeKill` | kill actions logged only to townlog, invisible to feed/event consumers |
| D3 | `events.TypeSessionEnd` defined, **never emitted** | `TypeSessionDeath` used instead | asymmetry: `SessionStart` has no clean-close complement |
| D4 | Doc drift: architecture "Implementation Status" lists `agent.instantiate`, `mol.*`, `bead.create` as roadmap/missing | those `Record*` funcs exist in `recorder.go` on this branch (PR #2199 landed) | misleads future contributors; status table needs correction |
| D5 | Duplicate metric name `gastown.polecat.spawns.total` registered in both `recorder.go` and `daemon/metrics.go` | two `Int64Counter` with same name in different meters | double-count risk / ambiguous attribution on the spawn KPI |
| D6 | `session.start`/`session.stop` emitted but **no session-duration histogram** | recorder.go counters only | cannot measure session length distribution (uptime/churn) |

---

## 4. Proposed Signal Catalog — Town-Health KPIs

KPIs are ranked by value-to-effort. Each lists the signal, where to emit, format, the aggregation, and what existing data it builds on. **Most reuse data already produced** — the gap is the missing derived/timing fields and the absence of any consumer that computes them.

### Tier 1 — Highest value, data already mostly present

#### KPI-1: Polecat time-to-close (throughput latency)
- **What:** wall-clock from polecat session start (or first `gt prime`) to `gt done`.
- **Emit:** add `time_to_complete_ms`, `rig`, `bead_id` attributes to the existing `done` event + a new histogram `gastown.done.duration_ms`. (`done` currently carries only `exit_type`, `status` — `RecordDone` in `recorder.go:631`.)
- **Source data:** session-start timestamp is recoverable from `agent.instantiate`/`session.start` (same `run.id`) or from the heartbeat record the kiro-wrapper already reads. Simplest: stamp session start into the tmux env at spawn, read at `done`.
- **Aggregation:** `histogram_quantile(0.5|0.95, ...) by (rig)` — median/p95 time-to-close per rig; `count(done)/hour` = throughput.
- **Effort:** Low. One field on an existing event + a histogram.

#### KPI-2: Agent reliability — crash rate vs clean exit
- **What:** ratio of unexpected session deaths to clean completions. Recovery success = kiro-wrapper `state=done` / total invocations.
- **Emit:** the building blocks already exist — `events.TypeSessionDeath`, `townlog.EventCrash`, and the kiro-wrapper `terminal_state.total{state}` histogram (the single best-instrumented signal). The gap is **a derived crash-rate KPI metric**.
- **Fix D2/D3 first:** emit `TypeSessionEnd` on clean `gt done`/handoff so crash-rate has a clean denominator from the events plane (today `SessionDeath` covers both kills and crashes ambiguously).
- **Aggregation:** `sum(rate(kiro_wrapper_terminal_state_total{state="done"})) / sum(rate(kiro_wrapper_terminal_state_total))` = recovery success rate; `rate(session_death) / rate(done)` = crash rate. Both per-rig.
- **Effort:** Low-Medium. Mostly aggregation + one event symmetry fix.

#### KPI-3: Dispatch efficiency — queue depth & wait time
- **What:** pending-bead queue depth over time and enqueue→dispatch latency.
- **Emit:** roadmap P1/P2 already specifies `refinery.dispatch` (`queue_depth`, `wait_ms`) and `scheduler.dispatch_cycle` / `scheduler.queue_depth`. Scheduler already emits the discrete events (`TypeSchedulerEnqueue`, `TypeSchedulerDispatch` in `events.go`) — they carry no timing and no consumer aggregates them.
- **Reuse:** `gastown.hooked_beads.total{db}` daemon gauge is already a live queue-depth proxy per rig. Pair it with a `wait_ms` histogram computed as `dispatch_ts − enqueue_ts` (both timestamps already in the scheduler events).
- **Aggregation:** queue-depth time series per rig (gauge); `histogram_quantile(0.95, wait_ms)` per rig.
- **Effort:** Medium. Add timing to scheduler dispatch path; one histogram.

### Tier 2 — High value, modest effort

#### KPI-4: Dolt health KPI surface (data fully present, no consumer)
- `gastown.dolt.query_latency_ms`, `.healthy`, `.disk_usage_bytes`, `.connections/.max_connections` already emitted by `daemon/metrics.go`. **No alert rule or dashboard in-repo consumes them.**
- **Action:** ship a checked-in alert spec (mirroring `kiro-wrapper-dashboard.md`): latency p95 > 5s → page; `healthy==0` → critical; `connections/max_connections > 0.8` → warn; orphan-DB accumulation rate (needs new gauge, see KPI-7).
- **Effort:** Low (doc + queries; no new code for the core gauges).

#### KPI-5: Witness patrol health
- **What:** per-patrol duration, stale-sessions-detected, restarts-triggered.
- **Emit:** roadmap-specified `witness.patrol` event (`duration_ms`, `stale_count`, `restart_count`, `status`). Witness already emits discrete `patrol_started`/`patrol_complete` events (`events.go`) with no timing.
- **Aggregation:** patrol-cycle duration trend; restart-rate (a spike = instability or a spawn-storm like the gu-ronb class of bug).
- **Effort:** Medium.

#### KPI-6: Session duration / churn (fixes D6)
- Add `gastown.session.duration_ms` histogram recorded in `RecordSessionStop`, plus emit `TypeSessionEnd`. Distinguishes healthy long sessions from rapid churn (a leading indicator of spawn storms).
- **Effort:** Low.

### Tier 3 — Repurpose existing data / lower urgency

#### KPI-7: Dolt orphan-accumulation rate
- `gt dolt status` already counts orphan test DBs (testdb_*, beads_t*, …). Surface as a gauge `gastown.dolt.orphan_databases` updated on the daemon health tick (same callback that updates the other Dolt gauges). Alert on positive accumulation slope.
- **Effort:** Low-Medium.

#### KPI-8: Token cost per run/rig (roadmap P1)
- `agent.usage` already carries token counts. Add `gastown.token.cost_usd{rig, role, agent_type}` derived from model pricing. Enables cost dashboards.
- **Effort:** Medium (needs pricing table + model-id attribution).

#### KPI-9: Go runtime metrics (roadmap P1, ~5 LOC)
- Activate `go.opentelemetry.io/contrib/instrumentation/runtime` in `telemetry.Init` → goroutines, GC pause, heap. Directly answers the "resource utilization (goroutines, memory)" acceptance item.
- **Effort:** Trivial.

#### KPI-10: Circuit-break frequency
- `circuit_break_dog` already reads `.runtime/scheduler-circuit-breaks.jsonl`. Emit a counter `gastown.scheduler.circuit_breaks.total{rig}` when a break is logged so the repeated-failure signature is visible as a time series, not just a patrol-time scan.
- **Effort:** Low.

---

## 5. Aggregation & Consumption Strategy

The core structural recommendation: **close the consumption gap.** Two complementary paths:

1. **Dashboards/alerts (external Victoria stack):** check into the repo a `docs/design/otel/town-health-dashboard.md` (mirroring the existing kiro-wrapper spec) with PromQL/VictoriaLogs queries for KPI-1..KPI-10 and an alert-rule file. This makes the external dashboard reproducible and version-controlled, and stops silent doc drift (D4).
2. **In-loop KPIs (witness-consumable):** the witness/deacon control loop reads the file-log plane, not Victoria. For KPIs that should *drive automation* (crash-rate spike → throttle spawns; queue-depth high → add capacity), compute a lightweight rolling aggregate from `.events.jsonl` inside the witness patrol rather than depending on an external query.

Pick per-KPI: alert-only KPIs → OTel/Victoria; control-loop KPIs (crash rate, queue depth, circuit-breaks) → also surface in-repo.

---

## 6. Recommended Instrumentation Backlog (beads to file)

Ranked. Top items filed as beads under the epic (`discovered-from:gu-nid89.14`).

1. **KPI-1 polecat time-to-close** — enrich `done` event + `gastown.done.duration_ms` histogram. *(Tier 1, Low effort, highest value.)* → **gu-nniyx**
2. **KPI-3 dispatch wait-time + queue-depth time series** — add timing to scheduler dispatch; reuse `hooked_beads` gauge. *(Tier 1, Medium.)* → **gu-y7p6j**
3. **KPI-2 crash-rate / recovery-success KPI** + fix `TypeSessionEnd` emission (D3). *(Tier 1, Low-Medium.)* → **gu-dnkz4**
4. **Doc-drift + dead-signal fix (D1/D2/D4/D5)** — correct `otel-architecture.md` Implementation Status table; resolve dead `RecordPaneOutput`/`TypeKill` and the duplicate `polecat.spawns.total` metric. *(Low.)* → **gu-pkhxh**
5. **KPI-4 Dolt health dashboard + alert spec** (data already present) + KPI-7 orphan-rate gauge. *(Low.)* → **gu-nate5**
6. **KPI-9 Go runtime metrics** (~5 LOC). *(Trivial.)* → **gu-ojvc7**
7. **KPI-5 witness.patrol** + **KPI-6 session.duration** (fix D6) + **KPI-10 circuit-break counter** — *(Medium, batch; not yet filed — fold into a follow-up once Tier 1 lands.)*

---

## 7. Acceptance Checklist

- [x] Current state map of `telemetry`/`events`/`agentlog`/`townlog`/`feed` (+ `daemon/metrics.go`) — §2.
- [x] What's emitted / consumed / dead — §2, §3.
- [x] Format/schema and queryability — §2.1 (OTel/PromQL-queryable), §2.2 (JSONL/text).
- [x] KPI identification across throughput, reliability, Dolt, dispatch, resource utilization — §4.
- [x] Proposed signal catalog with emit location, format, aggregation, reuse notes — §4.
- [x] Dashboard/alert aggregation strategy — §5.
- [x] Beads filed for top instrumentation gaps — §6: gu-nniyx, gu-y7p6j, gu-dnkz4, gu-pkhxh, gu-nate5, gu-ojvc7.

---

## Sources

- `internal/telemetry/telemetry.go`, `recorder.go`, `subprocess.go` — accessed 2026-06-11
- `internal/events/events.go`, `circuit_break.go` — accessed 2026-06-11
- `internal/townlog/logger.go` — accessed 2026-06-11
- `internal/feed/curator.go` — accessed 2026-06-11
- `internal/agentlog/event.go` — accessed 2026-06-11
- `internal/daemon/metrics.go` — accessed 2026-06-11
- `internal/cmd/metrics.go`, `internal/cmd/telemetry.go` — accessed 2026-06-11
- `docs/otel-data-model.md`, `docs/design/otel/otel-architecture.md`, `docs/design/otel/kiro-wrapper-dashboard.md` — accessed 2026-06-11
- Codebase grep verification of dead emitters/event types (RecordPaneOutput, TypeKill, TypeSessionEnd) — accessed 2026-06-11

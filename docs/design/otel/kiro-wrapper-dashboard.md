# polecat-kiro-wrapper observability (gu-6jgi)

The `gt polecat-kiro-wrapper` supervisor is the permanent mitigation for the
kiro-cli clean-exit-mid-task bug (gu-ronb). Because it's a permanent
mitigation, its health needs to be graphable — not just inferred indirectly
from beads that fail to close.

This document lists the queries and panels needed to answer the operational
questions:

1. How often is the wrapper firing per polecat session?
2. What's the distribution of iteration counts (1 = no bug hit, 2–max-1 =
   recovery worked, max = gave up)?
3. How often do we hit max-iterations exhaustion?
4. Is wrapper effectiveness degrading over time (signal that kiro-cli
   behavior has shifted — new release, rate-limit quirk, prompt drift)?

## Metrics

All metrics have namespace `gastown.polecat.kiro_wrapper.*`. See
[`../otel-data-model.md`](../otel-data-model.md#polecatkiro_wrapperterminal)
for the canonical attribute/label list.

| Metric | Type | Labels | Purpose |
|---|---|---|---|
| `invocations.total` | counter | `state`, `rig`, `polecat` | One increment per wrapper run (total ≈ total polecat sessions with kiro-cli preset) |
| `terminal_state.total` | counter | `state`, `rig`, `polecat` | Same cardinality as invocations; kept separate so dashboards can slice by state × any label without cross-referencing |
| `iterations` | histogram | `state` | Distribution of iterations consumed. 1 = no bug hit, N > 1 = recovery worked on iter N, max = exhaustion |
| `recovery_duration_seconds` | histogram | `state` | Total wallclock from first spawn to terminal exit |
| `iteration_events.total` | counter | `event`, `rig`, `polecat` | One per iteration boundary (`resume_start`, `clean_exit_not_done`, `timeout_kill`) |

## VictoriaMetrics / PromQL queries

These assume the default VictoriaMetrics deployment configured in
[`otel-architecture.md`](./otel-architecture.md).

### Wrapper firing rate — invocations per hour by state

```promql
sum by (state) (
  rate(gastown_polecat_kiro_wrapper_invocations_total[1h])
) * 3600
```

Use for a stacked bar chart. Expected shape: most bars dominated by `done`,
a small slice of `max_iterations`. A growing slice of `max_iterations` or
`non_zero_exit` is the regression signal.

### Recovery rate — % of invocations that hit the bug and recovered

```promql
sum(rate(gastown_polecat_kiro_wrapper_invocations_total{state="done"}[1d]))
/
sum(rate(gastown_polecat_kiro_wrapper_invocations_total[1d]))
```

This is the "wrapper is holding" signal. Should trend close to 1.0;
anything below ~0.95 is worth investigating.

### Exhaustion rate — % of invocations that used up all iterations

```promql
sum(rate(gastown_polecat_kiro_wrapper_invocations_total{state="max_iterations"}[1d]))
/
sum(rate(gastown_polecat_kiro_wrapper_invocations_total[1d]))
```

Inverse of the recovery rate's useful half. Alerting threshold: > 5% over
24 hours means the mitigation is starting to fail, investigate kiro-cli
upstream changes.

### Iterations distribution

```promql
histogram_quantile(0.5, sum by (le) (rate(gastown_polecat_kiro_wrapper_iterations_bucket[1d])))
histogram_quantile(0.95, sum by (le) (rate(gastown_polecat_kiro_wrapper_iterations_bucket[1d])))
```

p50 should be 1 (fast path dominates). p95 rising above 1 means gu-ronb is
hitting more polecats. p95 approaching `max_iterations` means the recovery
loop itself isn't working.

### Recovery duration — how long deep recoveries take

```promql
histogram_quantile(0.5,
  sum by (le) (rate(gastown_polecat_kiro_wrapper_recovery_duration_seconds_bucket{state="done"}[1d]))
)
histogram_quantile(0.95,
  sum by (le) (rate(gastown_polecat_kiro_wrapper_recovery_duration_seconds_bucket{state="done"}[1d]))
)
```

Slice by `state="done"` to separate "recovered successfully" from "gave up"
distributions. The gap between these tells you whether retries are paying off
or just burning wallclock.

## VictoriaLogs queries

Structured log events carry the same fields. Useful for forensic drill-down.

### All wrapper exits in the last hour, grouped by state

```
_time:1h body:polecat.kiro_wrapper.terminal | stats count() by state
```

### Specific polecat's wrapper history

```
_time:1d body:polecat.kiro_wrapper.terminal polecat:"rust" rig:"gastown_upstream"
| fields _time, state, iterations_consumed, duration_seconds, bead_id, exit_code
```

### Iteration-boundary events leading up to a given failure

```
_time:1h body:polecat.kiro_wrapper.* session_name:"<session-name-from-mail>"
| sort by _time
```

## town.log grep

For operators without dashboard access, every terminal + iteration event is
also appended to `logs/town.log` in the town root:

```bash
# Terminal exits by state
grep 'kiro-wrapper terminal' ~/gt/logs/town.log | awk -F'state=' '{print $2}' | awk '{print $1}' | sort | uniq -c

# Wrapper activity for a specific polecat
grep 'kiro-wrapper' ~/gt/logs/town.log | grep 'testrig/polecats/testcat'

# Recent exhaustions
grep 'kiro-wrapper terminal.*state=max_iterations' ~/gt/logs/town.log | tail -20
```

## Alerting thresholds (starter set)

These are intentionally conservative defaults — tune them once you have
baseline data.

| Condition | Threshold | Action |
|---|---|---|
| `max_iterations` rate | > 5 % of invocations over 24h | Investigate kiro-cli upstream; ping mayor |
| `non_zero_exit` rate | > 1 % of invocations over 24h | Check for clap/wrapper regression (see gu-q319) |
| `recovery_duration_seconds` p95 | > 20 min and rising | Deep recoveries are taking too long; consider lowering `GT_KIRO_ITERATION_TIMEOUT` |
| No invocations for > 1h during active dispatch | 0 | Check telemetry pipeline — wrapper should fire continuously when polecats are spawning |

## Related documentation

- [`otel-data-model.md`](../otel-data-model.md) — event/metric schemas
- [`otel-architecture.md`](./otel-architecture.md) — backend setup
- gu-m3ne (CLOSED) — wrapper implementation
- gu-ronb (DEFERRED) — the bug this wrapper mitigates
- gu-q319 (CLOSED) — the argparse fix that these metrics would have caught faster

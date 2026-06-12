# Polecat time-to-close KPI (gu-nniyx)

Wall-clock latency from a polecat session starting to it running `gt done` —
the throughput-latency KPI from the telemetry audit (KPI-1,
`reports/audit-2026-06/telemetry-signals.md`). It answers:

1. How long does a polecat take to close a bead, per rig (median / p95)?
2. What's the per-rig throughput — completions per hour?
3. Is time-to-close drifting (a signal of harder work, slower agents, or
   churn from crash/retry loops)?

## Signal

`gt done` emits the `done` event and increments `gastown.done.total`. As of
gu-nniyx it also:

- adds `rig`, `bead_id`, and `time_to_complete_ms` attributes to the `done`
  log event,
- adds a `rig` label to `gastown.done.total`,
- records a new histogram `gastown.done.duration_ms{exit_type,rig}`.

The session-start timestamp is stamped into the `GT_SESSION_START` tmux env
(Unix seconds) by the polecat session manager at spawn
(`internal/polecat/session_manager.go`). `gt done` reads it via
`sessionDurationMs()` (`internal/cmd/done.go`). When `GT_SESSION_START` is
absent or unparseable (sessions predating this change, or manual invocations),
the duration is reported as 0 — the histogram is not recorded and
`time_to_complete_ms` is omitted from the log event, so those runs do not skew
the distribution.

| Metric | Type | Labels | Purpose |
|---|---|---|---|
| `gastown.done.total` | counter | `status`, `exit_type`, `rig` | Completion count → throughput |
| `gastown.done.duration_ms` | histogram | `exit_type`, `rig` | Time-to-close distribution |

`bead_id` is intentionally **not** a metric label (unbounded cardinality); it
lives only on the `done` log event for per-bead correlation in VictoriaLogs.

## VictoriaMetrics / PromQL queries

These assume the default VictoriaMetrics deployment configured in
[`otel-architecture.md`](./otel-architecture.md). Histogram buckets are
exported as `gastown_done_duration_ms_bucket`.

### Median (p50) time-to-close per rig

```promql
histogram_quantile(0.5,
  sum by (rig, le) (
    rate(gastown_done_duration_ms_bucket[1h])
  )
)
```

### p95 time-to-close per rig

```promql
histogram_quantile(0.95,
  sum by (rig, le) (
    rate(gastown_done_duration_ms_bucket[1h])
  )
)
```

### Throughput — completions per hour per rig

```promql
sum by (rig) (
  rate(gastown_done_total{exit_type="COMPLETED"}[1h])
) * 3600
```

### Clean-completion ratio per rig

```promql
sum by (rig) (rate(gastown_done_total{exit_type="COMPLETED"}[1d]))
/
sum by (rig) (rate(gastown_done_total[1d]))
```

A falling ratio means more `ESCALATED` / `DEFERRED` exits — work the polecats
could not finish.

## VictoriaLogs — per-bead drill-down

To inspect the slowest individual closes (e.g. the tail behind a bad p95):

```logsql
done AND time_to_complete_ms:>3600000
| sort by (time_to_complete_ms desc)
| fields _time, rig, bead_id, exit_type, time_to_complete_ms
```

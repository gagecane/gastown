# Dolt server health dashboard + alerts (gu-nate5, KPI-4 / KPI-7)

Dolt is the data plane for all of Gas Town — beads, mail, identity, work
history. It runs as a single shared `sql-server` on port 3307 serving every
database, and it is fragile (see `CLAUDE.md` → *Dolt Server — Operational
Awareness*). When Dolt degrades, every agent stalls.

The daemon already emits Dolt health gauges from `internal/daemon/metrics.go`
(updated on each `ensureDoltServerRunning` tick), but until now nothing in the
repo consumed them. This document is the checked-in dashboard + alert spec so
Dolt health is graphable and pageable rather than inferred after the fact.

It answers the operational questions:

1. Is the Dolt server up and healthy right now?
2. Are queries slow enough to be stalling agents?
3. Are we running out of connections?
4. Is the data directory growing unbounded?
5. Is test pollution (orphan databases) accumulating and degrading
   performance?

## Metrics

All metrics are emitted by the daemon health tick. See
[`otel-data-model.md`](./otel-data-model.md#4-metrics-reference) for the
canonical registry. VictoriaMetrics normalizes the OTel dotted names to
underscores (e.g. `gastown.dolt.healthy` → `gastown_dolt_healthy`).

| Metric | Type | Unit | Purpose |
|---|---|---|---|
| `gastown.dolt.healthy` | gauge | — | 1 = healthy, 0 = unhealthy (connections < 80% of max, not read-only) |
| `gastown.dolt.query_latency_ms` | gauge | ms | Round-trip latency of a `SELECT active_branch()` probe |
| `gastown.dolt.connections` | gauge | — | Active connections from `information_schema.PROCESSLIST` |
| `gastown.dolt.max_connections` | gauge | — | Configured connection ceiling (Dolt default 1000) |
| `gastown.dolt.disk_usage_bytes` | gauge | By | Total size of the `.dolt-data/` directory |
| `gastown.dolt.orphan_databases` | gauge | — | Count of unreferenced databases on the shared server (KPI-7) |

The health gauges are observable gauges collected on every export interval;
their values are refreshed by the daemon's `ensureDoltServerRunning` phase via
`updateDoltHealth`. The orphan count is computed each tick from
`doltserver.FindOrphanedDatabases`, the same scan that backs `gt dolt status`
and `gt dolt cleanup`.

## VictoriaMetrics / PromQL panels

These assume the default VictoriaMetrics deployment configured in
[`otel-architecture.md`](./otel-architecture.md).

### Health status — is Dolt up?

```promql
gastown_dolt_healthy
```

Single-stat / state-timeline panel. `1` = healthy, `0` = unhealthy. Any `0`
sample is the strongest signal that agents are about to stall.

### Query latency — p95

The daemon emits a single instantaneous latency sample per tick, so the gauge
is already the observed latency. For a smoothed p95 over a window use the
`_over_time` aggregation rather than `histogram_quantile` (this is a gauge, not
a histogram):

```promql
quantile_over_time(0.95, gastown_dolt_query_latency_ms[15m])
```

Expected shape: a few ms when idle. Sustained climb is the lead indicator of a
struggling server.

### Connection saturation — % of max in use

```promql
gastown_dolt_connections / gastown_dolt_max_connections
```

Render as a percentage (0–1). Warning band at `0.8`, matching the daemon's own
`Healthy=false` threshold.

### Disk usage growth

```promql
gastown_dolt_disk_usage_bytes
```

Plus the daily growth rate to spot unbounded accumulation:

```promql
delta(gastown_dolt_disk_usage_bytes[1d])
```

### Orphan database accumulation (KPI-7)

```promql
gastown_dolt_orphan_databases
```

The absolute count matters less than the *slope*: orphans (testdb_*, beads_t*,
doctest_*) leak from test runs and degrade performance until
`gt dolt cleanup` reaps them. A steady upward slope means cleanup isn't
keeping pace.

```promql
deriv(gastown_dolt_orphan_databases[6h])
```

## Alerts

PromQL alerting rules (Prometheus/VictoriaMetrics `vmalert` syntax). Thresholds
mirror the daemon's internal health logic so the dashboard and the in-process
`Healthy` flag agree.

```yaml
groups:
  - name: dolt-health
    rules:
      # CRITICAL: server is unhealthy or unreachable.
      - alert: DoltUnhealthy
        expr: gastown_dolt_healthy == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Dolt server unhealthy (read-only or connections saturated)"
          runbook: "docs/dolt-health-guide.md; gt dolt status / gt dolt dump"

      # PAGE: query latency p95 above the agent-stall threshold.
      - alert: DoltQueryLatencyHigh
        expr: quantile_over_time(0.95, gastown_dolt_query_latency_ms[15m]) > 5000
        for: 5m
        labels:
          severity: page
        annotations:
          summary: "Dolt p95 query latency > 5s — agents stalling"
          runbook: "Collect gt dolt dump + gt dolt status before restart; gt escalate -s HIGH"

      # WARN: connection pool approaching the ceiling.
      - alert: DoltConnectionsHigh
        expr: gastown_dolt_connections / gastown_dolt_max_connections > 0.8
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Dolt connections > 80% of max — approaching limit"

      # WARN: orphan databases accumulating faster than cleanup (KPI-7).
      # Slope-based: fires when the count rises by >5 over 6h without reaping.
      - alert: DoltOrphanDatabasesAccumulating
        expr: deriv(gastown_dolt_orphan_databases[6h]) > 0 and gastown_dolt_orphan_databases > 20
        for: 30m
        labels:
          severity: warning
        annotations:
          summary: "Dolt orphan databases accumulating — run gt dolt cleanup"
          runbook: "gt dolt status; gt dolt cleanup (safe — protects production DBs)"
```

### Alert threshold rationale

| Alert | Threshold | Why |
|---|---|---|
| `DoltUnhealthy` | `healthy == 0` for 2m | Direct read of the daemon's own health verdict; 2m debounces a single bad probe |
| `DoltQueryLatencyHigh` | p95 > 5s for 5m | 5s is the CLAUDE.md "Dolt trouble" symptom threshold; agents start timing out |
| `DoltConnectionsHigh` | > 80% for 5m | Matches `GetHealthMetrics` `ConnectionPct >= 80` → `Healthy=false` |
| `DoltOrphanDatabasesAccumulating` | positive 6h slope and count > 20 | Accumulation, not a one-time spike, is the regression; the `> 20` floor suppresses noise from normal transient test DBs |

## Operator fallback — no dashboard access

Every signal is also available from the CLI on the town host:

```bash
gt dolt status     # health, latency, connection count, orphan count
gt dolt dump       # non-fatal diagnostics snapshot (never signals the process)
gt dolt cleanup    # reap orphan databases (protects production DBs)
```

## Related documentation

- [`otel-data-model.md`](./otel-data-model.md) — metric registry
- [`otel-architecture.md`](./otel-architecture.md) — VictoriaMetrics backend
- [`kiro-wrapper-dashboard.md`](./kiro-wrapper-dashboard.md) — companion dashboard spec (gu-6jgi)
- `docs/dolt-health-guide.md` — Dolt RCA capture checklist
- `CLAUDE.md` → *Dolt Server — Operational Awareness* — operator protocol
- gu-nid89.14 — telemetry audit that surfaced these gaps (KPI-4)

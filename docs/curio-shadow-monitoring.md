# Curio Shadow-Mode Monitoring Runbook

This runbook tells a human how to monitor Curio while it runs in **shadow mode**:
candidates and would-page decisions are recorded, but **no Overseer pages are
sent and no beads are filed**. It provides copy-paste SQL against the live Dolt
server, a heartbeat-freshness check, a daemon-log audit grep, and a recurring
human review cadence.

Scope: this is the Phase 1 monitoring deliverable for epic `gu-fuycd`. It does
not cover flipping `page_for_real` (live paging) or the offline LLM lane — those
are gated on the review this runbook feeds.

## What Shadow Mode Means

Curio's enable knob lives in `<town>/mayor/daemon.json` under
`patrols.curio`:

```json
"curio": {
  "enabled": true,
  "page_for_real": false,
  "interval": "15m"
}
```

`enabled: true` + `page_for_real: false` = **shadow mode**. Curio runs its
detection cycle every 15m, writes structured findings and would-page decisions
to Dolt, logs an audit trail to the daemon log, and refreshes a heartbeat file —
but pages nobody.

## The Three Reporting Sinks

Everything Curio produces in shadow mode lands in one of three places. All SQL
sinks live in database **`hq`** on the Dolt server at `127.0.0.1:3307`.

| Sink | What it holds | Notes |
|------|---------------|-------|
| `hq.curio_candidate` | Every detected finding, one structured row | PK = `fingerprint`. Re-detections **overwrite** the row, so counts undercount recurrence. |
| `hq.curio_shadow_page` | Would-page **decisions** from the paging engine | `would_page` is the verdict; `proof` stores cluster hashes only. |
| `<town>/daemon/daemon.log` | Human audit trail | `curio: WOULD-PAGE [...]` and `curio: cycle complete ...` lines. |

`hq.curio_ledger` is the would-have-filed-bead ledger. It is **gated on
`page_for_real`** and stays **empty (0 rows)** in shadow mode — that emptiness is
itself a health signal (see Query 3).

> **Critical caveat — always filter `window_id LIKE 'live/%'`.** Test fixtures
> write rows with `window_id = 'win-1'`. Unfiltered counts mix test pollution
> into live data. Every query below filters to `live/%`.

## SQL Queries

Run these with the canonical wrapper (works headless, against the running
server):

```bash
gt dolt sql -q "<query>"
```

All queries below were verified to run clean against live `hq` on 2026-06-12.

### Query 1 — Candidate volume by rule / series / day

```sql
SELECT rule_id, series, DATE(created_at) AS day, COUNT(*) AS n
FROM hq.curio_candidate
WHERE window_id LIKE 'live/%'
GROUP BY 1, 2, 3
ORDER BY day DESC, n DESC;
```

Tracks how much each rule is firing over time. A sudden spike for one rule is the
first thing to investigate.

### Query 2 — Shadow-page cadence and would-page count

```sql
SELECT kind, lane, severity, would_page, COUNT(*) AS n
FROM hq.curio_shadow_page
WHERE window_id LIKE 'live/%'
GROUP BY 1, 2, 3, 4
ORDER BY n DESC;
```

Shows what the paging engine **would** have done. `kind` is one of
`verified_page` / `judgment_trip` / `judgment_bump`. In shadow mode `would_page`
should be `0` across the board — any `1` is a decision that would have paged the
Overseer for real once `page_for_real` flips.

### Query 3 — Confirm no beads filed (expect 0)

```sql
SELECT COUNT(*) AS ledger_rows FROM hq.curio_ledger;
```

**Must return 0 in shadow mode.** A non-zero result means Curio filed (or
would-file) beads — i.e. it is not actually in shadow mode. Investigate
`page_for_real` in `daemon.json` immediately.

### Query 4 — Rule distribution (which rules dominate)

```sql
SELECT rule_id, COUNT(*) AS n
FROM hq.curio_candidate
WHERE window_id LIKE 'live/%'
GROUP BY rule_id
ORDER BY n DESC;
```

A one-line read on which rules account for the bulk of findings.

### Query 5 — Live findings detail (signal review)

```sql
SELECT window_id, rule_id, target, rig, summary
FROM hq.curio_candidate
WHERE window_id LIKE 'live/%'
ORDER BY created_at;
```

The raw findings, for human signal-vs-noise judgment. Feed samples from this into
the review (`gu-fuycd.9`).

### Query 6 — Would-page log detail

```sql
SELECT window_id, kind, lane, severity, occurrences, clusters, would_page, summary
FROM hq.curio_shadow_page
WHERE window_id LIKE 'live/%'
ORDER BY created_at;
```

Each would-page decision with its occurrence/cluster counts. Note: `proof` stores
cluster **hashes only**, so the full membership of a cluster is not enumerable
from this table alone.

## Heartbeat Freshness Check

Curio refreshes a heartbeat file at the end of every cycle:

```bash
cat <town>/.runtime/curio-paging-heartbeat.json
```

Example (healthy):

```json
{"last_cycle_at":"2026-06-12T17:43:40Z","window_id":"live/2026-06-12T17:43:39Z","actions":1,"breaker_state":"closed","shadow_mode":true}
```

Check three things:

1. **`last_cycle_at` is fresh** — within ~2× the configured interval (15m), so
   under ~30m old. Staler than that means Curio has stopped cycling; check the
   daemon is alive.
2. **`shadow_mode` is `true`** — confirms it is still not paging for real.
3. **`breaker_state`** — `closed` is normal. `open` means a lane breaker tripped
   (the paging engine throttled a misfiring lane); cross-check the
   `curio_shadow_page` rows for `judgment_trip` / `judgment_bump`.

## Daemon-Log Audit Grep

The daemon log is the human-readable trail of each cycle:

```bash
grep 'curio:' <town>/daemon/daemon.log | tail -20
```

Useful lines to look for:

- `curio: WOULD-PAGE [kind] lane=... severity=... occurrences=N clusters=M: ...`
  — a would-page decision (mirrors a `curio_shadow_page` row).
- `curio: cycle complete — found=N new=M paged=K (candidates only, no beads filed)`
  — the per-cycle summary. `paged=K` here is the would-page count, **not** real
  pages.
- `curio: SHADOW MODE — N action(s) logged + ledgered, NO Overseer page sent`
  — confirms the shadow guarantee held for that cycle.

## Review Cadence

A recurring human rhythm keeps shadow data honest and builds toward the go/no-go
gate (`gu-fuycd.10`).

**Daily (≈2 min) — liveness check:**
- Read the heartbeat file: `last_cycle_at` fresh, `shadow_mode: true`,
  `breaker_state` sane.
- Run Query 3 — confirm `curio_ledger` is still 0.
- If either fails, escalate (`gt escalate -s HIGH "Curio: <symptom>"`).

**Twice weekly (≈15 min) — candidate review:**
- Run Queries 1, 2, and 4 — eyeball volume, would-page cadence, and rule mix for
  anomalies vs. the prior check.
- Spot-check Queries 5 and 6 — sample a handful of live findings and would-page
  decisions for obvious noise (duplicate targets, dead rigs, zero-deviation
  static-threshold trips).
- Note anything that looks like a false positive or a too-sensitive threshold.

**After ~1 week of accumulation — output-quality assessment (`gu-fuycd.9`):**
This runbook feeds that review. When judging signal vs. noise, point reviewers at
the open calibration questions already recorded in `gu-fuycd.9`'s notes:
1. Is the recurring multi-cluster judgment-lane trip a real standing problem or
   noise needing threshold tuning before `page_for_real` is justified?
2. `alarm_rate_spike` rows with `ewma`/`deviation` = 0 are **static-threshold**
   hits, not EWMA deviations (e.g. `done` rate 1270 > threshold 400 with no prior
   baseline). Is the threshold calibrated to real throughput?
3. `sched_fail` / `escalation` thresholds of 0 trip on **any** occurrence —
   likely too sensitive; candidates for tuning.

The per-rule false-positive impressions from that review become the evidence for
the `page_for_real` go/no-go gate (`gu-fuycd.10`).

## Sources

- Bead `gu-fuycd.8` — Curio monitoring: shadow-mode runbook (task + verified sink notes); read via `bd show gu-fuycd.8` — accessed 2026-06-12
- Bead `gu-fuycd.9` — Curio review: first output-quality assessment (calibration questions); read via `bd show gu-fuycd.9` — accessed 2026-06-12
- Bead `gu-fuycd` — EPIC: Enable Curio in shadow mode; read via `bd show gu-fuycd` — accessed 2026-06-12
- Live `hq` Dolt server (`127.0.0.1:3307`) — tables `curio_candidate`, `curio_shadow_page`, `curio_ledger`; queries verified via `gt dolt sql` — accessed 2026-06-12
- `/home/canewiw/gt/.runtime/curio-paging-heartbeat.json` and `/home/canewiw/gt/daemon/daemon.log` — live heartbeat and daemon audit trail — accessed 2026-06-12

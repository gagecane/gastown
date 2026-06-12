# Log Archaeology — Dolt Failure Taxonomy from `~/gt/.dolt-archive/jsonl/`

**Audit bead:** gu-nid89.16 (parent epic gu-nid89 — Whole-Repo Gastown Audit)
**Date:** 2026-06-11
**Author:** gastown_upstream/polecats/fury

---

## 1. What the archive actually is

`~/gt/.dolt-archive/jsonl/` is **not** a free-text log directory. It is a
time-series of **full Dolt-database snapshots** — one JSONL file per rig per
archive run, each a single JSON object with a `rows` array of bead records.

| Metric | Value |
|---|---|
| Files | 509 |
| Total size | 1.5 GB |
| Date range | 2026-04-27 → 2026-06-11 (bulk: 06-10 / 06-11) |
| Databases (rigs) | 23 (hq, gastown_upstream, 9× casc_*, talon*, ralph*, codegen_ws, agentforge, …) |
| Snapshot cadence | ~hourly per rig; `<rig>-latest.jsonl` = newest |

**Methodology.** Because each file is a snapshot, a bead can appear in dozens of
files. I unioned **all** snapshots keyed by `(rig, id)`, keeping the record with
the latest `updated_at`. This recovers beads that were later closed/deleted and
survive only in older snapshots.

- **Unique beads recovered:** 17,959 (hq 13,482; gastown_upstream 1,481; rest spread across 21 rigs)
- The bead record *is* the log line: `title`, `description`, `notes`,
  `close_reason`, `created_at`, `status`, `priority`, `issue_type`.
- "ESCALATION-*" files do **not** exist by filename. Escalations are **bead
  records** — `gc-wisp-*` / `gc-*` rows in the `hq` DB whose titles carry a
  `[CRITICAL]` / `[HIGH]` / `[MEDIUM]` severity tag.

Reproduce: `python3` union over `*.jsonl` → `/tmp/universe.json` (script logic in §8).

---

## 2. Headline numbers

| Signal | Count |
|---|---|
| Dolt-related **bug** beads (deduped) | **201** (open: 13) |
| Dolt-related **severity-tagged escalations** | **142** (CRITICAL 29 · HIGH 111 · MEDIUM 8) |
| Distinct CRITICAL Dolt outages | 29 escalation beads across **6 incident windows** |
| Peak escalation density | **18 escalations in one hour** (2026-06-07 02:00Z) |
| Escalations that are explicit follow-ups/recurrence of an earlier one | 26 / 134 (**19 %**) |

### Dolt-bug creation over time (the data plane got *worse*, not better)

```
2026-04-27 .. 05-31   ~1-8/day  (steady background)
2026-06-04   ###################  19
2026-06-06   ########################################  40   ← bulk triage/re-file day
2026-06-09   ###############  15
2026-06-10   ############  12
2026-06-11   ################  16
```

The 06-06 spike is partly a **bulk re-file event** (many bugs cross-filed
hq↔gastown_upstream the same day), but the sustained 06-09…06-11 elevation is
real: the imposter-server outage cluster (§4.1) is concentrated there.

---

## 3. Dolt failure taxonomy

Bugs classified by root failure mode (first-match wins; counts are deduped bug
beads). Ranked by severity-weighted impact, not raw count.

| # | Failure mode | Bugs | P0/P1 | Open | Representative beads |
|---|---|---|---|---|---|
| 1 | **Rogue / imposter sql-server on :3307 (wrong data-dir)** | 22 | 2 / 11 | 4 | gu-ohu3n (P0), gu-3phku (P0), gu-hvw2a, gu-msz5t, gc-o4yt68, gc-wakgb4 |
| 2 | **Connection storm / pool saturation** | 10+ | 0 / 5 | 0 | gu-hm0vw (196 conn/s), gu-nf6aj, gu-zo723, gc-lkmfn |
| 3 | **Server crash / outage / OOM** | 24 | 1 / 8 | 2 | gu-g7q6z (P0), gu-5j7p4 (load 166/64), casw-7ol |
| 4 | **Contention / flock / dispatch starvation** | 21 | 3 / 8 | 0 | gc-pai9b (P0), gc-wbk1b (P0), gu-06tji (P0), gu-el5bx |
| 5 | **Orphan / test-DB pollution of production server** | ~15 | 0 / 2 | 4 | gu-4str3 (19 orphans), gu-5ja0e, gc-trt9ml, gu-nx8s |
| 6 | **Schema drift / uncommitted-table migration** | 10 | 1 / 4 | 2 | gu-tqtwt (wisp_* never committed), gt-cljxg, gu-yb173, gu-iebpz |
| 7 | **Metadata / config misrouting** (`metadata.json` `dolt_database`) | 21 | 0 / 5 | 0 | gu-euef, cws-d02, cws-qhi, gu-pkamt |
| 8 | **Read/write consistency lag (stale-revert)** | several | 0 / 1 | 0 | gu-9qbg5 (~5min lag), gu-hdx7w |
| 9 | **Remote-sync push/pull (URL validation)** | 4 | 0 / 1 | 1 | gu-z8iqx / be-6ev / gc-ufe3ko (triple cross-file), gc-nqc1hb |
| 10 | **Goroutine / handle leak in dolt-go** | 2 | 0 / 1 | 0 | gu-hkxvr (sendingThread), gu-yyh5 (done-chan) |
| 11 | **Test infra (testcontainer / Dolt version)** | 6 | 0 / 2 | 1 | gu-f9tl (need ≥1.84.0), gu-4aurv, gu-w5mo |

> Note: categories 4 & 7 contain some bugs that are *triggered by* Dolt
> contention but live in dispatch/scheduler code. They are included because the
> archive shows the data plane is their failure surface.

---

## 4. The two dominant incident clusters

### 4.1 Imposter-server / wrong-data-dir cluster (HIGHEST severity, RECURRING, partly OPEN)

This is the single most damaging failure mode in the archive — it causes
**town-wide write outages** and is **not fully fixed**.

**Mechanism.** `bd`'s `dolt.auto-start` defaults **ON**. When the canonical
Dolt server (data-dir `~/gt/.dolt-data`, port 3307) dies (usually OOM), the
*next* `bd` call from any rig dir whose config lacks `dolt.auto-start: false`
spawns a **rig-local embedded** `dolt sql-server` with **no `--data-dir`**, from
its own cwd (e.g. `talon/.beads/dolt`). That imposter binds :3307 and serves
**empty / near-empty DBs** to the entire town. Every agent then reads an empty
`hq` (`issue_prefix config is missing`, `0 issues`), and escalation itself is
down because `gt escalate`/`gt mail` are bd writes.

**Timeline (chronological lineage):**

| Date | Bead | What |
|---|---|---|
| 05-21 | gu-orhn (P0, closed) | CLAUDE.md told agents `kill -QUIT` was a "safe" Dolt dump — it **terminates** the server. Documented footgun. |
| 06-10 | gc-o4yt68 (P1, **open**) | First botched restart: bd from `talon/.beads/dolt` auto-started empty server; canonical restart restored 10,657 beads. |
| 06-10 | gu-hvw2a (P1, closed, fix 1f891767) | Root-caused `dolt.auto-start` imposter spawn. |
| 06-11 05:21 | gu-msz5t (P1, open) / gc-96zaij ([CRITICAL]) / gc-wakgb4 (P1) | Post-OOM daemon respawned imposter on `.beads/dolt`; ~10 min town-wide write outage. |
| 06-11 15:58 | gu-ohu3n (**P0**, closed) | Server-start resolves rig-local data-dir, binds :3307 serving EMPTY DBs. |
| 06-11 18:54 | gu-3phku (**P0, in_progress**) | Daemon **still** respawns Dolt on wrong data-dir after OOM. Fix not landed. |
| 06-11 | gc-6lzjy2 (P1, **open**) | "Guard against rogue per-rig dolt sql-server hijacking town port 3307" — the durable prevention item. |

**Status: NOT RESOLVED.** gu-3phku (P0) is in_progress, gu-msz5t and gc-6lzjy2
are open. The fixes so far (gu-hvw2a/gu-ohu3n) addressed *specific* spawn paths;
the daemon respawn path (gu-3phku) re-opened the hole after OOM.

### 4.2 Convoy auto-close hot-loop → connection leak → saturation (RECURRING, regressed)

The connection-storm escalations (56 of the 142 Dolt escalations) trace to **one
root cause**: the daemon's hq convoy auto-close logic hot-loops and leaks Dolt
connections.

**Mechanism.** Daemon convoy-close path opens a Dolt connection per check and
fails to close it (FIN-WAIT-2 / CLOSE-WAIT pileup). Under ~16-rig concurrency
the pool climbs monotonically toward the cap; once saturated, all rigs see
`invalid connection` / `unexpected EOF` / `i/o timeout`, and `gt patrol report`
+ bd queries fail town-wide.

**Evidence — a single storm (2026-06-06 15:37→16:30Z), reconstructed from
escalation beads:**

```
15:37 gc-012kq  storm worsening
15:41 gc-e6oh6  leak trending to exhaustion
15:46 gc-r3del  climbing toward 1000 cap
16:17 gc-uh1dx  771/1000 (77%), exhaustion ~15min
16:18 gc-66avl  809/1000 (81%)
16:20 gc-buyfi  818→843 in 30s (~50/min)
16:25 gc-8te8s  911/1000 SATURATION IMMINENT
16:27 gc-9ixxp  935/1000 (94%)
```

This pattern **recurred at least 3 times** (06-06 16:00, 06-07 02:00 [18
escalations in that hour], 06-08 02:00). gu-g7q6z (P0) explicitly notes the
binary fix **"did not hold" — 2 near-outages in 10 hr**, then was re-fixed
(commit 9528dd78). The convoy-auto-close logic itself has been re-filed ~10×
since 05-29 (gu-kawd → gu-f0gq → gu-4cxuv → gu-urwg6 → gu-g7q6z → gc-m3ya3y →
gu-eafok), indicating an unstable subsystem.

**The max_connections flip-flop.** Operators oscillated the pool ceiling under
pressure: gc-1ld5e/gu-qlcr lowered 1000→100 (reduce OOM target), then under
saturation gu-zo723 reversed it 100→1000 (commit 0772a6a3). Neither addresses
the leak — they trade OOM risk against saturation risk. The leak fix (close the
connection) is the actual remedy.

---

## 5. Escalation trigger taxonomy

What fires a Dolt escalation, by frequency (severity-tagged escalation titles,
n=142):

| Trigger | Escalations | Notes |
|---|---|---|
| Connection leak / pool saturation | **56** | The dominant trigger. Monotonic climb toward 1000 cap. |
| i/o timeout / broken socket / EOF | 37 | Downstream symptom of saturation. |
| Server down / unreachable / "0 issues" | 31 | Includes imposter-server empty reads. |
| Stale / consistency / revert | 7 | DOLT_COMMIT -A stale-revert (gu-9qbg5/gu-yb173). |
| Imposter / wrong data-dir / empty DB | 6 | The §4.1 cluster (under-counted — many tagged "server down"). |
| OOM / host load | 2 | Usually the *cause*, escalated as the downstream Dolt symptom. |

**Escalation-storm anti-pattern (operational finding).** 19 % of escalations are
explicit follow-ups/recurrences of an earlier one, and peak density hit **18
escalations/hour**. During a saturation event, *each* agent independently
re-detects the climbing pool and fires its own `[CRITICAL]`, producing 10-18
near-duplicate escalations for one incident. This is partly tracked (gu-ah40
`gt escalate dedup`, gu-qh6fx convoy-storm dedup — both closed) but the
**Dolt-saturation escalation path specifically is not deduped**: the mayor still
gets a storm-of-escalations-about-the-storm. See filed bead §7.

---

## 6. Operational anti-patterns (agent behaviors that damage Dolt)

Mined from bug descriptions and the project CLAUDE.md history. Frequency = beads
referencing the pattern.

1. **`kill -QUIT` / SIGQUIT on the Dolt PID** (26 references). The documented
   "safe goroutine dump" recipe in CLAUDE.md actually **terminated** the server
   in Dolt 1.86.5. Root-caused in gu-orhn (P0). CLAUDE.md now warns against it
   and mandates `gt dolt dump` (non-signaling). *Verified fixed in the live
   CLAUDE.md.*

2. **`bd` auto-starting a rogue embedded server** (48 "imposter" references).
   The §4.1 root cause — agents (and the daemon) invoking `bd` while the
   canonical server is down silently spawn an empty imposter. *Partly fixed,
   daemon path still open (gu-3phku).*

3. **Unilateral `gt dolt restart` by refineries** (22 "unilateral" / 3
   "authority drift"). Refineries restarting Dolt without operator direction
   caused **botched restarts** (18 "botched restart" refs → the gc-o4yt68 empty
   data-dir incident). Tracked + fixed via authority-drift policy (gu-k00y0 /
   gc-vkwkfr): Dolt restart is operator-owned.

4. **Tests writing to the production server** (gu-4str3, gu-5ja0e, gc-trt9ml).
   Test/migration code creates DBs directly on :3307 with no cleanup → orphan
   accumulation → reaper noise + bd-init slowness. `gt dolt cleanup` mitigates;
   the leak source (migration tests) is **still open** (gu-5ja0e).

5. **Restart-without-diagnostics** (the reason the CLAUDE.md "collect diagnostics
   BEFORE restarting" protocol exists). Blind `gt dolt stop && start` during a
   hang destroys the evidence needed to root-cause; the imposter incidents are
   exactly what happens when a restart goes wrong under pressure.

---

## 7. Cross-reference with existing tracked beads + newly filed beads

**Cross-referenced (from acceptance criteria):**

- **gu-msz5t** (P1, open) — Rogue embedded sql-server hijacks :3307. Confirmed
  the live head of the §4.1 cluster. Already tracked.
- **gu-5ja0e** (P2, open) — Migration-test leaks DBs into `.dolt-data/`.
  Confirmed open; the §6.4 anti-pattern's untracked-source half.

**Systemic gaps already tracked (no new bead needed):**

- Imposter prevention: **gc-6lzjy2** (P1, open) + **gu-3phku** (P0, in_progress).
- bd schema drift town-wide: **gt-cljxg** (P1, open).
- Remote-URL validation: **gu-zl25s** (P2, open).
- Deacon heartbeat-interrupt parking: **gc-6o0rdv** (P3, open).

**Newly filed beads (systemic issues NOT already tracked):**

| Bead | P | Title |
|---|---|---|
| **gu-s2l9t** | P2 | Dolt-saturation escalation storm: N agents independently re-fire `[CRITICAL]` for one pool-saturation event (no incident-coalescing) — peaked 18 escalations/hr |
| **gu-d1r8g** | P2 | Daemon convoy auto-close connection-leak subsystem is regression-prone (re-filed ~10× since 05-29); needs a connection-lifecycle test guard + leak-rate alarm, not another point fix |

> The connection-leak *point* fix landed (gu-g7q6z/9528dd78) but the subsystem's
> regression history and the absence of a leak-rate regression test are the
> systemic gap. The escalation-storm-during-saturation is genuinely untracked
> (existing dedup beads gu-ah40/gu-qh6fx cover scheduler/convoy storms, not the
> Dolt-saturation observer storm).

---

## 8. Prevention strategies (ranked by leverage)

1. **Make the imposter impossible, not just unlikely** (addresses §4.1, the
   top outage source). The daemon and `bd` must **never** auto-start a server
   without an explicit canonical `--data-dir`. A `bd` call that finds no server
   on :3307 should *fail loudly* ("canonical Dolt down — run `gt dolt start`"),
   not silently spawn an empty store from cwd. This is gc-6lzjy2 / gu-3phku;
   it is the highest-leverage open item.

2. **Fix the connection leak at the source + add a regression guard** (§4.2).
   The leak is a not-closed connection in the convoy-close hot-loop. A
   `TestDaemonStoreIdleTimeout`-style guard exists; extend it to assert
   **connection count returns to baseline** after a convoy-close cycle, so the
   ~10× regression cycle stops. Add a daemon leak-rate alarm (conn climbing
   >X/min) so the *daemon* self-heals before the pool saturates — instead of 18
   agents escalating about it.

3. **Coalesce saturation escalations** (§5 anti-pattern). One incident → one
   escalation thread. When pool >threshold, the first observer opens an
   incident bead; subsequent observers *append* rather than file new
   `[CRITICAL]`s. Cuts mayor noise ~10-18×.

4. **Quarantine test/migration DB creation** (§6.4). Tests must use an
   isolated server/port or a teardown-guaranteed temp DB, never :3307. Close
   the open source (gu-5ja0e) and add a CI check that fails if a test leaves a
   DB on the shared server.

5. **Keep the diagnostics-before-restart discipline** (already in CLAUDE.md via
   gu-orhn). `gt dolt dump` over any signal; operator-owned restart authority
   (gu-k00y0). These are correct — the archive shows the cost of violating them
   (the botched-restart imposter incidents).

6. **Stop the max_connections flip-flop.** 100 (OOM-safe) vs 1000
   (saturation-safe) is a false choice while the leak exists. Fix the leak
   (#2), then size the pool to real concurrency (observed 1-19 normal, spikes
   only under leak) — likely well under 100.

---

## 9. Limitations

- The archive is **bead snapshots**, not raw server/stdout logs. Sub-incident
  timing (exact connection counts per second, goroutine stacks) is only what
  agents transcribed into escalation bead titles/descriptions — itself a
  reason connection-leak RCAs were hard (gu-g7q6z had to reason from
  FIN-WAIT-2 counts in escalation text).
- Earliest snapshot is 2026-04-27; anything before that is invisible. The
  single 04-28 file is a lone early snapshot, so April coverage is thin.
- The 06-06 bug spike conflates a genuine outage day with a bulk re-file/triage
  event; I separated cross-filed duplicates (hq↔gastown) but exact
  "new vs re-filed" attribution per bead is approximate.
- Classification into the §3 taxonomy is keyword-driven (first-match); a
  handful of dispatch/scheduler bugs that merely *surface* on the data plane
  are included where the archive evidence pointed at Dolt.

---

## Sources

All evidence is from the local archive and the live beads DB (no external
sources):

- `~/gt/.dolt-archive/jsonl/*.jsonl` — 509 Dolt-DB snapshot files — accessed 2026-06-11
- Live beads DB via `bd show <id>` (gu-msz5t, gu-5ja0e, gu-ah40, gu-qh6fx, etc.) — accessed 2026-06-11
- `~/gt/CLAUDE.md` — Dolt operational-awareness section (gu-orhn provenance) — accessed 2026-06-11

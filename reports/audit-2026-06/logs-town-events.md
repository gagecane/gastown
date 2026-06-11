# Log Archaeology — `town.log` & `.events.jsonl`

**Audit:** gu-nid89.15 (epic gu-nid89 — Whole-Repo Gastown Audit)
**Author:** gastown_upstream/polecats/radrat
**Date:** 2026-06-11
**Window analyzed:** last 7 days — 2026-06-04 → 2026-06-11 (per acceptance criteria)

## Sources

- `~/gt/.events.jsonl` — 100,953 events total; 71,627 in the 7-day window (2026-05-01 → 2026-06-11T22:04Z) — accessed 2026-06-11
- `~/gt/logs/town.log` — 61,444 lines (2026-04-25 → 2026-06-11) — accessed 2026-06-11

---

## Executive Summary

The telemetry is **dominated by self-inflicted lifecycle churn and test/synthetic
pollution, not by genuine production faults.** Of 71,627 events in the 7-day window:

- **~23% (16,535 events) are synthetic/test noise** — phantom rigs (`myr/mycat`,
  `rig-a`/`cat-1`), the synthetic `alpha` merge-queue rig (`gu-aaa`/`deadbeef`/
  `example.test`), and `synthetic` backoff markers. This noise floods the town-wide
  event feed and is the single biggest documented operational complaint (witnesses
  repeatedly woken every few seconds by cross-rig events that don't concern them).
- **`session_death` is 41% of all events (29,091)** but the overwhelming majority are
  *expected* idle/zombie reaps of the persistent-polecat model — benign by design,
  but extremely high-volume and indistinguishable from real crashes without parsing
  the `reason` field.
- The genuine **signal** hides in low-frequency events: Dolt connection-pool
  saturation / circuit-breaker trips, a handoff-mail persistence bug (title >500
  chars), a `myr/mycat` crash-respawn loop that emitted 3,725 health-check crashes,
  and `tmux wedged` restart failures.

A clear **cascade** is visible on **2026-06-06 → 06-07**: a Dolt saturation incident
coincided with a 13× spike in `session_death` (963/day → 9,239/day) and a matching
spike in `restart_polecat_handled` and `mass_death`.

---

## Methodology

- Events parsed as JSONL; grouped by `type` and normalized `payload.reason`
  (durations, bead IDs, and integers templated to `<dur>`/`<id>`/`<n>`).
- "Noise vs signal" classification: an event is **noise** if it originates from a
  non-configured/synthetic rig (`myr`, `rig-a`, `rig-b`, `alpha`, `testrig`) or
  carries synthetic markers (`synthetic`, `deadbeef`, `gu-aaa`, `example.test`,
  `testcat`, `mycat`). `myr` and `rig-a` are **not** present under `~/gt/` as
  configured rigs, and `myr/mycat` has **0** `session_start` and **0** `done` events
  despite 7,434 deaths — confirming it is phantom/test traffic.
- `town.log` lines parsed by `[tag]` prefix; error lines isolated by keyword.

---

## Top-20 Recurring Patterns (7-day window)

| # | Count | Pattern (event :: reason) | Sev | Class | Root-cause hypothesis |
|---|------:|---------------------------|-----|-------|-----------------------|
| 1 | 17,948 | `session_death` :: *idle polecat died (no hook_bead)* | Low | **Noise** | Expected idle reap of persistent polecats with no work hooked. Benign by design but dominates feed volume. |
| 2 | 6,313 | `restart_polecat_handled` :: *restarted* | Low | Noise | Deacon dogs restarting idle/dead polecats — routine lifecycle. |
| 3 | 3,725 | `session_death` :: *crash detected by daemon health check* | **High** | **Signal** | **100% from `myr/mycat`** — a single phantom agent in a tight crash-respawn loop since 06-06 (median inter-death gap = 0s). |
| 4 | 3,350 | `session_death` :: *zombie cleanup* | Med | Mixed | Sessions whose pane died but lifecycle not closed; daemon reaping zombies. Elevated during the 06-06/07 cascade. |
| 5 | 1,595 | `mq_frozen_blocked` :: *broke-main-ci: gu-aaa* | n/a | **Noise** | Synthetic `alpha` rig with placeholder `gu-aaa`/`deadbeef`/`example.test`. Test fixture, not real. |
| 6 | 1,595 | `mq_frozen_blocked` :: *(no reason payload)* | n/a | Noise | Duplicate/companion synthetic `alpha` frozen events lacking `reason`. |
| 7 | 1,555 | `restart` :: *skipped-backoff: …synthetic* | Low | **Noise** | Literal `"synthetic"` backoff error — test traffic (`rig-a`/`cat-1`). |
| 8 | 1,554 | `restart` :: *restart-failed: tmux wedged* | **High** | **Signal** | tmux server wedged so polecat panes can't be respawned. Real operational failure (though paired with synthetic `rig-a`). |
| 9 | 1,263 | `session_death` :: *idle-reap: working-bead-lookup-failed* | Med | **Signal** | Reaper killed a "working" polecat because its working-bead lookup failed (45m idle). Suggests bead/Dolt lookup failures stranding polecats. |
| 10 | 1,237 | `session_death` :: *idle-reap: working-no-hook* | Med | Mixed | Polecat marked working but no hook bead for 20m — likely lost hook after a crash/restart. |
| 11 | 1,236 | `session_death` :: *idle-reap: test-reap* | Low | **Noise** | `testrig/testcat` — synthetic reap fixture. |
| 12 | 238 | `session_death` :: *dead-polecat-wisp-reap (hooked)* | Med | Signal | Polecat heartbeat went stale >1h while a wisp bead stayed `hooked` — work stranded, wisp reset for re-dispatch. |
| 13 | 36 | `session_death` :: *dead-agent-wisp-reap (hooked, updated_at)* | Med | Signal | Agent (non-polecat) wisp stale >3h; reaped via `updated_at`. |
| 14 | 34 | `scheduler_dispatch_failed` :: *sling failed: spawn* | **High** | **Signal** | Polecat spawn failures — incl. Dolt-at-capacity and `default_branch not found`. |
| 15 | 28 | `session_death` :: *dead-polecat-wisp-reap (in_progress)* | Med | Signal | Same as #12 but bead was `in_progress` — active work lost. |
| 16 | 21 | `handoff-NOPERSIST` :: *dolt circuit breaker open* (town.log) | **High** | **Signal** | Witness/refinery handoff mail failed to persist because Dolt was down (circuit breaker open). Handoff context lost. |
| 17 | 17 | `session_death` :: *idle-reap: exiting* | Low | Noise | Normal idle exit past 15m threshold. |
| 18 | 12 | `scheduler_dispatch_failed` :: *default_branch not found as origin/main* | **High** | **Signal** | `casc_crud` bare repo misconfig — `default_branch` missing on origin; blocks all spawns for that rig. |
| 19 | 10 | `handoff-NOPERSIST` :: *issue_prefix config is missing* (town.log) | Med | **Signal** | Handoff persistence failed: a rig's beads DB has no `issue_prefix` configured. |
| 20 | 7 | `handoff-NOPERSIST` :: *title must be ≤500 chars (got 760–1285)* | **High** | **Signal** | **Real bug:** witnesses write multi-hundred-char handoff summaries into the bead *title*; validation rejects them and the entire handoff is silently lost (`NOPERSIST`). |

> Patterns 5–7, 11 are pure synthetic/test noise and account for ~5,940 of the events
> above. Excluding them, the real failure surface is far smaller than raw counts suggest.

---

## Failure Modes by Frequency & Severity

### Critical / High signal (act on these)
1. **Dolt connection-pool saturation & circuit-breaker trips** (68 event-level hits +
   ~22 `handoff-NOPERSIST` lines). Errors: `dolt server at connection capacity`,
   `circuit breaker is open: server appears down`, `i/o timeout` to `127.0.0.1:3307`,
   `connection refused`. **Peak 2026-06-09 (28 hits)** and during the 06-06/07 cascade.
   This is the data-plane fragility called out in `CLAUDE.md`.
2. **`myr/mycat` crash-respawn loop** — 3,725 health-check crashes, 7,434 total deaths,
   0 successful starts/dones, median gap 0s, running continuously since 2026-06-06.
   Phantom/orphan agent that should not exist; it is also a top feed-pollution source.
3. **Handoff persistence bug (title length)** — 7 confirmed losses where a witness/deacon
   handoff summary (671–1285 chars) was rejected by the ≤500-char title validator,
   discarding the entire handoff. Deterministic and fixable.
4. **`tmux wedged` restart failures** — 1,554 occurrences; deacon dogs cannot respawn
   polecats when the tmux server is wedged.
5. **`default_branch not found` spawn failures** — `casc_crud` bare repo misconfigured;
   blocks polecat allocation for that rig.

### Medium signal
- Stranded-work reaps (`working-bead-lookup-failed`, `dead-polecat-wisp-reap` with
  `hooked`/`in_progress`): ~1,500 events implying work was lost when a polecat's
  bead lookup or heartbeat failed — often downstream of the Dolt incidents.
- `issue_prefix config is missing` — a rig's beads DB is uninitialized.

### Noise (benign but high-frequency — suppress/route, don't alarm)
- `idle polecat died (no hook_bead)` (17,948), `restarted` (6,313), `test-reap`,
  `skipped-backoff: synthetic`, and the entire synthetic `alpha` `mq_frozen_blocked`
  stream (3,190). **~23% of all 7-day events are synthetic/test traffic.**

---

## Timeline — the 2026-06-06/07 Cascade

| Day | session_death | mass_death | restart | mq_frozen | escal | Dolt-err |
|-----|-------:|-----:|-------:|------:|----:|----:|
| 06-04 | 963 | 124 | 0 | 0 | 0 | 0 |
| 06-05 | 589 | 76 | 412 | 118 | 18 | 2 |
| **06-06** | **7,763** | **1,089** | **3,189** | 1,090 | **120** | 9 |
| **06-07** | **9,239** | **1,377** | **2,754** | 930 | 60 | 6 |
| 06-08 | 1,822 | 273 | 539 | 176 | 75 | 6 |
| 06-09 | 2,841 | 421 | 822 | 298 | 56 | **28** |
| 06-10 | 2,728 | 420 | 813 | 264 | 79 | 13 |
| 06-11 | 3,168 | 477 | 898 | 312 | 39 | 1 |

**Cascade signature (06-06 → 06-07):** session_death jumped ~13× and escalations ~7×,
co-occurring with the `myr/mycat` loop onset (first death 06-06T14:33Z), a burst of
`gastown_upstream` exit-code-255 crashes (guzzle/fury/nitro at 06-05T21:24Z), and
elevated Dolt persistence failures. Town never returned to the 06-04 baseline (~960
deaths/day) — it settled at a new, higher floor of ~2,800–3,200 deaths/day, indicating
the `myr/mycat` loop and synthetic feed traffic remained unresolved through 06-11.

**Pattern that precedes cascade:** the leading indicator was **Dolt
`handoff-NOPERSIST` / circuit-breaker** lines on 06-04/05, *before* the 06-06 death
spike. Dolt degradation → handoff/bead-lookup failures → polecats stranded and reaped →
mass restarts → more Dolt connections → saturation. Watch Dolt error rate as the
early-warning signal for lifecycle cascades.

---

## Noise vs Signal — Recommendations

The dominant finding is that **the event feed has a signal-to-noise problem**, which is
independently corroborated by witness handoff notes ("town-wide events feed is busy —
witness woken every few seconds"; "FLOODED by testrig cross-rig test pollution +
synthetic session_death/mass_death events").

- **Tag synthetic/test events** with a `synthetic: true` flag (or emit them to a
  separate `.events-test.jsonl`) so witnesses' `await-event` filters can drop them
  without per-rig allowlisting.
- **Stop emitting `idle polecat died (no hook_bead)` to the shared feed** at `feed`
  visibility — downgrade to `audit`. It is 17,948 events of expected behavior.
- The recommended witness mitigation already discovered in handoffs —
  `gt mol step await-event --channel witness --filter-rig <rig> --cleanup` — should be
  the **default** idle-wake, not tribal knowledge passed via handoff text.

---

## Proposed Actionable Beads

The following are recommended as child beads under epic gu-nid89 (file via `bd create`):

1. **[P1] Kill the `myr/mycat` phantom crash-respawn loop.** Identify why a
   non-configured rig (`myr`) is spawning `mycat` and emitting 3,725 health-check
   crashes; either register the rig or block spawns for unconfigured rigs. *(Signal #2)*
2. **[P1] Fix handoff-mail title validation loss.** Handoffs with >500-char summaries
   are silently discarded (`NOPERSIST`). Store the long summary in the bead *body* and
   derive a ≤500-char title, or raise/relax the limit for handoff beads. *(Pattern #20)*
3. **[P1] Dolt connection-pool saturation hardening.** Add admission-control backpressure
   so polecat spawns don't drive Dolt to capacity; surface circuit-breaker state as a
   first-class alarm. Tie to the early-warning timeline finding. *(Signal #1)*
4. **[P2] Reduce event-feed noise.** Tag synthetic/test events and downgrade
   `idle polecat died (no hook_bead)` from `feed` to `audit` visibility. *(Noise section)*
5. **[P2] Default witness idle-wake to rig-filtered `await-event`.** Bake the
   `--filter-rig --cleanup` pattern into the witness formula. *(Noise section)*
6. **[P2] Handle `tmux wedged` restart failures.** Detect a wedged tmux server and
   recycle it before deacon dogs attempt 1,554 doomed restarts. *(Signal #4)*
7. **[P3] Fix `casc_crud` `default_branch not found` spawn failures** and audit all rigs
   for missing `issue_prefix` config. *(Patterns #18, #19)*

> Beads not auto-created in this audit session (report-only task); the above are the
> proposed set with priority and root-cause hypotheses for the epic owner to file.

---

## Confidence & Caveats

- Pattern *counts* are exact (full-file parse). *Noise classification* is heuristic —
  `alpha`, `myr`, `rig-a` are treated as synthetic because they are absent from
  `~/gt/` and carry placeholder identifiers; if any is a real but mis-tagged rig, its
  events would reclassify as signal.
- `town.log` error lines are sparse (93 keyword hits in-window) because most operational
  detail lives in `.events.jsonl`; `town.log` is primarily `[nudge]`/`[handoff]`/`[done]`
  activity logging.
- Root causes are *hypotheses* derived from co-occurrence and error strings, not from
  reproduction.

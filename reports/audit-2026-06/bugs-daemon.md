# Bug Audit — `internal/daemon/`

**Bead:** gu-nid89.2 (epic gu-nid89 — Whole-Repo Gastown Audit)
**Scope:** `internal/daemon/` non-test source (~52K LOC, ~120 files)
**Date:** 2026-06-11
**Method:** Parallel adversarial review across 6 file groups (lifecycle/core, dog
groups A/B, dolt subsystem, restart/reap/convoy, gc/metrics/test-runner), followed
by direct code verification of every HIGH finding.

Focus areas per the bead: goroutine leaks, signal handling, lifecycle race
conditions, restart loops, resource exhaustion.

## Confidence / severity legend

- **Confidence** — how sure we are it is a real bug: **HIGH** = traced in code and
  verified; **MED** = likely but runtime-dependent; **LOW** = smell worth noting.
- **Severity** — blast radius if it fires.

---

## HIGH-confidence findings (beads filed)

| ID | Finding | Bead |
|----|---------|------|
| D1 | Concurrent map writes in `isRigOperational` | gu-nid89.39 |
| D2 | Concurrent map writes in `operatorStoppedRefineryLogged` | gu-nid89.40 |
| D3 | Dolt circuit breaker stuck in HalfOpen | gu-nid89.41 |
| D4 | `reapLeakedActContainers` docker calls no timeout | gu-nid89.42 |
| D5 | restart-pending bead marked handled on failed escalation | gu-nid89.43 |

### D1. Concurrent map writes in `isRigOperational` crash the whole daemon
- **File:** `daemon.go:2856-2861`, `2882-2887` (writes); reached concurrently from
  `ensureWitnessesRunning` (`daemon.go:2232,2259`), `ensureRefineriesRunning`
  (`daemon.go:2309,2368`), and the convoy `isRigParked` closure (`daemon.go:719`).
- **Confidence:** HIGH · **Severity:** HIGH
- The heartbeat fans per-rig work out across up to 10 concurrent goroutines via
  `d.rigPool.runPerRig` (verified `runPerRig` spawns one goroutine per rig bounded
  by a semaphore, `worker.go`). Each goroutine calls `isRigOperational`, which on
  the rig-bead-missing / collision error paths does an unsynchronized lazy
  `make` + write into `d.missingRigBeadLogged` / `d.collisionRigBeadLogged`. Those
  maps are explicitly documented "Only accessed from heartbeat loop goroutine — no
  sync needed" (`daemon.go:160-161,171-172`) — an invariant that is false because
  the heartbeat dispatches the checks concurrently. Two goroutines writing the map
  at once produce Go's unrecoverable `fatal error: concurrent map writes`, killing
  the daemon. Trigger state (multiple rigs with no/colliding identity beads) is a
  common bootstrap state and is *persistent* — these maps exist precisely because
  the condition recurs every heartbeat.
- **Fix:** Guard the dedup maps with a mutex (one `dedupMu` covering all three), or
  pre-init in `New()` and lock the check-write, or use `sync.Map`/`sync.Once`-per-rig.

### D2. Concurrent map writes in `logOperatorStoppedSkip` / `clearOperatorStoppedLog`
- **File:** `daemon.go:2322-2344`; reached via `ensureRefineryRunning`
  (`daemon.go:2348`) inside the `ensureRefineriesRunning` per-rig fan-out.
- **Confidence:** HIGH · **Severity:** HIGH
- Same root cause as D1, distinct map (`d.operatorStoppedRefineryLogged`, also
  documented "no sync needed"). When two+ rigs are operator-stopped in the same
  heartbeat (the SSH-cert-recovery scenario the dedup was built for), concurrent
  `make`/write/`delete` on this map triggers `fatal error: concurrent map writes`.
- **Fix:** Protect with the same mutex as D1.

### D3. Dolt circuit breaker gets stuck in HalfOpen, disabling all protection
- **File:** `dolt_circuit_breaker.go:135-156` (`Allow()` HalfOpen path); early-return
  callers `curio_dog.go:104-107,147-150` and `failure_classifier_dog.go:340-347`.
- **Confidence:** HIGH · **Severity:** HIGH
- In HalfOpen, `Allow()` returns `true` unconditionally and leaves the state at
  HalfOpen — the breaker only leaves HalfOpen when a caller invokes `Record()`.
  Several dogs call `Allow()` then hit an early `return` *before* reaching any
  `Record()`: curio on the collect-error and no-candidates paths;
  failure_classifier on the compile-error / empty-signatures paths (verified).
  When cooldown elapses and the admitted probe lands on one of those paths, the
  breaker stays HalfOpen forever. Because HalfOpen admits *every* caller with no
  gating, the breaker silently stops short-circuiting — re-amplifying bd-subprocess
  load on a recovering Dolt server, the exact failure it exists to prevent. It
  self-heals only by accident if a later dog happens to reach a `Record()`.
- **Fix:** Gate HalfOpen to a single in-flight probe in `Allow()` (admit once, then
  short-circuit until `Record` lands), or make every `Allow()`-gated dog `Record()`
  on all exit paths (e.g. `defer` a success record on clean early returns).

### D4. `reapLeakedActContainers` shells out to `docker` with no timeout — hangs the daemon main loop
- **File:** `main_branch_test_runner.go:1141-1175`; called synchronously from
  `runMainBranchTests` (`:333`), which runs inline in the daemon main `select`
  (`daemon.go:911-916`).
- **Confidence:** HIGH · **Severity:** HIGH
- `docker ps` / `docker inspect` / `docker rm -f` all use plain
  `exec.Command(...).Output()/.Run()` with no context or timeout. The patrol runs
  inline on the main loop (not a goroutine). If the Docker daemon is unresponsive —
  exactly the host-overload condition this reaper exists to mitigate (gs-rd8) — the
  `docker` call hangs indefinitely, stalling the entire daemon: no heartbeat, no
  Dolt health probe, no other patrol, until Docker recovers. Every other subprocess
  in this file uses `CommandContext` with a timeout; this path is the outlier.
- **Fix:** Wrap each docker call in `exec.CommandContext` with a short timeout
  (e.g. `context.WithTimeout(d.ctx, 30*time.Second)`); bail on first timeout.

### D5. Restart-pending bead marked handled even when escalation fails — silently buried
- **File:** `restart_pending_dog.go:125-135`; `escalate` impl
  `jsonl_git_backup.go:521-558`.
- **Confidence:** HIGH · **Severity:** HIGH
- `d.escalate(...)` returns nothing and swallows all subprocess errors (verified:
  it only logs on `cmd.CombinedOutput()` failure). `runRestartPendingDog`
  unconditionally calls `markRestartPendingEscalated(b.ID)` right after, adding the
  `restart-escalated` label; `listUnescalatedRestartPending` filters that label out
  forever. If `gt escalate` fails (gt missing, Dolt degraded-but-breaker-not-tripped,
  timeout), the escalation never lands yet the bead is marked handled permanently.
  This silently reintroduces the exact gu-muj66 failure the dog was built to
  prevent (pending daemon-restart beads dropped → daemon runs stale code). The bead
  stays `open` so a human *could* notice, but the loud signal is lost.
- **Fix:** Make `escalate` return an error; only call `markRestartPendingEscalated`
  on success; on failure log and leave the bead unlabeled so the next tick retries.

---

## MED-confidence findings (not filed — recommend triage)

### D6. Compactor runs session-scoped Dolt ops over a connection POOL, not a pinned conn
- **File:** `compactor_dog.go:253-345` (`compactDatabase`), `398-543`
  (`surgicalRebaseOnce`), `589-592` (`compactorOpenDB`).
- **Confidence:** HIGH (logic) / MED (live-impact frequency) · **Severity:** HIGH
- `compactorOpenDB` returns a `*sql.DB` pool with no `SetMaxOpenConns(1)` and no
  pinned `*sql.Conn`. The compaction sequence relies on per-connection Dolt session
  state that does not survive across pooled connections: `USE \`db\`` then
  `DOLT_RESET`/`DOLT_COMMIT`; `DOLT_CHECKOUT(workBranch)` then `DOLT_REBASE`, the
  `dolt_rebase` SELECT/UPDATE, and the branch swap can each land on a *different*
  pooled connection. The current database, current branch, and in-progress
  interactive rebase are all per-session in Dolt. So `USE` can run on conn A while
  `DOLT_RESET` runs on conn B (wrong/default DB); the rebase started on conn A is
  invisible to the `dolt_rebase` UPDATE on conn B. Result: nondeterministic
  compaction failures, operating on the wrong DB, or a half-applied rebase — even
  with zero concurrent writers. (Listed as HIGH severity but MED-flagged for filing
  because compaction is periodic/maintenance-path; recommend filing after confirming
  run cadence.)
- **Fix:** Acquire one `conn, _ := db.Conn(ctx)` and run every step on that single
  `*sql.Conn` (or `db.SetMaxOpenConns(1)`), threaded through all helpers.

### D7. Surgical-rebase branch swap deletes `main` before the rename succeeds
- **File:** `compactor_dog.go:530-535`
- **Confidence:** MED · **Severity:** MED
- Step 8 does `DOLT_BRANCH('-D','main')` then `DOLT_BRANCH('-m', workBranch,'main')`.
  If the rename fails (or the shared timeout expires at this late step, or D6 sends
  them to different sessions), `main` is already gone and recovery is manual.
- **Fix:** Force-rename (`DOLT_BRANCH('-f','-m',...)`) or stage onto a temp so `main`
  is never absent.

### D8. `CrashLoopWindow` config is dead — window check is always true
- **File:** `restart_tracker.go:191,204-210`
- **Confidence:** HIGH · **Severity:** MED
- `info.LastRestart` is set to `now` at line 191; the guard then tests
  `info.LastRestart.After(now - CrashLoopWindow)` → `now.After(now-15m)`, always
  true. The streak start time is never recorded, so the 15m window constrains
  nothing — only the 30m `StabilityPeriod` reset gates accumulation. An agent
  restarting every ~25m (each gap < StabilityPeriod) accrues `RestartCount` and is
  flagged crash-looping after 5 restarts spanning ~2h, far wider than the configured
  window. The knob can only ever be too lax.
- **Fix:** Record the first-restart time of the current streak and compare *that*
  against `windowStart`; or reset `RestartCount` when the inter-restart gap exceeds
  `CrashLoopWindow`.

### D9. `processedLifecycleEvents` map grows unbounded for the daemon lifetime
- **File:** `convoy_manager.go:200,531,552`
- **Confidence:** HIGH · **Severity:** MED
- `sync.Map` keyed by unique/monotonic event ID, written via `Store`/`LoadOrStore`
  per close/reopen event, never deleted. Accumulates one entry per lifecycle event
  for the whole process lifetime (the code itself notes ~18k-close backlogs). Slow
  memory growth over weeks of uptime. (`feedChurn` has the same shape but is bounded
  by distinct issue IDs — lesser concern.)
- **Fix:** TTL/cap the map (drop entries older than the poll lookback HWM) or use a
  bounded LRU.

### D10. `RecordPause` bumps `LastRestart`, deferring the stability reset indefinitely
- **File:** `restart_tracker.go:221-234`
- **Confidence:** MED · **Severity:** MED
- `RecordPause` sets `info.LastRestart = now` without touching `RestartCount`. Both
  `RecordSuccess` and the `RecordRestart` reset key off
  `now.Sub(LastRestart) > StabilityPeriod`, so a session repeatedly hitting the
  usage-limit path (`restartStuckDeacon` → `RecordPause` each cycle) can never
  accrue 30m of quiet. The "pause doesn't count toward fault budget" intent is
  partially defeated: after the limit clears, the next real crash continues the old
  streak (escalated backoff) rather than starting fresh.
- **Fix:** Don't overwrite `LastRestart` in `RecordPause`; use a separate `LastPause`
  field for pause-backoff bookkeeping.

### D11. Failed `gt maintain` still stamps `lastMaintenanceRun`, suppressing retry ~20h
- **File:** `scheduled_maintenance.go:212-230`
- **Confidence:** MED · **Severity:** LOW
- The `d.lastMaintenanceRun = now` assignment is unconditional, outside the
  success/failure branch. A transient `gt maintain --force` failure burns the whole
  daily interval — no retry until the next window despite the 5-minute cadence.
- **Fix:** Only set `lastMaintenanceRun` on success.

### D12. Scheduler-stuck / feed-storm escalations marked sent on failure (same class as D5)
- **File:** `scheduler_stuck_dog.go:194-199`; `feed_storm_monitor.go:81-84,114-121`
- **Confidence:** HIGH (scheduler) / MED (feed_storm) · **Severity:** MED
- Both persist `Escalated = true` immediately after the error-less `escalate` call
  (feed_storm even sets it *before* firing). A failed escalation is never retried
  for the life of the episode — the storm/stall goes unescalated while still active.
- **Fix:** Flip/persist `Escalated` only after a successful escalation.

### D13. Goroutine leak in Dolt `stopLocked` force-kill path
- **File:** `dolt.go:1149-1166`
- **Confidence:** MED · **Severity:** MED
- The shutdown poll spawns `go func(){ for isProcessAlive(process) { sleep } }()` and
  `select`s on `done` vs 30s timeout. The timeout branch sends SIGKILL and returns
  *without joining `done`*; the poll goroutine exits only when the PID dies. Since
  the Dolt process is detached and PID-adopted after a daemon restart (not reaped via
  `cmd.Wait`), a lingering PID (D-state during disk flush — which the 30s grace
  explicitly anticipates) leaks one goroutine + a captured `*os.Process` per timed-out
  restart cycle.
- **Fix:** Drive the poll goroutine off a cancelable signal so the timeout branch
  always terminates it.

---

## LOW-confidence / smell findings (noted, not filed)

- **D14.** Startup dispatch goroutine uses `context.Background()` not `d.ctx`
  (`daemon.go:804→4479`) — not canceled on shutdown; bounded 5m + 1 goroutine. LOW.
- **D15.** `recordDogStartSuccess` never calls `restartTracker.Save()` while
  `recordDogStartFailure` does (`dog_startup_backoff.go:100-105`) — recovered-dog
  reset is in-memory only; stale backoff state survives a daemon restart. LOW.
- **D16.** Inconsistent `d.doltBreaker` nil-guarding: `curio_dog.go:79` and
  `failure_classifier_dog.go:331` deref unconditionally; `restart_pending_dog.go:95`
  and `scheduler_stuck_dog.go:142` guard `!= nil`. Always non-nil in prod (constructor),
  latent panic for literal-constructed `Daemon`. LOW.
- **D17.** Unescaped DB name interpolated into `USE \`%s\`` in `dolt_remotes.go`
  (`:117,127,137,176,257,277`) — db name from `os.ReadDir` not run through
  `validDBName` (which `jsonl_git_backup.go:245` does use). Local-fs trust boundary;
  a backtick in a dir name breaks the query. LOW.
- **D18.** `exportTableToJsonl` passes the Dolt password as `-u/-p` CLI args
  (`jsonl_git_backup.go:317-323`), visible in `ps`, bypassing the
  `DOLT_CLI_PASSWORD` env convention used by `buildDoltSQLCmd`. LOW (security).
- **D19.** Event-driven dispatch rate-limit slot consumed before dispatch succeeds
  (`auto_dispatch_watcher.go:255-286`) — failed dispatch still rate-limits the rig
  for the full 30s window. Heartbeat fallback covers it. LOW.
- **D20.** Long gate-suite runs (`runMainBranchTests`, minutes–tens of minutes ×
  N rigs) run inline in the main select loop (`daemon.go:911-916`), starving
  heartbeat + Dolt-health probe for the duration. May be intentional; flagged as an
  availability gap. MED-behavior / design question.
- **D21.** `MarkSessionActive` globs the raw (un-`/`→`-`-sanitized) session
  (`notification.go:196`) while `slotPath` sanitizes (`:45-54`) — no-op for sessions
  containing `/`. No live callers today; latent. LOW.
- **D22.** `RotateLogs` skips logs < 100MB and `enforceDiskBudget` only deletes
  `*.gz` (`log_rotation.go:68-71,263-289`) — a large uncompressed active log under
  100MB is never rotated and is undeletable by the budget enforcer. LOW.
- **D23.** Recovery callback fired as bare `go fn()` not tracked by `alertWg`
  (`dolt.go:824-833`) — abandoned mid-flight on shutdown, unlike the alert goroutines
  that `Stop()` drains. LOW.
- **D24.** `parseWispID` fallback can return a flag/garbage token as a wisp ID
  (`dog_molecule.go:217-226`) — cosmetic log noise (molecule tracking is best-effort).
  LOW.

---

## Areas checked and found clean

- Main `Run()` select loop is single-threaded; patrol ticks/heartbeat execute
  serially, so the many "no sync needed" heartbeat-only fields are safe **except**
  where the heartbeat fans out via `rigPool` (D1/D2).
- Mutex-guarded and verified safe under per-rig fan-out: `recordSessionDeath`,
  session-alarm maps, `emitMassDeathEvent`, `rigStatusCache`, all `daemonMetrics`
  accessors (also nil-safe).
- `RigWorkerPool` itself (bounded semaphore, per-rig timeout ctx, `wg.Wait()`) is
  correct — only the callers' shared-state mutations are unsafe.
- Patrol tickers are all created once and `Stop()`ed via `defer`; no per-dog
  goroutine leak, no `time.After`-in-loop leak.
- `KRCPruner` stops cleanly via ctx+WaitGroup. Parallel-gate fan-out writes disjoint
  slice slots. Attribution state serialized under `stateFileMu` with atomic
  tmp+rename. `restartWithBackoff` correctly re-checks `isRunning()` after the
  unlock/sleep/relock window. Reaper TOCTOU guards sound. No convoy lock-ordering
  deadlock (`storesMu` released before any blocking Dolt call; never held with
  `scanMu` across a blocking call).

---

## Existing beads (NOT duplicated by this audit)

gu-nid89.19 (ConvoyManager WaitGroup undercount), gu-xrkoq (boot-storm ~57 sessions),
gu-lv5lt (blocked-bead dispatch), gu-tucci (lost-update RMW races), gu-sz1xl (merge
slot RMW), gu-q326r (scheduler test hang), gu-40xsf (pre-push OOM), gu-48uk8
(audit-report no MR), gu-nid89.31 (poller drops drained nudges), gu-6reia/gu-gvwqx
(reaper 'hooked' omission), gu-1ufs3 (witness auto-save misclassify),
gu-nid89.38 (doctor stale-dolt-port).

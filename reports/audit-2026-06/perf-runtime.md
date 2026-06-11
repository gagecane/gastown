# Performance Audit — Runtime Inefficiencies

**Bead:** gu-nid89.11 (parent epic gu-nid89: Whole-Repo Gastown Audit)
**Date:** 2026-06-11
**Scope:** Runtime performance of hot-path packages — `internal/daemon`, `internal/witness`,
`internal/polecat`, `internal/beads`, plus subprocess (`bd`/`git`/`ps`), Dolt query, memory,
and file-I/O patterns reachable from daemon tick loops, witness/reaper patrols, and dispatch.

**Method:** Static read of the Go source at commit `a9afe585`. Four category sweeps
(concurrency, fork/exec, Dolt/beads queries, memory/file-I/O), then manual verification of
every High/Medium finding against the actual source (file:line, call cadence, lock span).
No code was modified. Findings whose impact did not survive verification were downgraded or
dropped — noted inline.

> **Caveat:** Impact estimates are reasoned from call cadence and fan-out, not from profiling.
> No CPU/heap profile or benchmark was run. Treat magnitudes as directional. Where I could not
> confirm a runtime cost (e.g. whether a Dolt column is indexed), the finding says so.

---

## Summary of findings

| # | Area | File:Line | Impact | Correctness risk? |
|---|------|-----------|--------|-------------------|
| C1 | Concurrency | `daemon/convoy_manager.go:347` | **High** | Yes — WaitGroup undercount |
| C2 | Concurrency | `polecat/manager.go:2673` | Medium | No |
| C3 | Concurrency | `daemon/convoy_manager.go:685` | Medium | No |
| C4 | Concurrency | `polecat/session_manager.go:658` | Medium | No |
| F1 | Fork/exec | `util/orphan.go:532,545` | **High** | No |
| F2 | Fork/exec | `doctor/rig_config_sync_check.go:252` | Medium | No |
| F3 | Fork/exec | `cmd/convoy_stage.go:1552` | Medium | No |
| Q1 | Dolt/beads | `reaper/orphan_reconcile_git.go:148` | **High** | No |
| Q2 | Dolt/beads | `beads/beads.go:1713` (GetAssignedIssue) | Medium | No |
| Q3 | Dolt/beads | `daemon/agent_heartbeat_dog.go:209` | Medium | No |
| Q4 | Dolt/beads | `daemon/reap_dead_agent_wisps.go:121` | Low | No |
| M1 | Memory/I/O | `daemon/jsonl_git_backup.go:933,957` | Medium | No |
| M2 | Memory/I/O | `daemon/pressure*.go` (/proc reads) | Low | No |
| M3 | Memory/I/O | `daemon/failure_classifier_dog.go:246` | Low | No |

**Top items recommended for fix beads:** C1, F1, Q1, Q2 (see "Proposed follow-up beads").

---

## Concurrency

### C1 — ConvoyManager spawns 3 goroutines but `wg.Add(2)` — **High** (correctness)
`internal/daemon/convoy_manager.go:347`

```go
m.wg.Add(2)
go m.runEventPoll()
go m.runStrandedScan()
go m.runStartupSweep()   // <-- third goroutine, not counted
```

`runStartupSweep` (line 1513) does **not** call `m.wg.Add(1)` itself — verified. So `Stop()`'s
`m.wg.Wait()` can return while `runStartupSweep` is still sleeping on its 10s timer and about to
call `m.scan()`. A scan firing after the manager believes it has stopped can spawn convoy checks
and `bd`/`gt sling` subprocesses during shutdown — a resource/lifecycle race, not just a perf nit.

**Fix:** `m.wg.Add(3)` (or add `m.wg.Add(1)` immediately before the `go m.runStartupSweep()` line).
One-line change; low risk. **Recommend a fix bead.**

### C2 — Unbounded goroutine fan-out in polecat enumeration — Medium
`internal/polecat/manager.go:2673`

One goroutine per polecat is spawned with no concurrency cap; each does `bd`/`git` subprocess
work. On a large rig (hundreds of polecats) this is a burst of hundreds of concurrent goroutines
each holding a subprocess + fds. Compare the bounded-semaphore pattern already used well in
`daemon/handler.go:480-516` (buffered-channel semaphore + WaitGroup) — that is the reference fix.

**Fix:** Bound fan-out with a semaphore sized O(10–50) instead of O(num polecats).

### C3 — `ConvoyManager.scan()` holds `scanMu` across sling/bd subprocess I/O — Medium
`internal/daemon/convoy_manager.go:685`

`scan()` takes `m.scanMu.Lock()` with `defer Unlock()` and, while holding it, calls
`feedFirstReady()` (which forks `gt sling` + `bd` per ready issue) and `closeEmptyConvoy()`.
A comment at the lock site states the serialization is **intentional** — it prevents concurrent
scans from spawning duplicate checks. So this is a deliberate trade, not a bug. The cost is that
the convoy scan is fully serialized including its subprocess time; a slow `gt sling` blocks the
next scan. Acceptable today (scans are infrequent, stranded set is small), but worth revisiting
if convoy volume grows — release the lock before the per-issue I/O once duplicate-spawn is guarded
another way (e.g. per-convoy in-flight set).

### C4 — Fire-and-forget startup-nudge verifier goroutine, no lifecycle tie — Medium
`internal/polecat/session_manager.go:658`

`go m.verifyStartupNudgeDelivery(...)` is spawned untracked; the function sleeps in a retry loop
(up to maxRetries × delay). Under heavy session spawning these accumulate, and daemon shutdown
neither waits for nor cancels them. Not a leak in steady state (they exit after retries), but
they should take a context so shutdown can cancel promptly and so they're bounded.

**Fix:** Pass a cancellable context (timeout-bounded) and/or track in a WaitGroup.

> **Downgraded from the sweep:** the `dolt.go:1039` "stopLocked polling goroutine leaks forever"
> finding was checked and is **not** a leak — the `select` has a 30s `time.After` that force-kills
> and the goroutine exits once the process is gone. The 30s lock-hold during graceful Dolt stop is
> real but intentional (avoids SIGKILL mid-journal corruption, per the code comment). Not filing.

---

## Fork / exec subprocess overhead

### F1 — Orphan detection forks `ps` once per candidate (N+1 fork) — **High**
`internal/util/orphan.go:532, 545` (and the zombie variant ~654, 672)

`FindOrphanedClaudeProcesses` / `FindZombieClaudeProcesses` iterate every candidate process and,
per candidate, call `isRealAgentProcess(pid)` and `isIDEClaudeProcess(pid)` — **each of which forks
`ps -p <pid> -o args=`** (verified at `orphan.go`, both helpers fork `exec.Command("ps", ...)`).
With N candidates that's up to 2N extra `ps` forks on top of the initial listing. On a busy host
(this codebase's own learnings cite 300+ zombie processes and load spikes >1000) this is a
meaningful fork storm. Called from deacon orphan patrol, `cmd/orphans`, `cmd/cleanup`, and
`start_orphan_unix` — i.e. routine cleanup paths.

**Fix:** Fork `ps -eo pid,args` **once** before the loop, build a `map[pid]argv`, and have both
helpers look up the map instead of re-forking per pid. Eliminates the 2N forks.
**Recommend a fix bead.**

### F2 — Doctor forks `bd show` once per rig (N+1) — Medium
`internal/doctor/rig_config_sync_check.go:252`

Inside the per-rig loop, `rigBeadExists()` forks `bd show <rig-identity-id>` per rig. With dozens
of rigs that's dozens of `bd` cold-start forks per doctor cycle. `doltDatabaseExists()` is likewise
called per rig (line 234/238) via in-process `doltserver.ListDatabases()` — cheaper, but still
re-enumerates per rig.

**Fix:** One `bd list` (all rig-identity beads) up front into a set; membership-check locally.
Hoist `ListDatabases()` out of the loop.

### F3 — Convoy stage forks `bd show` per child bead (N+1) — Medium
`internal/cmd/convoy_stage.go:1552`

`bdListChildrenViaDeps()` loops child IDs and forks `bd show <id> --json` per child. A 20-child
convoy = 20 forks. Path is `gt convoy stage` (interactive/dispatch), not a tick loop, so cadence
is lower — hence Medium.

**Fix:** `bd list --parent=<id> --json` (single fork) where the dep model allows, or a batched
multi-id `bd show`.

---

## Dolt / beads query patterns

### Q1 — N+1 `bd.Show()` in reaper orphan reconciliation — **High**
`internal/reaper/orphan_reconcile_git.go:133-148`

Lists all `awaiting_refinery_merge` issues, then issues an individual `bd.Show()` per candidate in
the loop (line 148). Each `Show` is a separate query (and, via the `bd` CLI path, a separate fork).
Runs on the reaper cycle; with a large awaiting-merge backlog this is 1 + N round-trips.

**Fix:** Batch — either a single multi-ID fetch (`ShowMultiple`) or have the list call return full
`Issue` objects so the per-row `Show` is unnecessary. **Recommend a fix bead.**

### Q2 — `GetAssignedIssue` issues 3 sequential `List` calls — Medium
`internal/beads/beads.go:1713`

Loops `["open", "in_progress", "hooked"]` and calls `b.List()` once per status — three full
query/fork round-trips per assignment check, returning on the first non-empty.

```go
for _, status := range []string{"open", "in_progress", StatusHooked} {
    issues, err := b.List(ListOptions{Status: status, Assignee: assignee, Priority: -1})
    ...
}
```

**Note / caveat:** the clean fix is a single multi-status query, but I could **not** confirm
`ListOptions.Status` / the `bd list` flag accepts a comma-separated status list — that needs a
quick check before implementing. If multi-status isn't supported, the alternative is one
`Status: "all"` + assignee query filtered to the three active statuses in Go (trades a wider scan
for fewer round-trips). **Recommend a fix bead** that first confirms the `bd` flag capability.

### Q3 — Per-rig MR-count query on every heartbeat-dog tick — Medium
`internal/daemon/agent_heartbeat_dog.go:209`

Loops every rig and calls `PendingMergeRequestCount()` → `ListMergeRequests()` per rig, per tick.
With many rigs and frequent ticks this re-enumerates MR tables repeatedly. `ListMergeRequests`
itself (beads.go:1619) queries issues then wisps and dedups in Go — two queries per call.

**Fix:** Short-TTL cache (e.g. 1–5 min) of per-rig MR counts, or a single cross-rig aggregate.
Confirm with a profile before investing — tick cadence and rig count determine whether this is
actually hot.

### Q4 — Two `bd list` calls for dual-status wisp reap — Low
`internal/daemon/reap_dead_agent_wisps.go:121-145`

Separate `bd list --status=hooked` and `--status=in_progress` calls where one multi-status query
would do. Same multi-status-flag caveat as Q2. Minor; folds into the Q2 fix if the flag exists.

> **Index recommendations (unverified):** several hot filters key on `status`, `(label,status)`,
> `(assignee,status)`, and `wisp_labels(issue_id)`. I did **not** inspect the Dolt schema, so I
> cannot confirm which indexes already exist. Before filing index work, dump the schema
> (`bd`/Dolt `SHOW INDEX`) and confirm the gap — listed here only as a lead, not a finding.

---

## Memory & file I/O

### M1 — JSONL git-backup reads whole files into memory, twice — Medium
`internal/daemon/jsonl_git_backup.go:933` (filter) and `:957` (verify)

`applyPollutionFilter()` loops databases and `os.ReadFile`s each `issues.jsonl` fully into memory;
`verifyNoPollution()` then re-reads the same files in a second pass. For multi-MB JSONL across many
DBs this is double the I/O and full-file allocations. Per-line `json.Unmarshal` into
`map[string]interface{}` (lines 659/969) also allocates a map per record.

**Fix:** Stream with `bufio.Scanner`; fold filter+verify into a single pass; unmarshal into a typed
struct instead of `map[string]interface{}`. This is a backup/maintenance path (not every tick), so
Medium not High.

### M2 — `/proc/loadavg` and `/proc/meminfo` read via `os.ReadFile` on each pressure check — Low
`internal/daemon/pressure.go:157`, `internal/daemon/pressure_linux.go:17`

`checkPressure()` (callers: refinery/dog/polecat spawn decisions) reads `/proc/loadavg` and
`/proc/meminfo` fresh each call. Files are tiny (a few KB) so cost per call is small; under
high-spawn bursts the repeated syscalls + transient allocations add up modestly.

**Fix:** Cache pressure for ~1–5s with time-based invalidation; sub-second freshness isn't needed
for spawn gating. Low priority.

### M3 — Failure-classifier recompiles regexes each run — Low (downgraded)
`internal/daemon/failure_classifier_dog.go:246`

`runFailureClassifier()` reloads signatures and calls `compileSignatures()` (recompiling every
regex) each invocation. **Downgraded from the sweep's "High":** verified cadence is
`defaultFailureClassifierInterval = 15 * time.Minute` with a small signature set — recompiling a
handful of regexes every 15 minutes is negligible. Worth a tidy (compile-once cache, reload on
file mtime change) but not a performance priority.

> **Dropped from the sweep (not real / too minor to file):** numerous `+=` string-concat and
> `append`-without-preallocation sites (`molecule.go:313/408`, `townlog/logger.go:131`,
> `witness/handlers.go:528/4630`, `config/loader.go:2418`) are all in cold paths (molecule
> instantiation, one-shot startup, per-event logging of bounded size). Real micro-inefficiencies
> but not hot-path; listing for completeness, not filing beads.

---

## Proposed follow-up beads

Recommend filing these as children of the epic (gu-nid89), highest value first:

1. **C1 (P1, correctness):** Fix ConvoyManager WaitGroup undercount (`wg.Add(2)`→`Add(3)`),
   `convoy_manager.go:347`. One-liner; prevents shutdown race.
2. **F1 (P2):** Eliminate per-candidate `ps` forks in orphan/zombie detection — fork `ps` once,
   look up a pid→argv map. `util/orphan.go`.
3. **Q1 (P2):** Batch the N+1 `bd.Show()` in reaper orphan reconciliation,
   `reaper/orphan_reconcile_git.go:148`.
4. **Q2 (P3):** Collapse `GetAssignedIssue`'s 3 sequential `List` calls into one (pending
   confirmation that the `bd list` status flag accepts a multi-status value), `beads/beads.go:1713`.

C2/C3/C4, F2/F3, Q3, M1 are valid Medium follow-ups but lower urgency; group into a single
"perf cleanup" bead if desired rather than one each.

---

## Sources

- Static analysis of the Gastown repository at commit `a9afe585`, branch
  `polecat/radrat/gu-nid89.11--mqa11u9l`. Files cited inline by path:line.
- Audit bead: gu-nid89.11; parent epic gu-nid89.

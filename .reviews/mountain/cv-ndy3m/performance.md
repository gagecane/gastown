# Performance Axis

## Summary
The dominant theme is the **daemon main loop**. `Daemon.Run()`
(`internal/daemon/daemon.go:843-1066`) is a single-goroutine `select`: the
heartbeat tick and ~25 patrol-ticker cases each invoke their handler
**synchronously on that one goroutine**, and `heartbeat()` itself runs ~25
phases serially (`daemon.go:1148-1310`). The per-rig pool (`worker.go`
`runPerRig`) parallelizes *within* a phase but always `wg.Wait()`s, so the
heartbeat blocks until the slowest rig finishes. Consequently any multi-minute,
network-bound, or unbounded operation placed on that goroutine stalls the
heartbeat, every patrol, signal handling, and step-14 dispatch until it
returns. The highest-impact findings are all instances of this single
structural issue, and the right pattern already exists in-tree:
`AutoDispatchWatcher` (`auto_dispatch_watcher.go:150`) runs dispatch on its own
goroutine.

By contrast, the two leak classes that have *historically* bitten gastown —
per-iteration connection leaks and goroutine leaks — are now well-hardened.
The convoy event-poll opens stores once and reuses them with a query timeout
(`convoy_manager.go:414-475`, `pollStore` `:502-522`), there is a dedicated
`dolt_conn_leak_monitor.go` defense-in-depth subsystem, and every long-lived
loop uses the canonical `ctx.Done()` + `wg.Wait()` + `defer ticker.Stop()`
lifecycle. The residual N+1 issues are on the dispatch *validation* path
(per-bead config reload + `bd show` subprocesses), real but lower-frequency
than the daemon-loop blocking.

## Score
score: 0.62

## Critical Findings (P0 — file as beads, fix urgently)

### Full CI/test suite (20-40 min) runs inline on the daemon select loop
- **Location**: `internal/daemon/daemon.go:944-949` → `internal/daemon/main_branch_test_runner.go:343` (`runMainBranchTests`); gate exec at `main_branch_test_runner.go` per-rig loop (`:391+`, behind `acquireGlobalGateLock`)
- **Impact**: The `patrols.mainBranchTest` case calls `d.runMainBranchTests()`
  directly on the select goroutine. That method loops over every rig and runs
  each rig's full quality-gate suite — the code's own comments repeatedly
  describe these as "act/Docker, 20-40min" runs (`main_branch_test_runner.go:38,
  53, 61, 349`). While it runs, the entire select loop is blocked: no
  heartbeat, no signal handling, no dispatch. There is a host-load *defer* guard
  (`:355-361`) and a per-rig timeout, but on a normally-loaded host the suite
  still executes inline and can hold the loop for the better part of an hour
  across rigs.
- **Suggested fix**: Kick `runMainBranchTests` off on a dedicated worker
  goroutine guarded by an "in progress" flag (mirror `AutoDispatchWatcher`); the
  tick should only start it and return. Keep the existing global gate lock and
  per-gate timeout.
- **Fingerprint**: `axis:performance|internal/daemon/daemon.go|mainBranchTest-inline-on-select-loop`

### `gt scheduler run` dispatch blocks the heartbeat for up to 5 minutes
- **Location**: `internal/daemon/daemon.go:4602-4624` (`dispatchQueuedWork`),
  invoked as heartbeat phase 14 at `daemon.go:1300`
- **Impact**: Phase 14 shells out to `gt scheduler run` via
  `exec.CommandContext(ctx, ...)` with a **5-minute** timeout and blocks on
  `cmd.CombinedOutput()`. This runs synchronously inside `heartbeat()` on the
  loop goroutine, so a slow or stuck scheduler stalls the whole heartbeat — and
  therefore all remaining phases and patrol handling — for up to the full 5
  minutes (the comment at `daemon.go:1087` notes it "was timing out at 5m").
  The dispatch path is exactly the hot path this axis is meant to protect.
- **Suggested fix**: Run dispatch on its own goroutine with a non-blocking
  in-flight guard (it already self-serializes via `scheduler-dispatch.lock`), so
  the heartbeat finishes its other phases and the next tick can fire while
  dispatch drains. At minimum drop the inline timeout well below the heartbeat
  interval.
- **Fingerprint**: `axis:performance|internal/daemon/daemon.go|dispatchQueuedWork-5m-blocks-heartbeat`

## Major Findings (P1 — track, do not auto-bead)

### `git fetch --prune` per rig (network I/O) on every heartbeat is not cancelable
- **Location**: `internal/daemon/daemon.go:4560` (`g.FetchPrune("origin")` in
  `pruneStaleBranches`, heartbeat phase 13 at `:1292`) → `internal/git/git.go:1199`
  `FetchPrune` → `git.go:153` `run` → `:164` `runRaw` (`exec.Command("git", ...)`,
  **no context**)
- **Impact**: Phase 13 runs `git fetch --prune origin` (a network call) for
  every rig plus the town root. Although `pruneStaleBranches` runs under
  `runPerRig` with a 30s per-rig context, `Git.run`/`runRaw` build a plain
  `exec.Command` with **no context** (verified `git.go:175`), so the 30s
  deadline cannot cancel a hung fetch. The heartbeat blocks on `wg.Wait()` until
  every rig's fetch returns; one unreachable remote hangs the heartbeat
  indefinitely.
- **Suggested fix**: Plumb the per-rig `ctx` into `Git` (`exec.CommandContext`)
  so network ops honor the deadline, or move branch pruning to a low-frequency
  off-loop patrol.
- **Fingerprint**: `axis:performance|internal/git/git.go|runRaw-no-context-on-network-fetch`

### Pre-spawn `git fetch` + `git pull --rebase` (60s each) block the heartbeat during agent spawn
- **Location**: `internal/daemon/lifecycle.go:652-659` (fetch), `:698-700` (pull)
  in `syncWorkspace`, called from `startSession` (`lifecycle.go:377`); reached
  via the ensure{Deacon,Witness,Refinery}Running heartbeat phases
  (`daemon.go:1157,1187,1201`)
- **Impact**: When the heartbeat spawns/restarts a persistent-worktree agent,
  `syncWorkspace` runs `git fetch origin` then `git pull --rebase`, each with a
  **60s** `gitNetworkTimeout` (`lifecycle.go:26`). These are correctly
  `CommandContext`-cancelable, but they run inside the per-rig pool and are
  `wg.Wait()`-joined into the heartbeat, so a slow remote can add up to ~2
  minutes of network wait per spawning rig before dispatch (phase 14) even
  starts.
- **Suggested fix**: Spawn the sync+session-start as a detached worker
  (heartbeat records "spawn in flight" and returns), or cut the 60s budget.
- **Fingerprint**: `axis:performance|internal/daemon/lifecycle.go|syncWorkspace-60s-fetch-pull-on-spawn`

### `gt-idle-check` subprocess runs with no timeout inside the Boot heartbeat phase
- **Location**: `internal/daemon/daemon.go:1810-1813` (in `ensureBootRunning`,
  heartbeat phase 2 at `:1170`)
- **Impact**: Phase 2 execs the `gt-idle-check` binary with `cmd.CombinedOutput()`
  and **no context/timeout** (`exec.Command`, not `CommandContext`). It runs on
  the loop goroutine; if the binary hangs (it queries tmux/beads), the heartbeat
  stalls with no deadline.
- **Suggested fix**: Use `exec.CommandContext` with a few-second timeout so a
  wedged idle-check degrades to "needs waking" instead of freezing the heartbeat.
- **Fingerprint**: `axis:performance|internal/daemon/daemon.go|gt-idle-check-no-timeout-on-heartbeat`

### N+1: full rigs-config reload + two `bd show` subprocesses per bead on the dispatch validation path
- **Location**: `internal/cmd/capacity_dispatch.go:518-519` (per-bead `Validate`
  seam) → `validatePendingBeadForDispatch` (`:1336`) → `rigBeadsPrefix`
  (`rig_helpers.go:83`) → `rig/operational.go:204` `RigBeadsPrefix` →
  `config/loader.go:89` `LoadRigsConfig` (uncached `os.ReadFile` + `json.Unmarshal`
  + validate). Dry-run path `validateDryRunDispatchPlan`
  (`capacity_dispatch.go:1306-1326`) additionally fires `getBeadInfoFromTownRoot`
  (`bd show`) and `verifyBeadExistsInTargetRigDatabase` (`bd show --json`) per bead.
- **Impact**: `capacity.DispatchCycle.Run` calls `Validate` once per planned bead
  (`dispatch.go:162-170`). Each call re-reads and re-parses `mayor/rigs.json`
  from disk — `LoadRigsConfig` is uncached (verified `loader.go:89-110`). On the
  dry-run plan path each bead additionally spawns up to two `bd show`
  subprocesses (each opening a Dolt connection). That is up to one config reload
  + two subprocess/DB round-trips × N beads per cycle. The sibling
  `filterByPerRigCapacity` already memoizes the identical per-rig lookup;
  `Validate` does not.
- **Suggested fix**: Load `RigsConfig` once at the top of the dispatch cycle and
  pass a `map[rig]prefix` into the `Validate` closure; batch a single
  `bd show id1 id2 … --json` for all `plan.ToDispatch` IDs into a map the loop
  consults.
- **Fingerprint**: `axis:performance|internal/cmd/capacity_dispatch.go|per-bead-rigsconfig-reload-and-bd-show`

### Serial connect-per-database hooked-mail scan on every heartbeat
- **Location**: `internal/daemon/hooked_beads_metrics.go:59-82` (loop), called
  from `daemon.go:1310` every heartbeat
- **Impact**: Each heartbeat walks every rig database serially, opening and
  closing a fresh `sql.DB` (`reaper.OpenDB` → `sql.Open`) per database before a
  cheap COUNT. The queries are cheap; the serialized per-db connect/handshake
  latency × N rigs is the cost, paid inline on the heartbeat phase.
- **Suggested fix**: Fan out across databases with the bounded `RigWorkerPool`
  pattern already used elsewhere in this file (collect counts under a mutex), or
  reuse a pooled connection per db.
- **Fingerprint**: `axis:performance|internal/daemon/hooked_beads_metrics.go|serial-connect-per-db-on-heartbeat`

## Minor Findings (P2 — informational)

### Preallocate filter result slices in the dispatch pipeline
- **Location**: `internal/scheduler/capacity/pipeline.go:113-124`
  (`FilterNoAutoDispatch`), `:162-173` (`FilterMessagingBeads`), `:205-214`
  (`BlockerAware`)
- **Impact**: These filters run every dispatch cycle over the full candidate
  list and almost always remove zero beads, but build the result via `append`
  on a `nil` slice — repeated regrow+copy of `PendingBead` structs when the
  final length is known to be `<= len(beads)`.
- **Suggested fix**: `result := make([]PendingBead, 0, len(beads))`.
- **Fingerprint**: `axis:performance|internal/scheduler/capacity/pipeline.go|filter-result-slice-not-preallocated`

### `bd list` re-queried once per molecule during rig seeding
- **Location**: `internal/rig/manager.go:2090-2094` (`seedPatrolMoleculesManually`)
- **Impact**: Runs a full `bd list --type=molecule --format=json` subprocess
  inside the per-molecule loop to existence-check by title. Genuine
  per-iteration query but bounded (fixed slice) and on the one-time
  rig-creation path, not under sustained load.
- **Suggested fix**: List molecules once before the loop, check titles against
  the single result.
- **Fingerprint**: `axis:performance|internal/rig/manager.go|bd-list-per-molecule-on-seed`

### Process-death poll goroutine has no hard exit if SIGKILL fails to reap
- **Location**: `internal/daemon/dolt.go:1205`
- **Impact**: A poll goroutine loops `for isProcessAlive(process) { sleep 100ms }`
  and exits only when the process dies; the caller's `select` waits 30s then
  sends SIGKILL and returns without joining `done`. In practice the daemon's own
  `cmd.Wait()` reaps the child, so this self-terminates — an edge case, not a
  confirmed leak.
- **Suggested fix**: Pass a context into the poll goroutine and `return` on
  `ctx.Done()`, or bound iterations after SIGKILL.
- **Fingerprint**: `axis:performance|internal/daemon/dolt.go|process-poll-goroutine-no-ctx-exit`

## Verified-clean (checked, NOT findings)
- **Convoy event-poll connection leak (prior incident)**: remediated — stores
  opened once and reused, query timeout + cancel (`convoy_manager.go:414-475,502-522`),
  plus a dedicated `dolt_conn_leak_monitor.go` watchdog.
- **Goroutine lifecycles**: all long-lived loops select on `ctx.Done()` with
  `defer ticker.Stop()` and are joined via `wg.Wait()`; all per-request fan-out
  goroutines are semaphore-bounded (`worker.go`, `handler.go:494`,
  `main_branch_test_runner.go`) and joined.
- **Resource handles**: `compactor_dog.go`, `doltserver.go` heartbeat probes,
  `feed/curator.go`, `events`/`channelevents` all `defer Close()` connections,
  rows, and files; subprocess calls use `Run`/`Output`/`CombinedOutput` (which
  reap the child).
- **regexp.MustCompile**: all package-level, none in loops.
- **Reaper / compactor / dolt-backup serial DB iteration**: intentionally serial
  — these are heavy long-interval dog patrols where parallelism would spike host
  load (the inverse gate-run-storm incident).

## Counts
counts: critical=2 major=6 minor=3

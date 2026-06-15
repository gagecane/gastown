# Test Coverage & Quality Axis

## Summary

The gastown tree is, on the whole, **well-tested**: nearly every package under
`internal/` and `cmd/` has a companion `_test.go`, the core merge logic
(`internal/refinery/engineer_pr_merge_test.go`), dispatch loop
(`internal/daemon/auto_dispatch_watcher_test.go`), recovery
(`reclaim_stalled_polecat_test.go`), and the `gt done` completion path
(`internal/polecat/completion/*` — every source file has a real test) are
exercised with real git repos and value/negative-path assertions, not theatre.
The top theme is therefore not "no tests" but **two narrower risks**: (1) a
re-opened instance of the known production-Dolt test-pollution class in
`internal/autotestpr`, and (2) a cluster of `sleep-then-assert-negative` flaky
patterns plus a handful of permissive no-op tests in `internal/refinery/manager_test.go`.

A secondary structural point worth stating plainly for the "what would catch a
regression today" question: **per-push CI runs `go test -race -short` and does
NOT install `bd` or Dolt** (`.github/workflows/ci.yml:178`). All 41 test files
that require a live `bd`/Dolt/tmux are `//go:build integration` tagged and run
**only nightly** (`nightly-integration.yml`). This is a documented, deliberate
tradeoff (gu-d9m7o, gu-xzc5p — integration suite is slow/Dolt-heavy), so it is
**not new debt** — but it means a Dolt-layer or bd-routing regression would NOT
be caught on the PR that introduces it, only by the nightly run. Reviewers
relying on green PR checks should know the per-push net excludes the entire
integration tier.

## Score
score: 0.78

## Critical Findings (P0 — file as beads, fix urgently)

- **Title**: `autotestpr` integration test writes to production Dolt with no port isolation and no cleanup
- **Location**: `internal/autotestpr/branch_gc_integration_test.go:259-266` (`initBeadsDB`), prefix gen at `:59`, header admission at `:14`
- **Impact**: `initBeadsDB` runs `exec.Command("bd", "init", "--prefix="+prefix)` with **no `--server-port`** flag, and the package has **no `TestMain`** (confirmed: `grep "func TestMain" internal/autotestpr/` → none) and never calls `requireDoltServer`/`RequireDoltContainer`. When the test runs (gated `GT_RUN_OQ4_SPIKE=1` + `//go:build integration`), `bd` auto-detects the shared production Dolt server on **:3307** and creates real `beads_ret<N>` databases with **zero teardown** (no `DROP DATABASE`, no `t.Cleanup`, no container). Because the leaked DBs hold user tables, `gt dolt cleanup` refuses to remove them — exactly the orphan-DB pollution that degrades the production server. This is the **unfixed twin of the `crew` incident (gs-z76 / gu-4str3)**; `crew` was fixed with a stub-`bd` `TestMain` (`internal/crew/testmain_test.go`) but `autotestpr` never received that treatment.
- **Suggested fix**: Mirror the `cmd/` integration helpers — add a package `TestMain` calling `EnsureDoltContainerForTestMain` (or call `requireDoltServer(t)` in the test), forward `--server --server-port $GT_DOLT_PORT` in `initBeadsDB` (`:261`), and add a `t.Cleanup` dropping the `beads_ret*` databases. Closes the last instance of the gu-4str3/gs-z76 production-pollution class.
- **Fingerprint**: `axis:test-coverage|internal/autotestpr/branch_gc_integration_test.go|initBeadsDB-no-port-isolation`

## Major Findings (P1 — track, do not auto-bead)

- **Permissive no-op test — `TestManager_Queue_NoBeads`** verifies nothing.
  `internal/refinery/manager_test.go:106-120`. Passes on **both** branches: if
  `Queue()` errors it `t.Logf`s and returns; if it succeeds it `t.Log`s and
  returns. No assertion can ever fail. `Queue()` is the refinery merge-queue
  read path. The sibling `TestManager_Queue_FiltersClosedMergeRequests` (:121)
  is the real test, so impact is false-confidence + maintenance noise. Fix:
  delete or make it assert the expected no-DB error.
  Fingerprint: `axis:test-coverage|internal/refinery/manager_test.go|TestManager_Queue_NoBeads-noop`

- **Permissive no-op test — `TestManager_IsRunning_NoSession`**.
  `internal/refinery/manager_test.go:76-92`. `if err != nil { t.Logf(...); return }`
  swallows the no-tmux case, making the only real assertion (`if running` at :89)
  unreachable in CI (no tmux server). The named "NoSession → false" behavior is
  unverified per-push. Fix: assert behavior under an injected/faked tmux or skip
  explicitly with a tracking note rather than silently passing.
  Fingerprint: `axis:test-coverage|internal/refinery/manager_test.go|TestManager_IsRunning_NoSession-permissive`

- **Flaky: sleep-then-assert-negative on Dolt recovery callbacks.**
  `internal/daemon/dolt_test.go:337` (`TestSetRecoveryCallback_Clear`) and `:369`
  (`TestClearUnhealthySignal_NoFireWhenNotUnhealthy`), plus
  `internal/daemon/convoy_manager_test.go:2738`
  (`TestDoltRecoveryCallback_NoFireWhenAlreadyHealthy`). Each does
  `time.Sleep(100ms)` then asserts `!called.Load()`. A negative assertion behind
  a fixed sleep can only ever *under*-wait — on a loaded host the erroneous
  goroutine may simply not have run yet, so it passes for the wrong reason; it
  can never positively confirm "the callback will not fire." This guards the
  **critical-path Dolt recovery callback**. Fix: expose a "callbacks drained"
  barrier (enqueue a sentinel callback and await *it*), or inject a fake clock.
  Fingerprint: `axis:test-coverage|internal/daemon/dolt_test.go|recovery-callback-sleep-negative`

- **Flaky: fixed 6s sleep waits for a real 5s async poll.**
  `internal/daemon/convoy_manager_integration_test.go:81` and `:102` —
  `time.Sleep(6 * time.Second)` hardcoded to outlast the 5s event-poll tick
  before asserting a closed issue was detected. Under GC/CI/Dolt-latency the
  poll can slip past 6s → flaky failure. The same file already uses a proper
  `select{}`-with-timeout for `Stop()` (`:110-115`); the poll-completion signal
  should be exposed and awaited the same way instead of sleeping.
  Fingerprint: `axis:test-coverage|internal/daemon/convoy_manager_integration_test.go|sleep6s-poll-wait`

- **Flaky: keepalive ticker cancellation race.**
  `internal/polecat/heartbeat_test.go:601-618` (`TestWithKeepalive_CancelStopsTicker`)
  — `Sleep(50ms)`, `cancel()`, `Sleep(100ms)`, then assert the heartbeat
  timestamp did not advance. The 10ms ticker + filesystem write under load can
  land a write after the cancel is observed. Fix: have `WithKeepalive` return a
  "stopped" channel to await deterministically.
  Fingerprint: `axis:test-coverage|internal/polecat/heartbeat_test.go|keepalive-cancel-race`

- **Flaky: real tmux/pipe/port tests paced by fixed sleeps.**
  `internal/tmux/respawn_hook_test.go:207-216` (`Sleep(500ms)`/`Sleep(300ms)`/
  `Sleep(5s)` to catch a respawn race against a 3s shell-sleep hook — thin
  margin), `internal/tmux/session_creation_test.go:118,139` and
  `dialog_acceptance_test.go:24-160` (sleep-then-`CapturePane`),
  `internal/acp/proxy_test.go:911,922` and
  `internal/acp/forward_from_agent_test.go:71-326` (13× `Sleep(100ms)` pacing
  pipe handoff), `internal/proxy/server_test.go:310` (`Sleep(20ms)` waiting for
  a listener). The repo already has the right idiom — poll-with-deadline
  (`dolt_test.go:297-303`, `runGTCmdOutputUntil` in
  `scheduler_integration_helpers_test.go:59`). Migrate these to it.
  Fingerprint: `axis:test-coverage|internal/tmux,acp,proxy|fixed-sleep-for-async-io`

- **Per-push CI excludes the entire integration tier (awareness, not a bug).**
  `.github/workflows/ci.yml:122` runs `-race -short` and `:178` confirms it does
  **not** install bd or Dolt; 41 test files (`//go:build integration`) only run
  nightly (`nightly-integration.yml`). A Dolt-routing / bd-schema regression
  would pass per-push and only surface nightly. Deliberate tradeoff (gu-d9m7o,
  gu-xzc5p) — flagged so reviewers don't over-trust green PR checks. No action
  required unless the team wants a minimal smoke-tier of the most critical
  integration tests gated per-push.
  Fingerprint: `axis:test-coverage|.github/workflows/ci.yml|integration-tier-nightly-only`

## Minor Findings (P2 — informational)

- **`TestManager_Status_NotRunning`** (`internal/refinery/manager_test.go:94-104`)
  asserts only `err != nil` and `t.Logf`s the value — never checks it's the
  expected `ErrNotRunning` vs an unrelated tmux error.
  Fingerprint: `axis:test-coverage|internal/refinery/manager_test.go|Status-NotRunning-weak-err`

- **`TestManager_FindMR_NoBeads`** (`internal/refinery/manager_test.go:171-177`)
  asserts only `err != nil` with comment "Any error is acceptable" — doesn't
  distinguish not-found from another failure on a merge-lookup path.
  Fingerprint: `axis:test-coverage|internal/refinery/manager_test.go|FindMR-NoBeads-weak-err`

- **`TestScanMu_PreventsConcurrentScans`** (`internal/daemon/convoy_manager_test.go:2605`)
  launches 5 concurrent `scan()`s but makes **no assertion** on serialization
  ("sling was never called ... acceptable for race test"). Only useful under
  `-race` for panic/data-race detection; doesn't verify the mutex actually
  serialized. Rename/strengthen to assert serialization.
  Fingerprint: `axis:test-coverage|internal/daemon/convoy_manager_test.go|ScanMu-no-assertion`

- **`internal/connection` registry path lightly tested.** `registry.go`
  (`MachineRegistry` load/save/Add/Remove/Get/Connection) has no direct test;
  `address_test.go` covers only address parsing. Not critical-path (machine
  registry is multi-host config), hence minor.
  Fingerprint: `axis:test-coverage|internal/connection/registry.go|MachineRegistry-untested`

- **Two non-trivial source files with no test:** `cmd/gt-proxy-client/main.go`
  and `internal/testutil/doltcleanup/doltcleanup.go`. The latter is itself a
  test-cleanup helper (lower risk); the former is a thin client `main`.
  Fingerprint: `axis:test-coverage|cmd/gt-proxy-client,internal/testutil/doltcleanup|no-test-file`

- **`internal/daemon/restart_tracker_test.go:260`** sleeps 110ms for a 100ms
  crash-loop-expiry window (10ms margin) because `restart_tracker.go` uses
  `time.Now()`/`time.Since` directly with no injectable clock. Slow-path, short
  sleep → minor, but a clock interface would make expiry deterministic.
  Fingerprint: `axis:test-coverage|internal/daemon/restart_tracker.go|no-injectable-clock`

## Counts
counts: critical=1 major=7 minor=6

# Complexity Hotspot Audit

## Summary

Gas Town's `internal/` Go code (840 non-test files, ~304k LOC in this worktree)
is, on average, healthy: mean cyclomatic complexity is **5.73**, and the
overwhelming majority of files are small and single-purpose. The debt is not
spread thin — it is **concentrated in a handful of monster CLI entry-point
functions** that have accreted every edge case of a complex distributed
workflow into one linear `run*` body. The two worst offenders, `runDone`
(`internal/cmd/done.go`) and `runSling` (`internal/cmd/sling.go`), are
genuine outliers by every metric: 1858 and 1092 source lines, cyclomatic
complexity 323 and 203, cognitive complexity 806 and 345 respectively. For
scale, the next-highest cognitive-complexity function in the tree scores 188.

The dominant theme is **"god command" functions**: top-level `cobra` handlers
that orchestrate detached-HEAD recovery, push retries, MR submission, bead
state transitions, zombie detection, and Dolt commits inline rather than
delegating to named, testable units. This compounds with the second theme —
**high churn on under-tested giants**: `internal/daemon/daemon.go` is the most
frequently changed file in the repo (281 commits/6mo) yet `Daemon.Run` alone is
a 723-line, CC-98 method, and the file's test-to-source ratio (~0.29) is the
lowest among the hot files. Frequent change to a large body with a thin safety
net is exactly where the next regression will originate. There are also routine
style-level smells (10-plus-parameter functions, boolean flag params) but those
are minor relative to the structural giants.

## Score

score: 0.46

## Critical Findings (P0 — file as beads, fix urgently)

- **`runDone` is a 1858-line function with cyclomatic complexity 323 and
  cognitive complexity 806 — the single most dangerous function in the tree.**
  - **Location**: `internal/cmd/done.go:298` (body spans to ~line 2155).
  - **Impact**: This is the polecat completion path — the most
    safety-critical control flow in the system (push, MR submit, bead
    transition, sandbox teardown). It contains **352 control-flow statements**
    (`if`/`for`/`switch`/`select`) and nests up to **6 tab levels** deep
    (detached-HEAD fallback logic around `done.go:171-179`). No human can hold
    this in their head; every change risks an untested interaction. It is also
    high-churn: `done.go` is the #2 most-changed file (239 commits/6mo). The
    next bug in the completion path will almost certainly come from here.
  - **Suggested fix**: Extract by phase into named, independently testable
    functions — `resolveBranch()`, `pushWithRetry()`, `submitToMergeQueue()`,
    `transitionBead()`, `teardownSandbox()` — each returning a typed result so
    `runDone` becomes a readable top-level orchestrator. Add table-driven
    tests per extracted phase before refactoring the next.

- **`runSling` is a 1092-line function with cyclomatic complexity 203 and
  cognitive complexity 345.**
  - **Location**: `internal/cmd/sling.go:209` (body spans to ~line 1300).
  - **Impact**: The dispatch entry point — second-highest churn file in the
    repo (233 commits/6mo). Deep nesting (5+ levels at `sling.go:192-194`)
    around batch-vs-explicit-rig scheduling means the branching for scheduler
    ID-type validation is buried where it is hard to test or audit.
    Notably much of the real work is *already* delegated to
    `executeSling`/`scheduleBead`, so `runSling`'s bulk is argument parsing,
    validation, and mode branching that can be peeled off cleanly.
  - **Suggested fix**: Pull the flag/validation/mode-selection prologue into a
    `parseSlingInvocation() (SlingPlan, error)` so `runSling` reduces to
    parse → plan → execute. The downstream `executeSling` (453 lines, CC 69)
    deserves the same treatment as a follow-up.

- **`internal/daemon/daemon.go`: highest-churn file in the repo paired with a
  723-line `Daemon.Run` and the lowest test ratio among hot files.**
  - **Location**: `internal/daemon/daemon.go:569` (`Daemon.Run`, CC 98,
    cognit 141). File: 4488 src lines vs 1287 test lines (~0.29 ratio); 281
    commits in 6 months — the single most-edited file in `internal/`.
  - **Impact**: This is the churn-vs-coverage hotspot the audit brief
    explicitly flags as "the most dangerous combination." A long, deeply
    branched run loop that changes weekly without proportional test coverage is
    a standing regression risk in the always-on daemon.
  - **Suggested fix**: Decompose `Run` into per-responsibility tick handlers
    (patrol scheduling, reconciliation, heartbeat) behind small interfaces, and
    raise daemon test coverage of the loop's branch decisions before the next
    feature lands on top of it.

## Major Findings (P1 — track but do not auto-bead)

- **A cluster of 300–700-line `run*` / orchestration functions in
  `internal/cmd` and core managers.** Each is individually long and branchy;
  collectively they are the bulk of the complexity tail (94 functions exceed
  CC 30):
  - `(*Manager).AddRig` — `internal/rig/manager.go:323` (662 lines, CC 101,
    cognit 154). Largest non-command function; core rig-registration logic.
  - `Daemon.Run` covered above; also `(*Daemon).reapWispsInline` —
    `internal/daemon/wisp_reaper.go:190` (299 lines, CC 54).
  - `executeSling` — `internal/cmd/sling_dispatch.go:142` (453 lines, CC 69).
  - `AgentEnv` — `internal/config/env.go:79` (409 lines, CC 46): a single
    map-building function that is long but mostly flat assignment.
  - `runInstall` (`internal/cmd/install.go:96`, 403 lines),
    `findSettingsFiles` (`internal/doctor/claude_settings_check.go:172`, 377
    lines, cognit 146), `gatherStatus` (`internal/cmd/status.go:680`, 377
    lines), `runDown` (`internal/cmd/down.go:96`, 376 lines, cognit 147),
    `runCrewAt` (`internal/cmd/crew_at.go:22`, 374 lines),
    `doltserver.Start` (`internal/doltserver/doltserver.go:1812`, 374 lines,
    CC 62).
  - `(*Engineer).doMerge` — `internal/refinery/engineer.go:584` (297 lines,
    CC 51): core merge logic in the refinery hot path.
  - **Suggested approach**: These do not each need a bead, but the
    `internal/cmd` package as a whole would benefit from a convention that
    `run*` handlers stay thin orchestrators. Consider enabling `funlen` and a
    `gocyclo`/`gocognit` threshold (e.g. CC > 30) in `golangci-lint` in
    *warn* mode to stop the tail from growing.

- **`updateAgentStateOnDone` is a 343-line, CC-65 helper inside the already
  giant `done.go`** (`internal/cmd/done.go:2973`). Splitting `runDone` should
  not stop at the top level; this helper is itself a refactor target and
  carries an 8-parameter signature (see minor findings).

## Minor Findings (P2 — informational)

- **Functions with > 5 parameters (data-clump candidates).** Highest counts:
  - `detectZombieDeadSession` — 11 params, `internal/cmd/` zombie detection.
  - `RecordAgentInstantiateFromDir` (10), `outputPatrolScanJSON` (10),
    `detectZombieLiveSession` (10).
  - `outputPatrolScanHuman` (9 params, `internal/cmd/patrol_scan.go:526`),
    `recoverBlankToolsPolecat` (9), `fileStrandedPushWisp` (9),
    `recordPushFailure` (9), `NewFixNeededMessage` (9).
  - The zombie-detection and patrol-scan families repeatedly thread the same
    `bd, workDir, townRoot, rigName, polecatName, sessionName, t *tmux.Tmux,
    …` tuple — a clear candidate for a `PatrolContext` / `SessionContext`
    struct that collapses 8–11 args to 1–2.

- **Boolean-flag parameters that branch the body internally.**
  - `updateAgentStateOnDone(… stranded bool, awaitingRefineryMerge bool …)`
    (`internal/cmd/done.go:2973`) — two booleans that select distinct state
    transitions; candidate for an enum/typed mode.
  - `(*Engineer).doMerge(… skipGates ...bool)`
    (`internal/refinery/engineer.go:584`) — variadic-bool-as-flag is an
    anti-pattern; an explicit `DoMergeOptions` would read better.
  - `(*Manager).RemoveWithOptions` (`internal/polecat/manager.go:1179`) —
    already options-struct-based; good model the flag-param sites above could
    follow.

- **General observation**: complexity correlates almost perfectly with the
  `cobra` command boundary. Non-command packages (`internal/git`,
  `internal/config`, `internal/beads`) carry healthy test ratios (≥1.0
  test:src) and far fewer outliers. The remediation lever is narrow and
  well-targeted: thin out the command-handler layer.

## Counts

  counts: critical=3 major=2 minor=3

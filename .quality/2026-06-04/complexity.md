# Complexity Hotspot Audit

_Leg: complexity · Module: `github.com/steveyegge/gastown` · Date: 2026-06-04_

## Summary

The gastown Go codebase (838 non-test source files) is **bimodal** on complexity.
A large, healthy middle of the tree is well-factored and well-tested (e.g.
`internal/doltserver`, `internal/beads`, `internal/git`, and `internal/witness`
all ship more test lines than source). But the command layer — `internal/cmd`,
the busiest and most-churned package in the repo — concentrates nearly all of the
dangerous debt. Cyclomatic outliers cluster there: **94 functions exceed cyclo 30
and 247 exceed cyclo 20**, and the two worst are not close calls. `runDone`
(`internal/cmd/done.go:298`) is **1,850 lines, cyclomatic 323, cognitive 806**;
`runSling` (`internal/cmd/sling.go:209`) is **1,092 lines, cyclomatic 203**. These
are not methods delegating to helpers — `runDone` is a single linear procedure
with 227 control-flow statements and zero internal closures.

The defining finding of this dimension is the **collision of churn, complexity,
and coverage** in exactly one place. `internal/cmd/done.go` is the most-churned
file in the tree (95 commits in 90 days), `runDone` is the most complex function
in the tree, and measured statement coverage of `runDone` is **0.0%**. The whole
`internal/cmd` package sits at **27% coverage** while absorbing the highest commit
volume of any package. That is the textbook "next bug comes from here" profile:
the code changes constantly, no one can hold it in their head, and the safety net
under the worst of it is empty. Secondary debt takes the form of god-objects
(`Daemon` carries 242 methods, `Manager` 184, `Beads` 178) and a long tail of
wide signatures (58 functions take >5 parameters; 273 carry boolean-flag params).

## Score

score: 0.45

## Critical Findings (P0 — file as beads, fix urgently)

- **Title**: `runDone` is a 1,850-line, cyclo-323 function with 0% test coverage
  - **Location**: `internal/cmd/done.go:298` (`runDone`)
  - **Impact**: This is the highest-risk function in the codebase on every axis at
    once. It is the longest function (1,850 lines), the most cyclomatically complex
    (323; next is 203), the most cognitively complex (806; next is 345), it lives in
    the most-churned file in the repo (**95 commits / 90 days**), and `go test
    -coverprofile` over `./internal/cmd/` measures its statement coverage at
    **0.0%**. It contains 227 `if`/`for`/`switch`/`case`/`select` statements and
    nests to ~7 levels deep. `gt done` is the polecat completion path — the single
    most-exercised command in production — so a regression here strands work
    silently. Every change to it is made blind.
  - **Suggested fix**: Treat as a characterization-test target first, not a refactor
    target. Pin current behavior with table-driven tests over the major exit paths
    (`--pre-verified`, `--status DEFERRED`, zero-commit rejection, stranded-push,
    await-merge) *before* extracting. Then carve the linear body into named phases
    (validate → submit → record-receipt → update-agent-state → cleanup), each a
    testable function. `updateAgentStateOnDone` (already extracted) shows the seam.

- **Title**: `internal/cmd` — highest-churn package, 27% statement coverage
  - **Location**: `internal/cmd/` (package-wide; `go test -cover` → **26.5–27.0%**)
  - **Impact**: The command package is where users and agents actually drive the
    system, and it is the busiest package by commit volume, yet two of every three
    statements are unexercised by tests. Concrete cold spots beyond `runDone`:
    `scheduleBead` (`sling_schedule.go:123`, cyclo 64, **4.5%** covered) and
    `executeSling` (`sling_dispatch.go:142`, cyclo 69, 453 lines, **44.8%**, and its
    file `sling_dispatch.go` has *no* dedicated `_test.go`). The sling dispatch path
    is high-complexity, high-churn (36–48 commits/90d across `sling*.go`), and
    thinly tested — the second-most-likely source of the next production incident
    after `runDone`.
  - **Suggested fix**: Set a coverage floor for `internal/cmd` in `gates.yaml`/CI and
    ratchet it. Prioritize tests for `scheduleBead` and the `executeSling` dispatch
    path, which combine high cyclomatic complexity with near-absent coverage.

## Major Findings (P1 — track but do not auto-bead)

- **`runSling` — 1,092 lines, cyclo 203** (`internal/cmd/sling.go:209`). Second-worst
  function in the tree; 49.2% covered and 48 commits/90d. Same linear-procedure
  shape as `runDone`. A bug here misroutes dispatch.
- **God-objects with 100+ methods.** `Daemon` (242 methods), `Manager` (184),
  `Beads` (178), `Git` (162), `Tmux` (120). These types are the change-amplifiers
  of the codebase: every feature bolts another method on, and the type's surface is
  too large to reason about as a unit. `Daemon.Run` alone (`internal/daemon/daemon.go:569`)
  is 723 lines, cyclo 98, in the second-most-churned file (84 commits/90d).
- **`AddRig` — 662 lines, cyclo 101** (`internal/rig/manager.go:323`). 63.5% covered.
  Core rig-onboarding logic; long and branchy but has a real safety net, hence P1.
- **`doMerge` — cyclo 51, 54.9% covered** (`internal/refinery/engineer.go:584`), with
  `doMergePR` at 40%. This is the core merge-execution path of the Refinery; half
  its branches are untested, and `internal/refinery` ships many 0%-covered
  list/query helpers (`ListReadyMRs`, `ClaimMR`, `RejectMR`, `landConvoySwarm`, …).
- **`Start` — 374 lines, cyclo 62** (`internal/doltserver/doltserver.go:1812`). Dolt
  lifecycle bring-up; the file is well-tested overall (5,580 test lines) but this
  one function is a deeply-nested state machine worth isolating.
- **`outputPatrolScanHuman` — cyclo 98, cognitive 188** (`internal/cmd/patrol_scan.go:526`).
  Pure presentation logic that has grown a parallel control structure as severe as
  the business logic it renders; a prime extract-and-table-test candidate.
- **`updateAgentStateOnDone` — 343 lines, 8 parameters, cyclo 65** (`internal/cmd/done.go:2932`,
  58.5% covered). Already extracted from `runDone` but now its own hotspot; the
  8-param signature (4 of them a `bool`/string state-clump) is a refactor smell.

## Minor Findings (P2 — informational)

- **58 functions take more than 5 parameters.** Worst offenders:
  `detectZombieDeadSession` (11), `detectZombieLiveSession` /
  `RecordAgentInstantiateFromDir` / `outputPatrolScanJSON` (10 each). The
  witness zombie-detection cluster threads the same ~10 values through several
  functions — a clear parameter-object candidate.
- **273 function signatures carry a boolean-flag parameter** (e.g.
  `stopAllPolecats(..., force bool, dryRun bool)`). Each boolean typically forks the
  body and doubles the paths a reader must consider; the multi-bool signatures
  (`updateAgentStateOnDone`, `stopAllPolecats`) are the ones worth splitting.
- **278 functions exceed 100 lines; 62 exceed 200.** Beyond the named criticals,
  notable long bodies: `runInstall` (403), `runDown` (376), `runCrewAt` (374),
  `fillRuntimeDefaults` (`config/loader.go`, 357), `runMqSubmit` (328).
- **Deepest nesting (~7 levels inside the body)** appears in `runDone`,
  `runRigAdopt` (`rig.go:1125`), and two doctor checks
  (`claude_settings_check.go:172`, `stale_agent_beads_check.go:44`). Deep nesting in
  the doctor checks is lower-stakes (diagnostic, not on the hot path) but still
  hurts readability.

## Counts

  counts: critical=2 major=7 minor=4

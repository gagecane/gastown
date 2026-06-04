# Quality Report — gastown — 2026-06-04

## Executive Summary
- Overall score: 0.70 (delta vs last: — baseline run, no prior summary)
- Findings: 6 critical / 29 major / 27 minor (delta: — / — / —)
- Top theme: Quality debt is concentrated, not diffuse — a handful of "god"
  CLI command functions carry the complexity, superseded subsystems were never
  deleted, and the most safety-critical paths have disabled or blind tests.
- Recommendation: **act now** on the 6 critical findings (all filed as P0 beads).

## Per-Dimension Scores
| Dimension | Score | Critical | Major | Minor | Delta |
|---|---|---|---|---|---|
| architecture-drift | 0.72 | 0 | 3 | 4 | — |
| complexity | 0.46 | 3 | 2 | 3 | — |
| dead-code | 0.58 | 1 | 14 | 4 | — |
| dependency-health | 0.85 | 0 | 1 | 5 | — |
| documentation | 0.74 | 0 | 4 | 3 | — |
| tech-debt-trend | 0.90 | 0 | 2 | 5 | — |
| test-quality | 0.63 | 2 | 3 | 3 | — |
| **Overall** | **0.70** | **6** | **29** | **27** | **—** |

## Critical Findings (P0)

- **runDone is a 1858-line function (CC 323, cognitive 806)** — `internal/cmd/done.go:298` _(found by: complexity)_
  - Impact: The polecat completion path — the most safety-critical control flow
    in the system (push, MR submit, bead transition, sandbox teardown). 352
    control-flow statements, nested to 6 levels, on the #2 most-churned file
    (239 commits/6mo). The next completion-path bug will almost certainly
    originate here.
  - Suggested fix: Extract by phase into named, testable functions —
    `resolveBranch()`, `pushWithRetry()`, `submitToMergeQueue()`,
    `transitionBead()`, `teardownSandbox()` — with table-driven tests per phase.
  - Bead filed: `gs-bn1`

- **runSling is a 1092-line function (CC 203, cognitive 345)** — `internal/cmd/sling.go:209` _(found by: complexity)_
  - Impact: The dispatch entry point and highest-churn file's sibling (233
    commits/6mo). Deep nesting around batch-vs-explicit-rig scheduling buries
    validation where it is hard to test. Much real work is already delegated;
    the bulk is parse/validate/mode-branch that can be peeled off cleanly.
  - Suggested fix: Pull the prologue into `parseSlingInvocation() (SlingPlan,
    error)` so `runSling` reduces to parse → plan → execute.
  - Bead filed: `gs-9z1`

- **Daemon.Run is a 723-line method (CC 98) with the lowest test ratio among hot files** — `internal/daemon/daemon.go:569` _(found by: complexity)_
  - Impact: `daemon.go` is the single most-edited file in `internal/` (281
    commits/6mo) with a ~0.29 test:src ratio — the churn-vs-coverage hotspot the
    audit flags as "the most dangerous combination." A standing regression risk
    in the always-on daemon.
  - Suggested fix: Decompose `Run` into per-responsibility tick handlers (patrol
    scheduling, reconciliation, heartbeat) behind small interfaces; raise branch
    coverage before the next feature lands.
  - Bead filed: `gs-28y`

- **Legacy witness message-handler dispatch path is fully dead but presents as live infrastructure** — `internal/witness/handlers.go` _(found by: dead-code)_
  - Impact: Exported handlers (`HandleMerged`, `HandlePolecatDone`,
    `HandleMergeFailed`, `HandleSwarmStart`, `HandleLifecycleShutdown`, …) are
    named identically to the LIVE ones in
    `internal/protocol/witness_handlers.go`. A maintainer editing witness
    merge/cleanup/shutdown behavior can edit the dead copy and see no effect — a
    correctness trap on a core control-plane path. None have a non-test caller.
  - Suggested fix: Confirm `DefaultWitnessHandler` is the sole live path, then
    delete the dead handlers and orphaned private helpers as one auditable PR.
  - Bead filed: `gs-adm`

- **Critical-path bead-lifecycle tests are disabled with no tracking bead** — `internal/cmd/done_test.go:305` (+ 3 more) _(found by: test-quality)_
  - Impact: Four tests covering the paths the convoy depends on (find hooked
    bead for `gt done`, nuke→respawn reuse, prime hooked-bead state, session cost
    accounting) are unconditionally `t.Skip`-ed for "bd CLI 0.47.2 bug." A
    regression in any path ships green; no open tracking bead exists. Effectively
    permanent and invisible.
  - Suggested fix: File one tracking bead for the bd-0.47.2 write-commit bug,
    reference it in all four skip sites, and either pin/upgrade bd in CI or
    rewrite against an in-process beads store. Add a CI guard that fails on skips
    referencing no live bead.
  - Bead filed: `gs-98w`

- **Tautology gate (gate 4d) is blind to standard-library assertions** — `internal/autotest/tautology/tautology.go:668` _(found by: test-quality)_
  - Impact: The auto-test-pr gate that rejects tautological tests recognizes
    ONLY testify (`assert.`/`require.`). It produces 7,747 false-positive
    "zero-assertion" findings, and — the real risk — a genuinely empty/tautological
    stdlib-style test (the style of 764/789 files, 97% of the codebase) passes
    undetected. False confidence over almost the entire codebase.
  - Suggested fix: Teach the linter to recognize `t.Error`/`t.Errorf`/
    `t.Fatal`/`t.Fatalf` and the `if got != want {}` idiom as assertions. Until
    then, document that gate 4d only protects testify-style PRs.
  - Bead filed: `gs-c00`

## Major Findings (P1)

_Grouped by dimension. Tracked in leg findings under `.quality/`; not auto-beaded._

**architecture-drift** (`.quality/2026-06-04/architecture-drift.md`)
- `internal/cmd` is a 523-file, ~199K-LOC flat package (fan-out 65) with zero
  sub-packages — no compiler-enforced boundary anywhere in the CLI surface.
- `internal/beads` (fan-in 24 × fan-out 9) and `internal/session` (17 × 12) are
  god packages with the highest fan-in×fan-out product in the tree.
- Presentation layer `internal/style` leaks into core data packages (`beads`,
  `doltserver`, `rig`), dragging Lipgloss/TUI into the domain layer.

**complexity** (`.quality/gastown/complexity.md`)
- A cluster of 300–700-line `run*`/orchestration functions (94 functions exceed
  CC 30): `(*Manager).AddRig` (662 lines, CC 101), `executeSling` (453, CC 69),
  `runInstall`, `gatherStatus`, `runDown`, `doltserver.Start`, `(*Engineer).doMerge`.
- `updateAgentStateOnDone` is a 343-line, CC-65 helper inside the giant `done.go`
  (`internal/cmd/done.go:2973`).

**dead-code** (`.quality/gastown/dead-code.md`) — 14 majors, highlights:
- `internal/ui/pager.go` is an entirely dead file; 9 dead `Render*` helpers in
  `internal/ui/styles.go`.
- Dead `autotestpr` rig-state persistence subsystem; dead structured-logging API
  in `internal/cmd/log.go`; duplicate worklog query free-functions in
  `internal/doltserver/wl_charsheet.go` (same shadowing hazard as witness).
- Dead provider codecs, agent-bead-ID constructors, mail router helpers,
  templates provisioning API, reaper mail-scan entry points, upstreamsync
  helpers, wasteland spider detection, synthesis triggers, and numerous orphaned
  store/registry constructors and health/lifecycle accessors.

**dependency-health** (`.quality/2026-06-04/dependency-health.md`)
- No vulnerability-scanning gate (`govulncheck` absent from gates.yaml, CI,
  scripts, Makefile) — "no CVEs" is unverified, not confirmed.

**documentation** (`.quality/gastown/documentation.md`)
- README "Example Formula" references a non-existent `release.formula.toml` and
  `scripts/publish.sh`; the worked example fails on copy-paste.
- `RELEASING.md:17` Option A `cd gastown/mayor/rig` target doesn't exist;
  contradicts the correct Option B.
- Broken intra-repo cross-links in `docs/design/convoy/` (spec.md,
  mountain-eater.md).
- Core infra packages `acp` and `mayor` lack package-level doc comments.

**tech-debt-trend** (`.quality/gastown/tech-debt-trend.md`)
- Doctor integration test passes vacuously — actor validation gated behind an
  always-empty `wantActor` skip (`internal/doctor/integration_test.go:188`).
- Doctor fix-path test skips its assertion when `runtime-gitignore` detection
  fails (`integration_test.go:395`).

**test-quality** (`.quality/gastown/test-quality.md`)
- Skip-on-failure masks a known nil-deref bug in `cleanupSpawnedPolecat`
  (`internal/cmd/sling_rollback_cleanup_test.go:190`).
- Doctor fix-verification test skips when its target check fails
  (`internal/doctor/integration_test.go:388`).
- `t.Logf`-instead-of-fail soft assertion in `internal/tmux/tmux_test.go:158`.

## Minor Findings (P2)

_Brief, grouped by dimension. See leg files for full detail._

- **architecture-drift:** `internal/daemon` (62 files, fan-out 38) on the same
  trajectory as `cmd`; flat `internal/` layout offers little layering signal;
  27 interfaces to watch; baseline run (no growth data).
- **complexity:** functions with >5 params (`detectZombieDeadSession` 11 params,
  patrol/zombie families) — `PatrolContext`/`SessionContext` struct candidates;
  boolean-flag params that branch the body; complexity correlates with the cobra
  command boundary.
- **dead-code:** ~20 single dead methods/helpers (one-line cleanups); dead
  `tui/feed` combined source; dead config resolvers; test-only mocks (leave);
  `Example*` and build-tagged stubs are false positives — keep.
- **dependency-health:** duplicate `cenkalti/backoff` v4+v5 compiled (upstream-blocked);
  stale transitive majors in go.sum (not compiled); OTel log signals on
  pre-stable v0.x; mild ≤1-minor drift on direct deps (`glamour` v0.10→v1.0 worth
  review); npm `promptfoo` dev-pin fine.
- **documentation:** absolute `~/gt/docs/...` refs that don't resolve in-checkout
  (may be town-root); `shell`/`wrappers` use `// ABOUTME:` not `// Package`;
  scattered exported-doc gaps (`cmd/health.go` structs, `beads/exec.go`); comment
  hygiene is a strength (no aged TODO/FIXME).
- **tech-debt-trend:** dead deprecated rig-bead helpers (safe to delete);
  4 platform/CI-conditional debt-smell skips; 2 disabled plugin manifests
  (`code-scout`, `task-discovery`); 180-day aging not yet reachable (repo ~172d
  old).
- **test-quality:** 149 fixed-duration `time.Sleep` synchronizations (latent
  flake; worst are 6s in convoy_manager_integration_test.go); weak-shape testify
  findings mostly fine; 725/747 `t.Skip` correctly conditional.

## Trend Notes

This is the **baseline run** — no prior `.quality/**/summary.json` exists, so no
better/worse comparison can be computed. All seven legs independently confirmed
the baseline condition. Recorded for next run's deltas: overall 0.70; totals
6 critical / 29 major / 27 minor; key absolute sizes `internal/cmd`=523 files,
`internal/daemon`=132, `internal/beads`=52 (incl. tests), `internal/session`=23.

New themes appearing (to track):
- **"God command" concentration** in `internal/cmd` (complexity + architecture
  legs both surface this independently — the strongest cross-leg signal).
- **Shadowed dead code** (witness handlers, doltserver worklog queries) creating
  edit-the-wrong-copy correctness traps.
- **Disabled/blind tests** on safety-critical paths (test-quality + tech-debt
  both flag the doctor integration tests).

## Recommendations

1. **Land the 6 P0 beads** (`gs-bn1`, `gs-9z1`, `gs-28y`, `gs-adm`, `gs-98w`,
   `gs-c00`) — they are the highest-leverage, mostly-mechanical risk reductions.
2. **Adopt a "thin `run*` handler" convention** for `internal/cmd` and enable
   `gocyclo`/`gocognit`/`funlen` thresholds in `golangci-lint` in *warn* mode to
   stop the complexity tail from growing (addresses both complexity and
   architecture-drift majors).
3. **Add a `govulncheck ./...` CI gate** to `gates.yaml`, mirroring the existing
   vet/lint pattern, and baseline it once now.
4. **Fix the tautology gate's stdlib blindness** before relying on gate 4d — it
   currently protects only 3% of the test suite's style.
5. **Sweep the shadowed dead code** (witness handler cluster, doltserver worklog
   free-functions) as focused behavior-preserving PRs to remove the
   edit-the-wrong-copy trap.

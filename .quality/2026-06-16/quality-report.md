# Quality Report — gastown — 2026-06-16

## Executive Summary
- Overall score: 0.73 (delta vs last: +0.03)
- Findings: 7 critical / 26 major / 29 minor (delta: +1 / -3 / +2)
- Top theme: Complexity and test-meaningfulness remain the weak axis — the
  highest-churn orchestration functions (`runDone`, daemon loop, dispatch,
  reaper) are large, deeply branched, and carry ~0% direct coverage, while the
  tests that nominally guard the work-loss-critical paths re-implement or
  text-scrape the logic instead of executing it.
- Recommendation: track — overall health is improving (dead-code and
  test-quality both up), no new systemic regression; the 7 criticals are
  long-standing structural debt now filed as beads, not fresh breakage.

## Per-Dimension Scores
| Dimension | Score | Critical | Major | Minor | Delta |
|---|---|---|---|---|---|
| architecture-drift | 0.71 | 0 | 3 | 5 | -0.01 |
| complexity | 0.42 | 5 | 6 | 3 | -0.04 |
| dead-code | 0.82 | 0 | 2 | 4 | +0.24 |
| dependency-health | 0.82 | 0 | 3 | 4 | -0.03 |
| documentation | 0.75 | 0 | 5 | 3 | +0.01 |
| tech-debt-trend | 0.88 | 0 | 2 | 6 | -0.02 |
| test-quality | 0.71 | 2 | 5 | 4 | +0.08 |

## Critical Findings (P0)

- **runDone — most-churned untested function in the codebase** — `internal/cmd/done.go:499` _(found by: complexity)_
  - Impact: 602 lines, cyclo 74, churned 123× in 90d, **0.0% coverage**. `gt done`
    is the completion path every agent runs; recovery/edge-case bugs keep landing
    here and each fix re-touches a 600-line function blind.
  - Suggested fix: Extract phases (preflight → push/verify → MR submit → lifecycle
    update) into testable functions; table-test the push-failure and stranded-MR
    recovery branches.
  - Bead filed: `gu-8u7e3`

- **dispatchScheduledWork + capacity dispatch — 18 zero-coverage funcs** — `internal/cmd/capacity_dispatch.go:355` _(found by: complexity)_
  - Impact: 330 lines, cyclo 45, churn 63, main entry **0.0%** (18 of 53 funcs at
    0%). Heart of work dispatch/capacity gating — the exact code that guards
    against the spawn-storm failure class CLAUDE.md warns about.
  - Suggested fix: Extract capacity/eligibility decision logic from side effects;
    unit-test exhausted-capacity and no-ready-work branches.
  - Bead filed: `gu-t9kdv`

- **scheduleBead — 473-line scheduler, cyclo 73, 4% covered** — `internal/cmd/sling_schedule.go:123` _(found by: complexity)_
  - Impact: 73 independent paths, only 4% exercised — the untested 96% is where
    the next scheduling edge-case bug lives.
  - Suggested fix: Decompose into resolve-target / build-update / persist stages;
    cover the branch matrix with table tests.
  - Bead filed: `gu-1sgeb`

- **(*Daemon).Run — 486 lines, cyclo 83, nest depth 6, 0% coverage** — `internal/daemon/daemon.go:647` _(found by: complexity)_
  - Impact: The daemon supervisor loop, second-most-churned file (110×). 6-level
    nesting × 83 branches; recent daemon-recovery commits all land here. A logic
    error stalls the entire town.
  - Suggested fix: Extract per-iteration phases (startup, patrol tick, recovery
    scan, shutdown) into individually testable methods.
  - Bead filed: `gu-y07h7`

- **reapWispsInline — wisp reaper, cyclo 48, 0% coverage** — `internal/daemon/wisp_reaper.go:191` _(found by: complexity)_
  - Impact: 305 lines, 48 branches, none tested. A bug either leaks stale beads
    (spawn storms via re-dispatch) or reaps live work.
  - Suggested fix: Extract the should-reap predicate (pure function of bead state)
    and unit-test it exhaustively; keep the I/O wrapper thin.
  - Bead filed: `gu-t99i9`

- **done.go work-loss safety nets tested by re-implementing the logic, not calling it** — `internal/cmd/done_test.go:1839-1871,777-840,931-1026,1030-1131,1968-2003` _(found by: test-quality)_
  - Impact: Tests named for the most work-loss-prone paths (auto-commit on done,
    verify-MR-before-nuke, branch detection) "Simulate the logic from runDone…"
    against git primitives and local booleans, never invoking the real
    orchestration. A real done.go regression ships green.
  - Suggested fix: Extract runDone decisions into pure functions and test those,
    or drive runDone end-to-end with the existing bd/git stub harness
    (`done_awaiting_merge_test.go` is the model).
  - Bead filed: `gu-eup4p`

- **reaper agent-bead/SQL safety predicates tested by scraping source text, not executing queries** — `internal/reaper/reaper_test.go:389,445,148-206,356-389,411-428` _(found by: test-quality)_
  - Impact: 9 tests `os.ReadFile` `reaper.go` + `strings.Contains` for SQL
    fragments. The guarded exclusion (never reap `hq-mayor`/`witness`/`refinery`/
    `deacon` agent beads) is safety-critical; a substring check passes on
    semantically-wrong SQL and fails on harmless reformatting.
    `TestReapExcludesAgentBeads` is `t.Log`-only and can never fail.
  - Suggested fix: Execute against the fake SQL driver already used by
    `TestClosedMoleculeStepReapBehavior` (`:919`); delete the no-op test and add
    an execution-level `issue_type='agent'` → left-open assertion.
  - Bead filed: `gu-pxwh1`

## Major Findings (P1)

### complexity
- `(*Manager).AddRig` — 675 lines (longest function in tree), cyclo 103
  (`internal/rig/manager.go:323`); decompose by the layout sections in its own doc-comment.
- `executeSlingDispatch` / `executeSling` — dispatch core, 11 params, 485/625 lines, cyclo 94/87, half-covered (`internal/cmd/sling.go:1086`, `sling_dispatch.go:161`); introduce a `dispatchRequest` struct.
- `outputPatrolScanHuman` — cyclo 128 (highest in codebase), 0% covered, 11 params (`internal/cmd/patrol_scan.go:799`); bundle `*Result` params and split per-section printers.
- `IsPatrolEnabled` — cyclo 111 from a 237-line if-chain (`internal/daemon/types.go:268`, 94.6% covered); replace with a name→accessor map.
- God-structs: `*Daemon` (277 methods), `*Manager` (190), `*Beads` (183).
- 321 functions exceed 100 lines (919 in the 50–100 band), clustered in `internal/cmd`.

### test-quality
- Wall-clock `time.Sleep` as synchronization (flaky risk), not short-mode gated: `convoy_manager_test.go:2309,2314`, `convoy_manager_integration_test.go:81,102`, `molecule_await_event_test.go:324,555`.
- No-op / result-discarding smoke test on a safety-critical gate: `sling_test.go:2596` (`TestIsHookedAgentDead_NoTmuxSession`, `_ = result`).
- Validation tests assert only `err != nil`, not field/why: `engineer_test.go:519-522,561-564`.
- `TestIsRigOperational_DockedRig` never sets up a docked rig (`daemon_test.go:837-877`).
- `TestEventPoll_LazyStoreOpening` re-implements `runEventPoll`'s lazy-open loop (`convoy_manager_test.go:749-803`).

### architecture-drift
- `internal/cmd` is now a 590-file flat package, still growing (+12.8%); carve cohesive command groups into sub-packages (convoy first at 4.5K LOC).
- Presentation layer `internal/style` still leaks into core data packages `beads`/`doltserver`/`rig` (carried over, unfixed).
- `internal/beads` (fan-in 24 / fan-out 9) and `internal/session` (16 / 12) remain god packages; treat as frozen-interface packages.

### dependency-health
- M1: `govulncheck` / `go list -m -u all` could not run (network sinkholed) — CVE status unverified; run in CI and gate.
- M2: 38 commit-pinned pseudo-versions in the `dolthub/*` ecosystem (break-first-if-upstream-rotates); group in Renovate + mirror the module cache.
- M3: `go` directive skew — main `go 1.26.2` vs `plugins/dolt-snapshots/go.mod` `go 1.23`; align or document.

### documentation
- README "Example Formula" references nonexistent `release.formula.toml` / `scripts/publish.sh` (carried over).
- README Quick Start uses a `--human` flag `gt convoy create` does not define (`README.md:232,351`) — both copy-paste examples fail (NEW).
- `RELEASING.md:17` Option A `cd` target is wrong (`mayor/rig/` has no `scripts/`) (carried over).
- Broken intra-repo doc cross-links (3 missing targets, 7 references).
- `mayor` package still lacks a package doc (~17 undocumented exported ACP-lifecycle symbols).

### tech-debt-trend
- Doctor env-var integration test asserts nothing — now passes vacuously instead of skipping (regression) (`internal/doctor/integration_test.go:185-191`).
- Doctor fix-path test skips its own assertion when the detector fails (`integration_test.go:395`).

### dead-code
- Dead deprecated rig-bead helper cluster in `internal/beads/beads_rig.go` (`GetRigBead`/`GetRigByID`/`UpdateRigBead`/`DeleteRigBead`/`ListRigBeads`, ~75 lines, 0 callers).
- No-op stub `CreatePolecatCLAUDEmd` + orphan 345-line embedded template (`internal/templates/templates.go:226-243`, ~360 lines).

## Minor Findings (P2)

- **architecture-drift**: `internal/daemon` grew +22.7% (162 files, fan-out 38); `rig`/`runtime` surfaced as coupling hubs; flat `internal/` layout gives little layer signal; 84 interface decls (up from 27); package count fell 95→89 while files rose 1,642→1,784.
- **complexity**: boolean-parameter functions select code paths (prefer options struct/enum); long-function scale confirms the fat-command-handler theme.
- **dead-code**: 8 non-test `unparam` signals; 42 test-file `unparam` hits; no dead branches; no orphan/build-excluded files; commented blocks are doc prose; other `Deprecated` exported funcs still live.
- **dependency-health**: co-existing major versions (`backoff/v4`+`/v5`, `golang-lru`+`/v2`); three YAML libs in the tree; test-only deps in primary require block (no prod leak); `go-rod/rod` direct but `browser`-tag only.
- **documentation**: absolute `~/gt/docs/...` references that don't resolve in this checkout (likely town-root, uncertain); comment hygiene remains a strength (single benign marker).
- **tech-debt-trend**: dead deprecated rig-helpers (bead `gu-r83v` closed without deleting them); two new deprecated shims with live callers but no removal date (`HasRemote`, `GetMR`); `CreatePolecatCLAUDEmd` no-op (bead `gu-k9oj` closed); debt-smell skips flat (~3-4) though total `t.Skip` grew 591→817; new disabled plugin artifacts checked in; 180-day aging threshold now reachable but no marker hits it.

## Trend Notes
- **Better since last run:** dead-code jumped +0.24 (0.58→0.82) — the prior
  critical was resolved and the major count fell 14→2; test-quality rose +0.08
  (0.63→0.71) as both 2026-06-04 criticals (disabled `bd 0.47.2` lifecycle tests,
  tautology gate blind to stdlib assertions) were fixed and guarded. Overall
  score +0.03 (0.70→0.73) and total criticals roughly flat (6→7).
- **Worse since last run:** complexity slipped -0.04 (0.46→0.42) and gained a
  fifth critical as the load-bearing orchestration functions kept churning
  without coverage; tech-debt-trend -0.02 as the two doctor test findings went
  unaddressed and one regressed from "skips visibly" to "passes silently";
  architecture-drift -0.01 as `cmd` (+12.8%) and `daemon` (+22.7%) grew and the
  `style` leak remains open.
- **New themes:** the criticals have shifted from "broken/disabled tests" (now
  fixed) to "untested complexity on the hottest paths" — the next class of risk
  is structural, not cosmetic.

## Recommendations
1. Attack the complexity criticals coverage-first, not refactor-first: extract
   the pure decision predicates from `runDone`, `dispatchScheduledWork`,
   `(*Daemon).Run`, and `reapWispsInline` and table-test them — this buys the
   most safety per line on the highest-churn paths.
2. Fix the two test-quality criticals together with #1 — they cover the same
   `done`/reaper paths, so the extracted predicates give both real tests.
3. Add `govulncheck ./...` to CI on a network-enabled host to close the
   dependency-health coverage gap (M1) and group the `dolthub/**` pins in Renovate.
4. Knock out the cheap documentation majors (remove `--human` from README,
   fix `RELEASING.md` Option A, repoint the broken `testing.md` cross-links) —
   low effort, directly protects new-contributor onboarding.
5. Delete the now-dead deprecated rig-bead helper cluster and the
   `CreatePolecatCLAUDEmd` stub (both tracking beads already closed) to stop
   carrying ~435 lines of dead surface area.

## Sources
- Leg findings under `.quality/2026-06-16/` (architecture-drift.md, dead-code.md, dependency-health.md, tech-debt-trend.md, test-quality.md) — accessed 2026-06-16
- Complexity Hotspot Audit — bead `gu-leg-lclhw` notes — accessed 2026-06-16
- Documentation Coverage Audit — bead `gu-leg-nofum` notes — accessed 2026-06-16
- Prior run summary — `.quality/2026-06-04/summary.json` (overall 0.70) — accessed 2026-06-16

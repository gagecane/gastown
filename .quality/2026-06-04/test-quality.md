# Test Quality Audit

_Leg: test-quality · Module: `github.com/steveyegge/gastown` · Date: 2026-06-04_

## Summary

The gastown test suite is large and broadly meaningful: ~804 test files and
~8,000 `Test*` functions covering the high-churn core (`done`, `daemon`,
`witness`, `beads`, `sling`, `convoy`, `doltserver` all carry thousands of lines
of tests, frequently spread across many topic-specific `*_test.go` files rather
than a single sibling file). Assertions are mostly real value/state checks, not
tautologies — the codebase even ships its **own** test-quality tooling
(`internal/autotest/tautology`, `coverage`, `mutant`), and a sweep for
`assert.True(t, true)`-style theatre finds hits **only inside that tool's own
testdata fixtures**, not in production tests. This is a healthy sign.

The real weaknesses are concentrated and structural, not pervasive: (1) the
project's own tautology / mutant / coverage detectors are **not wired into any
blocking gate** (`gates.yaml`, `.golangci.yml`, `.github/`, `Makefile`, `scripts/`
all return nothing for `tautology`/`autotest`) — they run only via the on-demand
`gt auto-test-pr` agent flow, so nothing stops a new theatre test from landing;
(2) a cluster of **time-based flakiness** — 148 `time.Sleep` calls across ~60
test files, including fixed multi-second sleeps tied to hard-coded production
poll intervals, despite a clean injectable `Clock` interface already existing in
`internal/ciwatcher`; and (3) a handful of **fully or conditionally disabled
critical-path tests** (the `bd CLI 0.47.2` cluster and a runtime self-skip in the
polecat idle path), some with weak or missing tracking beads.

## Score

score: 0.78

## Critical Findings (P0 — file as beads, fix urgently)

- **Critical-path tests disabled by `t.Skip("bd CLI 0.47.2 bug")` with weak/missing tracking**
  - **Location**:
    - `internal/beads/beads_test.go:3560` — `TestResetAgentBeadForReuse_NukeRespawnCycle`
      (the polecat nuke/respawn reuse cycle — core to the autonomous lifecycle)
    - `internal/cmd/done_test.go:305` — `TestFindHookedBeadForAgent`
    - `internal/cmd/prime_test.go:381` — `autonomous_state_hooked_bead`
    - `internal/cmd/costs_workdir_test.go:30`
  - **Impact**: These verify DB-write paths on the autonomous `done`/`prime`/bead-
    reuse critical path — exactly the flows that, when broken, cause spawn storms
    and stranded work. All four are *fully* skipped, so a regression in these
    paths ships green. Tracking is inconsistent: `costs_workdir_test.go` cites
    `gt-lnn1xn`, but `done_test.go` and `prime_test.go` only say "See internal
    issue for tracking" (no ID), and `beads_test.go` cites none. A skip with no
    durable bead is invisible debt — the witness/reaper won't resurrect a
    disabled test.
  - **Suggested fix**: File one tracking bead covering all four, link it in each
    skip message, and either (a) pin/upgrade the `bd` CLI past the 0.47.2
    commit-visibility bug and re-enable, or (b) replace the real-`bd` dependency
    in these tests with the in-process beads store used elsewhere so they no
    longer depend on the buggy CLI build.

## Major Findings (P1 — track but do not auto-bead)

- **Test-quality tooling exists but is not a blocking gate**
  - **Location**: `internal/autotest/tautology/tautology.go` (functional —
    `go test ./internal/autotest/tautology/` passes), `internal/autotest/coverage.go`,
    `internal/autotest/mutant.go`. Wiring search: `gates.yaml`, `.golangci.yml`,
    `.github/`, `Makefile`, `scripts/` contain **no** reference to
    `tautology`/`autotest`. Only invoked via the `gt auto-test-pr` agent commands
    (`internal/cmd/auto_test_pr*.go`).
  - **Impact**: The org built a tautology/zero-assertion detector and a mutation
    tester, then left them off the default pipeline. New theatre tests (the exact
    thing this audit hunts) can land without challenge; mutation score is never
    asserted. The capability is paid for but not banked.
  - **Suggested fix**: Add a `ci-only` (or `required-if-installed`) gate to
    `gates.yaml` running the tautology detector over changed test files, mirroring
    the existing `go vet` / `golangci-lint` gate. Baseline once, then fail on new
    findings.

- **Fixed multi-second sleeps tied to hard-coded production poll intervals (flaky + slow)**
  - **Location**: `internal/daemon/convoy_manager_integration_test.go:81,102`
    — two `time.Sleep(6 * time.Second)` calls, each commented "Wait for the next
    event poll tick (~5s)". The interval is a package `const eventPollInterval =
    5 * time.Second` (`internal/daemon/convoy_manager.go:22`), not injectable.
  - **Impact**: ~12s of unavoidable wall-clock in one test, and structurally
    flaky: a slow CI runner can miss the tick, and any change to
    `eventPollInterval` silently breaks the test's timing assumption. A clean
    injectable clock/interval pattern already exists in the same repo
    (`internal/ciwatcher/types.go:127` `type Clock interface` + `SystemClock`),
    so the fix is idiomatic here.
  - **Suggested fix**: Make `eventPollInterval` (and the ticker) injectable for
    tests so the manager can poll on a sub-millisecond interval, or expose a
    "poll now" hook; replace the fixed sleeps with a poll-until-condition loop
    bounded by a timeout.

- **Runtime self-skip masks the assertion it is supposed to make**
  - **Location**: `internal/polecat/session_manager_test.go:585-591` — the test
    logs "Warning: idle state not detected (tmux timing)" and `t.Skip("idle
    detection unreliable in test environment")` *mid-test*, then would have
    verified `verifyStartupNudgeDelivery` retries on idle.
  - **Impact**: Idle detection drives witness restart of idle polecats — a core
    lifecycle signal. Because the skip fires conditionally at runtime whenever
    tmux timing is uncooperative, the test reports PASS/SKIP without ever
    exercising its central path; a regression in idle-retry would not be caught.
    This is flaky-masking dressed as a pass. No tracking bead.
  - **Suggested fix**: Make idle state deterministic via a tmux test double /
    injected pane-state probe so `IsIdle` is reliable, then assert the retry
    unconditionally. If genuinely un-testable in-process, move to an integration
    tier gated by an env var rather than a silent runtime skip, and file a bead.

## Minor Findings (P2 — informational)

- **Sleep-for-timestamp-ordering**
  - **Location**: `internal/nudge/queue_test.go:32` (and similar) —
    `time.Sleep(time.Millisecond) // ensure different timestamps`.
  - **Detail**: Relies on wall-clock advancing enough between two `Enqueue`
    calls to produce distinct, orderable timestamps. Fine in practice, but
    fragile to coarse clock resolution and pure timing luck. Prefer asserting
    order via an injected/monotonic sequence rather than sleeping to separate
    timestamps.

- **Broad reliance on fixed sleeps for async coordination (no shared test clock)**
  - **Location**: hotspots — `internal/tmux/tmux_test.go` (14),
    `internal/acp/forward_from_agent_test.go` (12), `internal/nudge/queue_test.go`
    (10), `internal/agentlog/tail_ctx_test.go` (10),
    `internal/daemon/convoy_manager_test.go` (8), plus `feed/curator`,
    `polecat/session_manager`, `polecat/heartbeat`, `witness/polecat_startup_backoff`.
  - **Detail**: 148 `time.Sleep` calls across ~60 files. Many are short (≤50ms)
    and low-risk, but the pattern is "sleep then assert" rather than
    "poll-until-condition", which trades determinism for wall-clock and adds tail
    flakiness under CI load. The `ciwatcher.Clock` interface is the in-repo model
    worth generalizing to these packages.

- **High-churn modules with thin *direct* test files (verify, don't assume, coverage)**
  - **Location**: `internal/daemon/daemon.go` (4,488 LOC, ~88 commits/3mo,
    `daemon_test.go` ≈1,287 LOC) and `internal/cmd/capacity_dispatch.go`
    (1,566 LOC, ~44 commits, 44 funcs, `capacity_dispatch_test.go` ≈366 LOC).
  - **Detail**: Both are hot, frequently-edited files. Their *sibling* test files
    are small, though logic is partly covered elsewhere (the `daemon` package has
    ~70 test files overall; capacity also has `capacity_exhaustion_test.go` and
    `sling_dispatch_lock_test.go`). Not a confirmed gap — but these two are the
    best candidates for a focused per-function coverage check to confirm the
    negative/error paths of the highest-churn dispatch logic are actually
    exercised.

## Questions Answered

- **If a real regression shipped today, which tests would catch it?** For the
  core flows (`done`, `sling`, `convoy`, `beads`, `witness handlers`,
  `doltserver`) the suite is dense and assertion-rich enough to catch most
  behavioral regressions. The blind spots are the **disabled** paths (P0
  bd-0.47.2 cluster, polecat idle-retry) and **timing-dependent** behavior, where
  a regression could pass intermittently because the test depends on sleeps
  rather than synchronization.
- **Which tests are theatre — green, but verify nothing?** Very few outright
  tautologies (none in production code — only in the tautology tool's own
  testdata). The closest real instances are the *conditional* theatre cases: the
  polecat idle test that runtime-skips its own assertion, and any "sleep then
  assert" test whose timing window is generous enough that the assertion almost
  always holds regardless of the code under test.

## Counts

counts: critical=1 major=3 minor=3

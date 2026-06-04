# Test Quality Audit

## Summary

The gastown test suite is large and, in the common case, healthy: 808 test
files back 1,648 Go source files, and the dominant style is table-driven tests
that assert concrete return values via standard-library `t.Errorf`/`t.Fatalf`
(764 of ~789 test files). Spot-checks of high-churn packages (`doltserver`,
`bitbucket`, `proxy`) show tests that genuinely pin behavior callers depend on,
not assertion theatre. The headline problem is **not** that most tests verify
nothing — it is concentrated, high-impact debt in two places: (1) a cluster of
**critical-path bead-lifecycle tests permanently disabled** behind a third-party
CLI bug with weak or missing tracking, and (2) a **blind spot in the project's
own tautology gate (auto-test-pr gate 4d)**, which only understands testify-style
assertions and is therefore unable to catch a tautological test written in the
standard-library style that 97% of this codebase uses.

The top theme: the suite's *meaningfulness* is fine where it runs, but the most
important behaviors (done/reset/respawn, prime, cost accounting) currently have
**no executing test at all**, and the automated guard that is supposed to keep
new tests honest cannot see the style in which almost all tests are written.

## Score

score: 0.63

## Critical Findings (P0 — file as beads, fix urgently)

### 1. Critical-path bead-lifecycle tests are disabled with no tracking bead
- **Location**:
  - `internal/cmd/done_test.go:305` (`TestFindHookedBeadForAgent`)
  - `internal/cmd/costs_workdir_test.go:30` (`TestQuerySessionEvents_FindsEventsFromAllLocations`)
  - `internal/cmd/prime_test.go:381` (`TestPrime/.../autonomous_state_hooked_bead`)
  - `internal/beads/beads_test.go:3560` (`TestResetAgentBeadForReuse_NukeRespawnCycle`)
- **Impact**: These cover exactly the paths the convoy depends on — finding the
  hooked bead for an agent (`gt done`), nuke→respawn reuse, prime's hooked-bead
  state, and session cost accounting. All four are unconditionally `t.Skip`-ed
  with the comment "bd CLI 0.47.2 bug: database writes don't commit." A real
  regression in any of these paths would ship green. `bd list` shows **no open
  tracking bead**; the skip comments variously say "See internal issue for
  tracking" (vague) or reference `gt-lnn1xn` (not in the active tracker). The
  skips are effectively permanent and invisible.
- **Suggested fix**: File one tracking bead for the bd-0.47.2 write-commit bug,
  reference its ID in all four skip sites, and either (a) pin/upgrade the bd CLI
  used in CI so the tests run, or (b) rewrite the affected tests against an
  in-process beads store that does not depend on the buggy auto-flush path. Add
  a CI guard that fails if a `t.Skip("bd CLI 0.47.2 ...")` references no live bead.

### 2. Tautology gate (gate 4d) is blind to standard-library assertions
- **Location**: `internal/autotest/tautology/tautology.go:668-675`
  (`isAssertionCall` → `isTestifyStyle`: only `assert.`/`require.` prefixes count
  as assertions).
- **Impact**: The auto-test-pr quality gate that is supposed to reject
  tautological tests recognizes *only* testify. Running `AnalyzeFile` across the
  whole repo yields **7,747 "zero-assertion" findings**, but verification shows
  these are almost entirely false positives: e.g. `doltserver_test.go`'s
  `TestFormatBytes` is a correct table-driven test using `t.Errorf` yet is
  flagged "zero assertions." Conversely — and this is the real risk — a genuinely
  empty or tautological test written in the standard-library style (the style of
  764/789 files) would pass gate 4d **undetected**. The gate gives false
  confidence over 97% of the codebase.
- **Suggested fix**: Teach the linter to recognize standard-library failure
  calls (`t.Error`, `t.Errorf`, `t.Fatal`, `t.Fatalf`) and the surrounding
  `if got != want { ... }` comparison idiom as assertions, so its taint and
  zero-assertion rules apply to stdlib tests. Until then, document that gate 4d
  only protects testify-style PRs.

## Major Findings (P1 — track but do not auto-bead)

### 3. Skip-on-failure masks a known nil-deref bug instead of catching it
- **Location**: `internal/cmd/sling_rollback_cleanup_test.go:190-204`
  (`TestCleanupSpawnedPolecat_NilSpawnInfo`).
- **Impact**: The test calls `cleanupSpawnedPolecat(nil, ...)` inside a
  `recover()` that, on panic, does `t.Logf("ISSUE: ... panics ...")` then
  `t.Skip("Known issue: cleanupSpawnedPolecat panics with nil spawnInfo")`. The
  test can never fail: if the function panics (the actual bug) it skips; if it
  doesn't, it passes. This documents a real defect as green CI. Pure theatre.
- **Suggested fix**: Decide the contract. If nil-safe is required, assert it does
  not panic and fix `cleanupSpawnedPolecat`. If panic is acceptable, assert the
  panic with `require.Panics`. Either way, remove the `t.Skip`.

### 4. Doctor fix-verification test skips when its target check fails
- **Location**: `internal/doctor/integration_test.go:388-396`.
- **Impact**: The test sets up a broken `runtime-gitignore` state, then if the
  check does **not** flag it, does `t.Skip("runtime-gitignore check not
  detecting broken state")` rather than failing. The very condition the test
  exists to catch (the detector missing a broken state) silently disables the
  test instead of failing it. The downstream `d.Fix(ctx)` assertions never run.
- **Suggested fix**: Replace the skip with a failing assertion that the check
  detects the broken state; that is the regression the test should guard.

### 5. `t.Logf`-instead-of-fail soft assertions
- **Location**: `internal/tmux/tmux_test.go:158-161`
  (`TestSendKeys`/CapturePane: "Don't fail, just note - timing issues possible").
- **Impact**: The test executes a command and checks the captured pane for a
  marker, but on mismatch only logs and passes. It can only ever pass, so it
  verifies nothing about `SendKeys`/`CapturePane` behavior. The companion
  comment "In real tests you'd wait for output, but for basic test we just
  capture" confirms it was written as a smoke check, not a behavior test.
- **Suggested fix**: Poll for the marker with a bounded deadline (e.g. retry up
  to ~1s) and `t.Errorf` if it never appears, removing the timing race instead
  of papering over it.

## Minor Findings (P2 — informational)

- **Fixed-duration `time.Sleep` as synchronization (149 occurrences).** The bulk
  are short (36× `100ms`, 17× `200ms`) in integration tests and are plausible
  process-startup waits, but they are latent flake sources under CI load. The
  6s sleeps in `internal/daemon/convoy_manager_integration_test.go:81,102` and
  2s sleeps in `internal/cmd/scheduler_integration_test.go:1004,1494` are the
  most fragile; prefer polling on the awaited condition. (The 30s sleeps in
  `doltserver_test.go:518` and `autotest/sandbox/integration_test.go:205` are
  deliberate slow-child/helper processes, not synchronization — not flaky.)
- **`assert.True(t, ok)` weak-shape findings are mostly fine.** The 3 `notnil`
  and 27 `no-input-derived` findings the linter reports on testify files were
  spot-checked (`proxy/exec_test.go:495`, `bitbucket/client_test.go:122`,
  `proxy/git_test.go:737`) and are legitimate: the asserted booleans are derived
  from the function under test, or the meaningful assertions live in `t.Run`
  subtests the linter does not descend into. No action needed beyond noting the
  linter's conservative taint analysis (relevant to finding #2).
- **Most `t.Skip` (725 of 747) are correctly conditional** on environment
  (Windows, missing `tmux`/`gt`/`bd`, absent formulas) — appropriate and not a
  concern. The audit-worthy skips are the 6 unconditional ones in findings #1,
  #3, and #4.

## Counts

  counts: critical=2 major=3 minor=3

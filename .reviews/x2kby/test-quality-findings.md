# Test Quality Review

## Summary

The test suite across the recently modified autotestpr, daemon, and cmd packages demonstrates strong test quality overall. Tests are behavior-focused, use table-driven patterns consistently, verify both happy and error paths, and employ deterministic fixtures (pinned clocks, tempdir isolation) over brittle mocks. The assertion patterns are specific — checking exact values, state transitions, and field-level correctness rather than just nil/non-nil. The primary weakness is in `cycle_test.go`, where the missing-town-bead test only exercises config validation (nil TownBeads) rather than the actual missing-bead code path it claims to test.

## Critical Issues

- **cycle_test.go:16-46** — `TestRunCycle_MissingTownBead` is misleadingly named. It claims to verify the missing-town-bead path (Round 2 fix #10: structured warning instead of panic), but actually only tests that `CycleConfig.validate()` rejects a nil `TownBeads`. The real missing-bead path (`ErrTownStateNotProvisioned` from `LoadTownState`) is never exercised because the test fails at validation before reaching `reconcileEnabledRigs` or `LoadTownState`. A bug in the warning codepath would go undetected by this test.
  - **Impact**: The acceptance criterion "Integration test covers missing-town-bead path (exits with structured warning, not panic)" is not actually verified.
  - **Suggested fix**: Provide a `*beads.Beads` test double that returns `ErrTownStateNotProvisioned` from `Show()`, then assert `result.ExitReason == "town-bead-missing"` and `result.Warning != ""`.

## Major Issues

- **cycle_test.go:257-268** — Custom `contains`/`containsSubstr` helper reimplements `strings.Contains`. This is unnecessary complexity; importing `strings` and calling `strings.Contains` is standard Go.
  - **Impact**: Readability and maintenance burden. A future contributor may not realize this is identical to the stdlib.
  - **Suggested fix**: Replace with `strings.Contains(s, substr)`.

- **cycle_test.go:52-80** — `TestRunCycle_NoRigsEnabled_ViaComputeSettings` tests `computeEnabledRigsFromSettings` directly, but its name claims to test `RunCycle`. It never calls `RunCycle` and therefore doesn't verify the full cycle's no-rigs-enabled exit path (ExitReason="no-rigs-enabled", Reconciled=true).
  - **Impact**: The test name creates a false sense of coverage. The actual `RunCycle` integration path for no-rigs-enabled is untested end-to-end.
  - **Suggested fix**: Either rename to `TestComputeEnabledRigsFromSettings_NoRigsEnabled` or add an actual `RunCycle` call that exercises the full path.

## Minor Issues

- **cycle_test.go:188-228** — `TestCycleConfig_Validate` only tests failure cases. A positive case (valid config that passes validation) would confirm the golden-path contract and prevent false negatives if validation is accidentally over-restrictive.
  - **Suggested fix**: Add a test case with a fully valid `CycleConfig` that asserts `validate() == nil`.

- **enabled_rigs_test.go:163-190** — `appendMutate`/`removeMutate` are explicitly documented as "local replicas" of production closures. This pattern is brittle: if the production closure logic drifts, these tests pass but the real behavior diverges silently.
  - **Suggested fix**: Consider extracting the production logic into named functions that both production and tests import, rather than duplicating.

- **cycle_close_handler_test.go:146-151** — Custom `min` function shadows the Go 1.21+ builtin `min`. This compiles fine but is unnecessary on modern Go.
  - **Suggested fix**: Remove the custom `min` if the project uses Go >= 1.21.

## Observations

- **Strong patterns observed**:
  - Table-driven tests with `t.Parallel()` used consistently across all reviewed files.
  - Deterministic time fixtures (`testClock`, `fixedNow`) eliminate flakiness.
  - `t.TempDir()` for filesystem isolation — no cross-test contamination.
  - Boundary testing (e.g., `TestAppendRigTransition_Bounded`, `TestAppendIncidentTrimsToCap`) verifies FIFO eviction at capacity.
  - Error message content assertions (e.g., `done_rebase_test.go:138-146`) verify agent-facing contract, not just error presence.
  - Real git repo tests alongside fakes in `done_rebase_test.go` — both decision logic (fast, isolated) and wiring (real, end-to-end) are covered.

- **Positive design**: The `cycle_close_handler_test.go` `applyHandlerToState` helper is a well-designed test seam that exercises state-machine logic without requiring a Dolt database, while clearly documenting that the CAS loop is tested elsewhere.

- **daemon/restart_tracker_test.go**: Strong coverage of prune lifecycle including disk persistence round-trip and dry-run mode verification.

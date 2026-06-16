# Test Quality Audit

## Summary

The gastown test suite is large and broadly healthy: 920 test files back 864
non-test Go source files (a ~1.06:1 ratio), and the dominant style is
table-driven tests asserting concrete return values via standard-library
`t.Errorf`/`t.Fatalf`. Spot checks confirm only **one** real test file in the
whole tree (`internal/cmd/integration_testmain_test.go`) has zero assertion
primitives, and high-churn safety-critical packages (`git`, `dispatch`,
`refinery` gate engine, `daemon` rig-operational classification) are pinned by
genuinely strong behavior-verifying tests. The two critical findings from the
2026-06-04 audit — permanently-disabled `bd 0.47.2` lifecycle tests and a
tautology gate blind to stdlib assertions — have both been **fixed and
guarded** (`internal/cmd/bd_skip_guard_test.go` now fails CI if the obsolete
skip marker returns; commit `cac4b24d` taught the gate stdlib assertions before
the gate tree was retired in `3766dcdc`).

The meaningfulness problem is now concentrated, not diffuse, and falls into two
themes. First, a cluster of **`done.go` tests that re-implement production
decision logic inside the test body** ("Simulate the logic from runDone…") so
they verify a copy of the branch, not the real code path — these are theatre on
work-loss-critical paths (auto-commit-on-done, MR-verified-before-nuke, branch
detection). Second, a systemic **source-text-scraping pattern in the `reaper`
package**, where tests `os.ReadFile` `reaper.go` and `strings.Contains` for SQL
fragments instead of executing the query — they break on harmless refactors and
pass on semantically-wrong-but-textually-matching SQL. A real regression in the
`done` orchestration or reaper exclusion predicate could ship green today.

## Score

score: 0.71

## Critical Findings (P0 — file as beads, fix urgently)

### 1. `done.go` work-loss safety nets are tested by re-implementing the logic, not calling it
- **Location**:
  - `internal/cmd/done_test.go:1839-1871,1896-1963` (`TestAutoCommitSafetyNet`) — comment `// Simulate the auto-commit safety net` (line 1855); exercises only git primitives (`Add`/`Commit`/`ResetFiles`), never the real `done` orchestration.
  - `internal/cmd/done_test.go:777-840` (`TestMRVerificationSetsMRFailed`) — `// Simulate the MR creation + verification flow from done.go` (line 817); re-codes the GH#1945 "verify MR bead persisted before nuking worktree" guard on local booleans.
  - `internal/cmd/done_test.go:931-1026` (`TestBranchDetectionGuard`, `TestBranchDetectionCleanupOnError`) — `// Simulate the branch detection logic from runDone` (line 964).
  - `internal/cmd/done_test.go:1030-1131` (`TestConvoyMergeStrategyBranching`, `…Notification`) — `// Simulate the branching logic from runDone` (line 1070).
  - `internal/cmd/done_test.go:1968-2003` (`TestSyncGuardWithUncommittedChanges`) — recomputes `syncSafe` inline (lines 1995-1997).
- **Impact**: These name and claim to regression-test the most work-loss-prone
  paths in the system — auto-committing a polecat's uncommitted work on `gt
  done`, refusing to nuke a worktree before the MR bead is confirmed persisted,
  and branch detection/cleanup. Because the test body re-implements the decision
  rather than invoking `runDone`/the real orchestration, a bug in the actual
  `done.go` flow (wrong commit order, skipped runtime-path reset, missing verify
  before nuke) ships green. This is the highest-impact theatre in the suite.
- **Suggested fix**: Extract the inline decisions in `runDone` into small pure
  functions (the codebase already does this well for `shouldNudgeRefinery`,
  `relayBaseForLocalMerge`, `localMergeWouldStrandReviewedCodeBead`) and have the
  tests call them. Where extraction is impractical, drive `runDone` end-to-end
  with the existing bd/git stub harness (`done_awaiting_merge_test.go` is the
  model: it calls the real `updateAgentStateOnDone` and asserts the exact bd
  command stream).

### 2. Reaper agent-bead/SQL safety predicates tested by scraping source text, not executing queries
- **Location**: `internal/reaper/reaper_test.go` — 9 tests `os.ReadFile`
  `reaper.go` and assert via `strings.Contains`, e.g. `:389,:445`
  (`w.issue_type != 'agent'`), `:148-206` (`TestReaperQueriesUseTypedDependencyColumns`),
  `:356-389` (`TestClosePluginReceiptsQueriesWispsTables`),
  `:411-428` (`TestReapExcludesAgentBeads` — body is **only** `t.Log` calls; it
  executes no code and can never fail).
- **Impact**: The exclusion these guard (never let the wisp reaper close
  `hq-mayor`/`witness`/`refinery`/`deacon` agent beads) is safety-critical — a
  regression caused doctor to report core agents missing. A substring check
  passes even if the surrounding SQL is semantically wrong (wrong table alias,
  inverted predicate) and fails on harmless reformatting/aliasing. `TestReapExcludesAgentBeads`
  in particular is pure documentation masquerading as coverage.
- **Suggested fix**: Replace source-scraping with execution against the fake SQL
  driver already used by `TestClosedMoleculeStepReapBehavior` (`reaper_test.go:919`,
  which drives real reap/skip outcomes). At minimum, delete the no-op
  `TestReapExcludesAgentBeads` and rely on `TestScanExcludesAgentBeads`/
  `TestAutoCloseExcludesAgentBeads`, then add one execution-level test that
  inserts an `issue_type='agent'` row and asserts `Reap` leaves it open.

## Major Findings (P1 — track but do not auto-bead)

- **Wall-clock `time.Sleep` as synchronization / timestamp spacing (flaky risk).**
  `internal/daemon/convoy_manager_test.go:2309,2314` sleep `1100ms` each to space
  Dolt second-precision timestamps within one poll (`TestPollAllStores_ReopenResetsPerCycleDedup`);
  not short-mode gated. `internal/daemon/convoy_manager_integration_test.go:81,102`
  sleep `6s` each. `internal/cmd/molecule_await_event_test.go:324,555` sleep `800ms`
  ("longer than one poll interval"). Only ~5 test files gate on `testing.Short()`,
  so these run in the default `go test ./...` gate and add real wall-clock time +
  timing fragility. Prefer injectable clocks / event-driven waits (the codebase
  already does this well via `now`-passing helpers and `timeNowForDispatchMaint`).

- **No-op / result-discarding smoke tests on safety-critical gates.**
  `internal/cmd/sling_test.go:2596` (`TestIsHookedAgentDead_NoTmuxSession`) calls
  `isHookedAgentDead(...)` then `_ = result` with comment "We just verify it
  doesn't panic." `isHookedAgentDead` gates auto-burning a hooked bead; a
  regression flipping its return (wrongly burning an in-progress bead, or never
  reclaiming a dead one) passes silently. No test asserts the live-agent →
  not-dead → do-NOT-burn outcome.

- **Validation tests assert only error presence, not which field/why.**
  `internal/refinery/engineer_test.go:519-522,561-564`
  (`…InvalidPollInterval`, `…InvalidStaleClaimTimeout`) check only `err != nil`
  without asserting the message or offending field — a validator that rejects the
  wrong field still passes. Contrast `done_falseclose_test.go:45-48`, which
  asserts error message content.

- **`TestIsRigOperational_DockedRig` (`internal/daemon/daemon_test.go:837-877`)
  never sets up a docked rig** — it asserts the no-rig-bead fail-safe (already
  covered by `…_FailSafeOnDoltUnavailable`) and only `t.Logf`s the reason rather
  than asserting the docked-detection path the test name claims.

- **`TestEventPoll_LazyStoreOpening` (`internal/daemon/convoy_manager_test.go:749-803`)
  re-implements `runEventPoll`'s lazy-open loop** in the test body; `runEventPoll`
  is never invoked in the daemon test suite, so the asserted `callCount` measures
  the test's own loop.

## Minor Findings (P2 — informational)

- **Tautology-by-construction tests.** `done_test.go:577-609`
  (`TestDoneIntentLabelFormat`) builds a label with `fmt.Sprintf` and asserts it
  equals the same `fmt.Sprintf`; `done_test.go:848-889` (`TestMRBeadCreationUsesRig`)
  asserts a struct literal it just set (all table rows have `wantRig == rigName`,
  no discriminating case). These test Go, not production behavior.

- **Brittle source-line references in comments.** `done_test.go:29-208` frames
  `beads.ResolveBeadsDir` tests as covering "done.go line 181 / line 277" — the
  resolution is well-covered but the line-number framing rots on any edit.

- **Wall-clock data in fixtures (benign today).** Many sling tests use
  `AddedAt: time.Now().Truncate(time.Second)` for `config.RigEntry`
  (`sling_test.go:540,728,827,958,1366`, all of `sling_rollback_cleanup_test.go`).
  Never asserted against, so not flaky — but it is wall-clock leaking into
  fixtures where the rest of the suite correctly injects clocks.

- **Contention test with no guaranteed contention.** `sling_lock_test.go:93`
  (`TestTryAcquireSlingAssigneeLock_Contention`) releases the lock before/racing
  the main acquire and asserts only the second acquire succeeds — it would pass
  even if the retry logic were removed.

## What is solid (the bar to aim for)

`internal/dispatch/eligibility_test.go` (table-driven with near-miss negatives),
`internal/git/git_test.go` (real git on temp repos; asserts `errors.Is(err,
ErrUnsafeTownRootGitMutation)` AND byte-for-byte state preservation),
`internal/refinery/engineer_test.go` runGate/runGates suite (marker-file proof
that a skip step did NOT run after failure) and `TestIsClaimStale` (boundary
table), `internal/cmd/done_awaiting_merge_test.go` and `done_falseclose_test.go`
(real functions, exact command/error assertions, fail-closed negatives), and
`internal/daemon` `isRigOperational` suite (real bd stub scripts exercising real
error classification).

## Counts

  counts: critical=2 major=5 minor=4

# Code Review Synthesis — Convoy x2kby

## Executive Summary

Gas Town is a 266K-LOC Go codebase implementing a multi-agent workspace manager.
Ten review legs examined correctness, performance, resilience, security, style,
elegance, code smells, wiring, test quality, and commit discipline.

**Overall assessment: B+ — solid engineering, no blockers, evolutionary debt.**

The codebase demonstrates strong fundamentals: principled error handling, good
test coverage, coherent domain modeling, defense-in-depth security in the sandbox,
and excellent documentation culture. The primary weaknesses are structural —
organic accretion has produced a few "god functions" and a monolithic `cmd`
package — but these are maintenance risks, not correctness bugs.

**Merge recommendation:** No blocking issues. All findings are evolutionary
improvements that can be addressed incrementally over subsequent sprints.

---

## Critical Issues

### P0-1: Two panics in library packages (Resilience)

**Files:**
- `internal/connection/address.go:135` — `panic("invalid address...")`
- `internal/workspace/find.go:191` — `panic("failed to get town name...")`

Library-code panics bypass all graceful shutdown, recovery, and observability
in a multi-agent system where processes must be long-lived.

**Fix:** Return errors. Callers can decide whether to fatal.

---

## Major Issues

### P1-1: 227 files fail `gofmt` (Style)

`gofmt -l internal/` reports 227 files with formatting drift. CI does not
currently gate on this. Common patterns: extra alignment padding in struct
fields, manual map alignment, extra whitespace before format verbs.

**Fix:** `gofmt -w internal/` + add gofmt to gate suite.

### P1-2: God functions — `runDone` (2497 LOC), `Daemon.Run` (499 LOC), `runSling` (1018 LOC) (Smells + Elegance)

*Found by: Smells, Elegance*

These monolithic functions resist isolated testing, make bisection difficult,
and force all new features through a single modification point. `done.go`
encodes an entire recovery state machine as procedural code.

**Fix:** Extract sub-workflows into named functions or a step-pipeline pattern.

### P1-3: `internal/cmd` package is 106K LOC across 258 files (Elegance + Smells)

*Found by: Elegance, Style*

Business logic lives in the CLI layer instead of domain packages. New developers
must navigate 258 files in one package to contribute.

**Fix:** Extract domain logic into focused packages (`internal/dispatch/`,
`internal/polecat/completion`, etc.).

### P1-4: Git subprocess calls without timeouts (Resilience)

**File:** `internal/daemon/lifecycle.go:644-712` + 5 other locations

`exec.Command("git", ...)` without context/timeout. A network partition hangs
the daemon heartbeat loop indefinitely, stalling all patrol dogs.

**Fix:** Replace with `exec.CommandContext` using 30-60s timeout.

### P1-5: Tautology analyzer over-classifies test-local helpers as FUT (Correctness)

**File:** `internal/autotest/tautology/tautology.go:450-457`

Any non-stdlib, non-helper call is classified as "potential FUT" — including
test-local factories. Tautological tests slip through when helpers provide
"taint" instead of the actual system-under-test.

**Fix:** Exclude functions defined in `_test.go` files from FUT classification.

### P1-6: `extractRigFromMRLabels` does not validate rig name content (Security)

**File:** `internal/cmd/auto_test_pr_revise.go:~180`

Rig name from label used in `filepath.Join` without validation. Path traversal
possible if a future codepath allows user-authored labels.

**Fix:** Validate rig name matches `^[a-z][a-z0-9_]{0,63}$`.

### P1-7: Environment variables as implicit config protocol — 77 `GT_*` vars (Elegance)

214 files read env vars directly with no central registry, type safety, or
validation. A typo in a var name silently breaks functionality.

**Fix:** Create `internal/env/env.go` with typed accessors.

### P1-8: Silent error swallowing in attribution state write (Resilience)

**File:** `internal/daemon/main_branch_test_attribution.go:140-145`

Error discarded with `_ = err`. If the state file is permanently unwriteable,
D16 auto-revert never fires (depends on attribution data).

**Fix:** Return error so caller with `d.logger` can log it.

### P1-9: `fmt.Fprintf(os.Stderr)` for operational logging in library code (Style)

**File:** `internal/autotestpr/cycle.go:199,264`

Library functions write directly to stderr rather than accepting a logger.
Bypasses structured logging, makes output untestable.

**Fix:** Accept `*log.Logger` in `CycleConfig`.

### P1-10: HandleEvent idempotency gap on circuit-breaker counter (Security)

**File:** `internal/autotestpr/cycle_close_handler.go:~115`

Duplicate event delivery increments the counter twice, potentially tripping
the circuit breaker prematurely (denial-of-service on operator attention).

**Fix:** Track processed MR IDs in the rejection log; skip counter increment
on duplicates.

### P1-11: WIP commits on main (Commit Discipline)

Two `WIP` commits leaked to main via stranded-polecat recovery. `git bisect`
may land on half-implemented states.

**Fix:** Recovery workflow should squash WIP into the completing commit, or
land on feature branches.

---

## Minor Issues

| ID | Leg | Summary | File |
|----|-----|---------|------|
| P2-1 | Correctness | `preVerified` param unused for control flow | `done_rebase.go:35` |
| P2-2 | Correctness | `isTrivialCheck` map allocates per call | `tautology.go:238` |
| P2-3 | Correctness | `exprEqual` doesn't handle `*ast.StarExpr` | `tautology.go:296` |
| P2-4 | Correctness | `ParseBugDiscoveredNotes` case-sensitive | `cycle_close_handler.go:344` |
| P2-5 | Performance | `BranchDelta` could pre-size map | `coverage.go:315` |
| P2-6 | Style | `sliceContains` vs stdlib `slices.Contains` | `main_branch_test_runner.go:677` |
| P2-7 | Style | `minInt` helper redundant with Go 1.25 `min()` | `main_ci_break_dog.go:365` |
| P2-8 | Style | Large file sizes (done.go, convoy.go) | `internal/cmd/` |
| P2-9 | Style | Inconsistent doc.go presence | `internal/` |
| P2-10 | Smells | DRY violation: bd-list pattern repeated 9× | `daemon/*.go` |
| P2-11 | Smells | Deep nesting in `detectZombieLiveSession` | `witness/handlers.go` |
| P2-12 | Smells | Data clump: `Enabled + IntervalStr` repeated 12× | `daemon/types.go` |
| P2-13 | Security | Sandbox doesn't strip HOME/XDG_* env vars | `sandbox.go:18` |
| P2-14 | Security | Unsanitized description in bug bead title | `cycle_close_handler.go:365` |
| P2-15 | Elegance | Deprecated `constants` package still imported | `constants.go` |
| P2-16 | Elegance | Config layer 3 (town defaults) is a stub | `rig/config.go:76` |
| P2-17 | Commit | Inconsistent scope tag: `auto-test-pr` vs `autotestpr` | git log |
| P2-18 | Commit | Multi-concern lint-fix commits | git log |
| P2-19 | Resilience | No circuit breaker on Dolt connectivity | daemon dogs |
| P2-20 | Resilience | Best-effort ops without observability | scattered |
| P2-21 | Wiring | UpstreamSync config defined, no daemon patrol | `config/types.go:764` |

---

## Wiring Gaps

From the wiring review:

- **UpstreamSync config defined but no daemon patrol**: `config/types.go:764`
  defines the struct, `internal/upstreamsync/` implements the state machine,
  CLI (`gt upstream status`) works — but no daemon patrol dog orchestrates
  automated sync. Clearly staged delivery (Phase 1 = CLI, Phase 2 = daemon).

- **`go-rod` only used in test files**: Direct dependency in go.mod but only
  imported in `internal/web/browser_e2e_test.go`. Valid Go module behavior but
  inflates the dependency tree for production awareness.

---

## Commit Quality

**Grade: B+**

- **Strengths:** Conventional commits used consistently. Atomic changes.
  Imperative mood. Bead ID references for traceability. Multi-agent workflow
  naturally produces well-scoped commits.
- **Weaknesses:** 2 WIP commits on main. Inconsistent scope tags
  (`auto-test-pr` vs `autotestpr`). A few batch-fix commits that bundle
  unrelated changes.

---

## Test Quality

**Grade: B+**

- **Strengths:** Table-driven tests with `t.Parallel()`. Deterministic time
  fixtures. `t.TempDir()` isolation. Boundary testing. Both fast fakes and
  real-git integration tests coexist.
- **Weaknesses:** `TestRunCycle_MissingTownBead` doesn't test the actual
  missing-bead path (only config validation). Custom helpers reimplement
  stdlib. One test duplicates production logic instead of importing it.

---

## Positive Observations

1. **Documentation culture is exceptional** — nearly every field and recovery
   path has a "why" comment with issue ID references. ADR-in-doc.go (sandbox)
   is exemplary.

2. **Domain naming is strong** — polecat, witness, refinery, sling, nudge,
   formula. The metaphor carries cognitive weight.

3. **Error handling is principled** — universal `%w` wrapping, sentinel errors,
   actionable error messages with file/ID context.

4. **Sandbox defense-in-depth** — credential strip + CWD pin + network
   namespace + process-group kill + wall-clock cap. The security engineering
   is thorough.

5. **Recovery model is sophisticated** — RestartTracker, crash-loop guard,
   exponential backoff, mass-death detection, deacon heartbeat monitoring.

6. **nolint annotations always justified** — 218 annotations, all with
   explanatory comments.

7. **Timeout coverage on `bd` calls is excellent** — every `bd` subprocess
   uses `exec.CommandContext` with 10-20s timeouts.

8. **The formula type system** (convoy, workflow, expansion, aspect) with
   TOML parsing is a well-designed DSL layer.

---

## Recommendations

**Immediate (next sprint):**
1. Run `gofmt -w internal/` and add gofmt to CI gates (P1-1)
2. Replace panics with error returns in connection/workspace (P0-1)
3. Add timeouts to git subprocess calls in daemon (P1-4)

**Short-term (1-2 sprints):**
4. Log (not discard) the attribution state-write error (P1-8)
5. Validate rig name from labels before path construction (P1-6)
6. Add `slices.Contains` and remove `minInt`/`sliceContains` helpers (P2-6, P2-7)
7. Standardize commit scope to `auto-test-pr` (P2-17)

**Medium-term (refactoring track):**
8. Extract `runDone` sub-workflows into named functions (P1-2)
9. Create `internal/env/` package for typed env-var access (P1-7)
10. Extract bd-list helper to deduplicate daemon dog pattern (P2-10)
11. Implement PatrolRunner registry pattern for daemon (P1-2 / P1-3)

**Long-term (architectural):**
12. Extract business logic from `internal/cmd/` into domain packages (P1-3)
13. Improve tautology analyzer FUT classification heuristic (P1-5)
14. Add Dolt circuit-breaker shared across patrol dogs (P2-19)

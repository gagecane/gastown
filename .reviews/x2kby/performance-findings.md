# Performance Review

## Summary

The recent code additions are well-structured with respect to performance. The
auto-test-pr pipeline components (coverage parser, tautology linter, sandbox,
cycle-close handler) operate on bounded inputs (single files, small profiles,
≤20-entry logs) and use appropriate data structures (maps for O(1) lookup,
bounded slices with cap). No O(n²) algorithms or unbounded growth patterns
were found in hot paths. The code is designed for correctness-first on small
inputs rather than scale — appropriate for v1 where the cadence is ≤1 MR/week
per rig.

## Critical Issues

(None found — no P0 performance issues)

## Major Issues

(None found — no P1 performance issues)

## Minor Issues

### P2-1: BranchDelta builds a full map even for profiles with thousands of blocks
**File:** `internal/autotest/coverage.go` (line 315-336)

```go
beforeCovered := map[BlockKey]bool{}
if before != nil {
    for _, b := range before.Blocks {
        if b.Covered() {
            beforeCovered[b.Key()] = true
        }
    }
}
```

For large Go packages, cover profiles can have thousands of blocks. The function
allocates a map entry for every covered block in the "before" profile. While this
is O(n) time and space (correct), it could be optimized by:
1. Pre-sizing the map: `make(map[BlockKey]bool, len(before.Blocks))`
2. Early-exit: if `delta > 0` is all we need, break after finding the first new
   coverage (though the current design counts all new blocks, which is useful for
   reporting).

**Impact:** Negligible for v1 (profiles are typically <5000 blocks). Worth noting
only if the system ever processes coverage for large monorepo packages.

### P2-2: Tautology analyzer re-traverses AST for each assertion type
**File:** `internal/autotestpr/tautology/analyzer.go`

The analyzer walks the AST statement-by-statement with `analyzeStmt`, which is
O(statements) per test function. For each assignment, it classifies the RHS by
traversing the expression tree. For each assertion call, it traverses argument
expressions for taint. This is effectively O(statements × avg_expr_depth), which
for typical Go test functions (10-50 statements, depth ≤5) is well within bounds.

However, the `exprToTaint` function recursively inspects expressions and performs
string concatenation for composite taint labels:
```go
for _, arg := range e.Args {
    if t := exprToTaint(arg, tainted); t != "" {
        return t
    }
}
```

For deeply nested expressions (unlikely in tests but possible with builder
patterns), this could create many short-lived strings. Using a `strings.Builder`
or early return would be more cache-friendly.

**Impact:** Negligible — test functions rarely have deep expression nesting.

### P2-3: ParseBugDiscoveredNotes uses strings.Split on potentially large MR bodies
**File:** `internal/autotestpr/cycle_close_handler.go` (line 342)

```go
for _, line := range strings.Split(body, "\n") {
```

`strings.Split` allocates a new `[]string` with one entry per line. For MR
bodies that are typically <100 lines, this is fine. However, a `bufio.Scanner`
approach would avoid the allocation of the full slice up front and short-circuit
once all `BUG-DISCOVERED:` lines are found.

**Impact:** Negligible — MR bodies are small. Mentioning only for completeness.

### P2-4: CycleBudget.Acquire takes a mutex for every call
**File:** `internal/autotest/sandbox/timeout.go` (line ~180)

The `CycleBudget` uses a `sync.Mutex` for thread safety. Since gates run
sequentially today (documented in the comment), the mutex contention is zero.
If future gates run in parallel (e.g., flakiness rerun), the per-acquire lock
is fine because each acquire is followed by a long subprocess execution.

No issue — just noting that the concurrency design is ready for parallelism
without requiring a redesign.

## Observations

- **Positive:** The `MaxRigTransitions = 20` cap on transition and rejection logs
  prevents unbounded growth in the TownState bead metadata. This is a smart
  bounded-buffer design that avoids the "state bead grows forever" problem.

- **Positive:** The sandbox's `RunWithTimeout` uses a 2-second grace period after
  SIGKILL, then accepts the goroutine leak rather than blocking indefinitely. This
  is the correct trade-off: a hung kernel shouldn't block the polecat's cycle budget.

- **Positive:** The `BranchDelta` function's O(n+m) approach (build map of before,
  scan after) is optimal for the coverage-delta computation. No improvement possible
  without sacrificing correctness.

- **Positive:** The `CycleBudget.Acquire` / `Charge` split (reserve first, charge
  actual elapsed later) means early exits don't waste budget. A gate that completes
  in 200ms when allowed 5min only charges 200ms, preserving budget for subsequent
  gates.

- **Observation:** At 10x scale (10 opted-in rigs, each with ≤1 MR/week), the
  cycle-close handler processes at most 10 events per week. At 100x scale (100
  rigs), it's 100 events/week. The handler's per-event cost is O(1) state lookup +
  O(MaxRigTransitions) log append = constant. No scaling concern in any realistic
  v1-v2 deployment.

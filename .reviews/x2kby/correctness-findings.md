# Correctness Review

## Summary

Reviewed recent commits on origin/main (d40e908c..332396b5 scope, plus the
5 newest commits through d40e908c). The codebase shows solid engineering
practices: good error handling, clear separation of concerns, and well-reasoned
trade-offs documented in commit messages. The most impactful correctness
concern is in the tautology analyzer's flow-sensitive taint analysis, which
has a false-positive risk from overly aggressive FUT classification. The
done_rebase fix (gs-4bn) is correctly implemented with proper variable
scoping and test coverage.

## Critical Issues

(None found — no P0 merge-blocking issues.)

## Major Issues

### P1-1: Tautology analyzer classifies ALL non-stdlib function calls as FUT (false negatives on rule i)

**File:** `internal/autotest/tautology/tautology.go:450-457`

The `classifyExpr` function treats any call that is NOT a test helper and NOT
stdlib as a "potential FUT" — including test-local factory/setup functions
defined in the test file itself. This means a test like:

```go
func makeFixture() *Foo { return &Foo{} }
func TestExample(t *testing.T) {
    got := makeFixture()
    assert.NotNil(t, got) // <- tainted by "makeFixture", rule (i) passes
}
```

...would pass rule (i) because `makeFixture()` is classified as FUT, but it's
actually a trivial factory that doesn't exercise the real system under test.
The test is tautological in practice — it only asserts that its own fixture is
non-nil.

**Impact:** False negatives for rule (i) — tautological tests slip through
when test-local helpers produce the "taint" instead of the actual FUT.

**Suggested fix:** Consider a heuristic: functions defined in the same file
(or `_test.go` files in general) as the test should not count as FUT sources.
This requires resolving the call target's declaration scope, which may require
`go/types` or filename-based heuristics (the function name starts with
`make`/`new`/`setup`/`create`).

### P1-2: Circuit breaker counter is "consecutive close" but resets on window expiry with count=1

**File:** `internal/autotestpr/cycle_close_handler.go:254-259`

```go
if now.Sub(windowStart) > CircuitBreakerWindow {
    s.CircuitBreaker.Count = 1 // current event still counts
    s.CircuitBreaker.WindowStartedAt = now.UTC().Format(time.RFC3339)
    return false
}
```

The comment says "consecutive-close counter" but the window-expiry logic
resets to 1 (counting the current event). However, a merged event between
two close events also resets the counter to 0 (line 181-183). This creates
an asymmetry:

- Merged events: reset to 0
- Window expiry: reset to 1

If a rig has a rejection, then goes 8 days idle, then another rejection:
count becomes 1 (window-expired reset) → correct.

But the comment at line 180 says "reset circuit-breaker counter
(consecutive-close resets)" — implying the intent is consecutive. The
window-expiry path creates a sliding-window semantic that contradicts the
consecutive intent. This is documented as a Phase 0 simplification, so it
may be intentional, but the dual semantics risk confusion.

**Impact:** Low in practice (the threshold is 3, so it takes 3 rejections
in 7 days regardless), but the mixed semantics could surprise future
maintainers.

**Suggested fix:** Add a brief comment clarifying the intended hybrid
semantic: "Phase 0: consecutive-close counter with a staleness guard."

## Minor Issues

### P2-1: `autoRebaseOnTarget` still accepts `preVerified` parameter but never uses it for control flow

**File:** `internal/cmd/done_rebase.go:35`

After the gs-4bn fix, the `preVerified` parameter is only used for a
`fmt.Printf` information message. The control-flow decision was removed. The
parameter signature is still part of the function's API — consider whether it
should remain for documentation/logging purposes or be removed for clarity.

Not a bug — just dead control flow that could confuse readers who expect the
parameter to affect behavior.

### P2-2: `isTrivialCheck` map allocates on every call

**File:** `internal/autotest/tautology/tautology.go:238-250`

The `isTrivialCheck` function allocates a new map literal on every
invocation. This is called once per assertion per test function during
analysis. For large test suites, this creates unnecessary GC pressure.

**Suggested fix:** Lift the map to package-level `var`:
```go
var trivialChecks = map[string]bool{...}
```

Same pattern applies to `isEqualStyle` (line 616) and `isSingleArgStyle`
(line 636) and `isStdlibCall` builtins map (line 684).

### P2-3: `exprEqual` doesn't handle `*ast.StarExpr` (pointer dereference)

**File:** `internal/autotest/tautology/tautology.go:296-328`

The `exprEqual` function handles Ident, BasicLit, SelectorExpr, IndexExpr,
and CallExpr — but not `*ast.StarExpr` (pointer dereference) or
`*ast.UnaryExpr`. An assertion like `assert.Equal(t, *a, *a)` would not
be caught by the self-equal detection. Low priority since this pattern is
rare.

### P2-4: `ParseBugDiscoveredNotes` is case-sensitive

**File:** `internal/autotestpr/cycle_close_handler.go:344`

```go
const prefix = "BUG-DISCOVERED:"
if strings.HasPrefix(line, prefix) {
```

This requires exact `BUG-DISCOVERED:` casing. But `extractTargetPathFromBody`
(line 324) uses `strings.ToLower` for case-insensitive matching. The
inconsistency could cause missed bugs if a polecat writes
"Bug-Discovered:" or "bug-discovered:".

**Suggested fix:** Use `strings.EqualFold` or `strings.ToUpper(line)` prefix
matching for consistency with `extractTargetPathFromBody`.

## Observations

- The done_rebase fix (gs-4bn) is well-engineered: it correctly separates
  "should we rebase?" (always yes when behind) from "should we attest
  pre-verification?" (only when rebase didn't invalidate gates). The test
  coverage explicitly documents the behavioral change.

- The tautology analyzer's architecture is clean — single-pass AST walk with
  well-named sub-rule functions. The taint analysis is sophisticated for a
  linter that avoids `go/types`.

- The cycle_close_handler's idempotency via `CreateIfNoDuplicate` is a good
  pattern for event-driven handlers that may face at-least-once delivery.

- The upstream sync types define a clean state machine with explicit valid
  states and proper JSON serialization. The separation of `SyncAttempt`
  from `SyncStatus` is well-motivated.

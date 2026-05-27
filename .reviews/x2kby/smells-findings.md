# Code Smells Review

## Summary

The codebase has a moderate level of technical debt concentrated in a few "god
functions" — notably `runDone` (1596 lines), `Daemon.Run` (499 lines), and
`runSling` (1018 lines). These are the primary sources of future maintenance
pain. The rest of the codebase is well-factored with good separation of
concerns, minimal TODO accumulation (only 1 real TODO in production code), and
clear package boundaries.

The daemon dogs (failure_classifier, mr_cycle_close, main_ci_break) share a
common `exec.CommandContext → bd list → json.Unmarshal` pattern that's
duplicated across files but not yet extracted into a shared helper. This is a
mild DRY violation that will compound as more dogs are added.

## Critical Issues

(None — no smell that would block a merge.)

## Major Issues

### P1-1: God function: `runDone` — 1596 lines

**File**: `internal/cmd/done.go:1`

The `runDone` function handles the entire `gt done` command — polecat done,
crew done, mayor done, deacon done, witness done, and several sub-workflows
(overlay stripping, submodule push, mq submit, agent state update). At 1596
lines, this function:

- Cannot be tested in isolation (any unit test must set up the entire world)
- Makes bisection of `gt done` bugs nearly impossible
- Makes adding a new "done" flavor risky (accidental interactions)

**Impact**: High — `gt done` is one of the most critical commands. A bug in one
branch of this function can affect all agents.

**Suggested refactor**: Extract sub-workflows into named functions:
```go
func (d *doneContext) handlePolecatDone() error { ... }
func (d *doneContext) handleCrewDone() error { ... }
func (d *doneContext) handleMQSubmit() error { ... }
```

### P1-2: God function: `Daemon.Run` — 499 lines

**File**: `internal/daemon/daemon.go:~400`

The `Run` method combines initialization, ticker creation, event loop, and
cleanup in a single function. The select statement at its core is growing with
each new dog (failure_classifier, mr_cycle_close, main_ci_break...). Adding a
new patrol requires touching this function in 3 places (ticker init, channel
var, select case).

**Impact**: Medium-high — adding a new daemon patrol requires modifying a 500-line
function in 3 coordinated locations.

**Suggested refactor**: Extract a `PatrolRunner` registry pattern:
```go
type PatrolRunner interface {
    Name() string
    Interval() time.Duration
    Run()
}
d.RegisterPatrol(&mainCIBreakRunner{...})
```
The event loop then ranges over registered patrols generically.

### P1-3: God function: `runSling` — 1018 lines

**File**: `internal/cmd/sling.go`

Similar to `runDone` — one function doing too many things for `gt sling`.

**Impact**: Medium — sling is less critical-path than done, but still complex.

## Minor Issues

### P2-1: DRY violation across daemon dogs

**Files**: `failure_classifier_dog.go`, `mr_cycle_close_dog.go`, `main_ci_break_dog.go`

All three dogs implement the same pattern:
1. `exec.CommandContext(ctx, d.bdPath, "list", "--label=...", "--status=...", "--json")`
2. `cmd.Dir = d.config.TownRoot`
3. `cmd.Env = os.Environ()`
4. `setSysProcAttr(cmd)`
5. `cmd.Output()` → `json.Unmarshal`

This is duplicated 9 times across the three files. The `dogMol.runBd` helper
exists but is scoped to molecule dogs only.

**Suggested fix**: Extract `Daemon.bdListJSON(labels, status string, opts ...bdListOpt) ([]byte, error)`
helper to dedup the subprocess + env + timeout boilerplate.

### P2-2: Deep nesting in `detectZombieLiveSession` (156 lines)

**File**: `internal/witness/handlers.go`

This function has multiple nested `if` blocks checking session state, agent
bead state, and zombie heuristics. Nesting reaches 5-6 levels in places.

**Suggested fix**: Early returns to flatten the nesting (guard clauses).

### P2-3: Data clump in daemon dog configs

Every `*Config` struct in daemon/types.go has `Enabled bool` + `IntervalStr string`.
This 2-field pattern is repeated 12+ times. Could be embedded:
```go
type DogConfigBase struct {
    Enabled     bool   `json:"enabled"`
    IntervalStr string `json:"interval,omitempty"`
}
```

**Impact**: Low — it's boilerplate but not causing bugs.

## Observations

- **TODO/FIXME discipline is excellent**: Only 1 real TODO in production code
  (`autotestpr/cycle.go:186`). This means TODOs get resolved or filed as beads.
- **Technical debt is being paid down**: Recent commits show refactoring
  (e.g., convoy system, checkpoint_dog, compactor_dog) alongside new features.
- **Package boundaries are clean**: No import cycles, clear ownership per
  package (daemon/ owns patrols, cmd/ owns CLI, witness/ owns health checks).
- **Struct Daemon has 40+ fields**: While large, each field is well-documented
  with concurrency notes. This is a trade-off for single-goroutine simplicity.
- **Copy-paste across design doc legs**: The `.designs/` directory has
  near-identical structure across convoy legs. This is intentional (convoy
  pattern) and not a codebase smell.

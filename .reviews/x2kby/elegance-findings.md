# Elegance Review

## Summary

Gas Town is a 266K-LOC Go codebase implementing a multi-agent workspace manager with a remarkably coherent domain model. The architecture maps cleanly to its real-world metaphor: a "town" with "rigs" (project worktrees), "polecats" (worker agents), "witnesses" (health monitors), a "refinery" (merge queue), and a "daemon" (recovery safety net). Individual packages like `formula`, `doctor`, `connection`, and `nudge` demonstrate strong abstraction boundaries with clear single responsibilities.

However, the codebase shows signs of organic accretion under operational pressure. The `internal/cmd` package (106K LOC across 258 files) has become a gravity well, housing business logic that belongs in domain packages. The `Daemon` struct accumulates ~30 fields representing concerns that could be separate collaborating subsystems. Environment variables (77 unique `GT_*` vars scattered across 214 files) serve as an implicit configuration protocol with no central registry or documentation. The `done.go` command at 2,500 lines embeds an entire recovery-oriented state machine in what should be a thin CLI wrapper.

Despite these structural issues, the codebase demonstrates strong design *principles*: consistent error handling via sentinel errors, a principled config layering system (wisp → bead → town → system), excellent doc comments with issue-tracking references, and a genuine attempt at interface-driven design in newer packages (`Connection`, `Check`, formula types). The challenge is more about refactoring debt than fundamental design flaws — the abstractions are right, they just need extraction and consolidation.

## Critical Issues

*(P0 - Must fix before merge)*

None. The codebase functions correctly and the design issues are evolutionary, not blocking.

## Major Issues

*(P1 - Should fix before merge)*

### 1. `internal/cmd` package is a monolithic gravity well

**File:** `internal/cmd/` (258 source files, 106K LOC)

The cmd package has become the default location for business logic. Files like `done.go` (2,497 lines), `convoy.go` (2,711 lines), and `sling.go` + `sling_helpers.go` (3,325 lines combined) contain domain logic, recovery state machines, safety nets, and operational heuristics that should live in dedicated domain packages.

**Impact:** New developers must understand 106K LOC of one package to contribute. Business logic is untestable in isolation from the CLI framework. Refactoring becomes risky because cmd is a single compilation unit where any change may have unexpected side effects.

**Suggested fix:** Extract domain logic into focused packages:
- `done.go` safety nets → `internal/polecat/completion.go`
- `sling_helpers.go` dispatch logic → `internal/dispatch/`
- `convoy.go` convoy operations → `internal/convoy/` (partially exists)
- `prime.go` context rendering → `internal/prime/`

### 2. Daemon struct accumulates too many concerns

**File:** `internal/daemon/daemon.go:53-226`

The `Daemon` struct has ~30 fields spanning: session death tracking, deacon lifecycle, Dolt health caching, rig status caching, boot spawn cooldown, convoy management, auto-dispatch watching, log rotation, doctor molecules, maintenance scheduling, and telemetry. Each concern is documented (good!) but they're all in one 3,845-line file.

**Impact:** The heartbeat loop touches all these subsystems in a single goroutine, making it hard to reason about timing interactions. Adding new daemon responsibilities requires modifying this file. The comments explaining "Only accessed from heartbeat loop goroutine - no sync needed" appear 10+ times — a signal that the struct has grown beyond what's natural.

**Suggested fix:** Extract each concern into a `DogKennel` pattern where each "dog" (already the naming convention for daemon subsystems like `compactor_dog.go`, `doctor_dog.go`) owns its own state and presents a `Tick(state *State)` interface to the heartbeat loop.

### 3. Environment variables as implicit configuration protocol

**Files:** 214 files, 77 unique `GT_*` vars, 267 references

Environment variables like `GT_BRANCH`, `GT_POLECAT`, `GT_RIG`, `GT_SESSION`, `GT_TOWN_ROOT`, etc. serve as an undocumented inter-process communication protocol. The same variable is read in multiple packages with no central registry, no type safety, and no validation. `done.go` alone reads 15 different env vars.

**Impact:** Debugging session failures requires knowing which env vars were set. New env vars get added without documentation. Tests must construct complex env var sets to simulate production states. A typo in a var name (`GT_POLCAT` vs `GT_POLECAT`) would silently break functionality.

**Suggested fix:** Create `internal/env/env.go` with typed accessor functions:
```go
package env

func Rig() string          { return os.Getenv("GT_RIG") }
func Polecat() string      { return os.Getenv("GT_POLECAT") }
func Branch() string       { return os.Getenv("GT_BRANCH") }
func SessionName() string  { return os.Getenv("GT_SESSION") }
```
This centralizes documentation, enables validation, and makes env vars discoverable via code navigation.

### 4. `done.go` encodes a complex recovery state machine as procedural code

**File:** `internal/cmd/done.go` (2,497 lines)

The `runDone` function handles: actor validation, workspace detection with fallback, rig inference with multi-source fallback, branch detection with detached-HEAD recovery, auto-cleanup-status detection, stash auto-pop with staleness guards, auto-commit safety net with mainline protection, MR submission, witness notification, idle transition, and cleanup. These are implemented as a linear sequence of fallback chains with extensive inline comments.

**Impact:** The function's complexity makes it fragile — each new edge case adds more conditional branches. The recovery logic (detached HEAD → env var → polecat name → error) is repeated in slightly different forms. A reader must hold the entire 2,500 lines in mind to understand any individual recovery path.

**Suggested fix:** Model as an explicit state machine or pipeline:
```go
type DoneContext struct { ... }  // Collected resolution state
type DoneStep interface { Execute(*DoneContext) error }

steps := []DoneStep{
    &ResolveWorkspace{},
    &ResolveBranch{},
    &RecoverStashes{},
    &AutoCommitSafetyNet{},
    &SubmitToMergeQueue{},
    &NotifyWitness{},
}
```

## Minor Issues

*(P2 - Nice to fix)*

### 5. Inconsistent package sizing creates cognitive load

Some packages are well-factored (`formula`: 5 focused files, `nudge`: 4 files) while others combine many concerns (`beads`: 20+ files with agent, channel, delegation, dog, escalation, group, merge_slot, mr, queue, redirect, rig, sling_context, types all in one package).

**File:** `internal/beads/` (23 source files, mix of beads_*.go suffixed files)

**Suggested fix:** Consider splitting `beads` into sub-packages: `beads/core`, `beads/agent`, `beads/channel`, `beads/mr`.

### 6. Magic strings in session naming conventions

**File:** `internal/session/identity.go`

Session name parsing relies on path conventions (`"polecats"`, `"crew"`) as magic strings. These are consistent but not enforced at a type level.

**Suggested fix:** Define directory constants alongside the Role constants they correspond to.

### 7. Config layering is well-designed but layer 3 (town defaults) is a stub

**File:** `internal/rig/config.go:76-80`

```go
// Layer 3: Town defaults
// Note: Town defaults for operational state would typically be in
// ~/gt/settings/config.json. For now, we skip directly to system defaults.
// Future: load from config.TownSettings
```

The 4-layer config system (wisp → bead → town → system) is an excellent design, but the town layer has been a TODO since the system was written. Code that needs town-level overrides likely bypasses this API entirely.

**Suggested fix:** Implement layer 3 or remove the comment and document that town defaults are accessed directly via `config.TownSettings`.

### 8. `constants` package is deprecated but still used

**File:** `internal/constants/constants.go:9-12`

```go
// DEPRECATED as single source of truth: These constants are retained for
// backward compatibility. New code should use config.OperationalConfig
// accessors which support per-town overrides via settings/config.json.
```

The package acknowledges its own obsolescence but remains imported. This creates confusion about which approach is canonical.

**Suggested fix:** Audit usages and migrate remaining consumers to `config.OperationalConfig`. Once empty, remove the package.

## Observations

*(Non-blocking notes and suggestions)*

- **Strong documentation culture**: Nearly every field, constant, and recovery path has a comment explaining *why*, often with issue ID references (gu-50qv, gt-pvx, gu-vtkn, etc.). This is exemplary for a multi-agent system where context is frequently lost.

- **Principled interface design in newer packages**: `Connection`, `Check` (doctor), formula types, `beadsdk.Storage` — the newer code shows clean interface boundaries. The `Connection` abstraction enabling local/SSH transparency is particularly elegant.

- **The "ZFC" (Zero File Configuration) philosophy** is consistently applied: tmux session existence as source of truth, no PID files, no state files. This is a strong architectural principle that simplifies recovery.

- **The doctor pattern is well-crafted**: Registration-based checks with categories, streaming output, auto-fix capability, and structured reporting. This is a good model for extensibility.

- **Naming quality is high overall**: `polecat`, `witness`, `refinery`, `sling`, `nudge`, `formula` — domain terms are evocative and consistent. The metaphor carries real cognitive weight.

- **The `cmd` package's flat namespace is Go-idiomatic for CLIs** (Cobra convention) but has outgrown the pattern. At 258 files, it would benefit from subpackages even if that's unconventional for Cobra.

- **The `_unix.go` / `_windows.go` split** is consistently applied for platform-specific code, which is good cross-platform hygiene.

- **The formula type system** (convoy, workflow, expansion, aspect) with TOML parsing and validation is a well-designed DSL layer that cleanly separates workflow definition from execution.

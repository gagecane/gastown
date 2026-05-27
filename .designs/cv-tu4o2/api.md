# API & Interface Design

## Summary

The nudge queue expiry eviction feature requires interfaces at three layers:
(1) a **programmatic Go API** in `internal/nudge/` for the eviction logic itself,
consumed by the daemon patrol dog and CLI commands; (2) a **CLI surface** via
`gt nudge gc` and `gt nudge status` subcommands for operator visibility and
manual intervention; and (3) a **configuration interface** through the existing
`NudgeThresholds` / `DaemonPatrolConfig` JSON structures for tuning GC behavior
without code changes.

The design follows Gas Town's established patterns exactly: the `poller_dog`
pattern for daemon integration, the `gt dolt status` / `gt dolt cleanup` pattern
for CLI subcommands, and the ZFC (zero-friction configuration) pattern for
thresholds. No new concepts are introduced — an operator who knows `gt dolt
cleanup --dry-run` already knows `gt nudge gc --dry-run`. The API naming
follows the existing codebase convention: exported functions with clear verb
prefixes (`Evict`, `QueueStats`), no abbreviations, and Go-idiomatic error
return patterns.

## Analysis

### Key Considerations

- **Existing API surface is stable**: `Enqueue()`, `Drain()`, `Pending()`,
  `QueueLen()`, `Requeue()`, `RemoveKindByThread()`, `RemoveKindByOriginal()`
  are all exported. The eviction feature must not modify these signatures.
- **Consumers are diverse**: The `nudge` package is imported by `internal/cmd`,
  `internal/acp`, `internal/crew`, `internal/daemon`, `internal/mayor`,
  `internal/refinery`, and `internal/polecat`. Any new exported API must not
  create import cycles.
- **CLI pattern precedent**: `gt dolt` uses parent command + subcommands (cobra).
  `gt nudge` currently has no subcommands (it's a direct command). Adding
  subcommands requires converting it to a parent or using hidden commands.
- **Cobra limitation**: A cobra `Command` can either have a `RunE` (direct
  execution) OR subcommands (routing), but not both seamlessly. The existing
  `nudgeCmd` has `RunE: runNudge`. To add `gt nudge gc` and `gt nudge status`,
  we need to handle this.
- **Config lives in `operational.nudge`**: The `NudgeThresholds` struct in
  `internal/config/types.go` is the canonical location for nudge-related
  thresholds. GC thresholds belong here.
- **Daemon patrol config is separate**: `DaemonPatrolConfig` (in
  `mayor/daemon.json`) controls which dogs are enabled and their intervals.
  The GC dog interval goes here, not in `NudgeThresholds`.

### Options Explored

#### Option 1: Subcommands Under `gt nudge` (gc, status)

- **Description**: Add `gt nudge gc` and `gt nudge status` as subcommands.
  The existing `gt nudge <target> "message"` behavior stays as the default
  (when no subcommand matches). Uses cobra's `TraverseChildren` or
  `Args: cobra.ArbitraryArgs` to distinguish "gt nudge gc" (subcommand)
  from "gt nudge gastown/alpha" (positional target).
- **Pros**:
  - Logical grouping: all nudge operations under `gt nudge`
  - Discoverable via `gt nudge --help`
  - Matches UX analysis recommendation
  - Follows `gt dolt status` / `gt dolt cleanup` precedent
- **Cons**:
  - Cobra routing ambiguity: "gc" and "status" become reserved words that
    cannot be used as nudge target names (unlikely conflict)
  - Requires care with existing `Args: cobra.RangeArgs(1, 2)` on nudgeCmd
- **Effort**: Low (cobra handles subcommand routing natively; just don't
  conflict with existing positional args)

#### Option 2: Separate Top-Level Commands (`gt nudge-gc`, `gt nudge-status`)

- **Description**: Add new top-level commands rather than subcommands.
- **Pros**:
  - No change to existing `gt nudge` command
  - Zero risk of breaking existing behavior
- **Cons**:
  - Pollutes the top-level command space
  - Not discoverable from `gt nudge --help`
  - Inconsistent with `gt dolt status` pattern
  - Operators won't find them without knowing they exist
- **Effort**: Low

#### Option 3: `gt queue gc` / `gt queue status` (New Parent Command)

- **Description**: Create a new `gt queue` parent command for queue management
  across all queue types (nudge, mail, etc.).
- **Pros**:
  - Future-proof if other queue types need management
  - Clean separation from `gt nudge` (send) vs `gt queue` (manage)
- **Cons**:
  - Only one queue type exists (nudge); premature abstraction
  - Unfamiliar concept for operators
  - "Which command manages nudges?" becomes ambiguous
- **Effort**: Medium

#### Option 4: Flags on Existing `gt nudge` Command

- **Description**: Add `--gc` and `--status` flags to the existing command.
  `gt nudge --gc` triggers eviction; `gt nudge --status` shows queue health.
- **Pros**:
  - No subcommand routing changes
  - Simple implementation
- **Cons**:
  - Flag-driven mode switching is an anti-pattern in CLI design
  - `gt nudge --gc --dry-run` reads awkwardly
  - Not discoverable: users expect `--help` to show flags, not modes
  - Violates cobra idioms (actions should be verbs/nouns, not flags)
- **Effort**: Low but poor ergonomics

### Recommendation

**Option 1: Subcommands under `gt nudge`** — this is the natural home for these
operations and matches the UX analysis recommendation. The cobra routing concern
is solvable: add subcommands as hidden aliases that take priority, or restructure
`nudgeCmd` to use `RunE` only when no subcommand matches (cobra's default
behavior when `TraverseChildren` is enabled).

Implementation approach: Keep `nudgeCmd` with `RunE: runNudge` but add
subcommands. Cobra routes to subcommands first — `gt nudge gc` hits `gcCmd`,
while `gt nudge gastown/alpha "msg"` falls through to `runNudge`. This works
because "gc" and "status" are not valid nudge targets (they're not
`<rig>/<name>` addresses).

## Proposed API Design

### Layer 1: Go Package API (`internal/nudge/`)

```go
// --- New file: internal/nudge/evict.go ---

// EvictResult summarizes a single eviction run.
type EvictResult struct {
    EvictedFiles    int           // Expired .json files removed
    PrunedDirs      int           // Empty session directories removed
    SkippedLive     int           // Non-expired nudges left in place
    SkippedClaimed  int           // In-flight .claimed files left alone
    BytesFreed      int64         // Approximate disk freed
    Duration        time.Duration // Wall time of the eviction run
    Errors          []string      // Non-fatal errors encountered
}

// QueueStats provides a snapshot of nudge queue health.
type QueueStats struct {
    TotalSessions   int   // Directories under nudge_queue/
    ActiveSessions  int   // Sessions with a live tmux session
    DeadSessions    int   // Sessions with no live tmux session
    TotalFiles      int   // All .json files across all sessions
    ExpiredFiles    int   // Files past their ExpiresAt
    LiveFiles       int   // Non-expired files
    ClaimedFiles    int   // In-flight .claimed files
    TotalBytes      int64 // Approximate total disk usage
}

// SessionQueueInfo describes a single session's queue state.
type SessionQueueInfo struct {
    Session     string // Session directory name
    IsAlive     bool   // tmux session exists
    Pending     int    // .json file count
    Expired     int    // Files past ExpiresAt
    Live        int    // Non-expired files
    OldestAge   time.Duration // Age of oldest file
}

// EvictExpired removes all expired nudge files across all session queues.
// It only removes .json files whose ExpiresAt is in the past. It does not
// touch .claimed files (active drainers) or non-expired nudges.
//
// If pruneDirs is true, also removes empty queue directories whose tmux
// session no longer exists. Live session dirs are never removed even if empty.
//
// The sessionChecker function should return true if the tmux session exists.
// Pass nil to skip directory pruning entirely.
//
// This function is safe to call concurrently with Drain and Enqueue:
// - It only removes .json files (not .claimed) so active Drain claims are safe.
// - It reads ExpiresAt from file content, not filename, so clock skew is bounded.
// - Files removed between read and remove produce a benign ENOENT (ignored).
func EvictExpired(townRoot string, pruneDirs bool, sessionChecker func(string) bool) (*EvictResult, error)

// EvictExpiredDryRun performs the same scan as EvictExpired but makes no
// changes. Returns what *would* be evicted. Used by `gt nudge gc --dry-run`.
func EvictExpiredDryRun(townRoot string, sessionChecker func(string) bool) (*EvictResult, error)

// Stats returns a QueueStats snapshot for all nudge queues.
// The sessionChecker function should return true if the tmux session exists.
func Stats(townRoot string, sessionChecker func(string) bool) (*QueueStats, error)

// SessionStats returns per-session queue information for all sessions,
// sorted by expired count (highest first). Used by `gt nudge status --verbose`.
func SessionStats(townRoot string, sessionChecker func(string) bool) ([]SessionQueueInfo, error)
```

**Design rationale:**
- `sessionChecker func(string) bool` — dependency injection avoids importing
  `tmux` from `internal/nudge` (which would create a cycle). The daemon
  dog passes its `sessionChecker` interface's `HasSession` method; the CLI
  builds a closure from `tmux.NewTmux().HasSession`.
- Separate `EvictExpired` and `EvictExpiredDryRun` — avoids a boolean parameter
  for dry-run mode (which is a Go anti-pattern per the proverb "accept
  interfaces, not booleans"). The dry-run variant can be implemented in terms
  of the same scan loop with a no-op delete.
- `EvictResult` returns all metrics the CLI needs for formatted output without
  requiring the CLI to re-scan.
- Error handling: non-fatal per-file errors (ENOENT races, permission issues)
  are collected in `EvictResult.Errors`; the function only returns a hard error
  if the queue root directory cannot be read.

### Layer 2: CLI Commands (`internal/cmd/`)

#### `gt nudge gc`

```go
// --- New file: internal/cmd/nudge_gc.go ---

var nudgeGCCmd = &cobra.Command{
    Use:   "gc",
    Short: "Evict expired nudges from all queues",
    Long: `Remove expired nudge files from all session queue directories.

Only removes files past their ExpiresAt timestamp — live nudges are
never touched. Optionally prunes empty directories for dead sessions.

The daemon patrol runs this automatically every 5 minutes. Use this
command for immediate cleanup or debugging.

Examples:
  gt nudge gc --dry-run    # Preview what would be evicted
  gt nudge gc              # Execute eviction
  gt nudge gc --session gt-gastown-alpha  # Single session only`,
    RunE: runNudgeGC,
}

// Flags:
//   --dry-run       Preview without deleting (default: false)
//   --session <s>   Only process a specific session directory
//   --no-prune      Don't remove empty dead-session directories
//   --json          Output as JSON (for scripting)
```

**Output format (text mode):**
```
$ gt nudge gc --dry-run

○ Nudge GC (dry run)

  Would evict:   131 expired nudges across 113 sessions
  Would prune:   108 empty dead-session directories
  Live nudges:   7 (untouched)
  Estimated:     ~65KB freed

  Run without --dry-run to execute.

$ gt nudge gc

✓ Nudge GC complete

  Evicted:   131 expired nudges
  Pruned:    108 empty directories
  Freed:     ~65KB
  Duration:  5ms
```

**Output format (JSON mode):**
```json
{
  "evicted_files": 131,
  "pruned_dirs": 108,
  "skipped_live": 7,
  "skipped_claimed": 0,
  "bytes_freed": 66560,
  "duration_ms": 5,
  "errors": []
}
```

#### `gt nudge status`

```go
// --- New file: internal/cmd/nudge_status.go ---

var nudgeStatusCmd = &cobra.Command{
    Use:   "status [session]",
    Short: "Show nudge queue health",
    Long: `Display nudge queue health metrics.

Without arguments, shows an aggregate summary across all sessions.
With a session name argument, shows detailed per-session state.

Examples:
  gt nudge status                        # Aggregate overview
  gt nudge status gt-gastown-alpha       # Single session detail
  gt nudge status --verbose              # Per-session breakdown
  gt nudge status --json                 # JSON output for scripting`,
    Args: cobra.MaximumNArgs(1),
    RunE: runNudgeStatus,
}

// Flags:
//   --verbose       Show per-session breakdown (sorted by expired count)
//   --json          Output as JSON
```

**Output format (aggregate):**
```
$ gt nudge status

● Nudge Queue Health

  Sessions:    121 total (8 active, 113 dead)
  Pending:     138 files (131 expired, 7 live)
  In-flight:   0 claimed
  Disk:        ~70KB
  Last GC:     2m ago (patrol: nudge_gc_dog)

  Active sessions with pending nudges:
    gt-gastown-witness:   2 pending (0 expired)
    gt-gastown-fury:      1 pending (0 expired)
```

**Output format (single session):**
```
$ gt nudge status gt-gastown-alpha

● Session: gt-gastown-alpha

  Status:      dead (no tmux session)
  Pending:     5 files (5 expired, 0 live)
  Oldest:      3h2m ago
  Claimed:     0

  Expired nudges:
    [2h30m ago] from witness: "Check your hook"
    [2h15m ago] from deacon: "dispatch: gu-xyz"
    ...
```

#### Command Registration

```go
// In internal/cmd/nudge.go init():
func init() {
    rootCmd.AddCommand(nudgeCmd)
    nudgeCmd.AddCommand(nudgeGCCmd)
    nudgeCmd.AddCommand(nudgeStatusCmd)
    // ... existing flag registration ...
}
```

Cobra's routing ensures `gt nudge gc` hits `nudgeGCCmd` and
`gt nudge gastown/alpha "msg"` hits `nudgeCmd.RunE` (since "gastown/alpha"
doesn't match any subcommand name).

### Layer 3: Configuration Interface

#### NudgeThresholds Extension (operational config)

```go
// In internal/config/types.go, add to NudgeThresholds:
type NudgeThresholds struct {
    // ... existing fields ...

    // GCGracePeriod is how long past ExpiresAt a file must be before eviction.
    // Provides clock-skew tolerance. Default "0s" (evict immediately on expiry).
    GCGracePeriod string `json:"gc_grace_period,omitempty"`

    // GCMaxDirAge is how long a dead-session directory must be untouched
    // before it's eligible for pruning. Default "1h".
    // Prevents pruning a directory for a session that just died and might
    // be restarted (session IDs can be reused within a few minutes).
    GCMaxDirAge string `json:"gc_max_dir_age,omitempty"`
}
```

**Config file path**: `~/gt/settings/config.json` under `operational.nudge`

```json
{
  "operational": {
    "nudge": {
      "normal_ttl": "30m",
      "urgent_ttl": "2h",
      "max_queue_depth": 50,
      "gc_grace_period": "0s",
      "gc_max_dir_age": "1h"
    }
  }
}
```

#### DaemonPatrolConfig Extension

```go
// In internal/daemon/ config types, add:
type NudgeGCDogConfig struct {
    // Enabled toggles the patrol. Default: true (unlike most dogs which default off).
    Enabled *bool `json:"enabled,omitempty"`

    // IntervalStr is how often the GC runs. Default "5m".
    IntervalStr string `json:"interval,omitempty"`
}
```

**Config file path**: `~/gt/mayor/daemon.json`

```json
{
  "patrols": {
    "nudge_gc_dog": {
      "enabled": true,
      "interval": "5m"
    }
  }
}
```

**Design choice: enabled by default.** Unlike `poller_dog` (opt-in because it
launches processes), the GC dog only removes expired files. It's purely
beneficial and the cost is negligible (~5ms/cycle). Operators can disable if
they want to preserve expired nudges for forensic analysis.

### Layer 4: Error Messages and Help Text

#### Error Messages Follow the Three-Part Pattern

| Scenario | Message |
|----------|---------|
| GC on non-town directory | `Error: gt nudge gc requires a Gas Town workspace (run from within ~/gt/)` |
| Status for nonexistent session | `⚠ Session "gt-gastown-xyz" not found in nudge queue directory` |
| Permission denied on file | `Warning: skipped 2 files (permission denied) — check filesystem permissions` |
| Queue root missing | `● No nudge queues found (directory .runtime/nudge_queue/ does not exist)` |

#### Help Text Hierarchy

```
$ gt nudge --help

Send nudges to Gas Town workers and manage nudge queues.

Usage:
  gt nudge <target> [message] [flags]
  gt nudge [command]

Sending:
  gt nudge <target> "message"     Send a nudge to a worker
  gt nudge <target> -m "message"  Same, using --message flag

Available Commands:
  gc          Evict expired nudges from all queues
  status      Show nudge queue health

Flags:
  -m, --message string   Message to send
  -f, --force            Send even if target has DND enabled
      --mode string      Delivery mode: wait-idle, queue, immediate (default "wait-idle")
      --priority string  Queue priority: normal, urgent (default "normal")
  -h, --help             help for nudge

Use "gt nudge [command] --help" for more information about a command.
```

### Naming Conventions and Consistency

| Concept | Name | Rationale |
|---------|------|-----------|
| Removing expired files | "evict" (Go API), "gc" (CLI) | "Evict" is precise (removing expired entries); "gc" is universally understood CLI shorthand for garbage collection |
| Removing empty dirs | "prune" | Standard term (git prune, docker prune) |
| Queue health snapshot | "status" | Matches `gt dolt status`, `git status` |
| Background cleaner | "nudge_gc_dog" | Follows `poller_dog`, `doctor_dog` naming |
| Config section | `operational.nudge.gc_*` | Groups with existing nudge config |

### Discoverability

**How will users discover this feature?**

1. **`gt nudge --help`** — Shows `gc` and `status` subcommands
2. **`gt doctor`** — Nudge health check warns when expired nudges accumulate,
   with "Fix: gt nudge gc" hint
3. **Daemon patrol digest** — GC activity appears as one line in patrol reports
4. **Error messages** — When queue is full, error message mentions expiry/GC

**What's the happy path?**
- Operator never thinks about it (daemon handles everything)
- If investigating: `gt nudge status` → `gt nudge gc --dry-run` → `gt nudge gc`

**Edge cases:**
- Session name conflicts with subcommand names: "gc" and "status" are not valid
  `<rig>/<name>` addresses (no `/`), so there's no ambiguity
- Running GC during active drain: safe — EvictExpired skips .claimed files
- GC with clock skew: `gc_grace_period` config provides tolerance buffer

## Constraints Identified

1. **No import cycles**: `internal/nudge` cannot import `internal/tmux` or
   `internal/daemon`. Session-alive checking must be injected via function
   parameter (already shown in API design).

2. **Cobra routing**: Adding subcommands to a command with `RunE` requires
   that subcommand names don't collide with positional argument values.
   "gc" and "status" are safe (not valid nudge targets).

3. **Backwards compatibility**: Existing `gt nudge <target> "msg"` syntax must
   work unchanged. No existing flags or behaviors may change.

4. **No new env vars**: The feature uses the existing config infrastructure
   (JSON files). Env vars are reserved for test hooks only.

5. **Daemon config is per-rig**: `mayor/daemon.json` is rig-specific. The GC
   dog runs from the daemon regardless of which rig's queues it's cleaning
   (all queues live under a shared `.runtime/` path).

6. **File operations must be ENOENT-tolerant**: Between listing files and
   removing them, active drainers may claim/remove them. All file operation
   errors where `os.IsNotExist(err)` should be silently ignored.

## Open Questions

1. **Should `gt nudge status` show last GC timestamp?** This requires either
   writing a timestamp file after each GC run or parsing daemon logs. The
   timestamp file is simpler (write `.runtime/nudge_queue/.last_gc` with the
   Unix timestamp). Low cost, high debugging value. **Recommendation: yes.**

2. **Should the daemon GC dog log every evicted file, or just the summary?**
   At DEBUG level, logging individual files helps trace "where did my nudge
   go?" problems. At INFO level, only the summary (X evicted, Y pruned).
   **Recommendation: summary at INFO, per-file at DEBUG.**

3. **Should `EvictExpired` also clean orphaned `.claimed` files?** The existing
   `Drain()` function already sweeps stale claims (>5m old). If the GC dog
   also sweeps them, there could be a brief race. However, stale claims from
   dead sessions will never be swept by Drain (no one calls Drain for dead
   sessions). **Recommendation: yes, GC should also sweep stale claims for
   dead sessions only.** This mirrors the existing Drain sweep logic but
   applies it globally.

4. **Should `gt nudge gc --session` accept a rig/polecat address (like
   `gastown/alpha`) or a raw session name (like `gt-gastown-alpha`)?**
   The address form is more ergonomic; the raw form is more precise.
   **Recommendation: accept both** — try address resolution first, fall back
   to literal session directory name.

## Integration Points

### -> Daemon Patrol (`internal/daemon/nudge_gc_dog.go`)
- New file following `poller_dog.go` template
- Consumes `nudge.EvictExpired()` with a `sessionChecker` from the daemon's
  tmux handle
- Reports `EvictResult` to daemon telemetry/patrol digest
- Writes `.runtime/nudge_queue/.last_gc` timestamp

### -> CLI Commands (`internal/cmd/nudge_gc.go`, `nudge_status.go`)
- Consumes `nudge.EvictExpired()` / `nudge.Stats()` / `nudge.SessionStats()`
- Builds `sessionChecker` closure from `tmux.NewTmux().HasSession`
- Formats output using existing `style` package (✓, ⚠, ○ prefixes)
- Supports `--json` via `encoding/json` marshaling of result types

### -> Config (`internal/config/types.go`, `operational.go`)
- Extends `NudgeThresholds` with `GCGracePeriod` and `GCMaxDirAge`
- Adds accessor methods (`GCGracePeriodD()`, `GCMaxDirAgeD()`) following
  existing pattern (`NormalTTLD()`, `UrgentTTLD()`, etc.)

### -> Doctor System (`internal/doctor/`)
- New health check: `nudge_queue_check.go`
- Calls `nudge.Stats()` and reports WARN if expired > 50 AND no recent GC

### -> Telemetry (`internal/telemetry/`)
- `RecordNudgeGC(ctx, result *nudge.EvictResult)` for observability
- Counter: `gastown.nudge.gc.evicted_total`
- Histogram: `gastown.nudge.gc.duration_ms`

### -> UX Dimension
- CLI output format follows UX analysis recommendations exactly
- Progressive disclosure: aggregate → verbose → per-session → JSON
- Error messages follow three-part pattern (what, why, what-to-do)

### -> Security Dimension
- `EvictExpired` only removes files past `ExpiresAt` (immutable once written)
- No symlink following: uses `os.ReadDir` (does not follow symlinks to entries)
- Session name sanitization preserved (same `queueDir()` function)
- Dead-session detection is defense-in-depth (prune dirs, not required for
  correctness — expired files are safe to remove regardless of session liveness)

### -> Scalability Dimension
- O(sessions × files_per_session) for full scan — bounded by MaxQueueDepth
- At current scale: <5ms per GC cycle
- At 10x scale: <50ms per GC cycle (still negligible)
- `Stats()` uses readdir+stat without reading file contents for the fast path
- Per-session detail (`SessionStats`) reads file contents — only for verbose mode

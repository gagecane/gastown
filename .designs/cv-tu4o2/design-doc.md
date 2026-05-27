# Design: Nudge Queue Expiry Eviction

## Executive Summary

Nudges are agent-to-agent coordination messages stored as JSON files under
`<townRoot>/.runtime/nudge_queue/<session>/*.json`. Every nudge has an
`ExpiresAt` timestamp set at enqueue time (30m for normal, 2h for urgent), and
the existing `Drain()` function already discards expired nudges at delivery
time. The bug is operational, not algorithmic: nudges queued for **dead
sessions** (polecats that exited without draining) are never delivered, so
their `Drain()` is never called, so they accumulate on disk forever. As of
analysis time, the production town has 121 session directories holding 138
nudge files — 131 of them already past TTL.

The proposed design adds a single new daemon patrol dog, `nudge_gc_dog`,
that walks all queue directories every 5 minutes, removes files whose
`ExpiresAt` is in the past, and prunes empty directories whose tmux session
no longer exists. The eviction logic is exposed as pure functions in
`internal/nudge/` so a new `gt nudge gc` CLI command can invoke the same
code path for manual operator intervention. A companion `gt nudge status`
command surfaces queue health, and a doctor check warns when expired
nudges accumulate despite the patrol running. No schema changes, no
changes to the `Enqueue()`/`Drain()` hot paths, no network exposure.

The design is conservative on scope (one ticker, one CLI noun with two
verbs) and aggressive on safety: only files past `ExpiresAt` are removed,
`.claimed` files are skipped, symlinks are rejected via `os.Lstat`, and
file-not-found races are tolerated as expected. Performance budget is
~5ms per GC cycle at current scale and ~50ms at 10× scale — negligible
compared to existing daemon dogs.

## Problem Statement

Polecats die. They die from `gt done`, from SIGKILL, from context
exhaustion, from session crashes, from worktree teardown. When a polecat
dies, any nudges queued for it that have not yet been delivered remain on
disk in `.runtime/nudge_queue/<session>/`. Because nudges are only
discarded by `Drain()` at hook turn boundaries, and dead sessions never
fire hooks, these orphaned nudges live forever.

Concrete symptoms:
- 121 session directories on a single dev town today, 113 of which have
  no live tmux session.
- 138 `.json` files total, 131 of them past the 2-hour urgent TTL.
- Linear growth: ~10–20 polecat cycles/day × ~3 nudges/cycle-death =
  30–100 new orphan files per day with no upper bound.
- Operators have no visibility into queue health (no `gt nudge status`).
- No automated cleanup mechanism exists.

This is not a security bug or correctness bug — orphaned nudges cause
no agent failures. But it is a steadily growing operational debt: disk
usage creeps up, `readdir` over the queue root slows over time, and
debugging "where did my nudge go?" requires raw filesystem inspection.

## Proposed Design

### Overview

A new daemon patrol dog (`nudge_gc_dog`) ticks every 5 minutes and runs
`nudge.EvictExpired(townRoot, pruneDirs=true, sessionChecker)`. The
function walks every session directory under `.runtime/nudge_queue/`,
opens each `.json` file, unmarshals it, and removes the file if
`ExpiresAt < time.Now()`. After processing files, it removes session
directories that are empty AND whose tmux session no longer exists (per
`sessionChecker`).

The same `nudge.EvictExpired` is callable from the CLI via `gt nudge
gc`, which adds operator-facing flags (`--dry-run`, `--session`,
`--no-prune`, `--json`). A companion `gt nudge status` command renders
queue health by calling `nudge.Stats()` (aggregate) or
`nudge.SessionStats()` (verbose / per-session).

The existing `Drain()` and `Enqueue()` hot paths are not modified.
Active drainers continue to use rename-then-process claim semantics; the
GC sweep skips `.claimed` files entirely and tolerates ENOENT races on
`.json` files that an active drainer claims between our readdir and our
remove.

### Key Components

| Component | New / Existing | Role |
|---|---|---|
| `internal/nudge/evict.go` | New | Pure-function eviction primitives: `EvictExpired`, `EvictExpiredDryRun`, `Stats`, `SessionStats` |
| `internal/nudge/queue.go` | Existing — unchanged | `Enqueue`, `Drain`, `QueueLen`, etc. |
| `internal/daemon/nudge_gc_dog.go` | New | Daemon patrol following `poller_dog.go` template |
| `internal/daemon/daemon.go` | Edited | New ticker case in select loop, gated by `d.isPatrolActive("nudge_gc")` |
| `internal/daemon/types.go` | Edited | Add `NudgeGC *NudgeGCDogConfig` to `DaemonPatrolConfig.Patrols` |
| `internal/cmd/nudge_gc.go` | New | `gt nudge gc` subcommand |
| `internal/cmd/nudge_status.go` | New | `gt nudge status` subcommand |
| `internal/cmd/nudge.go` | Edited | Register new subcommands; update help text |
| `internal/config/types.go` | Edited | Add `GCGracePeriod`, `GCMaxDirAge` to `NudgeThresholds` |
| `internal/config/operational.go` | Edited | Accessor methods for new fields |
| `internal/doctor/nudge_queue_check.go` | New | Doctor health check for queue hygiene |

### Interface

#### CLI

```
gt nudge gc                  # Evict expired nudges across all sessions
gt nudge gc --dry-run        # Preview only — no deletion
gt nudge gc --session <s>    # Evict only the named session
gt nudge gc --no-prune       # Skip empty-directory pruning
gt nudge gc --json           # Emit JSON output for scripting

gt nudge status              # Aggregate queue health
gt nudge status <session>    # Per-session detail
gt nudge status --verbose    # Per-session breakdown for all sessions
gt nudge status --json       # JSON output
```

The existing `gt nudge <target> "msg"` send command is unchanged — `gc`
and `status` are not valid `<rig>/<name>` addresses, so cobra's
subcommand-first routing handles disambiguation.

#### Go Package API

```go
package nudge

// EvictResult summarizes a single eviction run.
type EvictResult struct {
    EvictedFiles   int           // Expired .json files removed
    PrunedDirs     int           // Empty session directories removed
    SkippedLive    int           // Non-expired nudges left in place
    SkippedClaimed int           // .claimed files left alone
    BytesFreed     int64         // Approximate disk freed
    Duration       time.Duration // Wall time of the eviction run
    Errors         []string      // Non-fatal per-file errors (ENOENT races, etc.)
}

// QueueStats provides a snapshot of nudge queue health.
type QueueStats struct {
    TotalSessions  int
    ActiveSessions int
    DeadSessions   int
    TotalFiles     int
    ExpiredFiles   int
    LiveFiles      int
    ClaimedFiles   int
    TotalBytes     int64
    LastGCAt       time.Time // From .runtime/nudge_queue/.last_gc, zero if absent
}

// SessionQueueInfo describes a single session's queue state.
type SessionQueueInfo struct {
    Session   string
    IsAlive   bool
    Pending   int
    Expired   int
    Live      int
    OldestAge time.Duration
}

// EvictExpired removes all expired nudge files across all session queues.
// pruneDirs=true also removes empty queue directories whose tmux session is dead.
// sessionChecker(sessionName) reports whether the tmux session is alive; pass
// nil to skip directory pruning entirely.
//
// Concurrency: safe to call alongside Drain and Enqueue. Only .json files are
// removed (not .claimed); ExpiresAt is read from file content (not filename);
// ENOENT during remove is benign and ignored.
func EvictExpired(townRoot string, pruneDirs bool,
    sessionChecker func(string) bool) (*EvictResult, error)

// EvictExpiredDryRun performs the same scan as EvictExpired but makes no
// filesystem changes. Used by `gt nudge gc --dry-run`.
func EvictExpiredDryRun(townRoot string,
    sessionChecker func(string) bool) (*EvictResult, error)

// Stats returns an aggregate snapshot of all nudge queues.
func Stats(townRoot string,
    sessionChecker func(string) bool) (*QueueStats, error)

// SessionStats returns per-session queue information, sorted by expired
// count descending. Used by `gt nudge status --verbose`.
func SessionStats(townRoot string,
    sessionChecker func(string) bool) ([]SessionQueueInfo, error)
```

The `sessionChecker func(string) bool` parameter is dependency injection
to avoid an `internal/nudge` → `internal/tmux` import cycle. The daemon
passes its existing tmux handle's `HasSession`; the CLI builds a closure
from `tmux.NewTmux().HasSession`; tests pass a stub.

### Data Model

**No schema changes.** The eviction operates entirely on existing
`QueuedNudge` JSON files using the existing `ExpiresAt` field.

```go
// internal/nudge/queue.go (existing — for reference, not modified)
type QueuedNudge struct {
    Sender          string    `json:"sender"`
    Message         string    `json:"message"`
    Priority        string    `json:"priority"`
    Kind            string    `json:"kind,omitempty"`
    ThreadID        string    `json:"thread_id,omitempty"`
    Severity        string    `json:"severity,omitempty"`
    Timestamp       time.Time `json:"timestamp"`
    ExpiresAt       time.Time `json:"expires_at,omitempty"`
    DeliverAfter    time.Time `json:"deliver_after,omitempty"`
    OriginalFrom    string    `json:"original_from,omitempty"`
    OriginalSubject string    `json:"original_subject,omitempty"`
}
```

Storage layout (existing):
```
<townRoot>/.runtime/nudge_queue/
├── gt-witness/
│   ├── 1777375888899006192-418f3fd7.json
│   └── 1777375889084083168-d1ff62cb.json
├── gt-polecat-alpha-abc123/
│   └── (empty — session dead)
└── .last_gc                                # NEW: timestamp file written after each GC
```

The only new on-disk artifact is `.last_gc`, a tiny file containing the
Unix timestamp of the most recent successful GC run (used by `gt nudge
status` to show "Last GC: 2m ago").

#### Configuration Extensions

```go
// internal/config/types.go — extend existing struct, do not rename
type NudgeThresholds struct {
    // ... existing fields (NormalTTL, UrgentTTL, MaxQueueDepth, etc.) ...

    // GCGracePeriod is how long past ExpiresAt a file must be before eviction.
    // Provides clock-skew tolerance. Default "0s".
    GCGracePeriod string `json:"gc_grace_period,omitempty"`

    // GCMaxDirAge is how long a dead-session directory must be untouched
    // before it's eligible for pruning. Default "1h". Prevents pruning a
    // directory for a session that just died and might be respawned.
    GCMaxDirAge string `json:"gc_max_dir_age,omitempty"`
}

// internal/daemon/types.go
type NudgeGCDogConfig struct {
    Enabled     *bool  `json:"enabled,omitempty"` // Default: true
    IntervalStr string `json:"interval,omitempty"` // Default: "5m"
}
```

`mayor/daemon.json` config sample:
```json
{
  "patrols": {
    "nudge_gc": {
      "enabled": true,
      "interval": "5m"
    }
  }
}
```

Unlike `poller_dog` (opt-in because it spawns processes), `nudge_gc_dog`
is opt-in **on by default**. It only removes expired files, the cost is
~5ms/cycle, and operators benefit immediately. Disable only if preserving
expired nudges for forensic analysis.

## Trade-offs and Decisions

### Decisions Made

1. **Daemon patrol dog (Option A from integration analysis), not
   per-session poller integration.** A single town-wide patrol cleanly
   handles dead sessions; per-session pollers can't help with sessions
   that have no poller. Security analysis suggested combining both
   (Options A+B); the synthesis prefers a single mechanism for
   simplicity, with the recognition that adding poller-level cleanup
   later is non-breaking.

2. **Pure file-level eviction (Option 1 from data analysis), not
   filename-based heuristic.** Reading + unmarshaling each `.json`
   respects custom `ExpiresAt`, deferred nudges, and priority-specific
   TTLs. Performance impact is negligible (<5ms at current scale).

3. **Subcommands under `gt nudge` (Option 1 from API analysis), not new
   top-level commands.** Matches the `gt dolt status` / `gt dolt
   cleanup` precedent. Discoverable via `gt nudge --help`. Cobra's
   subcommand-first routing handles disambiguation since "gc" and
   "status" are not valid `<rig>/<name>` addresses.

4. **Fixed 5-minute interval (Option 1 from scale analysis), not
   event-driven or tiered.** Math shows the simple ticker approach
   handles 1000× current scale. fsnotify hits inotify limits at high
   scale, and tiered GC is YAGNI until session count exceeds ~5000.

5. **Enabled by default in daemon config.** Unlike `poller_dog`, the GC
   dog is purely beneficial — it only deletes files past their TTL and
   has no side effects on running sessions.

6. **Per-file unmarshal, not partial-field unmarshal.** A nudge file is
   ~500 bytes; reading the whole struct is no slower than a streaming
   parser and avoids two parse paths. Optimization is YAGNI.

7. **`.last_gc` timestamp file.** Cheap (one write per cycle) and
   answers "did the GC run recently?" without log diving. Recommended
   by API and UX dimensions.

8. **Two grace periods to bound directory pruning:** `GCMaxDirAge`
   ensures we don't prune a directory for a session that just died and
   may respawn. `sessionChecker` is the primary signal; `GCMaxDirAge`
   is defense in depth. Both must be true to prune.

9. **Stale `.claimed` files for dead sessions are eligible for sweep.**
   Existing `Drain()` already sweeps stale claims for live sessions
   (>5min old). For dead sessions, claims will never be released —
   sweep them with the same threshold.

10. **Doctor integration as a separate health check, not embedded in
    daemon logs.** Makes nudge queue health a first-class system metric
    and surfaces problems even when operators aren't reading patrol
    digests.

### Open Questions (Need Human Input)

1. **`.last_gc` file location.** API dimension proposes
   `.runtime/nudge_queue/.last_gc` (sibling to session dirs). This is
   fine but means listing the queue root must filter dot-prefixed
   entries. Alternative: `.runtime/nudge_gc/last_run` (separate
   directory). **Recommendation: `.runtime/nudge_queue/.last_gc`** —
   colocated with the data it describes; the readdir filter is one line.
   Confirm before implementation.

2. **Default for `GCMaxDirAge`.** API dimension proposes 1h (covers
   "session about to respawn" window). Scale dimension is silent.
   Security dimension implies "any duration past ExpiresAt is fine."
   **Recommendation: 1h.** This is the only knob most operators will
   touch. Confirm.

3. **Should `gt nudge gc --session` accept a rig/polecat address
   (`gastown/alpha`) or a raw session name (`gt-gastown-alpha`)?** API
   dimension recommends accepting both. **Recommendation: accept both
   — try address resolution first via existing `addr.Resolve`, fall
   back to literal session directory name.** Confirm.

4. **How verbose should the daemon log be per GC cycle?** Per-file at
   DEBUG, summary at INFO is the consensus across dimensions. **Confirm
   the log line format**: `"nudge_gc: evicted=131 pruned=108
   skipped_live=7 freed=66560B duration=5ms"`.

5. **Should the doctor check threshold be hardcoded or configurable?**
   UX dimension suggests "warn if expired > 50 AND last_gc_age > 10min."
   **Recommendation: hardcoded for v1** (simpler, easier to tune from
   real data); add config knob in v2 if needed.

6. **Do we need a `--config` flag on `gt nudge gc` to override the
   grace period for one-off cleanup?** Operators investigating
   incidents may want `gt nudge gc --grace=24h`. **Recommendation:
   defer to v2** — current `--dry-run` already lets operators preview
   what would be removed without grace tuning.

### Trade-offs

| What we trade | What we get | Why this trade |
|---|---|---|
| Up to 5 minutes of orphan persistence | Simple, predictable resource usage | Orphans cause no functional harm; immediacy is unnecessary |
| Reading every `.json` file every cycle | 100% accuracy on expiry | <5ms cost is negligible; lossy heuristics are not worth it |
| One more daemon ticker | Centralized cleanup that covers dead sessions | Per-session cleanup can never address sessions that already exited |
| Two new CLI subcommands | Operator visibility and manual control | Pure invisibility (Option 1 in UX analysis) makes debugging painful |
| Daemon log volume +1 line/5min | Observable patrol activity | Already standard for poller_dog/doctor_dog |
| New sessionChecker dependency injection in API | No import cycle nudge↔tmux | Closures are cheap; cycle would be a bigger problem |

## Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| GC removes a non-expired nudge (bug) | Low | High — silent data loss | Strict `ExpiresAt < now - GCGracePeriod` check; unit tests on boundary cases; TOCTOU-tolerant remove |
| GC races with active Drain on a respawned session | Low | Low | Skip `.claimed` files; tolerate ENOENT on remove; rely on `sessionChecker` for directory prune (not file delete) |
| Symlink attack via crafted session name | Low (single-user) | Medium | `os.Lstat` + `mode.IsRegular()` before any remove; existing `queueDir()` already collapses `/` to `_` |
| Clock skew evicts files that should be live | Low | Medium | `GCGracePeriod` config knob (default 0s, can raise to 30s on suspect hosts) |
| Daemon ticker stalls due to slow GC | Very Low at current scale | Medium | Bounded execution time (<50ms at 10× scale); patrol runs in a goroutine, doesn't block the select loop |
| Empty directory pruned for a session about to respawn | Low | Very Low — directory is auto-recreated by `MkdirAll` in `Enqueue` | `GCMaxDirAge` grace period; `sessionChecker` confirms session is dead |
| Disk exhaustion from non-`.json` cruft | Very Low (single-user) | Medium | Future enhancement: also evict non-`.json` files older than 24h with a log line; not in v1 |
| `.runtime/` is on NFS | Environment-specific | High (slow GC) | `.runtime/` is on `/local/home/` by convention; document the requirement |
| Daemon GC dog crashes silently | Low | Medium (orphans accumulate) | Doctor check warns when last_gc_age > 10min and expired count is high; manual `gt nudge gc` works without daemon |

## Implementation Plan

### Phase 1: MVP (single PR, ~250 LOC + tests)

**Goal:** Daemon GC works automatically; CLI provides operator visibility.

1. **`internal/nudge/evict.go` (new)** — Pure functions:
   - `EvictExpired(townRoot, pruneDirs, sessionChecker) (*EvictResult, error)`
   - `EvictExpiredDryRun(townRoot, sessionChecker) (*EvictResult, error)`
   - `Stats(townRoot, sessionChecker) (*QueueStats, error)`
   - `SessionStats(townRoot, sessionChecker) ([]SessionQueueInfo, error)`
   - Internal helpers: `walkSessions`, `evictFile` (ENOENT-tolerant),
     `pruneEmptyDir` (gated on `sessionChecker` + `GCMaxDirAge`).
   - Reuses existing `queueDir()` from `queue.go` for path safety.

2. **`internal/nudge/evict_test.go` (new)** — Unit tests:
   - Boundary: file with `ExpiresAt == time.Now()` is NOT evicted;
     `ExpiresAt < now` IS evicted.
   - Skips `.claimed` files unconditionally.
   - Skips files where `ExpiresAt` is zero (treat as immortal in v1; in
     v2, reconsider per data dimension question).
   - ENOENT during `os.Remove` is silently ignored.
   - `pruneDirs=false` never removes any directory.
   - `sessionChecker` returning true blocks directory removal.
   - `GCMaxDirAge` blocks pruning of a directory whose mtime is recent.
   - Symlink in queue dir is rejected (Lstat check).

3. **`internal/config/types.go` + `operational.go` (edit)** — Add
   `GCGracePeriod`, `GCMaxDirAge` to `NudgeThresholds`. Add accessor
   methods following existing pattern (`NormalTTLD()` etc.).

4. **`internal/daemon/types.go` (edit)** — Add `NudgeGC
   *NudgeGCDogConfig` to `DaemonPatrolConfig.Patrols`.

5. **`internal/daemon/nudge_gc_dog.go` (new)** — Patrol loop:
   - Construct from daemon's tmux handle (closure for sessionChecker).
   - Read interval from config (default 5m).
   - Tick → call `nudge.EvictExpired` → log summary at INFO → write
     `.last_gc` timestamp file → record telemetry counter/histogram.
   - Run a startup sweep on daemon boot (recommended by scale
     dimension) to clear orphans from previous runs.

6. **`internal/daemon/daemon.go` (edit)** — Add new ticker case to
   select loop, gated by `d.isPatrolActive("nudge_gc")`.

7. **`internal/cmd/nudge_gc.go` (new)** — CLI:
   - Flags: `--dry-run`, `--session <s>`, `--no-prune`, `--json`.
   - Resolves `--session` via address resolver, falls back to literal.
   - Builds sessionChecker closure from `tmux.NewTmux().HasSession`.
   - Output: styled text by default, JSON with `--json`.

8. **`internal/cmd/nudge_status.go` (new)** — CLI:
   - Optional positional `[session]` arg for per-session detail.
   - Flags: `--verbose`, `--json`.
   - Reads `.last_gc` for "Last GC" timestamp.
   - Aggregate output by default; `--verbose` shows per-session table.

9. **`internal/cmd/nudge.go` (edit)** — Register new subcommands;
   update help text per UX dimension's recommended `gt nudge --help`.

10. **`internal/doctor/nudge_queue_check.go` (new)** — Doctor check:
    - Calls `nudge.Stats()`.
    - WARN (not ERROR) if `expired_count > 50 AND last_gc_age > 10min`.
    - Hint message: "Fix: gt nudge gc (or wait for daemon patrol)".

11. **`mayor/daemon.json` (edit)** — Default config block:
    `{"patrols": {"nudge_gc": {"enabled": true, "interval": "5m"}}}`.

**Exit criteria:** unit tests pass, daemon GC visibly cleans the
production town's 131 expired files within one cycle of restart, `gt
nudge status` and `gt nudge gc --dry-run` return correct counts.

### Phase 2: Polish (follow-up PRs)

- **Telemetry**: counters `gastown.nudge.gc.evicted_total`,
  `gastown.nudge.gc.pruned_total`, histogram
  `gastown.nudge.gc.duration_ms`. Currently scoped out of MVP because
  the existing telemetry surface for daemon dogs is inconsistent;
  Phase 2 standardizes.
- **Patrol digest line**: `Nudge GC: evicted=N pruned=M last 24h` in
  daily aggregate reports.
- **`gt nudge gc --grace=<duration>`** flag for one-off operator runs.
- **Non-`.json` cruft eviction**: remove non-`.json` files older than
  24h with a log line.

### Phase 3: Future

- **Lazy eviction in `Enqueue`**: when a queue is at depth, sweep that
  session's expired files first to make room. Useful only if backpressure
  becomes common (currently rare).
- **Tiered GC** (Option 4 from scale analysis): only worth it past
  ~5000 session directories. YAGNI for the foreseeable future.
- **Session liveness cache shared with `poller_dog`**: avoids
  duplicate tmux checks on the same tick. Marginal CPU win; revisit if
  daemon startup latency becomes a concern.
- **fsnotify-based GC**: only if scale ever justifies it (>>1000
  active sessions). Inotify watch limits make this hard.

## Appendix: Dimension Analyses

Each dimension was analyzed in depth. See:

- **API & Interface Design** — [`api.md`](./api.md):
  CLI subcommand structure, Go API signatures, configuration shape,
  error messages, help text, naming conventions.
- **Data Model Design** — [`data.md`](./data.md):
  No schema changes; eviction reuses existing `QueuedNudge` and
  `ExpiresAt`; access patterns; lifecycle states.
- **Integration Analysis** — [`integration.md`](./integration.md):
  Daemon patrol dog (Option A) selected over Drain-time / Enqueue-time /
  manual-CLI alternatives; integration points with daemon, config, tmux.
- **Scalability Analysis** — [`scale.md`](./scale.md):
  O(n) walk at 5-minute interval handles 1000× current scale; bounded
  by `MaxQueueDepth = 50`; ~5ms/cycle now, ~5s/cycle at 1000×.
- **Security Analysis** — [`security.md`](./security.md):
  Single-user/local-only trust boundary; symlink and TOCTOU defenses;
  must not delete non-expired files (the #1 invariant).
- **User Experience Analysis** — [`ux.md`](./ux.md):
  "Silent when healthy, informative when investigated, loud when
  broken"; `gt nudge status` + `gt nudge gc` + doctor check are the
  three operator touch points.

## Sources

All inputs are local design artifacts produced by sibling polecats in
the same convoy:

- [`.designs/cv-tu4o2/api.md`](./api.md) — accessed 2026-05-27
- [`.designs/cv-tu4o2/data.md`](./data.md) — accessed 2026-05-27
- [`.designs/cv-tu4o2/integration.md`](./integration.md) — accessed 2026-05-27
- [`.designs/cv-tu4o2/scale.md`](./scale.md) — accessed 2026-05-27
- [`.designs/cv-tu4o2/security.md`](./security.md) — accessed 2026-05-27
- [`.designs/cv-tu4o2/ux.md`](./ux.md) — accessed 2026-05-27

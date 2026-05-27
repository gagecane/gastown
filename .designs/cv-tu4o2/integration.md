# Integration Analysis

## Summary

The nudge queue expiry eviction feature integrates into an already well-structured system. The `internal/nudge` package already assigns `ExpiresAt` on every enqueued nudge and the `Drain()` function already discards expired nudges at drain-time. The gap is that nudges for **dead sessions** (polecats that completed or crashed without draining) accumulate on disk forever — 135 nudge files across 121 session directories on a production town as of today.

The integration path is low-risk: a new daemon patrol dog (following the existing `poller_dog` pattern) periodically walks all nudge queue directories, removes expired `.json` files, and optionally removes empty directories for dead sessions. The existing config infrastructure (`NudgeThresholds`, `OperationalConfig`, `DaemonPatrolConfig`) provides all the knobs needed. No changes to the `Drain()` or `Enqueue()` hot paths are required.

## Analysis

### Key Considerations

- **Existing expiry infrastructure**: Every `QueuedNudge` already has `ExpiresAt` set (30m for normal, 2h for urgent). `Drain()` already discards expired nudges. The gap is only for nudges that are never drained (dead sessions).
- **Session lifecycle**: Polecat sessions die frequently (SIGKILL, context exhaustion, `gt done` cleanup). Their nudge queue directories persist indefinitely.
- **Concurrency safety**: The `Drain()` function uses rename-then-process for claim safety. Any eviction must not race with an active `Drain()` — but dead-session queues by definition have no active drainer.
- **poller_dog precedent**: The existing `poller_dog` patrol already walks `.runtime/nudge_poller/` PID files on a 60s interval. A `nudge_gc_dog` can follow the identical pattern.
- **Configurable via ZFC**: The operational config (`config.NudgeThresholds`) already defines TTLs and thresholds. Adding an eviction interval/age threshold follows the same pattern.
- **Current scale**: 135 files / 121 dirs — manageable now but grows linearly with polecat pool size and nudge frequency.

### Options Explored

#### Option A: Daemon patrol dog (`nudge_gc_dog`)

- **Description**: A new daemon patrol that runs every 5 minutes (configurable), walks all nudge queue directories, removes expired `.json` files, and prunes empty directories whose tmux sessions no longer exist.
- **Pros**:
  - Follows established pattern (poller_dog, quota_dog, doctor_dog)
  - Centralized: runs once regardless of session count
  - Configurable interval via `mayor/daemon.json`
  - Can detect dead sessions via tmux session check (already available via `sessionChecker` interface)
  - Non-invasive: touches no hot paths (Enqueue/Drain)
- **Cons**:
  - Adds another ticker to the daemon select loop
  - Must handle races if a session dies mid-drain (claim files)
- **Effort**: Low (50-80 LOC + test, following poller_dog template)

#### Option B: Drain-time sweep extension

- **Description**: Extend `Drain()` to also scan and remove expired nudges from other sessions' directories while draining.
- **Pros**:
  - No new daemon patrol
  - Piggybacks on existing activity
- **Cons**:
  - Only runs when an active session drains — dead sessions never benefit
  - Adds latency to the hot path (hook response time)
  - Violates separation of concerns (Drain should only handle its own session)
  - Doesn't solve the core problem (dead sessions accumulate forever)
- **Effort**: Medium (more complex, cross-session filesystem access patterns)

#### Option C: `Enqueue`-time sibling eviction

- **Description**: When enqueueing a nudge, check if the target session's queue has expired nudges and remove them first.
- **Pros**:
  - Opportunistic cleanup
  - No new goroutine/ticker
- **Cons**:
  - Only cleans sessions that receive new nudges
  - Adds latency to Enqueue (which blocks the sender)
  - Dead sessions with no new nudges never get cleaned
  - Still needs a separate mechanism for fully abandoned directories
- **Effort**: Low, but incomplete

#### Option D: `gt` CLI subcommand (manual/cron)

- **Description**: Add `gt nudge gc` subcommand that an operator or cron runs.
- **Pros**:
  - No daemon changes
  - Operator-controllable
- **Cons**:
  - Requires remembering to run it
  - Not self-healing
  - Goes against Gas Town's autonomous philosophy
- **Effort**: Low, but poor operational experience

### Recommendation

**Option A: Daemon patrol dog (`nudge_gc_dog`)** — follows the established pattern, addresses the root cause (dead session accumulation), and integrates cleanly with existing infrastructure. Supplement with a `gt nudge gc` CLI command for immediate manual cleanup (Option D as a secondary convenience, not primary mechanism).

## Constraints Identified

1. **Must not interfere with active drainers**: If a session is currently draining (has `.claimed` files), the GC must not remove those files or their queue directory.
2. **Must respect `DeliverAfter` semantics**: A nudge with `deliver_after` in the future should not be evicted even if its apparent age looks stale — only `expires_at` determines eviction eligibility.
3. **Orphaned `.claimed` files**: The existing `staleClaimThreshold` (5min default) already handles these in `Drain()`. The GC should also handle them for dead sessions: remove `.claimed` files older than the stale threshold since no drainer is coming back.
4. **No queue directory removal while session is alive**: Use the same tmux `HasSession` check as `poller_dog` before removing empty directories.
5. **Configurable via `mayor/daemon.json`**: Must be opt-in (consistent with poller_dog, doctor_dog) and have a configurable interval.

## Open Questions

1. **Should GC also sweep nudge queue dirs for sessions that never existed in tmux?** (e.g., created by a test that didn't clean up). Leaning yes — if no tmux session and all nudges expired, the directory is garbage.
2. **Should we emit metrics/telemetry?** The daemon logger already captures patrol activity; structured telemetry (evicted_count, dirs_pruned) would help monitor nudge system health.
3. **Grace period for recently-dead sessions**: Should there be a delay between session death detection and directory pruning? A session might be about to respawn (Witness restart). Suggest: only prune if ALL nudges are expired AND session is dead — this naturally provides a grace period equal to the longest TTL (2h for urgent).

## Integration Points

| Component | How it connects |
|-----------|----------------|
| `internal/nudge/queue.go` | New exported `EvictExpired(townRoot, session string) (int, error)` function for per-session eviction |
| `internal/nudge/queue.go` | New exported `ListQueueSessions(townRoot string) ([]string, error)` to enumerate all queue directories |
| `internal/daemon/daemon.go` | New ticker in select loop, guarded by `d.isPatrolActive("nudge_gc")` |
| `internal/daemon/nudge_gc_dog.go` | New file following `poller_dog.go` structure |
| `internal/daemon/types.go` | Add `NudgeGC *NudgeGCDogConfig` to `DaemonPatrolConfig.Patrols` |
| `internal/config/operational.go` | Already has `NudgeThresholds` — no changes needed |
| `internal/cmd/nudge.go` | Optional: add `gt nudge gc` subcommand for manual trigger |
| `mayor/daemon.json` | Config entry: `{"patrols": {"nudge_gc": {"enabled": true, "interval": "5m"}}}` |
| `poller_dog` | Synergy: poller_dog already checks tmux sessions — could share the session liveness cache to avoid redundant tmux checks on the same tick |

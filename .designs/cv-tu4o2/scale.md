# Scalability Analysis

## Summary

The nudge queue expiry eviction problem is a bounded-growth issue, not an
unbounded-growth one — which makes scalability straightforward to analyze. The
queue depth per session is capped at 50 (`MaxQueueDepth`), nudge TTLs are short
(30min normal, 2h urgent), and the filesystem operations (readdir + remove) are
O(n) in queue depth. The scalability challenge lies not in per-session queue
processing but in the **number of dead session directories** that accumulate
without eviction.

On the production town today: 121 session directories containing 138 nudge
files, of which 131 are past their 2-hour TTL. At current polecat pool size
(~5 polecats across 2-3 rigs, cycling 10-20 times daily), directory count grows
at approximately 10-20 per day without eviction. This is linear growth bounded
only by disk space. The daemon patrol dog approach (Option A from the integration
analysis) dominates all alternatives on scalability: O(n) walk at configurable
intervals, with n naturally bounded by the eviction itself.

## Analysis

### Key Considerations

- **Current scale**: 121 dirs, 138 files, ~20KB total disk. Trivially small now.
  Problem is trajectory, not magnitude.

- **Growth rate**: Each polecat session that dies without draining leaves 0-50
  nudge files (typically 1-5 in practice: the dispatch message plus a few
  witness nudges). At 10-20 polecat cycles/day and ~3 nudges/cycle-death, that's
  30-100 new orphan files/day.

- **Steady-state with eviction**: With a 5-minute GC patrol, maximum orphan
  accumulation is bounded by: `(rate × interval) + (surviving sessions × depth)`.
  At 100 files/day ÷ 288 patrol cycles/day ≈ 0.35 new files per cycle —
  effectively every GC run finds 0-1 new expired files and removes them.
  Steady-state is near zero orphans.

- **Filesystem cost of GC**: `readdir` on 121 directories, each with 0-5 files.
  Total syscalls: ~240 readdir + ~135 stat + ~131 unlink ≈ 500 syscalls.
  At 0.01ms/syscall on ext4, total wall time: **~5ms per GC cycle**. Negligible
  even at 10x scale.

- **The Drain() hot path is untouched**: Eviction happens in a separate patrol
  dog goroutine. `Drain()` and `Enqueue()` have no new overhead. This is
  critical because `Drain()` runs on every Claude Code hook turn (latency-sensitive).

- **MaxQueueDepth (50) bounds per-session damage**: Even if a malicious or
  buggy sender tries to flood a queue, `Enqueue()` rejects at 50. At ~500 bytes
  per nudge JSON, max disk per session is 25KB. Even with 1000 sessions, total
  nudge disk is bounded at 25MB — not a concern.

### Options Explored

#### Option 1: Fixed-Interval Patrol (5-minute ticker)

- **Description**: GC runs every 5 minutes (configurable). Walks all nudge queue
  directories, removes expired files, prunes empty directories for dead sessions.
- **Pros**:
  - Simple, predictable resource usage
  - O(n) in total files across all sessions (n is small)
  - 5-minute interval means worst case is 5 minutes of orphan accumulation
  - Consistent with daemon patrol patterns (poller_dog: 60s, doctor_dog: 300s)
- **Cons**:
  - Wakes up even when nothing changed (unnecessary readdir)
  - At extreme scale (10K sessions), readdir overhead is noticeable but still <100ms
- **Effort**: Low

#### Option 2: Event-Driven GC (fsnotify on .runtime/nudge_queue/)

- **Description**: Watch the parent directory for new subdirectories. When a new
  session directory appears, schedule a deferred check (TTL + grace period later).
  Also trigger on session death events.
- **Pros**:
  - Zero CPU when nothing changes
  - Precise: evicts exactly when TTL expires
  - No unnecessary directory walks
- **Cons**:
  - More complex (timers per session, event buffering)
  - fsnotify has platform-specific limits (inotify watches on Linux: 8192 default)
  - Must still handle startup (scan existing dirs for immediate eviction)
  - Watcher per directory doesn't scale past ~1000 dirs without bumping kernel limits
- **Effort**: High

#### Option 3: Lazy Eviction on Access (Enqueue/Drain-time sweep)

- **Description**: When any process accesses a session's queue, opportunistically
  evict expired files from that specific session (not others).
- **Pros**:
  - No background goroutine
  - Proportional: active sessions get cleaned, dead ones don't matter
- **Cons**:
  - Dead sessions are never cleaned (the core problem)
  - Adds latency to hot path (Drain: 5-50ms depending on depth)
  - Breaks separation of concerns (every caller pays for GC)
- **Effort**: Low, but doesn't solve the problem

#### Option 4: Tiered GC (Fast sweep + Slow prune)

- **Description**: Two-phase patrol: a fast sweep (every 60s) that only removes
  files with `expires_at < now` from directories it already knows about (cached
  list), plus a slow prune (every 5m) that does a full readdir to discover new
  dead directories and remove empty ones.
- **Pros**:
  - Fast path is O(known_dead_sessions) — nearly constant time
  - Slow path handles discovery without blocking the fast path
  - Adapts: known-dead-sessions list shrinks as they get pruned
- **Cons**:
  - More complex state (cached session list)
  - Marginal benefit over Option 1 until scale exceeds ~500 sessions
  - Two timers in the daemon loop
- **Effort**: Medium

### Recommendation

**Option 1 (Fixed-Interval Patrol at 5 minutes)** is the right choice for
current and foreseeable scale. The math is clear:

| Scale | Sessions | Files | GC wall time | GC frequency | Disk usage |
|-------|----------|-------|-------------|--------------|------------|
| Current | 121 | 138 | ~5ms | 5min | ~70KB |
| 10x | 1,210 | 1,380 | ~50ms | 5min | ~700KB |
| 100x | 12,100 | 13,800 | ~500ms | 5min | ~7MB |
| 1000x | 121,000 | 138,000 | ~5s | 5min | ~70MB |

At 100x (12K sessions), 500ms every 5 minutes is 0.17% CPU — still negligible.
At 1000x, the town would be running 12K polecat sessions (impossible on a single
host) — this scale is beyond any realistic deployment.

**Only switch to Option 4 (Tiered) if:**
- Total session directory count exceeds 5,000 AND
- The 5-minute readdir cost exceeds 1 second AND
- There is observable daemon loop stalling

This is a YAGNI boundary. Build Option 1 now; it has a clear graduation path.

## Scale Dimensions Analyzed

### Data size

| Metric | Current | Max realistic | Hard limit |
|--------|---------|---------------|------------|
| Files per session | 1-5 (avg 1.1) | 50 (MaxQueueDepth) | 50 (enforced) |
| Bytes per file | 200-800 | ~500 avg | ~2KB (with long messages) |
| Total sessions | 121 | ~500 (10 rigs × 5 polecats × 10 cycles/day) | Disk |
| Total disk | ~70KB | ~12MB (500 sessions × 50 files × 500B) | Irrelevant |

### Request rate

| Operation | Rate | Latency |
|-----------|------|---------|
| Enqueue | ~50/hour (plugin dispatches + witness nudges) | <1ms |
| Drain | ~60/hour (hook fires per active session per turn) | 1-5ms |
| GC patrol | 12/hour (every 5min) | 5-50ms |
| Queue depth check | ~50/hour (before each Enqueue) | <0.5ms |

All well within single-host capabilities. No network involved.

### Resource usage

- **CPU**: GC patrol is I/O-bound (readdir + stat), not CPU-bound. <0.1% of a
  single core even at 100x scale.
- **Memory**: Zero persistent memory cost. GC reads directory entries, processes
  in-place, discards. Peak allocation during GC: ~50KB (directory entry buffers
  for readdir on 121 dirs).
- **Disk I/O**: Sequential reads + unlinks. On SSD/NVMe, trivial. On NFS (if
  townRoot is NFS-mounted), readdir is O(10ms) per directory — at 121 dirs,
  ~1.2s. Still acceptable for a 5-minute patrol.
- **File descriptors**: GC opens and closes immediately. Peak FD count: 1 (for
  readdir) + 1 (for file read if checking content). No FD accumulation.

### Bottlenecks: what limits growth?

1. **Filesystem inode exhaustion** (theoretical): 138,000 tiny files at 1000x
   scale. ext4 default: 16M inodes on a 1TB disk. Not a real concern.

2. **readdir performance on large directories** (practical at extreme scale):
   A single nudge_queue directory containing 121+ subdirectories is fine. If
   individual session directories grew past MaxQueueDepth (impossible with the
   guard), readdir would slow. The 50-file cap prevents this.

3. **NFS latency** (environment-specific): If `.runtime/` is on NFS, each
   readdir incurs network round-trip. The GC patrol would take seconds instead
   of milliseconds. Mitigation: `.runtime/` should always be local (it's already
   under `/local/home/` on this deployment).

4. **Inotify watch limits** (irrelevant for Option 1): Only relevant if using
   fsnotify-based GC (Option 2). Default: 8192 watches per user. With 121
   dirs, well within limits — but future scale could hit it.

### Degradation modes

| Mode | Trigger | Behavior | Recovery |
|------|---------|----------|----------|
| GC disabled/crashed | Daemon restart | Orphans accumulate until next daemon start | Daemon restart; manual `gt nudge gc` |
| GC takes too long | >1000 dirs (unrealistic) | Other patrol dogs are delayed by select loop blocking | Increase interval or switch to tiered GC |
| Disk full | Millions of nudge files (impossible with MaxQueueDepth) | Enqueue fails with ENOSPC | GC clears backlog; operator alerts on ENOSPC |
| Race with Drain | Session respawns during GC of "dead" dir | GC removes expired files, Drain processes non-expired ones | Safe: GC only removes files past ExpiresAt; Drain uses rename-claim |

## Constraints Identified

1. **MaxQueueDepth (50) is the critical safety valve.** Without it, a buggy
   sender could create unbounded files per session. The eviction GC assumes this
   bound exists — do not remove or raise it without re-evaluating GC timing.

2. **GC must not touch `.claimed` files younger than staleClaimThreshold
   (5min).** These indicate an active drainer. For dead sessions, all claims are
   stale by definition, but the GC should respect the threshold to avoid races
   during session restart windows.

3. **Directory removal requires tmux session death confirmation.** An empty
   queue directory could belong to a session that simply hasn't received nudges
   yet. Only remove directories where: (a) all files are expired/removed AND (b)
   tmux reports no matching session AND (c) no PID file exists for a live poller.

4. **GC interval must be ≥ TTL to prevent premature eviction ambiguity.**
   Actually, GC should only remove files whose `expires_at` is in the past.
   The interval just determines latency between expiry and actual deletion. No
   minimum constraint on interval — even 1-second intervals are safe.

5. **The 5-minute interval is a UX choice, not a technical requirement.** It
   balances "orphans linger briefly" against "daemon does unnecessary work." The
   operational config makes this tunable without code changes.

## Open Questions

1. **Should GC metrics feed into the health check system?** E.g., if orphan
   count exceeds a threshold despite GC running, something is wrong (sessions
   dying faster than GC can clean). A health check with `"nudge_orphan_ratio"`
   could alert.

2. **Should the GC also compact the `.runtime/nudge_queue/` parent directory?**
   On ext4, removing files doesn't shrink the directory's allocated blocks. After
   many create+delete cycles, the directory file itself grows (htree expansion).
   This is a filesystem-level concern and probably not worth addressing until
   observed.

3. **What's the right interval for the first production deployment?** 5 minutes
   matches doctor_dog. But given the tiny workload (<5ms), 1 minute would clear
   orphans faster with negligible cost. Suggest: start at 5m, operator can tune
   down after observing patrol logs.

4. **Should GC run at daemon startup?** The daemon restarts mean accumulated
   orphans from the previous run linger until the first patrol cycle. A
   "startup sweep" (GC immediately on daemon boot) eliminates the initial 5-minute
   gap. Cost: ~5ms of extra startup time. Benefit: clean slate from moment one.

## Integration Points

### → Daemon Loop (select statement)
- New ticker alongside existing patrol dogs
- Bounded execution time (5-50ms) prevents select loop starvation
- ConfigGuarded: `d.isPatrolActive("nudge_gc")` for opt-in/opt-out

### → `internal/nudge/queue.go` (new exports)
- `EvictExpired(townRoot, session string) (int, error)`: per-session eviction
- `ListQueueSessions(townRoot string) ([]string, error)`: directory enumeration
- These are pure functions with no concurrency concerns (operate on filesystem)

### → Operational Config (`config.NudgeThresholds`)
- Already exists with TTL fields. Add: `gc_interval`, `gc_grace_period`
- Consistent with ZFC: Go provides transport, config provides knobs

### → tmux Session Checker
- Reuse `sessionChecker` interface (already used by poller_dog) to verify
  session liveness before directory pruning
- No new infrastructure needed

### → Health/Doctor System
- Optional: surface "orphan_count" in `gt doctor` or daemon health endpoint
- Allows operator visibility into nudge system hygiene

### → Data Model Dimension
- GC needs to parse `expires_at` from JSON files to make eviction decisions
- File format is stable (`QueuedNudge` struct) — no schema evolution concerns
- Could add a `gc_evicted_at` field to a manifest for auditing, but YAGNI for v1

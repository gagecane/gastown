# User Experience Analysis

## Summary

The nudge queue expiry eviction feature is fundamentally **invisible
infrastructure** — when working correctly, no user ever interacts with it
directly. The entire feature exists to prevent a class of failure (queue
buildup from dead sessions) that manifests as degraded system behavior rather
than a user-facing error message. This makes the UX analysis unusual: instead
of designing an interaction, we're designing the *absence* of a bad experience
and ensuring operators have appropriate visibility when they want it.

The primary UX surfaces are: (1) operator visibility via `gt nudge` subcommands,
(2) diagnostic information when things go wrong (Dolt-style health reporting),
and (3) zero new commands or workflows for day-to-day operation. The operator
should never need to think about nudge queue hygiene unless investigating a
system-level issue — at which point the tools should provide clear, actionable
information. The key principle: **silent when healthy, informative when
investigated, loud when broken.**

## Analysis

### Key Considerations

- **Users don't think about nudge queues.** Nudges are a transport layer.
  Polecats receive them as `<system-reminder>` blocks injected via hooks.
  The Witness sends them. The Deacon dispatches via them. No user types
  `gt nudge` in their daily workflow — it's infrastructure.

- **The "user" is the operator (Mayor/human).** Only during debugging or
  system health checks would someone inspect nudge queue state. This means:
  the UX must be optimized for the debugging mental model, not a task-completion
  model.

- **Existing command surface (`gt nudge`)**: The `gt nudge` command currently
  sends nudges. Adding queue inspection/management subcommands here follows
  the principle of least surprise. Operators already know `gt nudge` exists.

- **Failure mode is subtle, not catastrophic.** When nudge queues fill with
  expired messages, the symptom is: queues report "full" for dead sessions,
  slightly slower directory scans, disk usage creeps up. No agent crashes, no
  data loss. This means: the feature should NOT alarm or escalate — just
  quietly clean up.

- **The daemon does the work; the CLI provides windows.** Consistent with
  Gas Town's architecture (daemon patrol dogs handle automation; CLI provides
  observability). The `gt doctor` and `gt dolt status` patterns are the UX
  precedent.

- **Progressive disclosure applies**: L0 users should never encounter this.
  L1 operators see it in patrol reports. L2 debuggers can inspect queue state.
  L3 tuners can adjust GC intervals. L4 developers can extend the system.

### Options Explored

#### Option 1: Completely Silent (No New CLI Surface)

- **Description**: The daemon GC dog runs silently. No new `gt nudge` subcommands.
  Operators see GC activity in daemon logs and patrol digests.
- **Pros**:
  - Zero new command surface to learn
  - True "invisible infrastructure"
  - Consistent with other daemon dogs (poller_dog has no dedicated CLI)
  - No maintenance burden for CLI help text
- **Cons**:
  - Debugging requires reading daemon logs (harder to grep/filter)
  - No way to manually trigger GC without `gt plugin run nudge-gc`
  - Can't inspect current queue state without filesystem exploration
  - "Did the GC run? When? What did it find?" requires log-diving
- **Effort**: Low (nothing to build)

#### Option 2: `gt nudge gc` + `gt nudge status` (Minimal CLI Surface)

- **Description**: Add two subcommands: `gt nudge gc` (manual trigger, with
  `--dry-run`) and `gt nudge status` (show queue health). These are the
  debugging windows; the daemon still handles automation.
- **Pros**:
  - Follows `gt dolt status` / `gt dolt cleanup` precedent exactly
  - Manual trigger useful for immediate cleanup (operator deploying fix)
  - Status command answers "is the queue healthy?" without filesystem diving
  - `--dry-run` mode shows what would be evicted (safe exploration)
  - Low learning curve: familiar pattern for Gas Town operators
- **Cons**:
  - Two more subcommands to maintain
  - Risk of operators running `gc` manually instead of fixing root cause
  - Status output format needs design
- **Effort**: Low-Medium

#### Option 3: Integration into `gt doctor` (Health Check Pattern)

- **Description**: Add a nudge queue health check to the existing `gt doctor`
  system. Shows queue stats alongside other health indicators. No dedicated
  `gt nudge` subcommands.
- **Pros**:
  - Unified health dashboard (one command for all system health)
  - Operators already run `gt doctor` when investigating issues
  - Consistent with existing health checks (dolt, sessions, hooks, etc.)
  - No new top-level command surface
- **Cons**:
  - `gt doctor` is read-only — no way to trigger GC from it
  - Check results are pass/fail, not rich inspection
  - Still might want `gt nudge gc` for manual intervention
- **Effort**: Low (one new check function following doctor pattern)

#### Option 4: Rich TUI Dashboard (`gt nudge dashboard`)

- **Description**: Interactive terminal UI showing real-time queue state per
  session, with live updates, GC trigger button, and historical trends.
- **Pros**:
  - Excellent debugging experience
  - Visual representation of queue health over time
  - Could show per-session depth, expiry timeline, GC efficiency
- **Cons**:
  - Massive overkill for a feature that should be invisible
  - High maintenance burden (TUI rendering, terminal compat)
  - Inconsistent with Gas Town's CLI-first philosophy
  - Nobody needs real-time nudge queue monitoring
- **Effort**: High

### Recommendation

**Option 2 (`gt nudge gc` + `gt nudge status`)** combined with **Option 3
(doctor integration)** as a complementary health check.

This gives operators exactly three touch points, matching the three questions
they'd ask:

| Question | Answer via |
|----------|-----------|
| "Is the nudge system healthy?" | `gt doctor` (includes nudge check) |
| "What's the current queue state?" | `gt nudge status` |
| "Clean it up right now" | `gt nudge gc` |

The daemon GC dog handles the normal case silently. These commands exist for
the exception case.

## Recommended CLI Design

### `gt nudge status`

```
$ gt nudge status

● Nudge Queue Health

  Sessions:     121 total (8 active, 113 dead)
  Pending:      138 files (131 expired, 7 live)
  Disk:         ~70KB
  Last GC:      2 minutes ago (evicted 3 files)
  GC interval:  5m (daemon patrol: nudge_gc)

  ⚠ 113 dead session directories with orphaned nudges
    (daemon GC will evict expired nudges on next patrol cycle)

  Active sessions with pending nudges:
    gastown_upstream_witness_001:  2 pending (0 expired)
    gastown_upstream_fury_002:     1 pending (0 expired)
    ...
```

Key design choices:
- **Summary first** (total counts, disk, last GC)
- **Problem highlighted** (dead sessions with orphans)
- **Actionable detail** (which active sessions have pending nudges)
- **Self-healing message** (daemon will handle it)

### `gt nudge gc`

```
$ gt nudge gc --dry-run

○ Nudge GC (dry run)

  Would evict 131 expired nudges across 113 dead sessions
  Would prune 108 empty directories (session not in tmux)
  Estimated cleanup: ~65KB

  Top 5 oldest orphans:
    gastown_upstream_coder_abc (3 days old, 5 nudges)
    gastown_upstream_fury_xyz (2 days old, 3 nudges)
    ...

  Run without --dry-run to execute.

$ gt nudge gc

✓ Nudge GC complete

  Evicted:  131 expired nudges
  Pruned:   108 empty directories
  Freed:    ~65KB
  Duration: 5ms
```

Key design choices:
- **`--dry-run` is default-visible** (shows what would happen first)
- **No confirmation prompt** (this is safe; only removes expired files)
- **Duration shown** (proves it's fast — builds operator confidence)
- **Not destructive** (only removes files past their TTL — cannot lose live nudges)

### `gt doctor` integration

```
$ gt doctor

  ...
  ✓ Nudge queues: 7 live nudges, 0 expired (last GC: 2m ago)
  ...
```

Or when unhealthy:
```
  ⚠ Nudge queues: 131 expired nudges across 113 dead sessions
    Fix: gt nudge gc (or wait for daemon patrol)
```

## Error Experience

| Scenario | What user sees | What to do |
|----------|---------------|------------|
| **GC daemon not running** | `gt nudge status` shows "Last GC: never" or very old timestamp | Start daemon: `gt daemon start` |
| **GC running but orphans growing** | Status shows rising orphan count despite recent GC | Check if sessions are dying faster than expected; investigate polecat health |
| **Queue full for active session** | Enqueue fails: "nudge queue for X is full (50/50 pending)" | `gt nudge gc` won't help (these are live). Check why the session isn't draining (hook broken? session stuck?) |
| **Nudge not delivered** | Agent never receives expected nudge | `gt nudge status` → check if target session is dead. If dead, nudge was orphaned and will be evicted |
| **Disk pressure from nudges** | Unlikely (<70KB total) but surface in `gt nudge status` | `gt nudge gc` for immediate relief |

### Error messages should include:
1. What happened (factual)
2. Why it matters (context)
3. What to do next (actionable)

Example:
```
⚠ Nudge delivery failed: queue for "gastown_upstream_fury_xyz" is full (50/50)

  The target session has 50 unread nudges. This usually means:
  - The session's drain hook is broken (check: gt nudge poller status <session>)
  - The session is stuck and not processing turns (check: gt peek <polecat>)

  Nudges expire naturally (normal: 30m, urgent: 2h). Expired nudges are
  evicted by the daemon GC patrol every 5 minutes.
```

## Feedback: How Does the User Know It's Working?

### Passive (no action required):
- **Patrol digest**: GC activity summarized as one line:
  "Nudge GC: evicted 15 expired across 8 dead sessions"
- **Daemon log**: Structured JSON events for each GC cycle (visible in
  `gt daemon logs` or journal)
- **No orphan accumulation**: `gt nudge status` stays clean

### Active (operator checks):
- **`gt nudge status`**: Shows real-time queue health
- **`gt doctor`**: Nudge check passes/warns/fails
- **Patrol report**: Historical GC activity in daily aggregate

### Absence of signal IS the signal:
The best UX for this feature is: operators forget it exists. If they never
run `gt nudge status`, that means the daemon GC is working perfectly and
no manual intervention was needed. The feature succeeded by being invisible.

## Discoverability

### `gt nudge --help` (updated):

```
Manage nudge delivery for Gas Town agents.

Nudges are non-destructive messages delivered at turn boundaries via hooks.
They're used for agent-to-agent communication without interrupting active work.

SENDING:
  gt nudge <target> "message"          Send a nudge
  gt nudge <target> "msg" --urgent     Send urgent nudge

INSPECTION:
  gt nudge status                      Queue health overview
  gt nudge status <session>            Detailed per-session state

MAINTENANCE:
  gt nudge gc                          Evict expired nudges (manual)
  gt nudge gc --dry-run                Preview what would be evicted

The daemon automatically evicts expired nudges every 5 minutes.
Manual gc is only needed for immediate cleanup or debugging.
```

### Progressive disclosure tiers:

| Level | Knowledge needed | Commands used |
|-------|-----------------|---------------|
| L0: Agent | None — receives nudges via hook automatically | (none) |
| L1: Operator | "Nudge system exists" | `gt doctor` |
| L2: Debugger | "Queues can accumulate" | `gt nudge status` |
| L3: Intervener | "I can clean up manually" | `gt nudge gc` |
| L4: Tuner | "GC interval is configurable" | Edit `operational.nudge.gc_interval` in config |

## Constraints Identified

1. **No interactive prompts in GC.** The daemon GC runs non-interactively;
   the CLI `gt nudge gc` should also be non-interactive (no "are you sure?"
   prompts). Expired nudges are definitionally safe to remove.

2. **Status output must be fast.** `gt nudge status` is a diagnostic tool —
   operators run it when investigating issues. It must complete in <500ms
   even with 1000+ session directories (achievable via readdir + stat without
   reading file contents for the summary view).

3. **GC must not require daemon running.** The CLI `gt nudge gc` should work
   standalone (for debugging when daemon is down). It should not RPC to the
   daemon — just directly walk the filesystem.

4. **No new concepts for agents.** Polecats, witnesses, and deacons should
   never need to know about GC. Their interface to the nudge system (`Drain()`,
   `Enqueue()`) is unchanged. GC is purely operational infrastructure.

5. **Output format consistency.** Follow existing `gt` CLI style:
   - `●` for healthy status
   - `⚠` for warnings
   - `✗` for failures
   - Indentation for detail
   - `--json` flag for scripting

## Open Questions

1. **Should `gt nudge status` show per-session pending counts, or just
   aggregates?** Per-session is more useful for debugging ("which session is
   full?") but verbose with 100+ sessions. Suggest: aggregates by default,
   per-session with `--verbose` or specifying a session name.

2. **Should expired nudge content be logged before eviction?** For debugging,
   knowing WHAT expired can help identify the root cause (e.g., "the witness
   kept nudging a dead polecat 50 times"). But logging nudge content creates
   noise. Suggest: log at DEBUG level only, summary at INFO.

3. **Metric naming for health checks?** The doctor system uses check names
   like `nudge_queue_health`. Should the warning threshold be configurable?
   (e.g., "warn if >100 expired nudges" vs "warn if any expired nudges").
   Suggest: warn if orphan_count > 50 AND last_gc_age > 10 minutes (GC not
   keeping up).

4. **Should `gt nudge gc` accept a `--session` flag?** To clean up a specific
   dead session rather than all. Useful when debugging a specific polecat's
   history. Low cost to add; follows `gt dolt cleanup --database` pattern.

## Integration Points

### → CLI Command Tree (`internal/cmd/nudge.go`)
- Add `statusCmd` and `gcCmd` subcommands
- Follow existing pattern: flags, JSON output, styled text output
- `gc --dry-run` reuses same logic as daemon GC but prints instead of deleting

### → Doctor System (`internal/doctor/`)
- New check: `nudge_queue_health_check.go`
- Criteria: orphan count, last GC time, queue depth per active session
- Severity: WARN (not ERROR) — this is a hygiene issue, not an outage

### → Daemon Patrol (`internal/daemon/`)
- GC dog reports results via structured log (already exists for other dogs)
- Results available for `gt nudge status` to display "Last GC" time
- No new RPC — status reads from filesystem + daemon log

### → Scalability Dimension
- Status command performance depends on queue size (O(n) readdir)
- At current scale (121 dirs): <5ms. At 1000x: <500ms. Acceptable.
- `--json` output is flat (not nested per-session) for easy piping

### → Security Dimension
- GC only removes files past their `expires_at` — cannot delete live nudges
- No authentication needed for CLI gc (already running as the town user)
- No new network exposure (filesystem-only operations)

### → Data Model Dimension
- GC reads `expires_at` from JSON files — depends on stable schema
- `QueuedNudge` struct is the canonical format — if it evolves, GC must handle
  both old and new formats gracefully (or old files get evicted by age fallback)

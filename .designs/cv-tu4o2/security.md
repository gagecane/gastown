# Security Analysis

## Summary

The nudge queue expiry eviction feature introduces a background process (or
periodic sweep) that deletes expired `.json` files from
`<townRoot>/.runtime/nudge_queue/<session>/`. The security surface is narrow:
the queue lives entirely on the local filesystem with no network exposure, and
all actors share the same Unix user. The primary risks are denial-of-service via
queue flooding, TOCTOU races in file operations, and path traversal through
crafted session names.

The existing code already mitigates several concerns (MaxQueueDepth, session
name sanitization, atomic claim-rename). An eviction mechanism must preserve
these invariants and not introduce new attack vectors through its own operation.

## Analysis

### Key Considerations

- **Single-user, local-only**: All queue operations run as the same Unix user
  (`canewiw`). There is no network listener, no remote API, no authentication
  layer. The trust boundary is the filesystem permission model.
- **No sensitive data at rest**: Nudge messages contain agent-to-agent
  coordination text (sender, message, priority). No credentials, PII, or
  secrets are stored in the queue.
- **Filesystem as IPC**: The queue is a directory of JSON files. Any process
  running as the same user can read, write, or delete entries. This is by
  design (multiple agents share a town).
- **Expiry is already implemented in Drain**: `Drain()` already discards
  expired nudges (line 257 of queue.go). The problem is that Drain is only
  called at turn boundaries — if no agent is actively draining, expired files
  accumulate indefinitely.
- **Orphan .claimed files**: The existing stale-claim sweeper already handles
  files left by crashed drainers. The eviction feature is conceptually similar
  but scoped to expired (not crashed) nudges.

### Trust Boundaries

| Boundary | Trust Level | Notes |
|----------|-------------|-------|
| Filesystem (same user) | Fully trusted | All agents run as the same user |
| Session name → path | Sanitized | `strings.ReplaceAll(session, "/", "_")` prevents traversal |
| Nudge content | Untrusted display | Rendered into `<system-reminder>` blocks — no code execution |
| ExpiresAt field | Sender-controlled | Sender sets TTL; eviction trusts this timestamp |
| System clock | Trusted | `time.Now()` is the sole time source |

### Attack Surface

#### 1. Queue flooding (existing, already mitigated)
- **Vector**: A malicious or misconfigured agent calls `Enqueue()` in a tight loop.
- **Existing mitigation**: `MaxQueueDepth = 50` (configurable). Enqueue returns
  an error when the limit is reached.
- **Eviction impact**: An eviction sweep that runs on a timer could race with
  MaxQueueDepth checks. If eviction removes files between the Pending() count
  and the write, the depth check becomes inaccurate (but only in the safe
  direction — allowing slightly more entries, never fewer).

#### 2. Path traversal via session name
- **Vector**: A crafted session name like `../../etc` could escape the queue
  directory.
- **Existing mitigation**: `queueDir()` replaces `/` with `_`, collapsing any
  traversal attempt into a flat directory name.
- **Eviction impact**: The eviction sweep must use the same `queueDir()`
  function — never construct paths from raw session names. If the sweep
  enumerates all subdirectories of `nudge_queue/`, it should only process
  directories within that tree and never follow symlinks.

#### 3. TOCTOU in file deletion
- **Vector**: Between checking expiry and calling `os.Remove()`, the file could
  be replaced with a symlink pointing elsewhere.
- **Existing mitigation**: The Drain function uses rename-then-process (atomic
  claim). However, a sweep that reads-then-deletes without claiming has a
  TOCTOU window.
- **Recommendation**: The eviction sweep MUST use the same claim-rename pattern
  as Drain, or at minimum verify the file is a regular file (not a symlink)
  before deletion. Since all processes run as the same user, symlink attacks
  would be self-inflicted, but defense-in-depth is cheap here.

#### 4. Time-of-check/time-of-expiry clock skew
- **Vector**: If `ExpiresAt` is set far in the past (e.g., epoch), a nudge
  would be immediately evicted on write — effectively a silent drop.
- **Impact**: Low. The sender controls their own TTL. A sender that sets
  `ExpiresAt` to the past is harming only themselves (their nudge is never
  delivered). This is not a privilege escalation.
- **Recommendation**: No action needed. This is equivalent to not sending the
  nudge.

#### 5. Denial of service via eviction suppression
- **Vector**: An attacker writes non-JSON files (or files without `.json`
  extension) into the queue directory. These bypass MaxQueueDepth (which only
  counts `.json` files) and are never evicted.
- **Impact**: Disk exhaustion over time. Mitigated by the fact that only the
  same-user processes can write to the directory.
- **Recommendation**: The eviction sweep should also remove non-`.json` files
  that are older than a conservative threshold (e.g., 24h), or at least log
  their presence.

### Options Explored

#### Option A: Timer-based sweep in the nudge-poller process
- **Description**: Add a periodic `EvictExpired()` call to the existing
  nudge-poller background loop (already runs every 10s per session).
- **Pros**: No new processes; reuses existing lifecycle management (PID files,
  SIGTERM cleanup); already has access to townRoot and session.
- **Cons**: Only sweeps sessions with an active poller. Dead sessions
  (no poller, no active agent) accumulate forever.
- **Effort**: Low
- **Security**: Inherits poller's existing permissions model. No new surface.

#### Option B: Standalone town-wide sweep (daemon cron or `gt dolt cleanup`-style)
- **Description**: A periodic sweep that iterates ALL session directories under
  `nudge_queue/`, evicting expired files regardless of whether a poller is
  active.
- **Pros**: Handles dead sessions; single sweep for the whole town; can also
  garbage-collect empty directories.
- **Cons**: New process lifecycle to manage; must handle concurrent Drain races;
  slightly larger blast radius if buggy.
- **Effort**: Medium
- **Security**: Must be careful not to race with active Drain operations.
  Should use `O_NOFOLLOW` semantics (no symlink traversal). Should validate
  that each entry is a regular file before deletion.

#### Option C: Lazy eviction on Enqueue (piggyback)
- **Description**: Before writing a new nudge, scan the directory and evict
  expired files to make room.
- **Pros**: Zero new processes; self-healing; eviction happens exactly when
  queue pressure builds.
- **Cons**: Adds latency to Enqueue (directory scan); doesn't help dead
  sessions; races with concurrent Drain.
- **Effort**: Low
- **Security**: Minimal new surface. The Enqueue path already reads the
  directory (Pending count). Adding eviction of clearly-expired files is a
  natural extension.

### Recommendation

**Option A + B combined** (timer sweep in poller + town-wide reaper for dead sessions):

1. Add `EvictExpired(townRoot, session)` to the poller's 10s loop (Option A) —
   this handles the common case (active sessions with stale nudges).
2. Add a town-wide sweep to the existing reaper patrol (Option B) — this
   handles dead sessions. The reaper already exists for orphan DB cleanup;
   adding nudge queue GC is a natural extension.

Both MUST:
- Use `os.Lstat` to verify regular file (not symlink) before deletion
- Only delete files with `.json` extension that parse as valid `QueuedNudge`
  with a past `ExpiresAt`
- Tolerate concurrent Drain (file-not-found after Lstat is fine, not an error)
- Log eviction counts for observability (not every file, just totals per sweep)

## Constraints Identified

1. **MUST NOT delete non-expired nudges.** An eviction sweep that incorrectly
   removes live nudges is a data-loss bug that silently drops agent messages.
   This is the #1 invariant.
2. **MUST NOT follow symlinks.** Use `os.Lstat` + check `mode.IsRegular()`
   before any deletion. The queue directory should never contain symlinks.
3. **MUST tolerate concurrent access.** Multiple pollers, Drain calls, and
   Enqueue calls can operate simultaneously. File-not-found errors during
   eviction are expected, not exceptional.
4. **MUST respect MaxQueueDepth semantics.** Eviction removes files from the
   count; concurrent Enqueue callers may see a lower Pending() and write
   slightly above the intended cap. This is acceptable (the cap is advisory,
   not a hard security boundary).
5. **MUST NOT introduce network exposure.** The eviction mechanism runs locally.
   No HTTP endpoints, no IPC sockets, no remote triggers.

## Open Questions

1. **Eviction frequency for the town-wide sweep**: Should the reaper run nudge
   queue GC on every patrol (every few minutes), or on a slower cadence (hourly)?
   Tradeoff: more frequent = less accumulation, but more filesystem churn on
   healthy systems.
2. **Should eviction log to the feed?** Currently nudge operations are silent.
   If eviction removes a large batch (>20 nudges), should it emit a feed event
   for observability? This could leak message volume information.
3. **Empty directory cleanup**: After evicting all files from a dead session's
   queue directory, should the empty directory itself be removed? This prevents
   orphan directory accumulation but makes the `Pending()` path slightly more
   expensive (MkdirAll on next write).

## Integration Points

- **Scalability dimension**: Eviction frequency and per-sweep cost directly
  affect filesystem I/O budget. The scalability analysis should account for
  town-wide sweep cost at scale (100+ session directories).
- **Data Model dimension**: The `ExpiresAt` field is the sole input for
  eviction decisions. If the data model changes (e.g., adding `evicted_at`
  metadata or an eviction log), the security implications shift.
- **User Experience dimension**: Silent eviction means agents never know a
  nudge was dropped. If a nudge expires before delivery, the sender gets no
  feedback. This is a UX choice with security implications (no information
  leakage to senders about recipient activity).
- **Performance dimension**: Lstat + ReadFile + JSON unmarshal per file during
  sweep. For large queues (approaching MaxQueueDepth × number of sessions),
  this could spike I/O. Batch processing with early-exit on healthy queues
  mitigates this.

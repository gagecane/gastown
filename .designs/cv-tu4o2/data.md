# Data Model Design

## Summary

The nudge queue expiry eviction feature operates on an existing, well-defined
data model: JSON files on the filesystem with an `ExpiresAt` timestamp. No new
persistent data structures are needed. The eviction logic simply needs to read
and compare timestamps from existing files, then remove expired ones. The data
model is already complete — the gap is purely operational (no process walks dead
session directories to act on the expiry data that's already present).

The schema is deliberately minimal (one JSON file per nudge, one directory per
session, flat hierarchy). This design is ideal for eviction: each file is
self-contained, independently removable, and carries all metadata needed to
determine expiry. No indices, no cross-references, no migrations required.

## Analysis

### Key Considerations

- **Schema is already in production**: `QueuedNudge` struct is stable with 10
  fields, serialized to JSON. No schema change is needed for eviction.
- **ExpiresAt is already set on every nudge**: `Enqueue()` assigns TTL-based
  expiry at write time (30m normal, 2h urgent). The eviction GC only needs to
  compare this field against `time.Now()`.
- **Data is ephemeral by design**: Nudges are communication artifacts with
  short lifetimes. Loss of any nudge file is acceptable (agents handle missing
  context gracefully). This means the GC can be aggressive.
- **No relational dependencies**: Each nudge file is independent. Removing one
  expired file has zero effect on other files in the same or different queues.
- **Filesystem IS the index**: Directory structure (`nudge_queue/<session>/*.json`)
  provides the only lookup dimension needed: list all sessions, list files per
  session.
- **File count in production**: 134 `.json` files across 122 session directories,
  with 0 `.claimed` files — confirming that accumulation is from dead sessions,
  not from active draining races.

### Current Data Model (No Changes Required)

```go
// QueuedNudge — the complete schema (internal/nudge/queue.go)
type QueuedNudge struct {
    Sender          string    `json:"sender"`
    Message         string    `json:"message"`
    Priority        string    `json:"priority"`           // "normal" | "urgent"
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

**Storage layout:**
```
<townRoot>/.runtime/nudge_queue/
├── gt-witness/
│   ├── 1777375888899006192-418f3fd7.json   # nanosecond timestamp + random suffix
│   └── 1777375889084083168-d1ff62cb.json
├── gt-polecat-alpha-abc123/
│   └── (empty — session dead, dir remains)
└── gt-crew-sean/
    └── 1777381352927340179-5bb34c1f.json
```

### Data Fields Relevant to Eviction

| Field | Type | Role in Eviction | Notes |
|-------|------|------------------|-------|
| `expires_at` | `time.Time` | **Primary eviction signal** | Always set by `Enqueue()`. If `time.Now() > expires_at`, file is stale. |
| `timestamp` | `time.Time` | Fallback ordering / age check | Write time. Can serve as secondary signal if `expires_at` were ever zero (defensive). |
| `deliver_after` | `time.Time` | **Must respect** | A deferred nudge that hasn't been delivered yet should NOT be evicted until `deliver_after + TTL`. Current code already handles this correctly: `ExpiresAt` is set based on `Timestamp`, not `DeliverAfter`. |
| `priority` | `string` | TTL selection | Normal=30m, Urgent=2h. Already baked into `ExpiresAt` at enqueue time. GC doesn't need to re-derive. |

### Options Explored

#### Option 1: Pure file-level eviction (read each `.json`, check `expires_at`)

- **Description**: GC opens each `.json` file, unmarshals, checks
  `ExpiresAt < now`, removes if expired. Most accurate.
- **Pros**:
  - 100% accurate — reads the authoritative expiry timestamp
  - Handles custom expiry (caller-specified `ExpiresAt`) correctly
  - Handles deferred nudges correctly (won't evict an undelivered deferred nudge
    whose `ExpiresAt` is still in the future)
- **Cons**:
  - Requires read + unmarshal per file (but at 500 bytes/file and <200 files total,
    this is ~100KB I/O — negligible)
  - Slightly slower than filename-based heuristic
- **Effort**: Low

#### Option 2: Filename-based heuristic (nanosecond timestamp in filename)

- **Description**: Parse the nanosecond timestamp from the filename
  (`1777375888899006192-*.json`), add max TTL (2h), evict if past that.
- **Pros**:
  - Zero file reads — pure readdir + string parsing
  - Faster (no I/O per file)
- **Cons**:
  - Filename timestamp is the *enqueue* time, not the expiry time
  - Cannot distinguish 30m-TTL nudges from 2h-TTL nudges without reading content
  - Would over-retain normal-priority nudges (using 2h as universal TTL) or
    prematurely evict urgent nudges (using 30m)
  - Breaks if custom `ExpiresAt` was specified by the caller
  - Breaks for deferred nudges (may have future `ExpiresAt` despite old filename)
- **Effort**: Low, but lossy

#### Option 3: Hybrid — filename pre-filter + full read for borderline files

- **Description**: First pass: skip any file whose filename timestamp is within
  the last 2h (guaranteed not expired). Second pass: read and unmarshal only
  files older than 2h to check actual `ExpiresAt`.
- **Pros**:
  - Avoids reading files that are obviously still fresh
  - Still 100% accurate for eviction decisions
- **Cons**:
  - Marginal optimization (we're talking <200 files; reading all is ~5ms)
  - Added complexity for negligible gain
  - Premature optimization given the scale analysis (see scale.md)
- **Effort**: Low-Medium

#### Option 4: Add eviction metadata to a side-file or directory-level manifest

- **Description**: Maintain a `_manifest.json` per session directory with
  summary data (oldest file timestamp, count, etc.) to enable O(1) skip of
  recently-active directories.
- **Pros**:
  - Enables skipping active directories without readdir
  - Could track GC statistics (last sweep time, eviction count)
- **Cons**:
  - New data structure to maintain (must be updated on every Enqueue/Drain)
  - Consistency risk (manifest can drift from reality after crashes)
  - Violates the current design principle of "each file is self-contained"
  - Adds write overhead to the hot path (Enqueue/Drain)
  - Premature: not needed until scale exceeds thousands of directories
- **Effort**: Medium

### Recommendation

**Option 1: Pure file-level eviction** — read each `.json`, unmarshal, check
`expires_at`, remove if expired. This is the simplest correct implementation:

1. No new data structures or schema changes
2. 100% accurate (respects custom expiry, deferred nudges, priority-based TTL)
3. Negligible performance cost at current and projected scale
4. Zero impact on the `Enqueue()`/`Drain()` hot paths
5. Follows the existing pattern: `Drain()` already does exactly this check
   (line 257-262 of queue.go)

The GC function essentially reuses the same logic as `Drain()` but:
- Walks ALL session directories (not just one)
- Does NOT claim files (no `.claimed` rename — just reads and removes)
- Does NOT deliver nudges (just evicts expired ones)
- Optionally removes empty directories for dead sessions

### Data Lifecycle

```
Enqueue()              Drain() [active session]       GC [dead session]
─────────────────────────────────────────────────────────────────────────
Write .json file  →   Read + claim + deliver  →   (session dies)  →  GC reads
  (ExpiresAt set)      (expired? discard)           (files remain)    (expired? remove)
                                                                       (empty dir? rmdir)
```

**State transitions for a nudge file:**
1. **Created**: `Enqueue()` writes `<timestamp>-<rand>.json`
2. **Delivered**: `Drain()` claims → reads → delivers → removes file
3. **Expired during active session**: `Drain()` claims → reads → detects expiry → removes
4. **Orphaned (session dies)**: File persists indefinitely ← **THE BUG**
5. **Evicted by GC**: GC reads → detects expiry → removes file ← **THE FIX**

### Schema Evolution Considerations

- **No migration needed**: The `QueuedNudge` struct has only grown additively
  (optional fields with `omitempty`). JSON unmarshal handles missing fields
  gracefully with zero values.
- **Future-proofing**: If new fields are added to `QueuedNudge`, the GC only
  cares about `ExpiresAt`. It can use partial unmarshal or just check the one
  field, but full unmarshal is cheap enough that optimization isn't warranted.
- **Backwards compatibility**: Any nudge file from any historical version of
  Gas Town will have either a valid `ExpiresAt` or a zero value (meaning "never
  expires" — legacy files before expiry was added). The GC should treat zero
  `ExpiresAt` as "expired after max TTL from `Timestamp`" for defensive cleanup.

### Access Patterns for the GC

The eviction GC needs exactly these filesystem operations:

```
1. ReadDir(<townRoot>/.runtime/nudge_queue/)     → list session dirs
2. For each session dir:
   a. ReadDir(session_dir)                       → list .json files
   b. For each .json file:
      i.   ReadFile(path)                        → read nudge content
      ii.  json.Unmarshal → check ExpiresAt
      iii. If expired: os.Remove(path)
   c. If dir is empty AND session is dead: os.Remove(session_dir)
```

**Critical concurrency constraint**: Step 2c (removing empty dirs) must verify
the session is dead (no active tmux session) to avoid removing a directory that
an about-to-enqueue caller will re-create. Check via `tmux has-session -t <name>`
or the existing `sessionChecker` interface.

## Constraints Identified

1. **No schema changes**: The `QueuedNudge` struct must not change. Eviction
   operates on existing data.
2. **Zero `ExpiresAt` handling**: Nudge files with `ExpiresAt` as zero time
   (legacy or malformed) should be evicted after `Timestamp + DefaultUrgentTTL`
   as a defensive measure.
3. **Claimed files are off-limits**: The GC must skip `.claimed.*` files — they
   belong to an active `Drain()` call. Only sweep `.json` files.
4. **Empty-dir removal requires session-death proof**: Don't remove a session
   directory unless the session is confirmed dead (tmux check or age heuristic).
5. **No write-path overhead**: The GC must not add any logic to `Enqueue()` or
   `Drain()`. It runs independently.
6. **Configurable thresholds**: Use existing `NudgeThresholds` in
   `config.OperationalConfig` for any new GC-related tuning knobs (e.g., GC
   interval, directory age threshold for removal).

## Open Questions

1. **Should zero-ExpiresAt files be treated as immortal or auto-expired?**
   Recommendation: auto-expire after `Timestamp + DefaultUrgentTTL (2h)`.
   There should be no legitimate reason for a nudge to live forever, and these
   are likely pre-expiry legacy files.

2. **Should the GC log eviction counts?** Useful for observability but must not
   flood stderr. Recommendation: log a single summary line per GC cycle
   (`"nudge_gc: evicted %d files from %d dirs"`).

3. **Should empty-dir removal be gated by session age or tmux check?**
   Recommendation: use tmux session check (via `sessionChecker` interface already
   available in the daemon). Only remove directories whose session name does not
   correspond to a live tmux session.

## Integration Points

- **`internal/nudge/queue.go`**: Contains the `QueuedNudge` struct and all
  existing queue operations. The GC will reuse `queueDir()` and the JSON
  unmarshal logic. Consider exporting a `EvictExpired(townRoot string) (int, error)`
  function in this package.

- **`internal/config/types.go` + `operational.go`**: `NudgeThresholds` already
  defines TTLs and thresholds. New GC interval config should live here
  (e.g., `GCInterval string` field in `NudgeThresholds`).

- **`internal/daemon/types.go`**: `PatrolsConfig` struct needs a new
  `NudgeGC *NudgeGCConfig` field for the daemon patrol dog configuration.

- **`internal/daemon/daemon.go`**: The main daemon select loop will add a new
  ticker case for the nudge GC patrol, following the `poller_dog` / `doctor_dog`
  pattern.

- **Security dimension** (`.designs/cv-tu4o2/security.md`): The GC must not
  follow symlinks or traverse outside `.runtime/nudge_queue/`.

- **Scalability dimension** (`.designs/cv-tu4o2/scale.md`): Confirms that pure
  file-level eviction at current scale (~134 files, ~122 dirs) completes in
  <5ms per cycle. No indexing or caching layer needed.

- **Integration dimension** (`.designs/cv-tu4o2/integration.md`): Confirms
  daemon patrol dog (Option A) as the orchestration mechanism. This data model
  analysis validates that the data layer supports that approach with zero changes.

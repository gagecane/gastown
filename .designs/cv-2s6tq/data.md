# Data Model Design

## Summary

The upstream-sync replacement needs to track sync operations between
`gastownhall/gastown` (upstream) and `gagecane/gastown` (fork origin).
Unlike the old plugin (shell script with ephemeral beads receipts, no
persistent state), the new system must persist: (1) sync attempt history
for observability, (2) conflict resolution state for agent handoff, and
(3) configuration for per-rig opt-in/opt-out.

The key insight is that **most state is ephemeral or computable**. The
fork's git refs themselves are the authoritative source of truth for
"are we in sync?" — a single `git merge-base --is-ancestor` answers
that question without any database. What needs persisting is the
*operational* state: when did we last sync, what conflicts did we
encounter, what is the agent currently working on, and what is the
sync configuration. This maps cleanly onto the existing Gas Town data
substrates: beads (Dolt) for operational state, rig config JSON for
settings, and git refs for the actual sync truth.

## Analysis

### Key Considerations

- **Git refs are the source of truth.** "Is the fork up to date with
  upstream?" is answered by `git merge-base --is-ancestor upstream/main
  origin/main`. No database replicates this — it's always computed.
- **Conflicts require agent state.** When a merge produces conflicts,
  an agent must resolve them. That resolution may span multiple tool
  calls and could fail mid-way. The state must survive session death.
- **The existing plugin uses ephemeral receipt beads.** These are
  fire-and-forget tracking records with no structured query path.
  The new system needs queryable history for "why didn't sync happen?"
- **CI gate (go build + go test) must pass before push.** The gate
  outcome is per-attempt data that needs recording.
- **No new storage substrate.** Following the established pattern
  (auto-test-pr design), all persistent state uses beads (Dolt) or
  extends existing rig config JSON.
- **Agents resolve conflicts autonomously.** This is a fundamental
  shift from the old plugin (which escalated conflicts to humans).
  Agent conflict resolution needs a work bead with structured metadata.
- **Multiple rigs may each have upstream sync configured.** State
  must be per-rig, not global.
- **The system runs via local rig infrastructure (polecats).** No
  external services, no GitHub Apps, no webhooks — purely local
  git operations and polecat dispatch.

### Options Explored

#### Option 1: Pinned state bead per rig (RECOMMENDED)

- **Description**: One pinned bead per sync-enabled rig, ID
  `<rig>-upstream-sync-state`. Carries the sync state machine,
  attempt history (bounded), conflict tracking, and configuration
  metadata. Mayor-owned, polecat-readable.
- **Pros**:
  - Reuses the established pattern from auto-test-pr.
  - Dolt transactions provide serialized state updates.
  - `bd show` works for human inspection out of the box.
  - One bead per rig isolates failures.
  - Schema evolution via `schema_version` field.
  - Supports CAS semantics for state transitions.
- **Cons**:
  - Metadata blob grows with attempt history; must be bounded.
  - Querying across all rigs requires scanning N beads.
- **Effort**: Low. Pattern already established.

#### Option 2: Ephemeral receipt beads only (status quo pattern)

- **Description**: Keep the existing plugin's approach: create
  ephemeral beads as receipts for each run, with labels for querying.
  No persistent state bead.
- **Pros**:
  - Simplest implementation.
  - No schema to evolve.
  - Works today (the old plugin does this).
- **Cons**:
  - Cannot track state machine across attempts (conflict resolution
    spanning multiple sessions has no home).
  - No CAS semantics — cannot prevent concurrent sync attempts.
  - Ephemeral beads expire and are GC'd — audit trail lost.
  - No structured query for "current sync state of rig X."
  - Agent conflict resolution cannot checkpoint progress.
- **Effort**: Low, but insufficient for the new requirements.

#### Option 3: File-based state (JSON in rig directory)

- **Description**: A `.gt/upstream-sync/state.json` file per rig,
  tracked in the rig's git repo, holding sync state.
- **Pros**:
  - Simple, inspectable, version-controlled.
  - No Dolt dependency for state reads.
- **Cons**:
  - State file is IN the repo being synced — chicken-and-egg
    problem when the sync itself modifies the file.
  - Merge conflicts on the state file itself during upstream sync.
  - No transactional semantics (race conditions between agents).
  - Write-to-repo-to-change-state is a privilege escalation vector.
- **Effort**: Low, but architecturally broken for this use case.

#### Option 4: Dedicated SQLite store

- **Description**: `~/.gt/upstream-sync/state.db` with proper tables.
- **Pros**:
  - Rich querying, proper relational model.
  - Fast local reads.
- **Cons**:
  - Adds a new storage substrate (violates established principle).
  - Separate backup, migration, corruption recovery path.
  - Not accessible via standard `bd` tooling.
  - Doubles "where does state live?" decisions for future features.
- **Effort**: High. Not justified at our scale.

### Recommendation

**Option 1: Pinned state bead per rig.** This follows the proven
pattern, integrates with existing tooling, and supports the new
requirements (agent conflict resolution, CI gating, concurrent-attempt
prevention).

## Concrete Schema

### Pinned State Bead (one per sync-enabled rig)

**ID**: `<rig>-upstream-sync-state` (e.g., `gu-upstream-sync-state`)
**Type**: `pinned`
**Owner**: Mayor (per gu-gal8 — never polecat)
**Status**: `open` for lifetime of opt-in

**Metadata** (`Issue.Metadata`, JSON blob):

```json
{
  "schema_version": 1,
  "rig": "gastown_upstream",
  "state": "idle",
  "upstream_remote": "upstream",
  "upstream_branch": "main",
  "target_branch": "main",
  "last_sync_at": "2026-05-25T14:00:00Z",
  "last_sync_outcome": "success",
  "last_sync_sha": "abc1234def5678",
  "current_attempt": null,
  "paused_until": null,
  "consecutive_failures": 0,
  "attempts": [
    {
      "id": "gu-sync-att-001",
      "started_at": "2026-05-25T14:00:00Z",
      "completed_at": "2026-05-25T14:02:30Z",
      "outcome": "success",
      "upstream_sha": "abc1234def5678",
      "pre_sync_sha": "999888777666",
      "post_sync_sha": "abc1234def5678",
      "strategy": "fast-forward",
      "gate_results": {
        "build": "pass",
        "test": "pass",
        "vet": "pass"
      },
      "actor": "polecat/guzzle"
    }
  ]
}
```

**Bounded history**: `attempts[]` retains last 30 entries (FIFO eviction).

### State Machine

States:
- `idle` — No sync in progress, fork may or may not be current.
- `checking` — Agent is evaluating whether sync is needed.
- `syncing` — Merge/rebase in progress (no conflicts).
- `resolving` — Conflicts detected, agent is resolving them.
- `gating` — Merge done, running CI gates (build, test, vet).
- `pushing` — Gates passed, pushing to origin.
- `failed` — Attempt failed (gates, push, or unresolvable conflict).
- `paused` — Manually or automatically paused (circuit breaker).

Valid transitions:
```
idle → checking       (patrol tick or manual trigger)
checking → idle       (already in sync, nothing to do)
checking → syncing    (upstream ahead, clean merge possible)
checking → resolving  (upstream ahead, conflicts detected)
syncing → gating      (merge completed cleanly)
resolving → gating    (conflicts resolved by agent)
resolving → failed    (agent cannot resolve conflicts)
gating → pushing      (all gates pass)
gating → failed       (gate failure)
pushing → idle        (push succeeded, sync complete)
pushing → failed      (push rejected — race condition)
failed → idle         (retry after backoff)
* → paused           (manual pause or circuit breaker)
paused → idle         (unpause)
```

### Current Attempt (non-null when state ∉ {idle, paused, failed})

```json
{
  "id": "gu-sync-att-002",
  "started_at": "2026-05-25T21:00:00Z",
  "upstream_sha": "def5678abc1234",
  "pre_sync_sha": "abc1234def5678",
  "strategy": "merge",
  "conflicts": [
    "internal/cmd/molecule_await_event.go",
    "internal/cmd/auto_test_pr_pause.go"
  ],
  "resolution_branch": "upstream-sync/gastown_upstream/gu-sync-att-002",
  "polecat_bead": "gu-leg-xyz",
  "gate_results": null,
  "actor": "polecat/dust"
}
```

### Sync Attempt Work Bead (ephemeral, one per attempt)

When the state machine enters `syncing` or `resolving`, a standard
work bead is created and dispatched to a polecat:

**ID**: Auto-generated (e.g., `gu-sync-att-002`)
**Type**: `task`
**Owner**: Mayor (dispatch authority)
**Labels**: `gt:upstream-sync`, `rig:<rig-name>`, `attempt:<n>`

The polecat work bead carries the merge instructions:
- Target branch, upstream SHA, strategy
- Conflict file list (if resolving)
- Gate commands to run post-merge
- Branch naming convention for the work

### Configuration (extends existing rig config)

**File**: `<rig>/settings/config.json` (existing `RigSettings` struct)

```go
type UpstreamSyncConfig struct {
    // Enabled controls whether upstream sync runs for this rig.
    Enabled bool `json:"enabled"`

    // UpstreamRemote is the git remote name for upstream (default: "upstream").
    UpstreamRemote string `json:"upstream_remote,omitempty"`

    // UpstreamBranch is the branch to sync from (default: "main").
    UpstreamBranch string `json:"upstream_branch,omitempty"`

    // TargetBranch is the local branch to sync into (default: "main").
    TargetBranch string `json:"target_branch,omitempty"`

    // Strategy is the merge strategy: "merge" or "rebase" (default: "merge").
    Strategy string `json:"strategy,omitempty"`

    // CadenceMinutes is how often to check for upstream changes (default: 360 = 6h).
    CadenceMinutes int `json:"cadence_minutes,omitempty"`

    // GateCommands are the CI commands to run before pushing.
    // Default: ["go build ./...", "go test ./...", "go vet ./..."]
    GateCommands []string `json:"gate_commands,omitempty"`

    // MaxConsecutiveFailures triggers circuit breaker (default: 3).
    MaxConsecutiveFailures int `json:"max_consecutive_failures,omitempty"`

    // ConflictResolution controls how conflicts are handled:
    // "agent" (default) = dispatch polecat to resolve
    // "escalate" = escalate to human (v1 fallback)
    ConflictResolution string `json:"conflict_resolution,omitempty"`
}
```

Added to `RigSettings`:
```go
type RigSettings struct {
    // ... existing fields ...
    UpstreamSync *UpstreamSyncConfig `json:"upstream_sync,omitempty"`
}
```

### What Persists vs. What Is Computed

| Data | Persists? | Why |
|------|-----------|-----|
| "Is fork in sync?" | No — computed from `git merge-base` | Git refs are authoritative |
| Upstream SHA to sync to | No — computed via `git fetch` | Always use latest |
| Sync state machine state | Yes — pinned bead metadata | Survives session death |
| Attempt history | Yes — pinned bead (last 30) | Observability, debugging |
| Current conflict list | Yes — current_attempt in bead | Agent handoff on session death |
| Gate results | Yes — per-attempt record | Audit trail |
| Config (cadence, gates) | Yes — rig settings JSON | Operator control |
| Conflict resolution work | Yes — polecat work bead | Standard dispatch pattern |
| Branch being worked on | Yes — current_attempt.resolution_branch | Recovery on crash |
| Merge commit content | No — lives in git | Git is authoritative |

### Data Growth

- **Pinned state bead**: Bounded. 30 attempts × ~500 bytes = ~15KB max
  metadata blob per rig. Stable once bounded.
- **Work beads**: Ephemeral, auto-GC'd by beads reaper after closure.
  At 6h cadence, max ~4 per day per rig. Negligible.
- **Git refs**: One branch per active sync attempt (deleted after push).
  At most 1 concurrent branch per rig.

### Access Patterns

| Query | Substrate | Pattern |
|-------|-----------|---------|
| "What state is rig X's sync in?" | Pinned bead metadata | Single bead read |
| "When did rig X last sync?" | Pinned bead `last_sync_at` | Single bead read |
| "Why did the last sync fail?" | Pinned bead `attempts[-1]` | Single bead read |
| "Is there a sync conflict being resolved?" | Pinned bead `current_attempt` | Single bead read |
| "Which rigs have upstream sync enabled?" | Rig settings JSON scan | File read per rig (cheap) |
| "Pause all syncs" | Write `paused_until` on each state bead | N bead writes |
| "Show sync history" | Pinned bead `attempts[]` | Single bead read |

### Schema Evolution

Every metadata blob carries `schema_version`. Evolution strategy:

- **v1 → v2 (additive)**: New fields get defaults. A v2 reader seeing
  a v1 blob fills defaults and rewrites on next state transition.
- **v1 reader seeing v2 blob**: Ignores unknown fields (round-trips
  through `json.RawMessage` for unknown top-level keys).
- **Breaking changes (unlikely)**: Increment schema_version, write
  migration in `internal/upstreamsync/migrate.go`, run at startup.

The metadata struct uses `json.RawMessage` for future-proof round-tripping:
```go
type SyncStateMetadata struct {
    SchemaVersion int             `json:"schema_version"`
    Rig           string          `json:"rig"`
    State         SyncState       `json:"state"`
    // ... typed fields ...
    Extra         json.RawMessage `json:"_extra,omitempty"` // forward-compat
}
```

### Migration from Old Plugin

The old `plugins/sync-upstream/` plugin has no persistent state beyond
ephemeral receipt beads. Migration is:

1. Create pinned state bead for each rig that had sync-upstream enabled.
2. Set initial state to `idle` with `last_sync_at` from the most recent
   receipt bead's timestamp (if any).
3. Disable old plugin (set `.disabled` sentinel or remove plugin dir).
4. Enable new system via rig config.

No data migration needed — the old plugin's receipts are informational
only and can coexist until naturally GC'd.

## Constraints Identified

- **Mayor owns all pinned beads (gu-gal8).** Polecats read state and
  request transitions; they never write the state bead directly.
  Transition requests go through Mayor RPC or a privileged CLI verb.
- **No new storage substrate.** All state in beads (Dolt) or rig
  settings JSON.
- **One sync attempt per rig at a time.** The state machine is serial.
  CAS on the state field prevents concurrent attempts.
- **Gate commands must pass before push.** Gate failure = attempt
  failure. No push without green gates.
- **Branch naming convention**: `upstream-sync/<rig>/<attempt-id>`.
  Ephemeral — cleaned after successful push or stale timeout.
- **Conflict resolution branch must survive session death.** The
  resolution branch exists in git (durable); the work-in-progress
  state on the bead tells the next session where to pick up.
- **Circuit breaker**: 3 consecutive failures → auto-pause. Requires
  human `gt upstream-sync unpause <rig>` to resume.

## Open Questions

1. **Merge vs. rebase strategy default.** The old plugin used merge
   (preserves polecat branch validity). The new system could default
   to rebase (cleaner linear history, required by
   `check-upstream-rebased.sh`). The gate script checks
   `merge-base --is-ancestor upstream/main HEAD` which works for both
   strategies. **Recommendation**: Default to merge for safety; rebase
   as opt-in config. Flag for API/UX dimension.

2. **Who triggers the sync check?** Options:
   (a) Deacon patrol plugin (periodic, like old plugin)
   (b) Witness pre-merge gate (sync before every MQ merge)
   (c) Dedicated event channel (upstream push webhook — not available
   locally)
   **Recommendation**: (a) periodic patrol via deacon, matching existing
   plugin cadence. Additionally, (b) can be a pre-merge optimization.
   Flag for integration dimension.

3. **Conflict resolution agent identity.** Should the conflict-resolving
   polecat be a dedicated "sync polecat" or any available pool polecat?
   Dedicated gives predictability; pool gives flexibility.
   **Recommendation**: Use pool polecat with standard dispatch. The work
   bead carries all context needed. Flag for integration dimension.

4. **What if upstream has force-pushed?** The old plugin doesn't handle
   this. If upstream rebases its main, our `merge-base --is-ancestor`
   check will fail in confusing ways. **Recommendation**: Detect
   force-push (upstream SHA not in our history) → escalate, don't
   auto-resolve. Flag for security dimension.

5. **Concurrency with in-flight polecat work.** The old plugin skips
   sync when polecats have hooked work. The new system should either:
   (a) maintain that guard, (b) sync on a dedicated branch then
   fast-forward main after polecats land. **Recommendation**: (b) —
   sync on a branch, run gates, then submit to MQ like any other MR.
   The refinery handles ordering. Flag for integration dimension.

6. **Fork sync topology preservation.** The existing
   `internal/refinery/fork_sync.go` detects fork-sync MRs and uses
   no-ff merge instead of squash to preserve upstream ancestry.
   The new system must integrate with this — its sync MRs should
   trigger the same preservation logic. **Recommendation**: Label
   sync MRs with `gt:upstream-sync` and have fork_sync.go detect
   them alongside its existing heuristic. Flag for integration.

## Integration Points

- **API & Interface**: `gt upstream-sync status [--rig=...]` reads
  pinned bead state. `gt upstream-sync pause/unpause <rig>` writes
  the bead. `gt rig config <rig> upstream-sync enable` writes
  settings JSON. Thin CLI over data reads/writes.

- **Refinery (fork_sync.go)**: Sync MRs entering the merge queue must
  be recognized by the existing fork-sync topology preservation logic.
  The `gt:upstream-sync` label or branch-name convention
  (`upstream-sync/...`) triggers no-ff merge path.

- **Witness (check-upstream-rebased.sh)**: The gate script is the
  pre-merge check that detects drift. The new system's goal is to
  keep this gate permanently green. The two work together: the system
  proactively syncs; the gate catches regressions.

- **Deacon (patrol plugin)**: The trigger mechanism. Deacon's periodic
  patrol checks the cooldown gate, evaluates whether sync is needed,
  and dispatches work if so.

- **Polecat dispatch**: Conflict resolution uses standard polecat
  dispatch with a work bead. The formula is `mol-polecat-work` with
  args describing the merge conflict to resolve.

- **Scale**: At <10 rigs with upstream sync, the per-rig pinned bead
  pattern is well within bounds. Each bead is ~15KB max. Total system
  state < 150KB. Git fetch is the expensive operation, not state reads.

- **Security**: Force-push detection prevents accepting malicious
  upstream history rewrites. Gate commands (build + test) prevent
  pushing broken code. Agent conflict resolution is sandboxed within
  polecat worktrees — no direct main push.

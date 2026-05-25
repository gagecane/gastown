# API & Interface Design

## Summary

The upstream-sync system replaces the existing `plugins/sync-upstream/run.sh` with a first-class `gt` subsystem that automatically merges `upstream/main` into the fork's `origin/main`, using polecat agents for conflict resolution and a full CI gate before push. The interface must serve three user personas: (1) the **human overseer** checking health and adjusting behavior, (2) the **Deacon/Witness** triggering and monitoring cycles programmatically, and (3) **polecats** dispatched to resolve merge conflicts.

The design follows existing `gt` CLI conventions: cobra subcommands, `--rig` scoping, `--json` output for machine consumption, and bead-backed state. The primary command group is `gt upstream` (with alias `gt us`), mirroring the pattern of `gt refinery`, `gt patrol`, and `gt mq` as rig-level subsystems. Configuration lives in the rig's identity bead (labels + metadata) rather than standalone config files, consistent with how other rig features are enabled/disabled.

## Analysis

### Key Considerations

- **Discoverability**: Users already know `gt status` and `gt doctor` for health checks. The upstream-sync status should surface in both, plus have its own detailed view.
- **Consistency**: Follows the existing pattern where rig-scoped subsystems are `gt <subsystem> <action>` (e.g., `gt refinery queue`, `gt mq list`, `gt patrol scan`).
- **Minimal new concepts**: No new config file formats. Uses bead labels for feature flags (matches `sync-upstream:disabled` pattern already in the plugin). Uses the existing patrol/cycle model for recurring execution.
- **Agent-friendly**: All commands must work headlessly (no interactive prompts) with `--json` for machine-parseable output. The Deacon triggers cycles; polecats run conflict resolution.
- **Safety**: Destructive operations (`gt upstream reset`, `gt upstream force-sync`) require explicit `--confirm` flag. Status/read operations are always safe.
- **Existing infrastructure reuse**: The `upstream` remote is already configured on this repo. The `scripts/check-upstream-rebased.sh` gate script already enforces ancestry. The refinery's `fork_sync.go` already preserves merge topology.

### Options Explored

#### Option A: Standalone command group `gt upstream`

- **Description**: New top-level command group with subcommands for status, sync, pause, history. Lives alongside `gt refinery`, `gt mq`, etc.
- **Pros**:
  - Clean namespace, discoverable via `gt --help`
  - Mirrors existing subsystem pattern (`gt upstream status` like `gt refinery queue`)
  - Room for future subcommands without namespace collision
  - Short alias `gt us` for ergonomics
- **Cons**:
  - Adds a new top-level command (cognitive load in `gt --help`)
  - Needs new Go files in `internal/cmd/`
- **Effort**: Medium

#### Option B: Subcommand under `gt refinery`

- **Description**: Add `gt refinery upstream-sync` since the refinery already handles merge operations.
- **Pros**:
  - Conceptually related (both merge things into main)
  - No new top-level command
- **Cons**:
  - Refinery is a role (agent), not a feature. Upstream sync runs via Deacon patrols, not the Refinery agent.
  - `gt refinery upstream-sync status` is verbose and confusing
  - Conflates two different merge flows (polecat MRs vs. upstream merges)
- **Effort**: Low

#### Option C: Enhanced plugin system (keep as plugin, add CLI wrapper)

- **Description**: Keep the shell script plugin but add `gt plugin run sync-upstream` and `gt plugin status sync-upstream` wrapper commands.
- **Pros**:
  - Minimal code change
  - Plugin system already has gate/cooldown/receipt infrastructure
- **Cons**:
  - The new system is fundamentally different (agent-based conflict resolution, CI gating) — doesn't fit the "dumb script" plugin model
  - Plugin system has no concept of dispatching polecats or managing state machines
  - Would need to rewrite the plugin in Go anyway to get bead-backed state
- **Effort**: Low for wrapper, but doesn't support the full feature

#### Option D: Patrol-based (no dedicated command group)

- **Description**: Implement as a Deacon patrol with `gt patrol` commands for status. No dedicated `gt upstream` command.
- **Pros**:
  - Patrols already have cycle management, timing, receipts
  - Deacon already triggers periodic work
- **Cons**:
  - Patrols are for monitoring/scanning, not for triggering multi-step workflows with agent dispatch
  - Status would be buried in `gt patrol digest` output
  - No natural place for `gt upstream pause` or `gt upstream history`
  - Harder to discover ("how do I check upstream sync status?" → "look in patrol digests")
- **Effort**: Low

### Recommendation

**Option A: `gt upstream` command group** — This is the right abstraction level. It's a rig-level subsystem (like refinery, witness) that happens to be triggered by the Deacon patrol cycle. The implementation *uses* patrol infrastructure internally but exposes a dedicated interface.

## Proposed Interface

### Command Group: `gt upstream`

```
gt upstream [command]

Aliases: gt us

Available Commands:
  status      Show upstream sync health for a rig
  sync        Trigger an immediate sync cycle (bypasses cooldown)
  pause       Pause automatic sync for a rig
  resume      Resume automatic sync for a rig
  history     Show sync history (successes, conflicts, skips)
  config      Show or update upstream sync configuration
```

### `gt upstream status`

The primary "how are things?" command. Surfaces in `gt status` and `gt doctor` output too.

```
$ gt upstream status
Upstream Sync: gastown_upstream
  Remote:     upstream (https://github.com/gastownhall/gastown.git)
  Target:     origin/main
  State:      ✓ synced (0 commits behind)
  Last sync:  2026-05-25 18:30:42 UTC (3h ago)
  Last check: 2026-05-25 21:28:01 UTC (2m ago)
  Cooldown:   4h remaining (next eligible: 2026-05-26 00:30:42)
  Paused:     no

$ gt upstream status --json
{"rig":"gastown_upstream","state":"synced","behind":0,"last_sync":"2026-05-25T18:30:42Z",...}
```

**Flags:**
- `--rig <name>` — Target rig (defaults to current worktree's rig)
- `--json` — Machine-parseable output
- `--all` — Show status for all configured rigs

**States:** `synced`, `behind`, `syncing`, `conflict`, `paused`, `error`

### `gt upstream sync`

Manual trigger. Useful for testing, catching up after a pause, or when the Deacon is down.

```
$ gt upstream sync
Fetching upstream/main...
  upstream/main is 3 commits ahead of origin/main
Merging upstream/main into origin/main...
  Merge clean ✓
Running gates...
  go build ./...    ✓ (12s)
  go test ./...     ✓ (45s)
  go vet ./...      ✓ (3s)
Pushing to origin/main...
  ✓ Synced (abc1234 → def5678, 3 commits merged)
```

**Flags:**
- `--rig <name>` — Target rig
- `--dry-run` — Show what would happen without executing
- `--skip-gates` — Push without running CI (emergency use, requires `--confirm`)
- `--confirm` — Required for `--skip-gates`
- `--force` — Bypass cooldown timer (still runs guards and gates)

**Exit codes:** 0 = success, 1 = conflict (dispatched to polecat), 2 = gate failure, 3 = guard prevented sync

### `gt upstream pause` / `gt upstream resume`

```
$ gt upstream pause --reason "Investigating broken test in upstream"
✓ Upstream sync paused for gastown_upstream
  Resume with: gt upstream resume

$ gt upstream resume
✓ Upstream sync resumed for gastown_upstream
```

**Flags:**
- `--rig <name>` — Target rig
- `--reason <text>` — Required for pause (stored on bead for audit trail)
- `--ttl <duration>` — Auto-resume after duration (e.g., `--ttl 24h`)

### `gt upstream history`

```
$ gt upstream history
  2026-05-25 18:30  ✓ synced    3 commits  (abc1234 → def5678)
  2026-05-25 12:30  ⊘ skipped   merge queue not empty (2 pending)
  2026-05-25 06:30  ✓ synced    1 commit   (012abcd → 345efgh)
  2026-05-24 18:30  ⚡ conflict  resolved by polecat/fury (2 files)
  2026-05-24 12:30  ✓ synced    5 commits  (789abcd → bcd0123)

$ gt upstream history --json --limit 10
[{"time":"2026-05-25T18:30:42Z","result":"synced","commits":3,...},...]
```

**Flags:**
- `--rig <name>` — Target rig
- `--json` — Machine output
- `--limit <n>` — Number of entries (default 10)
- `--since <date>` — Filter by date

### `gt upstream config`

```
$ gt upstream config
  Rig:              gastown_upstream
  Upstream remote:  upstream
  Upstream branch:  main
  Target branch:    main
  Cooldown:         6h
  Max divergence:   100 commits (escalates above this)
  Gates:            go build ./..., go test ./..., go vet ./...
  Conflict mode:    agent (dispatch polecat)

$ gt upstream config --set cooldown=4h
✓ Updated cooldown to 4h
```

**Flags:**
- `--rig <name>` — Target rig
- `--set <key>=<value>` — Update a config value
- `--json` — Machine output

### Configuration Storage

Configuration lives on the rig identity bead as structured metadata (not a standalone file). This follows the existing pattern where rig behavior is controlled via bead labels and metadata.

```
# Labels for feature flags:
upstream-sync:enabled        # Feature is active
upstream-sync:paused         # Temporarily paused (with reason in metadata)

# Metadata (JSON on bead) for tunable values:
{
  "upstream_sync": {
    "remote": "upstream",
    "upstream_branch": "main",
    "target_branch": "main",
    "cooldown": "6h",
    "max_divergence": 100,
    "gates": ["go build ./...", "go test ./...", "go vet ./..."],
    "conflict_mode": "agent"
  }
}
```

### Environment Variables (Override)

For CI and testing, environment variables override bead config:

| Variable | Purpose | Default |
|----------|---------|---------|
| `GT_UPSTREAM_REMOTE` | Override upstream remote name | `upstream` |
| `GT_UPSTREAM_BRANCH` | Override upstream branch | `main` |
| `GT_UPSTREAM_COOLDOWN` | Override cooldown duration | `6h` |
| `GT_UPSTREAM_DRY_RUN` | Force dry-run mode | `false` |
| `GT_UPSTREAM_SKIP_GUARDS` | Skip guards (testing only) | `false` |

### Programmatic API (Go)

```go
package upstream

// Config holds upstream sync configuration for a rig.
type Config struct {
    Remote        string        `json:"remote"`
    UpstreamBranch string      `json:"upstream_branch"`
    TargetBranch  string        `json:"target_branch"`
    Cooldown      time.Duration `json:"cooldown"`
    MaxDivergence int           `json:"max_divergence"`
    Gates         []string      `json:"gates"`
    ConflictMode  string        `json:"conflict_mode"` // "agent" or "escalate"
}

// Status represents the current sync state.
type Status struct {
    State        State     `json:"state"`
    Behind       int       `json:"behind"`
    LastSync     time.Time `json:"last_sync"`
    LastCheck    time.Time `json:"last_check"`
    Paused       bool      `json:"paused"`
    PauseReason  string    `json:"pause_reason,omitempty"`
}

type State string
const (
    StateSynced   State = "synced"
    StateBehind   State = "behind"
    StateSyncing  State = "syncing"
    StateConflict State = "conflict"
    StatePaused   State = "paused"
    StateError    State = "error"
)

// Syncer manages upstream sync operations for a rig.
type Syncer struct { ... }

// NewSyncer creates a syncer from rig configuration.
func NewSyncer(rigRoot string, cfg Config) (*Syncer, error)

// Check fetches upstream and returns current divergence without modifying state.
func (s *Syncer) Check(ctx context.Context) (*Status, error)

// Sync performs a full sync cycle: fetch, merge, gate, push.
// Returns SyncResult with details about what happened.
func (s *Syncer) Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error)

// SyncOptions controls sync behavior.
type SyncOptions struct {
    DryRun     bool
    SkipGates  bool   // Emergency bypass
    Force      bool   // Bypass cooldown
}

// SyncResult describes what happened during a sync cycle.
type SyncResult struct {
    Action    string   // "synced", "fast-forwarded", "skipped", "conflict", "gate-failed"
    Commits   int      // Number of commits merged
    OldSHA    string
    NewSHA    string
    Conflicts []string // Conflicting files (if conflict)
    GateError string   // Gate failure details (if gate-failed)
}
```

### Error Messages

Errors follow the existing `gt` pattern: emoji indicator + short description + actionable fix.

```
✗ Upstream sync failed: merge conflict in 3 files
  Conflicting: internal/cmd/done.go, go.sum, internal/refinery/fork_sync.go
  
  Dispatching polecat for conflict resolution...
  Bead: gu-xyz-abc (priority: P1)

✗ Upstream sync failed: gate failure (go test)
  --- FAIL: TestRefinery/merge_topology (0.42s)
  
  Fix the test failure and retry:
    gt upstream sync --force

✗ Upstream sync skipped: max divergence exceeded (142 commits)
  upstream/main is 142 commits ahead of origin/main (limit: 100)
  
  Review the divergence manually before syncing:
    gt upstream history
    gt upstream sync --confirm

⚠ Upstream sync paused: "Investigating broken test in upstream"
  Paused by canewiw at 2026-05-25 14:30
  Auto-resume: 2026-05-26 14:30 (--ttl 24h)
  
  Resume manually: gt upstream resume
```

### Integration with Existing Commands

**`gt status`** — adds upstream sync line:
```
  Upstream: ✓ synced (0 behind) — last sync 3h ago
```

**`gt doctor`** — adds upstream sync health check:
```
  ✓ Upstream remote configured (upstream → gastownhall/gastown)
  ✓ Upstream sync enabled and running
  ⚠ Fork is 5 commits behind upstream (within tolerance)
```

**`gt vitals`** — includes upstream sync in health dashboard

**Deacon patrol integration** — the Deacon calls `upstream.Syncer.Sync()` on its patrol cycle, respecting the cooldown gate. The Deacon doesn't need special upstream-sync knowledge — it just runs the patrol formula which includes an upstream-sync step.

### Conflict Resolution: Polecat Dispatch

When a merge conflicts, the system:
1. Creates a P1 bead: "upstream-sync: resolve merge conflict in <rig>"
2. Attaches formula `mol-polecat-upstream-conflict` with vars: conflicting files, upstream SHA, base SHA
3. Dispatches to the rig's polecat pool via `gt sling`

The polecat receives a structured assignment:
```
Resolve merge conflict between upstream/main and origin/main.

Conflicting files:
  - internal/cmd/done.go
  - go.sum

Instructions:
1. Fetch both branches
2. Attempt merge, resolve conflicts
3. Run full gate suite (go build, go test, go vet)
4. If gates pass: commit merge and gt done
5. If gates fail after resolution: escalate (may indicate upstream regression)
```

## Constraints Identified

1. **Single-writer invariant**: Only one sync operation can run per rig at a time. The Deacon/manual trigger must acquire a lock (bead-based mutex) before starting. This prevents the Deacon and a manual `gt upstream sync` from racing.

2. **Refinery coordination**: Sync must not push while Refinery has in-flight merges to the same target branch. The existing guard (merge queue empty) is necessary. Consider: sync acquires the same lock that Refinery uses for push.

3. **Gate commands must be configurable per-rig**: Different rigs may have different build/test commands. The gate list comes from rig config, not hardcoded.

4. **`upstream` remote must exist**: The `scripts/check-upstream-rebased.sh` already auto-adds it, but `gt upstream` commands should verify and error clearly if missing.

5. **No force-push to `origin/main`**: The system only does merge commits or fast-forwards. Force-push is never acceptable (consistent with project CLAUDE.md rules).

6. **The old plugin must be removed**: Having two systems (`plugins/sync-upstream/run.sh` AND `gt upstream`) targeting the same branch is a race condition. Deprecation path: rename to `run.sh.disabled`, remove after new system is proven.

## Open Questions

1. **Should `gt upstream sync` work from any directory, or only from within a rig worktree?** Current lean: accept `--rig` flag from anywhere in town, default to current rig when inside a worktree.

2. **Should conflict-resolution polecats use a dedicated worktree or the crew worktree?** The old plugin used `crew/gagecane`. Polecats have their own worktrees. Recommendation: use a dedicated `upstream-sync/` worktree within the rig (not crew, not a polecat worktree) to avoid conflicts with both.

3. **Should the `--skip-gates` escape hatch exist at all?** It violates the "full CI gate before push" requirement from the PRD. Counter-argument: emergencies happen (e.g., gate is broken by a flaky test, upstream is blocked). Recommendation: keep it but require `--confirm` and log an escalation when used.

4. **What's the right cooldown default?** The old plugin used 6h. With agent-based conflict resolution (faster than human), could reduce to 2-4h. The Deacon patrol cycle frequency is the practical lower bound.

5. **Should `gt upstream history` show data from beads (ephemeral receipts) or a persistent log?** Beads receipts are digestible (can be compacted away). For reliable history, consider a lightweight append-only log file (`.upstream-sync-history.jsonl`) alongside bead receipts.

## Integration Points

- **Deacon** — triggers sync cycles on patrol schedule; respects cooldown
- **Refinery** — shares the target branch push lock; `fork_sync.go` topology preservation still needed for conflict-resolution MRs
- **Witness** — monitors sync health; alerts if fork falls behind threshold
- **`scripts/check-upstream-rebased.sh`** — the enforcement gate that makes sync correctness critical (if sync fails, all polecat work is blocked)
- **Polecat pool** — receives conflict-resolution dispatches via `gt sling`
- **`gt status` / `gt doctor` / `gt vitals`** — surface sync health in existing dashboards
- **Mayor** — receives escalations for max-divergence or repeated failures

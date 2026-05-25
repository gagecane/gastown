# API & Interface Design

> Convoy leg: **api** for the `upstream-sync` design (cv-hpnja).
> Problem: Automatically keep fixes from `gastownhall/gastown` (upstream)
> merged into `gagecane/gastown` (fork) via the local `gastown_upstream` rig.

## Summary

The upstream-sync feature keeps the fork (`gagecane/gastown`, the `origin`
remote) current with the upstream open-source repo (`gastownhall/gastown`,
the `upstream` remote) by periodically merging `upstream/main` into the
fork's main branch. The user-facing surface is intentionally small: a
**plugin-based daemon** (already prototyped in `plugins/sync-upstream/`)
handles the heavy lifting, a **`gt upstream` CLI verb** provides operator
visibility and control, and **rig-level configuration** (already partially
wired via `gt rig add --upstream-url`) controls opt-in and tuning.

The design biases toward **zero-interaction steady state**: once a rig is
configured with an upstream URL, the sync plugin runs on a cooldown cadence
with no human involvement. The CLI exists for three scenarios only:
(1) initial setup, (2) operational visibility ("what's the sync status?"),
and (3) incident response ("pause the sync, there's a conflict"). All three
must be fast, discoverable, and safe to run under pressure.

## Analysis

### Key Considerations

- **Existing infrastructure covers 80% of the work.** The
  `plugins/sync-upstream/run.sh` script already implements the full
  merge logic with 7 safety guards, conflict escalation, fast-forward
  detection, and receipt recording. The `internal/refinery/fork_sync.go`
  module preserves merge topology during squash merges. The
  `scripts/check-upstream-rebased.sh` gate validates ancestry. The API
  design should expose and control this machinery, not reinvent it.

- **Two user populations.** Rig owners (one-time setup, occasional
  "is my fork current?" check) and operators/on-call (kill-switch under
  fire, status during incidents). The interface must serve both without
  conflicting.

- **The happy path is invisible.** A well-functioning upstream sync
  never surfaces to the user. The only time they interact with it is
  setup (once) or trouble (rare). This means discoverability matters
  less than clarity-under-stress.

- **Fork rigs are the minority.** Most Gas Town rigs don't have an
  upstream remote. The feature must be invisible for non-fork rigs
  (no noise in `gt status`, no extra help text) and discoverable for
  fork rigs (show sync status when relevant).

- **Safety is non-negotiable.** The sync modifies `main` (or the
  integration branch) across the organization's fork. A bad merge or
  race condition can break every polecat's work base. All the existing
  guards in `run.sh` must be preserved in any API redesign.

- **Naming must be intuitive.** "upstream sync" is the natural phrase.
  It matches the git remote name (`upstream`), the existing script name
  (`sync-upstream`), and the mental model ("sync from upstream").

### Options Explored

#### Option 1: `gt upstream` verb tree (recommended)

- **Description**: A new top-level subcommand:
  ```
  gt upstream status    [--rig=<rig>] [--json]
  gt upstream sync      [--rig=<rig>] [--dry-run]
  gt upstream pause     [--rig=<rig>] [--duration=<dur>]
  gt upstream resume    [--rig=<rig>]
  gt upstream log       [--rig=<rig>] [--limit=N]
  ```
  Implemented in `internal/cmd/upstream.go`, mirroring the shape of
  `gt auto-test-pr` and `gt patrol`.

- **Pros**:
  - Natural language: `gt upstream status` reads as English.
  - Discoverable: shows up in `gt --help` under Infrastructure or
    Work Management.
  - Consistent with existing patterns (`gt patrol`, `gt auto-test-pr`).
  - Room to grow: `gt upstream diff`, `gt upstream conflicts` could
    land later without reshuffling.
  - The `upstream` noun is already meaningful in this codebase (the
    remote name, the rig suffix, the script name).

- **Cons**:
  - Occupies a top-level noun. Mitigated: the word "upstream" has no
    other meaning in Gas Town CLI today.
  - Users might confuse `gt upstream sync` (fork sync) with
    `gt hooks sync` or `gt wl sync`. Mitigated: help text and
    subcommand description make the scope clear.

- **Effort**: Low. The sync logic already exists in `run.sh`; this is
  a Go wrapper that calls the same git operations with better error
  handling and structured output.

#### Option 2: `gt rig upstream <verb>` (nested under rig)

- **Description**: Upstream sync is a property of a rig, so nest it:
  ```
  gt rig upstream status --rig=gastown_upstream
  gt rig upstream sync   --rig=gastown_upstream
  gt rig upstream pause  --rig=gastown_upstream
  ```

- **Pros**:
  - Semantically correct: upstream sync IS a rig-level operation.
  - No new top-level command; `gt rig` already exists.
  - `--rig` flag is naturally scoped.

- **Cons**:
  - Deeply nested: 3 levels (`gt rig upstream status`) is ergonomically
    heavy for an incident-response command.
  - `gt rig` is already crowded (15+ subcommands).
  - Inconsistent with `gt auto-test-pr` which is also per-rig but
    lives at top level.
  - Typing under fire: `gt rig upstream pause` vs `gt upstream pause`
    — the shorter form wins when adrenaline is high.

- **Effort**: Low (same implementation, different cobra tree location).

#### Option 3: Plugin-only (no dedicated CLI verbs)

- **Description**: Keep the existing `plugins/sync-upstream/run.sh` as
  the entire interface. Operators interact via:
  ```
  gt patrol run sync-upstream         # manual trigger
  gt patrol pause sync-upstream       # pause
  touch plugins/sync-upstream/.disabled  # kill-switch
  ```

- **Pros**:
  - Zero new code in `gt` binary.
  - Plugin system already handles cooldown, receipts, and gating.
  - Minimal surface area.

- **Cons**:
  - No structured status output (only receipt beads to grep through).
  - Kill-switch is a sentinel file — terrible UX under fire.
  - No `--dry-run` path to preview what a sync would do.
  - No JSON output for monitoring/dashboards.
  - `gt patrol run sync-upstream` doesn't exist today; `gt patrol` is
    digest-shaped, not run-shaped.
  - Bash script can't easily provide the rich error messages and
    recovery suggestions that a Go implementation can.

- **Effort**: Zero new code — but rejected on operational grounds.
  The plugin remains the *execution engine*; the CLI wraps it with
  better UX.

#### Option 4: Fold into `gt refinery` (since refinery handles merges)

- **Description**: Since the Refinery already has fork-sync topology
  preservation (`fork_sync.go`), make upstream sync a Refinery
  responsibility:
  ```
  gt refinery upstream-sync --rig=gastown_upstream
  gt refinery status --include-upstream
  ```

- **Pros**:
  - Architecturally honest: the Refinery is the merge authority.
  - Reuses Refinery's existing git operations and error handling.

- **Cons**:
  - Conflates two concerns: Refinery merges *polecat work* into main;
    upstream sync merges *external commits* into the integration branch.
    These are different lifecycles with different safety guards.
  - `gt refinery` is an agent management command, not an operator
    workflow command. Mixing them confuses the mental model.
  - The sync plugin explicitly avoids running on refinery clones
    (per `plugin.md`: "operates on `crew/gagecane/<rig>` worktrees,
    NOT on refinery/mayor clones").

- **Effort**: Medium (requires refactoring Refinery's concerns).
  Rejected on separation-of-concerns grounds.

### Recommendation

Adopt **Option 1**: `gt upstream` as a top-level verb tree.

#### Proposed CLI Surface

```
gt upstream status   [--rig=<rig>] [--json]     # What's the sync state?
gt upstream sync     [--rig=<rig>] [--dry-run]  # Trigger a sync now
gt upstream pause    [--rig=<rig>] [--duration=<dur>]  # Pause syncing
gt upstream resume   [--rig=<rig>]              # Resume after pause
gt upstream log      [--rig=<rig>] [--limit=N]  # Recent sync history
```

**Omitted from v1** (defer to v2):
- `gt upstream diff` — show what upstream has that fork doesn't
- `gt upstream conflicts` — predict merge conflicts before sync
- `gt upstream cherry-pick` — selective sync of specific commits

#### Detailed Verb Specifications

##### `gt upstream status`

Shows sync health for one or all fork rigs.

```
$ gt upstream status
RIG                UPSTREAM                           BEHIND  STATE       LAST-SYNC     NEXT
gastown_upstream   gastownhall/gastown                6       idle        2h ago        4h
```

With `--json`:
```json
{
  "version": 1,
  "rigs": [
    {
      "name": "gastown_upstream",
      "upstream_url": "https://github.com/gastownhall/gastown.git",
      "upstream_branch": "main",
      "integration_branch": "main",
      "commits_behind": 6,
      "state": "idle",
      "last_sync_at": "2026-05-25T21:30:00Z",
      "last_sync_result": "merged",
      "next_sync_at": "2026-05-26T03:30:00Z",
      "paused_until": null,
      "conflict_files": null
    }
  ]
}
```

States: `idle` | `syncing` | `paused` | `conflicted` | `disabled`

**Non-fork rigs are omitted** — `gt upstream status` only shows rigs
with a configured upstream URL. If no rigs have upstream, print:
```
No rigs with upstream remotes configured.
  To add: gt rig add <name> --upstream-url=<url>
  Or configure existing: gt rig config <name> --upstream-url=<url>
```

##### `gt upstream sync`

Trigger an immediate sync. Respects all safety guards from the plugin.

```
$ gt upstream sync --rig=gastown_upstream
Fetching upstream/main...
  origin/main is 6 commits behind upstream/main
Checking guards:
  ✓ Merge queue empty
  ✓ No polecats in-flight
  ✓ Working tree clean
Merging upstream/main into main...
  ✓ Merged cleanly (fast-forward)
  ✓ Pushed to origin/main
```

With `--dry-run`:
```
$ gt upstream sync --rig=gastown_upstream --dry-run
Would sync gastown_upstream:
  upstream/main has 6 commits not in origin/main
  Guards: all pass
  Strategy: fast-forward (no merge commit needed)
  No changes made (dry run)
```

If guards fail:
```
$ gt upstream sync --rig=gastown_upstream
Checking guards:
  ✗ Merge queue not empty (2 pending MRs)
  → Skipping sync. Retry after queue drains, or use --force to override.
```

`--force` overrides soft guards (merge queue, polecat in-flight) but
NOT hard guards (dirty worktree, paused state). Documented as
dangerous and logged.

##### `gt upstream pause`

```
$ gt upstream pause --rig=gastown_upstream --duration=24h
✓ Upstream sync paused for gastown_upstream until 2026-05-26T23:30:00Z
  In-flight sync (if any) will complete normally.
  Resume with: gt upstream resume --rig=gastown_upstream
```

Without `--duration`: pauses indefinitely (until explicit resume).

##### `gt upstream resume`

```
$ gt upstream resume --rig=gastown_upstream
✓ Upstream sync resumed for gastown_upstream
  Next sync: ~6h (cooldown cadence)
```

##### `gt upstream log`

```
$ gt upstream log --rig=gastown_upstream --limit=5
TIME                 RESULT      COMMITS  DETAILS
2026-05-25 21:30     merged      3        abc1234..def5678
2026-05-25 15:30     skipped     0        merge queue not empty (1 pending)
2026-05-24 21:30     merged      8        112233..445566 (merge commit)
2026-05-24 15:30     fast-fwd    2        aabb..ccdd
2026-05-23 21:30     conflicted  -        escalated (internal/cmd/foo.go)
```

#### Configuration Interface

##### Rig-level config (in rig manifest / `rigs.json`)

Already partially implemented via `gt rig add --upstream-url`:

```json
{
  "gastown_upstream": {
    "url": "https://github.com/gagecane/gastown",
    "upstream_url": "https://github.com/gastownhall/gastown.git",
    "push_url": "",
    "upstream_sync": {
      "enabled": true,
      "upstream_branch": "main",
      "integration_branch": "main",
      "cadence": "6h",
      "auto_escalate_conflicts": true
    }
  }
}
```

New fields under `upstream_sync`:
- `enabled` — master toggle (default: `true` if `upstream_url` is set)
- `upstream_branch` — branch to track on upstream (default: `main`)
- `integration_branch` — local branch to merge into (default: `main`)
- `cadence` — cooldown between syncs (default: `6h`, matching existing
  plugin gate)
- `auto_escalate_conflicts` — file a bead on conflict (default: `true`)

##### Environment variables

For testing and override scenarios:

| Variable | Purpose | Default |
|----------|---------|---------|
| `GT_UPSTREAM_SYNC_DISABLED` | Global kill-switch | unset |
| `GT_UPSTREAM_SYNC_CADENCE` | Override cadence for all rigs | per-rig config |
| `GT_UPSTREAM_SYNC_DRY_RUN` | Make all syncs dry-run | `false` |

Environment variables override config but are NOT persisted. Use for
debugging and emergency situations only.

##### Sentinel file (backward compatibility)

The existing `plugins/sync-upstream/.disabled` sentinel is preserved as
a low-level kill-switch. `gt upstream pause` writes this file (plus
the structured pause state); `gt upstream resume` removes it. This means
operators who don't have `gt` available (e.g., SSH'd into a box) can
still stop syncing by touching the file directly.

#### Programmatic API (Go package)

```go
package upstream

// Config holds per-rig upstream sync configuration.
type Config struct {
    Enabled              bool          `json:"enabled"`
    UpstreamBranch       string        `json:"upstream_branch"`
    IntegrationBranch    string        `json:"integration_branch"`
    Cadence              time.Duration `json:"cadence"`
    AutoEscalateConflict bool          `json:"auto_escalate_conflicts"`
}

// Status represents the current sync state for a rig.
type Status struct {
    RigName           string     `json:"rig_name"`
    UpstreamURL       string     `json:"upstream_url"`
    CommitsBehind     int        `json:"commits_behind"`
    State             SyncState  `json:"state"`
    LastSyncAt        *time.Time `json:"last_sync_at"`
    LastSyncResult    string     `json:"last_sync_result"`
    NextSyncAt        *time.Time `json:"next_sync_at"`
    PausedUntil       *time.Time `json:"paused_until"`
    ConflictFiles     []string   `json:"conflict_files,omitempty"`
}

// SyncState represents the lifecycle state.
type SyncState string
const (
    StateIdle       SyncState = "idle"
    StateSyncing    SyncState = "syncing"
    StatePaused     SyncState = "paused"
    StateConflicted SyncState = "conflicted"
    StateDisabled   SyncState = "disabled"
)

// SyncResult records the outcome of a sync attempt.
type SyncResult struct {
    RigName     string    `json:"rig_name"`
    Result      string    `json:"result"` // merged|fast-fwd|skipped|conflicted|error
    CommitRange string    `json:"commit_range,omitempty"`
    CommitCount int       `json:"commit_count"`
    Conflicts   []string  `json:"conflicts,omitempty"`
    SkipReason  string    `json:"skip_reason,omitempty"`
    Duration    time.Duration `json:"duration"`
    Timestamp   time.Time `json:"timestamp"`
}

// Syncer performs upstream sync operations for a rig.
type Syncer struct { /* ... */ }

// NewSyncer creates a syncer for the given rig.
func NewSyncer(rigPath string, cfg Config) *Syncer

// Check runs all safety guards without performing the merge.
func (s *Syncer) Check(ctx context.Context) (*GuardResult, error)

// Sync performs the actual merge (after checking guards).
func (s *Syncer) Sync(ctx context.Context, opts SyncOpts) (*SyncResult, error)

// Status returns the current sync state.
func (s *Syncer) Status(ctx context.Context) (*Status, error)
```

#### Error Messages and Help Text

##### Error: upstream not configured
```
Error: rig "myrig" has no upstream URL configured.

To configure upstream sync:
  gt rig config myrig --upstream-url=https://github.com/org/repo.git

Or when creating the rig:
  gt rig add myrig https://github.com/fork/repo --upstream-url=https://github.com/org/repo.git
```

##### Error: merge conflict
```
Error: merge conflict syncing gastown_upstream

Conflicting files:
  internal/cmd/foo.go
  internal/cmd/bar.go

The sync has been paused automatically. To resolve:
  1. cd /home/user/gt/gastown_upstream/crew/gagecane
  2. git fetch upstream main
  3. git merge upstream/main
  4. Resolve conflicts, commit, and push
  5. gt upstream resume --rig=gastown_upstream

Or to dismiss and retry later:
  gt upstream resume --rig=gastown_upstream
  (The next sync attempt will try the merge again)
```

##### Error: guards failed
```
Sync skipped for gastown_upstream:
  ✗ Merge queue has 2 pending MRs

The sync will retry automatically on the next cadence tick (in ~6h).
To force sync now (caution: may conflict with in-flight work):
  gt upstream sync --rig=gastown_upstream --force
```

##### Help text style

```
$ gt upstream --help
Keep fork rigs synchronized with their upstream repositories.

When a rig has an upstream URL configured (via gt rig add --upstream-url),
the upstream sync plugin periodically merges upstream changes into the
fork's main branch. This command provides visibility and control over
that process.

Usage:
  gt upstream [command]

Available Commands:
  log         Show recent sync history
  pause       Pause upstream syncing for a rig
  resume      Resume upstream syncing for a rig
  status      Show upstream sync status
  sync        Trigger an immediate upstream sync

Flags:
  -h, --help   help for upstream

Use "gt upstream [command] --help" for more information about a command.
```

#### Discoverability

Users discover this feature via:

1. **`gt rig add --upstream-url`** — the flag name signals that
   upstream-aware behavior exists. Help text mentions `gt upstream`.
2. **`gt status`** — when a rig is behind upstream, show a one-liner:
   ```
   gastown_upstream: 6 commits behind upstream (last sync: 2h ago)
   ```
3. **`gt doctor`** — the existing `rig_config_sync_check.go` can flag
   rigs with upstream URLs but no recent successful sync.
4. **Escalation beads** — when a conflict occurs, the escalation bead
   body includes the exact `gt upstream` commands to diagnose and resolve.
5. **`gt --help`** — the `upstream` command is listed under Infrastructure.

#### Naming Conventions

| Concept | CLI name | Config key | Go type |
|---------|----------|------------|---------|
| The feature | `upstream` | `upstream_sync` | `upstream.Syncer` |
| A sync attempt | sync | - | `upstream.SyncResult` |
| The tracked remote | upstream | `upstream_url` | string |
| The tracked branch | upstream/main | `upstream_branch` | string |
| The local target | main | `integration_branch` | string |
| Pause state | paused | `paused_until` | `*time.Time` |

These names align with:
- Git terminology (`upstream` remote, `fetch`, `merge`)
- Existing Gas Town patterns (`pause`/`resume` from `auto-test-pr`)
- The existing rig config field name (`upstream_url`)

## Constraints Identified

- **Hard: merge-only, never rebase.** Per `plugins/sync-upstream/plugin.md`:
  "Merging (vs. rebasing) keeps polecat branches based on `gagecane/gt`
  valid — no orphaning." The API must NOT offer a rebase option.

- **Hard: all 7 safety guards must pass before merge.** The existing
  plugin guards (rig not parked, upstream remote exists, not already
  current, merge queue empty, no polecats in-flight, clean worktree,
  on correct branch) are safety-critical. The API exposes them in
  `--dry-run` output but does not allow bypassing hard guards.

- **Hard: conflict = abort + escalate.** Per plugin.md: conflicts cause
  `git merge --abort`, an escalation bead, and no push. The API must
  preserve this behavior — never leave a conflicted merge state.

- **Hard: operates on crew worktrees, not refinery.** The sync runs on
  `<rig>/crew/gagecane/` worktrees. The API must never touch refinery
  or mayor clones.

- **Soft: cooldown-gated.** The plugin runs on a 6h cooldown. The CLI
  `sync` command should respect this by default but allow override (the
  operator is explicitly requesting a sync).

- **Soft: idempotent.** Running `gt upstream sync` when already current
  is a no-op with exit code 0. Running `gt upstream pause` when already
  paused extends the pause (or is a no-op if no duration given).

- **Soft: backward-compatible with plugin.** The existing
  `plugins/sync-upstream/run.sh` continues to work. The Go
  implementation is the "next generation" — both can coexist during
  transition, with the Go version gradually replacing the shell script.

## Open Questions

1. **Should `gt upstream sync` bypass the cooldown timer?** The manual
   invocation implies intent. Recommendation: yes — manual sync always
   runs (unless guards fail). The cooldown only governs the daemon.
   Confirm with **scalability leg**.

2. **Which branch does the `gastown_upstream` rig sync to?** The plugin
   references both `gagecane/gt` (an integration branch) and `main`.
   The current repo shows `origin` is `gagecane/gastown` (the fork) and
   the main branch is `main`. Recommendation: the config specifies
   `integration_branch` explicitly; default to `main`.
   Confirm with **data model leg**.

3. **Should `gt upstream status` appear in `gt status` output?** Adding
   a "behind upstream" line to the global status view improves
   discoverability but adds noise for non-fork rigs. Recommendation:
   only show if the rig has an upstream URL AND is >0 commits behind.
   Confirm with **UX leg**.

4. **Who owns the sync state?** The plugin uses receipt beads. The Go
   implementation should use a pinned state bead (like `auto-test-pr`'s
   town-state bead) for structured state, falling back to receipt beads
   for the audit log. Confirm with **data model leg**.

5. **Should the `--force` flag exist?** It overrides soft guards (merge
   queue, polecat in-flight) but is dangerous. Alternative: require
   `--force --rig=<name>` (explicit rig prevents accidental town-wide
   force). Confirm with **security leg**.

6. **Merge commit message format.** Currently the plugin uses `--no-edit`
   (git's default merge message). Should we standardize to something
   machine-readable like `merge: sync upstream/main into main (gt-upstream)`?
   Confirm with **data model leg** (for grep-ability and audit).

7. **How does `gt upstream` interact with the refinery's fork-sync
   topology preservation?** When a polecat's branch has already merged
   upstream (detected by `preserveForkSyncTopology`), the refinery uses
   no-ff merge. Does `gt upstream sync` need to coordinate with this, or
   are they independent? Recommendation: independent — `gt upstream`
   syncs the *base branch*, refinery handles *polecat branches*.
   Confirm with **integration leg**.

## Integration Points

- **Data model leg**: Defines the pinned state bead schema (sync state,
  pause duration, last result, conflict files). The CLI reads/writes
  this structure.

- **Security leg**: Validates the `--force` flag behavior and ensures
  the upstream URL can't be spoofed or redirected (e.g., a compromised
  upstream remote pointing to a malicious repo).

- **Scalability leg**: Owns the cooldown/cadence logic, circuit-breaker
  behavior (if N consecutive syncs fail, pause automatically), and
  multi-rig scheduling (stagger syncs to avoid thundering herd on
  GitHub).

- **Integration leg**: Coordinates with `internal/refinery/fork_sync.go`
  (topology preservation), `scripts/check-upstream-rebased.sh` (gate),
  and `plugins/sync-upstream/run.sh` (existing implementation to
  migrate from).

- **UX leg**: Owns the `gt status` integration, error message content,
  and the escalation bead body format (what instructions does the
  conflict-resolution bead show to the operator?).

- **Existing Gas Town surfaces touched**:
  - `internal/cmd/upstream.go` (new file)
  - `internal/upstream/` (new package: `Config`, `Syncer`, `Status`)
  - `internal/rig/manager.go` (extend `RigEntry` with `UpstreamSync` config)
  - `internal/cmd/status.go` (add upstream-behind indicator)
  - `internal/doctor/` (add upstream-health check)
  - `plugins/sync-upstream/run.sh` (preserved; Go version is the replacement path)
  - `scripts/check-upstream-rebased.sh` (unchanged; consumed by gate)

---

*Convoy leg `api` for upstream-sync (cv-hpnja). Sibling legs: `data`,
`ux`, `scale`, `security`, `integration`. Synthesis: `gu-syn-jrtqq`.*

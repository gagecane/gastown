# Data Model Design

## Summary

The upstream-sync feature keeps `gagecane/gastown` (the fork, tracked as
`origin`) in sync with `gastownhall/gastown` (upstream) via the local
`gastown_upstream` rig. The data model needs are minimal: **the feature is
almost entirely stateless.** The existing `sync-upstream` plugin already
demonstrates the pattern — it runs as a shell script, checks preconditions
against existing data (rig config, git refs, beads labels, merge queue
state), performs a merge or fast-forward, and records an ephemeral receipt.
No new database, no new tables, no schema migrations are needed.

The key data-model insight is that git itself IS the primary data store for
this feature. The merge topology (upstream ancestry reachable from the fork
branch) is the source of truth for "are we synced?" — checked via
`git merge-base --is-ancestor`. The only supplementary state is (a) per-rig
configuration indicating which rigs participate and their upstream URLs, and
(b) lightweight operational receipts tracking sync history. Both fit cleanly
into existing substrates: `RigConfig`/`RigSettings` JSON for configuration,
and ephemeral beads for receipts.

## Analysis

### Key Considerations

- **Git is the source of truth.** The question "is the fork up to date with
  upstream?" is answered by `git merge-base --is-ancestor upstream/main
  origin/gagecane/gt`. No separate tracking table needed.
- **The plugin already exists.** `plugins/sync-upstream/` is a working v1
  implementation with safety guards, conflict handling, and receipt tracking.
  The data model question is "what does the *productionized* version need
  beyond what the shell script uses?"
- **Existing config has the upstream URL.** `RigConfig.UpstreamURL` and
  `RigEntry.UpstreamURL` already exist in `internal/config/types.go` (lines
  660, 693). The fork workflow plumbing is partially in place.
- **The refinery already handles fork-sync topology.** `internal/refinery/
  fork_sync.go` detects when a branch has integrated upstream commits and
  uses a no-ff merge instead of squash to preserve ancestry. This means the
  data model for "a sync happened" is embedded in git's DAG — no separate
  bookkeeping required.
- **Ephemeral receipts fit existing patterns.** The plugin uses
  `bd create ... --ephemeral` for run receipts. These are auto-purged by
  `PurgeClosedEphemerals` during Dolt sync, preventing unbounded growth.
- **The crew worktree is the execution context.** Sync runs in
  `<rig>/crew/gagecane/` — a real git clone dedicated to integration work.
  This is already set up; no new worktree or clone needs to be provisioned.

### Options Explored

#### Option 1: Zero new data — plugin as-is with git as state (RECOMMENDED)

- **Description**: Keep git as the source of truth. Rig config provides
  `upstream_url` and the integration branch name. The plugin checks
  preconditions against live git state and beads labels each run. Receipts
  are ephemeral beads. No persistent sync-state bead, no tracking table.
- **Pros**:
  - Simplest possible data model — no new schema to version or migrate
  - Git merge-base checks are authoritative and race-free
  - No split-brain risk between "sync state tracker" and actual git state
  - Ephemeral receipts auto-purge; no unbounded growth
  - Already working in production (`plugins/sync-upstream/run.sh`)
- **Cons**:
  - No queryable history beyond `git log --merges`
  - "When was the last sync?" requires git log parsing, not a quick lookup
  - No structured audit trail (just ephemeral receipt beads)
- **Effort**: Low (already implemented)

#### Option 2: Pinned state bead per rig (like auto-test-pr)

- **Description**: One Mayor-owned pinned bead `<prefix>-upstream-sync-state`
  per participating rig. Tracks last sync timestamp, consecutive failure
  count, conflict history, and current sync state.
- **Pros**:
  - Queryable sync history via `bd show`
  - Circuit breaker logic (pause after N failures) has a natural home
  - Consistent with the auto-test-pr pinned-bead pattern
  - Mayor-owned per gu-gal8
- **Cons**:
  - Adds bookkeeping that can drift from git reality
  - Requires CAS semantics if sync could race (it shouldn't — plugin has a
    6h cooldown and guard checks)
  - More complexity for a feature whose "state" is fundamentally just "is
    upstream/main an ancestor of origin/gagecane/gt?"
  - Overkill for a feature that runs at most once per 6h with a single
    outcome (success/skip/conflict-escalate)
- **Effort**: Medium

#### Option 3: Dolt table for sync history

- **Description**: A `sync_history` table in the rig's Dolt database with
  columns: `rig`, `timestamp`, `from_sha`, `to_sha`, `result`, `conflict_files`.
- **Pros**:
  - Full relational query capability over sync history
  - Easy to answer "show me all syncs that conflicted in the last month"
  - Structured conflict file tracking
- **Cons**:
  - Adds a new table to the operational surface (schema, migrations)
  - Dolt is already under load from beads; additional tables add latency
  - Violates the "don't add storage substrates" principle
  - Feature simplicity doesn't justify relational storage — syncs happen
    ≤4 times/day across all rigs
- **Effort**: High

#### Option 4: JSON state file on disk

- **Description**: `<rig>/crew/gagecane/.sync-state.json` on the local
  filesystem, tracking last sync, failure count, etc.
- **Pros**:
  - Simple to read/write, no Dolt dependency
  - Survives server restarts (it's a file)
  - Easy for shell scripts to consume
- **Cons**:
  - Not replicated — lost if disk dies
  - No audit trail visible to other agents
  - Another "where does state live?" location to remember
  - Git already provides the authoritative state
- **Effort**: Low-Medium

### Recommendation

**Option 1 (zero new data — git as state, ephemeral receipts).** The
upstream-sync feature is fundamentally a git operation with precondition
checks. Adding a tracking layer on top introduces split-brain risk for
negligible benefit. The only "history" worth retaining is the git commit
graph itself (merge commits from sync) and escalation beads on conflict.
Both already exist.

**Enhancement to Option 1**: If circuit-breaker logic is desired (pause sync
after N consecutive conflicts), add a single label to the rig identity bead:
`sync-upstream:paused-until:<ISO timestamp>`. This is the existing config
mechanism — no new schema, no new bead type, trivially inspectable via
`bd show`.

## Data Structures

### Per-rig configuration (existing, no changes needed for v1)

```go
// Already in internal/config/types.go
type RigEntry struct {
    GitURL      string `json:"git_url"`                    // origin (fork)
    PushURL     string `json:"push_url,omitempty"`         // optional push URL
    UpstreamURL string `json:"upstream_url,omitempty"`     // upstream (e.g., gastownhall/gastown)
    // ...
}

type RigConfig struct {
    GitURL      string `json:"git_url"`
    PushURL     string `json:"push_url,omitempty"`
    UpstreamURL string `json:"upstream_url,omitempty"`     // same field
    // ...
}
```

### Runtime state (derived from git, not persisted)

```
Computed per sync tick:
  - upstream/main SHA              ← git rev-parse upstream/main
  - origin/<integration-branch> SHA ← git rev-parse origin/gagecane/gt
  - is_ancestor(upstream, origin)   ← git merge-base --is-ancestor
  - divergence direction            ← ancestor check both ways
  - working_tree_clean             ← git status --porcelain
  - merge_queue_empty              ← gt refinery queue <rig>
  - polecats_idle                  ← bd list (hook_bead check)
```

### Precondition guards (existing data, read-only)

| Guard | Data source | Location |
|-------|-------------|----------|
| Rig not parked/docked | Rig identity bead labels | `.beads/` (Dolt) |
| Integration branch exists | Git remote refs | `origin/gagecane/gt` |
| Upstream ref exists | Git remote refs | `upstream/main` |
| Working tree clean | Git status | Crew worktree |
| Merge queue empty | Refinery queue | `gt refinery queue` |
| No in-flight polecat work | Beads query | `bd list` |
| sync-upstream:disabled label | Rig identity bead | `.beads/` (Dolt) |

### Receipts (ephemeral beads, auto-purged)

```
Title: "sync-upstream: <result> across <N> rigs"
Type: chore
Labels: ["type:plugin-run", "plugin:sync-upstream", "result:<success|skipped|failure>"]
Flags: --ephemeral
Description: Per-rig summary lines
```

These are fire-and-forget audit breadcrumbs. `PurgeClosedEphemerals` removes
them during `gt dolt sync`, keeping the database lean.

### Escalation beads (on conflict, standard bead lifecycle)

```
Created by: gt escalate -s medium "sync-upstream: merge conflict in <rig>"
Payload:
  - Conflicting file list (from git diff --name-only --diff-filter=U)
  - Manual resolution instructions (cd, fetch, merge)
  - Rig name, timestamps
```

### Rig identity bead labels (existing mechanism)

| Label | Meaning |
|-------|---------|
| `sync-upstream:disabled` | Exclude this rig from sync cycles |
| `rig:parked` | Rig is parked (general exclusion) |
| `rig:docked` | Rig is docked (general exclusion) |

**Potential addition for circuit breaker:**

| Label | Meaning |
|-------|---------|
| `sync-upstream:paused-until:<ISO>` | Temporarily paused after repeated conflicts |

### Git state (the real "database")

The merge commit graph IS the sync history:

```
git log --merges --format="%H %ai %s" origin/gagecane/gt
```

Each sync creates exactly one of:
- A merge commit (`Merge remote-tracking branch 'origin/main' into gagecane/gt`)
- A fast-forward (no commit — branch pointer moves)
- Nothing (already synced, or guards prevented run)

### Branch naming convention

| Branch | Purpose | Owner |
|--------|---------|-------|
| `gagecane/gt` | Integration branch on origin | Sync plugin writes |
| `main` | Upstream's main branch | Read-only from upstream |
| `polecat/<name>` | Polecat feature branches | Polecats |

## Data Lifecycle

| Data | Creation | Update | Deletion | Retention |
|------|----------|--------|----------|-----------|
| Upstream URL in rig config | `gt rig add --upstream=<url>` | Manual edit | Rig removal | Permanent |
| Integration branch | First sync or manual setup | Every successful sync (moves forward) | Never (permanent branch) | Permanent |
| Merge commits | Each non-ff sync | Never (immutable) | Never | Permanent (git history) |
| Ephemeral receipt beads | Each plugin run | Never | `PurgeClosedEphemerals` | Days to weeks |
| Escalation beads | On conflict | Mayor triages | Closed when resolved | Standard bead lifecycle |
| Rig identity labels | Rig creation / admin action | Admin toggle | Admin removal | Until removed |

## Schema Evolution

**There is no schema to evolve.** The feature uses:
- Git (immutable DAG, no schema)
- Existing `UpstreamURL` field (already in `RigConfig`)
- Existing bead labels (string-based, no schema)
- Ephemeral beads (structured only by convention, not typed)

If v2 adds structured sync-state tracking (pinned bead or table), the
migration path is:
1. Add a `schema_version` field to the pinned bead metadata
2. First write creates the bead with v1 schema
3. Future readers tolerate unknown fields (round-trip opaque JSON)

But this is speculative — the recommendation is to NOT add structured state
in v1.

## Access Patterns

| Query | Method | Cost |
|-------|--------|------|
| "Is rig synced with upstream?" | `git merge-base --is-ancestor` | O(git-walk), <100ms |
| "When was last sync?" | `git log --merges -1 origin/<branch>` | O(1 log entry), <50ms |
| "What syncs happened today?" | `bd list --label=plugin:sync-upstream` | Dolt query, <200ms |
| "Which rigs are opted in?" | `rigs.json` + label check | File read + N queries |
| "Is sync paused for this rig?" | `bd show <rig-bead>` → check labels | Single Dolt query |
| "What files conflicted?" | Escalation bead description | `bd show <escalation-id>` |

## Constraints Identified

- **No new database or table.** Feature uses git + existing beads substrates.
- **Sync plugin is the execution context.** It runs at most every 6h (cooldown
  gate in `plugin.md`). No concurrent execution risk.
- **Crew worktree must exist.** `<rig>/crew/gagecane/` must be provisioned
  before the plugin can run. This is a setup-time requirement, not a runtime
  one.
- **Rig config owns upstream URL.** The `upstream_url` field in `RigConfig`
  and `RigEntry` is the canonical source. Shell scripts derive it from there
  (or from `git remote -v` in the crew worktree).
- **Merge topology must be preserved.** `internal/refinery/fork_sync.go`
  ensures polecat branches that integrated upstream get no-ff merged (not
  squashed). This is critical: squashing destroys the ancestry chain that
  `check-upstream-rebased.sh` relies on.
- **No unbounded growth.** Ephemeral receipts are purged. Escalation beads
  follow standard lifecycle. Merge commits grow with upstream history (O(1)
  per sync), which is bounded by upstream's commit rate.

## Open Questions

1. **Should the integration branch always be `gagecane/gt`, or should it be
   configurable per rig?** Currently the plugin hardcodes
   `INTEGRATION_BRANCH="gagecane/gt"`. For the fork workflow with upstream
   `gastownhall/gastown`, the branch that receives upstream merges might
   differ (e.g., `main` directly, or a release branch). Recommend: keep
   `gagecane/gt` as default, add an optional `integration_branch` field to
   rig config if needed.

2. **How should the circuit breaker threshold be configured?** If we add
   `sync-upstream:paused-until:<ISO>` labels after N conflicts, what is N?
   Recommend: N=3 consecutive conflicts on the same rig within 7 days. This
   avoids spamming the mayor with escalations for chronically diverged rigs.
   The label can be cleared manually or by a patrol after the window expires.

3. **Multi-upstream support?** Some rigs might track multiple upstream repos
   (e.g., gastown tracks `gastownhall/gastown` but also needs selective
   cherry-picks from another repo). The current model is single-upstream per
   rig. Recommend: v1 stays single-upstream; multi-upstream is a v2 concern
   that would require a list of upstream configs rather than a single URL.

4. **Conflict resolution polecat dispatch.** When the plugin escalates a
   conflict, should it also file a dispatchable bead for a polecat to resolve,
   or rely on the mayor to triage manually? The current plugin only escalates.
   This is a workflow question more than a data-model question — the bead
   schema supports either path.

## Integration Points

- **Refinery (fork_sync.go).** The refinery's merge-topology preservation
  logic (`preserveForkSyncTopology`) is downstream of this feature. When
  the sync plugin merges upstream into `gagecane/gt`, polecat branches based
  on the old `gagecane/gt` tip remain valid (merge commit is a new ancestor).
  When those polecat branches are later submitted to the merge queue, the
  refinery detects the upstream ancestry and uses no-ff merge. **No changes
  needed** — the existing detection is sufficient.

- **Rig config (internal/config/types.go).** The `UpstreamURL` field is
  already present. Consumers: `gt rig add`, `gt rig show`, the sync plugin.
  If we add `integration_branch`, it goes here.

- **Plugin system (plugins/sync-upstream/).** The shell script is the
  execution engine. Its data needs are: `rigs.json` (rig discovery), git
  remotes (upstream/origin refs), beads (labels, queue state, receipts).

- **Beads labels.** The rig identity bead's labels control opt-out
  (`sync-upstream:disabled`, `rig:parked`, `rig:docked`). Adding circuit
  breaker state here keeps it inspectable via standard `bd show`.

- **Dolt sync (doltserver/sync.go).** `PurgeClosedEphemerals` handles
  cleanup of ephemeral receipt beads. No changes needed — sync receipts
  use the `--ephemeral` flag and are purged in the normal lifecycle.

- **`scripts/check-upstream-rebased.sh`.** This gate script verifies that
  `upstream/main` is an ancestor of the target branch before merge. It is
  the *consumer* of the sync plugin's work — if sync keeps the integration
  branch current, this gate passes for polecat branches based on it. The
  gate reads `UPSTREAM_REMOTE` env var (default: "upstream").

- **Scale considerations.** The feature scales linearly with rig count.
  Each rig adds one git fetch + one merge-base check + one optional merge
  per cycle. At 10 rigs with a 6h cooldown, that's ~40 git operations/day
  — trivial. The only scaling concern is if upstream repos are very large
  (slow fetches), which is bounded by network, not data model.

# Integration Analysis

## Summary

The upstream-sync replacement touches six existing subsystems: the plugin
system (where the old implementation lives), the refinery (which must
recognize and preserve fork-sync merge topology), the rig config layer
(where the feature is enabled/configured), the polecat dispatch system
(for conflict resolution work), the witness pre-merge gate
(`check-upstream-rebased.sh`), and the deacon/daemon patrol loop (which
triggers periodic checks). The good news: none of these require breaking
changes. The new system slots into existing patterns — rig config for
enablement, beads for state, MQ for landing, and standard polecat
dispatch for agent work.

The migration path is clean: the old shell-script plugin
(`plugins/sync-upstream/`) is disabled via its `.disabled` sentinel,
the new Go implementation runs alongside it during validation, and once
proven, the plugin directory is deleted entirely. Feature-flagging is
natural: the `UpstreamSync.Enabled` field in rig settings means the
feature is off by default and activated per-rig by an operator.

## Analysis

### Key Considerations

- **The old plugin is a shell script with no Go code.** There is nothing
  to refactor — the replacement is a clean new implementation in
  `internal/upstreamsync/` that uses existing infrastructure.
- **Refinery already handles fork-sync topology preservation.**
  `internal/refinery/fork_sync.go` detects branches that have integrated
  upstream and uses no-ff merge instead of squash. The new system's sync
  MRs will trigger this existing detection path.
- **`check-upstream-rebased.sh` is both the problem detector and the
  success metric.** The new system's job is to keep this gate green. It
  runs as a pre-merge gate (`scripts/check-upstream-rebased.sh`).
- **MergeQueueConfig already has gate commands.** The existing
  `BuildCommand`, `TestCommand`, `LintCommand` fields provide the CI
  gate infrastructure the new system needs.
- **No new bead types needed.** Standard pinned beads (for state) and
  standard work beads (for dispatch) use existing infrastructure.
- **Plugin system supports both agent-interpreted and script-executed
  plugins.** The old sync-upstream used `run.sh` (script execution).
  The new system doesn't use the plugin system at all — it runs as a
  first-class deacon/daemon patrol.

### Existing Components Touched

| Component | Location | How Affected |
|-----------|----------|--------------|
| Plugin: sync-upstream | `plugins/sync-upstream/` | Disabled then deleted |
| Rig config | `internal/config/types.go` | Add `UpstreamSync` field to `RigSettings` |
| Refinery engineer | `internal/refinery/engineer.go` | No change — fork_sync.go detection works as-is |
| Fork sync detection | `internal/refinery/fork_sync.go` | No change needed (branch naming triggers it) |
| Pre-merge gate script | `scripts/check-upstream-rebased.sh` | No change — this is what we keep green |
| Polecat dispatch | `internal/cmd/sling_*.go` | No change — standard dispatch via beads |
| Deacon patrol | `internal/deacon/` | Add upstream-sync check to patrol cycle |
| Beads state | `internal/beads/` | No code change — use existing pinned bead API |
| Git operations | `internal/git/git.go` | May need new helper methods for merge conflict detection |

### Options Explored

#### Option 1: First-class Go package with deacon patrol trigger (RECOMMENDED)

- **Description**: New package `internal/upstreamsync/` containing the
  state machine, sync logic, and conflict resolution dispatch. Triggered
  by the deacon's patrol cycle (periodic check). Submits sync work to the
  merge queue as standard MRs. Uses existing polecat dispatch for conflict
  resolution.
- **Pros**:
  - Full Go type safety and testability.
  - Integrates with existing patrol infrastructure.
  - Uses proven patterns (state bead, MQ submission, polecat dispatch).
  - Feature-flagged via rig config — zero risk to non-opted-in rigs.
  - Can be unit-tested without git operations (interface-based git ops).
- **Cons**:
  - More code than a script replacement.
  - Need to wire into deacon patrol loop.
- **Effort**: Medium. ~500-800 LOC for core logic, plus tests.

#### Option 2: Replace shell script with Go script-style plugin

- **Description**: Keep the plugin system but rewrite `run.sh` as a Go
  binary in `plugins/sync-upstream/main.go` that the deacon executes.
- **Pros**:
  - Keeps existing dispatch path (plugin system).
  - Minimal integration work — just a better script.
- **Cons**:
  - Plugin system not designed for stateful operations.
  - No access to typed config, beads API, or refinery integration.
  - Can't CAS-protect state transitions (no Dolt access from plugin).
  - Agent conflict resolution needs polecat dispatch which plugins
    don't naturally do.
  - Plugin execution model (dog worker) is too heavyweight for a
    simple check-and-dispatch.
- **Effort**: Medium, but architecturally constrained.

#### Option 3: Daemon background goroutine (always-on)

- **Description**: Add upstream-sync as a background goroutine in the
  gastown daemon, polling on a timer.
- **Pros**:
  - Fastest response to upstream changes (continuous polling).
  - No dispatch overhead.
- **Cons**:
  - Daemon is already complex; adding stateful logic increases risk.
  - Daemon runs in mayor context — not in rig worktrees where git
    operations happen.
  - Harder to test (requires daemon lifecycle).
  - Breaking daemon stability for a non-critical feature is bad.
- **Effort**: Medium-High. Integration risk is the concern, not LOC.

#### Option 4: GitHub webhook-triggered (event-driven)

- **Description**: Listen for push events on upstream via GitHub webhook
  or polling the GitHub API.
- **Pros**:
  - Reacts immediately to upstream changes.
  - No wasted polling cycles.
- **Cons**:
  - Requires external network access (not always available in
    corporate/air-gapped environments).
  - Requires GitHub PAT or App credentials.
  - Adds external dependency (GitHub API availability).
  - PRD explicitly states "runs via local rig infrastructure."
- **Effort**: High. External dependency makes this inappropriate for v1.

### Recommendation

**Option 1: First-class Go package with deacon patrol trigger.** This
gives full access to typed infrastructure, enables proper testing, and
integrates cleanly with existing patterns without modifying any existing
subsystem's core logic.

## Integration Plan

### Phase 1: Foundation (no behavior change)

1. **Add `UpstreamSync *UpstreamSyncConfig` to `RigSettings`**
   - File: `internal/config/types.go`
   - Add config struct with `Enabled`, `UpstreamRemote`, `CadenceMinutes`, etc.
   - Default-absent (nil) = disabled. Zero risk.

2. **Create `internal/upstreamsync/` package**
   - `types.go` — State machine types, metadata schema
   - `state.go` — State bead read/write (uses `internal/beads` API)
   - `checker.go` — Git ancestry check (is sync needed?)
   - `syncer.go` — Merge/rebase execution
   - `dispatcher.go` — Polecat dispatch for conflict resolution

3. **Add `gt upstream-sync status` CLI verb**
   - File: `internal/cmd/upstream_sync.go`
   - Read-only status display from state bead.

### Phase 2: Core logic (feature-flagged, no auto-trigger)

4. **Implement state machine transitions**
   - `idle → checking → syncing → gating → pushing → idle`
   - CAS on state bead for concurrency protection.

5. **Implement gate runner**
   - Reuse `MergeQueueConfig.BuildCommand`, `TestCommand` etc.
   - Or use dedicated `UpstreamSyncConfig.GateCommands`.

6. **Add `gt upstream-sync run [--rig=...]` manual trigger**
   - Operator can trigger sync manually for validation.

### Phase 3: Automation (deacon integration)

7. **Wire into deacon patrol**
   - File: new `internal/deacon/upstream_sync.go`
   - On patrol tick: for each rig with `UpstreamSync.Enabled`:
     check cooldown, check state != busy, trigger if needed.

8. **Disable old plugin**
   - Create `plugins/sync-upstream/.disabled` sentinel.
   - Old plugin skips runs, new system takes over.

### Phase 4: Conflict resolution (agent dispatch)

9. **Implement conflict detection and polecat dispatch**
   - When merge conflicts: create work bead with conflict list,
     dispatch to available polecat via standard sling.
   - Polecat receives: branch name, conflict files, resolution
     instructions.

10. **Add circuit breaker**
    - 3 consecutive failures → auto-pause.
    - `gt upstream-sync unpause <rig>` to resume.

### Phase 5: Cleanup

11. **Remove old plugin directory**
    - Delete `plugins/sync-upstream/` entirely.
    - Remove any lingering receipt beads with `plugin:sync-upstream` label.

### Where Does the Code Live?

```
internal/
├── upstreamsync/           # NEW — core logic
│   ├── types.go            # State machine types
│   ├── state.go            # Bead state read/write
│   ├── checker.go          # Git ancestry checking
│   ├── syncer.go           # Merge/rebase execution
│   ├── dispatcher.go       # Polecat dispatch for conflicts
│   ├── gate.go             # CI gate runner
│   └── *_test.go           # Unit tests per file
├── cmd/
│   └── upstream_sync.go    # CLI verbs (status, run, pause, unpause)
├── config/
│   └── types.go            # Add UpstreamSyncConfig (line ~758)
├── deacon/
│   └── upstream_sync.go    # NEW — patrol integration
└── refinery/
    └── fork_sync.go        # No changes needed
```

### What Needs to Change in Dependent Code?

| Dependent | Change Required | Risk |
|-----------|-----------------|------|
| `internal/config/types.go` | Add `UpstreamSync` field to `RigSettings` | None — additive, nil-safe |
| `internal/deacon/manager.go` | Register upstream-sync check in patrol loop | Low — one new handler |
| `internal/cmd/root.go` | Register `upstream-sync` subcommand | None — additive |
| `internal/git/git.go` | Possibly add `MergeWithResult()` helper | Low — additive method |
| `scripts/check-upstream-rebased.sh` | No change | None |
| `internal/refinery/fork_sync.go` | No change | None |
| `internal/refinery/engineer.go` | No change | None |

### Feature Flag / Gradual Rollout

The feature is **off by default** at every level:

1. **Rig config**: `UpstreamSync` field absent → disabled.
2. **Manual trigger first**: Phase 2 adds `gt upstream-sync run` before
   any automation. Operator validates manually.
3. **Enable automation per-rig**: Phase 3 deacon integration only runs
   for rigs where `UpstreamSync.Enabled = true`.
4. **Circuit breaker**: Auto-pauses on repeated failures.
5. **Old plugin coexists**: Sentinel file disables old plugin while new
   system proves itself. Can re-enable old plugin instantly if new
   system has issues.

### Testing Strategy

| Layer | Testing Approach |
|-------|-----------------|
| State machine transitions | Unit tests with mock beads API |
| Git operations (merge, rebase) | Integration tests with temp git repos |
| Deacon patrol integration | Unit tests with mock upstreamsync checker |
| CLI verbs | Integration tests (existing cmd test pattern) |
| End-to-end | Manual validation on `gastown_upstream` rig |
| Fork-sync topology preservation | Existing `fork_sync_test.go` covers this |
| Conflict resolution dispatch | Unit test: verify work bead creation |
| Gate runner | Unit test with mock command execution |

### Backwards Compatibility

- **No breaking changes.** All additions are additive (new config field,
  new package, new CLI verb, new deacon handler).
- **Old plugin remains functional** until explicitly disabled via sentinel.
- **Rig config without `upstream_sync` field**: treated as disabled (Go
  nil-pointer semantics on `*UpstreamSyncConfig`).
- **Beads schema**: New pinned bead IDs (`<rig>-upstream-sync-state`) don't
  collide with existing conventions.
- **Branch naming**: `upstream-sync/<rig>/<id>` doesn't collide with
  `polecat/`, `auto-test/`, `integration/`, or other conventions.

## Constraints Identified

- **Deacon patrol has a fixed tick interval.** Upstream sync cadence
  must be a multiple of the patrol tick, or use its own cooldown tracking
  (beads label on state bead).
- **Polecat dispatch requires an available slot.** If all polecats are
  busy, conflict resolution waits. The circuit breaker should account
  for "stuck in resolving" timeout.
- **Git fetch requires network access.** If `upstream` remote is
  unreachable, the check fails gracefully (state stays `idle`).
- **Refinery fork_sync detection relies on `upstream/main` ref existing
  locally.** The syncing polecat must `git fetch upstream` before
  creating its merge commit, so the refinery's worktree also has the ref.
- **One sync attempt per rig at a time** (state machine is serial).
  Concurrent dispatch must be prevented via CAS on state bead.

## Open Questions

1. **Should sync submit to the Refinery MQ or push directly?**
   - MQ: follows normal flow, gets reviewed by refinery, fork_sync
     detection works automatically. But adds latency (MQ batching).
   - Direct push: faster, but bypasses refinery's merge topology
     preservation and gates. Unsafe.
   - **Recommendation**: MQ. The refinery already handles fork-sync
     topology — submitting through MQ gets that for free. Flag for
     UX/API dimension confirmation.

2. **What git remote does the polecat use for upstream?**
   - Polecat worktrees have `origin` = `gagecane/gastown` (fork).
   - They may or may not have `upstream` = `gastownhall/gastown`.
   - The `check-upstream-rebased.sh` script auto-adds the remote if
     missing. Should the sync system do the same?
   - **Recommendation**: Yes — auto-add `upstream` remote in the sync
     polecat's worktree (same pattern as the gate script). Config
     stores the URL.

3. **How does conflict resolution interact with in-flight polecats?**
   - Old plugin skipped sync entirely if any polecat had hooked work.
   - New system submits through MQ, so it's just another MR in the
     queue. Refinery handles ordering.
   - But: the conflict-resolving polecat IS an in-flight polecat.
     Should other sync triggers be blocked while one is active?
   - **Recommendation**: Yes — state machine `resolving` state blocks
     new checks for this rig. Other rigs are independent.

4. **Deacon vs. dedicated patrol for triggering?**
   - Deacon already runs periodic patrols with various checks.
   - But deacon is rig-level; upstream sync may need to coordinate
     across rigs (e.g., "don't sync two rigs simultaneously" to avoid
     saturating network).
   - **Recommendation**: Deacon per-rig with CAS protection. Parallel
     sync of different rigs is fine — they're independent git repos.

5. **What happens when upstream force-pushes?**
   - `git merge-base --is-ancestor upstream/main HEAD` returns false.
   - `git merge upstream/main` may succeed or fail depending on content.
   - But: a force-push means upstream history was rewritten. Merging
     may introduce duplicate commits or confusing history.
   - **Recommendation**: Detect force-push (upstream SHA from last sync
     is not in current upstream history). If detected, escalate instead
     of auto-resolving. Flag for security dimension.

## Integration Points

- **Data Model (data leg)**: The state bead schema
  (`<rig>-upstream-sync-state`) and `UpstreamSyncConfig` struct defined
  in the data model leg are consumed here. Config placement in
  `RigSettings` is confirmed as the right integration point.

- **API & Interface (api/ux leg)**: CLI verbs (`gt upstream-sync
  status/run/pause/unpause/enable/disable`) surface the state bead data
  and write rig config. Keep verbs thin — logic lives in the
  `internal/upstreamsync/` package.

- **Security (security leg)**: Force-push detection, gate enforcement
  (no push without green build+test), and the fact that sync MRs go
  through the standard refinery review path (if `require_review` is
  enabled) provide defense-in-depth.

- **Scale (scale leg)**: At <10 rigs, per-rig polling is fine. Each
  check is one `git fetch` + one `git merge-base`. The expensive
  operation is `go test ./...` after merge — but that only runs when
  sync is actually needed (not on every patrol tick).

- **Refinery**: The new system's MRs flow through the refinery's
  standard path. Fork-sync topology preservation activates
  automatically when `upstream/main` is in the MR branch's history
  but not in `origin/main`'s history. No refinery code changes needed.

- **Witness**: The `check-upstream-rebased.sh` pre-merge gate
  validates the result. If the new system is working correctly, this
  gate never fails. If it does fail, it means the system has a bug
  or upstream moved between sync and merge (race window).

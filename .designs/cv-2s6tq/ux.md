# User Experience Analysis

## Summary

The upstream-sync replacement moves from a plugin-based shell script (`plugins/sync-upstream/run.sh`) operating on `gagecane/gt` integration branches to a first-class system where polecats autonomously merge `upstream/main` into `origin/main` with full CI gates. The UX shift is significant: the current plugin is a maintenance cron that escalates conflicts to humans; the replacement makes conflict resolution autonomous and invisible. The primary user experience question is whether this should remain a background system (zero daily interaction) or gain explicit CLI surface for observability and control.

The ideal UX is "zero-interaction steady state with full observability on demand." Users should never think about upstream sync unless something unusual happens — then the system should make the situation immediately legible.

## Analysis

### Key Considerations

- **Mental model**: Users think of their fork as "main + our stuff." Upstream sync keeps this mental model valid by ensuring "main" is always current with upstream. Any UX that requires users to think about sync mechanics violates this mental model.
- **Existing patterns**: The codebase already has `gt auto-test-pr` (autonomous feature with status/pause/resume/audit subcommands), `gt convoy` (batch tracking), and plugins (cooldown-gated background tasks). The replacement should follow one of these established patterns.
- **Transparency vs. invisibility**: The current plugin is *too* invisible — when it fails (conflicts), it escalates but gives no way to inspect history or understand cadence. The replacement needs an observability layer without requiring daily interaction.
- **Conflict resolution is the hard UX problem**: Making agents resolve conflicts autonomously means the user loses the ability to review conflict choices before they land. This requires trust mechanisms (audit trail, easy revert, notification on non-trivial resolutions).

### Options Explored

#### Option A: Pure background system (no CLI surface)

- **Description**: Runs as a daemon/timer task. No `gt` subcommand. Observability via `bd list --label plugin:sync-upstream` and `gt refinery queue`. Conflicts resolved by polecats dispatched automatically.
- **Pros**: Zero learning curve. Matches the "just works" aspiration. No new commands to remember.
- **Cons**: Hard to diagnose when something goes wrong. No way to pause/resume. No way to force an immediate sync. History is scattered across beads with no unified view. Violates the pattern set by `gt auto-test-pr` (which has rich CLI surface for the same class of autonomous feature).
- **Effort**: Low (implementation-wise), but high support burden due to poor observability.

#### Option B: `gt sync` subcommand tree (like auto-test-pr)

- **Description**: New `gt sync` command with subcommands: `gt sync status`, `gt sync pause`, `gt sync resume`, `gt sync now`, `gt sync log`. Follows the `gt auto-test-pr` pattern exactly.
- **Pros**: Consistent with existing UX patterns. Full observability. Pause/resume for maintenance windows. `gt sync now` for impatient users. `gt sync log` for audit trail.
- **Cons**: New command tree to learn. May over-engineer for something that should be invisible.
- **Effort**: Medium

#### Option C: Hybrid — background + minimal `gt sync` surface

- **Description**: Background operation with minimal CLI: `gt sync status` (health check), `gt sync now` (force immediate), `gt sync log` (recent history). No pause/resume (use `bd` labels like the current plugin's `sync-upstream:disabled`). Autonomous conflict resolution by polecats.
- **Pros**: Minimal learning curve for daily use. Adequate observability. Forces sync on demand when needed. Doesn't over-engineer pause/resume (rare operation — label-based is fine).
- **Cons**: No explicit pause command (users must know about the label). Less discoverable than Option B for pause/resume.
- **Effort**: Low-Medium

#### Option D: Refinery-integrated (no separate command)

- **Description**: Upstream sync becomes a refinery capability. The refinery checks fork freshness before merging any MR and auto-syncs as a pre-merge step. Visibility via `gt refinery status`.
- **Pros**: Architecturally elegant — sync is a natural refinery concern. No new concepts. Users see sync as part of the merge pipeline they already understand.
- **Cons**: Tight coupling — refinery failures now affect sync. More complex refinery logic. Doesn't handle the "no polecat work, but upstream moved" case (sync only happens when MRs land). Breaks the existing `fork_sync.go` topology-preservation model which already handles the merge-commit strategy.
- **Effort**: High (refinery is already complex)

### Recommendation

**Option C: Hybrid background + minimal CLI surface.** Specifically:

1. **Trigger**: Daemon-managed timer (like existing plugin cooldown, but in Go). Checks every 30 minutes. Also triggered automatically when refinery finds fork behind upstream (integrates with existing `check-upstream-rebased.sh` gate).

2. **CLI surface** (3 commands, progressive disclosure):
   ```
   gt sync status   — Am I up to date? When was last sync? Any pending conflicts?
   gt sync now      — Force immediate sync attempt (useful during development)
   gt sync log      — Show last N sync events (success/conflict/skip)
   ```

3. **Conflict resolution UX**:
   - Agent resolves autonomously (per requirement: "Agents resolve conflicts autonomously")
   - `gt sync status` shows "conflict resolution in progress" when a polecat is working on it
   - After resolution lands, `gt sync log` shows what was resolved and the merge commit SHA
   - If resolution fails (agent can't resolve after N attempts), escalate to human with `gt sync status` showing "NEEDS ATTENTION: <file list>"

4. **Discoverability**:
   - `gt sync --help` explains the mental model in 3 sentences
   - `gt doctor` includes a sync-health check (already has `hooks_sync_check.go` pattern)
   - First-time setup auto-detected from `git remote -v` showing an `upstream` remote

5. **Error experience**:
   - `gt sync status` when healthy: `✓ Fork is 0 commits behind upstream (last sync: 2h ago)`
   - `gt sync status` when behind: `⚠ Fork is 3 commits behind upstream (sync in progress...)`
   - `gt sync status` when stuck: `✗ Sync failed: merge conflict in 2 files (polecat resolving since 5m ago)`
   - `gt sync status` when needs human: `✗ Sync blocked: agent could not resolve conflict in internal/cmd/foo.go. Run gt sync log for details.`

6. **Power users vs. beginners**:
   - Beginners: never interact with sync. It just works. They notice via `gt sync status` in `gt doctor` output.
   - Power users: `gt sync now` to force, `gt sync log --verbose` for full event stream, `bd list --label plugin:sync-upstream` for bead-level tracking.
   - Admin: `sync-upstream:disabled` label on rig identity bead to pause (same pattern as current plugin).

## Constraints Identified

1. **Agent conflict resolution must have CI gates** — users must trust that autonomously-resolved conflicts don't break things. The full `go build && go test` gate is non-negotiable for this trust.
2. **No force-push to fork main** — the merge topology preservation in `fork_sync.go` already handles this correctly. The new system must maintain this invariant (merge commits, not rebases).
3. **Must not interfere with in-flight polecat work** — the existing plugin's guard (check for active hooks/MRs) is correct and must be preserved. A sync that conflicts with a polecat's base branch would cause chaos.
4. **`upstream` remote must exist** — the system should gracefully degrade (no-op with informational message) when there's no upstream remote configured, rather than erroring.
5. **Existing `scripts/check-upstream-rebased.sh` gate remains** — this is the enforcement mechanism that catches drift. The sync system prevents drift; the gate catches it if sync fails.

## Open Questions

1. **Notification on non-trivial conflict resolution**: When an agent resolves a merge conflict autonomously, should the user be notified? Options: (a) always notify, (b) only notify if resolution touches >N files or specific paths, (c) never notify (trust the CI gate). Recommendation: option (b) with N=3 — small conflict resolutions are routine, large ones deserve attention.

2. **Sync frequency**: The current plugin uses a 6h cooldown. For the replacement targeting this fork specifically (not multi-rig), should it be more aggressive (30m check, immediate on upstream push detection) or keep the conservative 6h? The `check-upstream-rebased.sh` gate already catches drift at merge time, so aggressive sync is less critical — but it reduces conflict size.

3. **What happens to the existing `gagecane/gt` integration branch?** The current plugin syncs `origin/main` into `gagecane/gt`. The new system syncs `upstream/main` into `origin/main` directly. Is `gagecane/gt` still needed? If not, the migration path needs to handle the branch's retirement gracefully.

4. **Observability for the Witness**: The Witness monitors polecat health. When a sync-polecat is dispatched to resolve conflicts, how does the Witness distinguish it from a normal work polecat? Should sync tasks have a special label or formula?

## Integration Points

- **Refinery (`fork_sync.go`)**: Already handles merge topology preservation. The new sync system's merge commits must be compatible with refinery's `preserveForkSyncTopology` decision logic. Specifically, after sync lands a merge commit, `upstream/main` must be an ancestor of `origin/main` so the gate passes.
- **Daemon/feeder**: If sync is daemon-managed (timer-based), it integrates with the existing daemon loop. Must not starve normal polecat feeding.
- **`gt doctor`**: Add a sync-health check (pattern: `doctor/hooks_sync_check.go`).
- **`check-upstream-rebased.sh`**: This gate validates the invariant that sync maintains. The two work together: sync prevents drift, gate catches missed syncs.
- **Beads/receipts**: Sync events (success, skip, conflict, resolution) should be recorded as ephemeral beads (same pattern as current plugin's `record_receipt`). This feeds `gt sync log`.
- **Convoy system**: If a sync dispatches a conflict-resolution polecat, it may optionally use a convoy for tracking. But single-task sync is simpler — no convoy needed unless multi-file conflicts spawn parallel resolution polecats.

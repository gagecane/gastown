# Integration Analysis

## Summary

The upstream sync feature—automatically keeping `gagecane/gastown` (fork/origin) up to date with `gastownhall/gastown` (upstream)—touches nearly every layer of the Gas Town stack: the plugin system (scheduling & gating), the refinery (merge topology preservation), the rig's git remotes, the polecat workflow (branch bases), and the gate scripts (ancestry checks). Critically, most of the foundational infrastructure **already exists**: the `sync-upstream` plugin defines the scheduling and safety rails, `internal/refinery/fork_sync.go` handles merge topology preservation during squash-merges, and `scripts/check-upstream-rebased.sh` enforces the fork-stays-current invariant as a pre-merge gate. The primary integration challenge is wiring these components into a reliable automated loop that handles conflicts, doesn't disrupt in-flight polecat work, and preserves the refinery's squash-merge-by-default strategy for normal MRs while using no-ff merges for sync commits.

The feature integrates with the existing system primarily through three touch points: (1) the plugin/daemon scheduling layer which triggers the sync, (2) the crew/worktree layer which performs the actual `git merge upstream/main`, and (3) the refinery layer which preserves the resulting merge topology when processing downstream MRs.

## Analysis

### Key Considerations

- **The `sync-upstream` plugin already defines the complete operational spec** — scheduling (6h cooldown gate), safety rails (7 pre-conditions), conflict handling (abort + escalate), and configuration discovery (`mayor/rigs.json`). Implementation means making this plugin executable, not designing the behavior from scratch.

- **The `fork_sync.go` refinery module is already deployed and working** — it detects when a polecat branch has integrated upstream and switches from squash-merge to no-ff merge, preserving the second-parent edge. This was built for gu-9yi3 and is production-tested.

- **The `check-upstream-rebased.sh` gate enforces upstream currency** — it's already wired as a pre-merge gate (`gates_commands` in this bead shows `scripts/check-upstream-rebased.sh`). Any polecat branch that doesn't contain upstream/main will fail this gate. This creates the forcing function: if upstream advances and no sync happens, all polecat MRs start failing.

- **The crew system (`internal/crew/`) provides persistent worktrees** — the plugin spec says syncs happen on `crew/gagecane/<rig>` worktrees, not polecat or refinery clones. This is the correct choice because crew worktrees are long-lived, user-managed, and don't interfere with transient polecat branches.

- **The daemon's plugin dispatch system is the scheduler** — plugins with `cooldown` gates are dispatched by the deacon patrol cycle. The `sync-upstream` plugin's 6h cooldown means it runs at most 4 times/day, which is appropriate for upstream sync cadence.

- **Remote configuration already exists** — `git remote -v` in this rig shows both `origin` (gagecane/gastown) and `upstream` (gastownhall/gastown). The `check-upstream-rebased.sh` script auto-adds the upstream remote if missing, so polecat worktrees are self-healing on this point.

### Existing Components Touched

| Component | File/Path | Role in Sync |
|-----------|-----------|--------------|
| Plugin definition | `plugins/sync-upstream/plugin.md` | Scheduling, safety rails, instructions |
| Plugin system | `internal/plugin/` | Discovery, scanning, dispatch |
| Daemon | `internal/daemon/` | Runs patrol cycles, dispatches plugins |
| Deacon dogs | `internal/dog/` | Executes plugin instructions |
| Refinery fork_sync | `internal/refinery/fork_sync.go` | Preserves merge topology on squash |
| Refinery engineer | `internal/refinery/engineer.go:710-754` | Calls `preserveForkSyncTopology` |
| Upstream gate script | `scripts/check-upstream-rebased.sh` | Enforces fork currency pre-merge |
| Crew manager | `internal/crew/manager.go` | Manages persistent worktrees |
| Rig config | `internal/rig/config.go` | Property layer for rig state |
| Beads integration | `internal/beads/integration.go` | Integration branch detection |

### Dependencies (What This Needs From Others)

1. **Plugin dispatch infrastructure** — The daemon must successfully dispatch `sync-upstream` to a dog worker. This path already works for other plugins (e.g., `auto-dispatch`, `dolt-backup`).

2. **Crew worktree `crew/gagecane/` per rig** — The plugin operates on this specific worktree. If it doesn't exist, the sync cannot proceed. Creation/maintenance is a prerequisite but separate concern.

3. **`upstream` remote configured on relevant worktrees** — Auto-added by `check-upstream-rebased.sh` on polecat branches, but the crew worktree needs it too. The plugin should verify this on startup.

4. **Refinery MQ empty check** — Safety rail #5 requires `gt refinery queue <rig>` to show no pending MRs. This API must exist and be queryable by the dog.

5. **Polecat hook/active_mr state query** — Safety rail #6 requires checking that no polecat has an active MR on the rig. This state is available via bead queries.

6. **Mayor's `rigs.json`** — Discovery of which rigs have `gagecane/gt` integration branches. Currently read by the plugin to determine scope.

### Dependents (What Depends On This)

1. **All polecat work on `gastown_upstream`** — When upstream advances and the sync hasn't run, the `check-upstream-rebased.sh` gate blocks ALL polecat MRs. The sync is a prerequisite for continued polecat productivity.

2. **The refinery's fork_sync logic** — Downstream MRs from polecats that rebase on the synced branch will contain the upstream merge commit. The refinery's `preserveForkSyncTopology()` detects this and uses no-ff merge instead of squash, preserving the ancestry chain.

3. **Future: auto-test-pr** — The PRD review in `.prd-reviews/rqoca/` describes an auto-test-PR system. That system would generate tests based on code from upstream, making sync freshness important.

4. **Integration branch system** — `internal/beads/integration.go` detects integration branches for epics. The main branch (which receives the synced upstream commits) is the ultimate landing target.

### Options Explored

#### Option 1: Plugin-as-agent (current design)

- **Description**: The `sync-upstream` plugin runs as a dog worker (agent) that interprets the plugin.md instructions, performs the git merge on the crew worktree, and records results. This is the existing design in the plugin definition.
- **Pros**: Uses existing plugin dispatch infrastructure; agent can handle conflict detection and escalation intelligently; instructions are human-readable and auditable.
- **Cons**: Agent execution has non-trivial cold start; agent might misinterpret instructions; 10-minute timeout might be tight for large merges + fetch.
- **Effort**: Low — plugin.md already written, infrastructure exists.

#### Option 2: Plugin-as-script (run.sh)

- **Description**: Add a `run.sh` script to `plugins/sync-upstream/` that the plugin system executes directly (the `HasRunScript` path in `plugin.go:FormatMailBody()`). The script handles the git operations deterministically.
- **Pros**: Deterministic execution, no agent interpretation variance, faster (no LLM cold start), testable in isolation.
- **Cons**: Less flexible for conflict resolution; can't escalate with contextual reasoning; requires shell error handling for all edge cases.
- **Effort**: Medium — script needs to implement all 7 safety rails + conflict handling + receipt recording.

#### Option 3: Dedicated Go command (`gt sync-upstream`)

- **Description**: Implement the sync logic as a first-class Go command within the `gt` binary, called by either the plugin system or a cron-like scheduler.
- **Pros**: Type-safe, testable in Go unit tests, can reuse existing `internal/git` package, fastest execution, no agent overhead, can share logic with `fork_sync.go`.
- **Cons**: Higher initial effort; more code to maintain; may be over-engineering for what's essentially a periodic `git fetch + git merge`.
- **Effort**: High — new command, tests, integration with plugin system.

#### Option 4: GitHub Actions on upstream (external)

- **Description**: A GitHub Action on `gastownhall/gastown` that creates a PR on `gagecane/gastown` when main advances.
- **Pros**: Runs where the code lives; no local infrastructure needed; GitHub handles merge/PR lifecycle.
- **Cons**: Requires write access from upstream to fork (unusual for OSS); introduces external dependency; doesn't integrate with Gas Town's safety rails or scheduling; can't check rig state (MQ empty, no polecats active).
- **Effort**: Medium — but architecturally misaligned with Gas Town's local-first model.

### Recommendation

**Option 2 (Plugin-as-script)** is the recommended approach for v1, with Option 3 as a follow-up if the feature proves its value.

Rationale:
- The plugin infrastructure already handles scheduling (6h cooldown gate) and dispatch.
- A `run.sh` script is deterministic and fast — no LLM interpretation variance.
- The 7 safety rails are simple shell conditionals that are easier to verify than agent behavior.
- Conflict handling (abort + escalate) maps cleanly to shell + `gt escalate`.
- The plugin system already supports `HasRunScript` — this is a well-trodden path.
- A script can be unit-tested via BATS or direct shell invocation.
- If the script path proves too rigid, upgrading to Option 3 (Go command) is a natural evolution.

## Migration Path

### Phase 1: Make the existing plugin executable
1. Create `plugins/sync-upstream/run.sh` implementing the logic described in `plugin.md`.
2. Script implements all 7 safety rails as guard conditions (exit 0 early with skip receipt).
3. Script does: `git fetch upstream`, checks preconditions, `git merge upstream/main --no-edit`, `git push origin`.
4. On conflict: `git merge --abort`, `gt escalate -s medium "Merge conflict: <files>"`, record failure receipt.
5. Verify that the daemon's deacon patrol dispatches it correctly on cooldown expiry.

### Phase 2: Verify end-to-end with fork_sync.go
1. After a successful sync, create a test polecat branch that rebases on the synced main.
2. Submit to refinery — verify `preserveForkSyncTopology()` detects the upstream ancestry and uses no-ff merge.
3. After merge, verify `check-upstream-rebased.sh` passes on the resulting HEAD.

### Phase 3: Monitoring and graduation
1. Plugin receipts track success/failure/skip history.
2. Daemon digest includes sync-upstream outcomes.
3. If 2+ consecutive failures (conflicts), escalation path ensures human intervention.

## Backwards Compatibility

**What might break:**

1. **Polecats rebased on pre-sync main** — After a sync merge commit lands on main, polecats whose branches were based on the old main tip are NOT broken. The plugin uses merge (not rebase), so old branches remain valid ancestors. This is explicitly called out in the plugin spec: "polecat branches based on the previous `gagecane/gt` tip remain valid after a sync."

2. **Refinery squash behavior** — Normal polecat MRs continue to be squash-merged. Only branches that have explicitly integrated upstream (detected by `fork_sync.go`'s 3-condition check) get no-ff treatment. This is already deployed and tested.

3. **Gate script behavior** — `check-upstream-rebased.sh` will PASS for branches that contain the latest upstream (post-sync). Pre-sync branches that haven't been rebased will start failing once upstream advances past the fork's HEAD. This is the desired behavior — it forces polecats to stay current.

4. **Plugin system** — Adding `run.sh` to an existing plugin directory is a supported upgrade path. The plugin system checks `HasRunScript` and dispatches accordingly. No schema changes needed.

**Nothing breaks** in the common case. The main risk is a failed merge (conflicts) that blocks the sync, causing `check-upstream-rebased.sh` to fail for subsequent polecat MRs. The plugin's conflict → escalate path handles this by alerting the mayor immediately.

## Testing Strategy

1. **Unit tests for `run.sh`** — Test each safety rail independently:
   - Rig is parked → skip
   - No `gagecane/gt` branch → skip  
   - Already up-to-date → skip
   - MQ not empty → skip
   - Polecat active → skip
   - Dirty worktree → skip
   - Clean merge succeeds → push + receipt
   - Conflict → abort + escalate + failure receipt

2. **Integration test for fork_sync.go** — Already exists at `internal/refinery/fork_sync_integration_test.go`. Verify it covers the post-sync-upstream scenario (branch that has merged upstream/main into itself).

3. **End-to-end test** — Create a test upstream repo, configure the sync plugin, trigger dispatch, verify:
   - Upstream commits appear on origin/main after sync
   - `check-upstream-rebased.sh` passes on new branches
   - Polecat MRs with upstream ancestry use no-ff merge

4. **Failure mode tests** — Verify each failure path:
   - Network failure during fetch → clean failure + retry on next cycle
   - Merge conflict → abort + escalate + no push
   - Push failure (remote changed) → error receipt + retry

## Feature Flagging / Gradual Rollout

- **Per-rig opt-out**: Label `sync-upstream:disabled` on rig identity bead disables sync for that rig.
- **Cooldown tuning**: The 6h cooldown in plugin frontmatter is adjustable without code changes.
- **Dry-run mode**: Add `--dry-run` flag to `run.sh` that does everything except `git push` — useful for validation.
- **Rig-by-rig rollout**: Plugin is rig-scoped when placed in `<rig>/plugins/` vs. town-level in `~/gt/plugins/`. Start rig-local, promote to town-level after confidence.

## Constraints Identified

1. **Single-threaded per rig** — Only one sync can run per rig at a time. The 6h cooldown gate + MQ-empty check + no-active-polecat check enforces this implicitly.

2. **Crew worktree must exist** — `crew/gagecane/<rig>` must be pre-created. The sync plugin cannot create it (separation of concerns).

3. **Upstream remote must be fetchable** — Network access to `github.com/gastownhall/gastown` is required during the sync window. If unfetchable, the cycle is skipped (not failed).

4. **Merge-only strategy** — The plugin uses merge (not rebase) to keep polecat branches valid. This means merge commits accumulate in the integration history. This is an explicit trade-off documented in the plugin.

5. **No force-push** — The plugin NEVER force-pushes. If the merge creates a non-fast-forward situation on origin (shouldn't happen if safety rails pass), it fails safely.

## Open Questions

1. **Who creates the crew worktree `crew/gagecane/<rig>`?** Is this a one-time setup during `gt rig add`, or does the sync plugin check and create it on first run?

2. **What's the escalation → resolution workflow?** When the plugin escalates a conflict to the mayor, what happens next? Is a polecat dispatched to resolve manually, or does a human intervene?

3. **Should the sync push to `origin/main` directly or to a branch + MR?** The plugin spec says "merge `origin/main` into `gagecane/gt`" but the rig architecture suggests all work goes through the refinery MQ. Pushing directly to main bypasses gates — is that acceptable for a pure upstream merge?

4. **How does this interact with the refinery's "fork-sync detected" path when the sync IS the MR?** The sync itself is a merge of upstream into the fork. If submitted as an MR to refinery, `preserveForkSyncTopology()` would detect it and use no-ff merge — which is correct. But if pushed directly (bypassing refinery), the topology is preserved by the direct push.

## Integration Points

| Dimension | Connection |
|-----------|------------|
| **API/Data** | Plugin receipts stored as ephemeral beads in Dolt; queryable via `bd list --label plugin:sync-upstream` |
| **Security** | Requires fetch access to upstream remote; push access to origin; no credential escalation beyond existing rig permissions |
| **Scale** | Runs at most 4x/day per rig (6h cooldown); O(1) git operations (single merge); bounded by upstream commit volume |
| **UX** | Invisible to polecats when working; visible only when upstream gate fails (forcing sync or rebase); `gt plugin history sync-upstream` shows run history |
| **Data model** | No schema changes; uses existing bead labels (`plugin:sync-upstream`, `type:plugin-run`, `result:<outcome>`) |

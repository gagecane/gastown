# Persistent Polecat Pool

**Issue:** gt-lpop
**Status:** Design
**Author:** Mayor

## Problem

Three concepts are conflated in the polecat lifecycle:

| Concept | Lifecycle | Current behavior |
|---------|-----------|-----------------|
| **Identity** | Long-lived (name, CV, ledger) | Destroyed on nuke |
| **Sandbox** | Per-assignment (worktree, branch) | Destroyed on nuke |
| **Session** | Ephemeral (Claude context window) | = polecat lifetime |

Consequences:
- Work is lost when polecats are nuked before pushing
- 219 stale remote branches from destroyed worktrees
- Slow dispatch (~5s worktree creation per assignment)
- Lost capability record (CV, completion history)
- Idle polecats were treated as waste and nuked

## Design

### Lifecycle Separation

```
IDENTITY (persistent)
  Name: "furiosa"
  Agent bead: gt-gastown-polecat-furiosa
  CV: work history, languages, completion rate
  Lifecycle: created once, never destroyed (unless explicitly retired)

SANDBOX (per-assignment, reusable)
  Worktree: polecats/furiosa/gastown/
  Branch: polecat/furiosa/<issue>@<timestamp>
  Lifecycle: synced to main between assignments, not destroyed

SESSION (ephemeral)
  Tmux: gt-gastown-furiosa
  Claude context: cycles on compaction/handoff
  Lifecycle: independent of identity and sandbox
```

### Pool States

```
         ┌──────────┐
    ┌───►│  IDLE    │◄──── sync sandbox to main
    │    └────┬─────┘      clear hook
    │         │ gt sling
    │         ▼
    │    ┌──────────┐
    │    │ WORKING  │◄──── session active, hook set
    │    └────┬─────┘
    │         │ work complete
    │         ▼
    │    ┌──────────┐
    └────┤  DONE    │──── push branch, submit MR
         └──────────┘
```

No `nuke` in the happy path. Polecats cycle: IDLE → WORKING → DONE → IDLE.

### Reconciliation: self-terminate vs. pool reuse (gs-4pg)

`operational.daemon.polecat_self_terminate` defaults to `true` (gu-ci0l): a
polecat kills its own tmux session at the end of `gt done`. This is **not** in
conflict with the persistent-pool reuse model — it is the lifecycle-separation
table above applied literally:

- **Identity** and **Sandbox** persist. The IDLE transition in `gt done`
  (`agent_state=idle`, worktree synced to main, old branch deleted) leaves a
  *warm worktree* in the pool. `FindIdlePolecats` keys only on `State==Idle`;
  session liveness is irrelevant to whether a polecat is reusable.
- **Session** is ephemeral. Even when self-terminate is off, `ReuseIdlePolecat`
  *always* kills any existing session before reuse and respawns a fresh one
  (`StartSession` + `gt prime --hook`) so the next assignment gets a clean
  context window and correct hook discovery. self-terminate merely ends that
  ephemeral session at done-time instead of at next-reuse-time.

So the reused unit is the **worktree** (skipping ~5s of worktree creation) plus
the **identity/CV** — never a live session. "done means idle" describes the
sandbox, not the session. The capacity-starvation symptom in gs-9wz was a
crash-detector misclassifying a *clean* self-terminated session as a crash
(parked `recovery_blocked`); that was fixed independently (idle-state detection
on clean `gt done` + the stalled-clean reclaim patrol, commit 18ad8b12) and is
not an argument against either model.

### Pool Management

**Pool size:** Fixed per rig. Configured in `rig.config.json`:
```json
{
  "polecat_pool_size": 4,
  "polecat_names": ["furiosa", "nux", "toast", "slit"]
}
```

**Initialization:** `gt rig add` or `gt polecat pool init <rig>` creates N polecats
with identities and worktrees. They start in IDLE state.

**Dispatch:** `gt sling <bead> <rig>` finds an IDLE polecat (already does this via
`FindIdlePolecat()`), attaches work, starts session. No worktree creation needed.

**Completion:** When a polecat finishes work:
1. Push branch to origin
2. Submit MR (if code changes)
3. Clear hook_bead
4. Sync worktree: `git checkout main && git pull`
5. Set state to IDLE
6. Session stays alive or cycles — doesn't matter, identity persists

### Sandbox Sync (DONE → IDLE transition)

When work completes and MR is merged (or no code changes):

```bash
# In the polecat's worktree
git checkout main
git pull origin main
git branch -D polecat/furiosa/<old-issue>@<timestamp>
# Worktree is now clean, on main, ready for next assignment
```

When new work is slung:
```bash
# Create fresh branch from current main
git checkout -b polecat/furiosa/<new-issue>@<timestamp>
# Start working
```

No worktree add/remove. Just branch operations on an existing worktree.

### Refinery Integration

No changes to refinery. Refinery still:
1. Sees MR from polecat branch
2. Reviews and merges to main
3. Deletes remote polecat branch (NEW: add this step)

The polecat doesn't care — it already moved to main locally during DONE → IDLE.

### Witness Integration

Witness patrol behavior (shipped):
- Sees idle polecat → healthy state, skip
- **Stuck detection:** Polecat in WORKING state for too long → escalate (don't nuke)
- **Dead session detection:** Session died but state=WORKING → restart session (not nuke polecat)

### What Nuke Becomes

`gt polecat nuke` is reserved for exceptional cases:
- Polecat worktree is irrecoverably broken
- Need to reclaim disk space
- Decommissioning a rig

It should be rare and manual, not part of normal workflow.

### Branch Pollution Solution

With persistent polecats, branches have clear owners:
- Active branches: polecat is WORKING on them
- Merged branches: refinery deletes after merge
- Abandoned branches: polecat syncs to main on DONE → IDLE, old branch deleted locally

The 219 stale branches came from nuked polecats that never cleaned up. With persistent
polecats, branch lifecycle is managed by the polecat itself.

### One-time Cleanup

For the existing 219 stale branches:
```bash
# Delete all remote polecat branches that don't belong to active polecats
git branch -r | grep 'origin/polecat/' | grep -v 'furiosa/gt-ziiu' | grep -v 'nux/gt-uj16' \
  | sed 's/origin\///' | xargs -I{} git push origin --delete {}
```

## Implementation Phases

### Phase 1: Stop the bleeding — SHIPPED
- Witness no longer nukes idle polecats
- `gt polecat done` transitions to IDLE instead of triggering nuke
- Refinery deletes remote branch after merge

### Phase 2: Pool initialization — DEFERRED
- `gt polecat pool init <rig>` creates N persistent polecats
- Pool size configured in rig.config.json
- Worktrees created once, reused across assignments

**Status:** Polecats are allocated on-demand by `gt sling` via `FindIdlePolecat()`
and `AllocateAndAdd()`. Pre-allocation is unnecessary because idle polecats are
reused automatically. Pool size enforcement is a future optimization, not a blocker.

### Phase 3: Sandbox sync — SHIPPED
- DONE → IDLE transition syncs worktree to main (`done.go`)
- IDLE → WORKING creates fresh branch (no worktree add) via `ReuseIdlePolecat()`
- `gt sling` prefers idle polecats via `FindIdlePolecat()`
- Branch-only reuse eliminates ~5s worktree creation overhead

### Phase 4: Session independence — SHIPPED
- Session cycling doesn't affect polecat state
- Dead sessions restarted by witness (restart-first policy, no auto-nuke)
- Handoff preserves polecat identity across session boundaries
- `gt handoff` works for all roles (Mayor, Crew, Witness, Refinery, Polecats)

### Phase 5: One-time cleanup — PARTIALLY SHIPPED
- Polecat branch cleanup after merge: SHIPPED (landed to main; PRs #2436/#2437 closed)
- Refinery notifies mayor after merge: not yet shipped
- Pool reconciliation (`ReconcilePool`): not yet implemented

### Implementation Status Summary

| Component | Status | Key Files |
|-----------|--------|-----------|
| `gt done` (push, MR, idle, sandbox sync) | SHIPPED | `internal/cmd/done.go` |
| `gt sling` (idle reuse, branch-only repair) | SHIPPED | `internal/cmd/sling.go`, `polecat_spawn.go` |
| `gt handoff` (session cycle, all roles) | SHIPPED | `internal/cmd/handoff.go` |
| Witness patrol (zombie, stale, orphan detection) | SHIPPED | `internal/witness/handlers.go`, `internal/polecat/manager.go` |
| Cleanup pipeline (POLECAT_DONE → MERGE_READY → MERGED) | SHIPPED | `internal/witness/handlers.go`, `internal/refinery/engineer.go` |
| Idle polecat heresy fix (skip healthy idle) | SHIPPED | `internal/witness/handlers.go` |
| Restart-first policy (no auto-nuke) | SHIPPED | `internal/polecat/manager.go` |
| Polecat branch always deleted after merge | SHIPPED | `internal/refinery/engineer.go` |
| Refinery notifies mayor after merge | NOT SHIPPED | — |
| Pool size enforcement | DEFERRED | — |
| `ReconcilePool()` | DEFERRED | — |
| `gt polecat pool init` command | DEFERRED | — |

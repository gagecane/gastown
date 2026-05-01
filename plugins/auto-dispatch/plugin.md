+++
name = "auto-dispatch"
description = "Auto-sling ready tasks to idle polecats across all rigs"

[gate]
type = "cooldown"
cooldown = "2m"
+++

## Auto-Dispatch

Dispatch ready tasks to idle polecats across all rigs. Uses `gt sling <bead> <rig>` which auto-selects an idle polecat and starts its session.

### Steps

1. Discover rigs: parse `~/gt/mayor/rigs.json` to get rig names
2. For each rig:
   a. Run `gt polecat list <rig>` — count polecats in `idle` state (skip `working`, `stalled`, `zombie`). Call this `idle_count`.
   b. If `idle_count == 0`, skip this rig
   c. Run `cd ~/gt/<rig> && bd ready` to find open unblocked tasks
   d. Filter out non-dispatchable beads and any that have unmet blockers. A bead is non-dispatchable if ANY of:
      - `issue_type` is `epic` or `convoy` (containers, not work)
      - `title` starts with `EPIC:` or `Epic:` (data-hygiene guard — these are mis-typed containers; see gu-smr1)
      - The bead is an identity/agent bead (title matches `<prefix>-<rig>-polecat-<name>`, `<prefix>-<rig>-witness`, etc.)

      Call the remaining list `ready_tasks`.
   e. If `ready_tasks` is empty, skip this rig
   f. Sort `ready_tasks` by priority (P1 > P2 > P3 > P4) — highest first
   g. Dispatch up to `min(idle_count, len(ready_tasks))` tasks by running `gt sling <task-id> <rig>` for each, iterating the sorted list from highest priority down. Each sling auto-selects a different idle polecat.

   Note: `gt sling` enforces the same filters server-side, so a mistakenly
   included epic/identity/EPIC-titled bead will be rejected with a clear error
   rather than wasting a polecat slot.
3. Report: "Dispatched N tasks across M rigs" or "No dispatchable work" if nothing matched

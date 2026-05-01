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
   d. Filter out tasks of type `epic` or `convoy` and any that have unmet blockers. Call the remaining list `ready_tasks`.
   e. If `ready_tasks` is empty, skip this rig
   f. Sort `ready_tasks` by priority (P1 > P2 > P3 > P4) — highest first
   g. Dispatch up to `min(idle_count, len(ready_tasks))` tasks by running `gt sling <task-id> <rig>` for each, iterating the sorted list from highest priority down. Each sling auto-selects a different idle polecat.
3. Report: "Dispatched N tasks across M rigs" or "No dispatchable work" if nothing matched

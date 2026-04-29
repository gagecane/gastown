+++
name = "auto-dispatch"
description = "Auto-sling ready tasks to idle polecats across all rigs"

[gate]
type = "cooldown"
cooldown = "10m"
+++

## Auto-Dispatch

Dispatch ready tasks to idle polecats across all rigs. Uses `gt sling <bead> <rig>` which auto-selects an idle polecat and starts its session.

### Steps

1. Discover rigs: parse `~/gt/mayor/rigs.json` to get rig names
2. For each rig:
   a. Run `gt polecat list <rig>` — count polecats in `idle` state (skip `working`, `stalled`, `zombie`)
   b. If zero idle polecats, skip this rig
   c. Run `cd ~/gt/<rig> && bd ready` to find open unblocked tasks
   d. If no ready tasks, skip this rig
   e. Pick the highest-priority ready task (P1 > P2 > P3). Skip tasks of type `epic` or `convoy`
   f. Run `gt sling <task-id> <rig>` — this auto-selects an idle polecat and spawns its session
   g. Dispatch at most ONE task per rig per cycle
3. Report: "Dispatched N tasks across M rigs" or "No dispatchable work" if nothing matched

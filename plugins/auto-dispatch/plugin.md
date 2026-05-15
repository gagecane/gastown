+++
name = "auto-dispatch"
description = "Auto-sling ready tasks to idle polecats across all rigs"

[gate]
type = "cooldown"
duration = "2m"
+++

## Auto-Dispatch

Sling all ready tasks across all rigs onto the scheduler queue. The scheduler
matches queued work to polecats as slots free up. **Do NOT gate on polecat
state** — `gt polecat list` reports stale display state ("working" even when
hook_bead is null), which would cause this plugin to underfill the scheduler
when polecats are actually available.

`gt sling <bead> <rig>` is idempotent for already-scheduled beads (returns
"already hooked" or reuses the existing context wisp), so calling it every
cooldown cycle for the same bead is safe.

### Steps

1. Discover rigs: parse `~/gt/mayor/rigs.json` to get rig names
2. For each rig:
   a. Run `cd ~/gt/<rig> && bd ready` to find open unblocked tasks
   b. Filter out non-dispatchable beads and any that have unmet blockers. A bead is non-dispatchable if ANY of:
      - `issue_type` is `epic` or `convoy` (containers, not work)
      - `title` starts with `EPIC:` or `Epic:` (data-hygiene guard — these are mis-typed containers; see gu-smr1)
      - labels include `phase:epic` (data-hygiene guard — phase-style epics
        that are typed as task/bug; see gu-fs88 / ta-823 recurrence)
      - labels include `mayor-only` or `no-polecat` (operator assertion that the work
        requires mayor-scope or human intervention — town root edits, origin
        config, cross-rig coordination; see gu-bk6e / gt-pb857). Polecats
        close these no-changes ("out of scope"), and without the filter the
        scheduler re-dispatches indefinitely.
      - The bead has any open (non-closed) children — it is a parent container
        whose work is tracked by its children, not itself (gu-fs88).
      - The bead is an identity/agent bead (title matches `<prefix>-<rig>-polecat-<name>`, `<prefix>-<rig>-witness`, etc.)
      - The bead is a rig identity bead (id matches `<prefix>-rig-<name>`, `issue_type` is `rig`, or labels include `gt:rig`). Title is just the rig name (e.g. "gastown") so the title regex above misses it (gs-2j6).
      - The bead is a role definition bead (`issue_type` is `role` or labels include `gt:role`).
      - labels include `type:plugin-run` (these are plugin-execution receipts, not work — slinging them creates feedback loops where the scheduler tries to dispatch a successful plugin run as if it were a task; observed today as gs-wisp-3rw stuck in convoy hq-cv-7lcc6 for 8+ hours)
      - labels include `gt:message`, `gt:agent`, `gt:rig`, `gt:role`, `gt:sling-context`, or `msg-type:notification` (system beads, not actionable work)
      - The bead `id` matches `*-wisp-*` (defense in depth: wisps are ephemeral by definition and should not be dispatched as work, regardless of their labels)

      Call the remaining list `ready_tasks`.
   c. If `ready_tasks` is empty, skip this rig
   d. Sort `ready_tasks` by priority (P1 > P2 > P3 > P4) — highest first
   e. For each task in the sorted list, run `gt sling <task-id> <rig>`. Slinging more tasks than the rig has idle polecats is intentional — the scheduler queues them and dispatches as polecats free up. Already-scheduled tasks return cleanly without creating duplicates.

   Note: `gt sling` enforces the same filters server-side, so a mistakenly
   included epic/identity/EPIC-titled bead will be rejected with a clear error
   rather than wasting a queue slot.
3. Report: "Slung N tasks across M rigs (S new, D already-scheduled)" with per-rig breakdown.

### Opting a Bead Out of Auto-Dispatch

If a bead should NOT be auto-dispatched (e.g. a human is actively working it),
set its status to `blocked`:

```bash
bd update <id> --status=blocked
```

**Why this works** (verified 2026-05-04 in gu-qbys / gt-n2f1n):

- `bd ready` hard-codes `Status: "open"` in its filter (see `cmd/bd/ready.go`),
  so the SQL `WHERE status = 'open'` clause excludes blocked beads from the
  candidate set entirely. Auto-dispatch uses `bd ready` as its source (step 2c
  above), so it inherits this exclusion.
- Closing a blocker does **not** auto-transition a `status=blocked` bead back
  to `open`. `GetNewlyUnblockedByClose` only *reports* newly-unblocked
  candidates; it does not mutate their status.
- The witness zombie patrol (`resetAbandonedBead`) and the daemon's
  dead-polecat reaper (`reapRigDeadPolecatWisps`) only reset beads whose
  status is `hooked` or `in_progress`. A bead with `status=blocked` is
  untouched by either reaper.

Once the human is done, transition the bead back to `open` and auto-dispatch
will pick it up again.

**Future improvement**: consider adding an explicit `do-not-auto-dispatch`
label and filtering on it here, so the intent is first-class rather than
relying on the `status=blocked` side effect.

---
name: crew-merge-queue
description: >
  Get a feature branch merged in Gas Town via the Refinery merge queue using
  gt mq. Use when you want to "submit to the merge queue", "land my branch",
  "get this merged", "check merge queue status", "see why my MR is stuck", or
  "retry a failed merge". Covers submitting a branch, reading queue state,
  diagnosing blocked/failed MRs, retrying, and post-merge cleanup. Gas Town
  merges flow through the Refinery, not direct GitHub PR merges.
allowed-tools: "Bash(gt *), Bash(git *)"
version: "1.0.0"
author: "Gas Town"
---

# Crew Merge Queue — Landing Work via the Refinery

In Gas Town, completed branches land through the **Refinery**, a merge-queue
processor. You submit your branch with `gt mq submit`; the Refinery rebases,
runs gates, and merges to the target (usually `main`) in priority order.

> **Do not push to `main` directly.** Submit to the queue and let the Refinery
> merge. This keeps `main` green and serializes merges safely.

## When to use this skill

- "Submit my branch to the merge queue" / "land this"
- "Check the merge queue" / "where is my MR?"
- "My MR is blocked/failed — why?" / "retry the merge"
- "Clean up after the merge"

## Prerequisites

Your branch must be committed and pushed to origin first (see the `crew-commit`
skill). Then:

```bash
git status              # clean working tree
git rev-parse --abbrev-ref HEAD   # confirm you're on the feature branch
```

## Instructions

### Step 1: Submit the current branch

```bash
gt mq submit
```

Auto-detection: branch = current git branch; target = `main` (or the parent
epic's integration branch if one exists); priority = inherited from the source
issue. Expected output names the **MR ID** (e.g. `gu-wisp-hmx`), source branch,
and target. Note the MR ID — you'll use it to track status.

Useful flags:
```bash
gt mq submit --no-cleanup        # don't auto-shutdown (use when not a polecat)
gt mq submit --priority 0        # bump priority (P0)
gt mq submit --epic gt-xyz       # target an epic's integration branch
gt mq submit --pre-verified      # attest gates already ran (re-verified locally)
```

> If your branch name doesn't encode an issue ID, you may see a "no route for
> prefix" warning and a skipped source-issue back-link. That's cosmetic — the
> MR is still created and queued.

### Step 2: Confirm it's in the queue

```bash
gt mq status <mr-id>             # detailed status for your MR
gt mq list <rig>                 # full queue with ordering
gt mq next <rig>                 # the highest-priority MR about to process
```

MR states you'll see: `ready` (queued), `in_progress` (Refinery working it),
`blocked` (waiting on another MR), `open` with an error (failed), or merged
(MR closes).

### Step 3: Confirm the Refinery is running

If nothing is processing, the queue won't drain.

```bash
gt refinery status               # State should be ● running; shows queue depth
```

### Step 4: Wait for the merge (priority order)

The Refinery processes by score (priority, then age). Re-check status
periodically:

```bash
gt mq status <mr-id>
```

When merged, the MR closes and your commit is on the target branch.

### Step 5: Post-merge cleanup (if not automatic)

```bash
gt mq post-merge <rig> <mr-id>   # close MR, delete the merged branch
```

## Examples

**Example 1 — submit and track**
User says: "Land my branch"
```bash
gt mq submit --no-cleanup
# → MR ID: gu-wisp-hmx, Target: main
gt mq status gu-wisp-hmx
```
Result: branch queued; Refinery merges it in priority order.

**Example 2 — check why an MR is stuck**
User says: "Why hasn't my MR merged?"
```bash
gt mq list gastown_upstream      # see your MR's state + what's ahead
gt refinery status               # confirm processor is running
gt mq status <mr-id>             # read blockers / error notes
```

**Example 3 — retry a failed merge**
User says: "Retry the merge"
```bash
gt mq retry <rig> <mr-id>        # reset a failed MR for reprocessing
gt mq retry <rig> <mr-id> --now  # jump the queue
```

## Troubleshooting

**MR sits in `ready` and never processes**
Cause: Refinery not running, or higher-priority MRs ahead.
Solution: `gt refinery status` (confirm ● running); `gt mq list <rig>` to see
queue position. Bump with `--priority 0` on resubmit if genuinely urgent.

**MR is `blocked` (waiting on another MR)**
Cause: a dependency MR must merge first.
Solution: that's expected — the blocker's ID is shown in `gt mq list`. Wait for
it, or address the dependency.

**MR failed (open with an error)**
Cause: rebase conflict or failing gates during merge.
Solution: read `gt mq status <mr-id>` notes. Fix on your branch, push, then
`gt mq retry <rig> <mr-id>`.

**"main is frozen" / merges halted**
Cause: the ci-watcher froze the queue because post-merge CI broke `main`.
Solution: do not force merges. Wait for the freeze to clear, or escalate
(`gt escalate -s HIGH "merge queue frozen: <detail>"`).

**Need to abandon an MR**
Cause: work superseded or rejected.
Solution: `gt mq reject <rig> <mr-id> --reason "<why>"`. Note: this does NOT
close the source issue — the work is simply not landed.

+++
name = "sync-upstream"
description = "Keep each rig's gagecane/gt branch up to date with origin/main by merging during quiescent windows"
version = 2

[gate]
type = "cooldown"
duration = "6h"

[tracking]
labels = ["plugin:sync-upstream", "category:maintenance"]
digest = true

[execution]
timeout = "10m"
notify_on_failure = true
severity = "medium"
+++

# Sync Upstream

For each rig whose integration branch is `gagecane/gt`, merge `origin/main`
into it and push, so that `gagecane/gt` stays current with upstream without
needing force pushes or manual rebases.

## Why

`gagecane/gt` is the integration branch where polecat work merges before
shipping back upstream. As `origin/main` advances, `gagecane/gt` falls
behind. Without periodic syncs the two diverge and shipping cycles end up
fighting a giant manual rebase under pressure.

This plugin keeps the divergence at zero by doing the merge on a quiet
schedule. Merging (vs. rebasing) keeps polecat branches based on
`gagecane/gt` valid — no orphaning — at the cost of merge commits in the
integration history.

## Safety rails

The merge is a fast-forward-able non-destructive update, but we still want
to avoid running it while in-flight work could conflict. The plugin only
syncs a rig if ALL of the following are true:

1. The rig is not parked or docked (read identity bead).
2. The rig has a `gagecane/gt` branch on origin.
3. `origin/main` is not already an ancestor of `gagecane/gt` (nothing to
   do).
4. `gagecane/gt` is not already an ancestor of `origin/main` (clean
   fast-forward case, handled separately).
5. The rig's merge queue is empty (`gt refinery queue <rig>` shows no
   pending MRs).
6. No polecat has a `hook_bead` or `active_mr` set on the rig (no in-flight
   work that could conflict mid-merge).
7. The local crew/gagecane checkout (used to perform the merge) has a
   clean working tree and is currently on `gagecane/gt`.

Any "no" → skip this rig, record a "skipped: <reason>" receipt, do not
escalate.

## Conflict handling

If `git merge` reports conflicts, the plugin:

1. Aborts the merge (`git merge --abort`).
2. Files a `bd escalate` to the mayor with severity=medium, listing the
   conflicting files.
3. Records a "failure: merge conflicted" receipt.
4. Does NOT push.

The mayor can then dispatch a polecat to resolve manually.

## Configuration

This plugin discovers rigs from `~/gt/mayor/rigs.json` and only acts on
rigs whose integration branch is `gagecane/gt`. To exclude a rig, set
label `sync-upstream:disabled` on its rig identity bead.

## Notes

- The plugin operates on `crew/gagecane/<rig>` worktrees, NOT on
  refinery/mayor clones — those are managed by other components and may
  have stale tracking refs.
- Merge commits are created with `--no-edit` so the receipt body stays
  predictable.
- Unlike the v1 rebase strategy, polecat branches based on the previous
  `gagecane/gt` tip remain valid after a sync — the merge commit just
  becomes a new ancestor.

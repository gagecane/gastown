+++
name = "github-sheriff"
description = "Monitor GitHub CI checks on open PRs across all rigs and file ci-failure beads"
version = 2

[gate]
type = "cooldown"
duration = "2h"

[tracking]
labels = ["plugin:github-sheriff", "category:ci-monitoring"]
digest = true

[execution]
type = "script"
timeout = "2m"
notify_on_failure = true
severity = "low"
+++

# GitHub Sheriff

Monitors GitHub CI checks on open pull requests **across every GitHub-backed
rig** and files `ci-failure` beads for failing required checks, routed to the
owning rig. Implements the PR Sheriff pattern from the
[Gas Town User Manual](https://steve-yegge.medium.com/gas-town-emergency-user-manual-cf0e4556d74b)
as a Deacon plugin.

Requires: `gh` CLI installed and authenticated (`gh auth status`), plus `jq`.

## Why per-rig (the blind spot it closes)

This plugin used to resolve a single repo from the `gastown` git remote — the
gt SOURCE repo only. A failing CI check on a customer-repo PR
(lia-health-backend / -web / -iac) therefore never produced a fix-CI bead: red
PR CI sat unnoticed unless a human caught it (gs-xtyg). It now iterates every
rig with a `github.com` remote — the same per-rig pattern `ci-watcher-poll` and
`pr-watcher-poll` use via `$GT_TOWN_ROOT/mayor/rigs.json`.

The sibling watchers cover adjacent gaps: `ci-watcher-poll` watches
main/post-merge Actions, `pr-watcher-poll` actions review *comments*. This
watcher covers failing CI *checks* on open PRs.

## Implementation

This plugin is `execution.type = "script"` — the daemon runs `run.sh` directly
(it ships a `run.sh`, so the dispatcher executes it verbatim rather than
interpreting this markdown). The script:

1. Discovers rigs from `$GT_TOWN_ROOT/mayor/rigs.json`.
2. Filters to rigs whose `git_url` contains `github.com` (the only host `gh`
   can query) and resolves each repo (`owner/name`) from its `git_url`.
3. For each such rig, lists open **non-draft** PRs and collects failing
   required checks from `statusCheckRollup`. Draft PRs are excluded — they are
   not yet asking for review/merge. A check is treated as required when
   `isRequired` is true or absent (so a genuinely red PR is never silently
   dropped when GitHub omits the flag).
4. Files `ci-failure` beads routed to the owning rig (`bd` runs from
   `townRoot/<rig>`, so prefix routing reaches the rig's database),
   deduplicated by title against the rig's existing open `ci-failure` beads.
5. Isolates per-rig failures: one rig's poll failure does not abort the rest.

## require_review parking posture

The plugin only **files** work beads — it never merges or slings the PR. A PR
parked awaiting human review stays parked; the `ci-failure` bead is the standing
work item to fix the red check, after which the PR resumes its normal review
path. Filing a bead does not disturb the parking posture.

## Result

`run.sh` records its own `type:plugin-run` receipt with the per-rig outcome
(`polled` / `skipped` / `failed` / `beads_created`) for the cooldown gate and
digest pipeline.

+++
name = "pr-watcher-poll"
description = "Poll GitHub PRs per rig, turn unresolved reviewer comments into Gas Town work"
version = 1

[gate]
type = "cooldown"
duration = "5m"

[tracking]
labels = ["plugin:pr-watcher-poll", "category:pr-monitoring"]
digest = true

[execution]
type = "script"
timeout = "5m"
notify_on_failure = true
severity = "low"
+++

# PR Watcher Poll

For Gas Town instances that push via pull requests, a human reviewer leaves
review comments on a PR and someone has to read each one and dispatch the fix.
This plugin closes that gap: it runs `gt pr-watcher poll` for each rig with a
GitHub remote, every 5 minutes.

## Why a plugin (not a witness/refinery patrol)

The same properties that made `ci-watcher` a plugin apply here:

- **Town-wide cross-rig** — iterates every rig with a GitHub remote, not scoped
  to one agent.
- **Periodic, idempotent, resumable** — the actioned-comments ledger at
  `<townRoot>/.runtime/pr-watcher-actioned-<rig>` makes re-invocation safe; a
  comment is actioned at most once.
- **Shell-friendly** — exposes `gt pr-watcher poll`; no long-lived Go state.
- **Failure-isolated from agent lifecycle** — fires regardless of witness /
  refinery health.

## Behavior

On each unresolved review comment on an open PR:

1. Triage (heuristic, gate-by-default):
   - **Mechanical** (typo, gofmt, rename, formatting, lint) → create a bead
     labeled `pr-review-comment` and `gt sling` it to the rig so a fresh
     polecat fixes and re-pushes to the PR.
   - **Judgment** (everything else — why/consider/refactor/security/race/
     correctness, an unrecognized comment, or a trailing question) → create a
     bead labeled `needs-human-triage`, mail the mayor at HIGH for
     confirmation, and do NOT auto-sling.
2. Post an ack reply on the PR so the reviewer sees the comment was picked up.

**Gate-by-default safety:** judgment wins over mechanical. A comment that mixes
a mechanical ask with any judgment signal gates rather than auto-dispatches.
Comments are only auto-dispatched when a junior could apply them blindly and be
right.

**Cold start:** on the first-ever poll for a rig (no actioned ledger), only
comments created within the last 24h (`DefaultColdStartLookback`) are
dispatched; an older review backlog is recorded as actioned but not dispatched,
so a fresh poller does not flood the rig.

## Implementation

This plugin is `execution.type = "script"` — the daemon runs `run.sh` directly.
The script:

1. Discovers rigs from `$GT_TOWN_ROOT/mayor/rigs.json`.
2. Filters to rigs whose `git_url` contains `github.com` (the only host
   `gt pr-watcher poll` knows how to query — it uses the `gh` CLI).
3. For each GitHub-backed rig, runs `gt pr-watcher poll --rig <rig>` with
   per-rig failure isolation: one rig's failure does NOT abort the rest.
4. Records a `type:plugin-run` receipt for the cooldown gate and digest.

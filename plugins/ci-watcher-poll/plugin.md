+++
name = "ci-watcher-poll"
description = "Poll GitHub Actions per rig, reopen broke-main beads, freeze MQ on failure"
version = 1

[gate]
type = "cooldown"
duration = "3m"

[tracking]
labels = ["plugin:ci-watcher-poll", "category:ci-monitoring"]
digest = true

[execution]
type = "script"
timeout = "5m"
notify_on_failure = true
severity = "low"
+++

# CI Watcher Poll

Last line of defense against bad commits landing on `main`. Runs `gt ci-watcher
poll` for each rig with a GitHub remote, every 3 minutes.

## Why a plugin (not a witness/refinery patrol)

`ci-watcher` properties match the plugin abstraction exactly:

- **Town-wide cross-rig** — iterates every rig with a GitHub remote, not scoped
  to one agent.
- **Periodic, idempotent, resumable** — the seen-runs file at
  `<townRoot>/.runtime/ci-watcher-seen-<rig>` makes re-invocation safe.
- **Shell-friendly** — already exposes `gt ci-watcher poll`; no internal Go
  state needed beyond what the CLI manages.
- **Failure-isolated from agent lifecycle** — if a witness or refinery is
  dead, the ci-watcher should still fire (otherwise we have the same
  supervision-gap pattern we just hardened against in gu-rh0g + gu-0nmw).

## Behavior

On a failed CI run on the rig's default branch:
- Identifies the responsible bead from the commit subject (Gas Town
  conventional commit format).
- Reopens the bead with the `broke-main-ci` label.
- Mails mayor at HIGH priority.
- Writes the freeze flag at `<townRoot>/.runtime/mq-freeze-<rig>`.
- Refinery's freeze guard refuses to merge until cleared.

On a passing CI run after a failure: the freeze is cleared automatically.

State files at `<townRoot>/.runtime/ci-watcher-seen-<rig>` dedupe runs across
invocations so a transient flake doesn't fire twice.

**Cold start:** on the first-ever poll for a rig (no seen-runs ledger), the
watcher only escalates failures that completed within the last 2h
(`DefaultColdStartLookback`); older historical failures are recorded as seen
but not escalated. This stops a fresh or rebuilt daemon from re-escalating
every past CI failure across all of history (gs-qth). Once the ledger exists,
every unseen failure escalates as normal — so a break that landed during a
daemon downtime gap is still caught.

**Superseded breaks:** a failed run is suppressed (recorded as seen, not
escalated) when a *later* passing run on the target branch already resolved
the break — in the merge-queue model main freezes on a break, so a newer green
run means the queue advanced past the failing commit. This applies on warm
polls too, so a rebuilt ledger or a wide fetch window that re-surfaces an old,
already-resolved failure does not re-escalate it (gs-218). The current break —
the newest failure with no later passing run — is never suppressed.

## Implementation

This plugin is `execution.type = "script"` — the daemon runs `run.sh`
directly. The script:

1. Discovers rigs from `~/gt/mayor/rigs.json`.
2. Filters to rigs whose `git_url` contains `github.com` (the only host
   `gt ci-watcher poll` knows how to query — it uses the `gh` CLI).
3. For each GitHub-backed rig, runs `gt ci-watcher poll --rig <rig>` with
   per-rig failure isolation: a single rig's poll failure does NOT abort
   the rest.
4. Records a `type:plugin-run` receipt for the cooldown gate and digest.

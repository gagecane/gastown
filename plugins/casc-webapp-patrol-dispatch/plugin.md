+++
name = "casc-webapp-patrol-dispatch"
description = "Daily dispatch of casc-webapp-patrol into casc_webapp (CodeGen Scheduler web app browser patrol)"
version = 1

[gate]
type = "cooldown"
duration = "23h"

[tracking]
labels = ["plugin:casc-webapp-patrol-dispatch", "category:webapp-monitoring"]
digest = true

[execution]
type = "script"
timeout = "15m"
notify_on_failure = true
severity = "medium"
+++

# CASC WebApp Patrol Dispatch

Once per day, sling the `casc-webapp-patrol` formula into the `casc_webapp` rig.
The formula (and its `scripts/casc-webapp-patrol.{sh,mjs}`) drives a headless
Chromium against the CodeGen Scheduler web app, runs a four-dimension check
suite (functional, interaction, performance, visual/a11y), and files deduped
`casw` beads. This plugin only *schedules* it.

## Why this lives in `gastown_upstream/plugins/`, not `casc_webapp`

Daemon plugins live in `~/gt/plugins/` (town-level), synced from the
`gastown_upstream` source repo — not from any individual rig. A `casc_webapp`
polecat cannot edit `~/gt/plugins/` (directory discipline forbids edits outside
its own worktree). So the dispatch wiring belongs in `gastown_upstream`, even
though the formula and the work it triggers are `casc_webapp`-specific. This
mirrors the `casc-patrol-dispatch` and `wiki-patrol-dispatch` precedents.

## Single target, single sling

Unlike `casc-patrol` (which fans out across Beta/Gamma/Prod AWS stages), this
patrol observes ONE web app URL (`target_url`, default the Beta CodeGen
Scheduler). So this plugin issues a single `gt sling`, not a per-stage loop.

## Cadence

Daily, via a **cooldown gate of 23h** — the daemon's `dispatchPlugins` path
honors cooldown gates, not cron gates (same constraint noted in
`casc-patrol-dispatch`). 23h gives once-per-24h cadence with an hour of slop for
daemon restarts. Switching to a true cron schedule is a follow-up if/when
cron-gate dispatch lands.

## Sling syntax — don't repeat gu-fc8h / gu-xd7b

```bash
gt sling casc-webapp-patrol casc_webapp --create \
  --var "project_path=$PROJECT_PATH" \
  --var "target_url=$TARGET_URL"
```

The formula is the FIRST POSITIONAL arg: `gt sling <formula> <rig>`. The
`--formula` FLAG is a separate apply-on-bead feature; passing it makes
`gt sling` read `casc_webapp` as the bead-to-sling and fail "deferred
dispatch requires a rig target" (gu-ono8h). `run_test.sh` asserts the
positional invocation shape.

## Project path resolution

The formula's preflight requires `project_path` to point at the casc_webapp
package working tree (the one containing `scripts/casc-webapp-patrol.sh`).
Resolution order:

1. `$CASC_WEBAPP_PATROL_PROJECT_PATH` (operator override)
2. `$HOME/gt/casc_webapp/crew/$USER` (the rig's crew working tree)
3. Fallback: skip with a diagnostic — the formula cannot run without it.

## Auth prerequisite

The patrol authenticates via the operator's Midway cookie (`~/.midway/cookie`).
If the cookie is missing or expired, the formula's preflight / checker escalate
HIGH (run `mwinit -o`). This plugin does not manage Midway.

## Manual trigger

```bash
gt plugin run casc-webapp-patrol-dispatch              # Run if gate allows
gt plugin run casc-webapp-patrol-dispatch --force      # Bypass cooldown
```

## Failure path

If `gt sling` fails, the script records a `result:failure` receipt and exits
non-zero. `notify_on_failure` raises a medium-severity escalation via the
deacon. The formula handles its own escalations on patrol-internal failures
(browser launch, expired Midway, checker crash).

## What this plugin does NOT do

- It does NOT drive the browser, classify findings, or file `casw` beads —
  that's the formula + scripts.
- It does NOT manage Midway credentials.
- It does NOT click mutating controls; the patrol itself is strictly read-only
  (it never triggers Delete / Run now / Pause or submits Create-schedule).

## Related

- `casc-webapp-patrol` formula + `scripts/casc-webapp-patrol.{sh,mjs}` (casc_webapp rig)
- `casc-patrol-dispatch` / `wiki-patrol-dispatch` — precedent plugins
- `gu-fc8h` / `gu-xd7b` — `gt sling` API drift lessons

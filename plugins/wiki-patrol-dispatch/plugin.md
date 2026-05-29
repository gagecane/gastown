+++
name = "wiki-patrol-dispatch"
description = "Daily dispatch of mol-casc-wiki-patrol to casc_cdk/polecats/wiki-patrol"
version = 1

[gate]
type = "cooldown"
duration = "23h"

[tracking]
labels = ["plugin:wiki-patrol-dispatch", "category:scheduler"]
digest = true

[execution]
type = "script"
timeout = "5m"
notify_on_failure = true
severity = "medium"
+++

# Wiki Patrol Dispatch

Once per day, sling the `mol-casc-wiki-patrol` formula to the dedicated
`casc_cdk/polecats/wiki-patrol` polecat so the CASC wiki stays current with
mainline RigFacts. The formula itself (cadk-1o2t / b9ecb4b) handles the
work — pull, regenerate, secrets-scan, publish-on-change. This plugin only
schedules it.

## Why this lives here (gastown_upstream/plugins/), not casc_cdk

Daemon plugins live in `~/gt/plugins/` (town-level) which is synced from the
gastown source repo, not from any individual rig. A `casc_cdk` polecat
cannot edit `~/gt/plugins/` — directory discipline forbids edits outside
the polecat's own worktree. So the dispatch wiring belongs in
`gastown_upstream`, even though the formula and the work it triggers are
casc_cdk-specific. See gu-ck0j for the refile from cadk-74a4.

## Cadence

The Phase 2 design (cv-jl42a, cadk-wss3 OQ#5 disposition) resolved the
cadence to **daily**. Phase 1 evidence showed that mechanical RigFacts
change at most weekly across the 8 rigs, and 6-hourly cadence is 4× more
expensive for under 24h freshness improvement.

The plugin uses a **cooldown gate of 23h** rather than a cron gate
(`type = "cron"`) because the daemon's `dispatchPlugins` path currently
honors only cooldown gates (see `internal/daemon/handler.go`). A 23h
cooldown gives a once-per-24h cadence with an hour of slop to absorb
daemon restarts and patrol jitter. The mayor's original suggestion of a
cron gate (`0 9 * * *`) is captured here as design intent; switching to
a true cron schedule is a follow-up if/when cron-gate dispatch lands.

## Single-instance enforcement

The patrol formula description (mol-casc-wiki-patrol) requires that
overlapping runs MUST NOT execute concurrently — Phase 1 cadk-xk4 confirmed
concurrent writes multiply 429s on `raw write`. The formula scheduler
enforces this on the casc_cdk side (the dedicated wiki-patrol polecat
holds at most one wisp at a time), but this dispatcher adds defense in
depth: before slinging, `run.sh` lists open beads attached to
`mol-casc-wiki-patrol` for `casc_cdk/polecats/wiki-patrol` and aborts if
any are in flight.

## Sling target

The formula slings to `casc_cdk/polecats/wiki-patrol` — the dedicated
Midway-scoped patrol identity established by cadk-xvsz (Phase 2 Step 5
follow-up, merged 2026-05-29). The polecat is created on-demand via
`gt sling --create` if it does not yet exist; this is the canonical
auto-spawn pattern for rig-scoped polecats.

## Manual trigger

```bash
gt plugin run wiki-patrol-dispatch              # Run if gate allows
gt plugin run wiki-patrol-dispatch --force      # Bypass cooldown
```

The dog dispatches `bash run.sh`. The script does the actual sling.

## Failure path

If `gt sling` fails — wiki-patrol polecat not provisioned, formula not
found in the casc_cdk worktree, Dolt connectivity issue — `run.sh` records
a `result:failure` receipt bead and exits non-zero. The `notify_on_failure`
setting in execution config raises a medium-severity escalation through the
deacon. This plugin does NOT itself escalate (the formula handles its own
escalations on patrol failures).

## What this plugin does NOT do

- It does NOT regenerate the wiki, scan for secrets, or publish pages —
  that's the formula's job (`mol-casc-wiki-patrol`).
- It does NOT enforce single-molecule guarantees at the gas-town scheduler
  level — that's the formula's job (it owns the wiki-patrol polecat
  identity, which is a single-slot resource).
- It does NOT manage the wiki-patrol polecat's lifecycle, Midway scope, or
  credentials — those live with the polecat identity (cadk-xvsz).

## Related

- `mol-casc-wiki-patrol` — the formula this plugin slings (cadk-1o2t)
- `cadk-xvsz` — dedicated wiki-patrol polecat identity (closed-merged)
- `cv-jl42a` — Phase 2 design doc
- `gu-ck0j` — this bead (refile of cadk-74a4)

+++
name = "wiki-patrol-dispatch"
description = "Daily dispatch of mol-casc-wiki-patrol into the casc_cdk rig"
version = 1

[gate]
type = "cron"
schedule = "0 9 * * *"

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

Once per day, sling the `mol-casc-wiki-patrol` formula into the `casc_cdk`
rig so the CASC wiki stays current with mainline RigFacts. The formula
itself (cadk-1o2t / b9ecb4b) handles the work — pull, regenerate,
secrets-scan, publish-on-change. This plugin only schedules it.

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

Daily at **09:00 host-local**, via a **cron gate** (`schedule = "0 9 * * *"`).
The daemon's `dispatchPlugins` path evaluates cron gates through
`Recorder.CronDue` (gastown `1b5cbecb`, "wire cron-gate evaluation into plugin
dispatch") — `parseCron` reads a standard 5-field expression and the schedule is
matched against the daemon host's local clock. The in-flight grace
(`DispatchGrace`, ~`execution.timeout` + buffer) suppresses a re-dispatch storm
around a freshly-slung run, so a missed heartbeat won't double-fire.

This replaces the previous 23h cooldown gate, which gave only ~once-daily
cadence with accumulating drift because the daemon did not yet honor cron gates.
A fixed off-peak time (09:00) is now possible and removes that drift.

## Single-instance enforcement

The patrol formula description (mol-casc-wiki-patrol) requires that
overlapping runs MUST NOT execute concurrently — Phase 1 cadk-xk4 confirmed
concurrent writes multiply 429s on `raw write`. The formula scheduler
enforces this on the casc_cdk side, but this dispatcher adds defense in
depth: before slinging, `run.sh` lists open beads attached to
`mol-casc-wiki-patrol` whose assignee is any `casc_cdk/polecats/*` and
aborts if any are in flight.

## Sling target

The formula slings to the `casc_cdk` rig (bare rig name). `gt sling`
auto-resolves a polecat — reusing an idle one or spawning a fresh one
when `--create` is set. This is the canonical syntax for rig-scoped
formula dispatch under deferred dispatch (`scheduler.max_polecats > 0`),
which rejects polecat-qualified targets like
`casc_cdk/polecats/wiki-patrol` with "is not a known rig" (gu-fc8h).

The dedicated `wiki-patrol` polecat identity (cadk-xvsz) remains in the
rig's identity pool but is no longer pinned at sling time. Per cadk-xvsz
findings, the Midway scope-narrowing the named identity was meant to
enforce isn't implementable, so the polecat-name pin no longer adds
isolation.

## Manual trigger

```bash
gt plugin run wiki-patrol-dispatch              # Run if gate allows
gt plugin run wiki-patrol-dispatch --force      # Bypass gate
```

The dog dispatches `bash run.sh`. The script does the actual sling.

## Failure path

If `gt sling` fails — rig not present, formula not found in the casc_cdk
worktree, Dolt connectivity issue — `run.sh` records
a `result:failure` receipt bead and exits non-zero. The `notify_on_failure`
setting in execution config raises a medium-severity escalation through the
deacon. This plugin does NOT itself escalate (the formula handles its own
escalations on patrol failures).

## What this plugin does NOT do

- It does NOT regenerate the wiki, scan for secrets, or publish pages —
  that's the formula's job (`mol-casc-wiki-patrol`).
- It does NOT enforce single-molecule guarantees at the gas-town scheduler
  level — that's the formula scheduler's job. The in-flight check in
  `run.sh` is defense in depth, not the primary guarantee.
- It does NOT manage polecat lifecycles, Midway scopes, or credentials —
  those live with the rig and (informationally) the wiki-patrol polecat
  identity (cadk-xvsz).

## Related

- `mol-casc-wiki-patrol` — the formula this plugin slings (cadk-1o2t)
- `cadk-xvsz` — dedicated wiki-patrol polecat identity (closed-merged)
- `cv-jl42a` — Phase 2 design doc
- `gu-ck0j` — this bead (refile of cadk-74a4)

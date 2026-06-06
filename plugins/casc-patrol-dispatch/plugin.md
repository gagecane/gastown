+++
name = "casc-patrol-dispatch"
description = "Daily dispatch of casc-patrol into casc_cdk for Beta/Gamma/Prod"
version = 1

[gate]
type = "cron"
schedule = "0 9 * * *"

[tracking]
labels = ["plugin:casc-patrol-dispatch", "category:scheduler"]
digest = true

[execution]
type = "script"
timeout = "15m"
notify_on_failure = true
severity = "medium"
+++

# CASC Patrol Dispatch

Once per day, sling the `casc-patrol` formula into the `casc_cdk` rig — once
per stage (Beta, Gamma, Prod). The formula itself (cadk-t4mh) handles the
read-only AWS observation, classification, and bead filing; this plugin only
schedules it.

## Why this lives here (gastown_upstream/plugins/), not casc_cdk

Daemon plugins live in `~/gt/plugins/` (town-level) which is synced from the
gastown source repo, not from any individual rig. A `casc_cdk` polecat
cannot edit `~/gt/plugins/` — directory discipline forbids edits outside
the polecat's own worktree. So the dispatch wiring belongs in
`gastown_upstream`, even though the formula and the work it triggers are
casc_cdk-specific. See gu-wuzn1 (refile of cadk-n86w) for the same constraint
that produced the wiki-patrol-dispatch precedent (gu-ck0j).

## Cadence

Phase 1c (cadk-t4mh) shipped the formula with daily-noon-PT cadence
(`CRON_DAILY_NOON_PT` in `lib/monitor/policy.ts`). The cv-tcoby disposition
ran all three stages on the same cadence; this plugin reflects that decision
by slinging Beta, Gamma, and Prod on every dispatch.

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

## What the script does

`run.sh` loops over Beta, Gamma, Prod and for each stage:

1. Slings `casc-patrol` to `casc_cdk` with `--var stage=<S>` and
   `--var project_path=<resolved>`.
2. Records a per-stage receipt bead with `result:success|skipped|failure`
   so a Gamma failure does not mask Beta success.
3. Continues to the next stage even if the prior stage's sling failed —
   each stage is independent.

The script exits 0 if all three stages slung (or were correctly skipped),
non-zero if any stage failed.

## Sling syntax — don't repeat gc-u2wjn / gc-0g2r5

The `wiki-patrol-dispatch` plugin shipped twice in a broken state because
it used outdated `gt sling` syntax (gu-fc8h, gu-xd7b). This plugin uses
the canonical form from the start:

```bash
gt sling casc-patrol casc_cdk --create \
  --var "stage=$STAGE" \
  --var "project_path=$PROJECT_PATH"
```

The formula is the FIRST POSITIONAL arg: `gt sling <formula> <rig>`. The
`--formula` FLAG is a separate apply-on-bead feature; passing it makes
`gt sling` read `casc_cdk` as the bead-to-sling and fail "deferred dispatch
requires a rig target" (gu-ono8h). `run_test.sh` asserts the positional
invocation shape.

## Project path resolution

The formula's preflight requires `project_path` to point at the casc_cdk
Brazil package working tree. Resolution order:

1. `$CASC_PATROL_PROJECT_PATH` (operator override)
2. `/workplace/$USER/CodegenAgentScheduler/src/CodegenAgentSchedulerCDK`
3. Fallback: skip with a diagnostic — the formula cannot run without it.

## Manual trigger

```bash
gt plugin run casc-patrol-dispatch              # Run if gate allows
gt plugin run casc-patrol-dispatch --force      # Bypass gate
```

## Failure path

If a per-stage `gt sling` fails, the script files a `result:failure`
receipt for that stage and continues to the next. After processing all
three stages, the script exits non-zero if any stage failed. The
`notify_on_failure` setting raises a medium-severity escalation through
the deacon; the formula handles its own escalations on patrol-internal
failures.

## What this plugin does NOT do

- It does NOT observe AWS, classify findings, or file observation beads —
  that's the formula's job (`casc-patrol`).
- It does NOT enforce single-instance semantics. Unlike wiki-patrol (where
  concurrent writes multiply 429s — cadk-xk4), the patrol is read-only and
  per-stage AWS-profile-isolated, so there is no cross-stage write
  contention. A stale Gamma run overlapping a fresh Beta run is fine.
- It does NOT manage AWS profiles, Conduit credentials, or stage
  isolation — those are operator-host concerns enforced by the formula's
  preflight step and `lib/monitor/policy.ts arnAllowlistFor(stage)`.

## Related

- `casc-patrol` formula (cadk-t4mh, Phase 1c)
- `gu-wuzn1` — this bead (refile of cadk-n86w)
- `gu-ck0j` / `wiki-patrol-dispatch` — precedent plugin
- `gu-fc8h` / `gu-xd7b` — `gt sling` API drift lessons

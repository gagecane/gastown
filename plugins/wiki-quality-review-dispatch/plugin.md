+++
name = "wiki-quality-review-dispatch"
description = "Weekly dispatch of mol-casc-wiki-quality-review into the casc_cdk rig"
version = 1

[gate]
type = "cooldown"
duration = "168h"

[tracking]
labels = ["plugin:wiki-quality-review-dispatch", "category:scheduler"]
digest = true

[execution]
type = "script"
timeout = "10m"
notify_on_failure = true
severity = "medium"
+++

# Wiki Quality Review Dispatch

Once per week, sling the `mol-casc-wiki-quality-review` formula into the
`casc_cdk` rig so the CASC wiki gets a proactive whole-corpus quality review.
The formula itself (cadk-hofe / Phase 3.5) handles the work — regenerate
`.wiki-output/`, run the Phase 3.3 reviewer over each page against the 4-axis
rubric (completeness, navigability, redundancy, structure), then file deduped,
≤5/run, triage-guarded `wiki-quality` beads. This plugin only schedules it.

This is the WEEKLY, proactive counterpart to the DAILY, reactive
`wiki-patrol-dispatch` (cadk-n86w). The two are deliberately separate: the
daily patrol owns incident-time RigFacts freshness; this weekly reviewer
surfaces slow-moving structural / IA quality issues.

## Why this lives here (gastown_upstream/plugins/), not casc_cdk

Daemon plugins live in `~/gt/plugins/` (town-level) which is synced from the
gastown source repo, not from any individual rig. A `casc_cdk` polecat
cannot edit `~/gt/plugins/` — directory discipline forbids edits outside
the polecat's own worktree. So the dispatch wiring belongs in
`gastown_upstream`, even though the formula and the work it triggers are
casc_cdk-specific. This mirrors `wiki-patrol-dispatch` (gu-ck0j refile
precedent).

## Cadence

WEEKLY (formula decision Q8). A whole-corpus IA/quality review surfaces
slow-moving structural issues, not incident-time facts, so weekly is
sufficient — the daily patrol owns freshness.

The plugin uses a **cooldown gate of 168h** (7 days) rather than a cron gate
(`type = "cron"`) because the daemon's `dispatchPlugins` path currently
honors only cooldown gates (see `internal/daemon/handler.go`). A 168h
cooldown gives a once-per-week cadence. Note the duration is expressed in
hours, not days: the gate is parsed by Go's `time.ParseDuration`, which
accepts only `h`/`m`/`s` units — a `7d` literal errors every heartbeat
(gu-vir5r). The intent is an off-peak schedule
(e.g. Monday ~05:00 PT, before the daily patrol); switching to a true cron
schedule is a follow-up if/when cron-gate dispatch lands.

### Cooldown re-arm caveat (gu-50nbo)

Cooldown-gate plugins currently count DISPATCH, not EXECUTION (gu-50nbo): a
failed dispatch still re-arms the 7d cooldown. At weekly cadence the blast
radius is one skipped review week. The single-instance check below skips
(exit 0, `result:skipped`) rather than failing when a run is already in
flight, and a true sling failure records `result:failure` and raises a
medium-severity escalation via `notify_on_failure` — so an operator is
notified rather than silently losing a week. Land alongside / after
gu-50nbo's fix for a clean execution-gated re-arm.

## Separate token budget

The reviewer's per-run Bedrock budget (`token_budget`, default 80000 in the
formula) is the POC-measured cap and is DECOUPLED from the daily patrol's
`WIKI_PATROL_TOKEN_BUDGET` — a full-corpus Opus pass can never blow the daily
fact-patrol budget. The reviewer is heavier than the daily patrol, so this
plugin's `[execution] timeout` is 10m (vs the daily's 5m). A budget bump is
tracked in cadk-zr52.

## Single-instance enforcement

The reviewer formula (mol-casc-wiki-quality-review) requires the
single-molecule guarantee: two reviewers running concurrently would race on
the durable seen-store dedup and the ≤5/run cap (both assume one writer). The
formula scheduler enforces this on the casc_cdk side, but this dispatcher adds
defense in depth: before slinging, `run.sh` lists open beads attached to
`mol-casc-wiki-quality-review` whose assignee is any `casc_cdk/polecats/*` and
aborts (skip) if any are in flight.

## Sling target

The formula slings to the `casc_cdk` rig (bare rig name). `gt sling`
auto-resolves a polecat — reusing an idle one or spawning a fresh one
when `--create` is set. This is the canonical syntax for rig-scoped
formula dispatch under deferred dispatch (`scheduler.max_polecats > 0`),
which rejects polecat-qualified targets like
`casc_cdk/polecats/<name>` with "is not a known rig" (gu-fc8h).

## Manual trigger

```bash
gt plugin run wiki-quality-review-dispatch              # Run if gate allows
gt plugin run wiki-quality-review-dispatch --force      # Bypass cooldown
```

The dog dispatches `bash run.sh`. The script does the actual sling.

## Failure path

If `gt sling` fails — rig not present, formula not found in the casc_cdk
worktree, Dolt connectivity issue — `run.sh` records a `result:failure`
receipt bead and exits non-zero. The `notify_on_failure` setting in execution
config raises a medium-severity escalation through the deacon. This plugin
does NOT itself escalate (the formula handles its own escalations on review
failures).

## What this plugin does NOT do

- It does NOT regenerate the wiki, run the reviewer, or file findings —
  that's the formula's job (`mol-casc-wiki-quality-review`).
- It does NOT publish or edit wiki content — the reviewer only FILES beads.
- It does NOT enforce single-molecule guarantees at the gas-town scheduler
  level — that's the formula scheduler's job. The in-flight check in
  `run.sh` is defense in depth, not the primary guarantee.

## Related

- `mol-casc-wiki-quality-review` — the formula this plugin slings (cadk-hofe, Phase 3.5)
- `wiki-patrol-dispatch` — the daily reactive counterpart (gu-ck0j / cadk-n86w)
- `gu-50nbo` — cooldown-gate counts dispatch not execution (re-arm caveat)
- `cadk-zr52` — reviewer token budget bump follow-up
- `cadk-w8a9` — arch-table follow-up

+++
name = "curio-retrospect-dispatch"
description = "Nightly dispatch of the Curio Retrospect polecat (LLM hypothesizer lane)"
version = 1

[gate]
type = "cron"
schedule = "0 8 * * *"

[tracking]
labels = ["plugin:curio-retrospect-dispatch", "category:scheduler", "curio"]
digest = true

[execution]
type = "script"
timeout = "30m"
notify_on_failure = true
severity = "low"
+++

# Curio Retrospect Dispatch

Once per day at **08:00 UTC**, render the deterministic Curio precision digest
and sling the `mol-curio-retrospect` formula into the `gastown_upstream` rig. The
formula drives a polecat that reads the digest and PROPOSES tuning work (a config
CR, a proposal bead, or a hypothesis bead) for a human to dispose. This plugin
only schedules and guards the dispatch — it does no reasoning itself.

This is Curio Phase 3 **B5** (epic `gu-60sk4`, child bead `gu-5d8os`). It mirrors
`casc-patrol-dispatch` / `wiki-patrol-dispatch`.

## Why this lives in `gastown_upstream/plugins/` (town-level)

Daemon plugins live in `~/gt/plugins/` (town-level), synced from the gastown
source repo (`gastown_upstream`), not from any individual rig. The Curio lane's
substrate (`cmd/curio-proposer`, `internal/curio`) and the `mol-curio-retrospect`
formula are part of the gastown source, so the dispatch wiring belongs here. See
`wiki-patrol-dispatch` / `casc-patrol-dispatch` for the same constraint.

## Cadence

Daily at **08:00 host-UTC**, via a **cron gate** (`schedule = "0 8 * * *"`).
08:00 UTC is past the overnight-batch busy window, so the closed window the
digest reads is settled. The daemon's `dispatchPlugins` path evaluates cron gates
through `Recorder.CronDue` (`parseCron` reads a standard 5-field expression
matched against the daemon host's local clock). The in-flight grace
(`DispatchGrace`, ~`execution.timeout` + buffer) suppresses a re-dispatch storm
around a freshly-slung run, so a missed heartbeat won't double-fire.

Nightly is the right cadence because outcome data (ledger precision) changes on
the order of days, not minutes — frequent enough to catch drift, sparse enough to
keep CR volume and cost negligible (design-doc Q1).

## What the script does

`run.sh` runs four guards then dispatches (design-doc Q1 + Q7):

1. **Kill-switch pre-check.** Read `mayor/daemon.json`
   `patrols.curio.llm.enabled`. If false/absent → `result:skipped`, exit 0.
   This is the same projection `cmd/curio-proposer/config.go` reads, and the
   lane is OFF by default. Flipping the flag OR uninstalling the plugin both
   disable the lane (defense in depth). It gates on `curio.llm.enabled` ONLY —
   never `curio.enabled` — so toggling the Retrospect lane never touches the
   live Patrol, and vice versa.
2. **Single-instance guard.** Skip (`result:skipped`) if a *fresh* open bead
   attached to `mol-curio-retrospect` is still in flight in the target rig —
   prevents a slow review from stacking a second polecat. See "Anti-wedge"
   below for the staleness semantics.
3. **Volume circuit breaker.** Skip (`result:skipped`) if the count of open
   `curio-proposal` beads is at/above the ceiling (default 10,
   `CURIO_PROPOSAL_CEILING`). An unreviewed backlog must be worked down before
   the lane adds more (design-doc Q7.3).
4. **Render + sling.** `curio-proposer --emit-digest <path>`, then
   `gt sling mol-curio-retrospect gastown_upstream --var digest_path=<path>
   --var max_proposals=<N>`. Record a `type:plugin-run` success receipt.

Lane-off and all three skip paths exit 0 — a disabled or guarded lane is the
expected posture, not an outage (`severity = "low"`).

## Digest-path sandbox contract

The formula's step 1 is `cat {{digest_path}}` — so the digest MUST live on a
path the slung polecat can read from inside its worktree. Polecats run in
isolated git **worktrees**, but those worktrees live on the **same host
filesystem** under the town root — there is no chroot/container boundary. So a
host-shared absolute path under the town root is readable from any worktree
without staging.

`run.sh` therefore renders the digest to:

```
$GT_TOWN_ROOT/artifacts/curio-retrospect/digest-<UTCstamp>.md
```

and passes **that exact path** as `--var digest_path=`. It deliberately does NOT
write into the plugin's CWD (the plugin directory), which the formula polecat
never sees. `run_test.sh` asserts this path-contract: the path the digest is
emitted to and the path slung are one and the same shell variable, anchored under
the town root.

## Anti-wedge: single-instance staleness (gc-i2nb6l Risk #4)

The single-instance and volume guards are check-then-act against Dolt state the
polecat itself mutates. A polecat that died after filing beads but before its
convoy closed would leave an in-flight marker that makes every subsequent night
`result:skipped` — wedging the lane.

To prevent that, the single-instance guard treats an in-flight marker whose
`updated_at` is older than the formula timeout (`30m`,
`CURIO_RETROSPECT_STALE_SECS`) as **dead** and ignores it — mirroring the witness
`MAYBE_DEAD` discipline. Only a *fresh* in-flight marker blocks dispatch, so a
crashed prior run cannot wedge the lane. `run_test.sh` asserts a stale prior run
does NOT block the next dispatch.

## Sling syntax — don't repeat gu-fc8h / gu-ono8h

```bash
gt sling mol-curio-retrospect gastown_upstream --create \
  --var "digest_path=$DIGEST_PATH" \
  --var "max_proposals=$MAX_PROPOSALS"
```

The formula is the FIRST POSITIONAL arg: `gt sling <formula> <rig>`. The
`--formula` FLAG is a separate apply-on-bead feature; passing it makes `gt sling`
read the rig as the bead-to-sling and fail "deferred dispatch requires a rig
target" (gu-ono8h). `run_test.sh` asserts the positional invocation shape.

## curio-proposer resolution

`curio-proposer` is its own write-incapable binary (`cmd/curio-proposer`), not
installed to a stable PATH location. `run.sh` resolves it: a binary on PATH or at
a gastown source-rig root, else `go run ./cmd/curio-proposer` from the discovered
source rig (the same source discovery `rebuild-gt` uses, preferring the
`gastown_upstream` fork name).

## Manual trigger

```bash
gt plugin run curio-retrospect-dispatch              # Run if gate allows
gt plugin run curio-retrospect-dispatch --force      # Bypass cron gate
```

## What this plugin does NOT do

- It does NOT reason about precision, classify clusters, or open CRs/beads —
  that's the formula's job (`mol-curio-retrospect`, B4).
- It does NOT enforce the per-run proposal cap or dedup at the agent layer —
  those are advisory prompt-level guidance in the formula. The MECHANICAL guards
  are this plugin's volume breaker and B6's `cluster:<id>` dedup linkage.
- It does NOT write to the Curio ledger or touch the live Patrol — it only reads
  the kill switch and renders the read-only digest the proposer produces.

## Related

- `mol-curio-retrospect` formula (B4, `gu-9lm9u`)
- `curio-proposer --emit-digest` (B1, `gu-7wv36`); air-gap filter (B2)
- B3 replay overlay (`gu-zx7e0`); B6 proposal taxonomy + dedup + CI guard
  (`gu-9tmry`)
- `curio-proposal-expiry` (B8, `gu-5zf4t`) — proposal expiry + breaker-reset
  (anti-wedge); runs 30m before this plugin so a sweep can free the volume
  breaker for the same night's dispatch
- `casc-patrol-dispatch` / `wiki-patrol-dispatch` — precedent plugins
- `gu-fc8h` / `gu-ono8h` — `gt sling` API drift lessons
- Design: `.designs/curio-p3-retrospect-agent/{design-doc.md,child-beads.md}`

+++
name = "curio-dispatch-reconcile"
description = "Nightly Curio Retrospect dispatch reconcile — close the success-of-dispatch != proposal-filed observability gap (gu-ac2bu fix #3)"
version = 1

[gate]
type = "cron"
schedule = "0 9 * * *"

[tracking]
labels = ["plugin:curio-dispatch-reconcile", "category:observability", "curio"]
digest = true

[execution]
type = "script"
timeout = "10m"
notify_on_failure = true
severity = "low"
+++

# Curio Dispatch Reconcile

Once per day at **09:00 UTC** — one hour *after* the 08:00 Retrospect dispatch
(`curio-retrospect-dispatch`, B5) and well past that run's 30m formula timeout —
this plugin closes the observability gap surfaced by bug **gu-ac2bu**:

> **success-of-dispatch != proposal-filed.** B5's `run.sh` records
> `result:success` the instant the `gt sling` succeeds, then exits. It cannot
> observe the polecat's terminal write: if the polecat reasons correctly but its
> session *dies before the final `bd create` lands*, the dispatch receipt still
> reads success while the run's only deliverable — the proposal — never
> materialized. The reasoning was sound; the output was silently dropped.

This plugin is the async reconcile B5 cannot do inline. It runs after the slung
run has definitely terminated, correlates what the run was *expected* to produce
against what *actually landed*, and records a `type:plugin-run` reconcile receipt
— `result:success` when the two agree (including a legitimately quiet night), or
`result:warning` plus a one-shot escalation when an actionable run filed nothing
(the gu-ac2bu silent-drop signature).

This is Curio Phase 3 follow-up **gu-l84k2 fix #3** (epic `gu-60sk4`, parent bug
`gu-ac2bu`). See `.designs/curio-p3-retrospect-agent/{design-doc.md,child-beads.md}`.

## Why this lives in `gastown_upstream/plugins/` (town-level)

Daemon plugins live in `~/gt/plugins/` (town-level), synced from the gastown
source repo. The Curio lane's substrate (`internal/curio`, the digest renderer,
the proposal-bead conventions in `docs/curio-proposals.md`) is part of the
gastown source, so this maintenance/observability wiring belongs here — exactly
as `curio-retrospect-dispatch` (B5) and `curio-proposal-expiry` (B8) do.

## Cadence

Daily at **09:00 host-UTC**, via a **cron gate** (`schedule = "0 9 * * *"`).
The 08:00 dispatch renders its digest and slings a polecat with a 30m formula
timeout; by 09:00 that run has either filed its proposals via `gt done` or died.
Running an hour later reconciles *that morning's* run — tight feedback, not a
day late — while staying safely past the formula timeout so a still-in-flight
run is never mis-flagged. A `CURIO_RECONCILE_MIN_AGE_SECS` floor (default 40m,
above the 30m timeout) is the belt-and-suspenders guard against reconciling a
run that has not yet terminated.

## Why the digest + filed beads, and NOT the slung wisp

The intuitive correlation — "find the slung Retrospect wisp and check whether it
filed a proposal" — is **infeasible the morning after**: a dead polecat's
molecule wisp is reaped within minutes by the daemon's orphan-molecule
reconcile pass (`reconcileOrphanMolecules`) and the recycled agent bead is
compacted away, so the wisp that the dispatch slung (e.g. `gu-wisp-vxza` in the
bug) no longer resolves by the time this plugin runs. The reconcile must use
signals that *survive*:

1. **The digest file** the dispatch rendered:
   `artifacts/curio-retrospect/digest-<UTCstamp>.md`. It is durable on disk, its
   filename carries the dispatch timestamp, and its fenced JSON block lists the
   run's actionable candidate `clusters[]` (each with a stable `cluster_id`).
   This is the record of what the run was *expected* to act on.
2. **Proposal / hypothesis beads** (`curio-proposal` / `curio-hypothesis`,
   B6 taxonomy). They are durable, carry `created_at`, and are stamped with a
   `cluster:<cluster_id>` dedup label. This is the record of what the run
   *actually landed*.

Correlating (1) against (2) over the dispatch window reconstructs proposal-filed
without ever touching the purged wisp.

## What the script does

`run.sh` is a single best-effort reconcile pass:

1. **Locate the dispatch.** Find the newest `digest-*.md` under the artifacts
   dir whose timestamp is within the lookback window
   (`CURIO_RECONCILE_LOOKBACK_SECS`, default 6h) **and** older than the min-age
   floor (`CURIO_RECONCILE_MIN_AGE_SECS`, default 40m). No such digest → the
   lane did not dispatch a terminated run in range (disabled, skipped by a
   guard, or still in flight): `result:skipped`, nothing to reconcile.
2. **Read what was expected.** Parse the digest's fenced JSON block; collect the
   set of actionable `cluster:<cluster_id>` keys from `clusters[]`.
   - **Empty** → the run had nothing to propose. A quiet night is a *correct,
     expected* result (the formula's finish step says so). `result:success`.
3. **Read what landed.** Count `curio-proposal` / `curio-hypothesis` beads whose
   `created_at` is at or after the digest's dispatch timestamp.
   - **≥ 1 filed** → the terminal write path worked: the run produced output.
     Because fix #1's file-first discipline files a one-line `curio-hypothesis`
     bead *before* even a threshold-tune CR, any non-empty run lands ≥ 1 bead, so
     a filed count ≥ 1 confirms proposal-filed. `result:success`.
4. **Disambiguate a zero-filed actionable run.** Zero filed against a non-empty
   digest is only a *gap* if there was genuinely new work. Subtract the clusters
   already covered by an open proposal/hypothesis bead (the B6 dedup query). If
   every actionable cluster was already covered, filing nothing was correct —
   `result:success`. Otherwise there were **uncovered actionable clusters and
   zero proposals filed since dispatch**: the gu-ac2bu silent-drop signature.
   `result:warning` + one ONE-SHOT escalation (deduped on the digest stamp).

## Verdict table

| Digest actionable clusters | Filed since dispatch | Uncovered actionable | Verdict | Meaning |
|---|---|---|---|---|
| 0 | — | — | `success` | Quiet night — nothing to propose, none needed. |
| ≥1 | ≥1 | — | `success` | Run landed output; terminal write path verified. |
| ≥1 | 0 | 0 | `success` | All actionable clusters already covered (deduped). |
| ≥1 | 0 | ≥1 | `warning` | **gu-ac2bu**: actionable work, zero filed — silent drop. |

## Why `result:warning`, not `result:failure`

The dispatch itself *succeeded* — the sling worked, the receipt was honest about
that. The gap is downstream of the sling, in the polecat's lifecycle. A warning
records the observability finding for the daemon digest and human attention
without claiming the dispatch plugin failed. `result:warning` (like
`result:failure`) is also exempt from the compactor's no-op-receipt TTL
promotion, so the finding persists rather than being silently aged out.

## Anti-false-positive discipline

The one rule that matters: **a legitimately quiet night must never warn.** An
empty digest (step 2) short-circuits to `success` before any filed-count check,
and an actionable-but-fully-deduped run (step 4) also resolves to `success`. The
only path to `warning` requires *both* a genuinely-new actionable cluster *and*
zero beads filed in the entire dispatch window — which, given fix #1's
file-first discipline, is the death-mid-run signature and nothing else.

## What this plugin does NOT do

- It does NOT re-dispatch a dropped run. Recovery is already covered: fix #1
  (file-first landing) bounds the loss to enrichment; B5's `STALE_AFTER_SECS`
  single-instance guard lets the *next* night re-dispatch a crashed run; and the
  daemon's general `reconcileOrphanMolecules` pass reaps the dead polecat's wisp.
  A curio-specific re-dispatcher would race that general pass — the same reason
  B8 (`gu-5zf4t`) scoped orphan re-dispatch OUT. This plugin only makes the gap
  *visible*; it does not act on the wisp.
- It does NOT write the `curio_ledger` — it has no Dolt write path. It reads
  beads + a digest file and records a receipt / escalation only.
- It does NOT reason about precision or open CRs/beads. It is a pure
  observability reconcile.

## Configuration

| Env var | Default | Meaning |
|---------|---------|---------|
| `CURIO_RECONCILE_LOOKBACK_SECS` | `21600` (6h) | How far back to look for the dispatch's digest. A digest older than this is ignored (a missed daemon cycle gets caught the next day). |
| `CURIO_RECONCILE_MIN_AGE_SECS` | `2400` (40m) | Minimum digest age before reconciling — must exceed the formula's 30m timeout so a still-in-flight run is never mis-flagged. |

## Manual trigger

```bash
gt plugin run curio-dispatch-reconcile           # Run if gate allows
gt plugin run curio-dispatch-reconcile --force   # Bypass cron gate
```

## Related

- `curio-retrospect-dispatch` (B5, `gu-5d8os`) — the nightly dispatch whose
  proposal-filed disposition this plugin reconciles; runs 1h before this one.
- `curio-proposal-expiry` (B8, `gu-5zf4t`) — the anti-wedge maintenance sweep
  this plugin is modeled on (shell + run_test.sh, best-effort, own receipt).
- `mol-curio-retrospect` formula (B4, `gu-9lm9u`) — its file-first landing step
  (gu-ac2bu fix #1) is what makes "≥1 bead filed" a reliable proposal-filed
  signal.
- `curio-proposer --emit-digest` (B1) — renders the durable digest this plugin
  reads.
- `docs/curio-proposals.md` — proposal-bead taxonomy + the `cluster:` dedup
  label this plugin correlates on (B6, `gu-9tmry`).
- Parent bug `gu-ac2bu`; tracking task `gu-l84k2`.
- Design: `.designs/curio-p3-retrospect-agent/{design-doc.md,child-beads.md}`.

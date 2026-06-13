+++
name = "curio-proposal-expiry"
description = "Nightly Curio proposal expiry + volume-breaker-reset alert (anti-wedge)"
version = 1

[gate]
type = "cron"
schedule = "30 7 * * *"

[tracking]
labels = ["plugin:curio-proposal-expiry", "category:cleanup", "curio"]
digest = true

[execution]
type = "script"
timeout = "10m"
notify_on_failure = true
severity = "low"
+++

# Curio Proposal Expiry + Breaker-Reset

Once per day at **07:30 UTC** — deliberately *before* the 08:00 Retrospect
dispatch (`curio-retrospect-dispatch`, B5) — this plugin keeps the Retrospect
lane from silently self-wedging via the volume circuit breaker. It does two
independent things and is purely a maintenance sweep — it does no reasoning and
opens no CRs:

1. **Proposal expiry.** Auto-close `curio-proposal` / `curio-hypothesis` beads
   that have sat untouched past an expiry window, feeding a `deferred` (or
   configured) outcome back to the `curio_ledger` via the existing post-close
   reconciler. This bounds the unreviewed backlog so the volume breaker is not
   tripped forever by stale proposals.
2. **Breaker-reset alert.** When the volume breaker (open `curio-proposal` count
   ≥ ceiling) has been tripped for M consecutive days, emit a single
   low-severity escalation so a wedged lane is *visible* rather than silently
   halting. The alert fires exactly once per trip and re-arms when the breaker
   clears.

This is Curio Phase 3 **B8** (epic `gu-60sk4`, child bead `gu-5zf4t`). It is the
anti-wedge follow to B5's dispatch plugin; see
`.designs/curio-p3-retrospect-agent/{design-doc.md,child-beads.md §B8}`.

## Why this lives in `gastown_upstream/plugins/` (town-level)

Daemon plugins live in `~/gt/plugins/` (town-level), synced from the gastown
source repo (`gastown_upstream`). The Curio lane's substrate (`internal/curio`,
the `curio_ledger`, the proposal-bead conventions in `docs/curio-proposals.md`)
is part of the gastown source, so this maintenance wiring belongs here — exactly
as `curio-retrospect-dispatch` (B5) does.

## Cadence

Daily at **07:30 host-UTC**, via a **cron gate** (`schedule = "30 7 * * *"`).
Running 30 minutes *ahead* of the 08:00 dispatch means a night's expiry sweep
works the backlog down *before* that same night's dispatch evaluates the volume
breaker — so a proposal that ages out tonight can free the breaker for tonight's
run, not tomorrow's. Outcome data (ledger precision) and proposal staleness
change on the order of days, so nightly is the right cadence; the in-flight
grace (`DispatchGrace`) suppresses re-dispatch storms.

## What the script does

`run.sh` runs two independent passes (design-doc Q7, child-beads §B8). Each pass
is best-effort and records its own `type:plugin-run` receipt; a failure in one
does not abort the other.

### Pass 1 — proposal expiry

For each **open** `curio-proposal` / `curio-hypothesis` bead in the target rig
whose `updated_at` is older than the expiry window (default **14 days**,
`CURIO_PROPOSAL_EXPIRY_DAYS`):

1. Stamp a structured `curio-outcome:<code>` close-label (default `deferred`,
   `CURIO_EXPIRY_OUTCOME`) so the B0b reconciler classifies the close
   deterministically rather than guessing from free text.
2. `bd close` the bead with an explanatory reason.

The close then flows through the daemon's existing bead-close event stream
(`ConvoyManager` → `onCurioBeadClose`, B0b): for any bead that has a
`curio_ledger` row, the reconciler stamps `(outcome, resolved_at)` — closing the
L1 precision loop B0 opened. This plugin therefore **never writes the ledger
directly** (it has no Dolt write path); it only closes beads with an accurate
outcome and lets the trusted reconciler do the ledger write. A proposal bead
with no ledger row (the polecat-filed sketches/hypotheses) is simply closed —
the reconciler no-ops on it, which is correct.

Only `status == open` beads are swept. An `in_progress` bead is being actively
worked by a human and is left alone; a `blocked` bead has an explicit reason to
wait. Freshness is measured from `updated_at`, so any human touch (a comment, a
relabel) resets the clock.

### Pass 2 — breaker-reset alert

The volume breaker is "open" when the count of open `curio-proposal` beads is at
or above the ceiling (default **10**, `CURIO_PROPOSAL_CEILING` — the *same*
ceiling B5's dispatch plugin enforces). This pass tracks how long the breaker
has been continuously open in a small state file and alerts when that exceeds M
days (default **3**, `CURIO_BREAKER_ALERT_DAYS`):

- **Breaker closed** (count < ceiling): clear the state file. The lane is
  healthy; the next trip starts a fresh clock and re-arms the alert.
- **Breaker open, first observed**: record `open_since` (now) in the state file.
- **Breaker open ≥ M days, not yet alerted**: emit ONE `gt escalate`
  (severity `low`, deduped on a stable signature) and set `alerted: true` so the
  alert never spams while the trip persists. The escalation tells the operator
  to work the proposal backlog down (or raise the ceiling).

The state file is `$GT_TOWN_ROOT/artifacts/curio-retrospect/breaker-state.json`
(the same host-shared artifacts dir B5 uses). "Exactly once per trip" is the
`alerted` latch: it is set on first alert and only cleared when the breaker
closes — so a breaker open for a week alerts once, not seven times.

## Anti-wedge: this plugin IS the breaker reset

B5's volume breaker stops dispatch when the backlog is deep, but nothing worked
the backlog down — so a tripped breaker stayed tripped until a human noticed.
This plugin is the missing half: expiry (pass 1) bounds the backlog mechanically
so the breaker self-clears, and the alert (pass 2) makes a genuinely-stuck lane
loud instead of silent. Together they close the self-wedge gap B5's `## Related`
section flags for B8.

## What this plugin does NOT do

- It does NOT reason about precision, classify clusters by content, or open
  CRs/beads — expiry is a pure age-out, not a judgement. (The Retrospect formula
  B4 owns reasoning.)
- It does NOT write the `curio_ledger` directly — it closes beads with an
  accurate `curio-outcome:` label and the trusted B0b reconciler performs the
  ledger write. This keeps the read/write air-gap (`cmd/curio-proposer` and the
  plugins stay read-only against Curio's own write surface).
- It does NOT age out orphaned in-flight Retrospect markers. B5's single-instance
  guard already treats a stale marker as dead (non-wedging), and reaping a dead
  Retrospect polecat is the witness zombie-patrol's job — a parallel reaper here
  would race it. (child-beads §B8 NOTE acknowledged; see the bead's plan note.)

## Configuration

| Env var | Default | Meaning |
|---------|---------|---------|
| `CURIO_PROPOSAL_EXPIRY_DAYS` | `14` | Age (days, by `updated_at`) past which an open proposal/hypothesis is auto-closed. |
| `CURIO_EXPIRY_OUTCOME` | `deferred` | The `curio-outcome:<code>` stamped on an expired bead (`deferred`/`fp`/`dup`/`fixed`). `deferred` is conservative: human inattention is not the rule's fault, so it must not decrement precision. |
| `CURIO_PROPOSAL_CEILING` | `10` | Volume-breaker ceiling — the same value B5 enforces. Open `curio-proposal` count ≥ this = breaker open. |
| `CURIO_BREAKER_ALERT_DAYS` | `3` | Consecutive days the breaker must stay open before the one-shot alert fires. |

## Manual trigger

```bash
gt plugin run curio-proposal-expiry              # Run if gate allows
gt plugin run curio-proposal-expiry --force      # Bypass cron gate
```

## Related

- `curio-retrospect-dispatch` (B5, `gu-5d8os`) — the nightly dispatch whose
  volume breaker this plugin keeps from wedging; runs 30m after this one.
- `mol-curio-retrospect` formula (B4); `curio-proposer --emit-digest` (B1).
- B0b post-close reconciler (`internal/daemon/curio_dog_reconcile.go`,
  `gu-wg7i5`) — the ledger writer this plugin's closes feed.
- Outcome classifier (`internal/curio/outcome.go`) — the `curio-outcome:<code>`
  label this plugin stamps.
- `docs/curio-proposals.md` — proposal-bead taxonomy + labels (B6, `gu-9tmry`).
- Design: `.designs/curio-p3-retrospect-agent/{design-doc.md,child-beads.md §B8}`.

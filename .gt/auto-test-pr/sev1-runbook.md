# Auto-Test-PR SEV-1 Response Runbook

**Audience:** Overseer (and any human-on-call who receives the
"AUTO-TEST-PR SEV-1" nudge).

**Trigger:** `main` CI broke on an opted-in rig **and** the
attributing commit's MR-bead carries the `gt:auto-test-pr` label,
i.e., a polecat-authored auto-test MR landed and broke `main`. Mayor
has already executed the automated SEV-1 chain (D16 in
`.designs/auto-test-pr/synthesis.md`):

1. filed a revert MR via the existing revert-MR formula;
2. CAS-transitioned the rig's state bead to
   `paused-by-circuit-breaker` with a 7-day cooldown;
3. incremented the town-wide circuit-breaker counter on
   `town-auto-test-pr-state`;
4. sent this high-priority SEV-1 nudge to the Overseer.

**Your job is *response*, not detection or revert.** The automation
already protected `main`. This runbook is the post-automation human
loop: confirm, audit, decide, log.

> ⚠️ **Do NOT touch the per-rig state bead manually except via the
> documented `bd update --append-metadata 'incidents=[...]'` command
> in step 5.** Per gu-gal8, the state bead is Mayor-owned and the
> only human-authored writes to it are this `incidents[]` log
> entry. Direct edits to `state`, `transitions[]`, or
> `rejections[]` will be clobbered by Mayor's next CAS-transition.

---

## Step 1 — Confirm the auto-filed revert MR landed and `main` is green

The first thing the automation does is open a revert MR. Verify it
actually merged.

```bash
# 1a. Find the revert MR for the broken rig.
gt auto-test-pr show --rig=<rig> --raw \
  | jq '.transitions[] | select(.context.revert_mr != null)' \
  | tail -1
# Look for: { "context": { "revert_mr": "<rig>-mr-<id>", ... } }

# 1b. Confirm the revert MR is merged.
bd show <rig>-mr-<id>
# Expected: status=closed, label=merged, label=revert-of:<original-mr-bead>

# 1c. Confirm main CI is green on the rig's origin.
#     (Use the rig's normal CI status surface — for gastown_upstream
#     this is the GitHub Actions main-branch view on the upstream
#     remote; for other rigs follow that rig's CI conventions.)
```

**If the revert MR did NOT merge** (still open, blocked, or rejected
by Refinery):
- Do **not** override the circuit breaker yet — `main` is still red.
- Escalate: `gt escalate -s CRITICAL "Auto-test-pr SEV-1: revert MR
  <id> did not land; main still red on <rig>"`. The Mayor or a human
  needs to land the revert before any further response.
- Return to step 1 once the revert is merged.

**If the revert MR landed and `main` is green** → continue to step 2.

---

## Step 2 — Verify the rig's state bead is `paused-by-circuit-breaker` with a 7d cooldown

Confirm the automation actually paused the rig. If it didn't, a
follow-up auto-test MR could fire before you finish investigating.

```bash
gt auto-test-pr show --rig=<rig> --raw | jq '{
  state: .state,
  paused_until: .paused_until,
  last_transition: .transitions[-1]
}'
```

**Expected output** (timestamps will differ):

```json
{
  "state": "paused-by-circuit-breaker",
  "paused_until": "<7d after the SEV-1 trigger>",
  "last_transition": {
    "from": "<previous-state>",
    "to": "paused-by-circuit-breaker",
    "at": "<SEV-1 timestamp>",
    "actor": "mayor",
    "context": {
      "trigger": "ci-break-handler",
      "broken_commit": "<sha>",
      "mr_bead": "<rig>-mr-<id>",
      "revert_mr": "<rig>-mr-<id>"
    }
  }
}
```

**If `state != "paused-by-circuit-breaker"`** (still in
`mr-pending`, `dispatched`, or `cooled-down`):
- The automation's CAS-transition step (b) failed silently. This is
  the failure mode covered by R26 (Mayor cycle path bugs). Do **not**
  proceed with normal step 3+ workflow.
- Escalate: `gt escalate -s CRITICAL "Auto-test-pr SEV-1: rig <rig>
  state is <state>, not paused-by-circuit-breaker after CI break"`.
- Manually pause the rig as a stop-gap so the next tick doesn't fire
  another auto-test MR while the state bead is wrong:
  ```bash
  gt auto-test-pr pause --rig=<rig>
  ```
  This sets `paused_until` on the **town-wide** denormalized cache
  via the documented operator path; it does NOT mutate the per-rig
  state bead's `state` field, but Mayor's tick reads
  `paused_until` before dispatching, so it's a safe belt-and-
  suspenders.

**If `state == "paused-by-circuit-breaker"` and `paused_until` is
~7d in the future** → continue to step 3.

---

## Step 3 — Decide whether to file an investigation bead for the test that broke main

The revert restored `main`, but the *root cause* — a bad test — is
still latent in the auto-test MR's diff history. Decide whether to
file a follow-up bead so the underlying issue is tracked.

**File an investigation bead if any of:**
- The test broke `main` because of a real product bug it was
  surfacing (the test was right; the service regressed). Per the
  test-assertion-changes SOP and R18, this is exactly the case where
  the polecat's test caught something real and the service needs a
  separate fix bead.
- The test introduced flakiness that the gates' N=10 flakiness
  check missed (gate 4c precision/recall question).
- The test broke `main` because of a determinism / time-of-day /
  parallelism interaction that the polecat's sandbox didn't
  reproduce (gate 4a-4f gap).
- The test broke `main` because of a tautology / no-op assertion
  that gate 4d (tautology linter) failed to catch (R27 territory).

**Do NOT file an investigation bead if:**
- The test was correctly red on a real and intentional service
  change that landed concurrently and was the *actual* cause; in
  that case the auto-test PR's revert is the right outcome and the
  service author should land the test update themselves with a
  root-cause writeup per the test-assertion-changes SOP.
- The break was an environmental flake that's already been retried
  and is now green (this should be vanishingly rare since the
  trigger was a CI break attributed to a `gt:auto-test-pr` commit;
  if you're sure it was environmental, document that in step 5's
  `incidents[]` decision).

**To file the investigation bead:**

```bash
bd create \
  --rig <rig> \
  --title "Investigate test that broke main: <one-line summary>" \
  --type=bug \
  --priority=1 \
  --labels="auto-test-pr-investigation,sev1-followup" \
  --description="Test broke main on <date>. Reverted via <revert-mr>.
Original auto-test MR: <mr-bead>.
Broken commit: <sha>.
Test file(s): <path(s)>.
Failure mode (suspected): <flaky | tautology | service-regression-correctly-caught | other>.
Investigation: determine whether to (a) re-introduce with fix, (b)
permanently exclude target, or (c) file a separate service bug if the
test was correctly red."
```

Note the bead ID for step 5.

---

## Step 4 — Decide whether to override the circuit breaker now or wait out the cooldown

The default is **wait out the 7-day cooldown.** The cooldown exists
specifically because the automation can't tell whether the cause was
this auto-test MR specifically or a more systemic problem with the
gate suite, the conventions sheet, or the polecat's test-writing
quality on this rig.

**Override the circuit breaker only if ALL of the following are
true:**
- The investigation in step 3 has determined the failure mode AND
  the failure was **isolated** to that one test (not a class of
  tests the polecat keeps writing — e.g., not "the polecat keeps
  generating tautologies despite gate 4d").
- The investigation bead is filed (so future polecats / patrols can
  see the root cause).
- The next auto-test cycle is high-value and time-sensitive (rare;
  cadence is 7d anyway, so by default waiting costs little).
- An Overseer or town admin is signing off (this is a manual
  override of a safety mechanism).

**To wait out the cooldown** (recommended default):
- Do nothing. Mayor's tick will NOT auto-release a
  `paused-by-circuit-breaker` rig — per D18, only operator action
  via `gt auto-test-pr resume --override-circuit-breaker` can exit
  this state. So the rig stays paused indefinitely until you
  explicitly resume it. After 7d the `paused_until` deadline will
  pass, but state will remain `paused-by-circuit-breaker` until you
  resume.
- After 7d (or whenever you've decided the issue is mitigated), run
  the resume command in the next bullet to return the rig to
  `idle`.

**To override the circuit breaker:**

```bash
gt auto-test-pr resume --rig=<rig> --override-circuit-breaker
# Expected output:
#   ✓ Rig <rig> resumed.
#   ✓ State transitioned: paused-by-circuit-breaker → idle.
#   ✓ Town-wide circuit-breaker counter unchanged (this was a
#     per-rig override, not a town-wide reset).
```

The `--override-circuit-breaker` flag is required: `gt auto-test-pr
resume --rig=<rig>` *without* the override flag will refuse with
"rig is paused-by-circuit-breaker; use --override-circuit-breaker
to acknowledge the SEV-1 path was triggered."

> ⚠️ Override is logged. The transition record on the per-rig state
> bead will name the operator who ran the command (per the
> `approved-by:<user>` write pattern from D15). This is NOT a quiet
> action — every override is part of the rig's audit trail.

---

## Step 5 — Record the decision in the rig's state bead's `incidents[]` log

This is the *one* hand-authored write to the per-rig state bead.
It's the durable record of the human response loop for this SEV-1.

The `incidents[]` field on the per-rig state bead's `Issue.Metadata`
JSON blob holds the audit trail of human SEV-1 responses. Per the
synthesis (`.designs/auto-test-pr/synthesis.md` §Data Model
lifecycle table, **round 3 fix #3**), the field is bounded at ≤20
entries with FIFO eviction, parallel to `transitions[]` (≤50) and
`rejections[]` (≤200).

### `incidents[]` entry schema

```json
{
  "ts": "2026-05-21T16:00:00Z",
  "actor": "<overseer-handle-or-username>",
  "trigger_mr_bead": "<rig>-mr-<id-of-the-auto-test-mr-that-broke-main>",
  "revert_mr_bead": "<rig>-mr-<id-of-the-revert-mr>",
  "investigation_bead": "<rig>-<id-from-step-3>" | null,
  "decision": "wait-cooldown" | "override-resume" | "rig-disabled",
  "decision_rationale": "<one-sentence why>",
  "schema_version": 1
}
```

**Field semantics:**

| Field | Required | Notes |
|-------|----------|-------|
| `ts` | yes | UTC timestamp of the decision (not the original CI break) |
| `actor` | yes | The Overseer / town admin executing the decision (matches `approved-by:<user>` D15 convention) |
| `trigger_mr_bead` | yes | Bead ID of the auto-test MR that broke main; from the SEV-1 nudge payload or step 1's `transitions[]` lookup |
| `revert_mr_bead` | yes | Bead ID of the auto-filed revert MR; from step 1 |
| `investigation_bead` | yes (nullable) | Bead ID from step 3, or `null` if step 3 declined to file one |
| `decision` | yes | One of: `wait-cooldown` (default), `override-resume` (step 4 override), `rig-disabled` (more aggressive — operator ran `gt auto-test-pr disable --rig=<rig>` instead of waiting or overriding) |
| `decision_rationale` | yes | Short prose; readable in `gt auto-test-pr history --rig=<rig>` |
| `schema_version` | yes | Always `1` for v1; future readers tolerate v2 per the synthesis's schema-versioning convention |

### Write the entry

```bash
bd update <rig>-auto-test-state \
  --append-metadata 'incidents=[{
    "ts": "<UTC ISO8601>",
    "actor": "<your-handle>",
    "trigger_mr_bead": "<rig>-mr-<id>",
    "revert_mr_bead": "<rig>-mr-<id>",
    "investigation_bead": "<rig>-<id>" | null,
    "decision": "<wait-cooldown|override-resume|rig-disabled>",
    "decision_rationale": "<short prose>",
    "schema_version": 1
  }]'
```

Note: `--append-metadata` appends to the existing `incidents[]`
array (creating it if absent). It does NOT replace the field. This
mirrors the `bd update --add-label approved-by:<user>` pattern from
D15 / Phase-0 task 10 — both use bead update verbs that don't
require Mayor coordination because they're additive-only and stay
within the human-authorized write surface.

The `gt auto-test-pr show --rig=<rig> --raw` verb is the
**read-only** path for verifying your entry landed correctly. It is
**not** a write path:

```bash
# Verify the new incident landed at the tail of incidents[].
gt auto-test-pr show --rig=<rig> --raw | jq '.incidents[-1]'
```

If the entry is missing or malformed, re-run the `bd update
--append-metadata` command. If the field exceeds 20 entries, the
oldest is FIFO-evicted by Mayor's next state-bead read-modify-write
cycle (this is automatic and does NOT require operator action).

---

## Quick checklist (post-incident summary)

- [ ] Step 1 — Revert MR landed; `main` is green.
- [ ] Step 2 — Per-rig state bead is `paused-by-circuit-breaker`
      with `paused_until` ≈ 7d out.
- [ ] Step 3 — Decided whether to file investigation bead; bead ID
      noted (or `null`).
- [ ] Step 4 — Decided to wait cooldown vs. override-resume vs.
      disable rig.
- [ ] Step 5 — `incidents[]` entry appended to per-rig state bead;
      verified via `gt auto-test-pr show --rig=<rig> --raw`.

If you completed the checklist, the SEV-1 response loop is closed.
The town-wide circuit-breaker counter on `town-auto-test-pr-state`
remains incremented — that's intentional; per the synthesis (§D16
and the risk register R15), repeated SEV-1s town-wide should
ratchet up the global pause threshold, and the counter is the input
to that ratchet.

---

## Related design artifacts

- `.designs/auto-test-pr/synthesis.md` — D16 (SEV-1 path), D18
  (cooldown release), §Data Model lifecycle table (state bead
  schema, **round 3 fix #3** introducing `incidents[]`).
- `.designs/auto-test-pr/data.md` — concrete state-bead JSON
  schema and `transitions[]` / `rejections[]` log conventions
  that `incidents[]` mirrors.
- `.designs/auto-test-pr/integration.md` — Phase-0 task 11
  (SEV-1 auto-revert) integration test that exercises the
  automated chain this runbook responds to.
- `.prd-reviews/auto-test-pr/prd-draft.md` — Q6 SEV-1
  classification ("auto-test PR breaks main CI on any rig:
  revert immediately, pause that rig 7d, notify Overseer").

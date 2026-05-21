# Phase 0a Spike Outcomes — Auto-Test-PR

This file records the one-page summary per Phase 0a verification
spike, per the synthesis Phase 0a exit criteria
(`.designs/auto-test-pr/synthesis.md`, §"Phase 0a exit criteria").

Spikes are filled in as they complete. PASS spikes summarize the
verified behavior; FAIL spikes summarize the gap and the prerequisite
bead that re-shaped the affected Phase 0 task.

---

## 0a-2 — Mayor main-CI-break event subscription (D16 prerequisite)

**Spike bead:** `gu-g3hm3`
**Prerequisite bead filed:** `gu-grkl`
**Affected Phase 0 task re-shaped:** task 11 (`gu-36voy`)
**Outcome:** **FAIL on key acceptance dimension**

### Acceptance criterion (synthesis §0a-2)

> A fixture main-CI-break event triggers a Mayor callback that can
> read the attributing commit's MR-bead.

### Result

The substrate that detects main-CI-breaks exists, but neither half of
the criterion ("Mayor callback" + "can read the attributing commit's
MR-bead") holds against today's code:

1. **There is no "Mayor callback" surface.** `internal/mayor/` is a
   tmux/ACP session manager (start/stop/attach/status). It has no
   programmatic event-subscription, patrol-callback, or escalation-
   consumer surface. `gt mayor` exposes only session-lifecycle
   subcommands.

2. **Detection lives in `daemon`, not Mayor.**
   `internal/daemon/main_branch_test_runner.go::runMainBranchTests`
   runs configured pre-merge gates against `origin/<default_branch>`
   in a temp worktree on a 30-minute ticker. Per-rig opt-in is via
   `MainBranchTestConfig.Rigs []string` (a daemon patrol config rig
   list), not a rig-bead-level `auto_test_pr.enabled` flag. On gate
   failure it emits `gt escalate -s HIGH "main_branch_test: ..."`.

3. **The callback that exists is `failure_classifier_dog`**, also in
   `internal/daemon/`. It reads open `gt:escalation` beads whose
   title prefixes `main_branch_test:`, pattern-matches the
   description against signatures, and files dedup-fingerprinted
   rig beads. This is a callback in the operational sense (daemon
   dog → escalation bead → daemon dog → rig bead).

4. **Attribution to the breaking commit / MR-bead is structurally
   absent from the escalation.** `runMainBranchTests` builds the
   escalation message from `rig name + gate name + failure output
   tail + go-test FAIL-signal lines`. It does NOT capture HEAD SHA
   on `origin/<default_branch>`, the previous-known-good SHA, or
   any MR-bead ID. The classifier therefore cannot read the
   attributing commit's MR-bead — there is nothing in the
   escalation body to read.

### Re-shape applied to Phase 0 task 11 (`gu-36voy`)

Original task 11 was framed as "wire-only on top of Mayor's existing
main-CI-break subscription." That framing is no longer accurate:

- "Mayor's main-CI-break subscription" should be re-stated as
  "a Town-level daemon dog handling main_branch_test escalations."
  The realistic owner is an extension of `failure_classifier_dog`
  or a new sibling daemon dog, NOT the literal Mayor session.
- "Whose attributing commit's MR-bead carries `gt:auto-test-pr`"
  requires substrate work first: `runMainBranchTests` must add
  structured `commit:` and `previous_commit:` lines to the
  escalation body, and a commit-SHA → MR-bead lookup path must
  be available to the consumer.

The substrate work (escalation attribution + a Town-level daemon-dog
consumer for auto-test-pr-labeled MR-beads) is tracked as a
BLOCKING prerequisite in `gu-grkl`. Phase 0 task 11 (`gu-36voy`) now
depends on `gu-grkl` and is re-scoped to land the SEV-1 chain on top
of that substrate.

### Documentation follow-up (non-blocking)

Synthesis §D16 should be updated in a future revision to say "a
Town-level daemon dog subscribes to main_branch_test escalations"
rather than "Mayor subscribes to main-CI-break events." This is a
docs-only fix; the prerequisite bead carries the operational
substance.

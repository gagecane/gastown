# Phase 0a Spike Outcomes

This file records the outcomes of the Phase 0a prerequisite-verification
spikes for the Auto-Test-PR design (synthesis at
`.designs/auto-test-pr/synthesis.md`). Each spike must produce a PASS / FAIL
outcome. A FAIL files a prerequisite bead that re-shapes the affected Phase 0
task before substrate work begins.

---

## 0a-1: Refinery per-MR-bead label query + `approved-by:<user>` semantics

**Bead:** gu-e0hex
**D-ref:** D15 (PRD-align round 1) — auto-test MRs require explicit
maintainer approval before Refinery merges them.
**Phase 0 dependent:** task #10 (wire D15 maintainer-approval gate into
Refinery merge handler).

### Question

Inspect `internal/refinery/` and confirm:

(a) Refinery's merge handler can be conditioned on the presence of a label
    on the MR bead.

(b) `bd update <mr-bead> --add-label approved-by:<user>` is canonical (or
    there is an existing equivalent).

### Method

Static inspection of `internal/refinery/engineer.go` (merge-handler entry
points) and `internal/beads/beads.go` (label API and `bd update` shellout).
No runtime fixture was needed because both mechanisms surface unambiguously
from the source.

### Findings

**(a) Label-conditioned merge handling — PRESENT.**

`Engineer.ListReadyMRs` (`internal/refinery/engineer.go:1705-1769`) is the
gating point that decides which MRs proceed through `doMerge`
(`engineer.go:558`). It already conditions on bead labels: the
`gt:owned-direct` belt-and-suspenders skip at `engineer.go:1733` reads
`beads.HasLabel(issue, "gt:owned-direct")` and excludes the MR from the
ready set when present.

`beads.HasLabel` (`internal/beads/beads.go:309-315`) is a stable helper that
takes an `*Issue` (with `Labels []string`) and returns a bool. It is the
same primitive used throughout the codebase
(`beads_escalation.go:213`, `beads_channel.go:194`, `beads_group.go:199`,
`beads_rig.go:201`, `beads_queue.go:216`).

Therefore the D15 wire-step (Phase 0 task 10) can add a check at the same
filter site of the form:

```go
if beads.HasLabel(issue, "gt:auto-test-pr") &&
   !beads.HasLabel(issue, "approved-by:"+user) {
    // hold: skip this iteration; do not advance to doMerge
    continue
}
```

There is no architectural change required — only the addition of a label
predicate beside the existing `gt:owned-direct` predicate.

**(b) `bd update --add-label approved-by:<user>` — CANONICAL.**

`UpdateOptions.AddLabels []string` is the documented Go-side field
(`internal/beads/beads.go:521-522`) and is shelled out as
`--add-label=<label>` per item in `Beads.Update`
(`internal/beads/beads.go:1850-1852`). Existing callers use this idiom
(e.g. `beads_escalation.go:298` `AddLabels: []string{"acked"}`,
`beads_escalation.go:328` `AddLabels: []string{"resolved"}`,
`beads_escalation.go:492-493` add+remove pair). The `bd update --add-label`
CLI surface is the same path operators use today, so the D15 approval
write — `bd update <mr-bead> --add-label approved-by:<user>` — uses
established primitives end-to-end.

### Acceptance check

> A fixture MR-bead labeled `gt:auto-test-pr` *without* `approved-by:<user>`
> is held by Refinery's merge handler (does not merge).

The "wire" step in Phase 0 task 10 is what makes this fixture pass — but
the prerequisite this spike verifies is that the **mechanism** exists to
implement that wire-step without new infra. Both required primitives —
label-conditioned filtering at the merge-handler entry point, and the
canonical `--add-label` write path — exist today and are exercised by
production code.

### Outcome

**PASS.** No prerequisite bead is required. Phase 0 task 10 (gu-mahth) can
proceed as a wire-only change adding two `beads.HasLabel` calls at
`engineer.go:1733`-area: one for `gt:auto-test-pr` and one for
`approved-by:<user>`, gated behind the
`auto_test_pr.require_review_approval` config flag (default-true per D15).

### Notes / risks deferred to wire-step

- **Username derivation for `approved-by:<user>`.** The wire-step must
  decide what `<user>` resolves to (operator's bd-recorded identity vs.
  rig-config admin list). Out of scope for this spike; tracked under
  task 10.
- **Hold vs. skip semantics.** `ListReadyMRs` currently *skips* a labeled
  MR (it remains in the open-MR set and will be re-evaluated next loop).
  This is the desired hold-until-approved behavior — no separate
  hold-state machine is needed. Confirmed by re-reading
  `engineer.go:1730-1737`.
- **Ordering with `gt:owned-direct`.** The new check should come *after*
  the `gt:owned-direct` skip so that owned-direct MRs are still
  unconditionally excluded (they should not exist for auto-test-pr but
  belt-and-suspenders ordering matches the existing comment at
  `engineer.go:1730`).

---

## 0a-2: Mayor main-CI-break event subscription

_To be completed under bead gu-g3hm3._

## 0a-3: Pinned-bead `Issue.Metadata` reliability

_To be completed under bead gu-g9ufm._

## 0a-4: OQ1 — rig settings JSON vs. `config.json`

_To be completed (bead TBD)._

## 0a-5: Tautology sub-rule (i) precision/recall

_To be completed under bead gu-m57p6._

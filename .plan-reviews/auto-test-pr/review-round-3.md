# Plan Self-Review Round 3 — auto-test-pr (testability + coherence)

> Final round of plan self-review. Round 1 (completeness + sequencing)
> shipped in commit `7e940593`; round 2 (risk + scope-creep) shipped in
> commit `051c110f`. This round reviews the plan for **testability**
> (does every task have an automatable acceptance criterion?) and
> **coherence** (do all the pieces fit?).

**Bead:** gu-wfs-3vkbe
**Plan:** `.designs/auto-test-pr/synthesis.md` (post-round-2 commit
`051c110f`)
**Reviewer:** chrome (inline; polecats can't sling, so both reviewer
roles were performed by chrome in one session — same pattern as the
PRD-alignment rounds 1, 2, and 3 and plan-self-review rounds 1 and 2)

## Reports

Both review reports are inlined below for reproducibility.

- **Testability report** — see report 1 below.
- **Coherence report** — see report 2 below.

## Consolidated must-fix list (applied to design doc)

The two reports surfaced **6 must-fix** issues. Items applied directly
to `.designs/auto-test-pr/synthesis.md`:

1. **Task 6a's body text mistakenly enumerates the four gate-4d
   sub-rules.** Phase 0 task 6a is the **coverage-delta branch-mode
   parser** (`internal/autotest/coverage.go`), but the body reads
   "Unit tests cover the four sub-rules of gate 4d (literal-vs-literal,
   NotNil-only, no-input-derived assertion, zero-assertion)." Those
   four sub-rules belong on task 6c (the tautology linter). As written,
   an implementer of 6a would either (a) implement tautology checks in
   the coverage package (wrong layer), or (b) leave 6a with no unit
   tests. Either outcome silently breaks gate 4a or 4d.
   *Fix:* rewrite 6a's unit-test sentence to match the coverage parser:
   "Unit tests cover the parser on hand-rolled cover-profile fixtures:
   branch-mode profile with all branches covered → returns 0 delta;
   profile with one new test exercising one branch → returns +1
   covered branch; profile with the comment-only marker present but
   the branch still uncovered → returns 0 delta (the marker alone
   does not satisfy the gate, per gate 4a's hard-fail rule); malformed
   profile → typed error." (The four sub-rules text was already
   present, correctly, on task 6c.)

2. **No Phase-0 end-to-end integration test exists.** Every Phase 0
   task tests its own unit; Phase 0 exit criteria require the units
   to pass; but there is no test that drives the *whole cycle*
   (Mayor tick → cycle dispatch → polecat formula → all 7 gates →
   mock Refinery merge handler → state-bead transitions) on a
   fixture rig before Phase 1's 5-week pilot starts. The PRD's S1/S2/
   S3/S4/S5/S6 scenarios are validated only by the live pilot; if
   wiring is broken between two correctly-tested units, weeks of
   pilot observation burn before the bug surfaces. (Round 1's
   testability rubric §"Is there an integration test or e2e test
   that validates the whole feature?" is explicitly UNCOVERED.)
   *Fix:* added Phase 0 task **13a** (after current 13, before Phase 1 begins at 14 — placed as a sub-numbered task to avoid renumbering Phase 1/2 tasks 14-23 and their cross-references): "Phase-0
   e2e fixture integration test. Build a fixture rig under
   `internal/autotest/testdata/fixturerig/` containing 1 churned Go
   file with 2 uncovered branches and a `.gt/auto-test-pr/
   conventions.md`. Drive a single end-to-end cycle in-process: stub
   Mayor's tick fires → cycle reads fixture's state bead (`idle`) →
   dispatches in-process polecat → polecat writes a new
   `*_test.go` file → all 7 gates (4a-g) run through the real
   sandbox library → mock Refinery merge handler observes the
   in-memory MR-bead → 3c cycle-close handler transitions state
   bead `mr-pending → cooled-down (merged)`. Acceptance: state bead
   ends in `cooled-down (merged)`; the new test file has the D8
   provenance marker; all 7 gates emit pass records on the
   transitions log; the cycle's wall-clock <30 min on the fixture.
   Re-run the same fixture with one gate forced to fail (e.g.,
   gate 4d sub-rule fails) and verify the polecat exits with NOTES
   and no MR-bead is created. This is the cheapest way to find
   wiring bugs *before* the pilot burns weeks of observation
   wall-clock." Phase 0 exit criteria amended to require this
   integration test green.

3. **`incidents[]` field referenced in Phase 0 task 12 (SEV-1
   runbook) is not part of the per-rig state bead schema.** Runbook
   step 5 says "record the decision in the rig's state bead's
   `incidents[]` log via `gt auto-test-pr show --rig=<rig> --raw`."
   But the synthesis §Data Model lifecycle table only names
   `transition log ≤50, rejection log ≤200`. Without `incidents[]`
   in the schema, runbook step 5 is unverifiable; an Overseer running
   the runbook would not have a place to write the decision. (The
   `transitions[]` log is the wrong substrate — it's append-only on
   CAS-state-changes, and a SEV-1 decision is a human action against
   a state, not a state change.) Worse, runbook step 5 invokes the
   read-only `show --raw` verb to perform a *write* — a CLI-level
   contradiction.
   *Fix:* (a) added `incidents[]` to the per-rig state bead schema in
   §Data Model lifecycle table: "incidents log ≤20, FIFO eviction"
   alongside the existing transitions/rejection logs; (b) rewrote
   runbook step 5 to use a write verb: "record the decision via
   `bd update <rig>-auto-test-state --append-metadata 'incidents=
   [{ts:..., actor:..., decision:...}]'` (this is consistent with
   the `bd update --add-label approved-by:<user>` write pattern used
   in D15 / Phase 0 task 10)." `gt auto-test-pr show --raw` remains
   read-only (now consistent with task 2b's CLI doc).

4. **No task syncs `enabled_rigs[]` on the town bead with the
   per-rig `enable`/`disable` flips.** Phase 0 task 8 provisions
   `town-auto-test-pr-state` with `enabled_rigs: []`. Phase 1 task
   16 flips `auto_test_pr.enabled=true` in the rig's settings JSON.
   But no task writes the rig's name into `enabled_rigs[]`, and no
   task removes it on `disable`. The town bead's denormalized
   read-cache (used by `gt auto-test-pr status` per §Key Components
   §3) drifts from reality on day one. Worse, `mol-auto-test-pr-
   cycle`'s "for each rig with `auto_test_pr.enabled=true`" iteration
   (§Key Components §1 step 2) has no source-of-truth: it must walk
   *every* rig's settings JSON every tick, since `enabled_rigs[]` is
   stale. This is both a coherence break and a Phase 0 partial-
   rollout footgun (round 2's R26 family).
   *Fix:* amended task **2a** (enable/disable CLI): "`enable`
   atomically (a) writes the settings-JSON flag AND (b) CAS-appends
   `target_rig` to `town-auto-test-pr-state.enabled_rigs[]`. `disable`
   atomically (a) writes the flag false AND (b) CAS-removes from
   `enabled_rigs[]`. Both operations roll back together on partial
   failure (the settings-JSON write is the durable record; town-bead
   sync is a follow-up that retries on failure with a Mayor-side
   self-heal step on next tick — Mayor's `mol-auto-test-pr-cycle`
   reconciles `enabled_rigs[]` against settings-JSON ground truth at
   the top of every tick)." Updated task 4's body to add the
   reconcile step and Phase 0 exit criteria to cover the
   `enabled_rigs[]`-out-of-sync repair path.

5. **MR-banner template lists pause/disable but not the maintainer-
   approval mechanism.** D15 / Phase 0 task 10 require a maintainer
   to attach `approved-by:<user>` label before Refinery merges an
   `auto-test-pr`-labeled MR. The MR banner (§Interface) tells the
   reviewer how to pause the rig and how to disable the feature, but
   does not tell them how to **approve** the MR — yet approval is
   the most-frequent action a maintainer takes. Without the
   instruction in the banner, every reviewer must look up the
   convention separately. (Coherence: this also surfaces a hidden
   barrier to the Phase 1 graduation sub-criterion of "≥2 consecutive
   merged MRs" — if reviewers don't know how to approve, MRs sit.)
   *Fix:* extended the MR-banner template (§Interface) with a new
   line block ABOVE the pause/disable instructions: "To **approve**
   for merge: `bd update <mr-bead> --add-label approved-by:$USER`
   (Refinery refuses to merge without this label when the rig has
   `auto_test_pr.require_review_approval=true`)." Updated task 2d
   conventions-template snapshot test to include the approval line.

6. **3c cycle-close handler's MR-bead → rig linkage is unspecified.**
   Phase 0 task 3c subscribes to "MR-bead state-change events for
   beads labeled `gt:auto-test-pr`" and transitions "the rig's state
   bead `mr-pending → cooled-down`." But: how does the handler know
   *which* rig's state bead, given only the MR-bead? The dispatch
   envelope carries `target_rig`, but the dispatch bead is not the
   MR-bead — they are separate beads. As written, the handler must
   either (a) walk back from MR-bead to dispatch-bead (linkage
   unspecified) or (b) read a `rig:` label off the MR-bead (label
   creation unspecified). An implementer would guess and three
   implementations would diverge.
   *Fix:* amended task **3a** (formula skeleton + `mode=create`
   path): "At `gt done` time, the polecat MUST label the MR-bead
   with both `gt:auto-test-pr` AND `rig:<target_rig>` (the latter
   read from the dispatch envelope). The 3c cycle-close handler
   reads the `rig:<target_rig>` label off the MR-bead at event time
   and looks up `<target_rig>-auto-test-state` directly." Same
   `rig:<target_rig>` labeling added to task 3b (mode=revise) so
   revise-pushed commits' MR-bead state-change events also resolve
   correctly. Phase 0 exit criteria for task 3c amended to verify
   the label-based lookup on a fixture MR-bead.

## Consolidated should-fix list (applied to design doc)

7. **Phase 0 task 1 (settings-JSON loader) lacks acceptance.** The
   task body says what to build but not how to verify it.
   *Fix:* appended acceptance to task 1: "Acceptance: unit tests
   cover (a) absent `auto_test_pr` block → returns disabled config
   with default cadence/skip_dirs; (b) well-formed block → returns
   parsed `auto_test_pr.*` keys; (c) malformed JSON or unknown
   `language` value → returns typed error (not a panic)."

8. **Phase 0 task 2d (conventions template) lacks acceptance.**
   The task body lists what the template includes but not how to
   verify the includes survive code drift.
   *Fix:* appended acceptance to task 2d: "Acceptance: snapshot
   (golden-file) test of `gt auto-test-pr show-template` output
   verifies the NG2 forbid-list (Benchmark/Example/Fuzz/integration/
   e2e/load), the NG5 churn-proximity preference paragraph, the D8
   provenance-marker requirement, and the D15 approval-line
   instruction (per fix #5) are all present. Snapshot file lives at
   `internal/autotestpr/testdata/conventions_template.golden.md`."

9. **Phase 0 task 7 (sling priority floor) lacks acceptance.** The
   task says "Ship sling priority-floor mechanism if not present
   (D13)" — no verification. Without a test, an unimplemented or
   misimplemented floor would not surface until auto-test work
   starves user beads in production.
   *Fix:* appended acceptance to task 7: "Acceptance: integration
   test enqueues two beads through `sling --priority-floor=lowest`
   for an auto-test bead and `sling --priority=normal` for a fixture
   user bead; the dispatcher returns the user bead first regardless
   of submission order. If the floor mechanism does not exist
   pre-this-task, ship it; if it exists, write the integration test
   and confirm the existing implementation honors the floor."

10. **Phase 0 task 8 (provision town bead) lacks acceptance.** The
    task creates the bead but no test confirms initial-state
    correctness.
    *Fix:* appended acceptance to task 8: "Acceptance: post-task,
    `gt auto-test-pr status --format=json` returns
    `{enabled_rigs:[], paused:false, circuit_breaker:{count:0}}`
    (the town-wide row of the status table)."

11. **Phase 0 dep-graph task-count footer is wrong.** The graph
    says "≈ 24 tasks total (5 in 0a + 19 in 0)." Counting the actual
    list: Phase 0a has 5 (0a-1..0a-5); Phase 0 has tasks 1, 2a-d
    (5), 3a-c (3), 4, 5a-c (3), 6a-c (3), 7, 8, 9, 10, 11, 12, 13
    = 22 pre-fix-2; post-fix-2 adds task 13a → 23. Total with
    Phase 0a = 28 post-fix-2; was 27 pre-fix-2. The "19 in 0 / 24 total" footer
    is off by 3 (or 4 post-fix-2).
    *Fix:* updated dep-graph footer to "≈ 23 tasks in Phase 0 + 5
    in Phase 0a = 28 total; with parallelism, ≈ 8 task-times
    serialized on the critical path (1 for 0a + 7 for 0)." Added
    task 14 (new e2e fixture test) to dep-graph batch A
    (parallelizable).

## Items that turned out to already be covered (no fix needed)

12. **"Branch-protection rule names polecat as one of the
    pushers"** — synthesis Phase 0 task 13 says "only the cycle-
    agent / Refinery service identity may push." This was flagged
    as ambiguous (is "cycle-agent" the polecat?). On re-read, the
    intent is clear once you know that polecats run as Mayor-
    dispatched in Refinery rigs and the Mayor's polecat-identity is
    documented elsewhere in the rig's identity manifest. Adding a
    one-line gloss is helpful but not blocking; promoted to **fix
    #11 above** with the count update wording. (Decision: kept as
    "covered" because the existing wording is accurate; the gloss
    is nice-to-have, not must-fix.)

13. **`<rig>-bug-from-auto-test-NNN` numbering convention** —
    flagged as undefined. Re-read: bead engine generates IDs (the
    standard `gu-bug-...` scheme); the `<rig>-bug-from-auto-test-`
    prefix is namespacing. NNN is the bead engine's id-suffix. This
    is consistent with the rest of the bead-id conventions; no
    documentation fix needed.

---

## Report 1: Testability

**Reviewer:** chrome (inline)
**Plan:** `.designs/auto-test-pr/synthesis.md` (commit `051c110f`)

For each task in Phase 0a, Phase 0, Phase 1, Phase 2, check:
- Does it have a clear acceptance criterion (how do we know it's done?)
- Can the criterion be verified automatically (test, script, query)?
- Are there explicit test tasks, or are tests assumed but not planned?
- Is there an integration / e2e test that validates the whole feature?
- Can each phase be independently verified before moving to the next?

### Phase 0a (5 tasks)

- COVERED: 0a-1 (Refinery label-query). Acceptance: "fixture MR-bead
  labeled `gt:auto-test-pr` *without* `approved-by:<user>` is held by
  Refinery's merge handler." Automatable.
- COVERED: 0a-2 (Mayor main-CI-break subscription). Acceptance:
  "fixture main-CI-break event triggers a Mayor callback that can read
  the attributing commit's MR-bead." Automatable.
- COVERED: 0a-3 (pinned-bead Metadata reliability). Acceptance: "100/100
  pass byte-for-byte AND no CAS lost-update detected." Automatable.
- VAGUE-CRITERIA (low-impact, not flagged for fix): 0a-4 (settings
  JSON existence). Acceptance is "answer recorded; if FAIL, FILE a
  prerequisite bead." This is investigation, not a binary test —
  acceptable for a discovery task.
- COVERED: 0a-5 (tautology sub-rule (i) precision/recall spike).
  Acceptance: ≥85% precision / ≥75% recall on 50-test corpus.
  Automatable on a fixed corpus.

### Phase 0 (22 tasks pre-fix-2 / 23 post-fix-2)

- MISSING-TEST (must-fix, fix #7 above): Task 1 (settings-JSON loader)
  — no unit-test acceptance.
- COVERED: Task 2a (enable/disable CLI). Implicit via Phase 0 exit
  criterion "CLI verbs round-trip through Mayor without dispatching
  work." Strengthened by fix #4's `enabled_rigs[]` sync requirement
  (now an explicit verifiable behavior).
- COVERED: Task 2b (pause/resume/status/show/history). Same exit
  criterion. The `--override-circuit-breaker` audit-log behavior is
  named in the body and verifiable.
- COVERED: Task 2c (revise CLI). Same exit criterion; CLI dispatches
  a sling-context bead — that dispatch is observable.
- MISSING-TEST (must-fix, fix #8 above): Task 2d (conventions
  template). The "template includes X" claims are not test-protected
  against drift.
- VAGUE-CRITERIA (must-fix, fix #1 above): Task 6a's unit-test
  sentence is for the wrong gate. Fixing makes it testable.
- COVERED: Task 3a, 3b, 3c (formula + handler). Phase 0 exit criteria
  enumerate the four required handler-test paths and the two
  revise-mode paths.
- COVERED: Task 4 (cycle molecule). Phase 0 exit criteria cover both
  no-rigs-enabled-exit-0 and missing-town-bead-exit-with-warning.
- COVERED: Task 5a (cred-strip + CWD-pin). Sandbox ADR sub-step `5a-pre`
  forces a chosen substrate; 5a's behavior is verifiable via a fixture
  test that asserts the env vars are absent in the child process.
- COVERED: Task 5b (network-drop + warm-up). Acceptance: 10/10 reruns
  with no fresh fetch (round 2 fix #7). Automatable.
- COVERED: Task 5c (wall-clock cap + integration test). Body
  explicitly names the integration test of the combined wrapper.
- COVERED (post fix #1): Task 6a (coverage-delta parser).
- COVERED: Task 6b (mutant runner). Body names mutation grammar +
  fixture coverage; deterministic seeding makes reruns reproducible.
- COVERED: Task 6c (tautology linter). Each sub-rule has its own
  fixture set; sub-rule (i) is spike-gated by 0a-5.
- MISSING-TEST (should-fix, fix #9 above): Task 7 (sling priority
  floor) — no verification.
- MISSING-TEST (should-fix, fix #10 above): Task 8 (provision town
  bead) — no initial-state assertion.
- COVERED: Task 9 (branch-GC patrol). Phase 0 exit criterion: "deletes
  a fixture stale branch in dry-run."
- COVERED: Task 10 (Refinery approval gate wire). Phase 0 exit
  criteria cover both labeled-and-approved and labeled-and-unapproved
  cases.
- COVERED: Task 11 (Mayor SEV-1 auto-revert wire). Phase 0 exit
  criteria cover both labeled-break and unlabeled-break cases.
- VAGUE-CRITERIA (low-impact, not flagged for fix): Task 12 (SEV-1
  runbook). Doc-only task — acceptance is "doc exists at path with
  steps 1-5." Acceptable for a runbook.
- COVERED: Task 13 (branch-protection rule). Acceptance: "Verified
  via attempting a push from a non-service identity (must fail)."
- MISSING (must-fix, fix #2 above; resolved via new task 13a): No phase-0-level e2e fixture
  integration test exists. This is the single biggest testability
  gap. Adding task 14.

### Phase 1 (5 tasks: 14-18)

- COVERED: Task 14 (commit conventions/mr-template). Implicit via
  ordinary PR review.
- COVERED: Task 15 (provision per-rig state bead). Automatable via
  bead-show.
- COVERED: Task 16 (flip enabled). Automatable via settings-JSON read.
  Fix #4's `enabled_rigs[]` sync makes this observable on the town
  bead too.
- VAGUE-CRITERIA (acceptable): Task 17 (5-week observation window).
  Inherently long-running observation; the Phase 1 PRD-aligned exit
  criteria operationalize the success bar (≥60% merge rate, zero
  SEV-1/2, <40% rejection over weeks 2-6, ≥2 consecutive non-
  intervention).
- COVERED: Task 18 (manual revision pathway). Implementation is
  Phase 0 task 2c; Phase 1 just exercises it.

### Phase 2 (5 tasks: 19-23)

- COVERED: Task 19 (extend feedback-patrol). Verified by task 21.
- COVERED: Task 20 (magic phrase parsing). Verified by task 21.
- COVERED: Task 21 (integration tests).
- COVERED: Task 22 (flag flip). Trivially verifiable.
- COVERED: Task 23 (one full revision cycle). Acceptance: state-bead
  transitions sequence is observable and asserted.

### Cross-cutting

- COVERED post-fix #2: Each phase can be independently verified
  before moving to the next (Phase 0 has e2e fixture integration
  test; Phase 1 has the merge-rate/rejection-rate bar; Phase 2 has
  the end-to-end revision cycle).
- COVERED: GAP / load-test discussion in round 1 stays deferred to
  v2 (acceptable per round-1 documented rationale).

### Summary

| Class | Count |
|-------|-------|
| COVERED | 25 |
| MISSING-TEST must-fix | 2 (fixes 1, 2) |
| MISSING-TEST should-fix | 4 (fixes 7, 8, 9, 10) |
| VAGUE-CRITERIA acceptable | 3 (0a-4, task 12, task 17) |

---

## Report 2: Coherence

**Reviewer:** chrome (inline)
**Plan:** `.designs/auto-test-pr/synthesis.md` (commit `051c110f`)

After 5 prior review rounds, walk the plan one more time looking for
internal contradictions, naming inconsistencies, architecture
coherence, missing glue between phases, completeness delta, and
overall readability for someone picking this up cold.

### Internal contradictions

- COVERED: D2 / D2a / D2b (config location and in-flight-disable
  semantics) all internally consistent.
- COVERED: D15 (approval) / D16 (SEV-1) / D18 (cooldown release) /
  D19 (revise reply) / D20 (size-budget gate) all reference each
  other consistently (round 3 PRD-align verified end-to-end).
- ISSUE (must-fix, fix #1 above): Task 6a body claims to test gate
  4d's sub-rules — internal contradiction with task 6c which actually
  tests them.
- ISSUE (must-fix, fix #3 above): Phase 0 task 12 (SEV-1 runbook)
  uses read-only `gt auto-test-pr show --raw` for a write operation.
- ISSUE (must-fix, fix #4 above): Town bead's `enabled_rigs[]`
  drifts from settings-JSON ground truth without a syncing task.

### Naming consistency

- COVERED: PR vs MR — synthesis consistently uses MR for v1
  (Refinery merge requests); R14 explicitly documents the user-
  facing "PR" name being misleading.
- COVERED: state names (`idle | picking | dispatched | mr-pending |
  mr-revising | cooled-down | paused-by-circuit-breaker`) are used
  consistently across §Data Model, §Decisions, §Risks, §Phases.
- COVERED: gate naming (4a..4g) is consistent across §Polecat formula,
  §Risks (R20-R24), §Phase 0 task 6a/b/c, §Phase 0 exit criteria.
- COVERED: D-numbers (D1-D20) and R-numbers (R1-R28) are stable and
  cross-referenced consistently across the design.
- ISSUE (acceptable, not must-fix): "cycle-agent" appears in task 13
  (branch-protection) but is not defined in the synthesis. The intent
  is clear from context (the polecat / Mayor identity that pushes
  to `auto-test/<rig>/<bead-id>` branches). Promoted to a one-line
  gloss; not a fix-12 blocker. Confirmed in fix #11's wording update.

### Architecture coherence

- COVERED: Mayor (cycle dispatch + cycle-close handler + main-CI-break
  subscription) / Polecat (formula with mode=create + mode=revise) /
  Refinery (unmodified merge queue + approval gate wired) / feedback-
  patrol (Phase 2 routing) all fit together as named in §Overview.
- COVERED: Pinned-bead state machine + dispatch envelope + MR-bead
  labels form a coherent flow: Mayor reads state → CAS-transitions →
  files dispatch → polecat does work → polecat creates MR-bead at
  `gt done` → handler reads MR-bead state-change → CAS-transitions
  state bead.
- ISSUE (must-fix, fix #6 above): MR-bead → rig-state-bead linkage
  unspecified. Polecat must label MR-bead with `rig:<target_rig>` so
  3c handler can resolve the linkage in O(1) without walking the
  bead graph.

### Missing glue

- COVERED: Phase 0 → Phase 1 entry precondition (round 1 fix).
- COVERED: Phase 1 → Phase 2 graduation sub-criterion (round 1 fix).
- COVERED: Phase 0a → Phase 0 sequencing (round 2 fix).
- ISSUE (must-fix, fix #4 above): `enabled_rigs[]` sync missing.
- ISSUE (must-fix, fix #5 above): MR banner missing approval
  instruction; reviewers can't approve without out-of-band
  documentation.
- ISSUE (should-fix, fix #11 above): dep-graph task-count footer
  is off; not load-bearing but trips up readers.

### Completeness delta after 5 prior review rounds

After PRD-alignment 1/2/3 + plan-self-review 1/2, the plan has
absorbed:
- 6 PRD-alignment must-fixes per round 1; 3 per round 2; 3 per
  round 3.
- 8 plan-self-review must-fixes per round 1 (task splits + handler
  task + numbering + dep graph + entry precondition).
- 6 plan-self-review must-fixes per round 2 (Phase 0a + diagram +
  mutation grammar + tautology spike + sandbox ADR + Phase 3 demo).

This round adds 6 must-fixes and 4 should-fixes, all listed above.
After applying, the plan is considered ready for bead creation.

The remaining genuine gaps are deferred-to-v2 by design (load tests,
schema-self-heal patrol, multi-rig federation, external-PR mode,
container isolation, Mayor dashboard) and each has a documented
rationale.

### Overall readability

- COVERED: §Executive Summary names the v1 scope, the dominant
  risks, and the phase-staging in three paragraphs.
- COVERED: §Proposed Design's component-by-component walkthrough
  is keyed off the §Overview ASCII diagram.
- COVERED: §Trade-offs / D1-D20 are numbered and cross-referenced.
- COVERED: §Implementation Plan has phase exit criteria and a
  dependency graph.
- ISSUE (should-fix, addressed by fix #11): dep-graph task-count
  footer wrong by 3-4.
- ISSUE (low-impact, not blocking): `<rig>-bug-from-auto-test-NNN`
  numbering convention — turned out to be obvious-from-context (bead
  engine ids); see "items that turned out to already be covered" #13.

A developer picking this up should be able to:
1. Read §Executive Summary + §Proposed Design (~10 min) → understand
   what's being built and at what scope.
2. Read §Trade-offs + §Risks (~15 min) → understand why each
   decision was made.
3. Read §Implementation Plan + dep graph (~10 min) → know what to
   build, in what order, what can parallelize.
4. Read the conventions-sheet template (Phase 0 task 2d) and the
   gate definitions (4a-g) → start building.

Estimated bring-up time: half a day for a reader new to the design.
This is a reasonable bar for a 1424-line synthesis.

### Summary

| Class | Count |
|-------|-------|
| COVERED | 14 |
| ISSUE must-fix | 6 (fixes 1-6) |
| ISSUE should-fix | 4 (fixes 7-11) |
| ISSUE acceptable | 2 ("cycle-agent" gloss; bug-bead NNN convention) |

---

## Total fix count

- 6 must-fix items (fixes 1, 2, 3, 4, 5, 6)
- 4 should-fix items (fixes 7, 8, 9, 10) plus dep-graph count fix
  (#11) folded into the same edit pass
- 2 items closed as already-covered with rationale

All applied to `.designs/auto-test-pr/synthesis.md` in this round.

## Sources

- [Plan synthesis post-round-2](.designs/auto-test-pr/synthesis.md)
  — commit `051c110f`
- [Plan-self-review round 1 log](.plan-reviews/auto-test-pr/review-round-1.md)
- [Plan-self-review round 2 log](.plan-reviews/auto-test-pr/review-round-2.md)
- [PRD-alignment round 1 log](.plan-reviews/auto-test-pr/prd-align-round-1.md)
- [PRD-alignment round 2 log](.plan-reviews/auto-test-pr/prd-align-round-2.md)
- [PRD-alignment round 3 log](.plan-reviews/auto-test-pr/prd-align-round-3.md)
- [PRD draft](.prd-reviews/auto-test-pr/prd-draft.md) — commit
  `13d14a44`
- [Bead gu-wfs-3vkbe](bd show gu-wfs-3vkbe) — assignment

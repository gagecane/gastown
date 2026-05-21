# Plan Self-Review Round 2 — auto-test-pr (risk + scope-creep)

> Round 2 of plan self-review. Round 1 (completeness + sequencing) shipped
> in commit `7e940593`. This round reviews the plan against itself for
> **execution risk** (what could go wrong during build?) and
> **scope creep** (what doesn't belong in v1?).

**Bead:** gu-wfs-ny25u
**Plan:** `.designs/auto-test-pr/synthesis.md` (post-round-1 commit
`7e940593`)
**Reviewer:** chrome (inline; polecats can't sling, so both reviewer
roles were performed by chrome in one session — same pattern as the
PRD-alignment rounds 1, 2, and 3 and plan-self-review round 1)

## Reports

Both review reports are inlined below for reproducibility.

- **Risk report** — see report 1 below.
- **Scope-creep report** — see report 2 below.

## Consolidated must-fix list (applied to design doc)

The two reports surfaced **6 must-fix** issues. Items applied directly
to `.designs/auto-test-pr/synthesis.md`:

1. **`paused-by-circuit-breaker` state is referenced eight times in
   the body of the design (D16, D18, R15, R22, Q6, gate-4e SEV-2,
   `gt auto-test-pr resume --override-circuit-breaker` flag, Phase 0
   exit criteria) but is missing from the §Data Model state-machine
   diagram.** The diagram shows only `idle → picking → dispatched →
   mr-pending → cooled-down → idle` with the `mr-revising` revision
   loop. A reader following the diagram would think the state machine
   is six states, not seven; the SEV-1 path's terminal-ish state is
   undocumented at the only place the state machine is drawn.
   *Fix:* extend the §Data Model state-machine diagram to include
   `paused-by-circuit-breaker` with its inbound edge from any state
   on SEV-1 / 3-closes-in-7d trip and its only outbound edge being
   explicit operator action via `gt auto-test-pr resume
   --override-circuit-breaker` (no auto-release; D18 explicitly excludes
   it from cadence-elapsed transitions). Also annotate the diagram with
   the four CAS-transition triggers (cadence-elapsed, mayor-dispatch,
   merge-handler, ci-break-handler) so the reader can see which actor
   drives each edge.

2. **Refinery per-MR-bead label query and `approved-by:<user>` semantics
   verification (Phase 0 task 10a) is in the *middle* of Phase 0's
   serial chain.** Round 1 task 10 split into "(a) verify ... if no,
   FILE a prerequisite bead and DEFER this task; Phase 0 cannot complete
   without it. (b) wire ..." This is structured correctly for the
   *outcome* (DEFER if missing) but wrong for *scheduling*: task 10's
   verify-step depends only on knowing what's in Refinery, NOT on tasks
   1-9 of Phase 0. Yet because task 10 is numbered after task 9 in a
   nominally serial Phase 0, the verification doesn't run until Phase 0
   is mostly built. If Refinery doesn't support label query, ~2 weeks
   of Phase 0 work has been spent before discovering Phase 0 cannot
   complete. Same risk for task 11a (Mayor main-CI-break subscription
   verification).
   *Fix:* introduce a new **Phase 0a (prerequisite verification +
   spikes)** that runs to completion *before* Phase 0 proper begins.
   Phase 0a contains: (1) the verify-step from task 10a (Refinery
   label query + `approved-by` semantics); (2) the verify-step from
   task 11a (Mayor main-CI-break subscription); (3) OQ1 settings-JSON
   existence answer; (4) OQ4 pinned-bead Metadata reliability spike
   (round-trip a 5KB JSON blob through `Issue.Metadata` and verify
   read-after-write semantics). Each of these is independently fast
   (hours, not days) and any FAIL outcome reshapes Phase 0 (FILE a
   prerequisite bead and re-plan). Phase 0 task numbering shifts: the
   former 10b becomes new task 10 (wire-only); 11b becomes new task 11
   (wire-only); the verify sub-steps move into Phase 0a as tasks
   0a-1, 0a-2, 0a-3, 0a-4.

3. **Mutant runner's mutation-selection algorithm is unspecified.**
   Phase 0 task 6b says "AST-aware mutant runner. Bounded to ≤5 mutants
   per test (D11); copies package directory to `os.MkdirTemp` (D6); runs
   through sandbox (depends on 5c)." But: *which* lines does it mutate?
   D11 caps the count, not the selection. If the runner mutates random
   lines, most mutations will be on test-irrelevant code and pass
   trivially. If it mutates the lines named in the test's coverage
   profile, that's the right behavior but it's an entire algorithmic
   commitment not in the plan. As written, an implementer could ship
   any of: (a) random mutation in package, (b) mutation only of lines
   the test covers, (c) mutation only of lines the test covers AND a
   member of an enumerated mutation grammar (negate-condition,
   bound-shift, return-zero) — all of which produce wildly different
   gate-4b false-positive rates.
   *Fix:* add to task 6b an explicit sub-spec: "Mutation selection: the
   runner mutates *only* lines marked covered-by-the-test in the test's
   own coverage profile, drawn from a fixed mutation grammar of (i)
   comment-out-line (the gate's literal D6 spec), (ii) negate-boolean
   (flip `!` / swap `==` ↔ `!=`), (iii) return-zero-value (replace the
   first `return` in the function with the type's zero value). Selection
   is deterministic given the file SHA + test name (seedable) so reruns
   are reproducible. ≤5 mutants per test is enforced over the *union*
   of (i)/(ii)/(iii); if more than 5 mutation candidates exist, the
   runner picks the 5 with greatest expected blast radius (lines with
   most coverage hits across the test suite) so a passing mutant is
   maximally likely to indicate a tautological test. Unit tests cover
   each grammar form on a hand-rolled fixture."

4. **Tautology-linter sub-rule (i) ("≥1 assertion must depend on the
   function-under-test's return value or observable side effect")
   requires non-trivial Go AST data-flow analysis that the plan never
   tasks as a spike or feasibility check.** Sub-rules (ii)
   (literal-vs-literal), (iii) (NotNil-only), and (iv) (zero-assertion)
   are all syntactic and trivially decidable. Sub-rule (i) requires
   tracing whether *any* expression in *any* assertion's argument list
   is data-dependent on a value returned from a call to the
   function-under-test. This is a small but non-trivial flow-sensitive
   analysis. If the implementation is naive ("assertion arguments
   contain an identifier whose declaration is on a line containing the
   function-under-test's name"), false-positive and false-negative rates
   could exceed 30% on real test code (e.g., the assertion uses a
   helper-derived value, or aliases a returned struct field through
   multiple lets). At a 30% false-negative rate, the gate provides
   little protection.
   *Fix:* (a) demote sub-rule (i) to a **spike-then-decide** item under
   Phase 0a (new task 0a-5): build a 50-test corpus of known-tautological
   and known-good test functions, run the candidate analysis, measure
   precision/recall. Acceptance threshold: ≥85% precision (false-positive
   rate ≤15% on known-good tests) AND ≥75% recall (false-negative rate
   ≤25% on known-tautological tests). If the threshold is not met, sub-
   rule (i) is **omitted from gate 4d** and the gate ships with the
   three syntactic sub-rules only. (b) Update gate 4d's description in
   §Key Components and §Phase 0 task 6c to mark sub-rule (i) as
   "spike-gated; ships if Phase 0a-5 acceptance criteria met, otherwise
   omitted with rationale recorded in conventions sheet." (c) Update
   risk register with the spike outcome dependency.

5. **The `gt sandbox or equivalent` phrasing in Phase 0 task 5a leaves
   the wrapper's *implementation strategy* uncommitted.** "Helper or
   equivalent" admits at least three implementations: (i) a wrapper
   binary `gt sandbox` that takes a command + args and execs it under
   the configured restrictions; (ii) a Go library that the polecat
   formula calls in-process; (iii) ad-hoc per-gate sandbox setup duped
   into each gate's code path. These have very different testability,
   reuse, and review surfaces. As written, three different polecats
   could split tasks 5a, 5b, 5c and each pick a different
   implementation. The risk: by the time an integration test (5c)
   runs, the three pieces don't compose because they target different
   substrates.
   *Fix:* add an architecture decision record (ADR) sub-step at the
   *front* of task 5a: "5a-pre: Decide and document whether the sandbox
   is (a) a wrapper command (`gt sandbox <cmd>...`), (b) a library
   (`internal/autotest/sandbox`), or (c) inline. Recommended: (b) — a
   library used by both the polecat formula and the gate runners,
   because it composes with `os/exec.Cmd` and avoids spawning a child
   process per gate. ADR is a one-page note in the rig's design
   notes (committed alongside `internal/autotest/sandbox/doc.go`). 5a,
   5b, 5c all consume the ADR's chosen substrate; deviation requires
   ADR amendment first."

6. **Phase 3 ("Generalization — out of scope but the design must not
   preclude it") lists numbered tasks 24-27.** Round 1's renumber put
   Phase 3 at 24-27. Numbered tasks read as commitments — a future
   reader may interpret them as "these are queued for delivery." The
   v1 PRD explicitly defers everything in Phase 3 (second-rig opt-in,
   external-PR mode, GitHub App, `gh pr create` tap-guard amendment).
   Numbered tasks in a deferred-by-design section blur the contract.
   *Fix:* rewrite Phase 3 as **narrative bullets** (no task numbers)
   prefaced with "v2 / v3 follow-on work, captured here for design
   continuity only — not committed in v1." Renumber away the 24-27.
   The implementation-plan task ledger ends at task 23 (Phase 2's last
   integration step).

## Consolidated should-fix list (applied to design doc)

7. **5b network-drop + `go test -count=10` interaction needs an
   explicit exit criterion.** Security-leg open question 1 is named in
   the design ("verify `go test -count=10` does not trigger a fresh
   fetch") and Phase 0 task 5b says "depends on module-cache warm-up,"
   but the exit criterion for task 5b is implicit. A polecat shipping
   5b could mark it done after `go mod download` runs without verifying
   that 10 sequential `go test` runs succeed under post-warm-up network
   drop.
   *Fix:* add to Phase 0 exit criteria (and to task 5b's body): "5b's
   acceptance: a fixture package on which `go mod download && drop-net
   && go test -count=10 ./...` succeeds 10/10 times with no fresh
   network fetch (verified by tcpdump or `strace` on connect()). If
   even one rerun triggers a fetch (e.g., a test imports a transitively-
   missing package), the warm-up step is amended to also run
   `go test -count=1 -run='^$' ./...` (a no-op test pass that triggers
   the same package compile graph as the test execution does)."

8. **Pinned-bead `Issue.Metadata` reliability (OQ4) is open but Phase 0
   tasks 8 and 14 (per-rig and town pinned-bead provisioning) depend on
   it being safe to round-trip ~5KB JSON blobs.** Round 1 left this as
   an open question to resolve "before Phase 0." But OQ1 is explicitly
   gated to Phase 0 entry; OQ4 is not. If OQ4 turns out negative
   (Metadata is not durable for 5KB blobs), the data-model design's
   fallback ("metadata-bead-attachment per rig") is materially different
   work — separate beads, separate CAS semantics.
   *Fix:* promote OQ4 to a **Phase 0a spike** (new task 0a-3, see fix #2):
   write a synthetic 5KB blob to a test bead's `Issue.Metadata`, read
   back, verify byte-for-byte. Run 100 round-trips concurrently to
   stress CAS isolation. Acceptance: 100/100 pass byte-for-byte AND no
   CAS lost-update detected. If FAIL, FILE a prerequisite bead for the
   metadata-attachment-bead fallback and re-shape Phase 0 task 8 + 14
   accordingly.

9. **Knowledge-risk: no named expert / SOP for the new Go AST work in
   tasks 6b (mutant runner) and 6c (tautology linter).** The plan
   assumes an arbitrary polecat will ship `internal/autotest/{mutant,
   tautology}.go`. AST work has well-known footguns (positions vs.
   line/col, comment handling, generic type parameters in 1.18+,
   build-tag-dependent files). A polecat with no Go AST background may
   re-derive these the hard way and miss edge cases that show up only
   in production code.
   *Fix:* add a **knowledge-prep** sub-step to task 6b and 6c each:
   "Before implementation, the assigned polecat MUST read
   `golang.org/x/tools/go/ast/astutil` package docs and at least one
   real-world AST tool (`go vet`, `staticcheck`, or `errcheck`) to
   absorb conventions for position-handling, comment-handling, and
   build-tag exclusion. Implementation MUST NOT shell out to `gofmt`
   or `goimports` for AST traversal — use `go/parser` +
   `go/ast` directly so the analysis is robust against unparseable
   input." Add risk-register row R25 (AST footgun → silent
   false-negative on mutation/tautology gates).

10. **Phase 0 task 4 (`mol-auto-test-pr-cycle` formula) has no test
    for the missing-town-bead path.** Task 4 says the cycle "first step
    is `if no rig has auto_test_pr.enabled == true → exit 0`" — but
    this presumes the town bead exists. If task 8 (town bead
    provisioning) is reverted (Phase 0 partial revert per §Reverting),
    the cycle's first step would need to read a non-existent bead and
    panic / error out rather than silently exiting 0. Round 1 added
    "all formulas parse" to the exit criteria but didn't task a
    missing-bead integration test.
    *Fix:* add a Phase 0 exit-criteria sub-bullet: "`mol-auto-test-pr-
    cycle` integration test covers the missing-town-bead path (cycle
    exits with a structured warning, not a panic) AND the
    no-rigs-enabled path (cycle exits 0)." Add to risk register R26
    (formula panics on partial Phase 0 revert).

11. **Conventions-sheet template's OQ7 TALON-style comment exception
    is gold-plating for v1 single-rig pilot.** The pilot rig is
    `gastown_upstream`, which is not a TALON-convention codebase. Round
    1 task 2d says the template "includes ... the OQ7 TALON-style
    comment exception ... and placeholders for rig-specific test
    conventions." The exception serves a hypothetical future TALON-
    convention rig that doesn't exist in v1's scope (which is single
    rig). Including it in the template adds template surface area for
    no v1 benefit and creates a reader-confusion risk: a `gastown_
    upstream` maintainer reading the emitted template wonders why
    "TALON-style codebases" are being mentioned at all.
    *Fix:* remove the OQ7 TALON-comment-exception language from the
    Phase 0 task 2d template description. OQ7 itself stays in the
    Open Questions list (it's accurate that future TALON-style rigs
    will need this exception when they opt in). Add a `v2 follow-up`
    note to OQ7: "When a TALON-convention rig opts in for the first
    time, amend the conventions template to include the marker
    exception." This follows the round-1 pattern of keeping concerns
    visible in OQ-list while not building hypothetical future
    accommodation into v1.

## Risks added to the risk register

- **R25** AST footgun in tasks 6b/6c → silent false-negative on
  mutation/tautology gates. Mitigation: knowledge-prep sub-step (fix #9).
- **R26** `mol-auto-test-pr-cycle` panics on partial Phase 0 revert
  (town bead absent). Mitigation: missing-town-bead integration test
  (fix #10).
- **R27** Tautology sub-rule (i) precision/recall below threshold →
  gate ships with three syntactic sub-rules only. Mitigation: Phase 0a-5
  spike with ≥85% precision / ≥75% recall acceptance gate (fix #4).

## Items considered and rejected (not applied — keeping for audit)

These were surfaced by the scope-creep walk-through but I judged each
to be net-positive after analysis. Recording the reasoning so a future
review doesn't re-litigate.

- **Considered cutting D17 manual `gt auto-test-pr revise` CLI.** The
  case for cutting: Phase 2 lands automated routing; manual CLI is
  redundant. Rejected: D17 is the documented G4 fallback that prevents
  G4 from being unreachable during the 5-week Phase 1 observation
  window. Without it, a maintainer who comments on a Phase 1 MR has no
  way to trigger revision until Phase 2 ships. Round 1 promoted this
  to a documented MR-banner path; cutting it would re-open the G4 hole.
- **Considered simplifying D19 reply step to "polecat appends a single
  comment to the MR" rather than threading replies on each
  `args.revision.comments[]`.** Rejected: PRD S3 explicitly requires
  "the comment thread is replied to" (singular thread). A single MR-
  level comment doesn't satisfy the thread semantics — a reviewer who
  left three comments expects three thread replies. The PRD bar is the
  PRD bar.
- **Considered deferring gate 4g (size-budget enforcer) to v2.**
  Rejected: D20 made gate 4g structural rather than model-judgment
  precisely because the alternative is "the dispatch envelope tells the
  polecat the budget and we hope it complies." A dispatched-but-
  unverified budget is a paper protection. The gate is ~30 LOC
  (file-count + LOC-count from `git diff --stat`); the cost of building
  it is trivially less than the cost of one oversized MR getting past
  review.
- **Considered replacing `paused-by-circuit-breaker` with a generic
  `paused` state parameterized by reason.** Rejected: D18 explicitly
  excludes `paused-by-circuit-breaker` from auto-cooldown-release while
  allowing operator `pause` to auto-release. A single state with a
  reason field requires the auto-release logic to switch on the reason
  field — same complexity, less self-documenting state-machine diagram.
- **Considered demoting Test*-form check (gate 4f extension from PRD-
  align round 2) to a should-fix.** Rejected: NG2 is a PRD non-goal,
  and `Benchmark*`/`Example*`/`Fuzz*` slipping past gate 4f's
  directory check is not hypothetical — same-package `_test.go` files
  routinely contain non-Test forms in real Go codebases. The check is
  a 5-LOC AST walk; cost-benefit overwhelmingly favors keeping it.
- **Considered moving Phase 0 tasks 9 (branch-GC), 12 (SEV-1 runbook),
  13 (branch-protection) to Phase 1.** Rejected: round 1's Phase 1
  entry precondition explicitly says "tasks 9/12/13 are desirable but
  non-blocking for Phase 1 entry" — i.e., they're *already* not on the
  Phase 1 critical path. Re-classifying them as Phase 1 deliverables
  would force them onto the Phase 1 critical path, which is the
  opposite of round 1's intent.
- **Considered cutting OQ5 (v1 → v2 mode migration) entirely.**
  Rejected: OQ5 is a *future-design-continuity* note ("don't paint
  ourselves into a corner where the v2 plan can't extend the v1
  artifacts"), not a v1 deliverable. It's exactly the kind of open
  question that belongs in a v1 design's OQ list.

---

## Report 1: Risk

**Reviewer:** chrome (inline)
**Plan:** `.designs/auto-test-pr/synthesis.md` (commit `7e940593`)

Walk-through of the plan looking for execution risks: technical
risks (unproven approaches, complex integrations, performance
unknowns), dependency risks (external services, third-party libraries,
API stability), knowledge risks (areas requiring expertise not
mentioned in the plan), rollback risks (what if a phase fails
mid-execution? Is there a recovery path?), and missing spike/POC tasks
for high-uncertainty items.

### Technical risks — unproven approaches

- **RISK: Tautology-linter sub-rule (i) requires Go AST data-flow
  analysis with no spike.**
  Impact: HIGH (gate 4d is a quality MUST per Q2; if its main
  protection sub-rule is fragile, the gate is paper protection)
  Likelihood: HIGH (data-flow analysis is a well-known
  precision/recall trap)
  Mitigation: must-fix
  Suggested action: spike before commitment (fix #4).

- **RISK: Mutant runner's mutation-selection algorithm is unspecified.**
  Impact: HIGH (D11 caps count to 5 but doesn't say what to mutate;
  three different polecats would ship three different runners with
  three different gate-4b false-positive rates)
  Likelihood: HIGH (any implementer must pick an algorithm; no choice
  is documented)
  Mitigation: must-fix
  Suggested action: write the mutation grammar and selection rule
  into task 6b (fix #3).

- **RISK: `gt sandbox or equivalent` is uncommitted on implementation
  shape.**
  Impact: MEDIUM (three different agents could pick three substrates;
  integration test 5c may fail to compose)
  Likelihood: MEDIUM (Phase 0 dependency graph allows tasks 5a/5b/5c
  to be parallelized in some conditions)
  Mitigation: must-fix
  Suggested action: ADR sub-step at the front of 5a (fix #5).

### Dependency risks

- **RISK: Refinery per-MR-bead label query + `approved-by:<user>`
  semantics may not exist; if absent, ~2 weeks of Phase 0 wasted
  before discovering it.**
  Impact: HIGH (Phase 0 cannot complete; D15 / G1 promise is
  unreachable)
  Likelihood: MEDIUM (round 1 task 10 split into verify + wire, but
  verify is buried mid-Phase-0)
  Mitigation: must-fix
  Suggested action: lift verify into Phase 0a (fix #2).

- **RISK: Mayor main-CI-break event subscription may not exist; same
  rolling-discovery risk as Refinery label query.**
  Impact: HIGH (D16 SEV-1 path is unreachable; R15 mitigation is
  paper)
  Likelihood: MEDIUM (Mayor patrol infra is asserted but not
  enumerated in the plan)
  Mitigation: must-fix
  Suggested action: lift verify into Phase 0a (fix #2).

- **RISK: Pinned-bead `Issue.Metadata` may not be durable for ~5KB
  JSON blobs; data-model fallback is materially different work.**
  Impact: HIGH (every state transition writes Metadata; if it loses
  data, the state machine is silently corrupt)
  Likelihood: LOW (Metadata is described as an extension point, not
  validated for blob size; LOW because the synthesis flagged it but
  the design rests on it being safe)
  Mitigation: should-fix → promoted to must by fix #2 (round-trip
  spike in Phase 0a).

- **RISK: 5b's `go mod download` warm-up may not catch all packages
  invoked by `go test -count=10`.**
  Impact: MEDIUM (a fresh fetch under network drop crashes the
  cycle; rare but reproducible only on first-time package interactions)
  Likelihood: LOW (Go's module system pulls aggressively in `go mod
  download`, but transitively-test-only deps may be missed)
  Mitigation: should-fix
  Suggested action: explicit 10/10-rerun acceptance + `tcpdump`
  fallback verification (fix #7).

- **RISK: `golang.org/x/tools/cover` may not already be an indirect
  dep of `gt`.** OQ2 names this; without verification, task 6a may
  inadvertently introduce a new top-level dep.
  Impact: LOW
  Likelihood: LOW (probably already pulled in; trivial to confirm)
  Mitigation: should-fix
  Suggested action: 5-min verification step inside task 6a; if it
  introduces a new direct dep, note in the MR description for the
  reviewer.

### Knowledge risks

- **RISK: Tasks 6b (mutant runner) and 6c (tautology linter) require
  Go AST expertise not named in the plan.**
  Impact: MEDIUM (footguns lead to silent gate false-negatives)
  Likelihood: MEDIUM (random polecat assignment cannot guarantee Go
  AST proficiency)
  Mitigation: should-fix
  Suggested action: knowledge-prep sub-step on each task; risk-
  register R25 (fix #9).

- **RISK: Refinery internals (label query, `approved-by` semantics)
  are not in the plan; verifying them requires spelunking
  `internal/refinery/` or asking the Refinery owner.**
  Impact: LOW
  Likelihood: LOW (verification is a 30-min task once someone knows
  where to look)
  Mitigation: should-fix
  Suggested action: assign Phase 0a-1 to a polecat with familiarity
  in the Refinery codebase (or have them mail the Refinery
  maintainer).

### Rollback risks

- **RISK: `mol-auto-test-pr-cycle` panics if `town-auto-test-pr-state`
  is missing — partial Phase 0 revert (drop task 8) leaves the
  formula unrunnable.**
  Impact: MEDIUM (revert is supposed to be safe; a panicking patrol
  blocks every other patrol)
  Likelihood: LOW (revert is rare)
  Mitigation: should-fix
  Suggested action: missing-town-bead integration test in Phase 0
  exit criteria; risk-register R26 (fix #10).

- **RISK: `paused-by-circuit-breaker` state is missing from the
  state-machine diagram in §Data Model.** A reader of the diagram
  alone wouldn't know this state exists, which means a future polecat
  iterating on the state machine could clobber the SEV-1 path.
  Impact: MEDIUM (state-machine drift; SEV-1 path corruption)
  Likelihood: LOW (the design body names the state eight times, so
  a careful reader catches it)
  Mitigation: must-fix (this is documentation correctness, not just
  a process risk)
  Suggested action: extend diagram (fix #1).

- **RISK: D18 cooldown auto-release fires immediately after a SEV-1
  if cadence_days has already elapsed; could re-fire on the same
  rig minutes after a circuit-breaker auto-pause.**
  Impact: HIGH if it happened (defeats the 7d cooldown)
  Likelihood: LOW (D18 explicitly says cycles in
  `paused-by-circuit-breaker` do NOT auto-release)
  Mitigation: covered by D18; verified by reading D18 (no fix needed).
  *Verification:* re-read D18 — yes, the carve-out is explicit:
  "Cycles in `paused-by-circuit-breaker` (D16) do **not** auto-
  release — they require explicit `gt auto-test-pr resume`."

### Missing spike / POC tasks

- **RISK: No POC of the polecat actually writing one auto-test PR
  end-to-end.** Phase 0 lands all the wiring inert; Phase 1 task 16
  flips `enabled=true` in the *production* pilot rig. There's no
  intermediate "dry-run on a fixture rig" step. The first time the
  full cycle runs end-to-end is on the rig whose green main blocks
  every other patrol (R3).
  Impact: MEDIUM (R3 mitigation rests on the ≤1 PR/week cap and the
  circuit breaker; a botched first cycle wastes a week)
  Likelihood: LOW (gates catch most issues)
  Mitigation: NOT applied (judged: R3 is mitigated by ≤1 PR/week +
  circuit breaker + magic-phrase pause; the marginal benefit of a
  fixture-rig dry-run vs. the cost of building one for a single-rig
  v1 is unfavorable). Recorded in "considered and rejected."

### Risk-summary table

| Class | Count |
|-------|-------|
| HIGH-impact must-fix | 4 (sub-rule (i) spike, mutant-grammar, Refinery verify, Mayor verify) |
| MEDIUM-impact must-fix | 2 (`gt sandbox` ADR, `paused-by-CB` diagram) |
| Should-fix | 4 (5b acceptance, OQ4 spike, 6b/c knowledge-prep, missing-town-bead test) |
| Considered + rejected | 1 (fixture-rig dry-run) |

---

## Report 2: Scope-creep

**Reviewer:** chrome (inline)
**Plan:** `.designs/auto-test-pr/synthesis.md` (commit `7e940593`)

Walk-through of the plan looking for: gold-plating (tasks that go
beyond what's needed for the stated goals), premature optimization
(performance work before proving it's needed), over-engineering
(abstractions, frameworks, generalization beyond requirements), nice-
to-haves disguised as requirements, and tasks that could be deferred
to a follow-up without impacting the core feature.

### Gold-plating

- **CUT: OQ7 TALON-style-comment-exception language in conventions
  template.** v1 pilot is `gastown_upstream`, not a TALON rig.
  Including the exception in the v1 template serves a hypothetical
  future v2 TALON rig.
  Classification: should-fix
  Suggested action: remove from template; keep OQ7 in the Open
  Questions list with a `v2 follow-up` annotation (fix #11).

- **CONSIDERED: Cutting the conventions-template `--emit-template` /
  `show-template` verbs (Phase 0 task 2d) entirely; instead let the
  pilot author the conventions sheet from scratch.** Rejected.
  Round 1 added the template precisely because "every opted-in rig
  re-derives these constraints from the design doc — drift is
  inevitable, and the polecat's refusal-to-run-without-conventions
  check is brittle." Cutting the template re-opens that risk. The
  template is ~50 LOC of Markdown shipped in the binary.

### Premature optimization

- **CONSIDERED: Phase 0 dependency graph (round 1 fix #6).**
  The graph documents critical-path parallelism that helps Mayor
  dispatch ~6 polecats in parallel. For a v1 with one rig and
  ~16 tasks, parallelism is nice-to-have rather than essential
  (a serial run is ~16 task-times; parallel is ~7). Rejected as
  cut: the graph also documents *task ordering*, which is the
  more important contract; the parallelism is a side benefit.
  Round 1 already shipped the graph; cutting it now would be churn.

- **NO premature optimization in Phase 0.** All gate work is
  bounded (≤5 mutants per test, ≤200 LOC per MR, ≤30 min per
  cycle). No caches, no batching, no fancy concurrency. Good
  shape for v1.

### Over-engineering

- **CONSIDERED: Single CLI namespace `gt auto-test-pr` (D1) with
  eight verbs.** Eight verbs feels like a lot for a v1. Could
  cut: `pause --all`, `resume --all`, `history`, `show --raw`?
  Rejected:
  - `pause --all` / `resume --all` are documented operator
    incident-response paths; a partial outage during pilot needs
    a town-wide kill switch.
  - `history` is read-only and surfaces the rig's transition
    log — directly satisfies the audit-trail half of D8.
  - `show --raw` is the operator's escape hatch for parsing the
    state bead during incidents (ux leg's persona analysis).
  Each verb is ~50 LOC; cumulative cost is small.

- **CONSIDERED: `feature_flags.auto_test_pr_revision_routing` flag
  in Phase 2 (D3).** A feature flag for a single Phase 2 rollout
  feels like ceremony. Rejected: the flag is the *revert
  primitive* for Phase 2 (per §Reverting "Phase 2 revert: flip
  `feature_flags.auto_test_pr_revision_routing=false`"). Without
  it, Phase 2 revert requires reverting the
  `mol-pr-feedback-patrol` extension code, which touches a patrol
  used by every PR in the rig.

### Nice-to-haves disguised as requirements

- **CONSIDERED: D9 reviewer magic-phrase pause `gt auto-test-pr:
  pause-rig-7d`.** This is a UX nice-to-have that the CLI also
  serves. Rejected: the magic phrase is the under-fire fallback
  that doesn't require finding the CLI during an incident. ux
  leg's persona analysis names this as the most important
  reviewer affordance. The implementation is ~10 LOC of regex in
  `mol-pr-feedback-patrol` + a state-bead write.

### Tasks that could be deferred without impacting v1

- **DEFER: Phase 3 numbered tasks 24-27.** These are explicitly
  deferred but numbered tasks read as commitments.
  Classification: must-fix (form, not substance)
  Suggested action: rewrite Phase 3 as narrative bullets without
  task numbers (fix #6).

- **CONSIDERED: Phase 0 task 9 (branch-GC patrol).** Could defer
  to Phase 1 since the first stale branch can't appear until 7
  days after the first cycle. Rejected: Round 1 explicitly added
  this as Phase 0 with the "non-blocking for Phase 1 entry"
  caveat. Building it inert in Phase 0 means it's ready to run
  the moment Phase 1 ships its first MR; building it in Phase 1
  forces a Phase-1-shipping polecat to do unrelated work mid-
  pilot.

- **CONSIDERED: Phase 0 task 12 (SEV-1 runbook).** A runbook
  before the first SEV-1 has occurred is somewhat speculative.
  Rejected: D16's auto-pause path nudges Overseer; an Overseer
  receiving a SEV-1 nudge with no runbook is the failure mode the
  runbook prevents. The cost is ~1 hour of writing.

- **CONSIDERED: Phase 0 task 13 (branch-protection rule).**
  Could defer to Phase 1. Rejected: R11 / C-SEC-6 says branch
  protection is required *before* the first auto-test cycle pushes
  to a branch. Configuring it in Phase 0 closes the window where
  an attacker could push to `auto-test/<rig>/<bead>` *before*
  the cycle agent has any work in flight.

### Scope-creep summary

| Class | Count |
|-------|-------|
| CUT (must-fix) | 1 (Phase 3 numbering) |
| CUT (should-fix) | 1 (OQ7 template language) |
| CONSIDERED + rejected | 8 (template emit, dep graph, CLI verb count, feature flag, magic phrase, branch-GC defer, runbook defer, branch-protection defer) |

---

## Total fix count

- 6 must-fix items (1, 2, 3, 4, 5, 6)
- 5 should-fix items (7, 8, 9, 10, 11)
- 11 items considered and rejected (kept for audit)

All 11 fixes applied to `.designs/auto-test-pr/synthesis.md` in this
round.

## Sources

- [Plan synthesis post-round-1](../../.designs/auto-test-pr/synthesis.md)
  — commit `7e940593`
- [Plan-self-review round 1 log](review-round-1.md)
- [PRD-alignment round 1 log](prd-align-round-1.md)
- [PRD-alignment round 2 log](prd-align-round-2.md)
- [PRD-alignment round 3 log](prd-align-round-3.md)
- [Bead gu-wfs-ny25u](bd show gu-wfs-ny25u) — assignment

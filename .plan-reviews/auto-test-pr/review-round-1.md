# Plan Self-Review Round 1 — auto-test-pr (completeness + sequencing)

> Round 1 of plan self-review. Three rounds of PRD-alignment have already
> shipped. This round reviews the plan against itself for **internal**
> quality: completeness (anything missing?) and sequencing (right order,
> hidden deps, parallelism).

**Bead:** gu-wfs-i24nq
**Plan:** `.designs/auto-test-pr/synthesis.md` (post-round-3 commit
`db1c32a0`)
**Reviewer:** chrome (inline; polecats can't sling, so both reviewer
roles were performed by chrome in one session — same pattern as the
PRD-alignment rounds 1, 2, and 3)

## Reports

Both review reports are inlined below for reproducibility.

- **Completeness report** — see report 1 below.
- **Sequencing report** — see report 2 below.

## Consolidated must-fix list (applied to design doc)

The two reports surfaced 8 issues. Items applied directly to
`.designs/auto-test-pr/synthesis.md`:

1. **Tasks too coarse-grained — Phase 0 tasks 2, 5, 6 each bundle
   multi-day work.** Task 2 ("Ship CLI commands") fuses 8 verbs each
   with their own validation paths; task 5 ("Implement sandbox
   wrapper") fuses credential-strip + network-drop + CWD pin +
   wall-clock cap; task 6 ("Land coverage-delta parser, AST-aware
   mutant runner, tautology linter, with full unit tests") fuses
   three substantial Go packages. As single tasks these obscure
   dependencies (e.g., task 6 actually blocks-on task 5 because the
   mutant runner shells through the sandbox; the tautology linter has
   four sub-rules each meriting their own test fixtures).
   *Fix:* split task 2 into **2a** (`enable`/`disable`) + **2b**
   (`pause`/`resume`/`status`/`show`/`history`) + **2c** (`revise`
   manual-fallback CLI). Split task 5 into **5a** (credential strip +
   CWD pin) + **5b** (network drop with module-cache warm-up) + **5c**
   (wall-clock cap + integration test of combined wrapper). Split
   task 6 into **6a** (coverage-delta branch-mode parser) + **6b**
   (AST-aware mutant runner) + **6c** (tautology linter with the four
   sub-rule fixtures). The dependency graph (S2 fix below) makes the
   parallelism explicit.

2. **`mode=revise` polecat formula support is implicit but
   never tasked.** Phase 0 task 3 says "Land
   `mol-polecat-work-test-improver` formula extending
   `mol-polecat-work` with the five quality-gate steps, the
   bug-discovery NOTES protocol, and the sandbox wrapper" — but
   doesn't call out that the formula MUST implement *both*
   `mode=create` AND `mode=revise` (with the D19 reply step). Phase 1
   step 12a (manual revise CLI) and Phase 2 step 14 (feedback-patrol
   routing) both depend on `mode=revise` working from day one. Without
   an explicit task, a polecat could land create-only and silently
   break the G4 Phase-1 fallback.
   *Fix:* split task 3 into **3a** (formula skeleton + `mode=create`
   path with all five gates + bug-discovery NOTES protocol + sandbox
   integration) and **3b** (`mode=revise` path including the D19 reply
   step on `args.revision.comments[]`). Task 3b is required for
   Phase 1 step 12a; explicit ordering captured in the dependency
   graph.

3. **Mayor cycle-close handler is implied throughout but never
   listed as a discrete task.** Multiple plan elements depend on
   Mayor having a handler that fires on MR-bead state-change events:
   D2a (in-flight-MR semantics on disable), D5 (bug-discovery NOTES
   parsing), Q6 circuit-breaker counter increment, the
   `mr-pending → cooled-down` transition (synthesis Key Components §1
   step 6), and the rejection-log append on close-unmerged. Phase 0
   has no task for this handler; it's referenced as if it already
   exists but the plan never says where the existing handler is or
   what's added.
   *Fix:* added Phase 0 task **3c** (sibling to 3a/3b, kept in the
   "Mayor + polecat formula" cluster): "Implement Mayor cycle-close
   handler. Subscribes to MR-bead state-change events for beads
   labeled `gt:auto-test-pr`. On merged → CAS-transition the rig's
   state bead `mr-pending → cooled-down` and append a transition
   record. On closed-unmerged → CAS-transition `mr-pending →
   cooled-down`, append both a transition record and a rejection
   record (with `target_path` for the per-file 21d cooldown), and
   increment the town-bead circuit-breaker counter; if the rig has
   ≥3 closes in any rolling 7-day window, CAS-transition to
   `paused-by-circuit-breaker` and nudge Overseer (Q6 SEV-2). On
   either path, parse any `BUG-DISCOVERED:` NOTES and file a P2
   bug bead in the rig (`<rig>-bug-from-auto-test-NNN`) linked to
   the cycle's MR bead." Updated Phase 0 exit criteria to include
   this handler's unit tests (merged path, closed-unmerged path,
   3-closes-trips-circuit-breaker path, BUG-DISCOVERED parsing).

4. **Conventions-sheet template is missing from Phase 0 deliverables.**
   Phase 1 task 9 ("Author and commit `.gt/auto-test-pr/conventions.md`
   and `.gt/auto-test-pr/mr-template.md`") instructs a maintainer to
   author the conventions sheet from scratch. But: the synthesis
   §Key Components §6 says the template MUST include the NG2
   forbid-list (no integration/e2e/load tests; no `Benchmark*`/
   `Example*`/`Fuzz*`), the NG5 churn-proximity preference, and the
   OQ7 TALON-style comment exception. Without a template shipped in
   the `gt` binary, every opted-in rig re-derives these constraints
   from the design doc — drift is inevitable, and the polecat's
   refusal-to-run-without-conventions check is brittle.
   *Fix:* added Phase 0 task **2d** (sibling under the CLI cluster):
   "Ship `internal/autotestpr/conventions_template.md` checked into
   `gt` and exposed via `gt auto-test-pr enable --emit-template >
   .gt/auto-test-pr/conventions.md` (and via a `gt auto-test-pr
   show-template` read-only verb). Template includes the NG2
   forbid-list, NG5 churn-proximity preference, OQ7 TALON-style
   comment exception, the provenance-marker requirement (D8), and
   placeholders for rig-specific test conventions (e.g., 'no
   `time.Sleep` in tests', 'use table-driven where ≥3 cases'). Phase
   1 step 9 amended to read 'Run `gt auto-test-pr enable
   --emit-template`, customize for `gastown_upstream`, commit via PR.'"

5. **Phase 1 task numbering collides with Phase 0.** Phase 0 ends at
   task 11; Phase 1 begins at task 9. The two lists overlap on
   numbers 9, 10, 11. Step 12a is a peer of step 12 but its lettered
   suffix suggests sub-step status. A reader following "task 11 must
   precede step 12" cannot tell whether "task 11" means Phase 0's
   SEV-1 wiring or Phase 1's flip-enabled.
   *Fix:* Phase 1 renumbered to start at task 12, with subsequent
   tasks 13, 14, 15, 16. The former 12a is promoted to peer task **15**
   ("Manual revision pathway during Phase 1 — `gt auto-test-pr
   revise`"). Phase 2 renumbered to start at task 17, ending at 21.
   Phase 3 renumbered 22-25. Cross-references in §Reverting and the
   risk-register columns updated accordingly. The phase-boundary
   number jumps make the sequence unambiguous.

6. **Critical-path / parallelism is not documented; Phase 0 reads as
   serial.** With 11 tasks (now ~16 after the splits in fix #1),
   Phase 0 reads as if a single agent works them in numbered order.
   In reality the dependency graph is shallow: most tasks are
   independent. The actual critical path is **task 5 (sandbox) →
   task 6 (gates) → task 3 (formula)**, with everything else
   parallelizable. Documenting this lets the Mayor dispatch ~6 polecats
   in parallel for Phase 0 instead of 1 serial worker, cutting
   Phase 0 wall-clock by roughly 3-4×.
   *Fix:* added a **Phase 0 dependency graph** sub-section under the
   Phase 0 task list. ASCII DAG showing: tasks 1, 2a-d, 7, 8, 11,
   13 are independent (parallelizable batch A); task 5 (5a, 5b, 5c) is
   a serial chain; task 6 (6a, 6b, 6c) blocks on task 5; task 3 (3a,
   3b, 3c) blocks on tasks 4 and 6 for tests; tasks 9 (branch-GC
   patrol), 10 (Refinery approval gate), 12 (SEV-1 auto-revert) block
   on task 3 for label semantics. Critical path: 5a → 5b → 5c → 6a/b/c
   (parallel) → 3a/b/c (parallel after 6 done). Documents the
   sequencing and unblocks parallelism.

7. **`--override-circuit-breaker` flag not in Phase 0 task 2's verb
   list.** D16 says "Manual override is `gt auto-test-pr resume
   --rig=<rig> --override-circuit-breaker`." Phase 0 task 2 lists the
   eight verbs but no flags. The flag is the *only* documented escape
   from `paused-by-circuit-breaker`; missing it from Phase 0 means
   a SEV-1 incident has no manual recovery path during the pilot.
   *Fix:* amended task 2b (after the split) to call out the flag
   explicitly: "`resume --rig=<rig> [--override-circuit-breaker]`
   and `resume --all`. The override flag bypasses the
   `paused-by-circuit-breaker` state per D16 and emits an audit-log
   entry naming the operator and timestamp."

8. **Hidden dep: Refinery MR-bead label-query and `approved-by:<user>`
   semantics are asserted but unverified.** D15 ("Auto-test MRs
   require explicit maintainer approval") says Refinery's merge
   handler "reads the bead label `gt:auto-test-pr` and refuses to
   merge until a maintainer-approval record exists on the MR bead
   (mirrors the existing approval mechanism used for human-authored
   MRs in repos that require review)." This is asserted as
   pre-existing infrastructure but the plan never tasks verifying it.
   If Refinery does NOT today support per-MR-bead label query +
   `approved-by:<user>` label semantics, Phase 0 task 10 silently
   becomes "build that infrastructure first" — which is a much larger
   scope.
   *Fix:* amended task 10 to include a verification sub-step
   *before* implementation: "(a) Verify Refinery supports per-MR-bead
   label query (`bd list --label gt:auto-test-pr` or equivalent) and
   `approved-by:<user>` label semantics. If yes, proceed with the
   merge-gate wiring. If no, FILE a prerequisite bead and DEFER this
   task; Phase 0 cannot complete without it. (b) Wire the merge-gate."
   Same verification pattern added to task 12 (Mayor SEV-1 path) for
   the "main-CI-break event subscription" — confirming whether
   Mayor already has this patrol infrastructure or needs new wiring.

## Consolidated should-fix list (applied to design doc)

9. **No CI / `go vet` / rebase-check exit criterion for Phase 0
   deliverables.** The bead's `gates_commands` block names `go build
   ./...`, `go vet ./...`, `go test ./...`, and
   `scripts/check-upstream-rebased.sh`. Refinery enforces these on
   merge. The plan doesn't say "all new packages pass." Implicit but
   worth surfacing — especially for the new `internal/autotestpr/*`
   packages from task 6.
   *Fix:* added bullet to Phase 0 exit criteria: "All new Go packages
   pass `go vet ./...`, `go build ./...`, `go test ./...`, and
   `scripts/check-upstream-rebased.sh` (the rig's standard refinery
   gates). New CI configuration is NOT required — these gates already
   run on every MR."

10. **Operator runbook for the SEV-1 auto-revert response is missing.**
    D16 says Mayor sends a "high-priority nudge to the Overseer with
    the SEV-1 payload." But: what does the Overseer DO when the nudge
    arrives? No runbook. R15 mitigates the technical path but the
    human side is undocumented; an Overseer who's never seen a SEV-1
    has no script.
    *Fix:* added Phase 0 task **14** (after the renumber): "Document
    Overseer SEV-1 response runbook at
    `.gt/auto-test-pr/sev1-runbook.md` (in the `gt` repo, not the
    pilot rig). Steps: (1) confirm the auto-filed revert MR landed and
    main is green; (2) verify the rig's state bead is
    `paused-by-circuit-breaker` with a 7d cooldown; (3) decide whether
    to file an investigation bead for the test that broke main; (4)
    decide whether to override the circuit breaker via
    `gt auto-test-pr resume --rig=<rig> --override-circuit-breaker`
    or to wait out the cooldown; (5) record the decision in the
    rig's state bead's `incidents[]` log via
    `gt auto-test-pr show --rig=<rig> --raw`."

11. **Branch-protection rule on `auto-test/<rig>/<bead>` branches is
    in R11 / C-SEC-6 but not implemented as a task.** R11 says
    "Branch-protection rule on origin: only Refinery / cycle agent
    can push to that prefix." The mitigation is asserted; no task
    creates the rule.
    *Fix:* added Phase 0 task **15** (after the renumber): "Configure
    branch-protection rule on `gastown_upstream`'s origin for
    `refs/heads/auto-test/*/*` — only the cycle-agent / Refinery
    service identity may push. Verified via attempting a push from
    a non-service identity (must fail). For multi-rig v2, this
    rule is captured in the per-rig opt-in template so new rigs
    inherit it on enable."

12. **Phase 1 step 11 (now 14, flip enabled) has no documented
    precondition that Phase 0 tasks 10 + 12 (approval gate + SEV-1
    auto-revert) are operational.** As written, a Phase 0 partial
    rollout could ship the cycle without the merge gate or the
    auto-revert, leaving the pilot exposed.
    *Fix:* amended Phase 1 to include an explicit precondition:
    "**Phase 1 may not begin until Phase 0 tasks 10 (approval gate)
    AND 12 (SEV-1 auto-revert) integration tests pass.** This is the
    minimum-viable safety net before flipping `enabled=true`. Phase 0
    other tasks (e.g., branch-GC, runbook, branch-protection) are
    desirable but non-blocking for Phase 1 entry."

## Items that turned out to already be covered (no fix needed)

(None — every PARTIAL/MISSING item in the two reports turned into a
fix above. No false alarms.)

---

## Report 1: Completeness

**Reviewer:** chrome (inline)
**Plan:** `.designs/auto-test-pr/synthesis.md` (commit `db1c32a0`)

Walk-through of the plan looking for missing infrastructure, missing
data migrations, missing test tasks, missing documentation, missing
error handling, implicit dependencies, and tasks too coarse-grained.

### Infrastructure / Build / CI

- COVERED: Formula registration in Mayor's patrol set (Phase 0 #4 with
  inert first step until rigs opt in).
- COVERED: New Go packages mentioned by file path (`internal/autotest/
  coverage.go`, `mutant.go`, `tautology.go`).
- PARTIAL (should-fix): No explicit "passes `go vet` + rebase-check"
  exit criterion. **Fix:** item 9.
- PARTIAL (must-fix): `gt sandbox` helper specified as "or equivalent"
  but its concrete shape isn't in the plan; tasks 5a-c (post-fix) make
  this concrete. **Fix:** item 1 (split task 5).
- COVERED: Sling priority floor (D13, Phase 0 #7).

### Data migrations / schema

- COVERED: `schema_version` field round-tripping (data-model section).
- COVERED: Pinned-bead provisioning (Phase 0 #8 town bead, Phase 1 #10
  per-rig bead).
- COVERED: FIFO eviction caps on transitions/rejection logs.
- GAP: No "schema-init / repair" task for the case where a state bead
  is deleted or corrupted. Operationally low-likelihood (pinned beads
  are durable); acceptable for v1. v2 should add a self-heal patrol.

### Test tasks

- COVERED: Unit tests for all five gates (Phase 0 #6), the four
  gate-4d sub-rules, the gate-4a branch-mode parser, the merge gate
  (10), the SEV-1 path (11).
- COVERED: Integration tests for `mol-pr-feedback-patrol` extension
  (Phase 2 #16).
- PARTIAL (must-fix): No task for Mayor cycle-close handler tests
  (and no task for the handler itself). **Fix:** item 3.
- PARTIAL (must-fix): `mode=revise` polecat formula tests are implicit.
  **Fix:** item 2.
- GAP: No load / wall-clock-budget perf test. The plan asserts 30-min
  cycle wall-clock cap; a fixture-based test that runs the polecat
  against a synthetic large package would validate the cap. Acceptable
  for v1 because the cap is enforced as a hard exit, not as a
  performance contract.

### Documentation updates

- PARTIAL (should-fix): No README / wiki / `gt auto-test-pr help` task
  for the v1 launch — R14 asserts the documentation will exist but
  no task creates it. The conventions-sheet template (item 4) covers
  per-rig docs; the global help/README is still missing. *Decision:*
  fold into item 4 (conventions template ships with `gt`, so the
  `gt auto-test-pr` help text comes from the same translation
  unit; updating CLI help is part of task 2's Cobra subcommand
  wiring).
- PARTIAL (should-fix): Operator SEV-1 runbook missing. **Fix:**
  item 10.

### Error handling / rollback

- COVERED: Per-phase revert in §Reverting.
- COVERED: D2a in-flight semantics on disable.
- COVERED: D12 cycle-failure backoff.
- PARTIAL (must-fix): No `--override-circuit-breaker` flag in CLI
  task list. **Fix:** item 7.
- COVERED: Idempotent CAS retries on next tick.

### Implicit dependencies not called out as tasks

- PARTIAL (must-fix): Mayor cycle-close handler. **Fix:** item 3.
- PARTIAL (must-fix): `mode=revise` polecat formula support. **Fix:**
  item 2.
- PARTIAL (must-fix): Conventions-sheet template. **Fix:** item 4.
- PARTIAL (should-fix): Branch-protection rule. **Fix:** item 11.
- PARTIAL (should-fix): Refinery's per-MR-bead label query and
  `approved-by:<user>` semantics existence. **Fix:** item 8.

### Tasks too coarse-grained

- PARTIAL (must-fix): Tasks 2, 5, 6 each fuse multi-day work units.
  **Fix:** item 1.

### Summary

| Class | Count |
|-------|-------|
| COVERED | 11 |
| PARTIAL must-fix | 6 |
| PARTIAL should-fix | 3 |
| GAP | 2 (load test, schema-self-heal — both deferred to v2 with rationale) |

---

## Report 2: Sequencing

**Reviewer:** chrome (inline)
**Plan:** `.designs/auto-test-pr/synthesis.md` (commit `db1c32a0`)

Walk-through of phase ordering, hidden dependencies, parallelization
opportunities, critical-path identification, and circular-dep checks
across all four phases.

### Phase 0 internal sequencing

- COVERED: Tasks 1 (settings JSON keys) and 8 (town bead) are
  legitimately early and independent.
- COVERED: Task 5 (sandbox) precedes task 6 (gates that use
  sandbox). Order is correct.
- COVERED: Task 3 (polecat formula) and task 4 (cycle molecule) are
  independent of each other and can land in either order.
- PARTIAL (must-fix): Tasks 2, 5, 6 too coarse to sequence
  meaningfully. **Fix:** item 1.
- PARTIAL (must-fix): Task 10 (approval gate) and task 11 (SEV-1
  path) depend on label semantics from tasks 3 + 4 but the dependency
  is undocumented. **Fix:** item 6 (dependency graph documents it).
- PARTIAL (must-fix): No critical-path identification or parallelism
  guidance. **Fix:** item 6.

### Phase 0 → Phase 1 boundary

- MISALIGNED (must-fix): Phase 1 task numbering collides with
  Phase 0 (both have 9, 10, 11). **Fix:** item 5.
- PARTIAL (should-fix): Phase 1 step 11 (flip enabled) has no
  documented precondition. **Fix:** item 12.

### Phase 1 internal sequencing

- COVERED: Conventions sheet authored (#9, now #12 after renumber)
  before per-rig state bead provisioning (#10/#13) before flip
  (#11/#14) is correct order.
- PARTIAL (must-fix): Step 12a (manual revise CLI) listed as a
  sub-step of step 12 (observation window) but is actually a peer
  capability that must work from week 1. **Fix:** item 5 (renumber
  promotes it to peer task 15).
- COVERED: Phase 1 exit criteria (#13/#16 after renumber) cleanly
  separate the PRD criteria from the graduation sub-criterion.

### Phase 1 → Phase 2 boundary

- COVERED: Phase 2 cannot start until Phase 1's graduation
  sub-criterion (≥2 consecutive merged MRs without operator
  intervention) is met. Documented in Phase 1 exit criteria.

### Phase 2 internal sequencing

- COVERED: Task 14 (`mol-pr-feedback-patrol` extension) before
  task 15 (magic phrase parsing) before task 16 (integration tests)
  before task 17 (feature-flag flip) before task 18 (one full
  revision cycle). Order correct.
- COVERED: Feature-flag default-false means task 14 can land before
  task 17 with no behavior change for non-`gt:auto-test-pr` PRs.

### Phase 3 sequencing

- COVERED: Phase 3 explicitly out of scope; tasks 19-22 (post-
  renumber 22-25) are listed for completeness but don't block v1.

### Critical path identification

- PARTIAL (must-fix): Not documented. **Fix:** item 6 (dependency
  graph). Critical path is **5a → 5b → 5c → {6a, 6b, 6c} → {3a, 3b,
  3c}** (~9 task-times serially; ~4 with parallelism: 5 chain + 6
  parallel + 3 parallel). Other 11 tasks parallel-ready alongside.

### Circular dependency check

- No circular deps detected. The `mr-pending → mr-revising →
  mr-pending` cycle in the state machine is intentional (revision
  loop) and correctly bounded by per-rig cadence + circuit breaker.

### Summary

| Class | Count |
|-------|-------|
| COVERED | 9 |
| PARTIAL must-fix | 4 |
| PARTIAL should-fix | 1 |
| MISALIGNED must-fix | 1 |
| GAP | 0 |

---

## Total fix count

- 8 must-fix items (1, 2, 3, 4, 5, 6, 7, 8)
- 4 should-fix items (9, 10, 11, 12)

All 12 applied to `.designs/auto-test-pr/synthesis.md` in this round.

## Sources

- [Plan synthesis post-round-3](.designs/auto-test-pr/synthesis.md)
  — commit `db1c32a0`
- [PRD-alignment round 1 log](.plan-reviews/auto-test-pr/prd-align-round-1.md)
- [PRD-alignment round 2 log](.plan-reviews/auto-test-pr/prd-align-round-2.md)
- [PRD-alignment round 3 log](.plan-reviews/auto-test-pr/prd-align-round-3.md)
- [Bead gu-wfs-i24nq](bd show gu-wfs-i24nq) — assignment

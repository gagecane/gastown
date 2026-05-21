# PRD Alignment Round 2 — auto-test-pr

> Round 1 covered PRD requirements + goals. Round 2 covers constraints
> + non-goals.

**Bead:** gu-wfs-et5vw
**PRD:** `.prd-reviews/auto-test-pr/prd-draft.md` (commit 13d14a44)
**Plan:** `.designs/auto-test-pr/synthesis.md` (post-round-1 commit
6303abb4)
**Reviewer:** chrome (inline; polecats can't sling, so both reviewer
roles were performed by chrome in one session — same pattern as round 1)

## Reports

Both review reports are inlined below for reproducibility.

- **Constraints-compliance report** — see report 1 below.
- **Non-goals-enforcement report** — see report 2 below.

## Consolidated must-fix list (applied to design doc)

The two reports surfaced 3 issues. Items applied directly to
`.designs/auto-test-pr/synthesis.md`:

11. **C2 — Refinery-vs-external-PR mode detection: explicit v1 scope
    note.**
    *PRD §Constraints:* "for repos where we have direct push, we go
    through gt done → Refinery. For external/community repos we use
    `gh pr create` directly. The mechanism must detect which mode
    applies per rig." *Plan gap:* Q1 cut external-PR mode entirely
    from v1, so the plan implicitly drops the detection step, but
    nothing in the plan says so explicitly. A future reader auditing
    C2 against the plan would see a missing constraint.
    *Fix:* added **D2b** ("Per-rig Refinery-vs-external-PR mode
    detection is N/A in v1") which records that C2 is satisfied by
    *scope removal* (Q1) rather than by implementation, and pins v2
    as the owner of the future detection step + a new
    `auto_test_pr.merge_mode` config key when external mode lands.

12. **NG2 — gate 4f doesn't reject `Benchmark*`/`Example*`/`Fuzz*`.**
    *PRD §Non-Goals:* "Not integration / e2e / load test generation.
    Unit tests only." *Plan gap:* gate 4f (post round 1) verifies file
    path globs and `//go:build integration`, but does not verify the
    *test-function form*. A polecat could write `func BenchmarkFoo(b
    *testing.B)` or `func FuzzBar(f *testing.F)` inside a same-package
    `*_test.go` file and slip past every gate. PRD explicitly forbids
    load-test generation; "unit tests only" excludes Benchmarks,
    Fuzzers, Examples by definition.
    *Fix:* extended gate 4f to require every newly-added top-level
    test function in the diff to match `func Test*(t *testing.T)`.
    Reject `Benchmark*`, `Example*`, `Fuzz*`, and any non-`Test*`
    test-form. Documented the forbid-list in the conventions sheet
    template requirements (Key Components §6 in-repo artifacts).
    Added **R20** to the risk register.

13. **NG5 — within-file targeting can backfill legacy branches.**
    *PRD §Non-Goals:* "Not retroactive coverage cleanup. Greenfield
    only — pick targets forward, don't try to backfill the whole
    rig." *Plan gap:* the cycle's target-pick step uses 30-day churn
    to *select files*, but treats all uncovered branches *within* a
    selected file equally. A churning file with one new function and
    50 untouched legacy branches would send the polecat to backfill
    legacy tests — a de-facto retroactive cleanup of that file,
    against NG5.
    *Fix:* in cycle step 4, after a target file is selected, the
    dispatch envelope's `uncovered_branches[]` is sorted by
    line-distance to recent-churn line ranges (from `git log -L` /
    `git blame` over the 30-day window) so the polecat preferentially
    writes tests for *recently-changed* uncovered branches rather
    than legacy untouched code in the same file. Conventions sheet
    template directs the polecat to prefer churn-adjacent branches.
    Added **R21** to the risk register.

## Items that were verified as already-respected (no fix needed)

- **C1 (gu-gal8 — no polecat-owned bookkeeping beads):** D4 names both
  pinned beads as Mayor-owned; R5 mitigation enforces it at the
  bead-client layer (security C-SEC-5); T4 reinforces. No fix.
- **C3 (reuse `mol-pr-feedback-patrol`):** D3 + Phase 2 wires the
  label-keyed dispatch *additively* into the existing patrol; doesn't
  reinvent. No fix.
- **C4 (no flooding bd ready / Mayor deprioritize):** D13 priority
  floor + ≤1/week cadence + state-machine CAS. No fix.
- **C5 (per-rig config for test/coverage/flakiness/lint):** Q4
  redefinition (language allow-list, single `language` key) replaces
  four per-rig command keys. No fix; the constraint was satisfied by
  the PRD's own clarification.
- **C6 (single-flip revertibility per rig):** `gt auto-test-pr disable
  --rig=<rig>` is the canonical single flip; D2a clarifies in-flight
  semantics. No fix.
- **NG1 (not coverage-percentage chasing):** targeting is `churn ×
  uncovered_branches`, not "lowest coverage first." Banner shows
  coverage delta as evidence, not as targeting input.
- **NG3 (not mutation testing infrastructure):** synthetic-mutant is a
  bounded sanity check (≤5 per test, AST-aware, tmpdir) used as a
  *gate*, not a pipeline. D11 caps cost; T3/D6 keep scope narrow.
- **NG4 (not a code-fixing tool):** bug-discovery NOTES protocol from
  round 1; T4 v1 disallows source changes entirely; gate 4f tests-only
  allow-list; polecat exits with `BUG-DISCOVERED:` NOTES on
  test-fails-on-main rather than landing fix-with-test.
- **NG6 (not language-agnostic day 1):** Q4 allow-list pins v1 to Go;
  CLI rejects unknown languages with v2 follow-up bead pointer.
- **NG7 (not auto-merge; human review required):** D15 + R16
  maintainer-approval gate (default-true `require_review_approval`).

---

## Report 1: Constraints compliance

**Reviewer:** chrome (inline)
**PRD:** `.prd-reviews/auto-test-pr/prd-draft.md` (commit 13d14a44)
**Plan:** `.designs/auto-test-pr/synthesis.md` (post-round-1)

Walk-through of every constraint in the PRD §Constraints section
(C1–C6) — technical, business, timeline, and resource constraints.

### C1 — Gas Town conventions; gu-gal8 no polecat-owned bookkeeping

- RESPECTED: D4 names both pinned beads as Mayor-owned; the polecat
  *reads* dispatch envelopes and emits NOTES, never writes the state
  bead; security C-SEC-5 adds bead-client-layer enforcement so a
  buggy polecat physically cannot write `<rig>-auto-test-state`; R5
  in the risk register tracks this.
- *Classification:* fully respected; no fix.

### C2 — Refinery vs external-PR mode detection

- VIOLATED (in the strict reading): PRD §Constraints requires "the
  mechanism must detect which mode applies per rig." Q1 (PRD review)
  cut external-PR mode from v1, so the plan does not implement
  detection. The plan does not, however, **explicitly say** that C2
  is N/A in v1 because the alternative was scoped out — it just
  silently omits the step.
- *Suggested fix (must-fix, scope-clarity):* add a Decisions-Made
  entry that names C2 explicitly, says "v1 hard-codes Refinery mode
  per Q1; v2 owns the detection step + a new `auto_test_pr.merge_mode`
  config key." This makes the constraint auditable rather than
  silently dropped. — **Applied as D2b (item 11).**

### C3 — `mol-pr-feedback-patrol` reuse, not reinvent

- RESPECTED: D3 (new molecule + new polecat-work variant) is
  paired with an *additive* extension to the existing patrol — D3
  text reads "the existing `mol-pr-feedback-patrol` is extended
  additively (not replaced)." Phase 2 task 14 keeps the patrol's
  default behavior intact behind a feature flag and adds an
  early-return label match.
- *Classification:* fully respected; no fix.

### C4 — No interference with normal dispatch; Mayor can deprioritize

- RESPECTED: D13 ("sling priority floor for auto-test beads") puts
  auto-test work in the lowest priority bucket; cadence cap (≤1/week)
  + state-machine CAS prevent multiple in-flight cycles per rig;
  the cycle's first-step exit on `enabled=false` lets Mayor
  deprioritize at any tick.
- *Classification:* fully respected; no fix.

### C5 — Per-rig config for test / coverage / flakiness / lint command

- RESPECTED via Q4 redefinition: PRD review Q4 collapsed the four
  proposed per-rig command keys into a single language allow-list
  (`auto_test_pr.language`). Implementation (Key Components §5)
  reflects this; CLI's `enable --language=go` is the only knob.
- *Classification:* fully respected; the constraint was satisfied
  by the PRD's own clarification. No fix.

### C6 — Single-flip revertibility per rig

- RESPECTED: `gt auto-test-pr disable --rig=<rig>` is the canonical
  single flip; D2a clarifies that disable does NOT cancel in-flight
  work but does prevent the next cycle. Phase 1 revert §"Reverting"
  documents the single-flip path. The cycle's first step is
  `if enabled == false → exit 0`, so flipping the bit is
  immediately effective on the next tick.
- *Classification:* fully respected; no fix.

### Summary

| Class | Count |
|-------|-------|
| RESPECTED | 5 |
| VIOLATED (must-fix, scope-clarity) | 1 |
| UNADDRESSED | 0 |

The single VIOLATED item (C2) is not a behavior bug — the plan does
the right thing for v1. It's a documentation gap: the constraint
needs to be explicitly named as N/A-in-v1 with a v2 owner pointer,
so an auditor checking the plan against the PRD can see why C2 has
no implementation. Hence "must-fix, scope-clarity" — applied as D2b.

---

## Report 2: Non-Goals enforcement

**Reviewer:** chrome (inline)
**PRD:** `.prd-reviews/auto-test-pr/prd-draft.md` (commit 13d14a44)
**Plan:** `.designs/auto-test-pr/synthesis.md` (post-round-1)

Walk-through of every Non-Goal in the PRD §Non-Goals section,
checking the plan for scope creep — tasks that go beyond what the
PRD calls for, or that fall under non-goal territory.

### NG1 — Not coverage-percentage chasing

- CLEAN: targeting algorithm in cycle step 4 is `(churn ×
  uncovered_branches)`. The MR banner shows coverage delta as
  *evidence* the test exercised something new, not as a targeting
  goal. No section of the plan optimizes for "raise coverage to
  X%."
- *Classification:* clean; no fix.

### NG2 — Not integration / e2e / load test generation; unit tests only

- SCOPE-CREEP (must-fix): gate 4f rejects files under `integration/`,
  `e2e/`, `test/` directories and tests with `//go:build integration`
  build tags. **But it does not check test-function form.** A polecat
  could write `func BenchmarkFoo(b *testing.B)` or `func FuzzBar(f
  *testing.F)` or `func ExampleX()` inside a same-package
  `*_test.go` file — that file would pass gate 4f's directory and
  build-tag checks. Benchmarks are load-tests-of-a-sort and are
  explicitly out of scope ("Not integration / e2e / load test
  generation"). Examples are documentation, not unit tests. Fuzzers
  are fuzzing infrastructure, also not unit tests.
- *Suggested fix (must-fix):* extend gate 4f to require every
  newly-added top-level test function to match `func Test*(t
  *testing.T)`. Document the forbid-list in the conventions sheet
  template. — **Applied as item 12; risk register R20.**

### NG3 — Not mutation testing infrastructure

- CLEAN: synthetic-mutant is bounded (≤5 per test per D11),
  AST-aware, runs in tmpdir, and is used as a *gate* on the polecat's
  new tests — not as a continuously-run pipeline that produces
  mutation-survival reports. There is no Mayor patrol that aggregates
  mutation results; there is no rig-wide mutation dashboard. The
  feature is a sanity check, not an infrastructure. T3 and D6 keep
  scope narrow.
- *Classification:* clean; no fix.

### NG4 — Not a code-fixing tool

- CLEAN: bug-discovery NOTES protocol (round-1 fix) explicitly says
  "the polecat does NOT push a fix and does NOT open a test-only MR
  for the buggy area." T4 ("Test-files-only allow-list") goes further:
  v1 disallows source changes entirely. Gate 4f enforces this
  structurally. If a polecat surfaces a bug, Mayor's cycle-close
  handler files a separate P2 bug bead — a deliberate hand-off, not
  a coupled fix-with-test PR.
- *Classification:* clean; no fix.

### NG5 — Not retroactive coverage cleanup; greenfield only

- BORDERLINE → SCOPE-CREEP (must-fix): cycle step 4 uses 30-day
  churn to *select files*, which honors NG5 at the file level. But
  **within** a selected file, the plan treats all uncovered branches
  equally. Concrete failure mode: a churning file with one new
  function (5 uncovered branches) and 50 untouched legacy branches.
  Ranked by `(churn × uncovered_branches)` at the file level the
  file is high-priority; the polecat dispatch envelope contains
  uncovered_branches indiscriminately, so the polecat may write tests
  for the 50 legacy branches — a de-facto retroactive cleanup of
  that file. PRD explicitly says "pick targets forward, don't try
  to backfill the whole rig" — within-file backfilling is the
  same anti-pattern at smaller scale.
- *Suggested fix (must-fix):* in cycle step 4, after file selection,
  rank `uncovered_branches[]` by line-distance to recent-churn line
  ranges (from `git log -L` / `git blame` over the 30-day window).
  Document in the conventions sheet template that the polecat should
  prefer churn-adjacent branches. — **Applied as item 13; risk
  register R21.**

### NG6 — Not language-agnostic on day 1

- CLEAN: Q4 language allow-list pins v1 to Go; CLI rejects unknown
  languages at `enable` time with a static error pointing to the v2
  follow-up bead. Phase 0/1 are explicitly Go-only. Phase 3
  (deferred) is where second-language opt-in lands.
- *Classification:* clean; no fix.

### NG7 — Not auto-merge; human review required

- CLEAN: D15 + R16 (round-1 fix) wire the maintainer-approval gate
  into Refinery's merge handler. Default-true on opted-in rigs;
  cannot merge without `approved-by:<user>` label when the MR
  carries `gt:auto-test-pr`. PRD G1's "not auto-merged" half is
  explicitly enforced.
- *Classification:* clean; no fix.

### Summary

| Class | Count |
|-------|-------|
| CLEAN | 5 |
| SCOPE-CREEP (must-fix) | 1 (NG2) |
| BORDERLINE → SCOPE-CREEP (must-fix) | 1 (NG5) |

Both must-fix items are *gate-tightenings* — closing structural holes
where a clever or unlucky polecat could produce output that violates
a non-goal despite passing every existing gate. Both are applied as
items 12 (NG2) and 13 (NG5).

---

## Total fix count

- 3 must-fix items (11, 12, 13)
- 0 should-fix items

All 3 applied to `.designs/auto-test-pr/synthesis.md` in this round.

## Sources

- [PRD draft](.prd-reviews/auto-test-pr/prd-draft.md) — commit 13d14a44
- [Plan synthesis](.designs/auto-test-pr/synthesis.md) — post round 1
- [Round 1 alignment](.plan-reviews/auto-test-pr/prd-align-round-1.md)
- [Bead gu-wfs-et5vw](bd show gu-wfs-et5vw) — assignment

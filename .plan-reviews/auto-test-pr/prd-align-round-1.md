# PRD Alignment Round 1 — auto-test-pr

> Round 1 covers PRD requirements + goals. Round 2 (gu-wfs-et5vw) will
> cover constraints + non-goals.

**Bead:** gu-wfs-ovn44
**PRD:** `.prd-reviews/auto-test-pr/prd-draft.md` (commit 13d14a44)
**Plan:** `.designs/auto-test-pr/synthesis.md` (commit f5b907bf)
**Reviewer:** guzzle (inline; polecats can't sling, so both reviewer
roles were performed by guzzle in one session)

## Reports

Both review reports are inlined below for reproducibility.

- **Requirements coverage report** — see report 1 below.
- **Goals alignment report** — see report 2 below.

## Consolidated must-fix list (applied to design doc)

The two reports converge on a small set of issues. Items applied directly
to `.designs/auto-test-pr/synthesis.md`:

1. **G1 / Q1 — explicit human-review checkpoint.**
   *PRD:* "land net-new tests autonomously, gated by ordinary human PR
   review (not auto-merged)." *Plan gap:* Refinery is unmodified, so
   nothing forces a human approval before merge.
   *Fix:* added **D15** ("Auto-test MRs require explicit maintainer
   approval before Refinery merges") + new Phase 0 task to wire
   `auto_test_pr.require_review_approval=true` (default-true on opted-in
   rigs) into Refinery's merge gate, label-keyed by `gt:auto-test-pr`.

2. **G4 — revision-routing fallback in Phase 1.**
   *PRD:* "feedback-driven revision on the same PR." *Plan gap:* Phase 2
   carries this; in Phase 1 a comment that needs revision has no
   automated path.
   *Fix:* added Phase 1 step 12a (manual revision CLI):
   `gt auto-test-pr revise --mr=<id> [--comment-id=<id>]` lets a
   maintainer trigger the revision polecat against a specific comment
   thread. Documents that G4 isn't *autonomously* validated until Phase 2
   exit, but a manual fallback exists during pilot weeks.

3. **G6 — tighten quality gates 4a + 4d.**
   *PRD:* "non-tautological + branch-exercising." *Plan gap:* tautology
   linter (4d) heuristic only catches a narrow set; coverage delta (4a)
   is "delta > 0" which a marker comment satisfies.
   *Fix:* tightened gate 4d spec (4 sub-rules covering literal-vs-literal,
   NotNil-only, no-input-derived assertion) and gate 4a (branch coverage
   delta > 0 using cover-tool branch mode, not line delta).

4. **Q6 — SEV-1 incident-response path.**
   *PRD:* "SEV-1: auto-test PR breaks main CI on any rig (revert
   immediately, pause that rig 7d, notify Overseer)." *Plan gap:* no
   mechanism to detect or revert.
   *Fix:* added **R15** in risk register + Phase 0 task: "Mayor subscribes
   to main-CI-break events. If failing commit's MR-bead carries
   `gt:auto-test-pr` label, Mayor auto-files a revert MR + transitions
   state bead to circuit-breaker pause + nudges Overseer with SEV-1
   payload."

5. **Branch GC patrol — task missing.**
   *PRD:* "Branch GC for stale `auto-test/<rig>/...` branches with no PR
   after 7 days. v1 MUST." *Plan gap:* mentioned in lifecycle table but
   no implementing task.
   *Fix:* added Phase 0 task: "Land `mol-auto-test-pr-branch-gc` patrol
   that lists `refs/heads/auto-test/*/*` branches and deletes those >7d
   old with no associated open MR or in-flight bead."

6. **Pilot success criteria mismatch.**
   *PRD:* "≥60% merge rate over first 5 PRs; zero SEV-1/SEV-2; rejection
   rate <40% over weeks 2-6." *Plan:* "≥2 consecutive merged MRs no
   intervention; zero SEV; <40% over observation window."
   *Fix:* rewrote Phase 1 exit criteria to adopt PRD criteria verbatim
   (≥60% over 5 MRs; weeks 2-6 / 5-week window) and kept the plan's
   stricter "≥2 consecutive non-intervention" as a sub-criterion for
   graduation to Phase 2.

## Consolidated should-fix list (applied to design doc)

7. **Non-Goals — forbid integration / e2e tests** (boundary on
   "Not integration / e2e / load test generation").
   *Fix:* expanded gate 4f (or new 4g) to reject test files under
   `integration/`, `e2e/`, `test/` (vs. same-package `*_test.go`), and
   tests with build tag `//go:build integration`. Documented in
   conventions sheet template.

8. **Non-Goals — bug-discovery NOTES protocol** (boundary on "Not a
   code-fixing tool").
   *Fix:* added a step to `mol-polecat-work-test-improver`: if a
   candidate test fails on current `main` before any modification (i.e.
   the polecat appears to have surfaced a real bug while iterating),
   exit with a structured NOTES section flagging the suspected bug;
   Mayor's cycle-close handler files a P2 bead from those NOTES.

9. **S4 — per-target-file cooldown after rejection.**
   *Fix:* Mayor's target-pick step (Mayor cycle molecule §Key
   Components) skips files in `rejection_log[].target_path` within a
   21-day per-file cooldown.

10. **S6 — in-flight-MR semantics on disable.**
    *Fix:* added clarification to D2 (or new D2a): disable does NOT
    cancel in-flight work. State bead is left as-is; cycle step 1
    exits on next tick. In-flight MR completes (merged or closed by
    human) and Mayor observes state transitions normally.

## Items that turned out to already be covered (no fix needed)

(None — every PARTIAL/MISALIGNED in the two reports turned into a fix
above. No false alarms.)

---

## Report 1: Requirements coverage

**Reviewer:** guzzle (inline)
**PRD:** `.prd-reviews/auto-test-pr/prd-draft.md` (commit 13d14a44)
**Plan:** `.designs/auto-test-pr/synthesis.md` (commit f5b907bf)

Walk-through of every requirement in the PRD: Problem Statement, Goals
(G1-G7), Non-Goals, User Stories (S1-S6), Constraints, Q1-Q7
clarifications, "Promoted to v1 MUST" section, OQ items resolved.

### Goals

- COVERED: G1 (autonomous net-new tests, gated by review, not auto-merged)
  → Plan §Overview + Phase 1 step 12 + R3. *See PARTIAL note below — "human
  review" semantics in Refinery-only v1 are not explicit.*
- COVERED: G2 (per-rig opt-in, single-flip, default OFF) → D2 + Phase 0 #1 +
  CLI `enable/disable`.
- COVERED: G3 (≤1 open auto-test PR per rig) → D4 state machine + CAS (Q7).
- COVERED: G4 (feedback-driven revision on same PR, never close+reopen)
  → Phase 2 + D3 (`mol-pr-feedback-patrol` label-keyed dispatch).
- COVERED: G5 (≤200 LOC, ≤3 files, no non-test) → dispatch envelope
  `size_budget` + D7 allow-list verifier + T4.
- COVERED: G6 (quality floor) → five quality gates 4a-4e.
- COVERED: G7 (gu-gal8) → two pinned beads, Mayor-owned; R5 + C-SEC-5.

### Non-Goals (boundary check)

- COVERED: not coverage-percentage tool → ranks by churn × uncovered.
- PARTIAL (should-fix): not integration/e2e — `**/*_test.go` covers BOTH
  unit and integration. **Fix:** forbid integration tests via gate +
  conventions sheet (item 7 above).
- COVERED: not mutation infrastructure → uses synthetic-mutant only as gate.
- PARTIAL (should-fix): not code-fixing tool — no bug-bead-on-discovery
  affordance. **Fix:** bug-discovery NOTES protocol (item 8).
- COVERED: not retroactive-coverage cleanup → 30d churn window.
- COVERED: not language-agnostic on day 1 → Q4 + Phase 0/1 Go-only.
- PARTIAL (must-fix): not auto-merge — no explicit human-review checkpoint.
  **Fix:** D15 (item 1).

### User Stories

- COVERED: S1 (steady drip).
- COVERED: S2 (coalesce when PR open).
- COVERED: S3 (revision on same branch).
- PARTIAL (should-fix): S4 (per-file cooldown). **Fix:** item 9.
- COVERED: S5 (broken test → no PR).
- PARTIAL (should-fix): S6 (in-flight-MR semantics on disable).
  **Fix:** item 10.

### Constraints

All COVERED (gu-gal8, Refinery interaction, mol-pr-feedback-patrol reuse,
no dispatch flooding, language plurality, single-flip revertibility).

### Q1-Q7 clarifications

- COVERED: Q1, Q2, Q3, Q4, Q5, Q7.
- PARTIAL (must-fix): Q6 — SEV-1 path missing. **Fix:** item 4.

### Promoted-to-MUST items

- COVERED: gitleaks, code marker, status command, MVP definition.
- PARTIAL (must-fix): branch GC patrol task missing. **Fix:** item 5.
- PARTIAL (must-fix): pilot success criteria mismatch. **Fix:** item 6.

### OQs Resolved

- COVERED: OQ7 (rate limit), OQ10 (flakiness), OQ11 (PR author/banner).

### Summary

| Class | Count |
|-------|-------|
| COVERED | 23 |
| PARTIAL must-fix | 4 |
| PARTIAL should-fix | 4 |
| GAP | 0 |

---

## Report 2: Goals alignment

**Reviewer:** guzzle (inline)
**PRD:** `.prd-reviews/auto-test-pr/prd-draft.md` (commit 13d14a44)
**Plan:** `.designs/auto-test-pr/synthesis.md` (commit f5b907bf)

Walk-through of every PRD goal: G1-G7, S1-S6, and Pilot Success Criteria.

### Numbered Goals

- ALIGNED: G2, G3, G5, G7.
- PARTIAL (must-fix): G1 — no human-review checkpoint. **Fix:** item 1.
- PARTIAL (must-fix): G4 — revision routing not in Phase 1; pilot can't
  hit comment-revision case. **Fix:** item 2 (manual `revise` CLI).
- PARTIAL (must-fix): G6 — tautology heuristic too narrow; coverage delta
  too loose (line delta vs branch delta). **Fix:** item 3.

### User Stories

- ALIGNED: S1, S2, S3 (with item 2 fix), S5.
- PARTIAL (should-fix): S4 (per-file rejection cooldown). **Fix:** item 9.
- PARTIAL (should-fix): S6 (disable while in-flight). **Fix:** item 10.

### Pilot Success Criteria

- MISALIGNED (must-fix): metrics differ. **Fix:** item 6.
- ALIGNED: ≥60% merge rate is achievable given the 5 gates (after fix).
- ALIGNED: zero SEV-1/SEV-2 is achievable given gates 4e + sandbox + the
  SEV-1 path added in item 4.

### Summary

| Class | Count |
|-------|-------|
| ALIGNED | 9 |
| PARTIAL must-fix | 3 |
| PARTIAL should-fix | 2 |
| MISALIGNED must-fix | 1 |

---

## Total fix count

- 6 must-fix items (1, 2, 3, 4, 5, 6)
- 4 should-fix items (7, 8, 9, 10)

All 10 applied to `.designs/auto-test-pr/synthesis.md` in this round.

## Sources

- [PRD draft](.prd-reviews/auto-test-pr/prd-draft.md) — commit 13d14a44
- [Plan synthesis](.designs/auto-test-pr/synthesis.md) — commit f5b907bf
- [Bead gu-wfs-ovn44](bd show gu-wfs-ovn44) — assignment

# Scope Analysis

## Summary

The PRD describes a focused, mostly well-bounded mechanism: an opt-in
per-rig automation that opens small, human-reviewed PRs adding unit
tests to under-tested, recently-churned files. The Non-Goals section is
unusually strong (no auto-merge, no integration tests, no coverage-%
chasing, no mutation pipeline, no code fixes, no retroactive cleanup,
single-language pilot), which dramatically reduces drift risk relative
to the "automation that improves a codebase" archetype.

That said, scope is shakier in three places: (1) the boundary with the
existing `mol-pr-feedback-patrol` is described as a reuse but the
hand-off contract is not pinned down, (2) the per-rig config surface
straddles "tiny opt-in flag" and "full multi-language test/coverage/lint
DSL" without committing to a phasing, and (3) the "tautology / low-value
test detection" sub-problem (Open Q9) is a research project nested
inside what is otherwise a plumbing PRD. Each of those is a plausible
failure mode where v1 quietly grows into v2.

## Findings

### Critical Gaps / Questions

- **MVP is not explicitly defined.** Goals 1–7 read as a single
  unified bar; there is no statement of "the smallest version that
  delivers value." Without that, every Open Question is implicitly
  promoted to a v1 requirement.
  - Why this matters: the PRD spans ~6 substantively different
    sub-systems (cadence/dispatch, target selection, size enforcement,
    feedback handoff, tautology detection, rate limiting, per-rig
    config). Building all of them before any land risks classic
    big-bang failure.
  - Suggested clarifying question: *"What is the smallest end-to-end
    slice — for one rig, in one language, with one cadence, with the
    crudest possible target picker — that we'd ship and learn from?
    Can we name what's explicitly cut from v1?"*

- **Hand-off contract with `mol-pr-feedback-patrol` is unspecified.**
  The PRD says the new mechanism only handles PR creation and that
  feedback/CI revision is the existing patrol's job, "keyed off the
  `gt:auto-test-pr` label." But there is no description of what state
  the patrol must read, what variant of `mol-polecat-work` it
  dispatches for revisions, or who owns the rate-limit / rejection
  signals if the patrol decides to abandon a PR.
  - Why this matters: this is the single biggest place the project
    will silently grow. "Just reuse the existing patrol" is doing
    enormous work in this PRD. If the patrol can't do it as-is, this
    PRD has just absorbed patrol changes too.
  - Suggested clarifying question: *"Has someone confirmed that
    `mol-pr-feedback-patrol` can drive auto-test PRs today without
    modification? If not, are those modifications in scope for this
    project or filed as a separate dependency?"*

- **"PR is small enough to review in one sitting" is stated but not
  enforced.** Goal 5 lists ≤200 added test LOC and ≤3 files. Open Q2
  acknowledges it's undecided whether the polecat enforces this or a
  post-check rejects oversized candidates. There is no spec for what
  happens when targets cannot fit (bigger file, etc.) — split the
  cycle? Skip the target?
  - Why this matters: size is the main mechanism keeping reviewer
    fatigue acceptable. Without an enforcement strategy this is a
    promise the PRD can't keep, and the first oversized PR will get
    closed by an annoyed maintainer (which then trips the rejection
    cooldown — interaction effect).
  - Suggested clarifying question: *"What does the cycle do when no
    candidate fits in the size budget — skip the cycle, escalate, or
    relax the budget? And is the budget hard-coded or per-rig?"*

- **"Quality floor" is the riskiest unscoped sub-problem.** Goal 6
  ("not flaky, not tautology, exercises real branches") is recapped in
  Open Q9 as "hardest problem." The proposed defenses (heuristic
  linter, line-comment mutation, diff-marker comments) are reasonable
  but each is its own implementation effort, and none is part of the
  Rough Approach in concrete deliverable form.
  - Why this matters: if the quality floor is weak, the mechanism
    earns a reputation as "noise PRs from the bot" within a week and
    gets disabled rig-by-rig. If the quality floor is strong, it's a
    project of its own. Right now it's both undersized and
    underspecified.
  - Suggested clarifying question: *"What's the minimum quality gate
    for v1 — purely 'tests pass + flakiness re-run + no `assert(true)`
    pattern' — and is mutation-sanity / branch-coverage proof a
    deliberate v2 ask? If so let's say so."*

- **Per-rig config surface is uncommitted.** Open Q13 acknowledges the
  config doesn't yet have a clear home in `gt`. The PRD lists at least
  five config keys (enabled, cadence, test_cmd, coverage_cmd,
  lint_cmd) and Constraints implies more (flakiness re-run command).
  - Why this matters: the design touches a yet-undecided gt subsystem
    (rig-level config). That's a cross-team / cross-component
    dependency that isn't explicitly called out as a blocker.
  - Suggested clarifying question: *"Is rig-level config in gt
    (today) sufficient to host these keys, or is part of this work
    actually 'design and ship rig-level config as a feature'? If the
    latter, that may want to be a separate sibling project."*

### Important Considerations

- **"Per-rig opt-in" plus "pilot rig only" implies dead config knobs
  in v1.** The PRD wants `auto_test_pr.cadence` per rig, plus
  per-language test/lint/coverage commands, but Non-Goals say v1 is
  one rig in one language. Pinning the config schema before exercising
  it on multiple rigs/languages risks freezing a shape that won't
  generalize. Counter-suggestion: ship the bare minimum schema
  (`enabled` only, hardcode the rest for the pilot rig) and explicitly
  defer the schema design to a follow-up bead after pilot data.

- **"Twice a week" cadence is presented as an example, not a
  requirement.** S1 ("twice a week the mechanism wakes up") is
  illustrative. The PRD does not commit to a default cadence. This is
  fine, but the rate-limit logic in Open Q7 (24h soft cooldown,
  3-rejections-in-a-row → 7-day pause) implicitly assumes a cadence
  faster than weekly. If the default is "weekly," the cooldown
  numbers are wrong. Worth picking *one* default cadence in the PRD.

- **"On-rejection cooldown" interacts with target selection.** Open Q4
  proposes a per-file cooldown after a maintainer says "this file is
  intentionally untested," but the persistent storage for that is
  filed as Mayor-owned pinned bead state (Open Q12). That's fine, but
  the PRD doesn't say whether the cooldown is per-file or per-rig.
  Per-rig is overly punitive (one bad target should not stop all
  testing); per-file is the right shape but adds a per-file ledger to
  the Mayor's pinned bead.
  - Suggested resolution: state explicitly that rejection cooldown is
    per-file and document the data shape on the pinned bead.

- **External-repo path is mentioned but not designed.** Constraints
  notes that some rigs use `gh pr create` directly (external/community
  repos) and others use Refinery. The Rough Approach only describes
  the Refinery path. The external path differs in important ways
  (no merge queue → no Refinery-driven feedback signals → fully
  reliant on `mol-pr-feedback-patrol` to know when CI has gone red).
  Could quietly become 30%+ extra work.
  - Suggested resolution: if the pilot is `gastown_upstream`, declare
    external-repo support an explicit v2 and remove from v1 scope.

- **gu-gal8 honoring is asserted, not enforced.** Goal 7 and Open Q12
  both reference gu-gal8 (no polecat-owned bookkeeping beads). The
  Rough Approach says "Mayor files the polecat bead" and "Mayor owns
  the pinned state bead." Good. But there is no architectural check
  in the PRD that prevents a future change from sliding into
  polecat-creates-its-own-followup. Worth a one-line invariant:
  *"polecat work bead is the ONLY bead created per cycle, and it is
  filed by Mayor, not by the polecat."*

- **"Reuse vs reinvent feedback handling" creates a coordination
  point.** The PRD says reuse `mol-pr-feedback-patrol`, but this
  proposed mechanism may want a different revision behavior (e.g.,
  "if the maintainer asks for a different test approach, regenerate
  rather than tweak"). The PRD doesn't say whether `mol-pr-
  feedback-patrol` already supports per-formula revision logic or
  whether that's a fork point. If the latter, this is a hidden
  cross-project dependency.

### Observations

- The Non-Goals section is unusually strong and is doing most of the
  scope-control work. Keep it visible in any future revision — it's
  an asset, not boilerplate.

- Stories S1–S6 cover the happy path, coalesce, comment-driven
  revision, rejection, broken-build, and opt-out. Two scenarios feel
  missing and would tighten scope:
  - S7 — *flake discovered post-merge.* What happens if a test we
    landed turns out to be flaky two weeks later? Do we file it for
    cleanup? Does the cooldown re-engage on the file?
  - S8 — *no candidates this cycle.* What if every recently-churned
    file is already well-tested or above the size budget? The
    mechanism quietly exits, presumably, but worth saying so.

- "PR author / attribution" (Open Q11) is a small-but-real
  social-engineering question for external repos: opening PRs from a
  bot identity to a human-maintained external repo has very different
  optics than internal Refinery merges. Worth flagging that the
  external-repo path may need a different attribution model — and
  another reason to defer external-repo support to v2.

- The Rough Approach maps cleanly onto five legs (`gate`,
  `target-pick`, `dispatch`, `polecat-work`, feedback-handoff,
  closure). That's six, actually — closure is a separate
  observability/state-update step. Worth explicitly numbering the
  legs so the molecule structure is unambiguous when implementation
  starts.

- Pilot rig choice (`gastown_upstream`) is reasonable: Go, healthy
  test infra, the maintainer is the overseer, and the PRD's review
  convoy itself uses this rig. One observation: "pilot on the rig
  designing the feature" is fine for v1 but produces selection bias
  for the v2 generalization step. Worth picking a non-Go rig as the
  *first generalization target* in the same PRD, even if just as a
  named follow-up.

- The Rough Approach's sentence "We may need to teach it [feedback
  patrol] to honor the `gt:auto-test-pr` label by dispatching to the
  same polecat-work-test-improver formula" is the load-bearing weasel
  word in the document. "May need to teach" is shorthand for "this
  could be a small or large amount of work and we haven't checked."
  Convert that to a concrete decision before implementation starts.

## Confidence Assessment

**Medium-high.** The scope is unusually disciplined for a PRD that
spans automation, agents, and human review. Non-Goals are explicit and
well-chosen. The main risks are:

1. The implicit reliance on `mol-pr-feedback-patrol` being capable
   as-is (could absorb a second project).
2. The quality-floor sub-problem being underscoped relative to its
   importance for adoption.
3. The per-rig config surface being committed before pilot data.

None of these is a fatal scope flaw, but each is a credible path by
which v1 quietly becomes v2. The MVP question is the single most
useful clarification to add — once "v1 is just X" is committed in the
PRD, most of the other ambiguities resolve mechanically.

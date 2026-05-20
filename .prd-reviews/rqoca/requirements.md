# Requirements Completeness

## Summary

The PRD has a clear problem statement, sensible non-goals, and good
narrative coverage of the happy path through user stories S1–S6. The
"Rough Approach" is detailed enough that a reader can imagine the
moving parts. However, the document is structured more as a *design
sketch* than as a buildable spec: most of the actionable detail lives
in the "Open Questions" section as proposals, not as decisions.
Until those proposals are accepted or rejected, an implementer cannot
write tests against this PRD — for almost every behaviour, two or
three plausible implementations would be defensible.

The biggest gaps are around **measurable success criteria**, **explicit
acceptance / done conditions**, **failure-mode definitions** (especially
around the quality floor for generated tests), and **rollback /
observability**. These are not nice-to-haves: the entire mechanism is
"a bot opens code-modifying PRs against your main branch on a cadence,"
which is exactly the kind of system whose failure modes need to be
specified before the first line is written.

## Findings

### Critical Gaps / Questions

- **No measurable success criteria for the mechanism itself.**
  - The Goals list outcomes ("land tests autonomously", "at most one
    open auto-test PR per rig") but no thresholds: what merge rate
    counts as success vs. failure? What rejection rate triggers a
    re-think rather than a per-rig backoff? Over what window?
  - Why this matters: without a target, you cannot tell whether the
    pilot on `gastown_upstream` (Open Question 14) is succeeding or
    quietly producing slop. "Maintainer reviewed and merged within a
    day" appears in S1 as colour, not as a measured target.
  - Suggested clarifying question: *What ≥X% merge rate over Y PRs
    over Z weeks counts as "the pilot worked"? Below what merge or
    rejection rate do we disable the mechanism for that rig?*

- **"Quality floor" (Goal 6) is asserted but not made testable.**
  - Goal 6 names four properties (pass, not flaky, not tautological,
    not behaviour-freezing) but only flakiness has a concrete check
    (Open Question 10: N=10 reruns). Tautology detection is described
    only as a "proposed layered defense" in Open Question 9, with the
    hardest sub-bullet ("not just freeze current behavior") left
    completely unspecified. Goal 6 is therefore aspirational, not
    acceptance criteria.
  - Why this matters: a low quality floor here turns the mechanism
    into a tech-debt pump. The PRD acknowledges this is the "hardest
    problem" but defers it to implementation choice.
  - Suggested clarifying question: *What is the v1 definition of
    "behaviour-freezing" we will detect and reject? If we cannot
    detect it cheaply, do we accept that risk and let humans catch it
    in review, or do we block on it?*

- **No definition of "done" for v1 / pilot.**
  - The PRD mentions a pilot on `gastown_upstream` but does not say
    what shipping the pilot looks like. Is "done" = first auto-test
    PR is merged? = N consecutive merges without rejection? = the
    cycle has run for two weeks without operator intervention?
  - Why this matters: with no "done" line, the project is open-ended,
    and any reviewer can claim the implementation is not finished.
  - Suggested clarifying question: *Define the exit criteria for the
    pilot: what observation closes the project and unlocks rollout to
    other rigs?*

- **Open Questions are not resolved before build.**
  - 14 questions are listed, each with a "Proposed:" answer, but no
    indication of which proposals are accepted as decisions vs. still
    open. At least 7 of them (lifecycle, PR-size cap enforcement,
    target selection, existing-PR detection authority, state
    authority, feedback handling reuse, rate-limiting numbers) gate
    fundamental architecture choices. An implementer cannot proceed
    without these resolved.
  - Why this matters: this is the difference between a PRD and a
    design sketch. Without resolution, two parallel implementers
    would build incompatible systems.
  - Suggested clarifying question: *Before build, can the human
    explicitly accept/reject each proposed answer in Open Questions
    1–14, so they become Decisions rather than Proposals?*

- **No rollback / kill-switch beyond per-rig opt-out.**
  - Goal 2 / S6 says "single config flip per rig" disables the
    mechanism. Constraint says "in-flight PR is left alone." But
    there is no global kill-switch ("disable across all rigs
    immediately"), no rollback for a bad batch (e.g., the mechanism
    just opened 14 PRs of slop because the tautology linter
    regressed), and no plan for what happens if the
    `gt:auto-test-pr` label disappears or PR detection breaks
    (Open Question 4) and the mechanism opens a duplicate.
  - Why this matters: this mechanism *modifies main branches* of
    every rig that opts in. The blast radius if it misbehaves is
    bigger than the per-rig opt-out implies.
  - Suggested clarifying question: *What is the global kill-switch
    (Mayor-level), and what is the recovery procedure if existing-PR
    detection fails and the mechanism creates duplicates?*

- **Failure modes / error states are largely undefined.**
  - The PRD covers four "negative" scenarios (S4 reject, S5 broken
    test, polecat-side discard, S6 opt-out) but does not enumerate:
    - What happens if `gh pr create` fails / is rate-limited by
      GitHub?
    - What happens if the Mayor cannot reach the rig (no idle
      polecat, witness down)?
    - What happens if the rig's test command times out or hangs?
    - What happens if the coverage command requires credentials the
      polecat doesn't have?
    - What happens if the rig's main branch has changed during the
      polecat's run (rebase race)?
    - What happens if two cycles fire concurrently due to a Mayor
      crash + restart?
  - Why this matters: the system spans Mayor → polecat → GitHub → MQ.
    Each interface is a failure point, and "mechanism quietly exits"
    (S2) is the only documented response.
  - Suggested clarifying question: *Provide a short failure-mode
    table: for each external dependency (GitHub API, rig polecat
    pool, test command, coverage command, Refinery MQ), what is the
    detection signal and what is the response?*

- **No acceptance test plan / "how would QA verify this".**
  - There is no list of cases a tester or reviewer should run before
    declaring the mechanism healthy. The user stories are scenarios,
    not assertions: "twice a week the mechanism wakes up" is not
    something we can run, observe, and bless.
  - Why this matters: test-from-PRD is the primary completeness
    check requested in this review.
  - Suggested clarifying question: *Add an "Acceptance Tests"
    section listing concrete, observable assertions (e.g., "Given
    a rig with `enabled=false`, the cycle does not modify the rig's
    GitHub state. Verify by …").*

### Important Considerations

- **Non-functional requirements are absent.**
  - No latency budget for the cycle (how long can `target-pick` take
    before we abort?), no throughput constraint (what if 30 rigs
    enable this — does Mayor's daemon serialize them?), no resource
    cap (a coverage run on a large rig can take 10+ minutes and
    burn CPU). These should at least be acknowledged.

- **Observability is mentioned only implicitly.**
  - The "Extension points" mentions a Mayor-level dashboard, but it
    is deferred. For v1 the PRD does not say what gets logged, what
    metrics the Mayor emits ("auto-test-pr cycles run today",
    "auto-test PRs opened, merged, rejected, abandoned"), or how an
    operator inspects state when something looks wrong. Without
    this, the rejection-cooldown logic in Open Question 7 is hard to
    verify in production.

- **The "no non-test source changes unless absolutely required"
  carve-out (Goal 5) is undefined.**
  - "Absolutely required" is a judgement call. Does the polecat
    refuse and abandon? Does it ask the Mayor? Does it allow
    formatting / import-only edits but not logic edits? This is the
    difference between a tightly-scoped tool and a slowly-creeping
    one.

- **Coverage thresholds and "rig threshold" are not defined.**
  - Open Question 3 references "below rig threshold" without
    specifying who sets it, where it lives, or what the default is.
    A rig with no threshold configured — does the mechanism refuse
    to run, or use a sensible default (e.g., 80% line coverage)?

- **Reuse of `mol-pr-feedback-patrol` is described as a proposal,
  but the PRD also names it as a constraint.**
  - Constraints section says "we should reuse it rather than
    reinvent feedback handling." Open Question 6 says the same as
    a proposal. This conflation makes it unclear whether reuse is
    a hard requirement or a preference. If
    `mol-pr-feedback-patrol` doesn't already exist or isn't generic
    enough, the build cost may be in the patrol, not in the new
    mechanism.

- **The "hard cap of one open auto-test PR per rig" is
  under-specified for race conditions.**
  - Two cycles firing within seconds (Mayor restart, daemon hiccup)
    could both observe "no PR open" and both create one. The PRD
    doesn't say whether the existing-PR check is read-modify-write
    safe (e.g., GitHub API + advisory lock) or best-effort.

- **Attribution / governance for the bot identity (Open Question 11)
  has security implications.**
  - "PR opened under the polecat's rig identity" — does the polecat
    have its own commit identity / GPG key / GitHub account, or does
    it use a shared service account? If shared, what are the audit
    trails when something goes wrong?

- **Per-rig config schema is sketched but not specified.**
  - Open Question 13 lists fields but does not commit to a file
    location, format, schema validation, or migration path. New
    fields (e.g., `auto_test_pr.coverage_threshold`) will need to
    land somewhere.

### Observations

- The structure is excellent: Problem → Goals → Non-Goals → Stories →
  Constraints → Open Questions → Approach. Easy to navigate.
- "User Stories" are vivid and concrete enough to anchor disagreement,
  which is the right job for stories at this stage.
- The PRD correctly cites prior art (gu-gal8) and respects it as a
  hard constraint, not a soft preference.
- "Greenfield only — pick targets forward, don't try to backfill" in
  Non-Goals is a good piece of self-discipline that will save a lot
  of scope-creep arguments later.
- The deliberate framing of cross-language support as a follow-up
  (Non-Goal #6) is appropriate for v1.
- Consider adding a glossary or "Cast of characters" — the PRD names
  Mayor, polecat, witness, Refinery, MQ, slings, beads, and three
  patrol molecules. A reviewer outside Gas Town will struggle.
- The "Rough Approach" leg names (`gate`, `target-pick`, `dispatch`,
  `polecat-work`, `handoff`, `closure`) read like a molecule
  formula — worth promoting from prose to a labelled state diagram
  before build, even at low fidelity, so the dispatch ownership
  (Mayor vs. polecat) is unambiguous.

## Confidence Assessment

**Medium-low.** The PRD is sufficient for a design conversation but
not yet sufficient to build from. A buildable version needs:

1. Open Questions 1–14 promoted from "Proposed" to "Decided".
2. A measurable success / done definition for the pilot.
3. A v1 definition of the test-quality floor (Goal 6) with a
   testable specification — even if the specification is "we accept
   we cannot detect behaviour-freezing tests in v1 and rely on human
   review", that is itself a decision.
4. A failure-mode table covering each external dependency and the
   recovery / kill-switch story.
5. An "Acceptance Tests" section so QA / reviewers have something to
   check against.

Without (1)–(5), an implementer making good-faith choices could
deliver something the PRD's author did not intend, and a QA engineer
would have nothing to test against beyond the happy-path stories.

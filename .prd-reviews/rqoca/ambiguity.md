# Ambiguity Analysis

## Summary

The PRD for **Auto-Test-PR** is reasonably structured and the author has
already surfaced many open questions in §Open Questions, which lowers the
ambiguity surface in the obvious places. However, several requirements
read clearly on first pass but contain latent disagreements that two
engineers would implement differently. The biggest classes of ambiguity
are: (a) goal/scope verbs ("land", "tests with marginal value", "small
enough to review") that look concrete but lack measurable definitions;
(b) ownership/lifecycle language that conflates "Mayor" with "the
mechanism" and is unclear about which agent type is the *authoritative*
actor for each cycle phase; and (c) failure-mode language ("retries
quietly", "abandons the cycle quietly") that does not specify what state
must be persisted, who observes the failure, or how the cooldown clock
is started.

A second-order concern: the PRD mixes prescriptive proposals
("Proposed: ...") with normative requirements (Goals, Constraints) in
the same prose register. A reader cannot easily tell which sentences
are decisions vs. defaults vs. starting suggestions, which will cause
PR-review debate.

## Findings

### Critical Gaps / Questions

#### 1. "Land" is defined inconsistently across internal vs. external rigs

- **Finding.** Goal 1 says "Definition of 'land': merged via Refinery
  (internal repos) or via maintainer merge (external repos)." Constraints
  later say "for repos where we have direct push, we go through gt done
  → Refinery. For external/community repos we use `gh pr create`
  directly." But §Goals also says "**At most one open auto-test PR per
  rig at any time.**" For external repos, the Refinery is not in the
  loop at all — what does "PR" even mean for an internal-only Refinery
  rig where work normally goes through the merge queue without a GitHub
  PR?
- **Why this matters.** Two engineers will disagree on whether `gastown_upstream`
  (the pilot rig) actually opens a *GitHub PR* or just submits an MR
  through Refinery. Goal 1 implies merged-via-Refinery counts as
  "landing", but §S1 and the existing-PR-detection mechanism (label,
  branch prefix, PR body marker) all assume a GitHub PR exists.
- **Suggested clarifying question.** For Refinery-merged rigs, is the
  artifact a GitHub PR, a Refinery MR-bead, or both? Where does the
  `gt:auto-test-pr` label live for a non-GitHub MR?

#### 2. "Bounded blast radius per PR" mixes hard and soft limits

- **Finding.** Goal 5 reads: "≤200 added test LOC, ≤3 files touched, no
  non-test source changes unless absolutely required." The "**unless
  absolutely required**" escape hatch is not defined. Open Question 2
  asks "enforced by the polecat itself or by a post-check?" — but the
  goal already states the limit as if normative.
- **Why this matters.** A polecat that needs to add a test helper file
  (e.g., a fake clock or a small fixture loader) will have to decide
  alone whether that counts as "absolutely required". Two polecats given
  the same task will make different calls. The 3-file cap further
  ambiguates this — does a new test helper count toward the 3?
- **Suggested clarifying question.** Are the 200 LOC / 3 files / no-source-change
  limits HARD (refuse to push) or SOFT (warn and continue)? What
  exemption process exists for "absolutely required" source changes?

#### 3. "Quality floor" — unverifiable in current form

- **Finding.** Goal 6: "not assert tautologies, and not just freeze
  current behavior without exercising real branches." Open Question 9
  acknowledges this is "the hardest problem" and proposes layered
  defenses, but the *Goal* is stated as if testable. The phrase "freeze
  current behavior" is itself ambiguous — many legitimate
  characterization tests do exactly that as a first step.
- **Why this matters.** Without a checkable definition, every PR review
  will re-litigate "is this tautological?" The PRD does not say what
  the polecat MUST do (run mutation-sanity? run linter? both?) vs. what
  it MAY do.
- **Suggested clarifying question.** Of the three layered defenses in
  Q9 (heuristic linter, mutation-sanity, diff-marker comments), which
  are MUST and which are SHOULD for v1? What's the failure action when
  the linter flags but the polecat believes the test is valuable?

#### 4. "Mayor owns the parent state bead" — but who runs the cycle?

- **Finding.** Constraints say "polecats are transient and
  witness-managed; crew are persistent." Open Question 1 proposes
  "standing patrol, but with a per-rig schedule." Approach §1 says
  "Mayor files a single bead." But Mayor is the *town* coordinator —
  is the Mayor literally the patrol runner, or does Mayor delegate to a
  rig-level crew agent? The PRD never says.
- **Why this matters.** "Mayor-owned" pinned beads are described
  per-rig (`<rig>-auto-test-state`), but Mayor is town-wide. Is there
  one patrol process iterating all rigs, or does each rig get its own
  crew agent that runs the patrol locally? Refinery interaction also
  differs: a rig-local crew can `gt done`; the town-level Mayor can't.
- **Suggested clarifying question.** Concretely: which agent runs
  `mol-auto-test-pr-cycle`? Is it (a) Mayor's daemon iterating rigs,
  (b) per-rig crew agent, or (c) a new singleton crew role
  (e.g. `auto-test-conductor`)? The "Mayor's daemon" wording in the
  Approach §6 suggests (a), but the rig-local `gt done` flow suggests
  (b).

#### 5. "Per-rig opt-in via single config flip" — config schema not specified

- **Finding.** Goal 2: "single config flip; default OFF." Open Question
  13 asks where rig config conventionally lives in gt today. The PRD
  cannot mark the goal as testable until that location is decided.
- **Why this matters.** "Single flip" is itself ambiguous — does the
  flip enable the patrol *and* require the rig to also configure
  test_cmd / coverage_cmd / lint_cmd, or are those auto-detected?
  Without the cmds, the flip is necessary but not sufficient. Two
  engineers will disagree on whether a missing test_cmd is an error,
  a warning, or auto-detected.
- **Suggested clarifying question.** Is `auto_test_pr.enabled = true`
  alone sufficient to start running, or are the per-rig commands
  (test/coverage/lint/flakiness) also required? What's the behavior
  when `enabled=true` but `test_cmd` is unset?

### Important Considerations

#### 6. "Twice a week" / "cadence" is suggestive, not normative

- §S1 says "Twice a week the mechanism wakes up." Open Question 1
  proposes `cadence: "twice-weekly"`. The PRD never says whether
  cadence is a free-form string, an enum, a cron expression, or a
  Duration. Implementer will pick one and a reviewer will object.

#### 7. "Backs off (rate limit), and avoids retargeting that file for some cooldown period"

- §S4 ("Reviewer rejects") and Open Question 7 (rate limiting) overlap
  but use different units: §S4 is per-FILE cooldown, Q7 is per-RIG
  cooldown after 3 closes. Both could exist. Or only one. Two engineers
  will disagree on whether file-level cooldowns are required for v1 or
  deferred.

#### 8. "Comments → new commits on the existing branch. Never close+reopen."

- Goal 4 is clear, but the boundary between "comment that requires a
  revision" vs. "comment that's a question" is not. The existing
  `mol-pr-feedback-patrol` is referenced as the handler — what's its
  rubric for "this comment requires a code change"? The PRD assumes
  that patrol's behavior; if the patrol's rubric is fuzzy, this PRD
  inherits that fuzziness.

#### 9. "Run the test suite N times for flakiness. Discard if non-deterministic."

- Approach §4 and Open Question 10 both say N=10. But:
  - "The test suite" is ambiguous — only the new tests, only the
    affected package, or the full rig suite?
  - "Non-deterministic" — is a single non-deterministic test
    discarded, or does the whole PR get abandoned?
  - Compute cost: 10x full-suite runs per cycle is non-trivial on
    larger rigs and may conflict with the "no interference with normal
    dispatch" constraint.

#### 10. "Polecat reuse: spawn-per-cycle"

- Open Question 8 proposes spawn-per-cycle. But in §S3 (reviewer
  comments), the existing `mol-pr-feedback-patrol` dispatches *another*
  polecat to push a revision commit. That second polecat does not have
  the original's context. The PRD says context is in the bead, but if
  the original polecat learned something subtle about the codebase
  (e.g., "this mock is finicky"), that learning is lost. Acceptable?
  Worth flagging.

#### 11. "Pinned bead is owned by Mayor (NOT polecat) per gu-gal8"

- gu-gal8 is about polecats not *creating their own beads*. But pinned
  state beads are different: a polecat *reading* a Mayor-owned pinned
  bead is fine; *updating* it is the question. The PRD's Approach §1
  says "Update the pinned-bead cache with current state" but doesn't
  say whether the polecat or Mayor performs that update. If polecat
  updates a Mayor-owned bead, gu-gal8's spirit is preserved (no
  self-created beads) but ownership semantics are muddier.

#### 12. "External/community repos use `gh pr create` directly"

- Constraint says this, but the rest of the PRD reads as if Refinery
  is the standard path. Goal 1 says "merged via Refinery (internal
  repos) or via maintainer merge (external repos)" — but this is the
  ONLY mention of external repos in the goals. Are external rigs in
  scope for v1, or is the pilot strictly internal (gastown_upstream)
  per Open Question 14? The PRD pulls in both directions.

### Observations

#### 13. "Should" / "must" / "could" — actually used carefully, but...

The PRD uses normative language correctly in §Goals and §Constraints
("must NOT", "must detect", "must not flood"). Approach §4 mixes
"if all pass: creates" with "may need to teach it to honor the label"
— "may" here is normative-vague. Recommend a sweep to elevate every
"may" / "should" / "we could" to either MUST or explicitly OPTIONAL.

#### 14. "Recently-changed file" — undefined window

§Problem mentions "Recently-changed files" without a window. Approach
§2 specifies "last 30 days." The two should be aligned, and "30 days"
itself is a magic number with no rationale.

#### 15. "Edge case bugs" / "real branches"

Both terms are used in the quality discussion. "Real branches" appears
in Goal 6 ("exercising real branches") and again in Open Question 9.
Without a definition, branch coverage tooling is implied — but only
some rig languages have cheap branch-coverage tooling. Worth defining
or relaxing to "code paths."

#### 16. "Bookkeeping beads must NOT be polecat-owned"

This restates gu-gal8 as a constraint, but gu-gal8 is about
*self-creation*, not ownership. A bead created by Mayor and assigned
to a polecat is "polecat-owned" in the assignee sense but doesn't
violate gu-gal8. The constraint should clarify: "polecat-FILED" vs.
"polecat-ASSIGNED."

#### 17. "Pilot on gastown_upstream itself (Go, well-tested, maintainer is the
overseer)"

The parenthetical introduces a NEW persona — "the overseer" — that
appears nowhere else in the PRD. Is this synonymous with "rig
maintainer" from §"Who it's for"? If so, harmonize.

#### 18. "PR feedback patrol" referenced as if specified

§Constraints and §Approach §5 both reference `mol-pr-feedback-patrol`
as if its behavior is fully specified elsewhere. This PRD should at
minimum link to or summarize the behavior it depends on. Without that,
"reuse" is a load-bearing assumption with no contract.

#### 19. "Diff-marker comments: every new test must reference *which* branch /
behavior / edge case it's exercising in a comment"

This is in tension with most language test-style guides, including
the TALON convention loaded in the agent's own context: "No comments
in test code except brief intent comments." For Go's `_test.go`
convention, comments above test functions are fine; for TS Jest,
test-name strings carry that information. Mandating a comment may
fight the existing rig style.

#### 20. "Mutation-style sanity: comment out one line in the function under
test; if the new test still passes..."

Implementation ambiguity: which line? Random? First non-trivial? All
lines in turn? Open Question 9 says "comment out one line" — singular —
but a single-line mutation is a very weak signal. This needs to be
either expanded to a real (cheap) mutation strategy or downgraded from
"must" to "exploratory."

## Confidence Assessment

**Medium.** The PRD is well-organized and the author has pre-surfaced
many of the obvious unknowns in the Open Questions section. That makes
it easier to spot *unstated* ambiguities rather than just listing the
acknowledged ones. The remaining ambiguities cluster in three places —
agent ownership/lifecycle, quality-floor enforcement, and the
internal-vs-external repo paths — and any one of those could absorb a
weeks-long PR review debate if not resolved before implementation
starts. I'd recommend the human address Critical Gaps 1-5 directly
in a v2 of the PRD before convoy review converges.

I have **low confidence** about how the existing `mol-pr-feedback-patrol`
actually behaves; this PRD treats it as a black box. If that patrol's
contract is fuzzy, several findings here transitively become fuzzier.

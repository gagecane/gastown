# Technical Feasibility

## Summary

The PRD describes a standing patrol that, on a cadence, picks under-tested
files in a rig and dispatches a polecat to author a small, reviewable test PR
gated on human review. The bulk of the substrate it relies on already exists
in Gas Town (rig configs via `gt rig config`, polecat dispatch via slings,
`mol-pr-feedback-patrol` for ongoing PR revision, Refinery for merge, the
`gh` CLI for external repos), so the *coordination* layer is largely
buildable on top of established patterns. The hard problems are not in
plumbing — they are in the **two open-ended ML-shaped sub-problems the PRD
proposes to solve with heuristics**: (a) picking targets that are actually
*worth* testing rather than merely "uncovered," and (b) preventing the
polecat from shipping low-value, tautological, or
freezes-current-behavior-without-asserting-anything tests at PR-review-grade
quality. The PRD acknowledges these are hard but underestimates how much
they will dominate the lifecycle of this feature.

The feature is **buildable as proposed for a Go pilot** with moderate
effort, with the caveat that the v1 quality floor is very likely to be
weak — early PRs will look plausible at a glance but may not actually
catch regressions, which risks burning maintainer trust quickly. The
PRD's Open Question 9 (tautology / low-value detection) is the single
biggest risk. The other major un-derisked area is the **dual merge mode**
(Refinery vs `gh pr create`): the PRD glosses over the asymmetry between
"polecat goes through Refinery" and "polecat opens an external GitHub PR,"
and these have substantially different lifecycles, identity, and rollback
semantics.

## Findings

### Critical Gaps / Questions

These must be answered before implementation can start.

- **Refinery vs external PR mode is treated as one feature; it is two.**
  - Why this matters: For internal/maintainer rigs we do NOT open GitHub
    PRs — Refinery merges via merge queue. For external repos we use
    `gh pr create` and a maintainer merges. The PRD's "feedback handling"
    section assumes a GitHub PR with comments and labels, which is the
    external-repo model. In the Refinery model there is no PR for
    `mol-pr-feedback-patrol` to scan — the work either lands or gets
    bounced. The proposed `gt:auto-test-pr` label, branch prefix, body
    marker, and "≤1 open PR" coalesce check all assume GitHub semantics.
  - Suggested clarifying question for the human: Is v1 strictly the
    external-PR model (i.e., this only runs on rigs where we go through
    `gh pr create` and a human maintainer)? Or do we genuinely need a
    Refinery-mode equivalent (where "open PR" maps to "open MR bead in
    the queue") on day 1? The PRD says "pilot on `gastown_upstream`,"
    which is itself a Refinery rig — so the proposed pilot uses the
    mode the PRD spec'd *least*.

- **"At most one open auto-test PR per rig" has no defined source of truth
  in the Refinery model.**
  - Why this matters: For external rigs, "open PR" is an unambiguous
    GitHub query. For Refinery rigs, an auto-test branch could be
    pre-MR (polecat working), MR-pending (in queue), MR-rejected
    (bounced for rework), or post-merge. The PRD references a
    "pinned bead per rig" cache but doesn't specify which states count
    as "open." Without this, the coalesce step (Goal #3, S2) is
    under-specified and will either deadlock the cycle or flood it.
  - Suggested clarifying question: For Refinery rigs, what bead/MR
    states map to "PR is currently open"? Is a rejected MR
    awaiting-rework "open"?

- **Tautology / low-value test detection has no acceptance criteria.**
  - Why this matters: This is Open Question 9, and it's flagged as the
    "hardest problem," but the proposed defenses (heuristic linter,
    mutation-comment-out, diff-marker comments) are not specified well
    enough to know when a test is "good enough to ship." The mutation
    sanity check (comment one line, re-run, see if test still passes)
    is a *targeted single-mutant* — that catches the most egregious
    tautologies but won't catch tests that exercise the line *only*
    via assertions on irrelevant return values. Without a clear quality
    bar, v1 PRs will land that pass all defenses and still be useless,
    which is exactly the failure mode the maintainer reviews are
    supposed to catch — but at the cost of maintainer time and trust.
  - Suggested clarifying question: What is the v1 "ship gate" for a
    test? Specifically: must each new test (a) cover a previously
    uncovered branch *as measured by coverage delta*, AND (b) fail when
    we introduce a synthetic mutant of that branch? If we can't
    commit to both, we should expect the maintainer-reject rate to be
    high enough that the cooldown logic pauses every rig within the
    first month.

- **Coverage tooling is not actually language-agnostic and the per-rig
  config doesn't capture enough.**
  - Why this matters: Open Question 13 lists `test_cmd`, `coverage_cmd`,
    `lint_cmd` — but coverage tooling produces wildly different formats
    (Go: `go test -coverprofile`, JS: lcov.info via Jest/c8, Python:
    coverage.py XML, Rust: tarpaulin/llvm-cov). The polecat needs not
    only the command but also a parser to identify "uncovered branches
    in this specific function." The PRD says "language plurality is
    not a day-1 concern," but then proposes pilot on `gastown_upstream`
    (Go) — fine — without specifying the Go-coverage-parser path. That
    parser is non-trivial: mapping "uncovered statement at file.go:42"
    to "candidate test function" requires AST work in Go.
  - Suggested clarifying question: For the Go pilot, do we adopt an
    existing tool (e.g., `gocov` JSON output, or just parse the
    coverprofile with `golang.org/x/tools/cover`) or write our own?
    What's the interface the polecat consumes?

- **No prerequisite analysis of what the polecat *actually* needs to write
  a non-trivial test in 30 minutes.**
  - Why this matters: The PRD specifies the *workflow* but not the
    *information envelope* the polecat receives. To write a real test
    for `func Foo` the polecat needs: source of `Foo`, source of every
    type it depends on, examples of existing tests in that package
    (test helpers, mocks, conventions), and the rig's existing test
    infrastructure (fixture loaders, golden file paths, factory funcs).
    A single-bead dispatch with "target file + coverage cmd + test cmd"
    is *not* enough. Without a specification for the prompt envelope,
    test quality will vary wildly between polecats and between targets.
  - Suggested clarifying question: What is the minimum payload in the
    dispatch bead? Are we relying on the polecat to discover test
    conventions itself (slow, error-prone), or do we pre-extract a
    "test conventions sheet" per rig (much higher quality, but
    requires building that extractor)?

### Important Considerations

These should be addressed but aren't blockers.

- **PR identity / attribution interacts with branch protection.** The PRD
  proposes the polecat opens the PR under its rig identity. On many
  repos, branch protection requires PRs from non-bot accounts, or
  requires a `[bot]` suffix to skip CODEOWNERS. The polecat identity
  scheme (`gastown_upstream/polecats/foo`) is not a real GitHub account
  — the actual PR is opened by the GitHub user whose `gh` is
  authenticated locally. This means *every auto-test PR will be
  attributed to that human user*, not a bot. That's surprising and
  may trip CODEOWNERS / required-reviewer rules.

- **Flakiness re-run = N=10 may not be enough and may be too slow.** Some
  Go test suites take >5 minutes; running new tests 10× = 50+ minutes per
  cycle. Also, N=10 catches ~90% of flakes that fail half the time, but
  near-deterministic flakes (1-in-100) sail through. Consider: re-run
  *only* the new tests, not the full suite, and use a tight inner loop
  (e.g., `go test -count=10 -run="^TestNewlyAdded$"`).

- **"Dispatch one polecat per cycle" plus per-rig cooldowns can still
  starve the rig's polecat pool** if the patrol fires on many rigs at
  once. Need a global rate limit on auto-test polecats across rigs, not
  just per-rig cooldown.

- **Mutation-sanity-via-comment-out is fragile.** Commenting out a single
  line can cause syntax errors (e.g., the line is the body of a single-
  expression function, an `if` condition, an `else` clause). The
  implementation needs to be AST-aware, not line-aware, or it will
  produce false-failures that look like test passes when they're
  actually compile errors that get swallowed. Several Go-specific
  edge cases here.

- **Backoff state is Mayor-owned but Mayor is not always running on the
  same machine the rig is on.** The PRD says rate-limit/cooldown counters
  live on a Mayor-owned pinned bead. That works because beads are
  Dolt-replicated. Just verify: does Mayor *poll* this state, or does
  the patrol consult it on each tick? If the latter, the patrol step
  needs to read Mayor's pinned bead, not the rig's own.

- **Per-rig opt-in surface is under-specified.** The PRD says rig config
  via TOML in `.beads/` or rig manifest. The actual `gt rig config`
  surface (already implemented per `internal/cmd/rig_config.go`) uses a
  layered scheme (wisp / bead / town / system) with override semantics.
  Adding `auto_test_pr.enabled` etc. is straightforward, but need to
  confirm: do these new keys live alongside existing keys (`status`,
  `priority_adjustment`, `auto_restart`, etc.) or in a sub-namespace?

- **No story for "test starts failing later because the source under test
  changed."** Once an auto-generated test is on main, it's a permanent
  asset that will be modified by future PRs. If the source under test
  legitimately changes its contract, the auto-generated test (which the
  human reviewer approved at face value, possibly without deeply
  understanding it) may be the *wrong* thing to update — or worse, a
  later polecat may modify it to match new behavior, masking a real
  regression. This is the "test assertion changes require root-cause
  writeup" SOP problem (cited in our own polecat steering) at scale.
  Worth flagging in the PRD as a known follow-on risk.

### Observations

Non-blocking notes, suggestions, things to watch.

- The PRD is well-aligned with `gu-gal8` (no polecat-owned bookkeeping
  beads). The proposed split — Mayor owns the pinned-state bead, the
  polecat receives a single dispatch bead — is the correct shape.

- Reusing `mol-pr-feedback-patrol` for revisions is the right call and
  avoids duplicated logic. The handoff is clean: this new mechanism
  only handles *initial creation*; the existing patrol handles
  *revision*. Just verify the existing patrol's polecat-dispatch step
  can be parameterized with a "use the test-improver formula variant"
  knob — at a glance it currently dispatches to a generic
  `mol-polecat-work`.

- Branch naming `auto-test/<rig>/<short-slug>` is fine but consider
  collision risk: if two cycles run before the first polecat pushes,
  both could pick the same slug. Recommend `auto-test/<rig>/<bead-id>`
  (which the PRD's polecat-work step actually uses) — bead IDs are
  unique by construction.

- The PRD's "spawn-per-cycle polecat" is correct and aligns with
  polecat lifecycle. No long-lived "test-improver agent."

- The proposed pilot rig (`gastown_upstream`) has *higher* test density
  than most candidates — coverage is already strong on the well-touched
  paths. This may make it hard to find good targets that haven't already
  been tested. Consider: pilot on a rig with *known* coverage gaps
  (perhaps a smaller agent rig) so you have signal that the mechanism
  is producing valuable PRs, not trivial-line-coverage-bumps.

- The "diff-marker comment" defense (every new test must reference which
  branch / behavior it exercises) is a great social mechanism — even if
  the linter is weak, it forces the polecat to *state* what it thinks
  it's testing, which is a powerful prompt for the human reviewer to
  spot-check. Recommend keeping this as a hard requirement, not a
  soft suggestion.

- Consider a "kill switch" beyond per-rig opt-out: if `N` consecutive
  auto-test PRs are closed unmerged across *all* rigs (not just one
  rig), pause the entire mechanism and notify Overseer. The
  per-rig-3-rejections-pause-7-days mechanism is good, but it doesn't
  catch a systemic quality regression in the polecat formula itself.

- The "≤200 added test LOC" cap should be enforced *both* by the polecat
  (refuses to write more) AND by a post-check (discards over-budget
  candidates). Belt and suspenders. Polecats may not respect prompt
  size caps reliably.

- File ownership note: this work fits naturally into the existing
  `internal/formula/formulas/` and `internal/cmd/` patterns. No new
  package boundaries needed for v1. The complexity is in the polecat
  formula itself (`mol-polecat-work-test-improver.formula.toml`),
  not in `gt` core.

## Confidence Assessment

**Medium.** The mechanical / coordination layer is well-derisked — Gas
Town has all the substrate (rig config, dispatch, slings, Refinery, PR
patrol) and the proposed shape (Mayor-owned state, polecat-receives-bead)
is conventional and right. I'm confident a v1 of the cycle/dispatch/
coalesce flow can be built and shipped in 1–2 weeks of focused work.

What I am *not* confident about, and what should drive the timeline
estimate, are the two open-ended quality problems:

1. **Target selection that produces marginal-value tests** (not just
   coverage-line-bumps) — this is hard, and I do not believe the
   proposed `churn × (1 − coverage)` ranking will be sufficient.
   Expect 2–4 weeks of iteration on the ranking and on what counts as a
   valid target before the maintainer-reject rate stabilizes.

2. **Tautology / low-value test detection** — the proposed defenses are
   reasonable starting points but will not be enough on their own.
   Realistically, the v1 quality floor will rely heavily on the human
   reviewer, which is fine *if* we accept that the early-life
   maintainer-reject rate may be 30–50%. If that's not acceptable,
   the Mutation-sanity check needs to be much stronger than
   "comment-out-one-line" — and getting it right is its own project.

The PRD is in good shape to enter implementation *if* the human is
explicit that v1 is a learning vehicle, not a steady-drip productivity
tool, and accepts that the first month is high-touch. The gaps flagged
above (especially Refinery-vs-PR-mode and the polecat dispatch payload)
should be answered before the first line of code is written, since
they shape the formula's structure.

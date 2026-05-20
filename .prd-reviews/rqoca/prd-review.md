# PRD Review: Auto-Test-PR — automated test-improvement PRs per rig

> **Synthesis** of six parallel reviews: feasibility, stakeholders, scope,
> requirements completeness, missing requirements / gaps, ambiguity.
> Source review files in this directory.

## Executive Summary

The PRD is **well-shaped at the design-sketch level** — opt-in, bounded
size, no auto-merge, reuse of existing patrols, alignment with gu-gal8
— but is **not yet a buildable spec**. Two engineers reading it would
plausibly ship two different systems. Across all six legs, the same
small set of failure modes keeps appearing: (a) the **internal-Refinery
vs. external-GitHub-PR** paths are spec'd as one feature but are two
lifecycles with different identity, locking, and feedback semantics;
(b) the **quality floor** for generated tests (tautology / behavior-
freezing / target value) is the load-bearing-but-undersized sub-problem
that, if weak, will burn maintainer trust within weeks; and (c) the
**operational surface** (who runs the cycle, who pays the on-call, what
the kill-switch looks like, how secrets are kept out of tests, how
config changes are authorized) is mostly absent.

Recommended posture: **do not start building yet.** Resolve the seven
"must answer" questions below in a v2 of the PRD, then enter
implementation. Most of the rest of the document already converges with
that direction; the gaps are concentrated and addressable.

Confidence in v1 readiness: **Low** as written, **Medium** after the
Critical Questions are answered.

## Before You Build: Critical Questions

These were flagged by ≥3 of 6 reviewers (or by one reviewer on a topic
with safety / data-loss blast radius). Each must be a Decision in the
PRD before implementation starts.

### Scope & Architecture

**Q1. Is v1 the Refinery path, the external-GitHub-PR path, or both?**
- Why this matters: the PRD says "land = merged via Refinery (internal)
  OR maintainer merge (external)" but the existing-PR-detection scheme
  (GitHub label `gt:auto-test-pr`, branch prefix, body marker) and the
  hand-off to `mol-pr-feedback-patrol` both assume a real GitHub PR. In
  Refinery mode there is no PR — work goes through the merge queue as
  an MR bead. The proposed pilot rig (`gastown_upstream`) is itself a
  Refinery rig, so the pilot uses the mode the PRD spec'd *least*.
- Found by: feasibility (critical), ambiguity (critical 1), scope,
  requirements, gaps.
- Suggested answer options:
  - (a) v1 is Refinery-only on the pilot rig; defer external-PR support
    to v2 with its own PRD.
  - (b) v1 is external-PR-only; choose a different pilot rig.
  - (c) v1 supports both; the PRD must add a side-by-side state-machine
    for each mode (open-state, locking, feedback handoff, closure).
- Recommendation: option (a) is the lowest-risk slice; it removes
  ~30% of the spec surface and the entire external-repo trust /
  identity question (Q3 below).

**Q2. What is the v1 quality floor for generated tests, made testable?**
- Why this matters: Goal 6 ("not flaky, not tautology, not behavior-
  freezing") is asserted but not verifiable. The proposed defenses
  (heuristic linter, comment-out-one-line mutation sanity, diff-marker
  comments) are reasonable starting points but are individually weak
  and not committed to as MUST. If the floor is weak the early-life
  maintainer-reject rate will be 30–50% and the cooldown logic will
  pause every rig within the first month; if it is strong it is its
  own multi-week project. The PRD currently treats it as a one-line
  bullet.
- Found by: feasibility (critical), requirements (critical),
  ambiguity (critical 3), scope (critical), stakeholders, gaps.
- Suggested answer options:
  - **Tight:** every new test MUST (a) cover a previously-uncovered
    branch as measured by coverage delta, AND (b) fail when a
    synthetic mutant is introduced into that branch. Diff-marker
    comments are MUST. v1 ships only if both gates pass.
  - **Loose:** v1 ships with linter + N=10 flakiness check + diff-
    marker comments, and accepts that the human reviewer is the
    real quality gate. Document explicitly that maintainer-reject
    rate is expected to be high in pilot.
  - **Hybrid (recommended):** Tight gates for the pilot rig only,
    Loose gates for a second pilot rig once tooling exists, before
    generalizing.

**Q3. Who is the GitHub identity / commit author for auto-test PRs,
and how is the token scoped?**
- Why this matters: this is missing entirely from the PRD. Default
  behavior in absence of a decision is either (a) PRs come from the
  rig owner's personal GitHub identity (impersonation; can't review
  one's own PR) or (b) a shared bot user with org-wide repo write
  (credential leak → every rig's repo writable). For external repos
  the question compounds with DCO / CLA: a polecat cannot legally
  sign DCO on behalf of a human. This is a security-review item that
  should be answered before the first PR opens.
- Found by: gaps (critical 1), stakeholders (critical 4 — sec/compl),
  requirements, ambiguity.
- Suggested answer options: per-rig GitHub App installation (preferred,
  scoped per-repo), shared bot user with rotation policy, or rig
  owner's PAT (not recommended). Whichever, the PRD must name it,
  name the rotation cadence, and name the security reviewer who
  approved it.

**Q4. Are `test_cmd`/`coverage_cmd`/`lint_cmd` arbitrary shell or a
constrained allow-list, and who is authorized to change them?**
- Why this matters: combined with the per-rig opt-in surface, this is
  a privilege-escalation primitive. Anyone who can edit rig config
  can force any polecat that runs the cycle to execute arbitrary
  shell with the polecat's credentials and access to its filesystem
  and any secrets reachable from it. If config lives in the repo and
  Mayor honors it on next tick, write-access-to-repo == shell-
  execution-on-polecat-host.
- Found by: gaps (critical 2 + 3), stakeholders (security/compliance),
  requirements.
- Suggested answer: language-keyed allow-list (e.g., per language,
  the cycle picks `go test ./...`, `npx jest`, etc. from a built-in
  table), with rig owner only able to choose from the menu, not
  inject custom commands. Custom commands require a town-level
  approval bead.

**Q5. What is the dispatch contract between the cycle and the
polecat — i.e., the minimum payload the polecat receives?**
- Why this matters: the PRD specifies the workflow but not the
  *information envelope*. To write a real test for `func Foo` the
  polecat needs: source of `Foo`, source of every type it depends on,
  examples of existing tests in that package (helpers, mocks,
  conventions), the rig's existing test infrastructure (fixture
  loaders, golden file paths, factory funcs), AND the previous
  reviewer comments if this is a revision. A single bead with
  "target file + coverage cmd + test cmd" is *not* enough — test
  quality will vary wildly between polecats and between targets.
  Open Q8 (spawn-per-cycle polecat) compounds this: each polecat
  starts with no continuity from the last one.
- Found by: feasibility (critical 5), ambiguity 10, requirements,
  stakeholders.
- Suggested answer options:
  - (a) Pre-extract a per-rig "test conventions sheet" (committed to
    the rig) that the dispatch bead references. Higher up-front cost,
    much higher quality.
  - (b) Polecat discovers conventions itself from the source tree.
    Slow, error-prone, will produce inconsistent style.
  - (c) Hybrid: per-rig sheet for the pilot rig only; defer
    auto-extraction to v2.

### Operational Safety

**Q6. Who is on-call for an auto-test-PR incident, and what is the
global kill-switch?**
- Why this matters: this mechanism modifies main branches across every
  opted-in rig on a cadence. Failure modes flagged across legs include:
  duplicate PRs from cooldown-detection regressions, mass-spam from
  rate-limit bugs, secrets leaked into test source from fixture data,
  flaky tests landing on Gas Town's own main and breaking every
  patrol. The PRD names Mayor as state-owner but not as on-call.
  Witness "monitors polecats" but not "monitors bot PR output
  quality." Per-rig opt-out exists; town-wide kill-switch does not.
  This is the single largest missing piece — a system that auto-
  modifies code in production with no on-call is an outage waiting
  to happen.
- Found by: gaps (critical 6), stakeholders (critical 3),
  requirements.
- Suggested answer: name an on-call owner (likely the Overseer or a
  rotation), define a SEV-tree for the common failure shapes, ship
  `gt auto-test-pr pause --duration=24h` and `gt auto-test-pr
  status` as v1 deliverables (not deferred to "Extension points"),
  and add a circuit breaker: if N consecutive PRs across all rigs
  are closed unmerged, the entire mechanism pauses and pages.

**Q7. What is the locking model for the cycle, and what counts as
"PR open" in Refinery mode?**
- Why this matters: Goal 3 says "≤1 open auto-test PR per rig" but
  doesn't define open-state for Refinery. A polecat-pre-MR branch,
  an MR-pending in queue, an MR-rejected awaiting rework, and a
  post-merge MR are four different states. The PRD also doesn't
  describe what happens when (a) the cycle ticks while the feedback
  patrol is mid-revision, (b) two cycles fire concurrently (Mayor
  restart), (c) a human is force-pushing to the same branch, or
  (d) the maintainer merges *after* the next cycle has already read
  "no PR open." Most of these races silently degrade to "two PRs
  open at once" or "polecat clobbers human's in-flight commits."
- Found by: gaps (critical 5), ambiguity (critical 1, 11),
  requirements, feasibility.
- Suggested answer: pinned-state-bead used as compare-and-set lock
  (not just cache), with explicit state-machine: `idle |
  picking | dispatched | pr-open | pr-revising | cooled-down`. Each
  transition is recorded. Refinery-mode "open" = bead in
  `dispatched` or `pr-open` (i.e., MR not yet merged).

## Important But Non-Blocking

These should be addressed before pilot graduates beyond `gastown_upstream`,
but implementation can start once the Critical Questions are decided.

### Stakeholder & Governance

- **CODEOWNERS / per-file reviewer opt-out is missing.** The rig owner
  flips the rig-level switch, but PR review pings whoever is on the
  CODEOWNERS line for the touched files — who never opted in.
  Recommend a per-file or per-CODEOWNERS opt-out, plus consult-vs-
  affected language in the PRD. (stakeholders critical 1)

- **External-repo maintainers cannot opt in via rig config.** If v1
  retains the external-PR path, it MUST include an explicit allowlist
  of upstream repos that have pre-agreed. Otherwise a single rig flip
  damages relations with an OSS community. Strong recommendation: cut
  external-PR from v1 entirely (see Q1). (stakeholders critical 2)

- **Polecat pool priority is undefined.** "Mayor must be able to
  deprioritize" is too vague. Specify: auto-test work is strictly
  the lowest priority and is preempted by any user-filed work. If
  preemption is not implemented in current sling code, that's part
  of this project's scope. (stakeholders critical 5)

- **CI / GitHub-API budget is unstated.** N=10 flakiness reruns on
  a 20-min suite is 200 CPU-min/cycle; 10 rigs × twice a week =
  ~67 CPU-hours/week from this mechanism alone. PR-state polling
  plus label/branch operations also consume the org's GitHub API
  budget. Need a per-cycle wall-clock budget and a town-wide cap on
  auto-test-PR opens per day. (stakeholders, gaps 7)

- **Bot identity must survive merge.** PR body banner is attribution
  but not audit; banners can be edited or stripped at merge. Embed
  a code-level marker (a `// gt:auto-test-pr origin=...` comment) in
  the test source so the bot-authored origin survives merge and a
  reviewer ten months later can answer "who do I yell at?"
  (stakeholders, gaps).

### Spec Completeness

- **MVP is not explicitly defined.** The PRD lists 7 goals + 14 open
  questions; without a "smallest end-to-end slice" call, every Open
  Question is implicitly v1. Decide the smallest slice that ships
  and is observable, and explicitly cut the rest from v1.
  (scope critical, requirements critical)

- **No measurable pilot success criteria.** "Pilot worked" is not
  defined. Suggest: ≥X% merge rate over Y PRs over Z weeks;
  fewer than W incidents; rejection rate stable below Q%. (requirements)

- **Rebase race / "source under test changes after a test landed."**
  Auto-generated tests on main are permanent assets that future PRs
  modify. If a future polecat (or human) modifies the test to match
  new behavior, that's the "test assertion changes require root-
  cause writeup" anti-pattern at scale. Worth flagging as a known
  follow-on risk in the PRD. (feasibility, requirements, gaps)

- **Hand-off contract with `mol-pr-feedback-patrol` is unspecified.**
  "Reuse" is doing enormous load-bearing work in this PRD. The patrol
  may need to be parameterized to dispatch a `polecat-work-test-
  improver` formula variant; "may need to teach it" is the load-
  bearing weasel-phrase. Confirm or budget the patrol changes
  before build. (scope critical, ambiguity 18, requirements,
  feasibility)

- **"≤200 LOC, ≤3 files, no source changes unless absolutely
  required" enforcement is undecided.** Specify: hard limits enforced
  by both polecat (refuses to write more) AND post-check (discards
  over-budget). Define "absolutely required" — recommend treating
  test-helper additions as still-test-only; logic source changes are
  always disallowed. (scope critical, ambiguity critical 2)

- **Per-rig config home, schema, and authorization are unset.** Open
  Q13 is a multi-part question — file location, schema validator,
  who-can-edit, whether `enabled=true` alone is sufficient (i.e.,
  what defaults exist for unset commands). Resolve before build, and
  see Q4 for the security implications. (feasibility, ambiguity 5,
  scope, requirements, gaps)

- **Coverage-tool parser interface is undefined for the Go pilot.**
  The polecat needs not just `coverage_cmd` but a parser that maps
  "uncovered statement at file.go:42" to "candidate test function."
  Pick: `golang.org/x/tools/cover` of the coverprofile, gocov JSON,
  or write a small AST helper. (feasibility critical 4)

- **Mayor vs. crew vs. per-rig-daemon — who actually runs the
  cycle?** The PRD says "Mayor's daemon" in one place and implies a
  per-rig actor in another. Concretely name the agent type. (ambiguity
  critical 4)

- **No failure-mode table.** Each external dependency (GitHub API,
  rig polecat pool, test command, coverage command, Refinery MQ,
  the rig's own main branch) needs a detection signal and a response.
  Currently only "exits quietly" is documented. (requirements critical,
  gaps).

- **Cleanup of abandoned branches.** When the cycle pushes a branch
  before discovering a flaky test and aborting, the branch is left
  on origin. Specify a GC: stale `auto-test/<rig>/...` branches with
  no PR after N days are deleted. Same for closed-but-not-merged
  PRs. (gaps)

- **Secrets in test source.** A polecat reading fixtures, `.env.*`,
  or testdata can copy high-entropy strings into a test assertion
  and that PR is publicly visible on a community repo. Pre-push
  secret scan (gitleaks/trufflehog) gating the PR open is a v1
  requirement, not nice-to-have. (gaps critical 4)

### Ergonomics & Spec Drift

- **"Land", "PR", and the open-state across modes need consistent
  language.** A glossary section ("In Refinery mode 'PR' means MR
  bead in queue; the `gt:auto-test-pr` label is replaced by ...")
  removes a class of ambiguity. (ambiguity critical 1, scope)

- **"Diff-marker comments" mandate vs. existing rig style guides.**
  TALON-style test conventions discourage explanatory test comments;
  Go test names and Jest test strings already carry that information.
  Decide whether the diff-marker is a code comment or a PR-comment-
  thread requirement. (ambiguity 19)

- **N=10 flakiness on "the test suite" is ambiguous.** Specify: only
  the new tests (+ their direct package), not the full rig suite.
  Otherwise the cycle's wall-clock cost spirals. (ambiguity 9, gaps,
  feasibility)

- **Mutation-sanity-via-comment-out is fragile.** Single-line comment-
  out can produce syntax errors (single-expression function bodies,
  if-conditions). Implementation must be AST-aware, and must run in
  a tmpdir copy, not the worktree. (feasibility, gaps).

- **"Recently-changed" needs a number.** Approach §2 says 30 days;
  problem statement says "recently." Pick one and justify the number
  (or make it per-rig). (ambiguity 14)

- **Open Questions are still proposals.** All 14 Q-blocks read as
  "Proposed: ..." and none has a clear accept/reject. Convert to
  Decisions before build. (requirements critical, ambiguity)

## Observations and Suggestions

Non-blocking notes worth carrying into v2.

- The **Non-Goals section is unusually strong** and is doing most of
  the scope-control work. Keep it visible in any future revision.
  (scope)

- The **Mayor-owns-state / polecat-receives-bead** split is the
  correct shape under gu-gal8. Consider a one-line invariant:
  "polecat work bead is the ONLY bead created per cycle, and it is
  filed by Mayor, not by the polecat." (scope, gaps).

- **Pilot-rig choice has selection bias.** `gastown_upstream` is Go,
  well-tested, and the maintainer is the overseer. That makes the
  pilot more likely to succeed than is honest. Consider naming a
  *second* pilot rig (non-Go, smaller test density) up front so its
  constraints inform v1 design even if not v1 deployment.
  (stakeholders, scope, feasibility)

- **Branch naming `auto-test/<rig>/<bead-id>`** beats `<rig>/<short-
  slug>` — bead IDs are unique by construction; slugs collide.
  (feasibility)

- **Diff-marker comments are valuable as a social mechanism even if
  the linter is weak** — they force the polecat to *state* what it
  thinks it's testing, which gives the human reviewer a high-signal
  spot-check. Recommend keeping as MUST. (feasibility)

- **Add user stories** S7 ("flake discovered post-merge") and S8
  ("no candidates this cycle") to the PRD. (scope)

- **The pilot-on-self setup is clever and risky.** If auto-test-pr
  lands a flaky test on Gas Town's own main, every other patrol
  depending on a clean CI on `gastown_upstream` becomes flaky. Run
  pilot with extra-tight rate limits (≤1 PR/week) until two cycles
  succeed without intervention. (gaps)

- **Reserve a "STOP" magic phrase in PR comments** (e.g.,
  `gt auto-test-pr: pause-rig-7d`) that the patrol respects, so
  maintainers don't need to find rig config under fire. (gaps)

- The **bead-graph hygiene gu-gal8 invariant** should be restated as
  "polecat-FILED" vs. "polecat-ASSIGNED" in the PRD — a Mayor-filed,
  polecat-assigned bead does not violate gu-gal8. (ambiguity 16)

- **"The overseer" appears once in OQ#14** and is otherwise unused —
  harmonize with "rig maintainer" or define it. (ambiguity 17)

## Confidence Assessment

| Dimension | Score | Notes |
|-----------|-------|-------|
| Requirements completeness | **L** | Goals are aspirational, not testable; no success criteria, no failure modes table, 14 unresolved Open Questions. |
| Technical feasibility | **M** | Coordination layer is well-derisked; the two open-ended sub-problems (target selection, tautology detection) will dominate the lifecycle and are undersized in the PRD. |
| Scope clarity | **M-H** | Non-Goals are unusually disciplined. Three credible v1→v2 drift paths: feedback-patrol absorption, quality-floor expansion, config-surface scope creep. |
| Ambiguity level | **M** | Author pre-surfaced many obvious ambiguities; remaining ones cluster in agent ownership, quality enforcement, and internal-vs-external paths. |
| Operational readiness | **L** | No on-call owner, no kill-switch, no admin tooling, no secret-handling story, no audit trail. The mechanism modifies main branches with no human accountability surface. |
| Security posture | **L** | PR identity, token scoping, config-driven RCE, secret-leak-via-fixtures all unaddressed. Each is capable of a security incident on first rollout. |
| Stakeholder coverage | **M** | Named stakeholders are reasonable; silent set is larger and includes veto-holders (security/compliance, external maintainers, CODEOWNERS). |
| **Overall readiness for build** | **L** | Address the 7 Critical Questions to reach **M**. Add operational + security story + admin tooling to reach **H**. |

## Next Steps

- [ ] Human answers Critical Questions Q1–Q7 above (recommended: numbered
  reply matching the question numbers).
- [ ] PRD author updates the draft to v2 with those answers promoted from
  Proposals to Decisions, and adds: (a) measurable pilot success criteria,
  (b) a failure-mode table, (c) an Acceptance Tests section, (d) the
  on-call / kill-switch / admin-tooling spec, (e) the security model
  (identity, token, secret-scan, RCE allow-list).
- [ ] Pour a `design` convoy on the v2 PRD to generate the implementation
  plan, with Q1's chosen path (Refinery-only vs. external-only vs. both)
  determining the molecule structure.
- [ ] If Q1 selects Refinery-only, file a separate v2 bead for external-
  PR support so it's not lost.
- [ ] Before pilot launch: confirm `mol-pr-feedback-patrol` can drive
  auto-test PRs as-is OR budget the patrol changes into this project's
  scope.

---

*Synthesized from six parallel review legs:
[feasibility](feasibility.md), [stakeholders](stakeholders.md),
[scope](scope.md), [requirements](requirements.md), [gaps](gaps.md),
[ambiguity](ambiguity.md). Each leg's full analysis is in this directory.*

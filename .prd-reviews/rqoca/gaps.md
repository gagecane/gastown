# Missing Requirements

## Summary

The PRD for `auto-test-pr` is well-structured around the happy path
(scheduled cycle → target pick → polecat-writes-tests → PR → human
merge) and is appropriately conservative on goals (opt-in, bounded
size, no auto-merge, no source changes). It also correctly anchors on
prior art (gu-gal8: no polecat-owned bookkeeping).

That said, the draft leaves a substantial number of operationally
critical requirements unspecified or only implied. The most
consequential gaps fall into four buckets: (1) **authentication and
PR-creation identity** (whose GitHub token does the PR get pushed
under, how do permissions for the `gt:auto-test-pr` label get
provisioned, what happens for repos that require signed commits or
DCO); (2) **concurrency / race control** between the patrol tick, the
feedback patrol, manual maintainer edits, and rig-config flips;
(3) **secrets / supply-chain safety** for the test code the polecat
emits (it can read the source tree, including fixtures that contain
secrets, and could embed them in test assertions); and (4) **admin
tooling and observability** — there is no specified way for a human to
ask "what is auto-test-pr doing right now," "why didn't it pick file
X," or "stop everything." The remaining gaps (audit logging, i18n,
accessibility, deprecation, edge cases on empty/degenerate rigs) are
real but lower in blast radius.

Confidence in the spec **as a v1 implementation contract is Low**.
Confidence in the *direction* is High — the missing pieces are
addressable but they are missing today.

## Findings

### Critical Gaps / Questions

These MUST be answered before implementation can start, because the
default behavior in the absence of an answer is either insecure,
operationally dangerous, or causes silent failures in production.

#### 1. PR-creation identity & GitHub credentials

- **Finding:** Goal #1 says "land net-new tests on rig main branches
  autonomously." The PRD names two paths (Refinery for internal repos,
  `gh pr create` for external). It does NOT specify *which identity*
  authors the PR, where that identity's GitHub token comes from, how
  it's rotated, or how it's scoped (per-rig PAT? GitHub App? machine
  user?). For external community repos, signed-commits / DCO / CLA may
  be required and a polecat cannot legally sign DCO on behalf of a
  human.
- **Why this matters:** Wrong default → either polecat PRs come from
  the *human owner's* personal GitHub identity (impersonation /
  attribution problem, and the human can't review their own PR on many
  projects), or from a shared bot identity with overscoped repo
  write — a credential leak makes every rig's repo writable. Either
  case is hard to fix after the fact.
- **Suggested clarifying question:** "What is the GitHub identity that
  authors auto-test PRs? Is it a per-rig GitHub App installation, a
  shared bot user, or the rig owner's PAT? How is the token scoped
  (single-repo vs. org-wide), where is it stored, and what's the
  rotation policy? For repos that require DCO sign-off or CLA, how do
  we handle that?"

#### 2. Authorization: who can enable / disable / configure the mechanism

- **Finding:** Constraint says "single-flip revertibility per rig" and
  open question #13 mentions a TOML in `.beads/` or rig manifest. It
  does NOT say *who* is allowed to flip that bit. Anyone with
  write access to the repo? Only the rig owner? Mayor? Can a
  malicious or confused polecat flip it?
- **Why this matters:** If config lives in the repo and is honored by
  Mayor, anyone with PR-merge rights to the repo can turn the
  mechanism on for someone else's rig; if they can also force-push
  config, they can change the test/lint/coverage commands and
  coerce the polecat into running arbitrary code. Open question #13
  is treating this as a UX question (where does config live), but it
  is also an authz question.
- **Suggested clarifying question:** "Who is authorized to (a) enable
  the mechanism for a rig, (b) change `test_cmd` / `lint_cmd` /
  `coverage_cmd`, (c) globally disable for the whole town in an
  incident? Should config changes be reviewed/approved before Mayor
  honors them, or honored on next tick?"

#### 3. Arbitrary-command execution boundary

- **Finding:** The proposed config exposes `test_cmd`, `coverage_cmd`,
  `lint_cmd`, `flakiness re-run command`. These will be executed by a
  polecat inside the rig's worktree — i.e., they are a vector for
  arbitrary code execution under the polecat's identity. The PRD does
  not state any sandboxing, command allow-list, or "must be a path
  inside the repo" constraint.
- **Why this matters:** Combined with gap #2, anyone who can edit rig
  config can force any polecat that runs the cycle to execute
  arbitrary shell with the polecat's credentials and access to its
  filesystem. This is a privilege escalation primitive.
- **Suggested clarifying question:** "Are `test_cmd` / `lint_cmd` /
  `coverage_cmd` constrained to predefined commands per language
  (e.g., `go test ./...`, `npx jest`), or arbitrary shell? If
  arbitrary, what sandboxing applies, and who reviews the config
  change that introduces a new command?"

#### 4. Secrets in the test source tree

- **Finding:** The polecat will read source files, possibly including
  fixtures, `.env.example`, snapshot files, integ test data, and
  testdata directories — many of which historically contain real or
  realistic secrets. The PRD's "tautology / low-value test detection"
  in OQ#9 is about test *quality*, not about the polecat *embedding
  secrets it observed* into a new test assertion.
- **Why this matters:** A polecat with no explicit "do not echo
  observed strings into source" guard can land tests like
  `assert(getKey() == "AKIA...REAL...")` from reading a fixture. That
  PR is then publicly visible on a community repo. This is a credible
  way to leak real secrets.
- **Suggested clarifying question:** "What protections prevent the
  polecat from copying fixture/secret-shaped strings (high-entropy
  values, AWS keys, JWTs, hex blobs > N bytes) into the new test
  source? Is there a pre-push secret scan (e.g., gitleaks,
  trufflehog) gating the PR open?"

#### 5. Concurrency between cycle, feedback patrol, and human edits

- **Finding:** OQ#5 acknowledges "PR open?" state lives in GitHub
  authoritatively with a pinned bead as cache. But the PRD does not
  describe what happens when:
  - The cycle ticks while `mol-pr-feedback-patrol` is mid-revision
    on the open auto-test PR.
  - A human is force-pushing to the same branch.
  - The maintainer merges the PR and the cooldown bead is updated
    *after* the next cycle has already read "no PR open."
  - Two Mayors / two daemons / a manual `gt mol dispatch` fire the
    cycle simultaneously.
- **Why this matters:** Most of these races silently degrade to "two
  PRs open at once" (violates Goal #3) or "polecat clobbers human's
  in-flight commits on the branch." The latter is a data-loss
  failure visible to the maintainer.
- **Suggested clarifying question:** "What is the locking model for
  the cycle? Is the pinned-state bead used as a mutex (compare-and-
  set), or is single-writer enforced at the Mayor daemon? What
  happens if a human is pushing to the auto-test branch when the
  feedback patrol fires?"

#### 6. Admin tooling / kill switch / triage commands

- **Finding:** No specified way for a human to:
  - Ask "what is auto-test-pr currently doing across all rigs?"
  - See the rejection history / cooldown table.
  - Say "stop globally for the next 24h" without editing every
    rig's config.
  - Say "skip target file X for the next N cycles" without closing
    a PR.
  - Inspect *why* a particular file was or wasn't picked.
- **Why this matters:** Once the mechanism is running across a
  handful of rigs, support / on-call needs a single place to look.
  Without it, the first incident becomes "ssh into Mayor, grep logs"
  — which we know from past patrols ages badly.
- **Suggested clarifying question:** "What CLI / dashboard is in
  scope for v1? Minimum viable: `gt auto-test-pr status`, `gt
  auto-test-pr pause --duration=24h`, `gt auto-test-pr explain
  --rig=X --file=Y`. Is any of this in the v1 cut?"

#### 7. Rate limiting / abuse: human-side

- **Finding:** OQ#7 covers rate limits *outbound* (one open PR per
  rig, cooldown after merge, backoff after rejections). It does NOT
  cover:
  - Per-org / per-token GitHub API quota (a buggy cycle hammering
    `gh pr list` across many rigs can exhaust the shared rate
    budget for the whole town).
  - Per-CI quota (each opened PR triggers CI; CI minutes are real
    money in many orgs).
  - Coordination with other patrols sharing the same API token
    (e.g., the existing PR-feedback-patrol).
- **Why this matters:** A bug in target-selection that opens (and
  immediately closes via lint) one PR per cycle per rig still burns
  CI minutes. We have observed similar runaway-loop patterns
  before; the cost is real.
- **Suggested clarifying question:** "Is there a town-wide cap on
  auto-test PR rate (e.g., ≤N opens/day across all rigs) and a
  shared GitHub-API budget tracker? What's the cost ceiling we
  refuse to cross?"

### Important Considerations

These should be addressed but are not gating for v1 build:

- **Audit logging.** Every PR opened, comment replied to, target
  rejected, cooldown applied — none of these have a specified
  audit destination. For internal compliance ("who/what changed this
  test on date X"), and for incident response, a structured log
  (e.g., to the Mayor's pinned audit bead or a dedicated jsonl
  file) should be required. PR body banner is *attribution* but not
  *audit* — it doesn't survive PR editing.

- **Cleanup of abandoned branches.** S5 says "no PR ever appears
  with a red build" — good — but what about the *branch* that the
  polecat created locally, ran tests on, and then aborted? Local
  state is nuked when the polecat exits. But what about branches
  pushed mid-flight before a fatal error? Spec needs an explicit
  GC: stale `auto-test/<rig>/...` branches with no PR after N days
  get deleted.

- **Closed-but-not-merged PR cleanup.** S4 mentions backoff if a
  reviewer closes a PR. The branch on origin is left dangling. Does
  the mechanism delete the closed PR's branch? GitHub's "auto-
  delete head branches" handles this if enabled, but the rig owner
  may not have it on. Spec should require it or handle it.

- **Empty / brand-new rig edge case.** What does the cycle do on a
  rig with zero coverage data, zero recent churn, or only one
  source file? OQ#3's ranking formula `churn × (1 − coverage)`
  divides by nothing on an empty rig but still produces zero
  candidates — spec should say "no candidates → no PR, log and
  exit cleanly" rather than letting the polecat improvise.

- **All-targets-untestable edge case.** Rigs whose source tree is
  generated code, vendored deps, or wrappers around CGo /
  proprietary SDKs may have no targets that can be unit-tested.
  Spec needs a "rig owner can declare these dirs out of scope"
  knob, and the cycle must respect it before consuming a polecat
  slot.

- **Backwards compat with existing tests.** The PRD says "no non-
  test source changes." But adding a test file may require *test
  helpers* (mocks, builders) to be added or modified. If a helper
  is shared, modifying it is technically a non-test change with
  blast radius. Spec should distinguish "test helper under
  testutil/" from "production source" explicitly.

- **PR template / CODEOWNERS interaction.** Many rigs have
  CODEOWNERS that auto-request reviewers, and PR templates that
  require checkboxes (e.g., "I added tests"). The auto-test PR
  needs to (a) honor the template or (b) explicitly opt out of it.
  Otherwise the PR is bot-spam from the maintainer's POV and gets
  filed away.

- **Failing `mol-pr-feedback-patrol` handoff.** OQ#6 reuses the
  existing patrol. The PRD does NOT specify what happens if the
  patrol *fails* to revise (e.g., cannot satisfy the comment, or
  the comment is "rewrite this entirely"). Should the auto-test
  cycle inherit the cooldown? Should the PR self-close after N
  failed revision attempts?

- **Detection of "this file is intentionally untested."** S4
  imagines the maintainer says so in the close comment. The cycle
  then "avoids retargeting that file for some cooldown period" —
  but this needs to be persisted, surface in the next target-pick,
  and ideally be promotable to a permanent skip-list. Spec is
  hand-wavy here.

- **i18n / l10n.** Not applicable to test code itself, but PR body,
  labels, and reviewer-facing comments should default to the rig
  owner's language preference if the project is non-English.
  Assume English unless overridden — but the spec should at least
  say so.

- **Accessibility.** PR bodies use markdown rendered by GitHub —
  default GitHub markdown is accessible. Worth a one-liner: avoid
  ASCII-art tables / box-drawing in PR body so screen readers
  handle it. (Cf. wiki-editing-conventions.md observation about
  proportional fonts.)

### Observations

Non-blocking, but worth flagging:

- **Goal #1's "merged via Refinery" assumes Refinery accepts test-
  only changes the same way.** Refinery's bisecting MQ should be
  fine, but if the rig has any "no test-only commits" rule (some
  monorepos do, to avoid review fatigue), the mechanism will
  produce work that can't land. Worth confirming on the pilot
  rig.

- **The pilot-on-self setup ("`gastown_upstream` itself") is
  clever and risky.** If auto-test-pr lands a flaky test on
  Gas Town's own main, every other patrol that depends on a clean
  CI on `gastown_upstream` becomes flaky. Recommend the pilot run
  with extra-tight rate limits (≤1 PR/week) until two cycles
  succeed.

- **OQ#9's "comment out one line and re-run" mutation-sanity
  check** modifies source files transiently. If the polecat
  crashes mid-mutation, the worktree is corrupt. Recommend doing
  this in a copy of the file in a tmpdir, never in the actual
  worktree.

- **"Spawn-per-cycle polecat" (OQ#8) is correct under gu-gal8 —
  but** the polecat's hook bead must be Mayor-filed, not polecat-
  filed. Spec should state this explicitly to prevent a future
  refactor from regressing the gu-gal8 invariant.

- **PR body banner (OQ#11) needs to include enough info that a
  maintainer ten months later can answer "is this still on?
  who do I yell at?" without reading the PRD.** Minimum: rig name,
  cycle ID / bead ID, link to opt-out config location, "what
  this PR was *not* allowed to do" (e.g., "no source changes were
  permitted").

- **The bead-graph hygiene question is partially open.** gu-gal8
  protects against polecat self-creation. But the *mol-pr-feedback-
  patrol* handoff might create revision sub-beads, and those would
  be attributed to whichever agent is running the patrol. Spec
  should confirm those follow gu-gal8 too (Mayor-owned, polecat-
  executed).

- **Token of last resort: maintainer "STOP" comment.** Not in the
  PRD. Suggest reserving a magic phrase in PR comments (e.g., `gt
  auto-test-pr: pause-rig-7d`) that the patrol respects. This
  avoids the maintainer needing to find rig config under fire.

## Confidence Assessment

**Confidence: Low** that the PRD as written is implementable into a
safe v1.

Rationale:

- **Direction is sound** — opt-in, bounded scope, no auto-merge,
  reuse existing patrols, honor gu-gal8. These are all correct.
- **Happy path is well-described** with realistic user stories and
  an articulated rough approach.
- **However**, the four critical-gap clusters (PR-author identity
  + token, config-driven RCE surface, secret-leak risk in
  generated tests, concurrency between patrols) are each capable
  of producing a security or data-loss incident on first
  rollout. They are missing in the *requirements*, not just in
  the *design* — i.e., the team building this could implement
  the PRD literally and still ship something unsafe.
- **The non-goals usefully prune scope** but two of them
  (i18n/accessibility) are dismissed implicitly and should be
  named.
- **No admin tooling is specified at all.** This is the single
  most common cause of patrols becoming un-operable in
  production. v1 needs at least `status` and `pause`.

A second pass that explicitly addresses Critical Gaps 1–7 above
should bring confidence to Medium. Confidence reaches High once
the operational + secret-handling story is defined and a kill
switch exists.

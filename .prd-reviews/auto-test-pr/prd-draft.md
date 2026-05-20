# PRD: Auto-Test-PR — automated test-improvement PRs per rig

## Problem Statement

Gas Town rigs accumulate code faster than humans can write tests for it.
Coverage drifts down. Recently-changed files often ship with the bare
minimum of tests. Edge-case bugs land in main and surface only when a
polecat (or human) gets bitten weeks later.

**Who it's for:**
- Rig maintainers who want a steady drip of "small, reviewable test PRs"
  rather than a one-time coverage push.
- Crew/Mayor agents who want to convert idle capacity into durable
  quality improvements.

**Why now:**
- Gas Town's polecat fleet is increasingly capable of scoped code work.
- We already have all the substrate: convoys, slings, refinery, beads,
  PR feedback patrol. We just don't have a mechanism that says "spend
  some idle time writing tests where it matters."

## Goals

1. **Land net-new tests on rig main branches autonomously**, gated by
   ordinary human PR review (not auto-merged). Definition of "land":
   merged via Refinery (internal repos) or via maintainer merge
   (external repos).
2. **Per-rig opt-in.** A rig owner enables the mechanism with a single
   config flip; disabling it is also a single flip. Default is OFF.
3. **At most one open auto-test PR per rig at any time.** No flooding.
4. **Feedback-driven revision on the same PR.** Comments → new commits
   on the existing branch. Never close+reopen.
5. **Bounded blast radius per PR.** Each PR is small enough to review
   in one sitting (target: ≤200 added test LOC, ≤3 files touched, no
   non-test source changes unless absolutely required).
6. **Quality floor.** New tests must pass, not be flaky (re-run N
   times), not assert tautologies, and not just freeze current behavior
   without exercising real branches.
7. **Honor gu-gal8.** No polecat-owned bookkeeping beads. The mechanism
   itself is owned by Mayor or system; the polecat is dispatched a
   single bead, executes it, returns.

## Non-Goals

- **Not** a coverage-percentage-chasing tool. We don't aim for an
  arbitrary number; we aim for tests with marginal value.
- **Not** integration / e2e / load test generation. Unit tests only.
- **Not** mutation testing infrastructure on its own. We may *consume*
  mutation results if cheap, but we won't introduce a dedicated
  mutation pipeline as part of this.
- **Not** a code-fixing tool. If a generated test surfaces a real bug,
  we file a separate bead — we do NOT bundle a fix into the test PR.
- **Not** retroactive coverage cleanup. Greenfield only — pick targets
  forward, don't try to backfill the whole rig.
- **Not** language-agnostic on day 1. Pilot is one rig; cross-language
  abstraction is a follow-up.
- **Not** auto-merge. Human review is required.

## User Stories / Scenarios

**S1 — Steady drip on a healthy rig.** Rig `gastown_upstream` has
auto-test-PR enabled. Twice a week the mechanism wakes up, picks 2-3
under-tested branches in a recently-changed file, drafts tests, opens a
PR. The maintainer reviews and merges within a day. No PR is open when
the next cycle ticks; a new one gets opened.

**S2 — Coalesce when a PR is already open.** The cycle ticks but PR
#487 is still open from the last cycle. The mechanism detects this and
exits — no new PR.

**S3 — Reviewer leaves comments.** The maintainer comments "this mock
is too coupled — use the test-helper in `internal/testutil`." The
mechanism (or the existing PR feedback patrol) reads the comments,
dispatches a polecat, the polecat pushes a new commit to the same
branch, the comment thread is replied to.

**S4 — Reviewer rejects.** The maintainer closes the PR with "wrong
target — that file is intentionally untested." The mechanism records
that rejection, backs off (rate limit), and avoids retargeting that
file for some cooldown period.

**S5 — New test breaks the build.** A test the polecat wrote fails
locally. The mechanism does NOT push it. It either retries with a
revision or abandons the cycle quietly. No PR ever appears with a red
build.

**S6 — Rig opt-out.** A rig owner sets `auto_test_pr.enabled = false`.
Next cycle, the mechanism skips that rig. Any in-flight PR is left
alone (the human can merge or close it manually).

## Constraints

- **Gas Town conventions:** polecats are transient and witness-managed;
  crew are persistent. Bookkeeping beads must NOT be polecat-owned
  (gu-gal8).
- **Refinery interaction:** for repos where we have direct push, we go
  through gt done → Refinery. For external/community repos we use
  `gh pr create` directly. The mechanism must detect which mode applies
  per rig.
- **Existing patrols:** `mol-pr-feedback-patrol` already handles "open
  PR has new comments or failing CI → revise." We should reuse it
  rather than reinvent feedback handling.
- **No interference with normal dispatch.** The PR-creation cycle must
  not flood `bd ready`. Mayor must be able to deprioritize it.
- **Language plurality.** Rigs use Go, TypeScript, Python, etc. The
  mechanism needs per-rig config for: test command, coverage command,
  flakiness re-run command, lint command.
- **Single-flip revertibility per rig.**

## Open Questions

These are known unknowns going into review. The PRD review will surface
more.

1. **Lifecycle.** Is this a standing patrol (cron-shaped, like
   `mol-pr-feedback-patrol`), a Mayor-dispatched task on a cadence, or
   on-demand only? Likely answer: standing patrol, but with a per-rig
   schedule controlled by config (e.g., `cadence: "twice-weekly"`).

2. **PR size cap — exactly what?** Proposed: ≤200 added test LOC, ≤3
   files touched. Need to decide whether this is enforced by the
   polecat itself (refuses to write more) or by a post-check that
   discards over-budget candidates.

3. **Target selection algorithm.** Proposed default: "files churned in
   last 30 days AND with line/branch coverage below rig threshold,
   ranked by churn × (1 − coverage)." Need to decide whether to layer
   in mutation testing where cheap (Go has go-mutesting, JS has
   stryker), or stick to coverage-only for v1.

4. **Existing-PR detection.** Proposed: combination of (a) GitHub
   label `gt:auto-test-pr`, (b) branch-name prefix
   `auto-test/<rig>/<short-slug>`, (c) PR body marker `<!--
   gt-auto-test-pr -->`. Multiple signals so we survive edits to one
   of them. Need to decide which is *authoritative*.

5. **Authoritative state for "PR open?".** GitHub state alone (query
   each cycle), or a sentinel pinned bead per rig (e.g.,
   `<rig>-auto-test-state`)? Proposed: GitHub is source of truth,
   pinned bead is a cache + audit trail. Pinned bead is owned by
   Mayor (NOT polecat) per gu-gal8.

6. **Feedback handling — reuse or new?** Proposed: reuse
   `mol-pr-feedback-patrol`. This mechanism is responsible for
   *initial* PR creation only; revision is the existing patrol's job,
   keyed off the `gt:auto-test-pr` label.

7. **Rate limiting.** Proposed: hard cap of one open auto-test PR per
   rig at all times, plus a soft cooldown (no new PR within 24h of the
   prior one being merged or closed). Backoff on consecutive
   rejections — e.g., 3 closes in a row → pause that rig for 7 days,
   notify Mayor.

8. **Polecat reuse.** Proposed: spawn-per-cycle polecat. The bead the
   polecat receives carries all the context (target file, current
   coverage, prior comments if revising). No long-lived "test
   improver" agent — that violates the polecat lifecycle and adds
   state we don't need.

9. **Tautology / low-value test detection.** Hardest problem. Proposed
   layered defense:
   - Heuristic linter: reject `assert(true)`, `expect(x).toBe(x)`,
     assertions on literals, tests with no `assert*` calls at all.
   - Mutation-style sanity: comment out one line in the function under
     test; if the new test still passes, it's not exercising that
     line — flag for human review.
   - Diff-marker comments: every new test must reference *which*
     branch / behavior / edge case it's exercising in a comment, so
     the human reviewer can sanity-check.

10. **Flakiness check.** Run new tests N=10 times locally before push.
    Any non-determinism → discard.

11. **PR author / attribution.** Proposed: PR is opened by the polecat
    under its rig identity (e.g., `gastown_upstream/polecats/foo`),
    body includes a clear `🤖 Auto-generated by gt auto-test-pr`
    banner with a link to the design doc and the rig's opt-out
    instructions.

12. **Bead bookkeeping.** Per gu-gal8: any persistent state (the
    pinned-bead cache, rate-limit counters, rejection history) is
    Mayor-owned. The polecat receives a bead; it does not file beads.

13. **Per-rig opt-in surface.** Proposed: a new section in the rig's
    Gas Town config (e.g., `gt rig config <rig>` writes to a TOML in
    `.beads/` or the rig manifest) with `auto_test_pr.enabled`,
    `auto_test_pr.cadence`, `auto_test_pr.test_cmd`,
    `auto_test_pr.coverage_cmd`, `auto_test_pr.lint_cmd`. Need to
    confirm where rig config conventionally lives in gt today.

14. **Pilot rig.** Proposed: pilot on `gastown_upstream` itself (Go,
    well-tested, maintainer is the overseer). Gather a couple of weeks
    of data before generalizing.

## Rough Approach

A new molecule `mol-auto-test-pr-cycle` with these legs:

1. **gate** — Read rig config; if not enabled, exit. If a PR with the
   `gt:auto-test-pr` label is already open for this rig, exit
   (coalesce). If rate-limit cooldown active, exit. Update the
   pinned-bead cache with current state (Mayor-owned bead).

2. **target-pick** — Run rig's coverage command; cross-reference with
   git churn (last 30 days). Rank candidates by `churn × (1 −
   coverage)`. Pick top 1-3 candidates that fit in the size budget.

3. **dispatch** — Mayor files a single bead `<rig>-auto-test-NNN` with
   the targets, test command, lint command, PR template. Slings it to
   a polecat in the rig. The polecat's bead is the only bead created
   in the cycle. The Mayor owns the parent state bead (pinned).

4. **polecat-work** — Polecat runs `mol-polecat-work` variant
   (`mol-polecat-work-test-improver`?) that:
   - Writes tests for the assigned targets.
   - Runs the test suite N times for flakiness.
   - Runs the tautology-linter.
   - Runs the mutation-sanity check (cheap — comment one line, re-run).
   - If all pass: creates branch `auto-test/<rig>/<bead-id>`, opens PR
     with the `gt:auto-test-pr` label and body marker.
   - If any fail: closes the bead with reason; no PR is opened.

5. **handoff to feedback patrol** — From this point,
   `mol-pr-feedback-patrol` (already standing) handles comments and
   CI failures. It already pushes new commits to the existing branch.
   We may need to teach it to honor the `gt:auto-test-pr` label by
   dispatching to the same polecat-work-test-improver formula.

6. **closure** — When the PR is merged or closed, Mayor's
   `mol-auto-test-pr-cycle` next tick observes the change, updates the
   cache, applies any cooldown, and the cycle is ready to fire again.

The patrol cadence runs in the Mayor's daemon (similar to other
patrols). The actual *work* runs in the rig's polecat fleet. Nothing
new is needed in Refinery — auto-test PRs go through the same merge
queue as any other polecat work for that rig.

**Extension points (deliberately deferred):**
- Cross-language helper packs (Go/TS/Py recipes for tautology
  detection, flakiness check).
- Mutation testing integration as a higher-quality target signal.
- Mayor-level dashboard of "tests added this week per rig."

---

## Clarifications from Human Review

The synthesis surfaced 7 critical questions blocking build. Decisions
below — these are committed for v1 unless explicitly revisited.

**Q1: Refinery vs external-PR — which path is v1?**
**A:** **Option (a) — v1 is Refinery-only on the pilot rig.** External-PR
mode is cut from v1 to halve the spec surface and remove the entire
DCO/CLA / external-identity question. File a separate v2 bead
("auto-test-pr: external GitHub PR mode") so it isn't lost. The pilot
rig is `gastown_upstream` (Refinery rig); a non-pilot rig with external-
PR-only repos is explicitly out of scope until v2.

**Q2: V1 quality floor — Tight, Loose, or Hybrid?**
**A:** **Option Hybrid (recommended), starting with Tight on pilot.**
- **MUST gates (blocking PR-open):**
  1. Each new test covers at least one previously-uncovered branch as
     measured by coverage delta (`go test -coverprofile` diff).
  2. Synthetic-mutant sanity: for each new test, comment out one line
     of the function under test in a tmpdir copy and re-run the test;
     it MUST fail. Implementation is AST-aware (skip lines whose
     comment-out produces syntax errors; pick a different line).
  3. Diff-marker comments: each new test function has a leading
     comment `// gt:auto-test-pr origin=<bead-id> covers=<file:line>`.
  4. N=10 flakiness re-run on the new tests + their direct package
     only (NOT the full rig suite — bound the wall-clock).
  5. Tautology linter (reject `assert(true)`, `expect(x).toBe(x)`,
     literal-equality, tests with zero assertions).
- **All gates run in the polecat sandbox before PR-open.** Any failure
  → polecat exits with NOTES recording which gate failed; no PR is
  opened. The synthetic-mutant check runs in a tmpdir copy, never the
  worktree.
- **Loose mode is reserved for v2** when generalizing past the pilot.

**Q3: GitHub identity / token scoping?**
**A:** **Per-rig GitHub App installation.** Each rig that opts in
provisions a GitHub App scoped to that rig's repo only. The polecat
authenticates as the App at PR-open time. Token lifetime: short-lived
(JWT-derived installation tokens, ≤1h, fetched per-cycle; no PATs in
config).
- Rotation cadence: handled by GitHub (installation tokens are
  ephemeral by construction).
- Security reviewer: Overseer signs off on the App's permission set
  before the rig flips opt-in.
- Until the App exists for a rig, that rig CANNOT enable auto-test-pr.
  No PAT fallback. Refinery-mode pilot bypasses this entirely (no PR,
  no App) — so Q3 is unblocking for v2 (external-PR), not v1.
- For Refinery-only v1: PR identity question collapses to "polecat
  commits as polecat" via the existing convention; no new identity
  surface. Document this in the PRD.

**Q4: Test/coverage/lint command authorization?**
**A:** **Language-keyed allow-list, no custom commands in v1.**
- Built-in table maps language → vetted command set:
  - `go`: `go test ./... -coverprofile=...`, `go vet ./...`,
    `golangci-lint run` (if present).
  - `typescript`/`javascript`: `npm test -- --coverage`, `npx eslint`.
  - `python`: `pytest --cov`, `ruff check`.
  - (more added per language as second / third pilots come online)
- Rig owner picks the language; commands are derived. They cannot
  inject custom shell.
- Custom commands deferred to v2 behind a town-level approval bead
  (Mayor-owned, requires Overseer sign-off). NOT in v1 scope.
- **Implication:** the `auto_test_pr.test_cmd` / `coverage_cmd` /
  `lint_cmd` keys proposed in the original draft are removed.
  Replaced by a single `auto_test_pr.language` key.

**Q5: Dispatch payload contract — what does the polecat receive?**
**A:** **Option (c) Hybrid — per-rig "test conventions sheet" for
the pilot only; auto-extraction deferred to v2.**
- For the pilot rig (`gastown_upstream`), commit
  `.gt/auto-test-pr/conventions.md` documenting: test framework,
  fixture loaders, factory funcs, common mocks, file/test naming
  patterns, anti-patterns to avoid (e.g., TALON-style "no comments
  in test code except brief intent" rule).
- Dispatch bead from Mayor includes:
  - Target file path(s) (≤3) with current coverage profile
  - Path to `.gt/auto-test-pr/conventions.md`
  - PR template (Refinery-mode MR description template)
  - For revision cycles: full prior-comment thread + last commit SHA
- v2: explore auto-extraction of a conventions sheet by reading the
  source tree on first opt-in; out of scope for v1.

**Q6: On-call owner + global kill-switch?**
**A:** **Overseer is on-call for v1 (single-pilot phase). Kill-switch
shipped as a v1 deliverable, not deferred.**
- **v1 kill-switch CLI (must ship):**
  - `gt auto-test-pr pause --rig=<rig> --duration=24h` (per-rig)
  - `gt auto-test-pr pause --all --duration=24h` (town-wide)
  - `gt auto-test-pr status` (which rigs enabled, last cycle, open PR
    state per rig, current pause windows)
- **Circuit breaker:** if 3 consecutive auto-test PRs across all rigs
  are closed unmerged within 7 days, the mechanism town-wide-pauses
  itself for 72h and posts a notification bead to the Overseer.
- **SEV tree (v1 — small):**
  - SEV-1: auto-test PR breaks main CI on any rig (revert immediately,
    pause that rig 7d, notify Overseer).
  - SEV-2: secrets leaked into a test (close PR, run gitleaks scan
    on the rig, pause rig 7d, notify Overseer).
  - SEV-3: maintainer-reject rate >50% over 5 PRs on a rig (auto-
    pause that rig 7d, notify Overseer for cooldown / detuning).
- **Pre-push secret scan** (gitleaks) is a v1 MUST gate (was in the
  Important-but-non-blocking list in the synthesis; promoting to MUST).

**Q7: Locking model + open-state semantics?**
**A:** **Pinned-state-bead per rig used as compare-and-set lock with
explicit state machine.**
- Bead ID: `<rig>-auto-test-state` (pinned, Mayor-owned, NEVER polecat-
  owned per gu-gal8). One per opted-in rig.
- States: `idle | picking | dispatched | mr-pending | mr-revising |
  cooled-down`.
  - `idle`: no work in flight; cycle is allowed to fire.
  - `picking`: cycle is selecting targets (held briefly).
  - `dispatched`: polecat has the bead, no MR yet.
  - `mr-pending`: MR submitted to Refinery, awaiting merge.
  - `mr-revising`: feedback patrol dispatched a follow-up polecat.
  - `cooled-down`: post-merge or post-close; rate-limit window.
- Each transition is recorded with a timestamp + actor (Mayor, polecat
  ID, Refinery, etc.) on the pinned bead's notes.
- **Compare-and-set**: cycle fires only if state == `idle`. Multiple
  Mayor-tick attempts read-modify-write on the bead in a Dolt
  transaction; the second one observes `picking` and exits.
- **Refinery-mode "PR open" definition**: any state in `{dispatched,
  mr-pending, mr-revising}` counts as "open." Coalesce/skip logic
  reads this bead, not GitHub.
- **Race protections:**
  - Cycle fires while feedback patrol mid-revision: state is
    `mr-revising`, cycle exits.
  - Two cycles fire concurrently (Mayor restart): compare-and-set on
    the pinned bead serializes them.
  - Maintainer merges after cycle reads "no PR open": Refinery's
    merge handler is responsible for transitioning the state bead
    from `mr-pending` → `cooled-down`. Cycle re-reads state at fire
    time; if it changed mid-cycle, cycle aborts and retries next tick.

---

### Promoted from "Important But Non-Blocking" → v1 MUST

Several items the synthesis listed as non-blocking are promoted to v1
MUST based on the Q1–Q7 decisions:

- **Pre-push secret scan (gitleaks).** v1 MUST (Q6 SEV-2 requires it).
- **Code-level bot-attribution marker** (`// gt:auto-test-pr origin=`).
  v1 MUST per Q2 diff-marker requirement.
- **Branch GC** for stale `auto-test/<rig>/...` branches with no PR
  after 7 days. v1 MUST (state-machine in Q7 leaves no orphan path
  if branch GC is omitted; orphan branches accumulate).
- **`gt auto-test-pr status` admin command.** v1 MUST per Q6.
- **MVP definition.** v1 MVP = "On `gastown_upstream` only, Refinery-
  mode only, Go-only, ≤1 PR/week, ≤3 files / ≤200 LOC, all 5 quality
  gates from Q2 enforced, with kill-switch CLI shipped." Two
  consecutive merged PRs without intervention = pilot graduates to a
  second rig.
- **Pilot success criteria:** ≥60% merge rate over the first 5 PRs;
  zero SEV-1 or SEV-2 incidents; rejection-rate stable below 40%
  over weeks 2–6.

### Explicitly Deferred to v2

- External-GitHub-PR mode (Q1).
- Loose-mode quality gates for non-pilot rigs (Q2).
- Per-rig GitHub App provisioning workflow (Q3 — only relevant when
  external-PR mode lands).
- Custom test/coverage/lint commands beyond the language allow-list (Q4).
- Auto-extraction of test conventions from the source tree (Q5).
- Cross-language helper packs (Go/TS/Py recipes), beyond the Go pilot.
- Mutation-testing integration as a higher-quality target signal.
- Mayor-level dashboard of "tests added this week per rig."

### Open Questions Resolved

The original draft listed 14 Open Questions. Q1–Q7 above resolve OQ
items 1, 2, 3, 4, 5, 6, 8, 9, 12, 13, 14. Remaining originals:

- **OQ7 — Rate limiting:** ≤1 PR per rig per 7-day window in v1
  (was "twice-weekly" in the draft; tightened given Q6's circuit
  breaker tolerances). Backoff: 3 closes in a row → 7d pause +
  Overseer notify.
- **OQ10 — Flakiness check:** N=10 reruns on new tests + direct
  package only (Q2 MUST gate 4).
- **OQ11 — PR author / banner:** PR body banner + code-level marker
  (`// gt:auto-test-pr origin=...`) per Q2 / promoted MUST.

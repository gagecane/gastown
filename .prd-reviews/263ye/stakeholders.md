# Stakeholder Analysis

## Summary

The PRD identifies a narrow stakeholder set — the overseer and crew workers
maintaining `gagecane/gastown` — and frames the problem exclusively from
their perspective (manual sync burden, CI breakage). This is understandable
for a fork-sync mechanism, but the blast radius is wider than stated.

The most critical gap is that this mechanism pushes directly to `origin/main`
without human review for clean merges, which means every consumer of the
fork's main branch (polecats, CI pipelines, downstream dependents, the
refinery) is a silent stakeholder who will be affected by bad merges they
never consented to receive. Additionally, the upstream project maintainers
(`gastownhall/gastown`) are unnamed but relevant — frequent automated fetches
and the skip-list mechanism both imply a relationship that should be
characterized. Several internal Gas Town subsystems (Witness, Refinery, Mayor
patrols) have operational dependencies on main-branch stability that the PRD
does not acknowledge.

## Findings

### Critical Gaps / Questions

- **Polecats and their in-flight work are invisible stakeholders of
  main-branch mutation.**
  - The mechanism pushes merged upstream commits directly to `origin/main`.
    At that moment, every polecat worktree that branched from a prior main
    state is now out of date. If an upstream merge introduces a breaking
    change to a file a polecat is mid-edit on, that polecat's eventual
    `gt done` will produce a merge conflict in the Refinery queue — or worse,
    a silent semantic conflict that passes CI but introduces a bug.
  - Why this matters: the rig has ~8+ active polecats. A daily sync that
    touches shared infrastructure (e.g., `cmd/gt/main.go`, test helpers,
    `go.mod`) can silently invalidate in-flight work across the fleet.
  - Suggested clarifying question: *What is the protocol when a sync merge
    touches files that active polecats are currently editing? Should the
    Witness notify active polecats? Should Refinery detect and rebase
    in-flight branches?*

- **No human reviews the merge before it lands on main.**
  - For "clean merges" (no git conflicts), the PRD says: merge, push, log a
    bead, done. But a merge can be conflict-free in git terms yet
    semantically broken. Upstream may rename a function that the fork calls
    under a different import path, or change behavior the fork depends on.
    The fork is ~350 commits ahead with custom features — semantic conflicts
    are likely.
  - Why this matters: the entire CI-stays-green goal relies on "clean merge =
    safe merge," which is only true if the fork's customizations don't
    interact with upstream changes. Given 350 commits of divergence, this
    assumption will fail.
  - Suggested clarifying question: *Should clean merges go through a PR (or
    at least a CI gate) before landing on main? If not, what is the rollback
    procedure when a "clean" merge breaks the fork?*

- **The Refinery is an unnamed operational stakeholder.**
  - Refinery processes polecat work by merging branches into main. If an
    automated upstream sync pushes to main between when a polecat branch was
    created and when Refinery attempts its merge, Refinery may encounter
    unexpected conflicts or need to rebase. The PRD does not describe how the
    sync mechanism coordinates with Refinery's merge queue.
  - Why this matters: two systems pushing to the same branch (`origin/main`)
    without coordination is a race condition. Refinery could be mid-merge
    when the sync pushes, causing a rejected push or split-brain history.
  - Suggested clarifying question: *How does the sync mechanism coordinate
    with Refinery? Is there a lock, a queue priority, or a "pause sync while
    Refinery is merging" protocol?*

- **Upstream maintainers (`gastownhall/gastown`) are unnamed but affected.**
  - The mechanism fetches from upstream on a cadence. If the frequency is
    high (every 6 hours), this creates a predictable traffic pattern against
    the upstream repo. More importantly, the "skip-list" mechanism (for
    intentional divergence) implies the fork is making judgments about
    upstream commits — some are "wanted," some are "skipped." If upstream
    maintainers become aware of this, it characterizes the fork's relationship
    to the project (selective consumer vs. full downstream).
  - Why this matters: if `gastownhall/gastown` is open source, the fork's
    selective skip-list could create confusion about feature parity. If it's
    an internal repo, the owners may have opinions about how their code is
    consumed downstream.
  - Suggested clarifying question: *Are the upstream maintainers aware that
    an automated sync consumer exists? Is there any coordination needed (e.g.,
    tagging breaking changes, maintaining a changelog the sync can parse)?*

### Important Considerations

- **CI/CD pipeline is a cost and correctness stakeholder.**
  - The PRD's Open Question about "CI gating" (run tests after merge before
    pushing) is framed as a latency tradeoff. But from CI's perspective, the
    question is: who pays for the test runs, and what happens when a "clean"
    merge breaks CI? If CI is not gated, broken merges land on main and block
    ALL other work (polecats, crew, Refinery) until someone fixes it. If CI is
    gated, the sync needs a workspace with sufficient resources to run the
    full test suite.
  - The PRD should name the test-before-push decision as a stakeholder
    tradeoff (speed for the sync mechanism vs. safety for everyone else), not
    just a latency question.

- **Crew workers have conflicting needs around conflict resolution.**
  - The PRD says conflicts "escalate to crew with full context." But crew
    workers are also doing their regular assigned work. An upstream sync
    conflict is unplanned interrupt work that arrives on a cadence the crew
    didn't choose. If upstream is actively developed, conflicts could arrive
    multiple times per week, effectively making a crew worker the permanent
    "merge conflict resolver" for a relationship they didn't create.
  - Suggested mitigation: name a specific crew member or role as the
    sync-conflict owner, or define a priority for conflict-resolution beads
    relative to other crew work. Consider whether conflict resolution should
    be a polecat task (with human-authored resolution strategy) rather than
    an ad-hoc crew interrupt.

- **The Witness is operationally dependent on main-branch stability.**
  - Witness monitors polecat health, detects stuck sessions, and validates
    work output. If the sync mechanism silently pushes to main and breaks
    something, the Witness may observe "all polecats are failing tests" and
    misdiagnose it as a polecat fleet issue rather than a bad sync. The
    Witness needs awareness that main-branch mutations can come from the sync
    mechanism.
  - Suggested resolution: sync beads should be labeled/tagged so the Witness
    can correlate "main changed via sync at T" with "polecat failures started
    at T+1."

- **The overseer's "when was the last sync?" observability need (Scenario 4)
  understates the real requirement.**
  - The real observability need is: "what upstream commits are on my fork's
    main, what's pending, what's skipped, and is there a problem right now?"
    A `bd list --label=upstream-sync` query answers the first question but
    not the last three.
  - The overseer also needs: "undo the last sync" (rollback), "pause syncing"
    (kill switch), and "show me what would sync next" (dry run). None of these
    are mentioned as operator tooling.

- **The skip-list is a governance surface with no named owner.**
  - Who decides what goes on the skip-list? The overseer? A crew member? Can
    a polecat add to it? The skip-list is effectively a policy document ("we
    intentionally reject these upstream decisions") that will grow over time
    and needs periodic review. No owner or review cadence is named.

### Observations

- **Conflicting need: "zero human intervention for clean merges" vs.
  "CI stays green."**
  These are in tension. Zero intervention means no review gate. CI-green
  means guaranteeing correctness. The only way both are true simultaneously
  is if clean merges are always semantically safe — which is unlikely given
  350 commits of fork divergence. The PRD should pick a primary: either
  "always fast, sometimes broken" or "always safe, sometimes slow."

- **Conflicting need: "within 24 hours" freshness vs. "escalate conflicts
  to crew."**
  If conflicts take days to resolve (crew is busy, the conflict is complex),
  the 24-hour goal is unmet for conflict cases. This is fine — but the PRD
  presents 24 hours as a blanket goal rather than a clean-merge-only SLA.
  Setting expectations correctly avoids future "why is this broken" from
  stakeholders who read "24 hours" as a guarantee.

- **The "merge vs. rebase" Open Question has stakeholder implications.**
  Merge commits create noise in `git log` for every developer who reads
  history. Rebase rewrites history (forbidden by the PRD's own constraints).
  The choice affects the daily experience of every developer reading the
  fork's git history. This should be framed as a stakeholder tradeoff, not
  a technical preference.

- **Batch vs. incremental has conflict-resolution stakeholder impact.**
  Batch merge (all upstream commits at once) makes conflicts harder to
  attribute — the crew resolver sees one giant conflict across many upstream
  changes. Incremental (one commit at a time) is kinder to the conflict
  resolver but slower for the mechanism. The crew worker resolving conflicts
  is the stakeholder who should influence this decision.

- **Internal team dependencies not enumerated:**
  - **Mayor patrol system** — if this becomes a patrol, the Mayor's scheduling
    and priority logic must accommodate it. Is the Mayor team consulted?
  - **`gt` CLI maintainers** — new commands like `gt upstream-sync status` or
    `gt upstream-sync skip` add surface area to the CLI.
  - **Dolt server** — every sync bead is a Dolt commit. Daily syncs add
    365 beads/year minimum. Is this meaningful load?

- **Launch coordination checklist (unnamed):**
  At minimum, these parties need notification before enabling:
  - All active polecats (their branches may be invalidated by syncs)
  - Refinery operators (new source of main-branch mutations)
  - Witness (new failure mode to detect: "broken by sync, not by polecat")
  - Upstream repo watchers (if the fetch pattern is detectable)
  - Any downstream consumers of `gagecane/gastown` artifacts

## Confidence Assessment

**Medium-High.** The stakeholder set for a fork-sync mechanism is inherently
smaller than for a code-generation mechanism (like auto-test-pr), because the
blast radius is bounded to one fork's main branch rather than arbitrary repos.
The critical gaps are real but tractable: the Refinery coordination race, the
"clean merge != safe merge" assumption, and the lack of operator tooling
(rollback, pause, dry-run) are all solvable within the current architecture.

The biggest risk is the unstated assumption that git-clean merges are always
semantically safe across 350 commits of divergence. This assumption will fail,
and when it does, the fallout hits every downstream consumer of `origin/main`
simultaneously. Gating clean merges behind a CI run (even without human review)
would eliminate this risk class entirely, at the cost of the "zero human
intervention" goal becoming "zero human intervention, 20-minute CI delay."

The conflicting-needs surface is smaller here than in auto-test-pr: the main
tension is speed-vs-safety, and the PRD can resolve it by clearly stating
whether CI-before-push is required. All other conflicts (crew interrupt burden,
batch-vs-incremental, merge-vs-rebase) are preference decisions that can be
made without architectural changes.

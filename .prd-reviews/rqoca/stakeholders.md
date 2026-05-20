# Stakeholder Analysis

## Summary

The PRD names a small set of stakeholders explicitly — rig maintainers,
crew/Mayor, polecats, Refinery — and centers the design on the
maintainer ↔ bot review loop. That framing is sound for the happy path.
But "a bot opens code-modifying PRs against your main branch on a
cadence" touches a much wider blast radius than the PRD acknowledges.
At least eight categories of affected actors are not mentioned: PR
reviewers who are not the rig maintainer (CODEOWNERS), external-repo
human maintainers who never opted in, original file authors whose code
becomes the bot's target, on-call / support owners for when the bot
misbehaves, security and compliance owners for the bot's commit
identity, CI / infra cost owners, the new-contributor onboarding
experience, and (recursively) the polecat pool itself competing with
its regular workload.

Of the stakeholders the PRD does name, several pairs have genuinely
conflicting needs that the document glosses over: maintainer review
burden vs. crew throughput goals, polecat fleet idle-utilization vs.
real-work prioritization, quality-floor strictness vs. cycle latency,
and gu-gal8's "polecat files no beads" rule vs. the polecat's need to
report a real bug it stumbles into mid-cycle. None of these conflicts
is fatal, but each is currently resolved in the PRD by the implicit
answer "trust the implementer," which is exactly the kind of decision
a stakeholder review is meant to surface.

## Findings

### Critical Gaps / Questions

- **PR reviewers who are not the rig maintainer are invisible in the
  PRD.**
  - The PRD repeatedly refers to "the maintainer" as if each rig has
    one. In practice a rig has CODEOWNERS, a rotation of reviewers,
    and team review queues. A bot that files a PR will auto-page
    whichever humans are listed as code owners on the touched files,
    which can include senior engineers who were never consulted about
    enabling the mechanism. Goal 2 ("single config flip per rig")
    binds the *rig owner*, not the *file owners* whose review queue
    the bot will fill.
  - Why this matters: review fatigue is the load-bearing assumption of
    the whole project (S1 says "merged within a day"). If the people
    actually paying that cost are not the people opting in, adoption
    will be felt as a top-down imposition and the cooldowns / kill
    switches will be invoked socially, not technically.
  - Suggested clarifying question: *Beyond the rig-level opt-in, is
    there a per-file or per-CODEOWNERS opt-out? Who is consulted —
    versus merely affected — when a rig is enabled?*

- **External-repo maintainers can be affected without opting in.**
  - The Constraints section says "for external/community repos we use
    `gh pr create` directly," and the Rough Approach mentions that
    rigs differ in mode. But the opt-in surface is *the rig's own
    config* (Open Q13), not the upstream repo's policy. A rig that
    targets a third-party repo can flip `enabled = true` and start
    opening PRs against a project whose maintainers never agreed.
    Open-source maintainers are increasingly hostile to AI-generated
    PRs (curl, Bitcoin Core, etc. have published rejection policies).
  - Why this matters: a single rig misconfiguration can damage Amazon
    / Anthropic reputation in an external community. This is not a
    per-rig blast radius, it is a project-wide one.
  - Suggested clarifying question: *For external repos, is there an
    explicit allowlist of upstreams that have pre-agreed to receive
    auto-test PRs? Should v1 disable the external-repo path entirely
    (deferring to v2) until that allowlist policy is designed?*

- **No on-call / support owner is named.**
  - The mechanism modifies main branches across opted-in rigs on a
    cadence. When it misbehaves — opens duplicate PRs (Open Q4
    failure), mass-spams a rig because the cooldown logic regressed,
    leaks credentials via PR body, or lands a behavior-freezing test
    that masks a real regression (Goal 6) — there must be someone to
    page. The PRD names Mayor as the owner of pinned-bead state but
    not as the on-call for bot misbehavior. Witness "monitors
    polecats" but not "monitors bot PR output quality."
  - Why this matters: this is the single largest missing piece. A
    system that auto-modifies code in production with no on-call is
    an outage waiting to happen. Compare with how every long-running
    Amazon service has explicit oncall ownership before launch.
  - Suggested clarifying question: *Who is on-call for an auto-test-PR
    incident? What is the paging path? What is the SEV triage tree
    for "the bot opened 50 PRs in 20 minutes" vs. "the bot landed a
    flaky test that's now blocking main"?*

- **Security / compliance team has no surface in this PRD.**
  - Open Q11 mentions PR attribution but treats it as branding. The
    actual security questions are unanswered: Does the polecat sign
    its commits? With what key? Is the GitHub identity a shared
    service account or a per-polecat identity? What is the credential
    rotation procedure? What is the audit trail when something is
    pushed by "the bot" — can we always answer "which polecat
    session, on which rig, at which timestamp"? For repos under SOX /
    audit / PCI scope, change-management may require named human
    approvers; bot-authored commits may be disallowed entirely.
  - Why this matters: bot-authored commits to main branches are a
    standard security-review item. Skipping that conversation now
    means re-doing it later under audit pressure.
  - Suggested clarifying question: *Has the security team reviewed
    the bot-identity model? Are there rigs (compliance-sensitive)
    that must be excluded by policy, not by per-rig config? What is
    the audit-trail commitment for a bot-authored commit?*

- **The polecat pool is a stakeholder competing with itself.**
  - Auto-test cycles consume polecat slots that could be running
    real, user-filed work. The PRD says "Mayor must be able to
    deprioritize it" (Constraints) but does not specify the priority
    relationship. If a rig has 4 polecats and 4 user-filed tasks
    pending, does the auto-test cycle skip? Defer? Steal one slot?
    What if a user-filed task arrives mid-cycle?
  - Why this matters: the entire "convert idle capacity into quality"
    framing rests on idle being a real signal. If auto-test cycles
    fire whenever the queue *happens* to be empty for 30 seconds, the
    next user task pays a polecat-busy penalty.
  - Suggested clarifying question: *Define the priority relationship:
    is auto-test work strictly the lowest priority (preempted by any
    user-filed work)? If preemption is not implemented in current
    Mayor / sling code, is implementing it part of this project?*

### Important Considerations

- **Original file authors are not mentioned as stakeholders.**
  - The author of a recently-churned file under test is, in
    git-blame terms, the human most likely to know whether a given
    test is meaningful. The bot will write tests against their code
    with no consultation — which is fine for some authors and
    annoying for others (e.g., authors mid-refactor whose
    soon-to-be-deleted code paths are getting frozen by tests).
    Open Q9's "behavior-freezing" risk lands directly on these
    people: they are the ones who will close the PR with "this is
    about to be removed." The cooldown will then engage on the file
    they're most actively working on.
  - Suggested mitigation: add the file's last-3-month committers to
    the PR's reviewer set, or skip files with active in-flight
    PRs touching the same symbols (heuristic: recent PR open against
    `path/to/file.go` → cool down that file in target-pick).

- **CI / build-infra owners are an unstated cost center.**
  - The quality-floor strategy (Open Q9, Q10) involves running the
    test suite N=10 times for flakiness, plus mutation-sanity (one
    line at a time). On a rig with a 20-minute test suite, that is
    200+ minutes of CI per cycle. Multiplied across 10 rigs at twice
    a week = 67 hours of CI per week from this mechanism alone. The
    PRD does not name a budget owner or a per-rig cost ceiling.
  - Worth pinning a per-cycle wall-clock budget (e.g. "≤30 min total
    CPU per cycle, abort otherwise") and naming the owner who pays.

- **GitHub API rate limits / labels are an external dependency that
  isn't enumerated.**
  - The mechanism creates labels (`gt:auto-test-pr`), branch names
    (`auto-test/<rig>/<slug>`), PR bodies, and reads PR state every
    cycle (Open Q5). On a fleet of 30 rigs all opted in, this is a
    measurable load on the GitHub Enterprise API — and one bad
    cycle (e.g., 14 mass-PR slop event) can saturate the rate limit
    for normal human work in the same org. No stakeholder is named
    for the GHE rate-limit budget.
  - Suggested resolution: name the GHE-admin / SCM-admin team as a
    consulted stakeholder, and commit to a max-API-calls-per-cycle
    budget.

- **New contributors / code-culture is an indirect stakeholder.**
  - When a new engineer joins and reviews a bot-authored test PR,
    they may not know it is bot-authored. Without a strong banner /
    visual marker, they will treat it as a peer's work, push back
    less, accept lower-quality assertions, and learn that "this is
    how we write tests on this rig." The bot becomes an implicit
    style-setter for tests.
  - Open Q11 proposes a banner; this stakeholder concern argues for
    making the banner mandatory and high-contrast, not optional, and
    for embedding a comment marker in the test source itself (not
    just the PR body) so the bot-authored origin survives merge.

- **The "real bug found mid-cycle" path conflicts with gu-gal8.**
  - The Rough Approach says "if a generated test surfaces a real
    bug, we file a separate bead — we do NOT bundle a fix." Good.
    But *who* files that bead? gu-gal8 forbids polecat-self-file.
    The Mayor is not in the polecat's session. The PRD's answer is
    silent.
  - Suggested resolution: the polecat surfaces the finding via the
    Mayor-owned dispatch contract — e.g., the polecat returns a
    structured "bug-discovered" status on its work bead, and the
    Mayor's next cycle reads that signal and files the bug bead.
    Worth naming this path in the PRD because otherwise it
    will silently become "polecat files a bug bead and wallpapers
    over gu-gal8."

- **Documentation owners / runbook owners are unnamed.**
  - This mechanism needs at least: an opt-in/opt-out doc for rig
    owners, a runbook for support, a "what is this PR?" link target
    for the PR body banner, a security-model doc for compliance.
    No one is named as accountable for any of these.

### Observations

- **Conflicting need: maintainer review-budget vs. crew throughput
  goal.** The PRD wants "a steady drip" (crew goal) AND "small enough
  to review in one sitting" (maintainer goal). These compete: a rig
  with tight review budget will want fewer / larger PRs to amortize
  context-switch cost; the PRD instead optimizes for many / small.
  Worth naming this tradeoff explicitly so a future tuning conversation
  has somewhere to land.

- **Conflicting need: quality-floor strictness vs. cycle latency.**
  Open Q9–10 pile defenses (heuristic linter + mutation sanity +
  flakiness re-runs + diff-marker comments). Each defense raises the
  per-cycle wall-clock and CI cost. Strong quality floor → slow
  cadence; fast cadence → weak floor. The PRD does not pick a point
  on this curve.

- **Conflicting need: opt-in simplicity vs. fine-grained governance.**
  Goal 2 wants "single config flip." But the file-cooldown ledger
  (Open Q4), per-file CODEOWNERS opt-out (Critical Gap above), per-rig
  cadence (Open Q1), per-language commands (Open Q13), and the
  rejection-history rate limit (Open Q7) all push toward a richer
  config surface. Each is justified individually; together they are a
  config explosion at odds with Goal 2.

- **Conflicting need: "bot pushes new commits to existing branch"
  (S3) vs. force-push-aversion of human reviewers.**
  Most maintainers prefer review-then-rebase or fixup commits over
  arbitrary new commits on a branch they are mid-review. The PRD does
  not specify whether the bot's revisions are appended commits, fixup
  commits, or force-pushes. Each has different review ergonomics.

- **Internal team dependencies the PRD does not name:**
  - **`mol-pr-feedback-patrol` owners** — Open Q6 reuses their
    molecule. Have they been consulted? (Scope review flagged this.)
  - **Refinery team** — needs to recognize the `gt:auto-test-pr`
    label / branch-prefix and route appropriately. Are MQ priorities
    aware of this new traffic source?
  - **Witness team** — when a polecat running this formula misbehaves
    (e.g. emits a 500-LOC test diff), the existing witness scope checks
    must catch it. New formula → new shape of "bad output" to watch
    for.
  - **`gt rig config` owners** — Open Q13 says rig-level config doesn't
    have a clear home today. This is a cross-component dependency
    (build the config surface, then build this on top).

- **Launch-coordination notifications the PRD does not enumerate:**
  At v1 launch, who must be told? Suggested list:
  - Each opted-in rig's owner (consent confirmation)
  - Each opted-in rig's CODEOWNERS (heads-up that they will start
    seeing bot PR review pings)
  - Refinery team (new label / branch prefix)
  - Witness team (new formula on the fleet)
  - Mayor operators (new cadence in the daemon, new pinned bead
    shape)
  - Compliance / security (bot-identity review)
  - Internal devx / docs (opt-out runbook published)

- The PRD's pilot choice (`gastown_upstream`, S1 / Open Q14) is
  reasonable in selection but the rig is also where this PRD is being
  reviewed. Self-reviewing rigs is fine but the implicit alignment of
  reviewer / maintainer / pilot host means dissenting voices from
  *other* rigs do not enter the v1 design — selection bias for
  generalization. Consider naming a non-Go rig as a "v2 first
  generalization target" up front so its constraints are at least
  noted.

## Confidence Assessment

**Medium.** The named stakeholders (rig maintainers, crew, Mayor,
polecats) are reasonable but the silent stakeholder set is larger than
the named one, and several of the silent ones (security/compliance,
external-repo human maintainers, CODEOWNERS who are not the rig owner)
have veto power in practice — meaning their absence from v1 design
does not mean they are uninvolved, only that their concerns will hit
during pilot or later as a costly retrofit.

The biggest gap is the absence of an on-call / support owner for a
mechanism whose blast radius spans every opted-in rig's main branch.
Closing that single gap (a named owner, a kill-switch, a SEV-tree)
would resolve a large fraction of the other concerns by giving the
mechanism a clear human accountability surface. Without it, the PRD
implicitly distributes the failure cost across whichever maintainers
happen to be paying attention.

The conflicting-needs review surfaces real tradeoffs (review burden
vs. throughput, quality vs. latency, simple opt-in vs. fine-grained
governance) that the PRD currently resolves by deferring. That is
acceptable for a design sketch but each tradeoff is a hidden v2
project unless it is explicitly priced in.

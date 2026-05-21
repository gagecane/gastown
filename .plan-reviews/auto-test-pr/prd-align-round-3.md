# PRD Alignment Round 3 — auto-test-pr

> Round 1 covered PRD requirements + goals. Round 2 covered constraints
> + non-goals. Round 3 (this log) covers user-stories + open-questions.

**Bead:** gu-wfs-6ihhi
**PRD:** `.prd-reviews/auto-test-pr/prd-draft.md` (commit `13d14a44`)
**Plan:** `.designs/auto-test-pr/synthesis.md` (post-round-2 commit
`712f5ef8`)
**Reviewer:** fury (inline; polecats can't sling, so both reviewer
roles were performed by fury in one session — same pattern as rounds
1 and 2)

## Reports

Both review reports are inlined below for reproducibility.

- **User-stories-coverage report** — see report 1 below.
- **Open-questions-resolution report** — see report 2 below.

## Consolidated must-fix list (applied to design doc)

The two reports surfaced 3 issues. Items applied directly to
`.designs/auto-test-pr/synthesis.md`:

14. **S1 — state machine has no `cooled-down → idle` edge.**
    *PRD §User Stories S1:* "Twice a week the mechanism wakes up,
    picks 2-3 under-tested branches in a recently-changed file,
    drafts tests, opens a PR. The maintainer reviews and merges
    within a day. No PR is open when the next cycle ticks; a new one
    gets opened." *Plan gap:* the state machine drawn in §Data Model
    is `idle → picking → dispatched → mr-pending → cooled-down`,
    plus `mr-pending ↔ mr-revising`. There is **no edge out of
    `cooled-down`.** Refinery's merge handler transitions `mr-pending
    → cooled-down` (Key Components §1 step 6), but nothing
    transitions `cooled-down → idle`. Concrete failure mode: pilot
    rig fires its first cycle, the MR merges, the rig enters
    `cooled-down`, and then never fires again — S1 is unreachable.
    *Fix:* added **D18** ("Cooldown-release transition is automatic
    and Mayor-driven"). Mayor's hourly tick adds a step before
    state-read: for each opted-in rig in `cooled-down`, if
    `now - last_transition.at >= cadence_days * 24h`, CAS-transition
    `cooled-down → idle`. Failed CAS is benign (next tick retries).
    Polecat is uninvolved (gu-gal8). `paused-by-circuit-breaker`
    (D16) does NOT auto-release — that requires explicit
    `gt auto-test-pr resume`. State-machine diagram updated to show
    the `cooled-down → idle (after cadence_days)` edge. Added **R22**
    to the risk register.

15. **S3 — revise mode never replies to the originating comment
    thread.**
    *PRD §User Stories S3:* "The maintainer comments...The
    mechanism (or the existing PR feedback patrol) reads the
    comments, dispatches a polecat, the polecat pushes a new commit
    to the same branch, **the comment thread is replied to.**" *Plan
    gap:* D17 (manual revision CLI) and Phase-2 task 14 (feedback-
    patrol routing) both describe how a revision is dispatched and
    how a new commit is pushed, but **neither specifies a reply
    mechanism on the originating comment thread.** A maintainer who
    left a comment would see no signal that the polecat acted on it
    — they'd have to manually diff the branch to find out. PRD
    explicitly names the reply as part of S3's happy path.
    *Fix:* added **D19** ("Reviewer comment threads are replied to
    in revise mode"). `mol-polecat-work-test-improver` in
    `mode=revise` emits a structured reply on each comment thread
    referenced in `args.revision.comments[]` after the new commit is
    pushed. The reply names: (a) new commit SHA, (b) gates passed,
    (c) one-line summary of what changed. Replies route through the
    same channel as the MR (Refinery v1: bead-comment threaded
    against the review-comment bead; v2 external mode: GitHub PR
    review reply). For `revise --mr=<id>` invocations without
    `--comment-id`, the polecat picks the most recent non-resolved
    thread and uses a generic "manual revision dispatched by <user>"
    template. Phase 1 task 12a (manual CLI) and Phase 2 task 14
    (feedback-patrol routing) both ship this reply step. Added
    **R23** to the risk register.

16. **OQ2 — `size_budget` envelope is dispatched but no gate
    enforces it.**
    *PRD §Open Questions Q2:* "PR size cap — exactly what?
    Proposed: ≤200 added test LOC, ≤3 files touched. **Need to
    decide whether this is enforced by the polecat itself (refuses
    to write more) or by a post-check that discards over-budget
    candidates.**" *Plan gap:* the dispatch-bead JSON envelope
    carries `size_budget: {max_files: 3, max_loc: 200}` (Q5
    decision), and goal G5 / R-table item R-PRD-G5 reference the
    cap, but **no gate in §Key Components §2 verifies the polecat
    actually respected it.** Gates 4a-4f cover coverage delta,
    mutant sanity, flakiness, tautology, gitleaks, and allow-list,
    but none counts files or LOC. A polecat that ignores the budget
    (whether through model-judgment failure or prompt injection)
    would slip past every gate and surface a 500-LOC / 8-file MR —
    exactly the reviewer-fatigue failure mode R1 names.
    *Fix:* added **D20** and a new **gate 4g (size-budget
    enforcer)**. After the test files are written but before MR
    creation, gate 4g counts files added/modified in the diff and
    added test LOC; hard-fails if either exceeds the dispatched
    envelope's budget (defaults: 3 files, 200 added test LOC). The
    polecat is still *told* the budget in the dispatch envelope (so
    it tries to stay within it), but the gate is the source of
    truth — structural, not model-judgment-based. This resolves OQ2
    explicitly with a "post-check" answer: the gate is the
    structural defense; self-enforcement is best-effort guidance.
    Failure exits the polecat with NOTES; no MR is opened. Added
    **R24** to the risk register.

## Items that turned out to already be covered (no fix needed)

Most user stories and open questions are addressed in earlier rounds
or in the original synthesis. Listed for completeness so an auditor
can see why they're not in the must-fix list:

- **S2 — coalesce when PR open:** Q7 state machine + cycle step 1
  read of `<rig>-auto-test-state` skip-when-not-idle. Clean.
- **S4 — per-file rejection cooldown:** added in round 1 (item 9 →
  cycle step 4 21-day per-file rejection-log filter). Clean.
- **S5 — broken test → no PR:** the five quality gates exit the
  polecat with NOTES on failure; no MR is opened. Clean.
- **S6 — disable while in-flight:** added in round 1 (D2a "disable
  does NOT cancel in-flight work"). Clean.
- **OQ1 (lifecycle):** §Overview + Key Components §1 — standing
  patrol, hourly tick, per-rig cadence via `cadence_days`. Clean.
- **OQ3 (target selection):** Key Components §1 step 4 + cycle-
  level rejection cooldown (round 1) + within-file churn-proximity
  ranking (round 2). Clean.
- **OQ4 (existing-PR detection):** D14 ("`gt:auto-test-pr` label is
  bead-applied"); state bead is authoritative; multi-signal in v2.
  Clean.
- **OQ5 (authoritative state):** Q7 / D4 — pinned bead is
  authoritative; town bead is read-cache. Clean.
- **OQ6 (feedback handling reuse):** D3 + Phase 2 — additive
  extension of `mol-pr-feedback-patrol`, not a rewrite. Clean.
- **OQ7 (rate limiting):** Q6 PRD-review decision (≤1/week + Q6
  circuit breaker). Clean.
- **OQ8 (polecat reuse):** §Key Components §2 — spawn-per-cycle via
  sling-context, no long-lived agent. Clean.
- **OQ9 (tautology / low-value detection):** Q2 PRD-review decision
  + gate 4d (four sub-rules from round-1 fix). Clean.
- **OQ10 (flakiness check):** Q2 / gate 4c — N=10 reruns on direct
  package only. Clean.
- **OQ11 (PR author / banner):** Q3 / D8 / D14 — polecat-as-author
  + machine-generated MR banner + greppable code marker. Clean.
- **OQ12 (bead bookkeeping):** Q7 / D4 — Mayor-owned pinned beads,
  polecat never writes them (gu-gal8 enforced at bead-client layer
  via security C-SEC-5 + R5). Clean.
- **OQ13 (per-rig opt-in surface):** D2 — settings JSON, operator
  authority. Clean (with OQ1 of the design doc still flagged).
- **OQ14 (pilot rig):** Q1 / Phase 1 — `gastown_upstream`. Clean.
- **Q1-Q7 + Promoted-to-MUST + Explicitly Deferred:** all handled
  by rounds 1-2 plus the original synthesis. Clean.

## Cross-round summary

- Round 1 (gu-wfs-ovn44, requirements + goals): 6 must-fix + 4
  should-fix → 10 fixes total (D2a, D15, D16, D17, gate 4a/4d/4f
  tightenings, Phase 0 tasks 9-11, Phase 1 step 12a, Phase 1 exit
  criteria, target-pick rejection-cooldown, bug-discovery NOTES
  protocol; R15-R19).
- Round 2 (gu-wfs-et5vw, constraints + non-goals): 3 must-fix
  (D2b C2 scope-clarification, gate 4f Test*-form check, cycle step 4
  within-file churn-proximity ranking; R20-R21).
- Round 3 (this round, user-stories + open-questions): 3 must-fix
  (D18 cooldown-release transition + state-machine edge, D19
  reviewer-comment-thread reply step in revise mode, D20 gate 4g
  size-budget enforcer; R22-R24).

Total across the three rounds: **12 must-fix + 4 should-fix = 16
fixes applied to `.designs/auto-test-pr/synthesis.md`.**

---

## Report 1: User-stories coverage

**Reviewer:** fury (inline)
**PRD:** `.prd-reviews/auto-test-pr/prd-draft.md` (commit `13d14a44`)
**Plan:** `.designs/auto-test-pr/synthesis.md` (post-round-2)

Walk-through of every USER STORY and SCENARIO in the PRD. For each,
trace the end-to-end user journey through the plan. Verify every step
in the scenario is covered by a plan task.

### S1 — Steady drip on a healthy rig

*Scenario:* "Rig `gastown_upstream` has auto-test-PR enabled. Twice
a week the mechanism wakes up, picks 2-3 under-tested branches in a
recently-changed file, drafts tests, opens a PR. The maintainer
reviews and merges within a day. No PR is open when the next cycle
ticks; a new one gets opened."

End-to-end trace through the plan:
- "Has auto-test-PR enabled" → Phase 1 task 11 (`enabled=true` in
  settings JSON). ✓
- "Twice a week the mechanism wakes up" → Mayor cycle ticks hourly;
  fires only if `cadence_days` elapsed. Phase 1 cadence default is 7
  days (≤1/week). The PRD's "twice-weekly" is the original draft;
  Q-decisions in the PRD body tightened to ≤1/week (OQ7 in
  clarifications). The plan tracks the post-clarification cadence
  (7d), so "twice a week" in S1 is *over-specified* relative to the
  ratified Q-decisions. **No fix needed** — S1 wakeup-mechanism is
  covered, just at the tightened cadence. ✓
- "Picks 2-3 under-tested branches in a recently-changed file" →
  cycle step 4 ranks by churn × uncovered_branches; 30-day churn
  window; round-2 within-file churn-proximity ranking. ✓
- "Drafts tests" → mol-polecat-work-test-improver implement step. ✓
- "Opens a PR" → polecat's `gt done` produces an MR bead. ✓
- "Maintainer reviews and merges within a day" → D15 maintainer-
  approval gate before Refinery merge. ✓
- "No PR is open when the next cycle ticks; a new one gets opened"
  → **GAP.** Cycle reads state bead; if state == `cooled-down`, it
  skips. But the state machine has **no edge out of `cooled-down`**.
  Once the rig enters `cooled-down`, it never re-enters `idle`, so
  the cycle never fires again. The PRD scenario explicitly requires
  re-firing on the next cadence-elapsed tick. **Must-fix:**
  cooldown-release transition needed. *(Applied as item 14 / D18 /
  R22.)*

*Classification:* PARTIAL → must-fix (item 14).

### S2 — Coalesce when a PR is already open

*Scenario:* "The cycle ticks but PR #487 is still open from the last
cycle. The mechanism detects this and exits — no new PR."

End-to-end trace:
- Cycle ticks → Mayor patrol runs. ✓
- Detects open PR → Q7 state machine. State bead in any of
  {`dispatched`, `mr-pending`, `mr-revising`} = "open." Cycle reads
  state bead first; CAS-transition `idle → picking` fails (state is
  not idle); cycle exits. ✓
- "No new PR" → no dispatch bead is filed; no polecat is slung. ✓

*Classification:* COVERED. Clean.

### S3 — Reviewer leaves comments

*Scenario:* "The maintainer comments 'this mock is too coupled — use
the test-helper in `internal/testutil`.' The mechanism (or the
existing PR feedback patrol) reads the comments, dispatches a
polecat, the polecat pushes a new commit to the same branch, **the
comment thread is replied to.**"

End-to-end trace:
- "Maintainer comments" → bead-comment on MR bead in v1. ✓
- "Reads the comments, dispatches a polecat" → Phase 1 manual
  fallback `gt auto-test-pr revise --mr=<id> --comment-id=<id>`
  (D17); Phase 2 automated via `mol-pr-feedback-patrol` label-keyed
  dispatch (D3 + task 14). ✓
- "Polecat pushes a new commit to the same branch" → revise mode of
  `mol-polecat-work-test-improver`; same branch, new commit. ✓
- "The comment thread is replied to" → **GAP.** Plan describes
  routing and commit-pushing but **no reply mechanism is
  specified.** A maintainer who left a comment would see no signal
  that the polecat acted on their feedback. PRD explicitly names
  the reply as a step in the scenario. **Must-fix:** reply step
  needed in revise mode. *(Applied as item 15 / D19 / R23.)*

*Classification:* PARTIAL → must-fix (item 15).

### S4 — Reviewer rejects

*Scenario:* "The maintainer closes the PR with 'wrong target — that
file is intentionally untested.' The mechanism records that
rejection, backs off (rate limit), and avoids retargeting that file
for some cooldown period."

End-to-end trace:
- "Closes the PR" → Refinery's merge handler observes close-unmerged,
  emits a nudge → Mayor transitions `mr-pending → cooled-down` and
  appends a rejection record to `<rig>-auto-test-state.
  rejection_log[]`. ✓
- "Records that rejection" → cycle's step 6 (Mayor close-handler)
  appends `rejection_log[].target_path` and timestamp. Round-1 fix
  (item 9) wired this. ✓
- "Backs off (rate limit)" → ≤1/week cadence; `cooled-down` blocks
  re-fire; circuit-breaker on 3 consecutive closes (Q6). ✓
- "Avoids retargeting that file for some cooldown period" → cycle
  step 4 21-day per-file rejection-log filter (round 1 item 9). ✓

*Classification:* COVERED. Clean.

### S5 — New test breaks the build

*Scenario:* "A test the polecat wrote fails locally. The mechanism
does NOT push it. It either retries with a revision or abandons the
cycle quietly. No PR ever appears with a red build."

End-to-end trace:
- "Test fails locally" → gate 4c (flakiness re-run) catches
  non-determinism; gate 4a (coverage delta ≤ 0) catches no-op tests;
  gate 4b (mutant sanity) catches assertion-free tests; gate 4d
  (tautology) catches literal tests. ✓
- "Does NOT push it" → polecat exits with NOTES on any gate failure;
  no `gt done`, no MR. ✓
- "Retries or abandons quietly" → cycle-failure backoff (D12) — the
  cycle's per-rig cadence budget is not consumed on failure; next
  cycle attempts again at the next scheduled tick after a 24h
  failure-backoff. ✓
- "No PR ever appears with a red build" → structural — the polecat
  never reaches `gt done` if any gate fails. ✓

*Classification:* COVERED. Clean.

### S6 — Rig opt-out

*Scenario:* "A rig owner sets `auto_test_pr.enabled = false`. Next
cycle, the mechanism skips that rig. **Any in-flight PR is left
alone (the human can merge or close it manually).**"

End-to-end trace:
- "Sets `enabled = false`" → `gt auto-test-pr disable --rig=<rig>`. ✓
- "Next cycle, the mechanism skips that rig" → cycle step 1 reads
  config; exits if `enabled=false`. ✓
- "Any in-flight PR is left alone" → D2a (round-1 fix) explicitly:
  state bead is left as-is during in-flight states; in-flight MR
  completes its lifecycle (merged or closed by human); Mayor's
  existing transition handlers move state through `cooled-down`
  normally. ✓

*Classification:* COVERED. Clean.

### Summary

| Class | Count |
|-------|-------|
| COVERED | 4 |
| PARTIAL must-fix | 2 |
| GAP | 0 |

The two PARTIAL items (S1 cooldown-release, S3 comment-thread reply)
both stem from the plan describing the *mechanism* of state
transitions and revisions but missing the *user-visible touchpoint*
(rig fires next time; maintainer hears back). Both applied as items
14 and 15.

---

## Report 2: Open-questions resolution

**Reviewer:** fury (inline)
**PRD:** `.prd-reviews/auto-test-pr/prd-draft.md` (commit `13d14a44`)
**Plan:** `.designs/auto-test-pr/synthesis.md` (post-round-2)

Walk-through of every Open Question — both the original §Open
Questions section (OQ1-OQ14) and the questions raised in the
§Clarifications from Human Review section (Q1-Q7). For each, verify
the question is either answered and reflected in the plan, or
explicitly deferred with a plan task to resolve it.

### Original Open Questions (OQ1-OQ14)

- **RESOLVED OQ1 (lifecycle):** "Standing patrol with per-rig
  schedule controlled by config." → §Overview + Key Components §1
  (hourly Mayor tick, per-rig cadence via `cadence_days`). ✓

- **UNRESOLVED OQ2 (PR size cap — polecat self-enforces or
  post-check?):** PRD asks the explicit question; the plan
  *dispatches* `size_budget` in the envelope but **no gate
  enforces it.** A polecat that ignores the budget would slip past.
  *Suggested resolution (must-fix):* add gate 4g size-budget
  enforcer (post-check). *(Applied as item 16 / D20 / R24.)*

- **RESOLVED OQ3 (target selection):** "Files churned in last 30
  days × low coverage; mutation testing deferred to v2." → cycle
  step 4 + round-1 rejection cooldown + round-2 within-file churn
  proximity. ✓

- **RESOLVED OQ4 (existing-PR detection):** "Multi-signal: label,
  branch prefix, body marker." → D14 (`gt:auto-test-pr` label is
  bead-applied in v1, since v1 has no GitHub PR); branch prefix
  `auto-test/<rig>/<bead-id>`; banner is in MR description.
  Authoritative source is the bead state machine. ✓

- **RESOLVED OQ5 (authoritative state):** "Pinned bead is
  authoritative; GitHub state is the cache (in v2)." → D4 + Q7. v1
  has no GitHub interaction so the question collapses to "pinned
  bead is authoritative." ✓

- **RESOLVED OQ6 (feedback handling reuse):** "Reuse
  `mol-pr-feedback-patrol`." → D3 (additive extension) + Phase 2
  task 14. ✓

- **RESOLVED OQ7 (rate limiting):** "≤1/week per rig + 24h cooldown
  + circuit breaker on 3 closes." → tightened in Q6/Q7
  PRD-review decisions; cadence + state machine + Q6 circuit
  breaker. ✓

- **RESOLVED OQ8 (polecat reuse):** "Spawn-per-cycle via
  sling-context envelope." → §Key Components §2; no long-lived
  agent. ✓

- **RESOLVED OQ9 (tautology / low-value detection):** "Layered:
  heuristic linter + mutant sanity + diff-marker comments." → Q2
  PRD-review decision; gates 4a, 4b, 4d (round-1 four-sub-rule
  expansion). ✓

- **RESOLVED OQ10 (flakiness check):** "N=10 reruns." → Q2 / gate
  4c, scoped to direct package only. ✓

- **RESOLVED OQ11 (PR author / attribution):** "Polecat under rig
  identity + body banner + code-level marker." → Q3 / D8 / D14. ✓

- **RESOLVED OQ12 (bead bookkeeping):** "Mayor-owned per gu-gal8;
  polecat receives a bead, never files one." → Q7 / D4 + R5
  bead-client-layer enforcement. ✓

- **DEFERRED-OK OQ13 (per-rig opt-in surface — where does config
  live?):** The plan picks settings JSON (D2) but synthesis OQ1
  flags an unresolved sub-question about whether settings JSON
  exists today as a distinct artifact. Plan owner: integration +
  data leg follow-up; **decision needed before Phase 0 ships.**
  This is a *deferred* unknown with an explicit plan task — not a
  silent gap. ✓

- **RESOLVED OQ14 (pilot rig):** "`gastown_upstream` itself." →
  Q1 / Phase 1. ✓

### Clarifications from Human Review (Q1-Q7)

- **RESOLVED Q1 (Refinery vs external-PR):** v1 Refinery-only on
  `gastown_upstream`. → §Executive Summary + Phase 1; round-2 D2b
  explicitly names C2 as N/A-in-v1. ✓

- **RESOLVED Q2 (quality floor):** Hybrid, starting Tight on pilot;
  five MUST gates. → gates 4a-4f (now 4g with item 16). ✓

- **RESOLVED Q3 (GitHub identity):** Per-rig App for v2;
  polecat-as-author for v1 Refinery mode. → D8 + Q3 acceptance. ✓

- **RESOLVED Q4 (test/coverage/lint command authorization):**
  Language-keyed allow-list, no custom commands in v1. → Key
  Components §5 + D11 (mutant cap is hard-coded, not configurable).
  Round-2 verified C5 satisfied via Q4 redefinition. ✓

- **RESOLVED Q5 (dispatch payload contract):** Conventions sheet at
  `.gt/auto-test-pr/conventions.md` + envelope shape. → D7 + §Key
  Components §6 + the envelope JSON in §Interface. ✓

- **RESOLVED Q6 (on-call + kill-switch):** Overseer on-call; CLI
  ships in v1; circuit breaker. → D5 + D16 SEV-1 path (round-1
  fix) + Phase 0 task 11. ✓

- **RESOLVED Q7 (locking + state machine):** Pinned-state-bead with
  CAS. → D4 + §Data Model state-machine block (now updated with
  D18 cooldown-release edge for item 14). ✓

### Promoted-to-MUST and Explicitly-Deferred items

- **RESOLVED — Promoted-to-MUST:** gitleaks (gate 4e), code marker
  (D8), `status` command (Phase 0 task 2), MVP definition
  (Phase 1 task 13), pilot success criteria (Phase 1 exit, round-1
  rewrite), branch GC (Phase 0 task 9, round-1 fix). All present.
- **DEFERRED-OK — v2:** external-PR mode, GitHub App, Loose mode,
  custom commands, conventions auto-extraction, cross-language,
  mutation infra, Mayor dashboard. All explicitly named in §Open
  Questions or §Trade-offs as v2.

### Summary

| Class | Count |
|-------|-------|
| RESOLVED | 21 |
| DEFERRED-OK | 9 |
| UNRESOLVED must-fix | 1 |

The single UNRESOLVED is OQ2 (PR size cap enforcement mechanism),
applied as item 16 (gate 4g + D20 + R24).

---

## Total fix count

- **3 must-fix items** (14, 15, 16)
- **0 should-fix items**

All 3 applied to `.designs/auto-test-pr/synthesis.md` in this round.

## Sources

- [PRD draft](.prd-reviews/auto-test-pr/prd-draft.md) — commit `13d14a44`
- [Plan synthesis](.designs/auto-test-pr/synthesis.md) — post-round-2 commit `712f5ef8`
- [Round 1 log](.plan-reviews/auto-test-pr/prd-align-round-1.md)
- [Round 2 log](.plan-reviews/auto-test-pr/prd-align-round-2.md)
- [Bead gu-wfs-6ihhi](bd show gu-wfs-6ihhi) — assignment

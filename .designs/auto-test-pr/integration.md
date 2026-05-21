# Integration Analysis: Auto-Test-PR

> Dimension: How this feature integrates with the existing Gas Town system.
> PRD: `.prd-reviews/auto-test-pr/prd-draft.md` (commit 13d14a44).
> Synthesized review: `.prd-reviews/rqoca/prd-review.md`.

## Summary

Auto-Test-PR is a **coordination feature, not a new substrate.** Every
moving part it needs already exists somewhere in Gas Town: standing
patrols (`mol-pr-feedback-patrol`, `mol-refinery-patrol`), polecat work
formulas (`mol-polecat-work` with `extends`/expansion), Mayor-owned
pinned beads as state, sling/dispatch, the Refinery merge queue, and
rig-config layering (`gt rig config`). The new code surface is a single
**new molecule** (`mol-auto-test-pr-cycle`), a **new polecat-work
variant** that extends `mol-polecat-work` with the quality-gate steps
from PRD Q2, a small **admin CLI** (`gt auto-test-pr {pause,status}`),
and a per-rig **conventions file** at `.gt/auto-test-pr/conventions.md`.

Because v1 is **Refinery-only on `gastown_upstream`** (Q1), the feature
plugs into the existing `gt done → MR bead → Refinery merge`
trajectory unchanged. There is no new GitHub identity, no new merge
path, and no new state machine outside the pinned-bead lock from Q7.
The most consequential integration point is the **`mol-pr-feedback-
patrol` reuse** for revision cycles — it already creates dedup-labeled
beads keyed by PR number, which maps cleanly onto auto-test PRs once
the patrol's dispatch step learns to honor the `gt:auto-test-pr`
label and route to the test-improver formula variant. Backwards
compatibility is **preserved by default**: opt-in is OFF, the new
molecule is gated by a new rig-config key, and the feedback patrol's
existing behavior on non-auto-test PRs is untouched.

## Analysis

### Key Considerations

- **The feature is composed almost entirely of reuse.** New code is
  thin coordination + quality-gate logic. This is the right shape for
  a v1 — fewer surfaces to break, fewer surfaces to security-review.
- **The Refinery-only v1 (Q1) collapses the dual-mode question.** Auto-
  test work goes through `gt done` like any other polecat work; the MR
  bead in the queue *is* the "PR." External-PR mode is a v2 problem
  with its own identity, DCO, and feedback semantics — explicitly out
  of scope here.
- **Polecat-work extension is well-supported.** `mol-polecat-work-
  monorepo-tdd` already demonstrates the `extends + compose.expand`
  pattern (gu-deat). A `mol-polecat-work-test-improver` extending
  `mol-polecat-work` with five quality gates as inserted steps is
  idiomatic and tested.
- **State-bead-as-lock is a pattern, not new infra.** Pinned beads with
  Mayor as owner already exist (gu-gal8 invariant). The auto-test
  state bead is one more instance using the same compare-and-set
  semantics on Dolt transactions.
- **The biggest integration risk is the feedback-patrol contract.**
  PRD Q5 + the synthesis both flag this as load-bearing. If the
  patrol cannot dispatch the test-improver formula variant (rather
  than its current generic dispatch), the revision loop breaks and
  the feature only works on first-shot PRs. We must either (a) extend
  the patrol with a per-label dispatch table, or (b) accept that
  revisions go through the same generic polecat-work and rely on the
  bead body + conventions sheet to carry context.
- **Pilot-on-self has a feedback loop.** The pilot rig is
  `gastown_upstream`, which is also the rig whose CI failures break
  every patrol that depends on it. A flaky auto-test PR landed on
  main can blue-screen the entire town. Rate limits (≤1/week in v1
  per OQ7) and the Q6 circuit breaker mitigate but do not eliminate.

### Existing Components: What This Touches

| Component | Touched How | Files / Surfaces |
|-----------|-------------|------------------|
| Formula registry | New molecule + new polecat-work variant | `internal/formula/formulas/mol-auto-test-pr-cycle.formula.toml`, `mol-polecat-work-test-improver.formula.toml` |
| `mol-pr-feedback-patrol` | Add label-aware dispatch routing for `gt:auto-test-pr`-labeled MR beads (revisions) | `internal/formula/formulas/mol-pr-feedback-patrol.formula.toml` (`dispatch-work` step) |
| Mayor daemon | New patrol tick consumer (registers `mol-auto-test-pr-cycle` like other standing patrols) | `mayor/`, `internal/mayor/manager.go` |
| Rig config layering | New keys: `auto_test_pr.enabled`, `auto_test_pr.language` | `internal/cmd/rig_config.go`, `internal/rig/` |
| Sling / bead dispatch | No code change — uses existing `gt sling <rig> <bead-id>` and `bd create --label` | `internal/cmd/sling.go`, `internal/cmd/sling_formula.go` |
| Refinery MQ | No code change — consumes auto-test MRs identically to any other MR | `internal/refinery/engineer.go`, `manager.go`, `batch.go` |
| Polecat lifecycle | No code change — quality gates run inside the formula steps before `gt done` | `internal/formula/formulas/mol-polecat-work*.toml` |
| Pinned-bead substrate | New `<rig>-auto-test-state` bead. Same Dolt mechanics as existing pinned beads. | `internal/beads/`, no schema changes |
| Tap guard | Confirm `gh pr create` still blocked (it is) — auto-test must not bypass via Refinery in v1 | `internal/cmd/tap_guard.go` (no change) |
| New CLI surface | `gt auto-test-pr {pause,status}` — new command package | `internal/cmd/auto_test_pr.go` (new file) |

**What this does NOT touch (intentionally):**

- `mol-refinery-patrol` — auto-test MRs are processed identically to
  other MRs.
- Witness — polecat lifecycle is unchanged. Witness already
  monitors all polecats; auto-test-pr polecats look the same.
- Gates / pipeline / reaper — no schema changes, no new wisp types
  beyond what the formula declares (`wisp_type=patrol` for the
  cycle, `wisp_type=task` for the polecat).
- GitHub Actions / CI definitions on the pilot rig — Refinery-mode
  v1 means the existing pre-merge gate suite already covers
  auto-test PRs.

### Dependencies: What This Needs From Others

| Need | Source | Status |
|------|--------|--------|
| Standing-patrol cadence runner | Mayor daemon | Exists; `mol-auto-test-pr-cycle` registers like other patrols. |
| Formula `extends` + step expansion | `internal/formula/parser.go` | Exists (gu-deat). Validated by `mol-polecat-work-monorepo-tdd`. |
| `bd ready` / sling dispatch with priority deprioritization | Sling subsystem | Sling exists; **per-rig priority floor is NOT yet enforced** — see "Constraints Identified" below. The synthesis flagged this (stakeholders critical 5). Likely small change to sling scoring. |
| Pinned-bead compare-and-set (Q7) | Dolt transactions on bead `notes`/`state` | Exists; pattern used by other Mayor-owned state beads. **Verify** that two simultaneous Mayor ticks correctly serialize on a single bead row update. |
| Coverage delta computation | `go test -coverprofile` plus a parser | Coverage flag exists; **no parser yet** — write a small AST-aware helper using `golang.org/x/tools/cover` (synthesis flagged this in `feasibility critical 4`). |
| Synthetic-mutant runner (Q2 gate 2) | New code; runs in tmpdir | **Net-new code.** Implemented inside the test-improver formula step. AST-aware via `go/ast` (Go pilot only in v1). |
| Tautology linter (Q2 gate 5) | New code | **Net-new code.** Go-only in v1; small static analyzer. |
| Pre-push gitleaks scan (Q6 SEV-2) | gitleaks binary | **External dependency** — must ship as part of polecat sandbox provisioning. Add to setup_command, fail-closed if missing. |
| Rig-config storage for `auto_test_pr.*` keys | `gt rig config` | Exists; thin schema addition. |
| Conventions sheet location | New file: `.gt/auto-test-pr/conventions.md` | **Net-new file**, committed to the pilot rig before opt-in flip. |

### Dependents: What Will Depend On This

- **The pilot rig's main branch** itself — every patrol on
  `gastown_upstream` depends on a green CI on main. Auto-test PRs that
  pass review and land become permanent assets.
- **`mol-pr-feedback-patrol`** — once configured to honor the
  `gt:auto-test-pr` label, it dispatches `mol-polecat-work-test-
  improver` variant instead of the generic.
- **Future v2 (external-PR mode)** — will reuse the molecule shell,
  add a GitHub-App identity layer, and replace "MR-pending" with
  "PR-open." The state machine (Q7) is already designed to
  accommodate this; v2 is a *capability extension*, not a redesign.
- **Cross-language pilot rigs** — TS/Python rigs will reuse the
  cycle molecule unchanged, but plug in language-specific quality-
  gate steps. The `language`-keyed allow-list (Q4) is the
  extension point.

### Migration Path: How We Get From Here To There

The migration is staged. Each phase ships independently and is
revertible by reverting one PR.

**Phase 0 — Substrate prep (no behavior change, no opt-in):**
1. Add `auto_test_pr.enabled`, `auto_test_pr.language` to rig-config
   schema. Default OFF for every rig.
2. Ship `gt auto-test-pr {pause,status}` CLI as no-op stubs
   (status reports "no rigs opted in"; pause is a write to a
   town-wide pinned bead). This validates the CLI surface before
   it's load-bearing.
3. Land `mol-polecat-work-test-improver` formula extending
   `mol-polecat-work`. Inserted steps: coverage-delta gate,
   synthetic-mutant gate, diff-marker check, flakiness re-run (N=10),
   tautology linter, gitleaks scan. Each is its own step so a single
   failed gate is easy to attribute. **No molecule registers it yet.**
4. Land `mol-auto-test-pr-cycle` formula. Registered in Mayor's
   patrol set but **gated by rig-config check** — exits immediately
   if no rig has `auto_test_pr.enabled = true`.

**Phase 1 — Pilot opt-in (`gastown_upstream` only):**
5. Author and commit `.gt/auto-test-pr/conventions.md` to the pilot
   rig (Go test patterns, fixture loaders, anti-patterns).
6. Provision the `<rig>-auto-test-state` pinned bead in the
   gastown_upstream beads store. Initial state: `idle`.
7. Flip `auto_test_pr.enabled = true` on `gastown_upstream`.
   Cadence: ≤1 PR/week (per OQ7-tightened). Mayor's next tick
   fires the cycle.
8. Watch for two consecutive merged PRs without intervention.
   Hold until that's true.

**Phase 2 — Feedback-patrol integration:**
9. Extend `mol-pr-feedback-patrol`'s `dispatch-work` step with a
   label-keyed dispatch table: `gt:auto-test-pr` → dispatch as
   `mol-polecat-work-test-improver` formula. Other labels keep
   today's generic behavior. **This is where the
   "feedback-patrol contract" risk lands** — write integ tests
   that exercise the label routing on a fixture rig.

**Phase 3 — Generalization (deferred to v2):**
10. Add a second rig (e.g., a TS rig) with its own
    conventions sheet and `language=typescript`.
11. Land the v2 PRD for external-PR mode + GitHub App identity.

**Reverting:** Each phase reverts independently.
- Phase 1 revert: `gt rig config set gastown_upstream
  auto_test_pr.enabled false` (or `--block`). Cycle exits at the
  gate step on next tick. Any in-flight MR completes or is closed
  manually.
- Phase 0 revert: drop the formulas and the CLI command; rig-config
  keys become inert.

### Backwards Compatibility: What Might Break

- **Default-off opt-in is a hard guarantee.** No rig changes
  behavior unless its owner flips a config key. The molecule's
  first-step gate enforces this even if the cadence runner fires.
- **No bead schema changes.** The state bead is a regular pinned
  bead with rich `notes`/`labels`. Existing bead consumers are
  unaffected.
- **`mol-pr-feedback-patrol` extension is additive.** Adding a label-
  keyed dispatch table is a strict extension — PRs without the new
  label keep current behavior. Risk of regression: the label routing
  is implemented as an `if` early-return in the existing dispatch
  loop, which is a small diff with high test coverage opportunity.
- **Refinery is unchanged.** Auto-test MR beads look identical to
  any other MR bead. No new MQ states, no new merge-time hooks.
- **Tap-guard remains intact.** Refinery-mode v1 means auto-test
  polecats never call `gh pr create` — they call `gt done`. The
  tap guard's existing block on `gh pr create` is preserved and
  serves as a safety belt: even if v2 code is accidentally enabled
  in v1, the guard catches it.
- **The Mayor patrol set grows by one.** Other patrols are
  unaffected; cadence is independent per patrol.
- **One forward-compat snag**: if/when v2 lands external-PR mode
  on a non-Refinery rig, the existing `gh pr create` tap-guard
  must learn to allow the auto-test polecat. That is a v2 decision
  but should be flagged in the v1 PRD so it isn't a surprise.

### Testing Strategy: How We Verify Integration

Layered, with strict isolation.

**Unit tests (per package):**
- Formula parser tests for the new molecule + extension (mirrors
  `internal/formula/parser_test.go::TestResolve_MonorepoTDD`).
- Coverage-delta parser unit tests against fixture coverprofiles.
- Synthetic-mutant gate: AST commenter tests on a fixture
  package, including syntax-error fallback (single-expression
  function bodies, single-line if conditions).
- Tautology linter unit tests on hand-crafted Go AST fixtures.
- Rig-config schema tests for the new keys + default-off semantics.

**Integration tests (mock rig):**
- `mol-auto-test-pr-cycle` end-to-end with a fixture rig:
  `auto_test_pr.enabled=false` → cycle exits at gate. Set true →
  cycle proceeds, target-pick on a fixture coverage report, dispatch
  to a fake polecat that asserts the bead payload contract from Q5.
- State-machine tests: simulate concurrent Mayor ticks; verify
  compare-and-set serializes on the pinned bead.
- Pinned-bead lifecycle tests for each Q7 transition (`idle →
  picking → dispatched → mr-pending → cooled-down`), with
  Refinery-merge events as the trigger for `mr-pending →
  cooled-down`.

**Refinery integration tests:**
- Submit an auto-test MR bead via `gt done` in a sandbox; verify
  Refinery batches and merges identically to other MRs. Reuse
  `internal/refinery/engineer_test.go` patterns.

**Feedback-patrol integration tests:**
- Phase 2 only. Fixture PR with `gt:auto-test-pr` label →
  patrol dispatches `mol-polecat-work-test-improver`. Fixture PR
  without label → patrol dispatches generic. Both verified in the
  same test pass.

**Acceptance tests on pilot:**
- After Phase 1 opt-in, watch first 5 cycles. Defined success
  (per PRD): ≥60% merge rate, zero SEV-1/SEV-2, rejection rate
  stable below 40% over weeks 2–6.
- Negative tests: deliberately introduce a failing test in a
  candidate file; verify the polecat catches it locally and
  exits with NOTES, no MR submitted.

**Test-assertion-changes SOP:** All tests added by auto-test-pr are
themselves subject to the polecat SOP. The diff-marker comment
(`// gt:auto-test-pr origin=...`) is the audit trail; if a future
polecat broadens or deletes one, the SOP fires and requires a
root-cause writeup.

### Where Does The Code Live?

```
internal/
  formula/formulas/
    mol-auto-test-pr-cycle.formula.toml        (NEW: cycle molecule)
    mol-polecat-work-test-improver.formula.toml (NEW: extends mol-polecat-work)
  cmd/
    auto_test_pr.go                            (NEW: gt auto-test-pr {pause,status})
    auto_test_pr_test.go                       (NEW)
  autotest/
    coverage.go                                (NEW: coverprofile parsing → candidates)
    coverage_test.go                           (NEW)
    mutant.go                                  (NEW: AST-aware comment-out gate)
    mutant_test.go                             (NEW)
    tautology.go                               (NEW: heuristic linter)
    tautology_test.go                          (NEW)

.gt/
  auto-test-pr/
    conventions.md                             (NEW, on the pilot rig only)
```

The polecat-work-test-improver runs the new gates *inline* via
formula steps; the actual logic ships as a small CLI invoked from
those steps (e.g., `gt auto-test-pr gate coverage-delta`,
`gt auto-test-pr gate synthetic-mutant`). This keeps the formula
shell-readable and the heavy logic Go-tested.

### How Does It Affect Existing Workflows?

- **Polecat workflows:** Unchanged for any polecat not assigned an
  auto-test bead. Auto-test polecats execute the test-improver
  formula variant, which inserts five quality-gate steps before
  `commit-changes` and `pre-merge rebase`. If any gate fails the
  polecat exits with NOTES and no MR is submitted — the same
  shape as today's "no commits → DEFERRED" path.
- **Refinery:** Unchanged. Auto-test MRs are merged identically to
  other MRs.
- **Witness:** Unchanged. Auto-test polecats look like any other
  polecat from Witness's perspective.
- **`bd ready` / dispatch:** Auto-test work must be lowest priority
  (synthesis stakeholders critical 5). If sling priority isn't
  already deprioritizable, that's part of this project's scope —
  add a `priority_adjustment` mechanism in sling that's controlled
  by the cycle molecule when filing the bead.
- **CI on the pilot rig's main branch:** Steady-state cost
  increase for review + merge of auto-test MRs. Bounded by the
  ≤1 PR/week cap.

### Feature-Flag / Gradual Rollout

Three layers of revertibility:

1. **Per-rig opt-in (default OFF).** Single-flip to enable, single-
   flip to disable. The molecule's gate step reads the rig-config
   key on every tick; flipping to false at any point stops the next
   cycle.
2. **Town-wide kill-switch.** `gt auto-test-pr pause --all
   --duration=24h` writes a town-level pause bead. The molecule's
   gate step reads this BEFORE the rig-config key. This is the
   "fast-stop everything" lever.
3. **Circuit breaker (Q6).** Three consecutive auto-test PRs
   closed-unmerged within 7 days → town-wide auto-pause for 72h
   + Overseer notification.

Each layer is independent; any one being tripped stops new
cycles. Layer 1 alone is sufficient for "this rig isn't ready."
Layer 2 is for incident response. Layer 3 is for systemic quality
regression.

**Rollout cadence (preferred):**
- Day 0: Phase 0 ships. Nothing visible.
- Day 7: Phase 1 ships, opt-in flipped on `gastown_upstream`.
  Cadence ≤1 PR/week. Two-week observation window.
- Day 21: If pilot-success criteria are met, Phase 2 ships.
- Day 42 onward: Second rig opt-in, language allow-list grows.

### Options Explored

#### Option 1: New molecule + new polecat-work variant (recommended)

- **Description:** Net-new `mol-auto-test-pr-cycle` for the
  Mayor-side cadence + state management; net-new
  `mol-polecat-work-test-improver` extending `mol-polecat-work`
  for the worker side. Reuses `mol-pr-feedback-patrol` for
  revisions via label-keyed dispatch.
- **Pros:** Idiomatic (matches `mol-polecat-work-monorepo-tdd`'s
  extension shape); minimal blast radius (existing molecules
  unchanged for non-auto-test work); each new file is small and
  reviewable; trivial to revert.
- **Cons:** Two new formulas instead of one; the feedback-patrol
  diff is the riskiest piece (cross-feature change).
- **Effort:** **Medium.** Most code is small Go helpers (coverage,
  mutant, tautology) and formula TOML; the feedback-patrol
  extension is the integration risk.

#### Option 2: Inline everything into a single mega-molecule

- **Description:** One molecule that does cycle + work + feedback
  in one place.
- **Pros:** Single file to read.
- **Cons:** Violates the patrol-vs-task separation; can't reuse
  `mol-pr-feedback-patrol`; conflates Mayor-owned state with
  polecat-owned work; produces a 600+ line formula no one wants
  to touch.
- **Effort:** Higher in the long run; lower up-front.

#### Option 3: Drive the cycle from a new daemon outside the
formula system

- **Description:** Bypass formulas; write a Go daemon that polls
  rig configs and dispatches polecats directly via Sling APIs.
- **Pros:** Tighter type-safety on the cycle logic; easier
  to unit-test the dispatch decisions.
- **Cons:** **Reinvents the patrol loop.** The Mayor daemon already
  runs standing patrols; bypassing it is foreign to Gas Town
  conventions and doubles operational surface (a new daemon to
  operate, monitor, restart). Loses the
  resume-on-crash semantics formulas already have.
- **Effort:** Higher; harder to revert.

### Recommendation

**Option 1.** The new molecule + extension shape is the lowest-risk
path because every dependency is already proven elsewhere in the
codebase, the diffs are small and isolated, and the Phase 0/1/2
staging gives three independent revert points.

The single non-trivial integration risk is **Phase 2's
feedback-patrol extension**. Mitigate by writing the label-keyed
dispatch as an additive `if` early-return that ships behind a
`feature_flags.auto_test_pr_revision_routing` rig-config bool —
default false until validated.

## Constraints Identified

These are **hard constraints** the integration analysis surfaced.
Some are restatements of PRD-imposed constraints; others are new
implementation-level constraints discovered during this analysis.

1. **Pilot rig MUST be a Refinery rig.** Per Q1, v1 has no PR
   creation path. `gastown_upstream` qualifies.
2. **Sling must support a strict priority floor for auto-test
   beads.** If not yet present, this is in-scope. Otherwise the
   PRD's "Mayor must be able to deprioritize" goal is unmet.
3. **gitleaks must be installed in the polecat sandbox.** Without
   it the Q6 SEV-2 secret-scan gate cannot run; the polecat MUST
   fail-closed on missing gitleaks.
4. **The pinned-bead compare-and-set must be transactional in
   Dolt.** A non-atomic update will allow concurrent cycles to
   race past the lock. Verify before relying on it.
5. **`mol-polecat-work-test-improver` MUST extend, not replace,
   `mol-polecat-work`.** Replacing forks the polecat lifecycle and
   loses upstream improvements (e.g., the `pre-verified` rebase
   step). Extension preserves them.
6. **The `gt:auto-test-pr` label MUST be applied on bead creation,
   not on PR creation.** v1 has no PR; the label is on the dispatch
   bead and on the MR bead Refinery creates. Feedback-patrol
   queries beads by label, not GitHub.
7. **Code-level diff-marker (`// gt:auto-test-pr origin=...`) is a
   merge-survival requirement, not an aesthetic choice.** It is the
   only audit signal that survives PR-body edits and squash merges.
   Promoted to MUST per Q2 / synthesis.
8. **Conventions sheet must be committed BEFORE opt-in flip.** The
   dispatch payload contract assumes the file exists; if missing,
   the polecat has no usable context (Q5). Treat opt-in flip as
   gated on `.gt/auto-test-pr/conventions.md` being present.
9. **Pilot must run with extra-tight rate limit (≤1 PR/week)
   until two consecutive merges without intervention.** This is
   the synthesis's pilot-on-self risk mitigation.
10. **No polecat-owned bookkeeping beads.** The state bead is
    Mayor-owned. The polecat receives a single dispatch bead and
    closes it via `gt done` (or NOTES + close on gate failure).
    Honors gu-gal8.
11. **Tap-guard's `gh pr create` block remains in force on the
    pilot rig.** Auto-test polecats use `gt done`, not `gh pr
    create`. Any future v2 external-PR work is its own
    tap-guard amendment.

## Open Questions

These need either human input or cross-dimension discussion (api,
data, ux, security, scale legs) before build.

1. **Sling priority floor — does it exist today?** If not, who
   owns implementing `priority_adjustment` in the sling scoring
   function? (Cross-dimension: API leg should specify the
   `gt sling --priority-adjustment` flag if it doesn't exist.)

2. **Coverage-delta source of truth — coverprofile diff vs. uncovered
   line list?** I recommend coverprofile diff because it's
   line-for-line attributable, but the coverage-tool parser
   (synthesis feasibility critical 4) is undefined. (Cross-
   dimension: data leg should pin the format.)

3. **Where does `gt auto-test-pr pause --all` write to?** A
   town-level pinned bead is the obvious answer (HQ-prefixed),
   but who owns it — Mayor or a new "town admin" identity?
   (Cross-dimension: data leg + security leg.)

4. **Feedback-patrol's label-routing dispatch — backed by what?**
   A static map in the formula step? A pluggable rig-config
   table? I lean static-map for v1 simplicity. (Cross-dimension:
   api leg.)

5. **Does the `gt:auto-test-pr` bead label need to be reserved /
   protected from manual creation?** A human or another patrol
   could file a bead with that label and trigger the
   test-improver formula on unrelated work. Probably benign in
   v1 (Refinery catches the absence of valid quality-gate
   output) but worth flagging. (Cross-dimension: security leg.)

6. **Conventions-sheet schema or freeform?** v1 is freeform
   markdown the polecat reads. Should it harden into a structured
   schema in v2 to support auto-extraction? (Cross-dimension:
   data leg.)

7. **Pilot graduation criteria's auditability.** "Two consecutive
   merges without intervention" — who decides "without
   intervention"? Mayor reads MR-bead history? Manual Overseer
   call? (Cross-dimension: ux leg + scale leg.)

## Integration Points

How this dimension connects to other design dimensions in the
convoy.

- **API leg (`gu-leg-vha3g`):** Specifies the `gt auto-test-pr
  pause/status` CLI surface, the `gt:auto-test-pr` label
  contract, the rig-config keys (`auto_test_pr.enabled`,
  `auto_test_pr.language`), the dispatch-bead payload schema
  (Q5), the conventions-sheet format, and the formula-step
  invocation contract (`gt auto-test-pr gate coverage-delta`,
  etc.).

- **Data leg (`gu-leg-svhds`):** Owns the pinned-state-bead
  schema (Q7 state machine + transitions), the labeling scheme
  (`gt:auto-test-pr`, `pr-num-N`, `auto-test-state-<rig>`), the
  coverage-profile / parser format, and how cooldown timestamps
  are encoded.

- **UX leg (`gu-leg-nehua`):** Owns the maintainer experience —
  what the auto-test PR body looks like, how diff-marker comments
  read, what `gt auto-test-pr status` displays, what the
  "STOP magic phrase" in PR comments looks like (synthesis
  observation).

- **Security leg (`gu-leg-sbpyq`):** Owns the threat model —
  language allow-list (Q4), gitleaks gate (Q6), config-injection
  attack surface, secret-leak-via-fixtures, who can flip the
  rig-config opt-in, the SEV-tree in Q6. The Refinery-only v1 path
  removes the GitHub-App / token-scoping question entirely from
  v1, but security must validate that.

- **Scale leg (`gu-leg-44w2u`):** Owns wall-clock budgets — how
  long the synthetic-mutant gate is allowed to take, the N=10
  flakiness rerun on the *direct package only* (synthesis
  promoted), the per-cycle GitHub-API budget (no GitHub calls
  in v1 Refinery mode — but PR-state polling for v2 is Scale's
  problem), the polecat-pool capacity floor we must reserve for
  user work, and the circuit-breaker thresholds.

- **Synthesis bead (`gu-syn-gdjtq`):** Synthesis must pin down
  cross-dimension conflicts. Likely conflict points:
  - Phase ordering (this leg's Phase 1 vs. UX leg's
    rollout requirements).
  - Dispatch-bead payload size (Q5 conventions sheet) vs.
    Scale leg's bead-size budgets.
  - Tap-guard interaction (this leg's "leave it on") vs.
    Security leg's analysis of v2 readiness.

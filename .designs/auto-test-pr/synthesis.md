# Design: Auto-Test-PR — automated test-improvement PRs per rig

> Convoy synthesis — combines the six dimension analyses (`api`,
> `data`, `integration`, `scale`, `security`, `ux`) into one
> actionable design.
> Source PRD: `.prd-reviews/auto-test-pr/prd-draft.md` (commit
> `13d14a44`).
> Source PRD review: `.prd-reviews/rqoca/prd-review.md` (Q1–Q7).
> Convoy legs: `gu-leg-vha3g` (api), `gu-leg-svhds` (data),
> `gu-leg-auvdq` (integration), `gu-leg-44w2u` (scale),
> `gu-leg-sbpyq` (security), `gu-leg-nehua` (ux).
> Synthesis bead: `gu-syn-gdjtq`.

## Executive Summary

Auto-Test-PR is a **Mayor-driven cycle** that produces small, reviewable,
test-only merge requests for opted-in Gas Town rigs. v1 is **Refinery-
only on the `gastown_upstream` Go pilot**, with a hard cap of **one
open MR per rig** and a cadence of **≤1 cycle per rig per 7-day
window**. The mechanism is composed almost entirely of *reuse*: the
existing Mayor patrol scheduler dispatches polecats via the existing
sling/dispatch-bead surface; the polecat runs an extended
`mol-polecat-work` formula whose new steps are five quality gates
(coverage delta, synthetic-mutant sanity, flakiness rerun, tautology
linter, gitleaks); `gt done` submits the MR through the unmodified
Refinery merge queue; `mol-pr-feedback-patrol` is taught to recognize
the `gt:auto-test-pr` label and dispatch a revision polecat on
review-comment activity. The only *new* persistent state is **two
pinned beads** — one per opted-in rig (`<rig>-auto-test-state`) and
one town-wide (`town-auto-test-pr-state`) — both **Mayor-owned per
gu-gal8**. The user-facing surface is a single CLI tree
`gt auto-test-pr {enable,disable,pause,resume,status,show,history}`,
a per-rig config stanza in the existing rig settings JSON, an in-repo
conventions sheet at `.gt/auto-test-pr/conventions.md`, and a
machine-generated MR banner that doubles as the reviewer's audit
receipt.

The dominant **risks** are (1) reviewer fatigue from low-quality
generated tests — mitigated by the five quality gates plus the per-rig
**circuit breaker** (3 consecutive unmerged closes within 7 days →
72-hour town-wide auto-pause + Overseer notification); (2) prompt-
injection of the polecat via target source, conventions doc, or
review comments — mitigated by structural constraints (test-files-
only allow-list, sandboxed test runs, mutant-in-tmpdir) rather than
model judgment; and (3) pilot-on-self feedback loops, since
`gastown_upstream` is the rig whose green main blocks every other
patrol — mitigated by the hard ≤1 PR/week cadence and a pause-the-
rig magic phrase reviewers can paste into any MR comment. Phase 0/1/2
staging gives three independent revert points; Phase 0 is invisible
to users, Phase 1 is the pilot opt-in, Phase 2 wires up revision
routing through the feedback patrol.

The major **open decisions needing human input**: per-cycle wall-clock
cap (recommended 30 min); whether `enable` writes to the rig's repo
config or to its settings JSON (the data leg recommends settings JSON
as authoritative; the api leg recommends repo `config.json`; this
synthesis sides with the **data leg** for security reasons — see
"Decisions Made"); and whether `gt:auto-test-pr` bead labels need
protection from manual creation.

## Problem Statement

Gas Town rigs accumulate code faster than humans can write tests for
it. Coverage drifts down; recently-changed files often ship with the
bare minimum of tests; edge-case bugs land in `main` and surface only
when a polecat (or human) gets bitten weeks later. The PRD names two
populations: **rig maintainers** who want a steady drip of small,
reviewable test PRs (rather than a one-time coverage push), and
**Crew/Mayor agents** who want to convert idle polecat capacity into
durable quality improvements.

The seven PRD goals (paraphrased): (1) land net-new tests on rig
`main` autonomously, gated by ordinary human PR review (no
auto-merge); (2) per-rig opt-in, single-flip on/off, default OFF; (3)
≤1 open auto-test PR per rig; (4) feedback-driven revision on the
same branch (no close-and-reopen); (5) bounded blast radius per PR
(≤200 LOC, ≤3 files, no non-test source); (6) quality floor (passing,
non-flaky, non-tautological, branch-exercising); (7) honor gu-gal8 —
no polecat-owned bookkeeping beads.

This synthesis assumes the seven Q1–Q7 PRD-review decisions:
v1-Refinery-only Go pilot (Q1), MUST-promotion of all five quality
gates (Q2), Refinery-mode polecat-as-author identity (Q3), language-
keyed allow-list with no custom commands (Q4), conventions sheet at
`.gt/auto-test-pr/conventions.md` (Q5), per-rig + town-wide pause +
auto-circuit-breaker (Q6), and pinned-state-bead state machine with
compare-and-set semantics (Q7).

## Proposed Design

### Overview

Three top-level components:

```
                ┌──────────────────────────────┐
                │  Mayor (mol-auto-test-pr-    │
                │   cycle, standing patrol)    │
                │   - reads rig config         │
                │   - reads <rig>-auto-test-   │
                │     state pinned bead        │
                │   - CAS-transitions state    │
                │   - dispatches polecat       │
                │     via sling-context bead   │
                └──────────────┬───────────────┘
                               │ dispatch bead (JSON envelope)
                               ▼
   ┌──────────────────────────────────────────────────┐
   │  Polecat (mol-polecat-work-test-improver,        │
   │   extends mol-polecat-work)                      │
   │   - reads conventions sheet + target source     │
   │   - writes new *_test.go files only             │
   │   - inserts five quality-gate steps:            │
   │       coverage-delta, synthetic-mutant,         │
   │       flakiness-N=10, tautology-linter,         │
   │       pre-push gitleaks                         │
   │   - all test/mutant runs go through              │
   │     hardened sandbox (no creds, no net          │
   │     post-warm-up)                                │
   │   - gt done → MR bead in Refinery MQ            │
   └──────────────────────────┬───────────────────────┘
                              │ MR bead with gt:auto-test-pr label
                              ▼
   ┌──────────────────────────────────────────────────┐
   │  Refinery (unmodified)                           │
   │   - merges identically to any other MR           │
   │   - notifies Mayor on merge or close-unmerged   │
   │   - Mayor transitions state bead to cooled-down │
   └──────────────────────────────────────────────────┘

      mol-pr-feedback-patrol (Phase-2 extension)
        - on review comment with gt:auto-test-pr label,
          dispatch mol-polecat-work-test-improver in
          mode=revise, transitioning state bead from
          mr-pending → mr-revising
        - parses reviewer magic-phrase pause requests
          from any comment thread
```

The cycle is **idempotent and stateless from the polecat's
perspective**: the polecat reads from its dispatch bead, writes
test files, runs gates, calls `gt done`. All persistent state and
all bookkeeping live on Mayor-owned pinned beads. The polecat
**never writes** the state bead (gu-gal8).

### Key Components

**1. Mayor cycle molecule (`mol-auto-test-pr-cycle`)**

A new standing-patrol formula registered alongside the existing
patrols. Per tick (recommended cadence: hourly check, fire only if
the rig's per-rig cooldown has elapsed):

1. Read `town-auto-test-pr-state` for global pause / circuit-breaker.
2. For each rig with `auto_test_pr.enabled=true`: read `<rig>-auto-
   test-state`; if `state != "idle"` or `paused_until > now`, skip.
3. CAS-transition `idle → picking`; on commit failure, skip (another
   tick is already running this rig — see scale leg's analysis of
   Dolt SERIALIZABLE-class isolation on row updates).
4. Compute target candidates: `git log --since=30d` × coverage profile
   from `go test -coverprofile`, ranked by `(churn × uncovered_branches)`.
   **Per-file rejection cooldown (PRD S4 fix):** before ranking, drop
   any candidate whose path appears in `<rig>-auto-test-state.
   rejection_log[].target_path` within the last 21 days. This honors
   the PRD's "avoid retargeting that file for some cooldown period"
   without requiring per-cycle human input. **Within-file churn-proximity
   ranking (PRD Non-Goal NG5 fix):** once a target file is selected, the
   dispatch envelope's `uncovered_branches[]` is sorted by line-distance
   to recent-churn line ranges (from `git log -L` / `git blame` over the
   30-day window) so the polecat preferentially writes tests for
   *recently-changed* uncovered branches rather than legacy untouched
   code in the same file. This keeps the mechanism greenfield-aligned
   per PRD Non-Goal "Not retroactive coverage cleanup" — without it, a
   churning file with one new function and 50 untouched legacy branches
   could send the polecat to backfill legacy tests, which the PRD
   explicitly rejects.
5. CAS-transition `picking → dispatched`; file the dispatch bead;
   sling-attach to the polecat pool with a strict priority floor
   (lowest bucket).
6. Refinery's merge handler observes MR closure (merged or rejected)
   and emits a nudge → Mayor transitions `mr-pending → cooled-down`,
   appending a transition record and (on close-unmerged) a rejection
   record.

**2. Polecat formula (`mol-polecat-work-test-improver`)**

Extends `mol-polecat-work` (idiomatic per `mol-polecat-work-monorepo-tdd`,
gu-deat). Inserts five quality-gate steps between the implement step
and the commit step, plus a final allow-list verification step:

| Step | Gate | Mode |
|------|------|------|
| 4a | coverage-delta — **branch coverage** delta (parsed via `golang.org/x/tools/cover`, branch mode) | hard fail if branch delta ≤ 0; the marker comment alone does not satisfy this |
| 4b | synthetic-mutant sanity (≤5 mutants per test, AST-aware, runs in `os.MkdirTemp` outside worktree) | hard fail if any new test still passes when its target line is commented out |
| 4c | flakiness rerun (`go test -count=10 -run="<exact-test-names>" ./<direct-package>` only) | hard fail if any flake |
| 4d | tautology linter — see expanded heuristic below: (i) ≥1 assertion must depend on the function-under-test's return value or observable side effect; (ii) reject tests where every assertion is literal-vs-literal (e.g. `assert.Equal("x", "x")` or constant-vs-constant); (iii) reject tests whose only assertions against the SUT are `NotNil`/`NotEmpty`/truthy checks; (iv) reject `assert(true)` / `expect(x).toBe(x)` / zero-assertion tests | hard fail |
| 4e | pre-push gitleaks scan (`gitleaks detect --no-banner --redact`) | hard fail; SEV-2 per Q6 |
| 4f | output allow-list verifier — every changed file in the diff matches `**/*_test.go` AND is NOT under `integration/`, `e2e/`, or `test/` (only same-package `_test.go` files allowed) AND has no `//go:build integration` build tag AND every newly-added top-level test function in the diff matches `func Test*(t *testing.T)` (reject `Benchmark*`, `Example*`, `Fuzz*` and any non-`Test*` test-form — these are not unit tests per PRD Non-Goal NG2) | hard fail |
| 4g | size-budget enforcer — count files added/modified in the diff and added test LOC; hard fail if `files > size_budget.max_files` (default 3) or `added_test_loc > size_budget.max_loc` (default 200) | hard fail |

Each gate runs through a **hardened sandbox wrapper** that strips
credential env vars (`AWS_*`, `GITHUB_TOKEN`, `BD_*`, `DOLT_*`,
`GIT_AUTHOR_*`, `GIT_COMMITTER_*`), drops network egress *after*
module-cache warm-up, pins CWD to the worktree, and caps wall-clock
per-target at 5 min (cycle-wide cap 30 min — see Decisions).

The molecule honors the existing `--pre-verified` rebase step from
`mol-polecat-work` so the Refinery can fast-path the merge.

**Bug-discovery NOTES protocol (PRD Non-Goal "not a code-fixing tool"
fix):** if, while iterating, the polecat writes a candidate test that
fails on `main` *as written* (i.e., the test appears to encode correct
behavior but the source is buggy), the polecat MUST exit with a
structured NOTES section under heading `BUG-DISCOVERED:` containing
(a) the file:line, (b) the failing assertion's expected vs. actual,
(c) the candidate test source. The polecat does NOT push a fix and
does NOT open a test-only MR for the buggy area (that would
encode-as-correct a behavior that is actually wrong). Mayor's
cycle-close handler parses any `BUG-DISCOVERED:` NOTES and files a
separate P2 bug bead in the rig (`<rig>-bug-from-auto-test-NNN`),
linked to the cycle's MR bead for audit trail. This is the explicit
boundary between "test improvement" and "code fixing" the PRD draws.

**3. State beads (Mayor-owned)**

Per rig: `<rig>-auto-test-state` (pinned). Town-wide:
`town-auto-test-pr-state` (pinned). Both with versioned JSON in their
`Issue.Metadata` field. The per-rig bead is **authoritative**; the
town bead is a **denormalized read-cache** for `gt auto-test-pr
status` plus the global pause flag and circuit-breaker counter.

**4. CLI surface (`gt auto-test-pr`)**

A single Cobra subcommand tree (api leg's Option 1, ux leg's Option 1).
Verbs: `enable`, `disable`, `pause`, `resume`, `status`, `show`,
`history`. Per-rig flags throughout. `status` and `show` are
read-only and time-out fast (≤2 s) when Dolt is degraded — they
must be reachable during incidents.

**5. Per-rig config**

Extends the existing per-rig settings JSON (data leg's Option a):

```json
{
  "auto_test_pr": {
    "enabled": false,
    "language": "go",
    "cadence_days": 7,
    "conventions_path": ".gt/auto-test-pr/conventions.md",
    "skip_dirs": []
  }
}
```

Note this is the rig's **settings JSON** under operator/Mayor authority,
**not** the in-repo `config.json` — see "Decisions Made" below for
the cross-leg conflict resolution.

**6. In-repo artifacts (per rig, source-controlled)**

- `.gt/auto-test-pr/conventions.md` — human-authored guide for the
  bot. Required to exist before opt-in flip; polecat refuses to run
  without it (per ux/integration leg's hard fail). **Template MUST
  include explicit forbid-list per PRD Non-Goal NG2:** integration
  tests, end-to-end tests, load tests, benchmarks (`Benchmark*`),
  examples (`Example*`), and fuzz tests (`Fuzz*`) are out of scope —
  unit tests only. The template MUST also call out that the polecat
  should prefer uncovered branches geographically near recent-churn
  line ranges within the targeted file (per PRD Non-Goal NG5 — not
  retroactive coverage cleanup).
- `.gt/auto-test-pr/mr-template.md` — the banner template, machine-
  filled per cycle.

**7. Code-level marker (in every generated test)**

```go
// gt:auto-test-pr origin=<bead-id> covers=<file:line>
func TestX_<scenario>(t *testing.T) { ... }
```

Single-line, greppable, survives PR-body edits and squash merges
(security-leg's audit-trail backstop). Promoted to MUST per Q2.

### Interface

**CLI (full v1 surface):**

```
gt auto-test-pr enable   --rig=<rig> --language=go [--cadence=weekly]
gt auto-test-pr disable  --rig=<rig>
gt auto-test-pr pause    --rig=<rig>|--all --duration=24h [--reason=...]
gt auto-test-pr resume   --rig=<rig>|--all
gt auto-test-pr status   [--format=table|json]            # town-wide
gt auto-test-pr show     --rig=<rig> [--verbose|--raw]    # per-rig
gt auto-test-pr history  --rig=<rig> [--last=10]
```

`status` table (most-read surface, ux leg):

```
RIG               STATE          NEXT TICK  LAST MR    REJECT-RATE  PAUSE
gastown_upstream  idle           +3d 4h     #482 ✓     0/3          —
casc_crud         disabled       —          —          —            —
beads             cooled-down    +2d        #117 ✗     2/5          rig 5d
(town-wide)       running        —          —          —            —
```

`enable` validates: language is in the v1 allow-list (`go` only;
unknown languages → static error pointing to the v2 follow-up bead);
`--rig` is the pilot rig (`gastown_upstream` only in v1; others →
static error). `pause`/`resume` work for any rig (need to operate on
partially-rolled-back rigs). All mutating verbs go through Mayor; the
CLI is a thin client.

**Reviewer magic phrase** (in any auto-test MR comment):

```
gt auto-test-pr: pause-rig-7d
```

`mol-pr-feedback-patrol` recognizes the literal string in any thread
on a `gt:auto-test-pr`-labeled MR and writes the pause to that rig's
state bead. Documented in the MR banner so a maintainer under fire
doesn't need to find the rig config.

**Dispatch-bead JSON envelope** (Mayor → polecat):

```json
{
  "version": 1,
  "work_bead_id": "<bead-id>",
  "target_rig": "gastown_upstream",
  "formula": "mol-polecat-work-test-improver",
  "args": {
    "mode": "create" | "revise",
    "targets": [{
      "path": "internal/cmd/foo.go",
      "uncovered_branches": [
        {"line": 42, "kind": "if-true"},
        {"line": 51, "kind": "switch-case-default"}
      ],
      "coverage_pct_before": 0.62
    }],
    "conventions_sheet_path": ".gt/auto-test-pr/conventions.md",
    "language": "go",
    "size_budget": {"max_files": 3, "max_loc": 200},
    "pr_template_path": ".gt/auto-test-pr/mr-template.md",
    "revision": null
  },
  "enqueued_at": "2026-05-21T..."
}
```

On revision, `args.mode == "revise"` and `args.revision` carries
prior comment thread + last commit SHA + branch name. This shape
matches the existing `gu-wisp-*` sling-context envelope.

**MR banner** (machine-generated, refinery-side post-check verifies
its presence and consistency with dispatch-bead notes — security
leg's defense against polecat self-attestation):

```markdown
🤖 Auto-generated by gt auto-test-pr (v1)
─────────────────────────────────────────
Target:        internal/refinery/queue.go (lines 47, 92, 158)
Why this file: high churn (12 commits in last 30d) × low branch
               coverage (62% → 78% with this MR)
Origin bead:   gu-leg-nehua
Conventions:   .gt/auto-test-pr/conventions.md (read first)

What's covered (delta):
  + queue.go:47   error path (LeaseExpired)
  + queue.go:92   nil-claim guard
  + queue.go:158  retry-after-rebase branch

Quality gates passed:
  ✓ coverage delta (+16%)
  ✓ synthetic-mutant sanity
  ✓ flakiness (10/10 reruns green)
  ✓ tautology linter
  ✓ gitleaks (no secrets)

To pause this rig:    gt auto-test-pr pause --rig=gastown_upstream
                      (or paste this in a comment: `gt auto-test-pr: pause-rig-7d`)
To turn it off:       gt auto-test-pr disable --rig=gastown_upstream
Design doc:           .designs/auto-test-pr/synthesis.md
```

### Data Model

Two pinned beads, no new substrates. Full schemas live in `data.md`;
the lifecycle table is the most useful surface here:

| Data | Substrate | Lifecycle | Authority |
|------|-----------|-----------|-----------|
| `<rig>-auto-test-state` pinned bead (state machine, transition log ≤50, rejection log ≤200, FIFO eviction) | Beads / Dolt | Per opted-in rig, persists for opt-in duration | Mayor only |
| `town-auto-test-pr-state` pinned bead (global pause, circuit-breaker counter, denormalized rig summary) | Beads / Dolt | One, town-wide | Mayor only |
| `auto_test_pr.*` config block | Per-rig settings JSON | Per-rig, edited via `gt auto-test-pr enable`/`disable` | Rig owner / town admin via `gt` CLI |
| Conventions sheet | In-repo `.gt/auto-test-pr/conventions.md` | Per-rig, source-controlled | Rig maintainers via PR review |
| Language allow-list | `internal/autotestpr/languages.go` | Town-wide, ships with the binary | Town developers via Refinery CR |
| Code marker | In-repo source files | Per-test, lives with the test forever | Polecat writes, humans review |
| Branch name `auto-test/<rig>/<bead-id>` | Ephemeral remote ref | Until merge or 7d-stale GC | Polecat creates; branch-GC patrol cleans |
| Dispatch / MR / cycle beads | Beads / Dolt | Standard bead lifecycle | Standard |

**State machine** (Q7):
```
idle → picking → dispatched → mr-pending → cooled-down → idle
                                    ↓ ↑                     (after cadence_days)
                                mr-revising
```
Transitions are append-only on the `transitions[]` array on the per-
rig bead. CAS uses Dolt SERIALIZABLE-class isolation on the bead's
single row. **Cooldown release (PRD S1 fix):** the `cooled-down →
idle` edge fires when Mayor's tick observes
`now - last_transition.at >= cadence_days * 24h` AND the rig is still
`enabled=true`. Without this transition the cycle would never re-fire
after the first MR — S1's "twice a week the mechanism wakes up"
requires an automatic cooldown-release path. See **D18**.

**Schema versioning:** every JSON blob carries `schema_version`. v2
readers tolerate v1 blobs (defaults for new fields); v1 readers
tolerate v2 blobs (round-trip unknown fields via `json.RawMessage`).

## Trade-offs and Decisions

### Decisions Made

The legs converged on most decisions; this section names the
non-obvious choices and the cross-leg conflict resolutions.

**D1. CLI surface is a single top-level `gt auto-test-pr` namespace.**
(api Option 1, ux Option 1.) The arguments for splitting config
under `gt rig` vs. runtime under `gt auto-test-pr` (ux Option 2) are
real, but the v1 pilot has one rig and ~3 humans. Single-namespace
discoverability wins. `gt auto-test-pr enable` internally aliases the
same code path that would be used by a future `gt rig config set
auto-test-pr.enabled=true` so the duplication is a thin shim, not
two writers.

**D2. Per-rig config lives in the per-rig settings JSON, not in the
in-repo `config.json`.** **This resolves a cross-leg conflict.** The
api leg recommended in-repo `config.json`; the data leg recommended
the rig settings JSON; the security leg flagged the in-repo location
as a privilege-escalation primitive (write-access-to-repo ==
enable-the-feature). The synthesis sides with **data + security**:
settings JSON is operated by `gt auto-test-pr enable` under
operator/Mayor authority, NOT by repo PRs. Rationale: enabling auto-
test-pr is an *authorization* event, not a *code* event, and putting
it in the repo conflates the two. The api leg's concern (config-
loader complexity) is addressed by reading the settings JSON via the
existing rig-settings loader, which already exists. The conventions
sheet, by contrast, **is** code (instructions to a code-writing bot)
and stays in-repo at `.gt/auto-test-pr/conventions.md`.

**D2a. `disable` does NOT cancel in-flight work.** **PRD S6 fix.** When
a rig owner runs `gt auto-test-pr disable --rig=<rig>` while a cycle is
in-flight (state ∈ {`picking`, `dispatched`, `mr-pending`,
`mr-revising`}), the state bead is left as-is; the cycle's first step
(read `auto_test_pr.enabled`) exits on the *next tick*. The in-flight
MR completes its lifecycle (merged or closed by human); Mayor's
existing transition handlers move the state bead through `cooled-down`
normally. Once the rig is back at `cooled-down` AND `enabled=false`,
no further cycles fire. This honors the PRD's "any in-flight PR is
left alone" semantics without introducing a polecat-side cancellation
pathway (which would be racy against the Refinery merge handler).

**D2b. Per-rig Refinery-vs-external-PR mode detection is N/A in v1.**
**PRD Constraint C2 scope clarification.** PRD §Constraints requires
"the mechanism must detect which mode applies per rig" between Refinery
and external-PR (`gh pr create`) modes. Q1 cut external-PR mode entirely
from v1; the pilot rig (`gastown_upstream`) is Refinery-only by
construction. Resolution: v1 hard-codes Refinery mode; the
`mol-auto-test-pr-cycle` formula has no mode-detection step. v2 (when
external-PR mode lands per the deferred bead) MUST add a per-rig
`auto_test_pr.merge_mode ∈ {refinery, external-pr}` config key and a
detection step at cycle entry. Documenting this here so a future reader
of C2 doesn't think the constraint was forgotten — it was satisfied by
removing the alternative, not by implementing detection. The v1 CLI's
`enable` already rejects rigs that aren't Refinery-resident with a
pointer to the v2 follow-up bead (per Q1).

**D3. New molecule + new polecat-work variant** (integration Option
1). Two new formulas instead of one mega-molecule. Each is small and
reviewable; the existing `mol-pr-feedback-patrol` is extended
additively (not replaced). The feedback-patrol extension lands in
**Phase 2** behind a feature-flag rig-config bool
(`feature_flags.auto_test_pr_revision_routing=false` until validated).

**D4. Pinned-bead state machine with Dolt CAS** (data Option 2,
Q7). Per-rig pinned bead is authoritative; town-wide bead is a read-
cache. Drift is non-fatal (stale `status` until next tick).

**D5. Hardened sandbox profile, not full container isolation**
(security Option 2). Strips credential env vars; drops network
post-warm-up; pins CWD; caps CPU/memory/wall-clock. Container
isolation (security Option 3) is the v2 step once we have a second
rig and better runtime tooling.

**D6. Synthetic-mutant in tmpdir only** (security C-SEC-2). The
mutant flip copies the worktree to `os.MkdirTemp`, applies the
comment-out, runs tests under sandbox, deletes the tmpdir on exit.
The actual worktree is never modified. Resolves scale leg's tmpdir-
copy cost concern by scoping to the **package directory**, not the
rig root.

**D7. Output allow-list: tests-only.** (security C-SEC-3.) The
polecat's final verification step verifies every changed file in the
diff matches `**/*_test.go`. Any non-test file → abort, no MR. This
is the structural defense against prompt-injection-driven source
mutation.

**D8. Single-line code-level provenance marker** (api Option 7a,
data, security, ux all aligned). `// gt:auto-test-pr origin=<bead-id>
covers=<file:line>` — greppable, survives merges, doubles as the
audit-trail-of-record.

**D9. Reviewer magic phrase in any MR comment** (api recommended
extra, ux endorsed). `gt auto-test-pr: pause-rig-7d` → patrol-side
state-bead write. The CLI is the canonical pause path; the magic
phrase is the under-fire fallback that doesn't require finding the
config or the CLI.

**D10. Per-cycle wall-clock cap of 30 minutes** (scale leg open
question 1, this synthesis ratifies). Polecat exits with NOTES on
overrun; rig auto-cools-down for the week; Overseer notified after 3
in a row. This is the v1 budget; tunable per-rig in v2.

**D11. Mutant-sanity bounded to ≤5 mutants per test** (scale leg's
narrow guard). Even if a test covers 50 lines, mutate ≤5. Hard-coded
in the formula, not user-configurable (honors Q4).

**D12. Failed cycles do not consume the per-rig cadence budget**
(scale leg open question 5, this synthesis ratifies). A cycle that
hits a wall-clock cap or fails all gates triggers a 24-hour cycle-
failure backoff, then the next scheduled tick attempts again. This
prevents a slow package from silently consuming the weekly slot.

**D13. Sling priority floor for auto-test beads is in scope.**
(integration constraint #2.) If a strict priority floor doesn't
exist in sling today, implementing it is part of this project. Auto-
test work is the lowest-priority bucket — never starves user work.

**D14. The `gt:auto-test-pr` label is bead-applied, not PR-applied.**
(integration constraint #6.) v1 has no GitHub PR; the label lives on
the dispatch and MR beads. Feedback-patrol queries beads by label.

**D15. Auto-test MRs require explicit maintainer approval before
Refinery merges.** **PRD G1 fix.** PRD says "gated by ordinary human
PR review (not auto-merged)." Refinery is unmodified, so by default it
would merge any polecat MR whose gates pass — that violates G1.
Resolution: per-rig config key
`auto_test_pr.require_review_approval=true` (default-true on opted-in
rigs); Refinery's merge handler reads the bead label
`gt:auto-test-pr` and refuses to merge until a maintainer-approval
record exists on the MR bead (mirrors the existing approval mechanism
used for human-authored MRs in repos that require review). Approval
is recorded by a `bd update <mr-bead> --add-label approved-by:<user>`
or equivalent (canonical mechanism per existing Refinery convention).
v2 may permit a `confidence-merge` mode behind explicit Overseer
opt-in. Without this gate, the system "lands net-new tests
autonomously" — directly violating the "not auto-merged" half of G1.

**D16. SEV-1 incident-response path is automated.** **PRD Q6 fix.**
PRD Q6 SEV-1: "auto-test PR breaks main CI on any rig (revert
immediately, pause that rig 7d, notify Overseer)." The plan must
implement the detect → revert → pause → notify chain, not just name
the SEV. Resolution: Mayor subscribes to main-CI-break events for
opted-in rigs (existing patrol infrastructure). On a main-CI-break
whose attributing commit's MR-bead carries the `gt:auto-test-pr`
label, Mayor automatically (a) files a revert MR via the existing
revert-MR formula, (b) CAS-transitions the rig's state bead to a new
terminal-ish state `paused-by-circuit-breaker` with a 7-day cooldown,
(c) increments the town-wide circuit-breaker counter, (d) sends a
high-priority nudge to the Overseer with the SEV-1 payload. This is
not a backstop — it's the *primary* SEV-1 response. Manual override
is `gt auto-test-pr resume --rig=<rig> --override-circuit-breaker`.

**D17. Phase-1 manual revision CLI fallback.** **PRD G4 fix.** PRD G4
requires "feedback-driven revision on the same PR." The plan's
automated revision routing lives in Phase 2 via `mol-pr-feedback-
patrol`. To prevent G4 from being unreachable during Phase 1's pilot,
v1 ships a *manual* fallback: `gt auto-test-pr revise --mr=<id>
[--comment-id=<id>]` lets a maintainer trigger the revision polecat
directly. The CLI: (a) reads the MR bead, (b) extracts comment thread
+ last commit SHA, (c) CAS-transitions rig state bead `mr-pending →
mr-revising`, (d) files a sling-context bead with `args.mode=revise`,
(e) dispatches `mol-polecat-work-test-improver`. After Phase 2's
automated routing lands, the manual CLI is preserved as an escape
hatch for cases the patrol misses. The CLI is documented in the MR
banner as the Phase-1 fallback path so maintainers can find it.

**D18. Cooldown-release transition is automatic and Mayor-driven.**
**PRD S1 fix.** PRD scenario S1 ("Twice a week the mechanism wakes
up...No PR is open when the next cycle ticks; a new one gets opened.")
requires the cycle to re-fire on a per-rig cadence after a prior MR
lands. The state machine as drawn (round 1) has no edge out of
`cooled-down`, so the rig would enter `cooled-down` once and never
again be eligible to fire. Resolution: Mayor's hourly cycle tick adds
a step *before* state-read: for each opted-in rig in `cooled-down`,
if `now - last_transition.at >= cadence_days * 24h`, CAS-transition
`cooled-down → idle`. Failed CAS (concurrent transition) is benign —
next tick retries. Polecat is uninvolved (gu-gal8). The transition
record names Mayor as actor and `cadence-elapsed` as the trigger.
Cycles in `paused-by-circuit-breaker` (D16) do **not** auto-release
— they require explicit `gt auto-test-pr resume`. Added **R22** to
the risk register (cadence-release miss → silent pilot stall).

**D19. Reviewer comment threads are replied to in revise mode.**
**PRD S3 fix.** PRD scenario S3 explicitly requires "the comment
thread is replied to" after a revision lands. The plan's revise mode
(via D17 manual CLI in Phase 1, via `mol-pr-feedback-patrol` in
Phase 2) writes a follow-up commit to the same branch but does NOT
specify a reply mechanism on the originating comment thread, so a
maintainer who left a comment has no signal that the polecat acted on
it. Resolution: `mol-polecat-work-test-improver` in `mode=revise`
emits a structured reply on each comment thread referenced in
`args.revision.comments[]` after the new commit is pushed. The reply
is a templated banner that names: (a) the new commit SHA, (b) which
gates passed, (c) a one-line summary of what the polecat changed in
response to that comment. Replies go through the same channel as the
MR (Refinery in v1: a follow-up bead-comment threaded against the
review-comment bead; v2 external mode: a GitHub PR review reply).
For `mode=revise` invocations triggered by `gt auto-test-pr revise
--mr=<id>` without `--comment-id`, the polecat picks the most recent
non-resolved comment thread and replies there with a generic
"manual revision dispatched by <user>" template. Phase 1 task 12a
(manual CLI) and Phase 2 task 14 (feedback-patrol routing) both
ship this reply step. Added **R23** to the risk register
(silent-revise → maintainer thinks comment was ignored).

**D20. PR size cap is enforced as a quality gate, not as a polecat
self-check.** **PRD OQ2 fix.** PRD Open Question 2 asks "PR size cap
— exactly what?...Need to decide whether this is enforced by the
polecat itself (refuses to write more) or by a post-check that
discards over-budget candidates." The plan's dispatch envelope carries
`size_budget.max_files=3` / `max_loc=200` (Q5) but **no gate verifies
the polecat actually respects it** — a polecat that ignores the
budget would get past every gate. Resolution: add **gate 4g
(size-budget enforcer)** to `mol-polecat-work-test-improver`. After
the test files are written but before MR creation, the gate counts
files added/modified in the diff and added test LOC; hard-fails if
either exceeds the dispatched envelope's budget (defaults: 3 files,
200 added test LOC). Failure exits the polecat with NOTES; no MR is
opened. Rationale for "post-check" over "polecat-self-enforcement":
the gate is structural and unforgeable; self-enforcement relies on
model judgment under prompt-injection pressure. The polecat is still
*told* the budget in the dispatch envelope (so it tries to stay
within it), but the gate is the source of truth. Added **R24** to
the risk register (size-budget bypass → reviewer fatigue from
oversized MRs).

### Open Questions

These need either human input or follow-on cross-team agreement before
build. Ranked by blast radius.

**OQ1. (Authoritative) Does the rig's settings JSON exist as a
distinct artifact today, or is "rig settings" actually the same as
`config.json`?** D2 above assumes settings JSON is a separate operator-
authority surface. If it isn't, we either (a) create one, or (b) fall
back to the in-repo `config.json` and accept the security trade-off
with a CODEOWNERS rule on `auto_test_pr.*` keys. **Owner:** integration
+ data leg follow-up. Decision needed before Phase 0 ships.

**OQ2. Coverage-tool parser dependency: stdlib `go/cover` vs.
`golang.org/x/tools/cover`.** The latter is more capable; the former
adds no new dep. Recommend the latter (richer per-line data); confirm
that `golang.org/x/tools` is already an indirect dep of `gt`.

**OQ3. Should the `gt:auto-test-pr` bead label be reserved /
protected from manual creation?** A human or another patrol could
file a bead with that label and trigger the test-improver formula on
unrelated work. v1 risk is benign (Refinery catches gate failures),
but the label namespace is operator-trusted. Recommend a town-level
"reserved labels" registry; defer enforcement to a small follow-up
bead.

**OQ4. Pinned-bead Metadata reliability.** `Issue.Metadata` is
described in `internal/beads/beads.go:303-305` as an "extension
point." Is it safe to round-trip ~5KB JSON blobs (transition log +
rejection log) on every state transition? Verify before Phase 0;
fallback is a separate metadata-bead-attachment bead per rig.

**OQ5. v1 → v2 mode migration.** When v2 lands external-PR mode, the
existing `gh pr create` tap-guard (`internal/cmd/tap_guard.go`) must
learn to allow the auto-test-pr polecat. Out of scope for v1, but
the v2 migration plan should call this out so the guard isn't a
silent blocker on the first external-rig pilot.

**OQ6. "Two consecutive merges without intervention" — pilot
graduation criterion.** Who decides "without intervention"? The
synthesis recommends: Mayor reads MR-bead history; the criterion is
operationalized as "two MR beads in `cooled-down (merged)` state with
no `revision` transitions in between." Confirm with Overseer before
Phase 1 → Phase 2.

**OQ7. Pre-existing intent-comment exception in TALON-style codebases.**
TALON team conventions forbid comments in test code. The provenance
marker (D8) is a hard exception. Document this explicitly in the
conventions sheet template so future rigs that adopt TALON style
don't reject it via auto-CR rules.

### Trade-offs

**T1. Single CLI namespace vs. split surface.** Single-namespace
loses the strict "rig settings live in `gt rig`" convention but wins
on discoverability and incident-response muscle memory. The pilot
size makes this trade-off lopsided in favor of the single namespace.

**T2. Bead-as-state vs. dedicated SQLite.** Bead-as-state reuses
gu-gal8-aligned patterns and gives CAS for free, but a per-rig blob
that grows with bounded history (50 transitions, 200 rejections)
trades query power for simplicity. At >100 rigs, a town-wide query
("rejection rate across rigs in last 7d") becomes O(rig_count). The
synthesis accepts this; v3 can add a dedicated store if needed.

**T3. Sandboxed wrapper vs. container isolation.** Sandbox wrapper is
"good enough" defense against the realistic attack (`go test`
running attacker-influenced code with ambient creds). Containers are
strictly stronger but require runtime tooling we don't have on Town
hosts today. The synthesis accepts this trade with the understanding
that v2 graduates to containers.

**T4. Test-files-only allow-list vs. tightly-scoped source patches.**
The PRD allows "no non-test source changes unless absolutely
required." The synthesis goes further: **v1 disallows source changes
entirely**, period. The "absolutely required" escape hatch is too
hard to gate safely against prompt injection in v1. v2 may allow
narrow source edits with a separate review path.

**T5. ≤1 PR/week vs. richer cadence.** A weekly cap is conservative.
Maintainers may wish for more (or less) once the system proves out.
Cadence is configurable in `auto_test_pr.cadence_days`; the v1 default
is 7. The hard town-wide cap is enforced by the cycle's CAS lock —
even if a rig misconfigures cadence, the state machine prevents
parallel cycles.

**T6. Polecat-author identity vs. dedicated bot user.** Q3 ratified
polecat-as-author for v1 (Refinery mode). v2 will need a GitHub App
identity for external-PR mode. The synthesis defers — v1 commits
look identical to any other polecat commit, and the
`gt:auto-test-pr origin=...` marker is the unforgeable provenance.

## Risks and Mitigations

| # | Risk | Severity | Mitigation |
|---|------|----------|------------|
| R1 | Reviewer fatigue from low-quality generated tests → kill-switch flipped, never re-enabled | High | Five quality gates (Q2 MUSTs); circuit-breaker auto-pause after 3 consecutive closes/7d (Q6); ≤1 MR/week cap; pilot graduation gate of 2 consecutive merges before Phase 2 |
| R2 | Prompt-injection of polecat via target source / conventions doc / review comments → adversarial test or backdoor | High | Test-files-only allow-list (D7); sandboxed test runs with credential strip + network drop (D5); `<untrusted-input>` delimiters in polecat prompt; mutant-in-tmpdir (D6); refinery-side banner consistency check |
| R3 | Pilot-on-self feedback loop — auto-test PR breaks `gastown_upstream` main, blue-screens every patrol | High | ≤1 PR/week pilot cadence; circuit breaker; standard Refinery gates protect main; magic-phrase pause is one-comment-away |
| R4 | Secret leakage in fixtures / generated test data | Medium | Pre-push gitleaks (Q6 SEV-2 MUST); refinery-side gitleaks as backstop; sandbox blocks egress so a leaked secret can't be exfiltrated mid-cycle |
| R5 | Polecat writes to `*-auto-test-state` bead, violating gu-gal8 | Medium | Bead-client-layer enforcement in code (security C-SEC-5); polecat-side guardrail; Mayor is the only writer |
| R6 | Wall-clock blow-up on slow packages → polecat slot wedged | Medium | Per-cycle 30-min wall-clock cap (D10); 5-min per-target sandbox cap; polecat exits with NOTES on overrun; cycle-failure backoff (D12) |
| R7 | Refinery MQ collision on shared test files at scale | Low (v1) | Negligible at 1 PR/week; v2 must add an MQ-collision metric per rig (scale leg constraint) |
| R8 | Dolt CAS contention on town-wide bead at 100+ rigs | Low | Per-rig bead is authoritative; town bead is best-effort cache; +1/-1 race tolerance is operationally acceptable for "3 closes" threshold |
| R9 | `mol-pr-feedback-patrol` extension regresses revision routing for non-auto-test PRs | Medium | Phase-2 ships the routing as an early-return `if` behind a feature flag; integration tests cover both labeled and unlabeled fixtures |
| R10 | Conventions sheet drift / absence | Medium | Polecat hard-fails if `.gt/auto-test-pr/conventions.md` missing; opt-in flip is gated on file existence (integration constraint #8) |
| R11 | Branch namespace collision / hijacking — attacker pushes into `auto-test/<rig>/<bead>` | Medium | Branch-protection rule on origin: only Refinery / cycle agent can push to that prefix (security C-SEC-6); refinery rejects MRs from this molecule with non-conforming branch names |
| R12 | Module-cache cold-start triggers re-fetch after network is dropped | Low | Sandbox warms `go mod download` before dropping network; verify `go test -count=10` doesn't trigger a fresh fetch (security open question 1) |
| R13 | Rejection record leaks internal-only file paths in v2 multi-rig federation | Deferred to v2 | v1 is one internal pilot rig; data leg flagged for v2 anonymization |
| R14 | `gt auto-test-pr` is misleading in v1 (no PR is opened) | Low | Document explicitly in CLI help, README, and MR banner that "PR" is a generic term and v1 produces an MR; rename rejected as mid-v1 churn |
| R15 | Auto-test MR breaks `main` CI on a rig and cascades to other patrols | High | D16 SEV-1 path: Mayor subscribes to main-CI-break events; auto-files revert MR + 7d circuit-breaker pause + Overseer SEV-1 nudge. Phase 0 task #11 implements; tested with both labeled-break (auto-reverts) and unlabeled-break (no action) fixtures |
| R16 | Auto-test MR auto-merges before any human reviewer sees it (G1 violation) | High | D15 maintainer-approval gate; Refinery refuses to merge label=`gt:auto-test-pr` MRs without `approved-by:<user>` label when `auto_test_pr.require_review_approval=true` (default-true); Phase 0 task #10 implements |
| R17 | G4 (revision on same branch) is unreachable during Phase-1 pilot | Medium | D17 manual revision CLI `gt auto-test-pr revise`; documented in MR banner as Phase-1 fallback path; Phase-2 automation supersedes but CLI persists as escape hatch |
| R18 | Polecat encodes a buggy current behavior as "correct" via a passing test, papering over a real bug | Medium | Bug-discovery NOTES protocol: polecat exits with structured `BUG-DISCOVERED:` NOTES on test-fails-on-main; Mayor's cycle-close handler files a separate P2 bug bead. No test-only MR is opened on the buggy area |
| R19 | Allow-list `**/*_test.go` admits integration tests (Non-Goal violation) | Medium | Gate 4f extended to reject files under `integration/`/`e2e/`/`test/` and tests with `//go:build integration` build tag; conventions sheet template forbids integration tests |
| R20 | Polecat writes a `Benchmark*`/`Example*`/`Fuzz*` function in a same-package `*_test.go` (slips past gate 4f directory/build-tag check; violates Non-Goal NG2 "unit tests only / no load tests") | Medium | Gate 4f extended (round 2) to reject any newly-added test-form other than `func Test*(t *testing.T)`; conventions sheet template forbids non-unit test forms |
| R21 | Target file is recently-churned but the polecat writes tests for legacy untouched branches in the same file (Non-Goal NG5 violation: de-facto retroactive cleanup) | Medium | Within-file churn-proximity ranking on `uncovered_branches[]` in the dispatch envelope (round 2 fix in cycle step 4); conventions sheet template directs the polecat to prefer recent-churn-adjacent branches |
| R22 | State machine has no `cooled-down → idle` edge; pilot rig fires once and never again (PRD S1 violation: "twice a week the mechanism wakes up") | High | D18 cadence-elapsed auto-release: Mayor's tick CAS-transitions `cooled-down → idle` when `now - last_transition.at >= cadence_days * 24h` and `enabled=true`; `paused-by-circuit-breaker` requires explicit resume |
| R23 | Polecat pushes a revision commit but never replies to the originating review-comment thread; maintainer thinks the comment was ignored (PRD S3 violation) | Medium | D19 reply step in `mol-polecat-work-test-improver mode=revise`: emit templated bead-comment / GH PR review reply on each thread in `args.revision.comments[]` with new commit SHA + gates passed + one-line summary; `gt auto-test-pr revise` without `--comment-id` replies on most-recent non-resolved thread |
| R24 | Polecat ignores the `size_budget` envelope and writes a 500-LOC / 8-file test diff; reviewer fatigue (PRD G5 / OQ2 unresolved) | Medium | D20 gate 4g size-budget enforcer: post-implement, pre-MR-creation diff count of files added/modified and added test LOC; hard fail if either exceeds dispatched budget; structural enforcement, not polecat self-judgment |

## Implementation Plan

Three phases. Each ships independently; each reverts independently
by reverting one PR.

### Phase 0: Substrate prep (no behavior change, no opt-in)

Goal: ship all the wiring inert, so Phase 1 is a single-flag flip.

1. Add `auto_test_pr.*` keys to per-rig settings JSON loader. Default
   absent → disabled. **OQ1 must be answered first.**
2. Ship `gt auto-test-pr {enable,disable,pause,resume,status,show,
   history,revise}` CLI commands. `status` reports "no rigs opted in"
   when the town bead has zero entries. `pause --all` and `resume
   --all` write to the town bead but no patrol consumes them yet.
   **`revise --mr=<id> [--comment-id=<id>]`** is the manual-fallback
   from D17 (Phase-1 revision pathway when feedback-patrol routing is
   not yet live).
3. Land `mol-polecat-work-test-improver` formula extending
   `mol-polecat-work` with the five quality-gate steps, the bug-
   discovery NOTES protocol, and the sandbox wrapper. **No molecule
   registers it yet.**
4. Land `mol-auto-test-pr-cycle` formula. Registered in Mayor's
   patrol set, but the first step is `if no rig has
   auto_test_pr.enabled == true → exit 0`. Inert.
5. Implement the sandbox wrapper (`gt sandbox` helper or equivalent)
   — credential strip + network drop + CWD pin + wall-clock cap.
6. Land coverage-delta parser (`internal/autotest/coverage.go` —
   **branch-mode** parser per gate 4a fix), AST-aware mutant runner
   (`internal/autotest/mutant.go`), tautology linter
   (`internal/autotest/tautology.go` — implementing the four
   sub-rules from gate 4d), with full unit tests.
7. Ship sling priority-floor mechanism if not present (D13).
8. Provision `town-auto-test-pr-state` pinned bead with `enabled_rigs:
   []`. Mayor-owned.
9. **Land `mol-auto-test-pr-branch-gc` patrol** (PRD promoted-MUST
   fix). Standing patrol that lists `refs/heads/auto-test/*/*`
   branches across all opted-in rigs, cross-references against each
   rig's state bead and any open MRs, and deletes branches >7 days
   old with no associated open MR or in-flight bead.
10. **Wire D15 maintainer-approval gate into Refinery's merge
    handler.** Refinery refuses to merge an MR bead with label
    `gt:auto-test-pr` unless an `approved-by:<user>` label is also
    present, when the source rig has
    `auto_test_pr.require_review_approval=true` (default-true).
    Backwards-compatible: MR beads without the auto-test label
    behave unchanged.
11. **Wire D16 SEV-1 auto-revert into Mayor's main-CI-break
    subscription.** On a main-CI-break whose attributing commit's
    MR-bead carries `gt:auto-test-pr`: file revert MR + transition
    rig state bead to `paused-by-circuit-breaker` (7d cooldown) +
    increment town circuit-breaker counter + nudge Overseer with
    SEV-1 payload.

**Phase 0 exit criteria:** All formulas parse; all gates have unit
tests (including the four gate-4d sub-rules and gate-4a branch-mode
parser); CLI verbs round-trip through Mayor without dispatching work;
sandbox wrapper works on a hand-rolled fixture; branch-GC patrol
deletes a fixture stale branch in dry-run; Refinery approval gate
unit-tests cover both labeled-and-approved (merges) and labeled-and-
unapproved (refuses) cases; SEV-1 path unit-tests cover both labeled
break (auto-reverts) and unlabeled break (no action).

### Phase 1: Pilot opt-in (`gastown_upstream` only)

Goal: produce 2+ consecutive merged auto-test MRs without
intervention, no SEV-1/SEV-2 incidents.

9. Author and commit `.gt/auto-test-pr/conventions.md` and
   `.gt/auto-test-pr/mr-template.md` to `gastown_upstream`. Manual
   author by a Go-fluent maintainer; reviewed via PR.
10. Provision `<rig>-auto-test-state` pinned bead for
    `gastown_upstream`. Initial state `idle`.
11. Flip `auto_test_pr.enabled=true` in `gastown_upstream`'s settings
    JSON. Cadence: 7 days.
12. **Five-week (weeks 2-6) observation window.** Each cycle:
    - Watched live by an on-call human; first 5 MRs reviewed in real
      time.
    - Wall-clock, gate pass/fail, and reject reasons logged to
      Overseer's channel.
12a. **Manual revision pathway during Phase 1.** Until Phase 2 lands
     the feedback-patrol routing, comment-driven revision is invoked
     manually via `gt auto-test-pr revise --mr=<id>
     [--comment-id=<id>]`. The CLI files a sling-context bead with
     the prior comment thread + last commit SHA, transitions the rig
     state bead from `mr-pending → mr-revising`, and dispatches a
     `mol-polecat-work-test-improver` polecat in `mode=revise`. This
     is the documented G4 fallback for Phase 1; it does NOT count as
     "no operator intervention" against the Phase 2 graduation
     sub-criterion below, but DOES count as a normal cycle for the
     PRD-aligned merge-rate criterion.
13. **Phase 1 exit criteria** (PRD pilot-success-criteria fix —
    adopting PRD bar verbatim):
    - **≥60% merge rate over the first 5 MRs** (≥3 of 5 merged, per
      PRD).
    - **Zero SEV-1 and zero SEV-2 incidents.**
    - **Rejection rate <40% sustained over weeks 2-6** (5-week
      window).
    - *Sub-criterion (graduation gate to Phase 2):* ≥2 consecutive
      merged MRs **with no operator intervention** (no manual
      revisions via `gt auto-test-pr revise`, no manual gate
      overrides). Phase 2 may not start until this sub-criterion is
      also met.

### Phase 2: Feedback-patrol integration

Goal: revision cycles work without human dispatch.

14. Extend `mol-pr-feedback-patrol`'s `dispatch-work` step with
    label-keyed dispatch (D3). Label `gt:auto-test-pr` →
    `mol-polecat-work-test-improver` formula in `mode=revise`.
    Default-other-labels keep current behavior. Behind feature flag
    `feature_flags.auto_test_pr_revision_routing=false` until tested.
15. Ship reviewer magic phrase parsing (D9) in
    `mol-pr-feedback-patrol`. Token: `gt auto-test-pr: pause-rig-7d`.
    Patrol writes the pause to the rig's state bead.
16. Integration tests: fixture MR with label → revision dispatched;
    fixture MR without label → generic dispatch (regression).
17. Flip `feature_flags.auto_test_pr_revision_routing=true` on
    `gastown_upstream` only.
18. Watch for one full revision cycle (reviewer comment → polecat
    revision → re-review → merge). Verify state bead transitions
    `mr-pending → mr-revising → mr-pending → cooled-down`.

**Phase 2 exit criteria:** One end-to-end revision cycle completes
without human intervention.

### Phase 3 (deferred): Generalization

Out of scope for this design but the design must not preclude it.

19. Add a second rig opt-in (e.g., a TS rig). Add TypeScript to the
    language allow-list (CR-gated, Overseer sign-off per Q4).
20. Land the v2 PRD for external-PR mode + GitHub App identity.
21. Migrate state-bead schema to v2 (additive; new states for
    `pr-pending`/`pr-revising`).
22. Tap-guard amendment for `gh pr create` on auto-test polecats
    (OQ5).

### Reverting

Each phase reverts independently:

- **Phase 2 revert:** flip `feature_flags.auto_test_pr_revision_routing
  =false`. Patrol stops routing revisions; in-flight MRs require
  manual revision dispatch.
- **Phase 1 revert:** `gt auto-test-pr disable --rig=gastown_upstream`.
  Cycle's first step exits on next tick. In-flight MR completes (or
  is closed manually); revision routing remains live but inert.
- **Phase 0 revert:** drop the formulas and the CLI command. Settings-
  JSON keys become inert but harmless.

## Appendix: Dimension Analyses

Full dimension analyses live alongside this synthesis in
`.designs/auto-test-pr/`. They are the authoritative source for
tradeoffs and details *within* each dimension; this synthesis
records cross-dimension decisions and resolves conflicts.

- **API & Interface** — `.designs/auto-test-pr/api.md` (`gu-leg-vha3g`).
  CLI verb tree, dispatch-bead envelope shape, code-level marker
  format, MR banner contract, `gt rig config` integration.
- **Data Model** — `.designs/auto-test-pr/data.md` (`gu-leg-svhds`).
  Pinned-state-bead schema (per-rig + town-wide), state machine,
  CAS semantics, language allow-list, lifecycle table, schema
  evolution.
- **Integration** — `.designs/auto-test-pr/integration.md`
  (`gu-leg-auvdq`). Component touchpoints, dependency map,
  migration phases, backwards-compatibility analysis, testing
  strategy, code locations, feature-flag layering.
- **Scalability** — `.designs/auto-test-pr/scale.md` (`gu-leg-44w2u`).
  Six scaling axes, per-cycle wall-clock budgets, mutant-check
  cost analysis, Refinery MQ collision rates, v2/v3 escape hatches
  (coverage cache, single-tmpdir mutant runs, async pipelined
  fleet).
- **Security** — `.designs/auto-test-pr/security.md` (`gu-leg-sbpyq`).
  Threat model, sandbox profile, prompt-injection mitigations,
  output allow-list, branch-namespace protection, ten hard
  constraints (C-SEC-1 through C-SEC-10).
- **User Experience** — `.designs/auto-test-pr/ux.md` (`gu-leg-nehua`).
  Four user personas, CLI surface tradeoffs, `status`/`show` output
  shapes, MR banner content, discoverability hooks, magic-phrase
  pause UX.

### Cross-leg conflicts resolved

| Conflict | Legs | Resolution | Section |
|----------|------|------------|---------|
| Where does `auto_test_pr.enabled` live? | api (in-repo `config.json`) vs. data (settings JSON) vs. security (must not be in-repo) | Settings JSON, operator authority | D2 |
| Pause CLI verb structure | api (`gt auto-test-pr pause`) vs. ux (single namespace) vs. integration (no preference) | Single namespace, `gt auto-test-pr pause` | D1 |
| Mutant tmpdir scope | scale (single tmpdir per cycle, faster) vs. security (separate tmpdir per mutant, safer) | Separate tmpdir per mutant in v1; consolidate in v2 once we have container isolation | D6 |
| User-facing state names | data uses `mr-pending`, ux uses `MR submitted` | Use `mr-pending` raw (advanced); show "MR submitted" in `gt auto-test-pr status` table; expose raw via `--verbose` | ux leg constraint |
| Provenance marker vs. TALON "no comments in tests" | ux + api want marker; some rigs forbid test comments | Marker is mandatory; document the exception in the conventions sheet template | OQ7 |
| Refinery default-merge vs. PRD G1 "not auto-merged" | plan said Refinery is unmodified; PRD requires human review | Default-true `require_review_approval` flag; Refinery refuses to merge `gt:auto-test-pr`-labeled MRs without `approved-by:<user>` label | D15 (PRD-align round 1) |
| Phase-2-only revision routing vs. PRD G4 | plan deferred routing to Phase 2; G4 must work end-to-end on pilot | Manual `gt auto-test-pr revise` CLI fallback in Phase 1; Phase 2 automation supersedes but CLI persists | D17 (PRD-align round 1) |
| Plan pilot exit criteria vs. PRD pilot success criteria | plan said "≥2 consecutive merged"; PRD said "≥60% over 5 PRs / weeks 2-6" | PRD criteria adopted verbatim; plan's "≥2 consecutive non-intervention" demoted to graduation sub-criterion | Phase 1 exit criteria (PRD-align round 1) |
| State machine missing `cooled-down → idle` edge vs. PRD S1 "twice a week wakes up" | plan's state machine had no cooldown-release path; cycle would fire once per rig and never again | Mayor cadence-elapsed CAS-transition `cooled-down → idle` after `cadence_days * 24h`; paused-by-circuit-breaker requires explicit resume | D18 (PRD-align round 3) |
| Revise mode pushes commits but doesn't reply to comment threads vs. PRD S3 "the comment thread is replied to" | plan handled routing but never specified a reply mechanism; maintainer would see no signal | Templated reply on each `args.revision.comments[]` thread (bead-comment in v1, GH review-reply in v2) with new SHA + gates + summary | D19 (PRD-align round 3) |
| `size_budget` envelope vs. PRD OQ2 "polecat self-enforces or post-check?" | plan dispatched the budget but no gate verified compliance; polecat could ignore it | Gate 4g post-implement diff-count enforcement; structural, not model-judgment | D20 (PRD-align round 3) |
| C2 (Refinery vs external-PR mode detection) vs. Q1 (v1 cut external-PR) | constraints reviewer flagged C2 as "v1 implementation missing" | C2 satisfied by *scope removal*, not by detection; v2 must add detection step | D2b (PRD-align round 2) |
| Gate 4f only checks file paths/build tags, not test-function form | non-goals reviewer flagged that `Benchmark*`/`Example*`/`Fuzz*` slip past gate 4f → violates NG2 | Gate 4f extended to require `func Test*(t *testing.T)` form on every newly-added test function | Gate 4f (PRD-align round 2) |
| Within-file target ranking treats all uncovered branches equally | non-goals reviewer flagged that legacy branches in churned file get backfilled → violates NG5 (greenfield only) | Cycle step 4 ranks `uncovered_branches[]` by line-distance to recent-churn ranges; conventions sheet directs polecat to prefer churn-adjacent | Cycle step 4 (PRD-align round 2) |

## Sources

- `.prd-reviews/auto-test-pr/prd-draft.md` — clarified PRD with
  Q1–Q7 decisions (commit `13d14a44`)
- `.prd-reviews/rqoca/prd-review.md` — synthesized parallel PRD
  review (7 critical questions and answers)
- `.prd-reviews/rqoca/{ambiguity,scope,gaps,requirements,
  stakeholders,feasibility}.md` — six-leg PRD review analyses
- `.designs/auto-test-pr/api.md` (gu-leg-vha3g)
- `.designs/auto-test-pr/data.md` (gu-leg-svhds)
- `.designs/auto-test-pr/integration.md` (gu-leg-auvdq)
- `.designs/auto-test-pr/scale.md` (gu-leg-44w2u)
- `.designs/auto-test-pr/security.md` (gu-leg-sbpyq) — landing on
  origin/main from a sibling polecat's MR; quoted via the leg's
  output as authoritative
- `.designs/auto-test-pr/ux.md` (gu-leg-nehua)
- `.plan-reviews/auto-test-pr/prd-align-round-1.md` — PRD-alignment
  round 1 (requirements + goals); applied 6 must-fix and 4 should-fix
  items to this synthesis (D2a, D15, D16, D17, R15-R19, gate 4a/4d/4f
  tightenings, Phase 0 tasks 9-11, Phase 1 step 12a, Phase 1 exit
  criteria rewrite, target-pick rejection-cooldown, bug-discovery
  NOTES protocol)
- `.plan-reviews/auto-test-pr/prd-align-round-2.md` — PRD-alignment
  round 2 (constraints + non-goals); applied 3 fixes to this synthesis
  (D2b scope-clarification, gate 4f Test*-form check, cycle step 4
  within-file churn-proximity ranking; conventions sheet template
  amendments; R20/R21 risk-register additions)
- `.plan-reviews/auto-test-pr/prd-align-round-3.md` — PRD-alignment
  round 3 (user-stories + open-questions); applied 3 must-fix items
  to this synthesis (D18 cooldown-release transition + state-machine
  edge, D19 reviewer-comment-thread reply step in revise mode, D20
  gate 4g size-budget enforcer; R22-R24 added to risk register;
  cross-leg conflicts table extended)

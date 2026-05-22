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
   any candidate whose path appears in a rejection attachment bead
   (`gt:auto-test-pr-attachment` + `kind:rejection` + `rig:<rig>`)
   with `cooldown_until > now` per the OQ4-fallback materializer.
   This honors the PRD's "avoid retargeting that file for some
   cooldown period" without requiring per-cycle human input — and
   without RMW into the parent state bead's `Issue.Metadata`, which
   the spike (`gu-g9ufm`) showed is unreliable under concurrency. **Within-file churn-proximity
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
   and emits a nudge → Mayor transitions `mr-pending → cooled-down`
   AND files an attachment bead per the OQ4 fallback: a
   `kind:transition` attachment on every closure, plus a
   `kind:rejection` attachment on close-unmerged. Both go through
   `bd create` (CAS-safe; see §Data Model "OQ4 fallback").

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

The per-rig bead's `Issue.Metadata` is **bounded to single-writer
fields only** — `schema_version`, `state`, `current_cycle`,
`last_cycle_at`, `last_cycle_outcome`, `paused_until`, and the
≤20-entry `incidents[]` log (operator-authored, also single-writer).
The high-cardinality `transition_log` and `rejection_log` move to
**attachment beads** per the **OQ4 fallback** (see §Data Model
"OQ4 fallback" subsection): each transition / rejection is a new
immutable bead linked to the parent state bead. The cycle-close
handler creates these via `bd create`, which is naturally CAS-safe;
reads materialize the logs by listing attachment beads filtered by
`rig:<rig>` + `kind:{transition,rejection}` labels and folding by
timestamp. This sidesteps the lost-update class that the OQ4 spike
(`gu-g9ufm`) demonstrated against `Issue.Metadata` RMW under
concurrency (~60/100 writers' contributions clobbered).

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

To approve for merge: bd update <mr-bead> --add-label approved-by:$USER
                      (Refinery refuses to merge without this label
                      when auto_test_pr.require_review_approval=true; D15)
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
| `<rig>-auto-test-state` pinned bead (state machine + **single-writer config** only: `schema_version`, `state`, `current_cycle`, `last_cycle_at`, `last_cycle_outcome`, `paused_until`, **incidents log ≤20** [round 3 fix #3]; transition_log and rejection_log moved to attachment beads — see **OQ4 fallback** below) | Beads / Dolt | Per opted-in rig, persists for opt-in duration | Mayor only |
| **Transition attachment bead** (per-transition immutable bead, labels `gt:auto-test-pr-attachment` + `kind:transition` + `rig:<rig>`, depends-on `<rig>-auto-test-state`; `Issue.Metadata` carries `{from,to,at,actor,context}`) — replaces `transition_log` per OQ4 fallback | Beads / Dolt | Append-only; cycle-close handler creates one per state transition; closed by branch-GC patrol after 60d (see OQ4 retention) | Mayor only |
| **Rejection attachment bead** (per-rejection immutable bead, labels `gt:auto-test-pr-attachment` + `kind:rejection` + `rig:<rig>`, depends-on `<rig>-auto-test-state`; `Issue.Metadata` carries `{file,rejected_at,reason,cooldown_until}`) — replaces `rejection_log` per OQ4 fallback | Beads / Dolt | Append-only; cycle-close handler creates one on close-unmerged; closed by branch-GC patrol 30d after `cooldown_until` | Mayor only |
| `town-auto-test-pr-state` pinned bead (global pause, circuit-breaker counter, denormalized rig summary) | Beads / Dolt | One, town-wide | Mayor only |
| `auto_test_pr.*` config block | Per-rig settings JSON | Per-rig, edited via `gt auto-test-pr enable`/`disable` | Rig owner / town admin via `gt` CLI |
| Conventions sheet | In-repo `.gt/auto-test-pr/conventions.md` | Per-rig, source-controlled | Rig maintainers via PR review |
| Language allow-list | `internal/autotestpr/languages.go` | Town-wide, ships with the binary | Town developers via Refinery CR |
| Code marker | In-repo source files | Per-test, lives with the test forever | Polecat writes, humans review |
| Branch name `auto-test/<rig>/<bead-id>` | Ephemeral remote ref | Until merge or 7d-stale GC | Polecat creates; branch-GC patrol cleans |
| Dispatch / MR / cycle beads | Beads / Dolt | Standard bead lifecycle | Standard |

**State machine** (Q7):
```
   ┌──────────────────────────────────────────────────────┐
   │                                                      │
   ▼                                                      │
 idle ──[mayor-dispatch]──► picking ──► dispatched        │
                                          │                │
                                          ▼                │
                              ┌─── mr-pending ◄──┐         │
                              │       │  ▲       │         │
                              │       ▼  │       │         │
                              │   mr-revising    │         │
                              │       │          │         │
                  [merge-handler]     │ [revise-handler]   │
                              │       │          │         │
                              └──────►┴──────────┘         │
                                      │                    │
                                      ▼                    │
                                cooled-down ───────────────┘
                                      │       [cadence-elapsed]
                                      │
                                ANY-STATE
                                      │
                                      ▼
                          paused-by-circuit-breaker
                                      │
                                      ▼
                       (no auto-edge — operator-only)
                          [gt auto-test-pr resume
                           --override-circuit-breaker]
                                      │
                                      └──► idle
```

The seven states and their transition triggers:

| From | To | Trigger | Actor |
|------|-----|---------|-------|
| `idle` | `picking` | mayor-dispatch (rig opted in, cooldown elapsed) | Mayor |
| `picking` | `dispatched` | dispatch bead filed | Mayor |
| `dispatched` | `mr-pending` | polecat `gt done` → MR bead exists | (transition observed by Mayor) |
| `mr-pending` | `cooled-down` | merge-handler (merged or closed-unmerged) | Mayor cycle-close handler |
| `mr-pending` | `mr-revising` | revise-handler (manual D17 CLI or D3 patrol) | Mayor / patrol |
| `mr-revising` | `mr-pending` | polecat re-pushes commit | (transition observed) |
| `cooled-down` | `idle` | cadence-elapsed (D18: `now − last_transition.at ≥ cadence_days · 24h`) | Mayor tick |
| any state | `paused-by-circuit-breaker` | ci-break-handler (D16 SEV-1) OR cycle-close handler (3 closes in 7d, Q6 SEV-2) | Mayor |
| `paused-by-circuit-breaker` | `idle` | operator `gt auto-test-pr resume --override-circuit-breaker` (D16 — no auto-release) | Operator |

Transitions are append-only as **attachment beads** (one bead per
transition, labels `gt:auto-test-pr-attachment` + `kind:transition`
+ `rig:<rig>`, depends-on the parent state bead) per the **OQ4
fallback** above. Each `bd create` mints a new ID, so there is no
shared row to clobber under concurrent writers — this fixes the
spike's acceptance #2 failure (`gu-g9ufm`). The state-machine field
on the parent pinned bead (`state`, `current_cycle`) IS still updated
in place; that surface IS reliable per the spike's acceptance #1
(single-writer sequential round-trips PASS) and the cycle-close
handler is the only writer to it, serialized through Dolt
SERIALIZABLE-class isolation on the bead's row. **Cooldown release
(PRD S1 fix):** the `cooled-down → idle` edge fires when Mayor's
tick observes the most-recent transition attachment for the rig has
`at + cadence_days * 24h <= now` AND the rig is still `enabled=true`
(read via the materializer over the rig's transition attachments;
see §Data Model "OQ4 fallback"). Without this transition the cycle
would never re-fire after the first MR — S1's "twice a week the
mechanism wakes up" requires an automatic cooldown-release path. See
**D18**.
**`paused-by-circuit-breaker` is explicitly excluded from cadence-
elapsed auto-release** (D16 / D18) — only operator action via
`gt auto-test-pr resume --override-circuit-breaker` exits this
state, which is the design contract for the SEV-1 manual-recovery
path.

**Schema versioning:** every JSON blob carries `schema_version`. v2
readers tolerate v1 blobs (defaults for new fields); v1 readers
tolerate v2 blobs (round-trip unknown fields via `json.RawMessage`).


### OQ4 fallback: metadata-attachment-bead pattern

The Phase 0a-3 spike (`gu-g9ufm`,
`internal/cmd/metadata_reliability_integration_test.go`) **PASSED**
acceptance #1 (100/100 sequential ~7-8KB blob round-trips
byte-for-byte equal — `Issue.Metadata` is a faithful storage surface)
but **FAILED** acceptance #2: under 100 concurrent goroutines using
the production read-modify-write pattern from
`internal/beads/store.go::mergeMetadataKey`, ~60/100 writers'
contributions were lost to clobber. `Issue.Metadata` is *not*
reliable as a multi-writer surface without external CAS. The
Auto-Test-PR design's `transition_log` / `rejection_log` writes
sit exactly on that pattern (Mayor patrol + revision-routed
polecats both writing to the same bead), so this fallback re-shapes
those two logs to use the **metadata-attachment-bead** pattern that
data-leg OQ4 always pointed to as the safe alternative.

**Why this works:** `bd create` mints a new ID per call. There is
nothing to clobber. Append-only history is what the design's two
logs actually want — they are *audit logs*, not mutable state —
and the read-side cost is tolerable at v1 scale (≤50 transitions
+ ≤200 rejections per opted-in rig, with one opted-in rig in
Phase 1).

**Attachment-bead schema.** Each transition or rejection is a single
new bead. The bead carries:

| Field | Purpose |
|-------|---------|
| `Issue.Title` | One-line human-readable summary, e.g., `auto-test-pr transition gastown_upstream: mr-pending → cooled-down @ 2026-05-21T14:23:00Z` |
| `Issue.Type` | `task` (standard bead type; not `pinned`) |
| `Issue.Labels` | MUST contain all of: `gt:auto-test-pr-attachment` (umbrella discriminator), `kind:transition` OR `kind:rejection`, `rig:<rig>` (e.g., `rig:gastown_upstream`), and the parent's `gt:auto-test-pr` umbrella |
| `Issue.Metadata` | Versioned JSON payload — see schema below per kind. Single-writer (Mayor cycle-close handler only); never RMW |
| `Issue.Status` | `open` while within the retention window; `closed` after retention elapses (branch-GC patrol). Closed attachments stay readable; closure does not delete the row |
| Dependency edge | The attachment bead `depends_on` the parent `<rig>-auto-test-state` pinned bead, so `bd show <state-bead>` walks naturally show recent attachments and so the dependency graph documents lineage |
| Created by | Mayor identity (`actor=mayor`) — gu-gal8 forbids polecat-owned bookkeeping beads, and this rule extends to the attachments |

**Per-kind metadata payloads** (mirror the previous in-blob entries):

```json
// kind:transition
{
  "schema_version": 1,
  "rig": "gastown_upstream",
  "from": "mr-pending",
  "to":   "cooled-down",
  "at":   "2026-05-21T14:23:00Z",
  "actor": "refinery",
  "context": {"mr_id": "gu-mr-abc12", "merged_sha": "abc1234"}
}
```

```json
// kind:rejection
{
  "schema_version": 1,
  "rig": "gastown_upstream",
  "file": "internal/foo/bar.go",
  "rejected_at": "2026-05-19T10:00:00Z",
  "reason": "wrong-target",
  "cooldown_until": "2026-06-02T10:00:00Z",
  "mr_id": "gu-mr-abc09"
}
```

**Materialize-from-attachments read path.** The materializer is a pure
function over a list-by-label query plus a recency window. It does
not mutate any bead.

```go
// MaterializeAutoTestState reads the per-rig logs by listing
// attachment beads. Returns the same shape the previous in-blob
// transition_log[] / rejection_log[] returned, so callers don't
// branch on storage form.
func MaterializeAutoTestState(b *beads.Beads, rig string) (
    transitions []TransitionRecord,
    rejections  []RejectionRecord,
    err error,
) {
    // One server-side label query per kind. Both queries fan out
    // through the existing `bd list --label=...` path which already
    // exists in beads.go::List.
    txAtt, err := b.List(beads.ListOptions{
        Label: "gt:auto-test-pr-attachment",
        // status=all so closed (retired) attachments still surface
        // for audit reads.
        Status: "all",
    })
    if err != nil { return nil, nil, err }

    for _, a := range txAtt {
        // Defensive: filter by the kind:* and rig:* label pair on
        // the client because List() takes one --label flag.
        if !hasLabel(a, "rig:"+rig) { continue }
        switch {
        case hasLabel(a, "kind:transition"):
            tr, perr := parseTransition(a.Metadata)
            if perr != nil { continue } // skip schema drift
            transitions = append(transitions, tr)
        case hasLabel(a, "kind:rejection"):
            rj, perr := parseRejection(a.Metadata)
            if perr != nil { continue }
            rejections = append(rejections, rj)
        }
    }
    // Newest-first ordering by attachment's `at` / `rejected_at`.
    sort.Slice(transitions, func(i, j int) bool {
        return transitions[i].At.After(transitions[j].At)
    })
    sort.Slice(rejections, func(i, j int) bool {
        return rejections[i].RejectedAt.After(rejections[j].RejectedAt)
    })
    // Recency window — keep the same caps the in-blob logs had.
    if len(transitions) > 50  { transitions = transitions[:50] }
    if len(rejections)  > 200 { rejections  = rejections[:200] }
    return transitions, rejections, nil
}
```

**Bounds:** identical to the previous in-blob bounds (transitions: 50
newest by `at`; rejections: 200 newest by `rejected_at`). The cap is
enforced by the materializer's slice, not by deletion — old
attachments simply fall out of the window. Per-file rejection cooldown
(synthesis §"Per-file rejection cooldown") still uses
`rejected_at + 21d > now`; closed attachments past the cooldown are
ignored by ranking.

**Retention / GC.** The existing `mol-auto-test-pr-branch-gc` patrol
(Phase 0 task 9) gains a second responsibility: close (not delete)
attachment beads outside their retention window.

| Kind | Retention rule |
|------|----------------|
| `transition` | Close at age > 60d (lookback covers the rolling-7d circuit-breaker window with comfortable margin) |
| `rejection`  | Close at `cooldown_until + 30d` (lookback covers the 21d per-file cooldown plus margin for late audits) |

Beads' append-only model means closed attachments are still readable;
closure just trims them from the materializer's `status=open` view by
default. Audit trails survive forever.

**Concurrency contract** (the whole point of this fallback):

- `bd create` is the only write to attachment beads. It mints a new
  ID per call; there is no shared row to clobber.
- The pinned bead's `Issue.Metadata` is **single-writer-only**: the
  cycle-close handler is the sole writer, and only between
  state-machine transitions of the parent bead, which itself runs
  under Dolt SERIALIZABLE-class isolation on the bead's row. The
  RMW pattern that failed acceptance #2 is no longer used for
  high-cardinality logs.
- Reads through the materializer take a list-by-label snapshot;
  concurrent attachment creation during a read just changes which
  attachments appear — there is no inconsistent state because each
  attachment is itself immutable.

**Acceptance test.** A new integration test (mirror of the OQ4 spike
harness) launches 100 concurrent goroutines that each call `bd create`
to file an attachment bead against the same parent state bead. After
all goroutines finish, materializing the logs MUST recover all 100
attachments. Lives at
`internal/cmd/metadata_attachment_bead_integration_test.go`, gated
by `GT_RUN_OQ4_SPIKE=1` so it does not run in default integration
runs (it shares the spike's host requirement of a real Dolt
container). This satisfies the prerequisite bead `gu-2s03` acceptance
criterion #5.

**Migration.** v1 ships the attachment-bead pattern from day one
(Phase 0 task 8 provisions the pinned bead with bounded single-writer
fields only; no transition/rejection arrays ever exist on the pinned
bead). There is no v0 → v1 migration because Phase 0 is the first
ship. Schema-evolution policy (§Schema versioning) applies to both
the pinned-bead payload and to attachment metadata payloads: every
JSON blob carries `schema_version`; readers tolerate forward-version
fields by round-tripping through `json.RawMessage`.

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
"manual revision dispatched by <user>" template. Phase 1 task 18
(manual CLI) and Phase 2 task 19 (feedback-patrol routing) both
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

**OQ4. Pinned-bead Metadata reliability — RESOLVED (FAIL → fallback adopted).**
Phase 0a-3 spike (`gu-g9ufm`,
`internal/cmd/metadata_reliability_integration_test.go`) **PASSED**
acceptance #1 (100/100 sequential ~7-8KB blob round-trips
byte-for-byte equal — `Issue.Metadata` IS faithful as a single-writer
storage surface) but **FAILED** acceptance #2 (concurrent CAS):
~60/100 writers' contributions are lost to clobber under the
production RMW pattern (`internal/beads/store.go::mergeMetadataKey`).
The fallback prerequisite bead `gu-2s03` re-shapes Phase 0 task 8 +
Phase 1 task 15 to use the **metadata-attachment-bead** pattern for
`transition_log` and `rejection_log`; the pinned bead retains only
single-writer config in `Issue.Metadata` (`schema_version`, `state`,
`current_cycle`, `last_cycle_at`, `last_cycle_outcome`,
`paused_until`, `incidents[]≤20`). See §Data Model "OQ4 fallback"
subsection above for schema, materialize-path, and retention rules.

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
marker (D8) is a hard exception. **v2 follow-up (round 2 fix #11):**
NOT included in the v1 conventions-sheet template — the v1 pilot
rig (`gastown_upstream`) is not TALON-convention. When a TALON-
convention rig opts in for the first time (Phase 3 second-rig
work), the template is amended to include the marker exception so
that rig's auto-CR rules don't reject the provenance marker.

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
| R25 | Go AST footgun in tasks 6b (mutant runner) and 6c (tautology linter) → silent false-negative on mutation/tautology gates (positions vs. line/col, comment handling, generic type parameters in 1.18+, build-tag-dependent files) | Medium | Knowledge-prep sub-step on each task (round 2 fix #9): assigned polecat MUST read `golang.org/x/tools/go/ast/astutil` package docs and at least one real-world AST tool (`go vet`, `staticcheck`, or `errcheck`) before implementation; MUST use `go/parser` + `go/ast` directly (no shelling to `gofmt`/`goimports`); fixture coverage for build-tag-dependent files |
| R26 | `mol-auto-test-pr-cycle` panics on partial Phase 0 revert (town bead absent) → patrol blocks every other patrol | Medium | Round 2 fix #10: missing-town-bead integration test in Phase 0 exit criteria; cycle exits with structured warning, not panic, when `town-auto-test-pr-state` cannot be read |
| R27 | Tautology sub-rule (i) precision/recall below threshold → gate ships with three syntactic sub-rules only; reduced protection against tautological tests | Low (mitigated by spike) | Phase 0a-5 spike with ≥85% precision / ≥75% recall acceptance gate (round 2 fix #4); if threshold not met, sub-rule (i) is omitted from gate 4d with rationale recorded in conventions sheet template; the three syntactic sub-rules remain unconditional |
| R28 | Refinery label-query / `approved-by:<user>` semantics, Mayor main-CI-break subscription, or pinned-bead `Issue.Metadata` durability turn out to be missing/insufficient → Phase 0 cannot complete | High (if hit) / Low (likelihood; mitigated upfront) | Phase 0a (round 2 fix #2) verifies all three before Phase 0 starts; any FAIL files a prerequisite bead and re-shapes the affected Phase 0 task before substrate work begins |

## Implementation Plan

**Four** phases. Phase 0a is a small prerequisite-verification
phase added in plan-self-review round 2 to surface unknowns
*before* Phase 0 commits to ~2 weeks of substrate work. Phase 0,
1, 2 each ship independently; each reverts independently by
reverting one PR. Phase 0 tasks are deliberately small (the
round-1 self-review split fused tasks 2/5/6/3 into independent
sub-tasks); the **Phase 0 dependency graph** below shows what can
parallelize.

### Phase 0a: Prerequisite verification + spikes

Goal: answer every "does X already exist?" / "does the substrate
support Y?" question *before* Phase 0 begins. Each task is
independently fast (hours, not days). Any FAIL outcome reshapes
Phase 0 and is captured by a prerequisite bead before Phase 0
begins. Plan-self-review round 2 added this phase because the
round-1 split of task 10 / task 11 into "verify + wire" left the
verify-step buried mid-Phase-0; if Refinery or Mayor lacks the
required infra, ~2 weeks of Phase 0 substrate work would have
been spent before discovering Phase 0 cannot complete.

0a-1. **Verify Refinery per-MR-bead label query and
      `approved-by:<user>` semantics exist** (D15 prerequisite).
      Inspect `internal/refinery/` and confirm: (a) Refinery's
      merge handler can be conditioned on the presence of a label
      on the MR bead; (b) `bd update <mr-bead> --add-label
      approved-by:<user>` is canonical (or there is an existing
      equivalent). Acceptance: a fixture MR-bead labeled
      `gt:auto-test-pr` *without* `approved-by:<user>` is held by
      Refinery's merge handler (does not merge). If FAIL, FILE a
      prerequisite bead naming the missing infra and re-shape
      Phase 0 task 10 accordingly.

0a-2. **Verify Mayor today subscribes to main-CI-break events for
      opted-in rigs** (D16 prerequisite). Inspect Mayor's patrol
      registration and confirm the event type, the rig-filter
      semantics, and the subscription callback shape. Acceptance:
      a fixture main-CI-break event triggers a Mayor callback
      that can read the attributing commit's MR-bead. If FAIL,
      FILE a prerequisite bead and re-shape Phase 0 task 11.

0a-3. **Pinned-bead `Issue.Metadata` reliability spike** (OQ4
      promoted to must-fix per round 2). Write a synthetic 5KB
      JSON blob (sized at the upper bound of `transition_log[≤50]
      + rejection_log[≤200]`) to a test bead's `Issue.Metadata`,
      read back, verify byte-for-byte. Run 100 round-trips
      concurrently to stress CAS isolation. Acceptance: 100/100
      pass byte-for-byte AND no CAS lost-update detected. If
      FAIL, FILE a prerequisite bead for the
      metadata-attachment-bead fallback (data-leg's documented
      fallback in OQ4) and re-shape Phase 0 task 8 + Phase 1
      task 15.

0a-4. **Answer OQ1: does the rig's settings JSON exist as a
      distinct artifact today, or is "rig settings" the same as
      `config.json`?** D2 above assumes settings JSON is a
      separate operator-authority surface. Outcome dictates Phase
      0 task 1's loader path. If settings JSON exists: proceed.
      If not: either (a) create one in Phase 0 task 1 (adds ~3
      days), or (b) fall back to in-repo `config.json` with a
      CODEOWNERS rule on `auto_test_pr.*` keys (re-litigates D2
      security trade-off; FILE a prerequisite bead before Phase
      0 begins).

0a-5. **Tautology sub-rule (i) precision/recall spike.** Build a
      50-test corpus (25 known-tautological, 25 known-good)
      sampled from real Go test files in `gastown_upstream`. Run
      the candidate flow-sensitive analysis (does any assertion's
      argument depend on a value returned from the
      function-under-test?). Acceptance: ≥85% precision (≤15%
      false-positive on known-good) AND ≥75% recall (≤25%
      false-negative on known-tautological). If threshold met,
      sub-rule (i) ships in gate 4d. If threshold NOT met,
      sub-rule (i) is **omitted from gate 4d**; the gate ships
      with the three syntactic sub-rules (ii/iii/iv) only and
      the conventions sheet template records the omission with
      rationale. Phase 0 task 6c's description is updated to
      reflect the spike outcome.

**Phase 0a exit criteria:**
- All five tasks complete with PASS, or any FAIL has filed a
  prerequisite bead and re-planned the affected Phase 0 task.
- Spike outcomes (0a-3, 0a-5) recorded in
  `.plan-reviews/auto-test-pr/phase-0a-spikes.md` (one-page
  summary per spike).
- **0a-3 outcome:** FAILED acceptance #2 (concurrent CAS); fallback
  prerequisite bead `gu-2s03` filed; this synthesis updated to
  re-shape Phase 0 task 8 + Phase 1 task 15 + Phase 0 step 3c
  (cycle-close handler) to use the **metadata-attachment-bead**
  pattern documented in §Data Model "OQ4 fallback" (this file).
  Phase 0a-3's open question is closed.
- No Phase 0 task is started until 0a is complete (this is the
  whole point of 0a).

### Phase 0: Substrate prep (no behavior change, no opt-in)

Goal: ship all the wiring inert, so Phase 1 is a single-flag flip.

1. Add `auto_test_pr.*` keys to per-rig settings JSON loader. Default
   absent → disabled. **Phase 0a-4 must be PASS first** (settings JSON
   path is the loader's input). **Round 3 fix #7 — acceptance:** unit
   tests cover (a) absent `auto_test_pr` block → returns disabled
   config with default cadence/skip_dirs; (b) well-formed block →
   returns parsed `auto_test_pr.*` keys; (c) malformed JSON or
   unknown `language` value → returns typed error (not a panic).

2a. Ship `gt auto-test-pr enable` and `gt auto-test-pr disable` CLI
    commands. `enable` validates language (`go` only in v1) and rig
    (`gastown_upstream` only in v1); other inputs return static errors
    pointing at the v2 follow-up bead. `disable` writes the
    settings-JSON flag and DOES NOT cancel in-flight work (D2a).
    **Round 3 fix #4 — `enabled_rigs[]` sync.** Both verbs operate on
    *two* surfaces atomically: (i) the per-rig settings JSON (durable
    record of intent) AND (ii) `town-auto-test-pr-state.enabled_rigs[]`
    (denormalized read-cache used by `status`). `enable` writes the
    flag THEN CAS-appends `target_rig` to `enabled_rigs[]`; `disable`
    writes the flag false THEN CAS-removes from `enabled_rigs[]`. If
    the second step fails after the first commits, the CLI exits
    non-zero with a "settings-JSON updated but town bead out-of-sync"
    notice; Mayor's tick reconciles on next iteration (per task 4
    update). Settings-JSON remains authoritative ground truth.
2b. Ship `gt auto-test-pr {pause,resume,status,show,history}` CLI
    commands. `status` reports "no rigs opted in" when the town bead
    has zero entries. `pause --all` and `resume --all` write to the
    town bead but no patrol consumes them yet. `resume --rig=<rig>
    [--override-circuit-breaker]` and `resume --all`: the override
    flag bypasses the `paused-by-circuit-breaker` state per D16 and
    emits an audit-log entry naming the operator and timestamp.
2c. Ship `gt auto-test-pr revise --mr=<id> [--comment-id=<id>]` CLI
    command — the manual-fallback from D17 (Phase-1 revision pathway
    when feedback-patrol routing is not yet live).
2d. **Conventions-sheet template ships with `gt`.** Land
    `internal/autotestpr/conventions_template.md` checked into the
    `gt` binary and exposed via two CLI verbs:
    `gt auto-test-pr enable --emit-template > .gt/auto-test-pr/
    conventions.md` and `gt auto-test-pr show-template` (read-only).
    Template includes the NG2 forbid-list (no integration/e2e/load
    tests; no `Benchmark*`/`Example*`/`Fuzz*`), the NG5
    churn-proximity preference, the provenance-marker requirement
    (D8), and placeholders for rig-specific test conventions (e.g.,
    'no `time.Sleep` in tests', 'use table-driven where ≥3 cases').
    **Round 2 fix #11: OQ7 TALON-style-comment-exception language is
    NOT included in the v1 template** — the pilot rig
    (`gastown_upstream`) is not a TALON-convention codebase, so the
    exception serves a hypothetical future rig. OQ7 itself stays in
    the Open Questions list with a `v2 follow-up` note: when a
    TALON-convention rig opts in for the first time, the template is
    amended to include the marker exception. **Round 3 fix #8 —
    acceptance:** snapshot (golden-file) test of `gt auto-test-pr
    show-template` output verifies the NG2 forbid-list (Benchmark/
    Example/Fuzz/integration/e2e/load), the NG5 churn-proximity
    preference paragraph, the D8 provenance-marker requirement,
    and the D15 approval-line instruction (per round 3 fix #5)
    are all present. Snapshot file lives at
    `internal/autotestpr/testdata/conventions_template.golden.md`;
    drift fails CI.
3a. Land `mol-polecat-work-test-improver` formula skeleton extending
    `mol-polecat-work` with the **`mode=create` path**: the five
    quality-gate steps (4a-g), the bug-discovery NOTES protocol, and
    the sandbox-wrapper integration (depends on task 5c). **No
    molecule registers it yet.** **Round 3 fix #6:** at `gt done`
    time, the polecat MUST label the resulting MR-bead with both
    `gt:auto-test-pr` AND `rig:<target_rig>` (the latter read from
    the dispatch envelope). The `rig:<target_rig>` label is the
    O(1) linkage from MR-bead to per-rig state bead used by the 3c
    cycle-close handler — without it, the handler must walk the
    bead graph back through the dispatch bead to resolve which rig
    the MR belongs to. Unit tests verify both labels are present on
    the MR-bead at `gt done` exit.
3b. Extend `mol-polecat-work-test-improver` with the **`mode=revise`
    path**: reads `args.revision` from the dispatch envelope (prior
    comment thread + last commit SHA + branch name), runs the same
    five gates, and emits the **D19 reply step** on each
    `args.revision.comments[]` thread after the new commit is
    pushed (templated banner: new commit SHA + gates passed +
    one-line summary). For `mode=revise` invocations triggered by
    `gt auto-test-pr revise --mr=<id>` without `--comment-id`, the
    polecat picks the most recent non-resolved comment thread and
    replies there with the "manual revision dispatched by <user>"
    template. Unit tests cover both `--comment-id`-targeted and
    most-recent-thread fallback paths. **Round 3 fix #6:** the
    revise-pushed commit's MR-bead state-change event also resolves
    via the existing `rig:<target_rig>` label set in task 3a — the
    MR-bead is the same bead across the create-revise lifecycle, so
    the label is set once at create time and persists.
3c. **Implement Mayor cycle-close handler.** Subscribes to MR-bead
    state-change events for beads labeled `gt:auto-test-pr`.
    **Round 3 fix #6:** the handler reads the `rig:<target_rig>`
    label off the MR-bead at event time and looks up
    `<target_rig>-auto-test-state` in O(1). On
    merged → CAS-transition the rig's state bead `mr-pending →
    cooled-down` AND **`bd create` a transition attachment bead**
    (labels `gt:auto-test-pr-attachment` + `kind:transition` +
    `rig:<target_rig>`, depends-on the rig state bead, metadata =
    `{schema_version:1, rig, from:"mr-pending", to:"cooled-down",
    at, actor:"refinery", context:{mr_id, merged_sha}}`) per the
    OQ4 fallback. On closed-unmerged → CAS-transition `mr-pending →
    cooled-down`, **`bd create` BOTH a transition attachment AND a
    rejection attachment** (rejection labels
    `gt:auto-test-pr-attachment` + `kind:rejection` +
    `rig:<target_rig>`, metadata =
    `{schema_version:1, rig, file:target_path, rejected_at, reason,
    cooldown_until = rejected_at + 21d, mr_id}`), and increment the
    town-bead circuit-breaker counter (the town bead's counter is a
    single-writer field on a single row — Mayor's cycle-close handler
    is the sole writer, so it remains in `Issue.Metadata` per OQ4
    spike acceptance #1); if the rig has ≥3 closes in any rolling
    7-day window (computed by materializing the rig's transition
    attachments over the window), CAS-transition to
    `paused-by-circuit-breaker` and nudge Overseer (Q6 SEV-2). On
    either path, parse any `BUG-DISCOVERED:` NOTES and file a P2
    bug bead in the rig (`<rig>-bug-from-auto-test-NNN`) linked to
    the cycle's MR bead.
    **Round-3-fix#6+OQ4 fallback wiring:** attachment-bead writes go
    through the same in-process `beadsdk.Storage` the handler already
    holds (no `bd` subprocess fan-out per cycle); the writes are
    independent across the merged / closed-unmerged paths so
    partial-failure semantics are simple — if the transition
    attachment commits but the rejection attachment fails, the next
    tick re-checks the MR-bead state and re-files only the missing
    attachment (idempotent, since attachment IDs are
    cycle-derived: title `auto-test-pr <kind> <rig>: <from>→<to>
    @ <iso8601>` plus the parent MR-bead ID in the dependency edge).
4. Land `mol-auto-test-pr-cycle` formula. Registered in Mayor's
   patrol set, but the first step is `if no rig has
   auto_test_pr.enabled == true → exit 0`. Inert. **Round 3 fix #4 —
   reconcile `enabled_rigs[]`.** Each tick begins with a reconcile
   step: walk all rigs' settings JSON, compute the set of
   `auto_test_pr.enabled=true` rigs, CAS-update
   `town-auto-test-pr-state.enabled_rigs[]` to match. This is
   self-healing for partial-failure cases from task 2a's two-step
   write and for partial Phase 0 reverts. The reconcile is idempotent
   and adds <100ms per tick at the v1 rig count.
5a. Implement sandbox wrapper **credential-strip + CWD-pin**
    component. **Round 2 fix #5: ADR sub-step `5a-pre` runs first.**
    Decide and document whether the sandbox is (a) a wrapper command
    (`gt sandbox <cmd>...`), (b) a library
    (`internal/autotest/sandbox`), or (c) inline per-gate code.
    Recommended: **(b) — a library** used by both the polecat
    formula and the gate runners, because it composes with
    `os/exec.Cmd` and avoids spawning a child process per gate. ADR
    is a one-page note in the rig's design notes, committed
    alongside `internal/autotest/sandbox/doc.go`. 5a, 5b, 5c all
    consume the ADR's chosen substrate; deviation requires ADR
    amendment first.

    After the ADR commits, 5a strips `AWS_*`, `GITHUB_TOKEN`, `BD_*`,
    `DOLT_*`, `GIT_AUTHOR_*`, `GIT_COMMITTER_*`; pins CWD to the
    worktree.
5b. Implement sandbox **network-drop** with module-cache warm-up
    (`go mod download` runs before egress is dropped). **Round 2
    fix #7 acceptance:** a fixture package on which `go mod download
    && drop-net && go test -count=10 ./...` succeeds 10/10 times
    with no fresh network fetch (verified by `tcpdump` or `strace
    -e connect`). If even one rerun triggers a fetch (e.g., a test
    imports a transitively-missing package), the warm-up step is
    amended to also run `go test -count=1 -run='^$' ./...` (a no-op
    test pass that triggers the same package compile graph as the
    test execution does). This subsumes security-leg's open
    question 1.
5c. Implement sandbox **wall-clock cap** (5-min per-target,
    cycle-wide 30-min cap per D10) and integration test of the
    combined wrapper (5a + 5b + 5c) on a hand-rolled fixture.
6a. Land coverage-delta **branch-mode** parser
    (`internal/autotest/coverage.go`) per gate 4a fix. Parses
    `golang.org/x/tools/cover` branch-mode profiles. **Round 3 fix
    #1:** unit tests cover the parser on hand-rolled cover-profile
    fixtures: branch-mode profile with all branches covered → returns
    0 delta; profile with one new test exercising one branch → returns
    +1 covered branch; profile with the comment-only marker present
    but the branch still uncovered → returns 0 delta (the marker
    alone does not satisfy the gate, per gate 4a's hard-fail rule);
    malformed profile → typed error. (Round 1 mistakenly attached the
    gate-4d sub-rule list to this task; those sub-rules are tested in
    task 6c.)
6b. Land **AST-aware mutant runner** (`internal/autotest/mutant.go`).
    Bounded to ≤5 mutants per test (D11); copies package directory
    to `os.MkdirTemp` (D6); runs through sandbox (depends on 5c).
    **Mutation selection (round 2 fix #3):** the runner mutates *only*
    lines marked covered-by-the-test in the test's own coverage
    profile, drawn from a fixed mutation grammar of (i)
    comment-out-line (the gate's literal D6 spec), (ii) negate-boolean
    (flip `!` / swap `==` ↔ `!=`), (iii) return-zero-value (replace
    the first `return` in the function with the type's zero value).
    Selection is deterministic given the file SHA + test name
    (seedable) so reruns are reproducible. ≤5 mutants per test is
    enforced over the *union* of (i)/(ii)/(iii); if more than 5
    mutation candidates exist, the runner picks the 5 with greatest
    expected blast radius (lines with most coverage hits across the
    test suite) so a passing mutant is maximally likely to indicate a
    tautological test. Unit tests cover each grammar form on a
    hand-rolled fixture. **Knowledge-prep (round 2 fix #9):** before
    implementation, the assigned polecat MUST read
    `golang.org/x/tools/go/ast/astutil` package docs and at least one
    real-world AST tool (`go vet`, `staticcheck`, or `errcheck`) to
    absorb conventions for position-handling, comment-handling, and
    build-tag exclusion. Implementation MUST NOT shell out to `gofmt`
    or `goimports` for AST traversal — use `go/parser` + `go/ast`
    directly so the analysis is robust against unparseable input.
6c. Land **tautology linter** (`internal/autotest/tautology.go`)
    implementing the four gate-4d sub-rules. Each sub-rule has
    its own test fixture set under
    `internal/autotest/testdata/tautology/{literal,notnil,
    no-input-derived,zero-assertion}/`. **Sub-rule (i)
    ("≥1 assertion must depend on the function-under-test's return
    value or observable side effect") is spike-gated by Phase 0a-5**
    (round 2 fix #4): if the spike's precision/recall thresholds are
    met (≥85% precision / ≥75% recall on the 50-test corpus), sub-
    rule (i) ships in the gate; if not, the gate ships with sub-rules
    (ii/iii/iv) only and the conventions-sheet template records the
    omission with rationale. The other three sub-rules are syntactic
    and trivially decidable — they ship unconditionally.
    **Knowledge-prep (round 2 fix #9):** same as 6b — read
    `golang.org/x/tools/go/ast/astutil` and one real AST tool before
    implementation; use `go/parser` + `go/ast` directly.
7. Ship sling priority-floor mechanism if not present (D13).
   **Round 3 fix #9 — acceptance:** integration test enqueues two
   beads through `sling --priority-floor=lowest` for an auto-test
   bead and `sling --priority=normal` for a fixture user bead; the
   dispatcher returns the user bead first regardless of submission
   order. If the floor mechanism does not exist pre-this-task,
   ship it; if it exists, write the integration test and confirm
   the existing implementation honors the floor.
8. Provision the **two state beads**: `town-auto-test-pr-state`
   (single-writer fields: `schema_version`, `global_pause_until`,
   `circuit_breaker.{consecutive_closes_townwide, window_started_at,
   tripped_until}`, `enabled_rigs[]`, `rig_summary{}`; Mayor-only
   writer) AND, when the first rig opts in, the per-rig
   `<rig>-auto-test-state` pinned bead with **single-writer
   metadata only** (`schema_version`, `state`, `current_cycle`,
   `last_cycle_at`, `last_cycle_outcome`, `paused_until`,
   `incidents[]≤20`). **No `transition_log[]` or `rejection_log[]`
   on either bead** — those move to attachment beads per the OQ4
   fallback (this section, Phase 0a-3 outcome). Mayor-owned.
   **Round 3 fix #10 — acceptance:** post-task,
   `gt auto-test-pr status --format=json` returns
   `{enabled_rigs:[], paused:false, circuit_breaker:{count:0}}`
   (the town-wide row of the status table). **OQ4 fallback
   acceptance:** unit tests cover (a) materializer over zero
   attachment beads returns empty `transitions[]` /
   `rejections[]`; (b) materializer over a single transition
   attachment returns the same record shape the previous in-blob
   `transition_log[]` returned (so callers don't branch on storage
   form); (c) cycle-close handler `bd create` round-trips: file a
   transition attachment, materialize, see it in `transitions[]`;
   (d) the parent state bead's `Issue.Metadata` post-cycle does
   NOT contain `transition_log[]` or `rejection_log[]` keys (guard
   against accidental regression to the RMW pattern).
9. **Land `mol-auto-test-pr-branch-gc` patrol** (PRD promoted-MUST
   fix). Standing patrol with **two responsibilities** under the OQ4
   fallback:
   (a) **Branch GC** — list `refs/heads/auto-test/*/*` branches
   across all opted-in rigs, cross-reference against each rig's
   state bead and any open MRs, and delete branches >7 days old
   with no associated open MR or in-flight bead.
   (b) **Attachment-bead retention** — list attachment beads via
   the `gt:auto-test-pr-attachment` label query and CLOSE (do NOT
   delete; beads are append-only audit) attachments outside their
   retention window: `kind:transition` attachments at `at + 60d <
   now`; `kind:rejection` attachments at `cooldown_until + 30d <
   now`. Closure trims them from the materializer's default
   `status=open` view; closed attachments remain readable for audit.
   **Acceptance:** integration test seeds 3 transition attachments
   (one fresh, one 30d old, one 90d old) and 3 rejection
   attachments (cooldowns at +21d / -10d / -45d relative to now),
   runs the patrol, and verifies (i) the 90d transition is
   `status=closed`, (ii) the rejection with `cooldown_until = -45d
   ago` is `status=closed`, (iii) the others remain `status=open`.
10. **Wire D15 maintainer-approval gate into Refinery's merge
    handler.** **Verification of label-query + `approved-by:<user>`
    semantics moved to Phase 0a-1** (round 2 fix). Wire the
    merge-gate: Refinery refuses to merge an MR bead with label
    `gt:auto-test-pr` unless an `approved-by:<user>` label is also
    present, when the source rig has
    `auto_test_pr.require_review_approval=true` (default-true).
    Backwards-compatible: MR beads without the auto-test label
    behave unchanged.
11. **Wire D16 SEV-1 auto-revert into Mayor's main-CI-break
    subscription.** **Verification of Mayor's main-CI-break event
    subscription moved to Phase 0a-2** (round 2 fix). Wire: on a
    main-CI-break whose attributing commit's MR-bead carries
    `gt:auto-test-pr`: file revert MR + transition rig state bead
    to `paused-by-circuit-breaker` (7d cooldown) + increment town
    circuit-breaker counter + nudge Overseer with SEV-1 payload.
12. Document Overseer SEV-1 response runbook at
    `.gt/auto-test-pr/sev1-runbook.md` (in the `gt` repo, not the
    pilot rig). Steps: (1) confirm the auto-filed revert MR landed
    and main is green; (2) verify the rig's state bead is
    `paused-by-circuit-breaker` with a 7d cooldown; (3) decide
    whether to file an investigation bead for the test that broke
    main; (4) decide whether to override the circuit breaker via
    `gt auto-test-pr resume --rig=<rig> --override-circuit-breaker`
    or to wait out the cooldown; (5) record the decision in the
    rig's state bead's `incidents[]` log via `bd update <rig>-auto-
    test-state --append-metadata 'incidents=[{ts:..., actor:...,
    decision:...}]'` (consistent with the `bd update --add-label
    approved-by:<user>` write pattern from D15 / task 10; the
    read-only `gt auto-test-pr show --rig=<rig> --raw` verb is for
    *reading* the resulting log entry, not writing it). **Round 3
    fix #3:** `incidents[]` field is added to the per-rig state bead
    schema in §Data Model lifecycle table.
13. Configure branch-protection rule on `gastown_upstream`'s origin
    for `refs/heads/auto-test/*/*` — only the cycle-agent / Refinery
    service identity may push (R11 / C-SEC-6 implementation).
    Verified via attempting a push from a non-service identity (must
    fail). For multi-rig v2, this rule is captured in the per-rig
    opt-in template so new rigs inherit it on enable. ("Cycle-agent"
    here is the polecat / Mayor identity that pushes
    `auto-test/<rig>/<bead-id>` branches at `gt done` time, as
    documented in the rig's identity manifest.)
13a. **Round 3 fix #2 — Phase-0 e2e fixture integration test.** Build
    a fixture rig under `internal/autotest/testdata/fixturerig/`
    containing 1 churned Go file with 2 uncovered branches and a
    `.gt/auto-test-pr/conventions.md`. Drive a single end-to-end
    cycle in-process: stub Mayor's tick fires → cycle reads fixture's
    state bead (`idle`) → dispatches in-process polecat → polecat
    writes a new `*_test.go` file → all 7 gates (4a-g) run through
    the real sandbox library (per the 5a ADR substrate) → mock
    Refinery merge handler observes the in-memory MR-bead (with
    `gt:auto-test-pr` and `rig:<target_rig>` labels per round 3
    fix #6) → 3c cycle-close handler transitions state bead
    `mr-pending → cooled-down (merged)`. **Acceptance:** state bead
    ends in `cooled-down (merged)`; the new test file has the D8
    provenance marker; all 7 gates emit pass records on the
    transitions log; the cycle's wall-clock <30 min on the fixture.
    Re-run the same fixture with one gate forced to fail (e.g.,
    gate 4d sub-rule (ii) literal-vs-literal fails) and verify the
    polecat exits with NOTES and no MR-bead is created. This is the
    cheapest way to find wiring bugs *before* the pilot burns weeks
    of observation wall-clock. Depends on tasks 3a/b/c + 5a/b/c +
    6a/b/c + 10 + 11 — runs after sandbox + gates + merge-gate +
    SEV-1 wires are in place. Critical path's final task.

#### Phase 0 dependency graph

```
Independent / parallelizable (batch A — start immediately):
  1   settings-JSON loader
  2a  enable/disable CLI
  2b  pause/resume/status/show/history CLI
  2c  revise CLI
  2d  conventions-sheet template + emit-template/show-template verbs
  4   mol-auto-test-pr-cycle (inert formula)
  7   sling priority floor
  8   town-state pinned bead provisioning
  9   mol-auto-test-pr-branch-gc patrol
  12  SEV-1 runbook (doc-only)
  13  branch-protection rule

Serial chain (critical path):
  5a  sandbox: cred-strip + CWD-pin
   ↓
  5b  sandbox: network-drop + module-cache warm-up
   ↓
  5c  sandbox: wall-clock cap + integration test
   ↓
  6a  coverage-delta branch-mode parser  ┐
  6b  AST-aware mutant runner            ├─ parallel after 5c
  6c  tautology linter (4 sub-rules)     ┘
   ↓
  3a  formula mode=create                ┐
  3b  formula mode=revise + D19 reply    ├─ parallel after 6a/b/c
  3c  Mayor cycle-close handler          ┘
   ↓
  10  Refinery approval gate (wire-only; verify in Phase 0a-1)  ┐
  11  Mayor SEV-1 auto-revert (wire-only; verify in Phase 0a-2) ┘ — parallel after 3a/b/c
   ↓
  13a Phase-0 e2e fixture integration test (round 3 fix #2)
      — final critical-path task; depends on 3a/b/c + 5a/b/c +
        6a/b/c + 10 + 11

Critical-path length (with parallelism):
  Phase 0a (5 prereq spikes, parallel) → 5a → 5b → 5c
  → {6a,6b,6c parallel} → {3a,3b,3c parallel} → {10, 11 parallel}
  → 13a (e2e integration)
  ≈ 9 task-times serialized (1 for 0a + 8 for 0; round 3 fix #11
  count update). Phase 0 task count = 23 (was 22 pre-fix-2: tasks
  1, 2a-d, 3a-c, 4, 5a-c, 6a-c, 7, 8, 9, 10, 11, 12, 13, 13a);
  Phase 0a = 5; total Phase 0 + 0a = 28. Mayor SHOULD dispatch
  Phase 0a's 5 spikes in parallel (each is hours, not days) and
  ~6 polecats in parallel for Phase 0 batch A and the parallel
  groups; expected wall-clock reduction ≈ 3-4×.
```

**Phase 0 exit criteria:**

- All formulas parse; all gates have unit tests (including the four
  gate-4d sub-rules and gate-4a branch-mode parser).
- CLI verbs round-trip through Mayor without dispatching work.
- Sandbox wrapper works on a hand-rolled fixture (5c integration
  test green).
- Branch-GC patrol deletes a fixture stale branch in dry-run.
- Refinery approval gate unit-tests cover both labeled-and-approved
  (merges) and labeled-and-unapproved (refuses) cases.
- SEV-1 path unit-tests cover both labeled break (auto-reverts) and
  unlabeled break (no action).
- **Mayor cycle-close handler** unit tests cover four paths: merged
  → cooled-down, closed-unmerged → cooled-down + rejection-log
  append, 3-closes-in-7d → `paused-by-circuit-breaker`, and
  `BUG-DISCOVERED:` NOTES → P2 bug bead filed. **Round 3 fix #6:**
  unit tests also verify the `rig:<target_rig>`-label-based lookup
  resolves to the correct per-rig state bead on a fixture MR-bead
  with `rig:gastown_upstream`.
- **`mode=revise` polecat formula** unit tests cover both
  `--comment-id`-targeted reply and most-recent-thread fallback.
- All new Go packages pass `go vet ./...`, `go build ./...`,
  `go test ./...`, and `scripts/check-upstream-rebased.sh` (the
  rig's standard refinery gates).
- **`mol-auto-test-pr-cycle` integration test** (round 2 fix #10)
  covers both the missing-town-bead path (cycle exits with a
  structured warning, not a panic — protects against partial Phase
  0 revert) AND the no-rigs-enabled path (cycle exits 0).
- **5b network-drop acceptance** (round 2 fix #7): a fixture
  package with `go mod download && drop-net && go test -count=10
  ./...` passes 10/10 with no fresh fetch (verified by `tcpdump`
  / `strace -e connect`).
- **Phase-0 e2e fixture integration test** (round 3 fix #2 — task
  13a) green: the happy path drives state bead from `idle` →
  `cooled-down (merged)` with the D8 provenance marker, all 7 gates
  passing, and wall-clock <30 min on the fixture; the gate-fail
  variant exits with NOTES and creates no MR-bead.
- **Phase 0 task 1 acceptance** (round 3 fix #7): settings-JSON
  loader unit tests cover absent block, well-formed block, malformed
  JSON, and unknown-language inputs.
- **Phase 0 task 2d acceptance** (round 3 fix #8): conventions-
  template golden-file snapshot test green.
- **Phase 0 task 7 acceptance** (round 3 fix #9): sling priority-
  floor integration test confirms user beads dispatched ahead of
  auto-test beads regardless of submission order.
- **Phase 0 task 8 acceptance** (round 3 fix #10): town-state
  bead returns the documented empty-state JSON via
  `gt auto-test-pr status --format=json`.
- **`enabled_rigs[]` reconcile** (round 3 fix #4): Phase 0 exit
  test confirms a stale `enabled_rigs[]` (rig present in cache but
  `auto_test_pr.enabled=false` in settings JSON, or vice versa) is
  reconciled by `mol-auto-test-pr-cycle`'s tick within one tick.
- **OQ4 fallback acceptance** (gu-2s03 — this section §"OQ4
  fallback"): the `mol-auto-test-pr-cycle` cycle-close handler files
  attachment beads (NOT RMW into the parent state bead's
  `Issue.Metadata`) for both transitions and rejections; unit tests
  cover the materializer over zero / one / many attachments and the
  empty / well-formed / missing-rig-label edge cases; the parent
  state bead's `Issue.Metadata` post-cycle does NOT contain
  `transition_log[]` or `rejection_log[]` keys (regression guard
  against accidental return to the RMW pattern). The 100-concurrent-
  writer harness in `internal/cmd/metadata_attachment_bead_integration_test.go`
  (gated by `GT_RUN_OQ4_SPIKE=1`) is run once at Phase 0 close to
  re-validate the fallback against any beads-SDK bump that lands
  between Phase 0a-3 and Phase 0 close.
- **Branch-GC patrol attachment retention** (task 9 fix per OQ4
  fallback): integration test seeds 3 transition + 3 rejection
  attachments at varying ages and verifies the patrol closes only
  the out-of-window attachments, leaves recent ones `status=open`,
  and never deletes any (audit trail preservation).

### Phase 1: Pilot opt-in (`gastown_upstream` only)

Goal: produce 2+ consecutive merged auto-test MRs without
intervention, no SEV-1/SEV-2 incidents.

**Phase 1 entry precondition:** Phase 1 may not begin until **Phase 0
tasks 10 (approval gate) AND 11 (SEV-1 auto-revert) integration
tests pass.** This is the minimum-viable safety net before flipping
`enabled=true`. Phase 0 other tasks (e.g., branch-GC, runbook,
branch-protection) are desirable but non-blocking for Phase 1
entry — they SHOULD ship before Phase 1 but don't gate it.

14. Author and commit `.gt/auto-test-pr/conventions.md` and
    `.gt/auto-test-pr/mr-template.md` to `gastown_upstream`. Run
    `gt auto-test-pr enable --emit-template`, customize for
    `gastown_upstream`, commit via PR. Reviewed via standard PR
    review.
15. Provision `<rig>-auto-test-state` pinned bead for
    `gastown_upstream`. Initial `Issue.Metadata`:
    `{schema_version:1, rig:"gastown_upstream", state:"idle",
    current_cycle:null, last_cycle_at:null, last_cycle_outcome:null,
    paused_until:null, incidents:[]}` — **single-writer fields
    only** per the OQ4 fallback. The pinned bead does NOT carry a
    `transition_log[]` or `rejection_log[]`; reads of those go
    through the materializer over attachment beads filtered by
    `rig:gastown_upstream` + `kind:{transition,rejection}` labels
    (see Phase 0 task 8 + §Data Model "OQ4 fallback"). The town
    bead's `enabled_rigs[]` is updated atomically with the rig's
    settings-JSON `enabled=true` flip in Phase 1 task 16 per Round
    3 fix #4 (CAS-append; reconcile in `mol-auto-test-pr-cycle`
    handles partial-failure cases). **OQ4 fallback acceptance:**
    materializer query against the freshly-provisioned bead returns
    empty `transitions[]` / `rejections[]`; the first cycle's
    cycle-close handler files a transition attachment which then
    surfaces in the materialized list (proves the read-path is
    wired before the pilot opt-in flip).
16. Flip `auto_test_pr.enabled=true` in `gastown_upstream`'s settings
    JSON. Cadence: 7 days.
17. **Five-week (weeks 2-6) observation window.** Each cycle:
    - Watched live by an on-call human; first 5 MRs reviewed in real
      time.
    - Wall-clock, gate pass/fail, and reject reasons logged to
      Overseer's channel.
18. **Manual revision pathway during Phase 1.** Until Phase 2 lands
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

**Phase 1 exit criteria** (PRD pilot-success-criteria fix —
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

19. Extend `mol-pr-feedback-patrol`'s `dispatch-work` step with
    label-keyed dispatch (D3). Label `gt:auto-test-pr` →
    `mol-polecat-work-test-improver` formula in `mode=revise`.
    Default-other-labels keep current behavior. Behind feature flag
    `feature_flags.auto_test_pr_revision_routing=false` until tested.
20. Ship reviewer magic phrase parsing (D9) in
    `mol-pr-feedback-patrol`. Token: `gt auto-test-pr: pause-rig-7d`.
    Patrol writes the pause to the rig's state bead.
21. Integration tests: fixture MR with label → revision dispatched;
    fixture MR without label → generic dispatch (regression).
22. Flip `feature_flags.auto_test_pr_revision_routing=true` on
    `gastown_upstream` only.
23. Watch for one full revision cycle (reviewer comment → polecat
    revision → re-review → merge). Verify state bead transitions
    `mr-pending → mr-revising → mr-pending → cooled-down`.

**Phase 2 exit criteria:** One end-to-end revision cycle completes
without human intervention.

### Phase 3 (deferred): Generalization

**v2 / v3 follow-on work, captured here for design continuity only —
not committed in v1.** Round 2 fix #6 demoted the previous
numbered tasks (24-27) to narrative bullets because numbered tasks
read as commitments, and the v1 PRD explicitly defers everything in
this section. The v1 implementation-plan task ledger ends at task
23 (Phase 2's last integration step).

The v1 design is forward-compatible with these follow-ons; do not
build them in v1, but do not paint v1 into a corner that precludes
them either:

- **Second-rig opt-in.** Adding a second rig (e.g., a TypeScript
  rig) requires extending the language allow-list (CR-gated,
  Overseer sign-off per Q4) and the per-rig opt-in template
  (branch-protection rule, conventions sheet, settings-JSON
  block). Not committed in v1.
- **External-PR mode.** A separate v2 PRD covers `gh pr create`
  mode (for rigs not on Refinery) and GitHub App identity. v1's
  D2b explicitly removes external-PR mode from scope; v2 must
  add a per-rig `auto_test_pr.merge_mode ∈ {refinery,
  external-pr}` config key and a detection step at cycle entry.
- **State-bead schema migration.** v2 will add new states for
  `pr-pending` / `pr-revising` (external-PR mode). The migration
  is additive — v1 readers tolerate v2 blobs via the
  `schema_version` field.
- **Tap-guard amendment.** `internal/cmd/tap_guard.go` blocks
  ad-hoc `gh pr create` invocations today. When external-PR
  mode lands in v2, the guard must learn to allow the auto-test-
  pr polecat. v1 does not modify the guard (per OQ5).

### Reverting

Each phase reverts independently:

- **Phase 2 revert:** flip `feature_flags.auto_test_pr_revision_routing
  =false`. Patrol stops routing revisions; in-flight MRs require
  manual revision dispatch (Phase 1 task 18 CLI).
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
| Phase 0 tasks 2/5/6/3 too coarse-grained (each fuses multi-day work) | plan-self-review round 1 (completeness) flagged that bundled tasks obscure dependencies and parallelism | Split task 2 → 2a-d; task 3 → 3a-c; task 5 → 5a-c; task 6 → 6a-c. Phase 0 dependency graph documents critical path | Phase 0 task list + dep graph (plan-self-review round 1) |
| Mayor cycle-close handler implied throughout but never tasked | plan-self-review round 1 (completeness) flagged that D2a, Q6, S1, bug-discovery NOTES all depend on a handler with no Phase 0 task | Phase 0 task 3c implements handler with all four paths (merged, closed-unmerged, 3-closes-trips-CB, BUG-DISCOVERED parsing); exit criteria covers all four | Phase 0 task 3c (plan-self-review round 1) |
| Phase 0 / Phase 1 task numbers collide (both have 9, 10, 11) | plan-self-review round 1 (sequencing) flagged that cross-references are ambiguous | Phase 1 renumbered to start at 14, Phase 2 at 19, Phase 3 at 24; 12a promoted to peer task 18 | Phase 1/2/3 numbering (plan-self-review round 1) |
| Conventions-sheet template not shipped with `gt`; every rig re-derives constraints | plan-self-review round 1 (completeness) flagged drift risk and brittle "refuse to run without conventions" check | Phase 0 task 2d ships `internal/autotestpr/conventions_template.md` + `gt auto-test-pr enable --emit-template` and `gt auto-test-pr show-template` verbs | Phase 0 task 2d (plan-self-review round 1) |
| `--override-circuit-breaker` flag named in D16 but missing from CLI task | plan-self-review round 1 (completeness) flagged that SEV-1 has no manual recovery path | Task 2b explicitly lists `resume --rig=<rig> [--override-circuit-breaker]` with audit-log entry | Phase 0 task 2b (plan-self-review round 1) |
| Refinery label-query and `approved-by:<user>` semantics asserted but unverified | plan-self-review round 1 (completeness) flagged that D15 assumes pre-existing infra | Task 10 split into (a) verify + (b) wire; same pattern applied to task 11 (Mayor main-CI-break subscription) | Phase 0 tasks 10 + 11 (plan-self-review round 1) |
| Phase 1 entry has no documented precondition on Phase 0 safety net | plan-self-review round 1 (sequencing) flagged that partial Phase 0 rollout could ship cycle without merge gate | Phase 1 entry precondition: tasks 10 + 11 integration tests must pass before flipping `enabled=true` | Phase 1 entry precondition (plan-self-review round 1) |
| `mode=revise` formula support implicit but never tasked | plan-self-review round 1 (completeness) flagged that Phase 1 step 18 + Phase 2 step 19 silently depend on it | Phase 0 task 3b explicitly tasks `mode=revise` path with D19 reply step + tests for both `--comment-id` and most-recent-thread fallback | Phase 0 task 3b (plan-self-review round 1) |
| `paused-by-circuit-breaker` state referenced eight times in the body but missing from the §Data Model state-machine diagram | plan-self-review round 2 (risk) flagged documentation correctness — reader of diagram alone wouldn't know the state exists | State-machine diagram extended (§Data Model) with the seventh state, its inbound trigger (D16 ci-break / Q6 3-closes), and its only outbound edge (operator `gt auto-test-pr resume --override-circuit-breaker`); annotated transition-trigger table added | §Data Model state machine (plan-self-review round 2) |
| Phase 0 task 10/11 verify-step buried mid-Phase-0; Refinery / Mayor infra unknowns could waste ~2 weeks of substrate work | plan-self-review round 2 (risk) flagged that round-1's "verify + wire" split was structurally right but scheduled wrong | New **Phase 0a** prerequisite-verification phase added (tasks 0a-1 through 0a-5); Phase 0 tasks 10 + 11 reduced to wire-only; Phase 0 cannot start until 0a is complete | Phase 0a + tasks 10/11 (plan-self-review round 2) |
| Mutant runner mutation-selection algorithm unspecified; three implementers would ship three runners with different gate-4b false-positive rates | plan-self-review round 2 (risk) flagged D11's count cap without a selection rule | Task 6b extended with explicit mutation grammar: (i) comment-out-line, (ii) negate-boolean, (iii) return-zero-value; deterministic selection seeded by file SHA + test name; ≤5-mutant cap enforced over the union with blast-radius-ranked tiebreak | Phase 0 task 6b (plan-self-review round 2) |
| Tautology sub-rule (i) requires non-trivial Go AST data-flow analysis; if naively implemented, false-positive/negative rates could exceed 30% | plan-self-review round 2 (risk) flagged that gate 4d's main protection sub-rule needs feasibility validation | Phase 0a-5 spike with 50-test corpus; ≥85% precision / ≥75% recall acceptance gate; if threshold not met, sub-rule (i) omitted from gate 4d (other three sub-rules unconditional) | Phase 0a-5 + task 6c (plan-self-review round 2) |
| `gt sandbox or equivalent` phrasing leaves implementation strategy uncommitted; tasks 5a/5b/5c could target three different substrates | plan-self-review round 2 (risk) flagged composability risk for the integration test | ADR sub-step `5a-pre` decides wrapper-vs-library-vs-inline (recommended: library at `internal/autotest/sandbox`); committed before 5a/5b/5c implementation | Phase 0 task 5a (plan-self-review round 2) |
| Phase 3 numbered tasks 24-27 read as commitments despite "out of scope" header | plan-self-review round 2 (scope-creep) flagged that numbering blurs the v1 contract | Phase 3 rewritten as narrative bullets with no task numbers; v1 task ledger ends at task 23 | Phase 3 narrative (plan-self-review round 2) |
| OQ7 TALON-style-comment-exception language in v1 conventions template is gold-plating for hypothetical future v2 rig | plan-self-review round 2 (scope-creep) flagged that pilot rig is not TALON-convention | OQ7 language removed from v1 template; OQ7 entry annotated with `v2 follow-up` note for first TALON-rig opt-in | Phase 0 task 2d + OQ7 (plan-self-review round 2) |
| 5b acceptance criterion implicit; could ship without verifying `go test -count=10` post-warm-up network drop | plan-self-review round 2 (risk) flagged subsumption of security-leg open question 1 | Task 5b body + Phase 0 exit criteria add 10/10-rerun acceptance with `tcpdump`/`strace` verification; warm-up amended on FAIL | Phase 0 task 5b + exit criteria (plan-self-review round 2) |
| Pinned-bead `Issue.Metadata` reliability open (OQ4) but Phase 0 tasks 8 + 14 depend on it for ~5KB JSON round-trips | plan-self-review round 2 (risk) flagged that fallback (metadata-attachment-bead) is materially different work | OQ4 promoted to Phase 0a-3 spike (100 round-trips, byte-for-byte verification, CAS isolation stress); FAIL files prerequisite bead and re-shapes tasks 8 + 14 | Phase 0a-3 (plan-self-review round 2) |
| Tasks 6b / 6c require Go AST expertise not named in the plan; AST footguns could cause silent gate false-negatives | plan-self-review round 2 (risk) flagged knowledge gap on AST work | Knowledge-prep sub-step on each task: assigned polecat MUST read `golang.org/x/tools/go/ast/astutil` and one real-world AST tool before implementation; MUST use `go/parser` + `go/ast` directly | Phase 0 task 6b + 6c (plan-self-review round 2) |
| `mol-auto-test-pr-cycle` would panic on partial Phase 0 revert (town bead absent); patrol blocks every other patrol | plan-self-review round 2 (risk) flagged rollback safety | Missing-town-bead integration test added to Phase 0 exit criteria; cycle exits with structured warning, not panic | Phase 0 exit criteria (plan-self-review round 2) |

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

- `.plan-reviews/auto-test-pr/review-round-1.md` — plan self-review
  round 1 (completeness + sequencing); applied 8 must-fix and 4
  should-fix items to this synthesis (Phase 0 task splits 2a-d /
  3a-c / 5a-c / 6a-c, new Phase 0 tasks 3c [Mayor cycle-close
  handler] + 12 [SEV-1 runbook] + 13 [branch-protection], renumber
  Phase 1/2/3 to start at 14/19/24, Phase 1 entry precondition,
  Phase 0 dependency graph documenting critical path, gate-test
  exit-criteria additions, `--override-circuit-breaker` flag
  surfacing, Refinery + Mayor pre-existing-infra verification
  sub-steps in tasks 10 + 11)

- `.plan-reviews/auto-test-pr/review-round-2.md` — plan self-review
  round 2 (risk + scope-creep); applied 6 must-fix and 5 should-fix
  items to this synthesis (Phase 0a prerequisite phase with tasks
  0a-1 through 0a-5; state-machine diagram extension covering
  `paused-by-circuit-breaker`; mutant grammar in task 6b; tautology
  sub-rule (i) spike-gating; sandbox ADR sub-step in 5a; Phase 3
  rewritten as narrative; OQ7 removed from v1 template;
  R25/R26/R27/R28 added to risk register; missing-town-bead
  integration test in Phase 0 exit criteria)

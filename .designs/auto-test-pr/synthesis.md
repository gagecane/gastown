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
| 4a | coverage-delta (computed from `go tool cover` parsed via `golang.org/x/tools/cover`) | hard fail if delta ≤ 0 |
| 4b | synthetic-mutant sanity (≤5 mutants per test, AST-aware, runs in `os.MkdirTemp` outside worktree) | hard fail if any new test still passes when its target line is commented out |
| 4c | flakiness rerun (`go test -count=10 -run="<exact-test-names>" ./<direct-package>` only) | hard fail if any flake |
| 4d | tautology linter (heuristic: rejects `assert(true)`, literal-equality, missing-assertion tests) | hard fail |
| 4e | pre-push gitleaks scan (`gitleaks detect --no-banner --redact`) | hard fail; SEV-2 per Q6 |
| 4f | output allow-list verifier — every changed file in the diff matches `**/*_test.go` | hard fail |

Each gate runs through a **hardened sandbox wrapper** that strips
credential env vars (`AWS_*`, `GITHUB_TOKEN`, `BD_*`, `DOLT_*`,
`GIT_AUTHOR_*`, `GIT_COMMITTER_*`), drops network egress *after*
module-cache warm-up, pins CWD to the worktree, and caps wall-clock
per-target at 5 min (cycle-wide cap 30 min — see Decisions).

The molecule honors the existing `--pre-verified` rebase step from
`mol-polecat-work` so the Refinery can fast-path the merge.

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
  without it (per ux/integration leg's hard fail).
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
idle → picking → dispatched → mr-pending → cooled-down
                                    ↓ ↑
                                mr-revising
```
Transitions are append-only on the `transitions[]` array on the per-
rig bead. CAS uses Dolt SERIALIZABLE-class isolation on the bead's
single row.

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

## Implementation Plan

Three phases. Each ships independently; each reverts independently
by reverting one PR.

### Phase 0: Substrate prep (no behavior change, no opt-in)

Goal: ship all the wiring inert, so Phase 1 is a single-flag flip.

1. Add `auto_test_pr.*` keys to per-rig settings JSON loader. Default
   absent → disabled. **OQ1 must be answered first.**
2. Ship `gt auto-test-pr {enable,disable,pause,resume,status,show,
   history}` CLI commands. `status` reports "no rigs opted in"
   when the town bead has zero entries. `pause --all` and `resume
   --all` write to the town bead but no patrol consumes them yet.
3. Land `mol-polecat-work-test-improver` formula extending
   `mol-polecat-work` with the five quality-gate steps and the
   sandbox wrapper. **No molecule registers it yet.**
4. Land `mol-auto-test-pr-cycle` formula. Registered in Mayor's
   patrol set, but the first step is `if no rig has
   auto_test_pr.enabled == true → exit 0`. Inert.
5. Implement the sandbox wrapper (`gt sandbox` helper or equivalent)
   — credential strip + network drop + CWD pin + wall-clock cap.
6. Land coverage-delta parser (`internal/autotest/coverage.go`),
   AST-aware mutant runner (`internal/autotest/mutant.go`), tautology
   linter (`internal/autotest/tautology.go`), with full unit tests.
7. Ship sling priority-floor mechanism if not present (D13).
8. Provision `town-auto-test-pr-state` pinned bead with `enabled_rigs:
   []`. Mayor-owned.

**Phase 0 exit criteria:** All formulas parse; all gates have unit
tests; CLI verbs round-trip through Mayor without dispatching work;
sandbox wrapper works on a hand-rolled fixture.

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
12. **Two-week observation window.** Each cycle:
    - Watched live by an on-call human; first 5 MRs reviewed in real
      time.
    - Wall-clock, gate pass/fail, and reject reasons logged to
      Overseer's channel.
13. **Phase 1 exit criteria:** ≥2 consecutive merged MRs with no
    operator intervention (no manual revisions, no manual gate
    overrides); zero SEV-1/SEV-2; rejection rate <40% over the
    observation window.

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

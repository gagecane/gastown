# API & Interface Design

> Convoy leg: **api** for the `auto-test-pr` design.
> Source PRD: `.prd-reviews/auto-test-pr/prd-draft.md` (commit `13d14a44`).
> Source review: `.prd-reviews/rqoca/prd-review.md` (Q1–Q7 decisions).

## Summary

Auto-test-PR is a Mayor-driven cycle (and per-rig opt-in) that opens
small, reviewable test-improvement MRs through the existing Refinery
merge queue on the `gastown_upstream` Go pilot. Because v1 is
Refinery-only and Go-only, the user-facing surface is small: a
**rig-config opt-in toggle**, an **admin CLI under `gt auto-test-pr …`**
(pause / status), a **dispatch-bead contract** consumed by a new
polecat-work formula variant, and a **PR/MR body + code-marker** so the
output is unambiguously bot-authored. There is **no `gt auto-test-pr
run`** in v1 — the cycle is daemon-driven; the CLI exists for
operators, not for triggering work ad hoc.

The design biases toward **borrowing existing patterns** so the feature
is discoverable: rig config lives where other rig config lives, the
admin verb shape mirrors `gt patrol` and `gt mol`, the formula slot
lives next to `mol-polecat-work` and `mol-pr-feedback-patrol`, and the
dispatch-bead JSON envelope reuses the same shape `gu-wisp-*`
sling-context beads already use today (e.g., `gu-wisp-1bz`).

## Analysis

### Key Considerations

- **Two distinct user populations.** Rig owners (rare touches; opt-in
  + occasional triage) and operators / on-call (kill-switch + status
  during incidents). The interfaces should optimize for each: config
  for the first, admin verbs for the second.
- **No "user runs the cycle" path in v1.** Per Q1, v1 is Mayor-daemon
  only on the pilot rig. Adding a user-facing `gt auto-test-pr run`
  would invite scope creep (auth, args, conflicts with cooldown) for
  zero v1 benefit. Design accordingly: omit it.
- **Discoverability is mostly social.** This is a feature most rig
  owners encounter via *seeing an MR appear*, not via reading docs.
  The PR/MR body and code marker therefore double as documentation:
  what created this, why, how to opt out / pause / report a bad PR.
- **Naming consistency over cleverness.** Gas Town verbs follow
  `gt <noun> <verb>` (e.g., `gt patrol digest`, `gt mail send`,
  `gt mol attach-from-mail`). New surface should match.
- **"Refinery-only" collapses lots of surface.** No GitHub App, no
  PAT, no `gh pr create`, no DCO, no external-repo allowlist. The
  external-PR mode is real future scope (filed as v2) but the v1
  spec must NOT pretend it exists in CLI/help text — that's how
  half-built abstractions ossify.
- **gu-gal8 invariant must show up in the API.** No CLI verb may
  cause a polecat to file a bookkeeping bead. State changes are
  Mayor-owned. The CLI is a thin client over that state.
- **Allow-list, not arbitrary commands.** Per Q4: rig config exposes
  `language=go` as the only knob in v1; no `test_cmd`,
  `coverage_cmd`, `lint_cmd`. The CLI must reflect that and reject
  unknown languages explicitly with a help message that names the
  v2 escape hatch (town-level approval bead).

### Options Explored

#### Option 1: Stand-alone `gt auto-test-pr` command tree

- **Description**: A new top-level subcommand: `gt auto-test-pr
  pause`, `gt auto-test-pr resume`, `gt auto-test-pr status`,
  `gt auto-test-pr enable --rig=…`, `gt auto-test-pr disable
  --rig=…`. Implemented as a sibling to `gt patrol` in
  `internal/cmd/auto_test_pr.go`.
- **Pros**:
  - Discoverable: `gt --help` lists it; `gt auto-test-pr --help`
    is a self-documenting surface. Operators can find the
    kill-switch under fire without reading the design doc.
  - Consistent with the rest of Gas Town's verb shape.
  - Easy to gate per-rig vs town-wide via `--all` / `--rig=` flags.
  - Mirrors the PRD's Q6 deliverables verbatim, removing
    interpretation risk for the implementer.
- **Cons**:
  - "Yet another top-level command" if more auto-* features land —
    risk of `gt auto-test-pr`, `gt auto-fix-flaky`, `gt auto-…`
    sprawl. Mitigated by namespacing under `gt auto …` (see
    Option 2).
  - Slightly more code than embedding the verbs in `gt patrol`.
- **Effort**: Low. The five verbs above are all thin wrappers around
  Mayor-side state changes (writes to the per-rig pinned
  `<rig>-auto-test-state` bead).

#### Option 2: Namespaced under `gt auto <feature>`

- **Description**: Reserve `gt auto` as the parent namespace. v1
  ships `gt auto test-pr pause | status | …`. Future auto-features
  slot under the same parent.
- **Pros**: Forward-compatible with future bot-driven features
  (auto-flake-fix, auto-changelog, etc). Clean grouping in `--help`.
- **Cons**:
  - Today there are zero other `auto` subcommands. Premature
    abstraction; the empty namespace adds a directory of
    indirection without benefit.
  - The PRD names the feature `auto-test-pr` consistently;
    splitting "auto" + "test-pr" creates a doc/CLI mismatch.
  - The feature's user-visible verb is `auto-test-pr`, not `auto`;
    dropping the hyphen in CLI but not in docs is a small but
    real ergonomics tax.
- **Effort**: Low (same code, just nested differently).

#### Option 3: Fold into `gt patrol`

- **Description**: Make auto-test-pr a patrol; expose `gt patrol
  pause auto-test-pr --rig=…`, `gt patrol status`.
- **Pros**:
  - Reuses an existing surface area — operators already know
    `gt patrol`. No new top-level verb.
  - Architecturally honest: the cycle IS a patrol (Mayor-daemon,
    cron-shaped, per-rig).
- **Cons**:
  - `gt patrol` today is *digest-management*-shaped (`digest`,
    `report`, `scan`, `new`), not control-shaped. Repurposing
    forces ambiguity: does `gt patrol pause` pause the whole
    daemon or one patrol?
  - The PRD's Q6 spec uses `gt auto-test-pr pause` literally, so
    reshaping to fit `gt patrol` requires *another* PRD edit —
    unwarranted churn.
  - Would have to retrofit a generic "named patrol" identifier and
    pause-verb on `gt patrol` first; that's a separate refactor
    that should not block v1.
- **Effort**: Medium-High (it pulls in a refactor of `gt patrol`
  semantics). Not worth it for a single new patrol.

#### Option 4: No CLI at all in v1; admin via `bd update`

- **Description**: Operators flip the per-rig pinned state bead
  directly via `bd update <rig>-auto-test-state --notes=…` to
  pause; status is read with `bd show`.
- **Pros**: Zero new code in `gt`; the pinned-state bead is the
  source of truth anyway, so the CLI is technically redundant.
- **Cons**:
  - Operationally fragile under fire. "Update the notes field of
    the pinned state bead with a JSON blob containing
    `paused_until`" is exactly the wrong UX during a SEV.
  - `gt auto-test-pr pause` is a v1 MUST per Q6; cutting it
    contradicts the PRD.
  - No discoverable "is this rig enabled?" answer for owners.
- **Effort**: Zero — but rejected on incident-response grounds.

#### Option 5: Per-rig Config — separate file vs. existing rig manifest

The PRD's OQ13 asks where rig config lives. Two sub-options:

- **5a (recommended): Add a section to existing rig config.** Today,
  rigs have a `config.json` (e.g., gastown's own
  `polecats/chrome/gastown_upstream/config.json`). Auto-test-pr adds
  one stanza:

  ```json
  {
    "auto_test_pr": {
      "enabled": false,
      "language": "go",
      "cadence": "weekly",
      "max_files": 3,
      "max_loc": 200
    }
  }
  ```

  - **Pros**: One config home; rig-owner already knows where it
    is; reads use existing config-loader code.
  - **Cons**: Requires schema versioning of `config.json` (add a
    field to the loader; v0 configs without it must default to
    `enabled=false`).
  - **Effort**: Low.

- **5b: Dedicated `.gt/auto-test-pr/config.toml` per rig.**
  Co-located with the conventions sheet from Q5
  (`.gt/auto-test-pr/conventions.md`).
  - **Pros**: Symmetrical with conventions sheet location;
    self-documenting via path.
  - **Cons**: Splits rig config across two files; a future
    `gt rig config` command has to know about both.
  - **Effort**: Low.

  Recommendation: **5a** — keep config consolidated; conventions
  sheet stays at `.gt/auto-test-pr/conventions.md` since *it is
  documentation*, not config.

#### Option 6: Dispatch-Bead Contract Shape

The polecat receives a single bead from Mayor. Per Q5, it must carry
target file(s), coverage profile, conventions-sheet path, PR
template, and (on revision) prior-comment thread + last commit SHA.
Two shapes were considered:

- **6a (recommended): Sling-context JSON envelope, mirroring
  `gu-wisp-*` beads.** The dispatch bead's description is a single
  JSON object — same shape as the existing `gu-wisp-1bz` example
  in this very molecule:

  ```json
  {
    "version": 1,
    "work_bead_id": "<bead-id>",
    "target_rig": "gastown_upstream",
    "formula": "mol-polecat-work-test-improver",
    "args": {
      "mode": "create" | "revise",
      "targets": [
        {
          "path": "internal/cmd/foo.go",
          "uncovered_branches": [
            { "line": 42, "kind": "if-true" },
            { "line": 51, "kind": "switch-case-default" }
          ],
          "coverage_pct_before": 0.62
        }
      ],
      "conventions_sheet_path": ".gt/auto-test-pr/conventions.md",
      "language": "go",
      "size_budget": { "max_files": 3, "max_loc": 200 },
      "pr_template_path": ".gt/auto-test-pr/mr-template.md",
      "revision": null
    },
    "enqueued_at": "2026-05-21T..."
  }
  ```

  On revision, `args.mode == "revise"` and `args.revision` carries
  prior comment thread + last commit SHA + branch name.
  - **Pros**: Reuses an established envelope; the polecat-work
    formula already knows how to read it; no new parser; symmetric
    with the rest of the system.
  - **Cons**: JSON-in-bead-description is awkward to read in `bd
    show` output; mitigated by a `bd dispatch render <id>` helper
    (defer to v2 if needed).
  - **Effort**: Low.

- **6b: Per-rig dispatch beads with structured fields.** Use beads'
  separate `--design`/`--notes`/`--labels` rather than packing into
  description JSON.
  - **Pros**: Slightly nicer in `bd show`.
  - **Cons**: Diverges from existing `gu-wisp-*` convention; tools
    like `gt mol attach-from-mail` and the formula-template renderer
    would need a second code path.
  - **Effort**: Medium. Rejected on consistency grounds.

#### Option 7: Bot Attribution / Code Marker Format

Per Q2 (gate 3) every new test function must carry a leading marker
comment. Two natural options:

- **7a (recommended): Single-line, machine-grep-able marker.**
  ```go
  // gt:auto-test-pr origin=<bead-id> covers=<file:line>[,<file:line>...]
  func TestFoo_emptyInput(t *testing.T) { … }
  ```
  - **Pros**: Greppable; survives merge; one-line; conveys "what
    is this test exercising?" Reviewer can audit by running
    `grep -r "gt:auto-test-pr origin=" internal/`.
  - **Cons**: Tests written under TALON-style "no comments in test
    code" conventions break the rule. Rationalization: this is a
    *provenance marker*, not a behavior comment. Worth calling out
    in the conventions sheet.
- **7b: Block comment with multi-line rationale.**
  ```go
  // gt:auto-test-pr
  //   origin: <bead-id>
  //   covers: <file:line>
  //   rationale: validates handling of empty slice input
  ```
  - **Pros**: Richer context for human reviewer.
  - **Cons**: Verbose; harder to grep; redundant with the test
    name. Rationale belongs in the MR description, not in the
    test file.
  - **Effort**: Same.

  Recommendation: **7a**.

#### Option 8: MR / PR Body Banner

Reviewers must instantly recognize a bot MR. Spec the banner as a
fixed top-of-body block:

```markdown
🤖 **Auto-generated by `gt auto-test-pr`** — adds tests only.

- Origin bead: `<bead-id>`
- Pilot rig: `gastown_upstream`
- Files touched: `<list>` (test files only)
- Quality gates passed: coverage-delta ✓ · synthetic-mutant ✓ ·
  flakiness N=10 ✓ · tautology-linter ✓ · gitleaks ✓

If this PR is wrong:
- Comment `gt auto-test-pr: pause-rig-7d` to pause this rig.
- Or: `gt auto-test-pr pause --rig=<rig> --duration=7d` (operators).
- Or: edit `.gt/auto-test-pr/conventions.md` to teach the bot.

Auto-revision is handled by `mol-pr-feedback-patrol`. Replies to
review comments are pushed as new commits on the same branch.
```

- **Pros**: Tells the reviewer *exactly* what they're looking at,
  what gates ran, and how to stop the bot — all in the place
  they're already looking.
- **Cons**: Banner is editable post-merge; that's why we ALSO have
  the code-level marker (7a). Defense in depth.

### Recommendation

Adopt **Option 1** (stand-alone `gt auto-test-pr` verb tree),
**Option 5a** (config in existing rig manifest), **Option 6a**
(JSON sling-context envelope), **Option 7a** (single-line code
marker), and **Option 8** (banner MR body).

Concretely, the v1 surface is:

#### CLI

```
gt auto-test-pr enable     --rig=<rig>             # turns it on (also rejects if language unsupported)
gt auto-test-pr disable    --rig=<rig>             # turns it off; in-flight MRs are NOT touched
gt auto-test-pr pause      --rig=<rig> --duration=24h
gt auto-test-pr pause      --all       --duration=24h
gt auto-test-pr resume     --rig=<rig>
gt auto-test-pr resume     --all
gt auto-test-pr status     [--rig=<rig>] [--json]   # human + machine output
```

Help text for the family explicitly names Refinery-only / Go-only /
pilot-rig-only; users trying `--rig=<not-pilot>` get a single-line
explanation that points to the v2 follow-up bead.

`gt auto-test-pr status` (no `--rig`) prints a table:

```
RIG                 ENABLED   LANG   STATE        OPEN-MR  PAUSED-UNTIL  LAST-CYCLE   LAST-MERGE
gastown_upstream    yes       go     idle         -        -             2026-05-19    2026-05-18
```

`--json` emits:
```json
{
  "version": 1,
  "rigs": [
    {
      "name": "gastown_upstream",
      "enabled": true,
      "language": "go",
      "state": "idle",
      "open_mr_bead_id": null,
      "paused_until": null,
      "last_cycle_at": "...",
      "last_merge_at": "..."
    }
  ]
}
```

(Useful for the inevitable Mayor dashboard in v2.)

`gt auto-test-pr status` is **read-only** (no Dolt writes), making
it safe to call repeatedly from monitoring.

#### Per-rig opt-in (Option 5a, in `config.json`)

```json
{
  "auto_test_pr": {
    "enabled": false,
    "language": "go",
    "cadence": "weekly",
    "max_files": 3,
    "max_loc": 200
  }
}
```

- `enabled` is the only toggle a rig owner ever needs.
- `language` is one of the v1 allow-listed values; `"go"` is the
  only valid value in v1. Anything else → schema-validation error
  with explicit "v1 only supports `go`; see <v2 bead-id>".
- `cadence` is one of `weekly` / `daily` / `disabled`. v1 default
  is `weekly` (per Q6 circuit-breaker tolerances).
- `max_files` / `max_loc` default to PRD values; can only be
  *tightened* by the rig owner, never loosened (the cycle reads
  `min(rig_setting, system_max)` so a higher value is silently
  capped).

The schema-loader and `gt auto-test-pr enable` BOTH validate this
shape. Editing `config.json` directly without `gt auto-test-pr
enable` is allowed but the CLI is the recommended path because it
performs validation + a confirmation prompt + writes a notes entry
on the rig's pinned state bead.

#### Conventions sheet

`.gt/auto-test-pr/conventions.md` — checked into the rig. The PRD
Q5 names this. v1 contains:

```
# Auto-test-PR conventions for <rig>
- Test framework: stdlib `testing` + `github.com/stretchr/testify/require`
- Fixture loaders: `internal/testutil/fixture.go::Load`
- Common factories: `internal/testutil/factory.go`
- Anti-patterns: do NOT use mock packages outside of internal/testutil/mocks
- Naming: TestX_<scenario> (snake-case scenario suffix)
- Coverage: aim for branch coverage; line-only is insufficient
- Pre-existing intent comments: ALLOWED only when explaining a
  non-obvious mock value or assertion. Provenance markers
  (`// gt:auto-test-pr origin=…`) are MANDATORY.
```

#### MR / PR template

`.gt/auto-test-pr/mr-template.md` — Option 8 banner.

#### Dispatch-bead JSON envelope

Option 6a, above. Mayor files; polecat reads.

#### Code-level marker

Option 7a, above. Polecat-side gate enforces presence.

#### Formula registration

A new entry: `mol-polecat-work-test-improver` slotted under
`internal/formula/formulas/`. It extends `mol-polecat-work` with
the five Q2 quality gates wired in (coverage-delta, synthetic-mutant
sanity, flakiness, tautology-linter, gitleaks). The formula-name
is referenced in the dispatch-bead envelope (`args.formula`).

#### Reviewer "magic phrase" (gaps non-blocking, kept)

Reviewers can reply to the auto-test MR with:

```
gt auto-test-pr: pause-rig-7d
```

`mol-pr-feedback-patrol` (already standing) recognizes this token
in any comment thread on a `gt:auto-test-pr`-labeled MR and writes
the pause to that rig's pinned state bead, no operator needed.
Magic phrase is documented in the MR banner so a maintainer under
fire doesn't need to find the rig config file.

## Constraints Identified

- **Hard: gu-gal8.** No CLI verb may cause a polecat to *file* a
  bookkeeping bead. Mayor files; polecat reads. The CLI verbs
  (`enable`, `disable`, `pause`, `resume`) all write through the
  rig owner / operator's identity, never through a polecat.
- **Hard: language allow-list (Q4).** `enable --rig=<rig>` MUST
  reject a rig whose `auto_test_pr.language` is unsupported, with
  a static error message naming the v2 bead. No custom-command
  escape hatch in v1.
- **Hard: pilot-rig restriction (Q1).** v1 CLI rejects
  `--rig=<not gastown_upstream>` for `enable`. (`status`,
  `pause`, `resume` work for any rig because they may need to
  operate on a partially-rolled-back rig.)
- **Hard: idempotency.** `enable` on an already-enabled rig is a
  no-op + non-zero exit code 0 (POSIX-friendly). `disable` on an
  already-disabled rig is the same. Pausing a paused rig extends
  the pause window (max of existing and new); never shortens.
- **Hard: in-flight MR safety on disable.** `disable --rig=<rig>`
  does NOT cancel in-flight MRs (per S6 in PRD). The CLI must
  warn if `state ∈ {dispatched, mr-pending, mr-revising}`:
  ```
  warning: rig has open MR <id>; it will continue. Use
           `gt auto-test-pr pause` to also block revisions.
  ```
- **Hard: short-lived auth.** Per Q3 the v1 Refinery-only path uses
  the existing polecat identity — the CLI MUST NOT introduce a
  PAT/token field in `config.json`.
- **Soft: ergonomic parity with `gt patrol`.** Verb shape
  (`pause`, `status`, `resume`) follows existing conventions so
  operators don't have to relearn. New verbs are not introduced.
- **Soft: discoverability via `--help`.** Each subcommand's
  long-help includes a one-line pointer to the design doc and the
  PRD; in v2 a wiki link replaces the file path.

## Open Questions

These need either a cross-leg decision or human input. They are
ranked by blast radius.

1. **Rig-config schema versioning.** Today's `config.json` has no
   `version` field. Adding `auto_test_pr` to it forces a decision:
   (a) bump an explicit version, (b) treat absence-of-key as
   v0-implies-disabled. Recommendation: (b) — zero migration cost.
   Confirm with the **integration** leg (which knows what other
   features are simultaneously editing `config.json`).

2. **Where does `enable` write?** Two candidates: (i) the rig's
   `config.json` directly, (ii) the rig's pinned state bead. Q7
   names the pinned state bead as authoritative for *runtime
   state*; the toggle is *config*, not state. Recommendation: (i).
   But: the cycle reads config from disk, so `enable` triggers a
   git commit. That means `enable` is git-history-touching — is
   that surprising? Recommendation: yes-but-fine; the commit is
   small and self-documenting (`feat(rig): enable auto-test-pr`).
   Cross-check with the **data** leg.

3. **`gt auto-test-pr status` against a paused-due-to-circuit-breaker
   town.** Per Q6, the cycle town-wide-pauses itself if 3 unmerged
   closes hit within 7 days. Should `status` surface this distinct
   from a manual pause? Recommendation: yes — emit
   `state: cooled-down (auto: 3 unmerged closes 7d)`. Confirm with
   the **scale** leg (it owns rate-limit semantics).

4. **`disable` semantics under in-flight MR.** PRD S6 says
   in-flight MRs are left alone. But: should `disable` also pause
   `mol-pr-feedback-patrol` from re-dispatching a revision polecat?
   Or does the in-flight MR get one revision attempt and then go
   quiet? Recommendation: leave revision in flight (re-enable would
   need `gt auto-test-pr enable` anyway; the maintainer's choices
   are merge / close-no-merge / wait-and-let-it-die-on-cooldown).
   Confirm with **integration** leg.

5. **Magic phrase parsing in `mol-pr-feedback-patrol`.** Owner of
   that change is *not* this leg — flag for the integration leg
   to budget the patrol changes (PRD review's "load-bearing weasel-
   phrase" risk). The CLI side here only emits the phrase in the
   banner.

6. **`gt auto-test-pr status --json` schema stability.** Op tools
   will start parsing it. Should the schema be in
   `internal/genjson/schemas/` and version-stamped now? Recommend:
   yes; bake the `version: 1` field in from day one. Cheap.

7. **Should `gt auto-test-pr` be a hidden / experimental command in
   v1?** Pros: signals "pilot-only". Cons: undermines the operator
   kill-switch (operators won't find a hidden command under fire).
   Recommendation: **NOT hidden**. Mark with a one-line "EXPERIMENTAL
   (v1: pilot rig only)" annotation in `--help`, not a hide flag.
   This is the same policy used for `gt sling-pool` historically.

## Integration Points

- **integration leg** (mol-pr-feedback-patrol changes): the magic
  phrase, the revision dispatch path, and how the patrol detects
  the `gt:auto-test-pr` label. The CLI emits the magic phrase in
  the banner; the patrol side recognizes it. Coordinate so the
  string literal is one constant in `internal/auto_test_pr/marker.go`
  shared by both.

- **data leg**: pinned state-bead schema (`<rig>-auto-test-state`
  bead's notes/design field — `state`, `paused_until`, `last_cycle_at`,
  `last_merge_at`, `consecutive_unmerged_closes`,
  `circuit_breaker_until`). The CLI reads/writes this; the data leg
  defines the structure.

- **scale leg**: cadence enforcement and circuit-breaker counting.
  `status` surfaces those values; `pause`/`resume` modify them via
  the same Mayor-side helpers the cycle uses (no duplicate paths).

- **security leg**: `enable` rejecting unsupported language is a
  trust-boundary enforcement (Q4). Coordinate the exact error string
  + audit-bead emission so security and CLI agree.

- **ux leg**: discoverability — does the MR banner answer everyone's
  first question? Whose CODEOWNERS gets pinged? The CLI emits the
  banner; the ux leg owns the *content* design.

- **Dispatch envelope** is shared with the **data** and **integration**
  legs (they own the polecat-side parsing).

- **Existing Gas Town surfaces touched:**
  - `internal/cmd/auto_test_pr.go` (new file; mirrors
    `internal/cmd/patrol.go` shape).
  - `internal/formula/formulas/mol-polecat-work-test-improver.formula.toml`
    (new).
  - Rig config loader in `internal/cmd/config.go` or wherever rig
    config is parsed today (cross-check with **data** leg).
  - `internal/cmd/sling.go` / sling-context envelope generator —
    reuse don't fork.
  - `mol-pr-feedback-patrol` formula — reuse don't fork (payload
    on the dispatch bead is enough to re-target it for revision).

---

*Convoy leg `api` for `auto-test-pr`. Sibling legs: `data`, `ux`,
`scale`, `security`, `integration`. Synthesis: `gu-syn-gdjtq`.*

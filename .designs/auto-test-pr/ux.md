# User Experience Analysis

## Summary

Auto-Test-PR has four distinct user personas — **rig maintainer**,
**Overseer/on-call**, **PR reviewer** (often the same human as the
maintainer), and **operator/Mayor** — and each interacts through a
different surface. The dominant UX risk is the **invisibility** of the
mechanism: it runs on a cadence, can pause itself, can rate-limit, can
silently abort a cycle, and (in v1 Refinery mode) doesn't actually
produce a GitHub PR — it produces a merge-queue MR. If users can't
*see* why nothing happened or *predict* what will happen next, they
will assume the system is broken and disable it. The remedy is a
single, opinionated `gt auto-test-pr` CLI namespace whose `status`
sub-command answers the four questions every user actually has —
*"Is it on for my rig? When does it next fire? What's the current
state? When did it last try and why did/didn't it produce work?"* —
plus a strict, machine-readable banner contract on every generated
MR/PR so reviewers never have to ask why a particular change exists.

The mental model we want users to form is **"a polite contributor who
opens at most one small test PR per rig per week, never auto-merges,
backs off on rejection, and is one CLI flip from being silenced."** The
mechanism succeeds when maintainers stop noticing it as a *system* and
start treating its output as ordinary code review.

## Analysis

### Key Considerations

- **v1 is Refinery-mode only (Q1 decision).** There is no GitHub PR in
  v1 — work flows as an MR bead through the merge queue. Naming the
  feature "auto-test-PR" while v1 produces no PR is itself a UX trap.
  Either the user-facing language must explicitly reconcile this
  ("auto-test PR = auto-test MR in Refinery mode") or the feature must
  be re-branded for the pilot.
- **Opt-in is single-flip, opt-out is single-flip (Goal 2).** This is
  the load-bearing UX promise. Every other interaction is rare; the
  enable/disable pair is the 80% of CLI usage. They MUST be
  symmetrical, idempotent, and obvious from `--help`.
- **The state machine has six states (Q7):** `idle | picking |
  dispatched | mr-pending | mr-revising | cooled-down`. Maintainers
  will absolutely ask "why isn't anything happening?" — and the answer
  is in this state machine. If we expose the raw states, we push
  internal vocabulary onto users; if we hide them, we make the system
  feel inscrutable. Surface human-readable summaries; expose raw
  states behind `--verbose`.
- **Discoverability matters more than feature-richness.** Power users
  will read source. Everyone else will type `gt auto-test-pr` with no
  args and read the help. The help text is the design doc for 90% of
  encounters.
- **Reviewer fatigue is the primary failure mode.** If five generated
  MRs in a row are tautology / behavior-freezing slop, the maintainer
  flips the kill-switch and never re-enables. The Q2 quality gates
  (coverage delta + synthetic-mutant + diff markers + flakiness +
  tautology lint) are an *engineering* concern but the UX is the
  banner: it must let a reviewer make the merge/reject decision in
  ≤30s of skim time.
- **Two failure modes look identical from outside:** "polecat ran the
  cycle but every gate failed and no MR was opened" vs "polecat never
  ran the cycle (rate-limited / paused / cooled-down)." Both produce
  no MR. They have very different remediation. `status` must
  distinguish them clearly.
- **Existing patrols overlap.** `mol-pr-feedback-patrol` handles
  comment revision today. Teaching it to special-case `gt:auto-test-pr`
  is invisible to users — but it changes feedback latency. The PRD
  body banner needs to set expectations: "comments are picked up by
  the next patrol cycle (typically within 1h)" not "comments will be
  responded to immediately."
- **The `<rig>-auto-test-state` pinned bead (Q7) is the source of
  truth.** Power users will want to inspect it; `gt auto-test-pr show
  --rig=<rig> --raw` should print it. Hiding it will lead to people
  reaching for `bd show <rig>-auto-test-state` and learning the
  schema by reverse engineering, which guarantees they'll write
  scripts against the internal layout that we'll then break.
- **Pause is a verb users will reach for in panic.** It MUST be fast,
  idempotent, scoped clearly (--rig vs --all), and reversible
  (`resume`). A 5-second wait for a Dolt round-trip in the middle of
  an incident is unacceptable.
- **The kill-switch is also a v1 deliverable (Q6).** This is rare;
  per-rig and town-wide pause + automatic circuit-breaker (3
  consecutive closes / 7d → 72h town-wide pause). The CLI must show
  *which* of these is currently in effect on `status` so an operator
  doesn't sit confused about why nothing fires.
- **Polecats are also "users" of the dispatch contract (Q5).** Their
  UX is the dispatch bead payload — target file, conventions sheet,
  prior comments — and the gate-failure path. A polecat that fails a
  gate must record *which* gate, on the bead notes, in a format that
  the next polecat (or a human debugging) can read.

### Options Explored

#### Option 1: Single top-level CLI namespace `gt auto-test-pr`
- **Description**: All operations live under one verb tree:
  `gt auto-test-pr enable|disable|pause|resume|status|show|history`,
  with per-rig flags. Config writes go through `enable`, not through
  a separate `gt rig config` flow. One namespace, one help screen,
  one mental model.
- **Pros**:
  - One thing to remember; one help screen explains the entire
    feature surface.
  - Symmetrical pairs (`enable`/`disable`, `pause`/`resume`) reduce
    cognitive load.
  - `--help` becomes the de-facto doc; mirrors how `gt mail`,
    `gt mol`, `gt dolt` are organized.
  - Easy to gate the entire surface behind a feature flag during the
    pilot.
- **Cons**:
  - Duplicates rig-config-writing surface (`enable` writes the rig's
    `auto_test_pr.*` config keys); risks drift from the canonical
    `gt rig config` flow.
  - Doesn't slot cleanly into "every rig setting goes through one
    place."
- **Effort**: Low (single Cobra subcommand tree; reuses existing
  Mayor-owned bead writes).

#### Option 2: Split — config under `gt rig`, runtime under `gt auto-test-pr`
- **Description**: Configuration (enable/disable/language/cadence)
  lives under `gt rig config <rig> auto-test-pr.*`. Runtime ops
  (status/pause/resume/show) live under `gt auto-test-pr`. This
  matches the existing convention where rig-level *settings* are in
  `gt rig` and town-level *runtime* commands have their own verbs.
- **Pros**:
  - Aligns with existing town conventions (no second surface for
    rig-level settings).
  - Cleaner separation: "what is configured" vs "what is happening."
- **Cons**:
  - Two surfaces to learn; `--help` is split; the question "where do
    I turn this on?" has two plausible answers.
  - Breaks the symmetry of `enable`/`disable`/`pause`/`resume` —
    pause is *not* a config flip, but users will reach for it
    expecting one.
  - Discovery cost: a new user runs `gt auto-test-pr --help` and gets
    only half the picture.
- **Effort**: Medium (touches `gt rig config` schema + new top-level
  command + cross-references).

#### Option 3: Everything under `gt rig` (no top-level namespace)
- **Description**: `gt rig auto-test-pr enable --rig=<rig>`,
  `gt rig auto-test-pr status` (with `--all` for town-wide). Keeps
  the entire feature contained inside the rig CLI, no new top-level
  verb.
- **Pros**:
  - Smallest CLI footprint; nothing new at the top level.
  - Reinforces "this is a rig-level feature."
- **Cons**:
  - Town-wide commands (`pause --all`, `status` for all rigs) feel
    awkwardly placed under a rig-scoped verb.
  - Operator muscle-memory for incidents (`gt auto-test-pr pause
    --all` is short, memorable, paste-into-runbook-able) is lost.
  - Doesn't match how `gt dolt`, `gt mail`, `gt mol` are organized
    (each is a feature with its own top-level verb).
- **Effort**: Low.

#### Option 4: Web/console UX (deferred)
- **Description**: A dashboard surface (Mayor-level) showing per-rig
  enabled/paused state, last cycle, open MR, recent rejection rate,
  circuit-breaker state.
- **Pros**:
  - Best discoverability; non-CLI users (overseers, lurkers) see the
    health at a glance.
- **Cons**:
  - PRD explicitly defers a Mayor-level dashboard to v2.
  - For the v1 pilot (one rig, one operator) it is over-investment.
- **Effort**: High (new surface) — out of scope for v1.

### Recommendation

**Option 1 (single top-level `gt auto-test-pr` namespace) for v1.** The
arguments for splitting (Option 2) are real but the v1 pilot has
exactly one opted-in rig and ~3 humans interacting with it; the cost
of a second surface to learn outweighs the cost of a small amount of
config-writing duplication. The duplication can be neutralized by
having `gt auto-test-pr enable` internally call into the same code
path as `gt rig config <rig> set auto-test-pr.enabled=true` — they
become aliases over a single config writer, not two writers.

**Concrete v1 CLI surface:**

```
gt auto-test-pr enable   --rig=<rig> --language=go [--cadence=weekly]
gt auto-test-pr disable  --rig=<rig>
gt auto-test-pr pause    --rig=<rig>|--all --duration=24h [--reason=...]
gt auto-test-pr resume   --rig=<rig>|--all
gt auto-test-pr status                       # town-wide table
gt auto-test-pr show     --rig=<rig> [-v]    # per-rig detail
gt auto-test-pr history  --rig=<rig> [--last=10]   # last N cycles
```

**`status` table shape** (the single most-read surface):

```
RIG               STATE          NEXT TICK  LAST MR    REJECT-RATE  PAUSE
gastown_upstream  idle           +3d 4h     #482 ✓     0/3          —
casc_crud         disabled       —          —          —            —
beads             cooled-down    +2d        #117 ✗     2/5          rig 5d
(town-wide)       running        —          —          —            —
```

State labels are user-facing names; raw state-machine names
(`mr-pending` etc.) appear only under `show -v`.

**`show --rig=<rig>` shape** (the second most-read surface):

```
Rig:       gastown_upstream
Status:    idle (next cycle in 3d 4h)
Language:  go
Cadence:   weekly (max 1 PR/week)
Last MR:   #482 (merged 2d ago, 3 files, +147 LOC)
History:   5 attempts, 4 merged, 1 closed-unmerged (rejection-rate 20%)
Cooldown:  none
Pause:     none
Conventions: .gt/auto-test-pr/conventions.md (last edited 12d ago)
```

**MR/PR body banner contract** (loadbearing for reviewer trust):

```
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
  ✓ coverage delta (+16% on target file's branch coverage)
  ✓ synthetic-mutant sanity (each new test fails when its target
    line is commented out)
  ✓ flakiness (10/10 reruns green on the new tests + their package)
  ✓ tautology linter (no `assert(true)`, no literal-equality, all
    tests have at least one assertion)
  ✓ gitleaks (no secrets in diff)

To pause this rig:    gt auto-test-pr pause --rig=gastown_upstream
To turn it off:       gt auto-test-pr disable --rig=gastown_upstream
Design doc:           .gt/docs/auto-test-pr.md
```

The banner answers the four questions a reviewer has in ≤10s of
skim: *what changed, why this file, what's now covered, how do I opt
out.* The "Quality gates passed" block is the receipt — it lets the
reviewer trust that the cheap gates are not the only check, without
actually running them.

**Code-level marker** (Q2 MUST gate):

```go
// gt:auto-test-pr origin=gu-leg-nehua covers=internal/refinery/queue.go:47
func TestQueue_Lease_ExpiresOnTimeout(t *testing.T) { ... }
```

The `covers=<file:line>` form is machine-readable for downstream
tooling (e.g. an `lsif`-style index of "which auto-tests cover which
lines"). The `origin=<bead-id>` form lets a future debugger trace a
test back to its dispatch.

**Defaults that minimize new-user surprise:**

- `cadence=weekly` (≤1 MR/week is a hard cap from OQ7; the cadence
  flag is functionally a no-op in v1 but reserved for v2 cadence
  loosening).
- `language=go` is required at enable time (no auto-detect — Q4 says
  language-keyed allow-list, derive commands from language).
- `--duration` on pause defaults to 24h with explicit upper bound 7d
  (anything longer should use `disable`).

**Discoverability hooks:**

1. `gt auto-test-pr` with no args prints a short overview + the
   `status` table (not just usage).
2. `gt auto-test-pr enable --help` shows a minimal example *plus* a
   pointer to where to put the conventions sheet.
3. The Mayor's daily summary (if such a thing exists) includes an
   "auto-test-pr" line per opted-in rig.
4. The first MR opened on a newly-opted-in rig posts a notification
   bead to the Overseer with subject `auto-test-pr: pilot first MR
   opened on <rig>` so it's not silent.

## Constraints Identified

- **Refinery mode does not produce a GitHub PR in v1.** The CLI verb
  `auto-test-pr` is therefore mildly misleading on the pilot. The
  banner contract above must be present *in the MR description* (which
  is what Refinery surfaces). The "GitHub PR" rendering is a v2
  concern. Document this in the v1 README explicitly so users don't
  hunt for a PR on github.com that doesn't exist.
- **`--rig=<rig>` is required on most commands.** Defaulting to "the
  rig of the current working directory" is tempting but bug-prone in
  cross-rig tooling like Mayor. v1 should require explicit `--rig`
  except where ambiguity is impossible (e.g. inside a polecat's
  worktree where `GT_RIG` is set, fall back to it; otherwise error
  with a helpful message).
- **`status` and `show` MUST be readable when Dolt is degraded.** The
  pinned-state-bead is the source of truth, but if Dolt is slow these
  commands must time out fast (≤2s) with a clear "Dolt unreachable"
  message, not hang. Operators reach for `status` *during* incidents.
- **The conventions sheet (Q5) is an authored artifact, not generated
  in v1.** The CLI must point users at it (`gt auto-test-pr enable`
  outputs "place your conventions at .gt/auto-test-pr/conventions.md
  before the first cycle fires") rather than auto-generating a stub.
  Auto-generation is v2 (Q5 explicit defer).
- **The `<rig>-auto-test-state` bead is Mayor-owned (gu-gal8 + Q7).**
  Polecats must NOT update it. The CLI must enforce this — `gt
  auto-test-pr show --raw` is read-only against the bead. Any
  mutating op (pause, resume, enable, disable) goes through Mayor,
  not direct bead writes.
- **Pause has compare-and-set semantics with the cycle (Q7).** A
  pause issued during `picking` must abort the in-flight pick. The
  CLI must wait for the abort to confirm before returning success;
  otherwise users issue `pause` and a cycle still completes 2 minutes
  later, which feels broken.

## Open Questions

- **Naming — "auto-test-pr" vs "auto-test-mr" vs "auto-test"?**
  v1 produces an MR not a PR; v2 will produce both. Keeping the name
  `auto-test-pr` is fine if the docs/banner explain it. Calling it
  `auto-test` (plain) is shorter but loses the "this opens
  patches" affordance. **Recommendation:** keep `auto-test-pr` and
  document that "PR" is a generic term for "patch request" in this
  context; don't rename mid-v1.
- **Should `status` default to JSON output, or a human table?**
  Operators need both. **Recommendation:** human table by default,
  `--format=json` for scripting. Match `gt dolt status` precedent.
- **How verbose should `show` be by default?**
  Three lines (status, last MR, next tick) vs the full ten-line block
  above. **Recommendation:** the full block by default. Users running
  `show` already opted into detail; brevity is what `status` is for.
- **Should `pause` accept `--reason` or be free-form?**
  Auditing the *why* of pauses helps post-mortems. **Recommendation:**
  optional `--reason="..."` recorded on the state bead's notes; not
  required, but strongly encouraged in `--help`.
- **Per-rig conventions edits — is there a CLI for them, or just `vi`?**
  The conventions sheet is a markdown file in-repo; a CLI is over-
  engineered. **Recommendation:** no CLI; `gt auto-test-pr enable`
  prints the path and, if missing, refuses to fire the first cycle
  with `conventions sheet not found at <path>`. Maintainer authors it
  by hand.
- **Does the MR body include the dispatch bead ID for traceability?**
  Yes — already in the banner above (`Origin bead: gu-leg-nehua`).
  Confirm with synthesis that this bead reference is stable across
  refinery flow.

## Integration Points

- **API dimension (`api.md`):** The CLI surface above is the API.
  Where this analysis specifies *what* the user sees, the API
  analysis should specify *how* commands are wired (Cobra subcommand
  tree, flag parsing, output format constants, exit codes). Confirm
  that exit codes are: 0 success, 1 user error, 2 system error
  (Dolt down), 3 not-enabled, so scripts can branch.
- **Data dimension (`data.md`):** The `<rig>-auto-test-state` bead
  schema directly drives `status` and `show` output. UX needs:
  `state`, `last_cycle_at`, `last_mr_id`, `last_mr_outcome`,
  `consecutive_closes`, `pause_until`, `pause_reason`,
  `pause_actor`, `next_tick_at`. If any of these are missing, the
  CLI cannot render the recommended tables.
- **Scale dimension (`scale.md`):** `status` and `show` will be
  called frequently by the Overseer (and possibly polled by an
  external dashboard in v2). They MUST be O(rigs) reads of pinned
  beads, not O(MRs) scans. Confirm the read pattern with the data
  dimension.
- **Security dimension (`security.md`):**
  - Banner explicitly *invites* the reviewer to "skim the receipt and
    merge"; this is friction-reduction, not trust-erosion, only if
    the gates listed actually ran. The banner format must be
    machine-generated from gate output, never hand-edited by a
    polecat (otherwise a polecat could lie about gate success).
    Encode the banner as a refinery-side post-check: "MR description
    must contain the gate receipt block, and the gate receipt's
    claims must match what was logged on the dispatch bead's notes."
  - `gt auto-test-pr disable --rig=<rig>` MUST require the same
    authorization as opt-in (rig owner or Overseer). Don't let a
    random polecat-owned process disable a rig.
- **Integration dimension (`integration.md`):**
  - Hand-off to `mol-pr-feedback-patrol` is invisible to the user but
    affects feedback latency. The MR banner should mention "comments
    are picked up by the next patrol cycle (typically <1h)." If the
    integration analysis recommends *not* reusing that patrol, the
    UX banner needs to be updated accordingly — the user should not
    learn about the handoff by experiment.
  - The state machine in Q7 has `mr-revising` — this corresponds
    directly to feedback patrol activity. `show` must surface
    "currently being revised by patrol cycle <id>" so a maintainer
    who left a comment 10 minutes ago understands the comment was
    seen and is being worked.
- **Cross-cutting:** every dimension must agree on the same set of
  user-facing state names. Don't let `data.md` use `mr-pending` while
  this UX uses `MR submitted`. Pick one set, propagate it.

## Sources

- `.prd-reviews/auto-test-pr/prd-draft.md` (clarified PRD with Q1–Q7
  decisions)
- `.prd-reviews/rqoca/prd-review.md` (parallel-review synthesis)
- `.prd-reviews/rqoca/{ambiguity,scope,gaps,requirements,
  stakeholders,feasibility}.md` (six-leg PRD review analyses)

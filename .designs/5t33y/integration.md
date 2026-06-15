# Integration Analysis

> Dimension analysis for the proposed **`mol-rig-retrospect`** formula — a per-rig
> retrospective that mines one rig's Claude session transcripts (plus the shared
> `daemon/daemon.log` and `.events.jsonl`) for workflow friction and files ranked,
> deduped PROPOSALS to improve that rig's agent context mechanisms.
> Leg: **integration** (how it fits the existing system). Convoy: `5t33y`. Scope: large.

## Summary

`mol-rig-retrospect` is not a greenfield component — it is the **third member of an
already-wired formula family** (`mol-curio-retrospect`, `mol-triage-retro`), and the
overwhelming integration finding is that *almost nothing new needs to be built at the
wiring layer.* The town already has every seam this feature plugs into: the three-tier
formula resolution model (embedded → town → rig), the cron-gated dispatch-plugin
pattern (`curio-retrospect-dispatch`), the deterministic-digest-then-reasoning-polecat
control flow, the file-first bead landing path, the `bd`-label dedup substrate, the
merge-queue gate hook for an air-gap guard, the `artifacts/` (gitignored) digest sink,
and the `rigs.json` rig registry. The integration job is therefore **composition and
placement, not invention** — and the single highest-leverage decision is *where each of
the four new artifacts lives* (formula, dispatch plugin, deterministic digest tool,
proposal-target guard), because that placement determines the migration path, the
rebuild coupling, and which git repo each change commits to.

The one genuinely novel integration risk — the one that has no precedent in the two
sibling formulas — is that this feature **reads a town-wide, externally-grown substrate
(`~/.claude/projects/`) that is not itself a Gas Town artifact and is not per-rig
partitioned on disk.** The resolver that maps `rig_name` → transcript dirs (API leg) and
the cursor that bounds re-reads (data leg) are the two components that touch this
foreign substrate, and they are where integration, scale, and security all converge.
Everything else is a known quantity: do exactly what curio did, in the same places.

## Analysis

### Key Considerations

- **Four new artifacts, four placement decisions.** The feature decomposes into:
  1. **The formula** `mol-rig-retrospect.formula.toml` — its canonical home is the
     gastown *source* repo at `internal/formula/formulas/` (the embedded set is the
     **single source of truth**, 51 formulas, compiled into the `gt` binary). The town
     copy at `~/gt/.beads/formulas/` is a *provisioned* copy written by
     `ProvisionFormulas` at `gt install` and refreshed by `UpdateFormulas`
     (`gt upgrade` step 5 / `gt doctor`). Verified: `mol-curio-retrospect` lives at
     `internal/formula/formulas/mol-curio-retrospect.formula.toml` in the source repo
     **and** as a provisioned copy in town `.beads/formulas/`.
  2. **The dispatch plugin** `rig-retrospect-dispatch/` — its canonical home is the
     gastown *source* repo `plugins/` directory (verified: `curio-retrospect-dispatch/`
     is git-tracked in the source repo at `plugins/curio-retrospect-dispatch/{plugin.md,
     run.sh,run_test.sh}`), and reaches the live town `~/gt/plugins/` as a synced copy
     (verified byte-identical today). Daemon plugins are town-level because their
     substrate (the formula + the digest tool) is gastown source, not any one rig — the
     `curio-retrospect-dispatch` plugin doc states this constraint verbatim.
  3. **The deterministic digest tool** — this is the live integration *fork in the
     road* (see Options). Curio uses a Go `cmd/curio-proposer --emit-digest` binary;
     triage-retro uses a Python `bin/triage-retro-analyze` script. The choice changes
     the rebuild coupling and which repo the tool commits to.
  4. **The proposal-target / air-gap guard** — modeled on
     `scripts/guards/curio-proposal-target-guard.sh`, wired as a per-rig merge-queue
     gate via *runtime* `gt rig settings set` (NOT committed config). This is a
     mayor-lane wiring step, enabled together with the dispatch plugin so the air-gap is
     live before the first real dispatch.

- **The control flow maps 1:1 onto curio's existing workflow type.** Curio's steps
  (`read-digest → dedup-open-proposals → reason-and-rank → land-proposals → finish`) are
  exactly the V1 shape the API leg recommends (Option 1, single polecat over a digest).
  `type = "workflow"`. No new molecule machinery, no new step-target dispatch, no engine
  change is required — verified the engine already dispatches its own pool-bound `-wfs-`
  steps (recent fix `685c1b20`, and `auto-dispatch`'s `*-wfs-*` exception confirms
  workflow step beads are first-class polecat work).

- **Dispatch is a solved pattern: cron gate + four guards + render-then-sling.** The
  daemon's `dispatchPlugins` path evaluates cron gates through `Recorder.CronDue`
  (`parseCron`, standard 5-field, host-local clock), with `DispatchGrace` suppressing
  re-dispatch storms. `curio-retrospect-dispatch` already demonstrates the full guard
  stack we copy: kill-switch pre-check, stale-tolerant single-instance guard, volume
  circuit breaker, then render-digest + positional `gt sling`. All skip paths exit 0
  (`severity = "low"`) — a guarded lane is the expected posture, not an outage.

- **The proposal beads must NOT be re-dispatched as work by the scheduler.** This is a
  concrete, verified dependent-side hazard. `auto-dispatch` slings *open, ready* beads
  to polecats. Curio defuses this by assigning proposal beads to `mayor` — and
  `auto-dispatch` explicitly **skips beads whose owner is a Gas Town agent**
  (`mayor`, `<rig>/...`, etc.): "if the owner string contains a `/` or matches a known
  agent role, treat as agent-owned and skip." So `rig-retro-proposal` beads MUST inherit
  the same `--assignee mayor` (or rig-owner) convention, or the scheduler will try to
  dispatch each proposal as if it were a task — the exact feedback-loop class
  auto-dispatch already guards against for `type:plugin-run` receipts (the gs-wisp-3rw
  8-hour-stuck incident). This is a hard integration constraint, not a nicety.

- **The substrate `~/.claude/projects/` is foreign, town-wide, and host-local.** Unlike
  curio (reads its own ledger) and triage (reads a `run_dir` it controls), this formula
  reads Claude's per-session transcript store, which: (a) is *not* a Gas Town artifact —
  no schema guarantee, the record shape can drift across Claude versions (the data leg
  catalogued today's record types but they are version-coupled); (b) is *not* per-rig
  partitioned — one dir per role per worktree, with the lossy `/`+`_`→`-` encoding the
  API leg flagged; (c) only exists on the **daemon host** — the resolver and digest tool
  must run where the transcripts are, exactly like `rebuild-gt`'s source discovery and
  `curio-proposer`'s `go run` fallback both assume a local source rig. A run dispatched
  to a worktree on a different host would see an empty/wrong substrate.

- **The digest path must be a host-shared absolute path under the town root.** Polecats
  run in isolated git worktrees on the *same host filesystem* (no chroot) under the town
  root, so the dispatch plugin renders the digest to
  `$GT_TOWN_ROOT/artifacts/rig-retrospect/<rig>-<UTCstamp>.md` and passes that exact path
  as `--var digest_path=`. Verified: `artifacts/` is gitignored (`.gitignore:149`) and
  `artifacts/curio-retrospect/` already exists as the curio precedent. The reasoning leg
  reads the digest with `cat {{digest_path}}` — never the raw `*.jsonl`.

- **Formula content drift is a known, documented operational cost.** The town has a
  written ledger (`docs/design/formula-drift-reconciliation.md`) precisely because the
  3-tier model produces drift: `ProvisionFormulas` never overwrites an existing file and
  `UpdateFormulas` only refreshes the *town* tier. So a new formula lands canonically in
  the embedded source set, but the provisioned town/rig copies refresh on a different
  cadence (binary rebuild + `gt upgrade`/`doctor`). The integration plan must account for
  the lag between "merged the formula to source" and "the live daemon resolves the new
  version."

### Options Explored

The integration-level decision that is genuinely open (the others are "copy curio") is
**where the deterministic digest tool lives and what language it is**, because that
choice cascades into the rebuild coupling, the commit repo, and the dispatch plugin's
tool-resolution logic.

#### Option A: Go binary in gastown source `cmd/rig-retro-digest` (the curio-proposer model)
- **Description:** A new `cmd/rig-retro-digest` Go command in the gastown source repo,
  resolved by the dispatch plugin the way `run.sh` resolves `curio-proposer` today:
  prefer a binary on PATH or at a source-rig root, else `go run ./cmd/rig-retro-digest`
  from the discovered gastown source rig. Shares `internal/` packages (e.g. a new
  `internal/rigretro` mirroring `internal/curio`).
- **Pros:** Exact curio parity — operators and the dispatch plugin already understand
  this resolution. Typed, unit-testable in the Go suite (the build/test/vet gates on
  *this very bead* — `go build ./...`, `go test ./...`, `go vet ./...` — would cover it).
  The injection-inert acceptance test (`TestRenderDigest_InjectionInert` analog) lives
  next to the renderer. Can reuse a `fingerprint.Of(...)` helper for stable `cluster_id`.
- **Cons:** Couples the tool's freshness to `rebuild-gt` (binary staleness) OR to the
  `go run` fallback (needs the source rig present + Go toolchain on the daemon host).
  Adds Go surface to build and maintain. The cursor/offset-resume logic (data leg C6)
  becomes Go code.
- **Effort:** High (but it is the load-bearing, most-testable component, and the build
  gates already exist).

#### Option B: Python script in a rig `bin/rig-retro-analyze` (the triage-retro model)
- **Description:** A standalone Python script, like `triage-retro-analyze`, living in a
  rig's `bin/`, invoked by the dispatch plugin / the formula's first step. The formula's
  `validate-rig` step exits non-zero with a remediation hint if the script is missing
  (triage's `validate-run-dir` precedent).
- **Pros:** No rebuild coupling — edit the script, it's live. Faster iteration on the
  friction taxonomy and sanitization regexes. JSONL/text parsing is ergonomic in Python.
  No Go compile step in the hot path.
- **Cons:** Diverges from the gastown-source-tool convention (the dispatch plugin lives
  in gastown source, but the tool would live in a rig `bin/` — a split-brain placement).
  Not covered by the Go build/test/vet gates this bead names; needs its own test harness
  (triage ships `bin/triage-retro-analyze` tests separately). Tool must be present in the
  dispatch host's source/rig checkout. Two languages in one feature.
- **Effort:** Medium.

#### Option C: No deterministic tool — reasoning polecat reads raw transcripts directly
- **Description:** Skip the digest step; the formula's reasoning leg globs and reads
  bounded slices of raw `*.jsonl` itself.
- **Pros:** Lowest build effort; no new tool to place or rebuild.
- **Cons:** **Rejected by the security and scale legs.** It puts the LLM directly on the
  most hostile substrate in the town, gives no testable redaction/sanitization choke
  point, and makes token cost unbounded. It also breaks the family mental model (every
  retrospect reads a *frozen, bounded digest*, never raw substrate). Non-starter.
- **Effort:** Low (but violates two sibling legs' hard constraints).

### Recommendation

**Option A (Go `cmd/rig-retro-digest` in gastown source), placing all four artifacts in
the gastown source repo, rolled out behind a kill-switch with a staged enablement.**

Option A wins on integration grounds specifically because it keeps the feature
*coherent in one repo and one toolchain*: the formula (embedded source set), the dispatch
plugin (source `plugins/`), the digest tool (source `cmd/`), and the guard (source
`scripts/guards/`) all commit to the gastown source repo and ride the same review/merge/
rebuild path. That coherence is the integration property that matters — Option B's
split-brain (plugin in source, tool in a rig `bin/`) creates two provenance stories for
one feature and dodges the Go gates this bead already mandates. The rebuild coupling
Option A introduces is already solved: `curio-proposer` demonstrates the exact `go run`
fallback for a not-on-PATH source-repo tool, and `rebuild-gt` keeps the binary fresh.

**Concrete placement table:**

| Artifact | Canonical location (commit here) | Reaches the live daemon via |
|---|---|---|
| `mol-rig-retrospect.formula.toml` | `internal/formula/formulas/` (embedded, source repo) | binary rebuild + `UpdateFormulas` provisions town `.beads/formulas/` |
| `rig-retrospect-dispatch/` plugin | `plugins/rig-retrospect-dispatch/` (source repo) | synced to `~/gt/plugins/` (same path the curio plugin uses) |
| `rig-retro-digest` tool | `cmd/rig-retro-digest/` + `internal/rigretro/` (source repo) | on PATH / source-rig root / `go run` fallback (curio-proposer resolution, copied) |
| proposal-target guard | `scripts/guards/rig-retro-proposal-target-guard.sh` (source repo) | wired as a per-rig merge-queue gate via runtime `gt rig settings set` (NOT committed) |
| run-dir digests | `$GT_TOWN_ROOT/artifacts/rig-retrospect/` | gitignored runtime sink (mkdir at dispatch) |
| cursor state | `~/gt/.runtime/rig-retro/<rig>/cursor.json` OR `bd kv` (data leg Q1) | gitignored runtime / Dolt — see Open Questions |

**Staged rollout (feature-flag, no big-bang enable):**

1. **Land the tool + formula + plugin with the lane OFF by default.** Mirror curio's
   kill-switch: the dispatch plugin reads a `mayor/daemon.json` flag (e.g.
   `patrols.rig_retro.enabled`, default false/absent) and exits `result:skipped` if
   unset. Merging the code does not start the lane. Uninstalling the plugin OR leaving
   the flag off both disable it (defense in depth) — verbatim curio posture.
2. **Wire the proposal-target guard as a merge-queue gate before first enable**, via
   runtime `gt rig settings set <rig> merge_queue.gates.rig-retro-proposal-target {...}`
   (the curio guard's documented wiring). The air-gap is live before any real dispatch.
3. **Dry-run on a single busy rig first** (`gastown_upstream`) — `--var dry_run=true`,
   inspect the rendered digest size/sanitization and the "would file N proposals" output.
   This is also the scale-leg's calibration run for the digest cap and `since` window.
4. **Enable for one rig** by flipping the flag; let one nightly cycle file ≤`max_proposals`
   real proposals; have the disposer triage them. Confirm no auto-dispatch pickup
   (verify the `--assignee mayor` skip).
5. **Generalize** — the plugin can iterate the `rigs.json` rig set (or be parameterized
   per rig) once one rig is proven. Per-rig cadence/flag if rigs differ in volume.

This sequence means there is **no backwards-compatibility surface to break**: the feature
is purely additive (new formula name, new plugin name, new tool, new label namespace),
and every existing consumer is untouched until the flag flips.

## Constraints Identified

- **I1 — All four artifacts commit to the gastown source repo.** Formula → embedded
  `internal/formula/formulas/`; plugin → `plugins/`; tool → `cmd/` + `internal/`; guard →
  `scripts/guards/`. Do NOT hand-edit only the town `~/gt/.beads/formulas/` copy — that
  is a provisioned copy and will drift (the `formula-drift-reconciliation.md` lesson).
- **I2 — Proposal beads MUST be agent-owned (`--assignee mayor` or rig-owner)** so
  `auto-dispatch` skips them. A human-or-unowned proposal bead will be re-dispatched as
  work, creating a feedback loop. This is the single most load-bearing dependent-side
  constraint. (Cross-ref UX open-Q "where do proposals route" and API's `--assignee mayor`.)
- **I3 — Positional `gt sling <formula> <rig>`, never `--formula`.** The dispatch plugin
  must use `gt sling mol-rig-retrospect <rig> --create --var rig_name=<rig> ...`; the
  `--formula` flag is a different feature and fails "deferred dispatch requires a rig
  target" (gu-fc8h / gu-ono8h). `run_test.sh` must assert the positional shape — copied
  from `curio-retrospect-dispatch/run_test.sh`.
- **I4 — Digest is a host-shared absolute path under `$GT_TOWN_ROOT/artifacts/`.** The
  slung polecat reads it from inside its worktree; the path must be absolute and on the
  shared host filesystem. The plugin renders + slings the *same* shell variable
  (path-contract asserted in `run_test.sh`).
- **I5 — The tool + resolver run on the daemon host.** `~/.claude/projects/` is
  host-local; the digest tool and `rig_name`→dir resolver must execute where the
  transcripts physically are (same assumption as `rebuild-gt`/`curio-proposer`).
- **I6 — `rig_name` validated against `rigs.json`, never interpolated raw into a glob.**
  (Shared with security HARD constraint.) The town's authoritative rig registry is
  `~/gt/rigs.json` (verified: `version:1`, `rigs:{<name>:{git_url,beads:{prefix}}}`).
  An unknown rig exits non-zero with the known-rig list + closest-match hint (triage's
  `validate-run-dir` precedent).
- **I7 — Lane OFF by default via kill-switch; staged enable.** No nightly dispatch until a
  `mayor/daemon.json` flag is flipped. All guard/skip paths exit 0, `severity = "low"`.
- **I8 — Formula resolution lag is real.** After merging the formula to the embedded
  source set, the live daemon resolves the new version only after a binary rebuild
  (`rebuild-gt`) and a town-tier `UpdateFormulas` refresh. Plan the enable step *after*
  the rebuild has propagated, not at merge time.

## Open Questions

- **Q1 — Digest-tool language/placement: Go `cmd/` (Option A, recommended) vs Python
  `bin/` (Option B)?** The recommendation is Go-in-source for repo coherence + Go-gate
  coverage, but if the team values script iteration speed over the rebuild coupling, B is
  defensible. *This is the one decision the eng/synthesis review should explicitly
  settle* — it determines which gates cover the tool and which repo it commits to.
- **Q2 — Cursor storage (inherited from data leg Q1).** `~/gt/.runtime/rig-retro/<rig>/
  cursor.json` (gitignored, simplest, matches the `**/.runtime/` ignore rule) vs `bd kv`
  (rides Dolt durability + backup, survives worktree recycling). Integration lean:
  **runtime file** — the cursor is regenerable (worst case = one expensive re-scan with a
  bounded `since` window), and `bd kv` adds a Dolt round-trip + the fragility the town's
  Dolt guidance warns about. But if cursor loss on worktree recycle is unacceptable, `bd
  kv`. Needs a durability-vs-simplicity ruling.
- **Q3 — Cross-rig shared logs in a per-rig formula (inherited from data Q3 / security
  Q4).** `daemon.log` + `.events.jsonl` are town-wide; folding them in (`include_shared_logs=true`)
  partially breaks per-rig scope and widens the substrate. Integration lean: **filter
  events to the target rig's sessions/actors**, surfacing town-level signals (mass_death
  windows) only when they involve the rig. Confirm the filter predicate with data/security.
- **Q4 — One plugin iterating `rigs.json`, or one dispatch per rig?** Curio dispatches a
  single rig (`gastown_upstream`). For per-rig generality, does one plugin loop the rig
  set (one digest+sling per rig per night) or do we instantiate per-rig plugins? Loop is
  simpler to maintain; per-rig plugins give independent cadence/flags. Defer to rollout
  step 5 — start single-rig, generalize after one rig is proven.
- **Q5 — Disposer routing + risk legibility (cross-cuts security Q1 + UX).** Proposal
  beads assigned to `mayor` satisfy I2, but security wants high-risk proposals (hook /
  permission / MCP) routed to a higher review bar with a `risk:*` label shown. Does the
  dispose step stay a flat `bd list`, or do high-risk proposals get a distinct
  assignee/label lane? Integration-side this is just label + assignee convention, but the
  *policy* needs a human owner decision.
- **Q6 — Does the guard need a Go-side substrate filter too?** Curio's air-gap is two
  layers: (1) the `--emit-digest` substrate filter excludes `curio.*` self-series, (2)
  the CI guard backstops the CR. For rig-retro, layer 1 = the resolver/extractor excluding
  the retrospective's *own* sessions and `rig-retro:*` beads (data leg C5 self-reference
  air-gap); layer 2 = a guard rejecting proposals that target the retrospect machinery
  itself. Confirm both layers are scoped (the guard's "security-sensitive surface" list is
  security Q2).

## Integration Points

- **→ Formula system (`internal/formula/`):** New embedded formula at curio's tier;
  `type = "workflow"`; steps map 1:1 onto curio's `read-digest → dedup → reason-and-rank →
  land-proposals → finish`. 3-tier resolution (rig > town > embedded) and the drift ledger
  apply. The data/API legs own the step bodies; this leg owns *where the file commits* (I1)
  and the *resolution-lag* enable timing (I8).
- **→ Plugin system (`internal/plugin/`) + daemon dispatch:** New `rig-retrospect-dispatch`
  in source `plugins/`, cron gate via `Recorder.CronDue`, four guards copied from curio,
  render-then-sling. `DispatchGrace` handles re-dispatch suppression. Kill-switch flag in
  `mayor/daemon.json` (I7).
- **→ `auto-dispatch` scheduler (DEPENDENT — hazard):** Proposal beads must be agent-owned
  so the scheduler skips them (I2). This is the dependent most likely to break if the
  assignee convention is dropped — verify with a one-rig enable + scheduler observation.
- **→ `gt sling` / `gt done` / Refinery:** Zero CLI changes. Positional sling (I3). The
  reasoning polecat terminates with `gt done`; any `context-edit` CR enqueues to the
  Refinery merge queue, where the proposal-target guard gate runs pre-merge.
- **→ `bd` + label namespace:** New `rig-retro-proposal` / `rig-retro-hypothesis` labels,
  `cluster:<id>` dedup (curio B6 query verbatim), `rig:<name>` scoping. No new bead table —
  labels are the dedup/migration substrate (data leg C7). File-first discipline (create +
  `bd sync` before enrich) inherited from curio.
- **→ Merge-queue gate wiring (mayor lane):** The proposal-target guard is enabled as a
  per-rig `merge_queue.gates.*` entry via runtime `gt rig settings set` — *not* committed
  config — enabled together with the lane so the air-gap is live before first dispatch
  (curio guard's documented wiring). This is the one wiring step that is operational, not
  a code commit.
- **→ `~/.claude/projects/` substrate (foreign, host-local):** The resolver + digest tool
  are the only components that touch it; read-only; host-local (I5). The resolver's
  lossy-encoding glob + fail-loud-on-collision contract is the API leg's; the bounded
  cursored read is the data/scale legs'. This is the only integration seam with *no*
  sibling precedent.
- **→ `rigs.json` rig registry:** `rig_name` validation source (I6). Per-rig generality
  (Q4) iterates this set.
- **→ Sibling legs:**
  - **data** — owns the digest schema, cursor (Q2), self-reference air-gap (its C5 = my
    Q6 layer 1), and the no-new-table label substrate. My placement table is the physical
    home for its logical records.
  - **api** — owns the resolver contract, the formula vars, and the positional-sling
    constraint I restate as I3. The `cmd/rig-retro-digest` tool (Option A) is the concrete
    binary behind its "deterministic digest step."
  - **security** — owns the trust-inversion threat model; my guard-wiring and kill-switch
    rollout are the *mechanical* enforcement of its agent-proposes/human-disposes + air-gap
    mandates. Its Q2 (security-sensitive surface) defines my guard's reject list.
  - **ux** — owns the disposer query + `[kind]` title prefixes + run-summary line; my I2
    (`--assignee mayor`) and Q5 (risk routing) are the integration realization of its
    legibility goals.
  - **scale** — owns the cap *values* (digest size, per-file read cap, `since` window) my
    dry-run rollout step (3) calibrates; the bounded read is simultaneously its cost
    control and security's injection-surface control (one number, two rationales).

## Sources

- [`mol-curio-retrospect.formula.toml`](file:///home/canewiw/gt/.beads/formulas/mol-curio-retrospect.formula.toml) — formula control flow, agent-proposes/human-disposes, file-first, cluster dedup, trust boundary, advisory cap — accessed 2026-06-15
- [`curio-retrospect-dispatch/plugin.md`](file:///home/canewiw/gt/plugins/curio-retrospect-dispatch/plugin.md) — cron gate, four-guard stack, digest-path sandbox contract, positional sling, town-level placement rationale, curio-proposer resolution — accessed 2026-06-15
- [`curio-retrospect-dispatch/run.sh`](file:///home/canewiw/gt/plugins/curio-retrospect-dispatch/run.sh) — `go run ./cmd/curio-proposer` source-discovery fallback, digest render path — accessed 2026-06-15
- [`mol-triage-retro.formula.toml`](file:///home/canewiw/gt/.beads/formulas/mol-triage-retro.formula.toml) + [`bin/triage-retro-analyze`](file:///home/canewiw/gt/talontriage/refinery/rig/bin/triage-retro-analyze) — Python deterministic-tool precedent, `validate-run-dir` guard, `dry_run`/`budget_usd` vars — accessed 2026-06-15
- [`scripts/guards/curio-proposal-target-guard.sh`](file:///home/canewiw/gt/gastown_upstream/mayor/rig/scripts/guards/curio-proposal-target-guard.sh) — air-gap guard pattern, merge-queue gate wiring via `gt rig settings set` — accessed 2026-06-15
- [`docs/design/formula-drift-reconciliation.md`](file:///home/canewiw/gt/gastown_upstream/polecats/chrome/gastown_upstream/docs/design/formula-drift-reconciliation.md) — 3-tier resolution (rig > town > embedded), embedded set is single source of truth, `ProvisionFormulas`/`UpdateFormulas` provisioning + drift cadence — accessed 2026-06-15
- [`plugins/auto-dispatch/plugin.md`](file:///home/canewiw/gt/plugins/auto-dispatch/plugin.md) — agent-owned-bead skip rule (proposal beads must be `--assignee mayor`), `type:plugin-run` feedback-loop precedent, `*-wfs-*` workflow-step exception — accessed 2026-06-15
- [`plugins/rebuild-gt/plugin.md`](file:///home/canewiw/gt/plugins/rebuild-gt/plugin.md) — binary staleness / forward-only rebuild, source-rig discovery, town-checkout sync — accessed 2026-06-15
- `~/gt/rigs.json` (verified `version:1`, `rigs.<name>.{git_url, beads.prefix}`) — rig registry for `rig_name` validation — accessed 2026-06-15
- gastown source repo layout — verified `internal/formula/formulas/mol-curio-retrospect.formula.toml`, `cmd/curio-proposer`, `internal/curio/*`, `plugins/curio-retrospect-dispatch/*` all git-tracked in source; `artifacts/` gitignored (`.gitignore:149`) — accessed 2026-06-15
- Sibling design legs `.designs/5t33y/{data,api,security,ux}.md` — cross-referenced for shared types, constraints, and open questions — accessed 2026-06-15

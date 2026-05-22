# Data Model Design

## Summary

`auto-test-pr` v1 (Refinery-only on `gastown_upstream`, Go-only) needs
**five distinct data surfaces**, each with a different lifecycle and
authority. Most state already has a natural home: beads (Dolt) for
durable, transactional state and audit trail; the existing rig
config/settings JSON for opt-in toggles; in-repo files for human-
authored conventions and code-level markers. **No new database, no
new SQLite, no new TOML schema** is required for v1 — the cost of
adding a new storage substrate dwarfs the cost of fitting state into
the substrates we already operate. The only fundamentally new schema
is the **pinned state bead** (`<rig>-auto-test-state`), and that is a
single bead-per-rig whose `metadata` JSON blob carries a versioned,
typed payload.

The hardest data-model decisions are concentrated in the **pinned
state bead's metadata schema** (because it doubles as a CAS lock per
Q7 *and* an audit trail per Q6's SEV-3 reject-rate calculation), and
in **what is in-repo vs. out-of-repo** (rig config in repo means
write-access-to-repo == enable-the-feature, which is a privilege-
escalation primitive per gaps.md #2-#3 — Q4's language-keyed allow-
list neutralizes the *command* exposure but the *enable bit* still
needs an authority answer). The other data — language allow-list
table, conventions sheet, code-level markers, PR body banner,
branch-name convention — is straightforward and slots into existing
patterns.

## Analysis

### Key Considerations

- **Substrate already exists.** Beads (Dolt) is the persistent,
  transactional, mayor-/system-owned store; rig config (JSON) is the
  per-rig settings layer; in-repo markdown/comments cover human-
  authored content. v1 must not add a sixth thing to operate.
- **gu-gal8 is structural, not advisory.** Polecats cannot create
  bookkeeping beads. The state-bead, the cycle-history records, the
  cooldown counters, and the rejection log are all Mayor-owned.
  Polecats *read* state and *transition* state (via Mayor-mediated
  RPC/CLI), but never *create* the bookkeeping.
- **The pinned state bead is the system's single source of truth for
  Q7's "PR open?" question** in Refinery mode (where there is no
  GitHub PR to query). It must support compare-and-set, must record
  every transition with timestamp + actor, and must survive Dolt
  restarts cleanly.
- **PR-author identity collapses in v1.** Q3 + Q1 together mean v1 is
  Refinery-only with polecat-as-commit-author. No GitHub App, no
  shared bot user, no PAT — and therefore no credential to store. The
  v2 GitHub-App schema is sketched below for forward-compat but does
  NOT ship in v1.
- **Per-file cooldown / skip-list** (Q4 from prd-draft + OQ7) is the
  only piece of historical data that grows unboundedly with PR count.
  Bound it by rig (≤200 entries per rig) with FIFO eviction; older
  rejections decay out. Don't try to make this a permanent skip-list
  — Goal 6 says "marginal value" not "perfect targeting."
- **Schema evolution is real.** This is v1; v2 will add external-PR
  mode (Q1), GitHub App identity (Q3), and possibly auto-extracted
  conventions (Q5). Every persisted record gets a `schema_version`
  field; readers tolerate forward-version blobs (skip unknown fields).
- **Most "data" is computed, not persisted.** Coverage profile, churn
  ranking, target candidates, mutation-sanity verdicts — all
  recomputed every cycle. The only thing we cache is what we *cannot*
  afford to recompute (current state-machine state, recent rejection
  history for cooldown logic, last-cycle timestamp for cadence).
- **Audit trail is not optional.** Q6 SEV-2 (secrets in PR) and SEV-3
  (>50% reject rate) are both data-driven, not vibes-driven. The
  state bead's transition log IS the audit trail.

### Options Explored

#### Option 1: Single pinned state bead per rig (RECOMMENDED)

- **Description**: One bead per opted-in rig, ID
  `<rig>-auto-test-state`, type=`pinned`, owned by Mayor. The bead's
  `metadata` JSON blob holds the full state machine, transition log
  (last N=50), per-file rejection cooldowns, and last-cycle timestamp.
  All updates go through a Dolt transaction (compare-and-set on the
  state field). Standard bead-show / bead-list / bead-update tooling
  works without modification.
- **Pros**:
  - Reuses gu-gal8-aligned pattern (Mayor-owned pinned bead) already
    in the system.
  - Dolt transactions provide CAS for free per Q7.
  - bd show works out of the box for human inspection.
  - One bead per rig keeps blast radius per failure tiny — corrupt
    one rig's state, others unaffected.
  - Schema-evolution friendly: metadata is a free-form JSON blob with
    `schema_version`.
- **Cons**:
  - Metadata blob grows with transition log + rejection cooldowns;
    must be bounded (last-50 transitions, last-200 rejections).
  - Querying "rejection rate across all rigs in last 7d" requires
    scanning every rig's bead — fine at our scale (<100 rigs), would
    not be at 1000+.
  - One blob per rig means the circuit breaker (Q6, "3 consecutive
    closes town-wide in 7d") needs a *town-wide* counter that doesn't
    naturally live on any one rig's bead.
- **Effort**: Low. The bead substrate exists; we add a JSON schema.

#### Option 2: One state bead per rig + a town-wide circuit-breaker bead

- **Description**: Option 1, plus a single Mayor-owned bead `town-
  auto-test-pr-state` holding the town-wide circuit-breaker counter,
  the global pause flag, and a denormalized index of which rigs are
  currently in `mr-pending` for fast `gt auto-test-pr status` output.
- **Pros**:
  - Q6's town-wide pause-all and circuit breaker have a natural home.
  - `gt auto-test-pr status` reads one bead, not N rig beads.
  - Town-wide audit trail is centralized.
- **Cons**:
  - Two write surfaces to keep consistent. The denormalized index
    (per-rig open-MR state) on the town bead can drift from the
    actual per-rig beads if a transaction fails between the two
    writes. Mitigation: per-rig bead is authoritative; town bead is
    cache-only and rebuilt opportunistically.
  - Slightly more code.
- **Effort**: Low-Medium. ~50 LOC over Option 1.

#### Option 3: SQLite-or-equivalent dedicated store

- **Description**: A new `.gt/auto-test-pr/state.db` or similar with
  proper tables (`rigs`, `cycles`, `rejections`, `cooldowns`,
  `transitions`).
- **Pros**:
  - Real relational queries (rejection rate, cooldown, etc.).
  - Easy schema migrations via standard tooling.
- **Cons**:
  - **Adds a new database to the operational surface** — backups,
    migrations, corruption recovery, all separate from beads.
  - Violates the "don't add storage substrates" principle from
    consideration #1.
  - Beads already provides everything we need at our scale.
  - Doubles the surface for "where does state live?" — every future
    feature has to decide beads vs. this.
- **Effort**: High. ~500+ LOC, plus operational overhead forever.

#### Option 4: Pure GitHub-as-source-of-truth (no persisted state)

- **Description**: Re-derive everything from GitHub on every cycle —
  open PRs, recent merges, rejection history, etc.
- **Pros**:
  - Zero persisted state to corrupt or migrate.
- **Cons**:
  - **Doesn't work for Refinery mode** (Q1: v1 is Refinery-only,
    where there *is* no GitHub PR for the cycle to read).
  - Even in v2 external-PR mode, GitHub API rate limits and the cost
    of "scan the last 200 PRs to compute rejection rate" make this
    impractical at cadence.
  - Q6's SEV-3 reject-rate calculation requires a stable historical
    record beyond GitHub's rate limits.
- **Effort**: Low to implement, High to operate.

### Recommendation

**Option 2 (per-rig pinned state bead + town-wide circuit-breaker
bead).** This is Option 1 plus a single town bead for global concerns
that don't fit a per-rig view. Deltas vs. Option 1 are small (~50
LOC, one extra Mayor write per cycle) but pay back immediately on Q6
(town-wide pause, circuit breaker, status command).

#### Concrete schema

**Pinned state bead (one per opted-in rig)**

ID: `<rig>-auto-test-state` (e.g., `gu-auto-test-state`)
Type: `pinned`
Owner: Mayor (NEVER polecat per gu-gal8)
Status: stays `open` for the lifetime of the rig's opt-in
Metadata (`Issue.Metadata`, JSON blob):

```json
{
  "schema_version": 1,
  "rig": "gastown_upstream",
  "state": "idle",
  "language": "go",
  "cadence_days": 7,
  "last_cycle_at": "2026-05-21T14:23:00Z",
  "last_cycle_outcome": "merged",
  "current_cycle": null,
  "transitions": [
    {
      "from": "mr-pending",
      "to": "cooled-down",
      "at": "2026-05-21T14:23:00Z",
      "actor": "refinery",
      "context": {"mr_id": "gu-mr-abc12", "merged_sha": "abc1234"}
    }
    // ... last 50, FIFO eviction
  ],
  "rejections": [
    {
      "file": "internal/foo/bar.go",
      "rejected_at": "2026-05-19T10:00:00Z",
      "reason": "wrong-target",
      "cooldown_until": "2026-06-02T10:00:00Z"
    }
    // ... last 200, FIFO eviction
  ],
  "consecutive_closes": 0,
  "paused_until": null
}
```

`current_cycle` is non-null while state ∈ {`picking`, `dispatched`,
`mr-pending`, `mr-revising`}:

```json
{
  "cycle_id": "gu-cycle-xyz98",
  "started_at": "2026-05-21T12:00:00Z",
  "polecat_bead": "gu-leg-abc12",
  "mr_bead": "gu-mr-abc12",
  "branch": "auto-test/gastown_upstream/gu-cycle-xyz98",
  "targets": [
    {"file": "internal/foo/bar.go", "lines_uncovered": [42, 51, 89]}
  ]
}
```

States (per Q7, with v1-only set):
`idle | picking | dispatched | mr-pending | mr-revising | cooled-down`

Transitions: see Q7 in prd-draft.md §Clarifications. Every transition
appends to `transitions[]`; the head N=50 is retained, older entries
are dropped.

**Town-wide bead (one, shared)**

ID: `town-auto-test-pr-state`
Type: `pinned`
Owner: Mayor
Metadata:

```json
{
  "schema_version": 1,
  "global_pause_until": null,
  "circuit_breaker": {
    "consecutive_closes_townwide": 0,
    "window_started_at": null,
    "tripped_until": null
  },
  "enabled_rigs": ["gastown_upstream"],
  "rig_summary": {
    "gastown_upstream": {
      "state": "idle",
      "last_cycle_at": "2026-05-21T14:23:00Z",
      "last_outcome": "merged"
    }
  }
}
```

`rig_summary` is a denormalized read-cache for `gt auto-test-pr
status`. Per-rig pinned beads are authoritative; town summary is
rebuilt opportunistically on each rig transition. Drift is non-fatal
(human gets slightly stale `status` output until next tick).

**Rig config (extends existing per-rig settings JSON)**

File: `<rig>/settings/config.json` (existing `RigSettings` struct,
internal/config/types.go:687).

Extension: add an `AutoTestPR` pointer field.

```go
type RigSettings struct {
    // ... existing fields ...
    AutoTestPR *AutoTestPRConfig `json:"auto_test_pr,omitempty"`
}

type AutoTestPRConfig struct {
    Enabled       bool   `json:"enabled"`               // default false
    Language      string `json:"language"`              // "go" only in v1
    CadenceDays   int    `json:"cadence_days,omitempty"` // default 7
    ConventionsPath string `json:"conventions_path,omitempty"` // default ".gt/auto-test-pr/conventions.md"
    SkipDirs      []string `json:"skip_dirs,omitempty"`   // out-of-scope dirs (vendored, generated)
    // NOTE: no test_cmd, coverage_cmd, lint_cmd in v1 per Q4
    // (language-keyed allow-list lives in code, not config)
}
```

**Language allow-list table (in code, not config)**

`internal/autotestpr/languages.go`:

```go
type LanguageCommands struct {
    TestCmd     []string // e.g., ["go", "test", "-coverprofile=cover.out", "./..."]
    CoverageCmd []string // sometimes same as TestCmd with extra flag
    VetCmd      []string // e.g., ["go", "vet", "./..."]
    LintCmd     []string // e.g., ["golangci-lint", "run"]; nil if absent
}

var LanguageAllowList = map[string]LanguageCommands{
    "go": { /* per Q4 */ },
    // typescript, python deferred to v2
}
```

This is **code, not data** — privileged because it requires a CR
review, blocked by Refinery, signed off by Overseer per Q4 deferral
language. No data-model implication beyond "the rig config holds a
language *key*, not a *command*."

**Conventions sheet (in-repo, source-controlled)**

Path: `.gt/auto-test-pr/conventions.md` (per Q5).
Format: Free-form markdown, human-edited, committed to the rig's
repo. Polecat reads it as part of its dispatch payload — it does not
*mutate* it.
Schema: None enforced. Sections suggested in PRD (test framework,
fixture loaders, factory funcs, common mocks, anti-patterns) but
template only.

**Code-level marker (in-repo, source-controlled)**

Per Q2 + "Promoted to MUST" §, every auto-test-pr-generated test
function carries a leading comment:

```go
// gt:auto-test-pr origin=gu-cycle-xyz98 covers=internal/foo/bar.go:42
func TestFoo_NilInputReturnsError(t *testing.T) { ... }
```

This is structured for grep / future tooling. Two structured fields:
`origin=<cycle-id>` and `covers=<file>:<line>`. The marker IS the
code-level audit trail — it survives PR edits, branch GC, and bead
expiration.

**Branch name convention**

`auto-test/<rig>/<cycle-bead-id>`, e.g.,
`auto-test/gastown_upstream/gu-cycle-xyz98`. This is ephemeral
state — branches are GC'd after 7d-with-no-PR per the promoted-to-
MUST list.

**MR bead (Refinery merge queue submission)**

Refinery already has its own MR bead schema; no changes needed. The
auto-test cycle's MR bead links to the cycle bead via standard bead
dependency. Adds a `gt:auto-test-pr` label so existing patrols can
identify it.

#### Lifecycle summary

| Data | Substrate | Lifecycle | Authority |
|---|---|---|---|
| Pinned state bead | Beads / Dolt | One per opted-in rig, persists for opt-in duration | Mayor only |
| Town circuit-breaker bead | Beads / Dolt | One, town-wide | Mayor only |
| Rig config (`AutoTestPR`) | `<rig>/settings/config.json` | Per-rig, edited by `gt rig config <rig> auto-test-pr enable` | Rig owner via gt CLI (NOT in-repo) |
| Conventions sheet | In-repo `.gt/auto-test-pr/conventions.md` | Per-rig, source-controlled | Rig maintainers via PR review |
| Language allow-list | Hardcoded in `internal/autotestpr/languages.go` | Town-wide, ships with the binary | Town developers via Refinery CR |
| Code marker | In-repo source files | Per-test, lives with the test forever | Polecat writes it, humans review |
| Branch name | Ephemeral remote ref | Until merge or 7d-stale GC | Polecat creates, branch-GC patrol cleans |
| MR bead | Beads / Dolt | Standard Refinery bead lifecycle | Standard |
| Cycle / polecat hook bead | Beads / Dolt | Created by Mayor at dispatch, closed by polecat at gt-done | Mayor creates, polecat closes |

#### Compare-and-set semantics (Q7)

Cycle fire sequence:

1. Mayor reads `<rig>-auto-test-state` bead within a Dolt transaction.
2. If `state != "idle"` or `paused_until > now` → exit.
3. Mayor updates `state` from `idle` to `picking`, appends transition
   record, commits the transaction. If commit fails (concurrent
   write), exit.
4. Mayor proceeds with target-pick, dispatches polecat, transitions
   `picking` → `dispatched`.

This works because Dolt's MySQL frontend gives us SERIALIZABLE-class
isolation on row updates. The state bead is one row; the read-modify-
write is one transaction. No new locking primitive needed.

#### Schema evolution

Every JSON blob carries `schema_version`. Readers honor the v1
version exactly; readers seeing a higher version log a warning and
treat unknown fields as opaque (don't drop them on rewrite — round-
trip through `Metadata json.RawMessage`). This pattern is already
used elsewhere (e.g., `RigsConfig.Version`). v2 introduces a state
machine extension for external-PR mode (`pr-pending`, `pr-revising`,
new transition actors); the v1 reader's tolerance for unknown states
keeps it from crashing if it observes a v2 bead during an upgrade.

Migration from v1 → v2:
- New states are appended; existing states keep their semantics.
- New metadata fields are additive.
- A v2 reader, when first observing a v1 blob, fills in defaults for
  new fields and rewrites with v2 schema_version on next transition.

## Constraints Identified

- **gu-gal8 forbids polecat-owned bookkeeping beads.** All pinned
  beads (per-rig, town-wide) and all transition writes are Mayor-
  authored. Polecats request transitions via the existing Mayor RPC
  / CLI surface; they do not write the bead directly.
- **No new database in v1.** All persistent state lives in beads
  (Dolt) or extends existing per-rig settings JSON.
- **Rig config lives in `settings/config.json`, not in-repo.** Per
  gaps.md #2 and the Q3/Q4 decisions, the enable bit is an authz
  primitive. Putting it in `.beads/` or repo TOML means write-access-
  to-repo == enable-the-feature, which we explicitly do not want.
  Settings JSON is operated by `gt rig config <rig> ...` and is
  scoped to humans with mayor/town-admin authority.
- **Language allow-list is code, not config** (Q4). Adding a language
  is a CR. Adding a command flavor for an existing language is a CR.
- **State bead is one-per-rig.** No multi-cycle parallelism on a
  single rig; the state machine is a strict serial pipeline.
- **Cycle-bead and MR-bead are standard beads** — no new bead types.
  v1 only adds two pinned-bead instances (per-rig state + town
  state). No new schemas in `internal/beads/`.
- **Pre-push gitleaks scan is a v1 MUST gate** (Q6 SEV-2 promotion):
  the polecat's emitted test files MUST pass gitleaks before push.
  This is execution, not data — but it implies the cycle bead
  records gitleaks pass/fail in its closure notes for SEV-2 audit.
- **Bounded history.** Transition log: last 50 entries per rig.
  Rejection log: last 200 entries per rig. FIFO eviction. The
  oldest rejection's `cooldown_until` may have already elapsed,
  which is fine — eviction does not re-open cooldown windows.

## Open Questions

These need cross-dimension or human input:

1. **Pinned-bead Metadata field reliability — RESOLVED (FAIL → fallback adopted).**
   The Phase 0a-3 spike (`gu-g9ufm`,
   `internal/cmd/metadata_reliability_integration_test.go`) PASSED
   sequential round-trip fidelity at ~7-8KB but FAILED concurrent CAS
   (~60/100 lost-update under the production RMW pattern). Per the
   prerequisite bead `gu-2s03`, the **transition log and rejection
   log have moved to attachment beads** (one bead per entry, labeled
   `gt:auto-test-pr-attachment` + `kind:{transition,rejection}` +
   `rig:<rig>`, depends-on the parent state bead). The pinned
   `<rig>-auto-test-state` bead's `Issue.Metadata` retains only
   single-writer fields (`schema_version`, `state`, `current_cycle`,
   `last_cycle_at`, `last_cycle_outcome`, `paused_until`,
   `incidents[]≤20`); the multi-writer logs are no longer in the
   blob, so the lost-update class is sidestepped entirely. See
   `.designs/auto-test-pr/synthesis.md` §Data Model "OQ4 fallback"
   for the full schema, materialize-from-attachments read path, and
   retention rules. The data-model schema in this file (§Concrete
   schema, "Pinned state bead", lines ~165-205) is **superseded by
   that synthesis subsection** — the in-blob `transitions[]` and
   `rejections[]` arrays it shows are NOT what v1 ships.

2. **Where does `AutoTestPRConfig` actually live in v1?** Two options:
   (a) extend `RigSettings` (the per-rig `settings/config.json`,
   already operated by `gt rig config`), or (b) a new top-level
   `gt auto-test-pr config` subsystem with its own JSON file. Option
   (a) reuses authz semantics; option (b) makes the feature easier
   to gate at the CLI surface but adds a file. Recommend (a); flag
   for UX/API leg confirmation.

3. **Town-wide bead conflict with existing town beads.** Does
   `town-auto-test-pr-state` collide with any existing
   `town-...`-prefixed bead conventions? Need to scan
   `internal/beads/` for naming. Also: in a multi-town federation,
   is "town-wide" actually town-wide or rig-cluster-wide?

4. **Circuit-breaker counter race.** Multiple rigs closing PRs in
   the same minute could interleave their writes to the town bead's
   `consecutive_closes_townwide` counter. Q7's CAS solves this for
   per-rig beads but the town bead's counter is touched by every
   rig's transition. Need to either serialize via the same Dolt
   transaction surface, or accept that the counter is best-effort
   (a small +1/-1 race tolerance, since the threshold is "3
   consecutive closes" — off-by-one is operationally acceptable).
   Flag for scale leg.

5. **Conventions-sheet absence.** What does the polecat do if
   `.gt/auto-test-pr/conventions.md` is missing on the pilot rig?
   Refuse to run? Run with no conventions and use generic Go test
   patterns? Per Q5 the pilot rig is required to ship one, but the
   spec needs a hard error vs. soft fallback decision.

6. **Rejection record privacy / size.** The rejection record carries
   the *file path* of rejected targets. If a rig's source tree
   contains internal-only paths, that's leaked into the bead — which
   is replicated via Dolt to other agents in the town. For v1 (one
   pilot rig, internal repo), this is fine. For v2 with external
   rigs, may need to anonymize. Flag for security leg.

7. **What if a rig's opt-in is revoked while state ≠ `idle`?** The
   spec says "Any in-flight PR is left alone" (S6). Translating to
   data: do we leave the pinned state bead in `mr-pending` forever,
   or transition it to `cooled-down` immediately and let the human
   close the PR? Recommend: on opt-out, set
   `paused_until = "9999-12-31T00:00:00Z"`, leave state untouched.
   On re-opt-in, clear the pause and let the next tick read state
   normally. The MR continues to live in Refinery's MQ and merges
   or is cancelled by humans.

## Integration Points

- **API & Interface (api leg).** `gt auto-test-pr status` reads the
  town bead's `rig_summary`. `gt auto-test-pr pause` writes the
  town bead's `global_pause_until` or a per-rig bead's
  `paused_until`. `gt rig config <rig> auto-test-pr enable` writes
  to `RigSettings.AutoTestPR`. The CLI surface is thin — it's
  read/write of these data structures.

- **UX (ux leg).** The pinned state bead's transition log IS the
  audit feed surfaced to humans (`gt auto-test-pr status --rig=...
  --history`). Schema needs to be human-readable enough that
  `bd show <rig>-auto-test-state` is useful without tooling.

- **Scale (scale leg).** Bounded histories (50 transitions, 200
  rejections per rig) keep blob size O(rig-count). At 100 rigs this
  is ~500KB total state, all in beads / Dolt. The town bead's
  `rig_summary` denormalization is the one scaling risk if rig
  count grows past ~1000 — at that point, switch to query-on-demand.

- **Security (security leg).** The rig config's enable bit is an
  authz primitive (gaps.md #2-3); it MUST live in
  `settings/config.json` and NOT in the repo. The language allow-
  list (Q4) is code, not data. Cycle bead notes record gitleaks
  pass/fail for SEV-2 audit.

- **Integration (integration leg).** No new storage substrate, no
  new bead types — extends existing `RigSettings`, adds two
  pinned-bead conventions. Backwards compatibility: rigs without
  `AutoTestPR` field default to disabled. Schema versioning via
  `schema_version` allows v2 forward compat.

- **API (api leg) again — code-level marker.** `// gt:auto-test-pr
  origin=<cycle-id> covers=<file>:<line>` is structured: a future
  `gt auto-test-pr trace <bead-id>` could grep the codebase for
  origin markers and surface "tests this cycle wrote." Treat the
  marker grammar as a stable v1 contract.

- **PR feedback patrol.** The existing `mol-pr-feedback-patrol`
  identifies auto-test PRs via the `gt:auto-test-pr` label. When
  it dispatches a revision polecat, it transitions the rig's state
  bead from `mr-pending` to `mr-revising` (and the polecat
  transitions back on completion). No new patrol-side data; just a
  state-bead update.

- **Refinery.** Refinery's merge handler observes auto-test MR
  closures (merged or rejected) and transitions the state bead from
  `mr-pending`/`mr-revising` to `cooled-down`. Merged → no
  rejection record. Closed-unmerged → append to rejection log,
  increment `consecutive_closes`. This is the single integration
  point Refinery owns; everything else goes through Mayor.

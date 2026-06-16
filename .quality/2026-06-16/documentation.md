# Documentation Coverage Audit

## Summary

The documentation surface of gastown remains broadly healthy. The core
onboarding path in `README.md` (`gt install` → `gt config agent list` →
`gt mayor attach`) is accurate, install commands resolve to real
subcommands, the `docs/` design table links all resolve, comment hygiene is
excellent (a single `TEMPORARY` marker repo-wide, no aged TODO/FIXME, no
comment-vs-code contradictions found in a wide sample), and the large
majority of central public packages — `witness`, `polecat`, `formula`,
`convoy`, `sling`, `dispatch`, `config`, `mail`, `doltserver`, `acp`,
`crew`, `boot`, `deacon` — carry package-level doc comments with full or
near-full exported-symbol coverage. Notably, `acp` (flagged in the prior
2026-06-04 audit as lacking a package doc) now has one.

The defects cluster in three themes. First, **the README's flagship formula
example and two Quick Start commands reference things that do not exist** —
a `release.formula.toml`/`scripts/publish.sh` that were never created, plus
a `--human` flag on `gt convoy create` that is not defined. A new contributor
who copy-pastes these hits "file/flag not found" at exactly the
"learn the core abstraction" moment. Second, **the `RELEASING.md` runbook's
recommended Option A `cd` target is wrong** (`mayor/rig/` has no `scripts/`).
Third, **a handful of broken intra-repo doc cross-links** and one core
package (`mayor`) still lacking a package doc with ~17 undocumented exported
ACP-lifecycle symbols. None of these break the first-run *install* path, so
the overall state is "good with isolated, fixable staleness" — essentially
unchanged from the prior audit, with one prior finding resolved (`acp`) and
one new onboarding defect surfaced (`--human`).

## Score

score: 0.75

## Critical Findings (P0 — file as beads, fix urgently)

_None._ The primary onboarding/install path verifies correctly: `gt install`,
`gt config agent list`, and `gt mayor attach` all resolve to real commands,
and the documented install methods (`brew`, `npm`, `go install`, clone+make)
are coherent. No document a first-time user *must* follow to get running is
broken.

## Major Findings (P1 — track but do not auto-bead)

- **README "Example Formula" references files and commands that do not exist.**
  - **Location**: `README.md:291` (the worked example through `:330`, plus the
    execute commands at `:339`/`:342`)
  - **Detail**: The "Beads Formula Workflow" section presents
    `internal/formula/formulas/release.formula.toml` as the canonical example
    and its body invokes `./scripts/publish.sh`. Verified: no
    `release.formula.toml` exists in `internal/formula/formulas/` (no
    `*release*` file at all), and `scripts/publish.sh` does not exist (only
    `scripts/bump-version.sh` is present). The follow-on examples
    `bd cook release --var version=1.2.0` (`:339`) and
    `bd mol pour release ...` (`:342`) therefore reference a `release` formula
    not embedded in the binary.
  - **Impact**: This is the README's flagship illustration of the formula
    system; a reader who copy-pastes it gets errors at the "learn the core
    abstraction" moment, undermining trust in the docs.
  - **Suggested fix**: Replace the example with a formula that actually ships
    (e.g. `code-quality.formula.toml`, `design.formula.toml`, or
    `tdd-cycle.formula.toml` in `internal/formula/formulas/`), or add a real
    `release.formula.toml`. Drop the `scripts/publish.sh` step.
  - **Note**: Carried over from the 2026-06-04 audit — still unfixed.

- **README Quick Start uses a `--human` flag that `gt convoy create` does not define.**
  - **Location**: `README.md:232` (`gt convoy create "Feature X" gt-abc12 gt-def34 --notify --human`)
    and `README.md:351` (`gt convoy create "Bug Fixes" --human`)
  - **Detail**: `gt convoy create`'s flag set (defined in
    `internal/cmd/convoy.go:472-483`) is `--molecule`, `--owner`, `--notify`,
    `--owned`, `--merge`, `--base-branch`, `--from-epic`. There is no `--human`
    flag on the command, on `convoyCmd`'s parent, or as a root persistent flag
    (grep for a `"human"` flag definition returns only string literals like
    `Sender: "human"`, never a Cobra flag). Cobra will reject the command with
    "unknown flag: --human".
  - **Impact**: Both copy-paste examples in the Quick Start / Common Workflows
    sections fail outright. This is the second-most-prominent worked example a
    new user encounters, right after install.
  - **Suggested fix**: Remove `--human` from both example lines, or — if a
    human-owned-convoy concept is intended — wire it to the existing `--owned`
    flag (which marks the convoy caller-managed) and update the text
    accordingly.
  - **Note**: NEW this cycle (not in the 2026-06-04 audit).

- **`RELEASING.md` Option A `cd` target is wrong.**
  - **Location**: `RELEASING.md:17`
  - **Detail**: Option A (labelled "recommended") says `cd gastown/mayor/rig`
    then `./scripts/bump-version.sh X.Y.Z ...`. Verified: `mayor/rig/` contains
    only `.gitignore` and `.kiro/` — there is no `mayor/rig/scripts/`
    directory. The `bump-version.sh` script lives at the repo root
    (`scripts/bump-version.sh`), which is exactly what Option B
    (`RELEASING.md:22`) correctly invokes. The two options contradict each
    other and the recommended one is the broken one.
  - **Impact**: A releaser following the recommended path gets
    "no such file or directory."
  - **Suggested fix**: Change Option A's `cd` to the repo root (drop
    `mayor/rig`), or merge Option A into the correct Option B invocation.
  - **Note**: Carried over from the 2026-06-04 audit — still unfixed.

- **Broken intra-repo doc cross-links (3 distinct missing targets, 7 references).**
  - **`docs/design/convoy/testing.md` — does not exist.** The real file is
    `docs/design/convoy/stage-launch/testing.md` (the references drop the
    `stage-launch/` segment). Referenced at:
    `docs/skills/convoy/SKILL.md:362`, `docs/design/convoy/spec.md:387`,
    `docs/design/convoy/spec.md:388`. (Note: `SKILL.md:360` correctly uses the
    `stage-launch/` path, so the file knows the right location.)
  - **`docs/design/daemon/convoy-manager.md` — does not exist** (no
    `docs/design/daemon/` directory, no `*convoy-manager*` file anywhere).
    Referenced at `docs/design/convoy/spec.md:385`, `:386`, and in the File-map
    table at `:585`. No obvious replacement — the doc appears unwritten.
  - **`docs/agent-as-bead.md` — does not exist** (no such file repo-wide).
    Referenced in the "Related Documents" list at
    `docs/design/mail-protocol.md:575`.
  - **Impact**: Readers following design-doc cross-references hit dead ends; the
    `convoy-manager.md` references in particular are cited as the authoritative
    source for the daemon polling architecture.
  - **Suggested fix**: Repoint the `testing.md` references to
    `stage-launch/testing.md`; either write or remove the `convoy-manager.md`
    and `agent-as-bead.md` references.

- **Core package `internal/mayor` lacks a package-level doc comment and leaves ~17 exported symbols undocumented.**
  - **Location**: `internal/mayor/cleanup.go` (the ACP PID/agent lifecycle
    cluster), `manager.go`, `process.go`
  - **Detail**: No `// Package mayor ...` comment exists in any non-test file
    (verified: `grep '^// Package mayor'` returns nothing), unlike the ~158
    files repo-wide that have package docs. The ACP-lifecycle API in
    `cleanup.go` is almost entirely undocumented:
    `ACPPidFilePath` (`:23`), `WriteACPPid` (`:27`), `RemoveACPPid` (`:41`),
    `GetACPPid` (`:49`), `ACPAgentFilePath` (`:67`), `WriteACPAgent` (`:71`),
    `GetACPAgent` (`:92`), `IsACPActive` (`:102`), `IsACPActiveInWorkDir`
    (`:117`), `CleanupVetoChecker` (`:125`), `NewCleanupVetoChecker` (`:129`),
    `ShouldVetoCleanup` (`:144`), `CleanupStaleACP` (`:167`), and the exported
    `ErrCleanupVetoed` var (`:20`). A `// ACP agent name persistence functions`
    section banner at `:65` is not a godoc doc comment.
  - **Impact**: `mayor` owns session/PID/cleanup lifecycle — a contributor
    reading `go doc github.com/steveyegge/gastown/internal/mayor` gets bare
    signatures with no overview for a central infra package.
  - **Suggested fix**: Add a `// Package mayor ...` comment and doc the exported
    PID/agent lifecycle and veto-checker symbols in `cleanup.go`.
  - **Note**: Carried over from 2026-06-04 (the companion `acp` finding is now
    RESOLVED — `acp` has a package doc).

## Minor Findings (P2 — informational)

- **Scattered small exported-doc gaps in otherwise well-documented packages.**
  All of the following packages DO have a package doc, so these are incremental
  one-line cleanups, not core gaps:
  - `internal/refinery/engineer.go:96` — exported `type GateConfig` undocumented.
  - `internal/doltserver/wl_commons.go` — `QueryStampsForSubject` (`:124`),
    `QueryBadges` (`:127`), `QueryAllSubjects` (`:130`), `UpsertLeaderboard`
    (`:133`).
  - `internal/daemon/poller_dog.go` — `ListPollers` (`:76`), `StartPoller`
    (`:79`), `RemoveStalePIDFile` (`:82`).
  - `internal/beads` — `ForAgentBead` (`beads.go:870`), `EnvForSubprocessMode`
    (`exec.go:47`), `SubprocessModeForArgs` (`exec.go:62`).
  - `internal/witness/handlers.go:5252` — `Show` (method on a large file; worth
    a one-liner).
  - (`Error`/`Unwrap` methods in `polecat/manager.go` and `hooks/config.go`, and
    the unexported `*PRProvider` interface-impl methods in `refinery`, are
    idiomatic to leave uncommented — not flagged.)

- **Absolute `~/gt/docs/...` references that resolve to nothing in this
  checkout.** `docs/design/federation.md:46`
  (`~/gt/docs/hop/GRAPH-ARCHITECTURE.md`), `docs/concepts/identity.md:248`
  (`~/gt/docs/hop/decisions/008-identity-model.md`),
  `docs/design/property-layers.md:411` (`~/gt/docs/hop/PROPERTY-LAYERS.md`),
  and `docs/contrib-harnesses/README.md:14` (`~/gt/docs/PRIMING.md`) all point
  under `~/gt/docs/`, which does not exist in this rig checkout. These are
  likely intentional references to the sibling town-root docs tree rather than
  repo-local files — flagged as uncertain. If repo-local, make them
  repo-relative; if town-root, say so explicitly. (Carried over from
  2026-06-04.)

- **Positive note (no action):** Comment hygiene remains a strength. A repo-wide
  scan of `internal/`, `cmd/`, and `plugins/` found a single marker (a
  `// ... TEMPORARY index ...` explanatory comment in
  `internal/daemon/checkpoint_dog.go:156`, which describes intended behavior,
  not debt) and no TODO/FIXME/XXX/HACK aged past 90 days. No comment was found
  to contradict its code in a wide spot-check.

## Counts

  counts: critical=0 major=5 minor=3

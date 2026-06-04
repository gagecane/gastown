# Documentation Coverage Audit

## Summary

The documentation surface of gastown is broadly healthy. The core onboarding
path in `README.md` (install → `gt config` → `gt mayor attach`) is accurate, the
`docs/` tree is large and well-maintained, comment hygiene is excellent (no aged
TODO/FIXME markers and no comment-vs-code contradictions found across a wide
sample), and the large majority of central public packages — `witness`,
`polecat`, `formula`, `mail`, `doltserver`, `config`, `convoy`, `sling` — have
100% doc-comment coverage on exported symbols.

The defects cluster in two themes. First, a worked **release/formula example in
the main README and the `RELEASING.md` runbook reference files and commands that
do not exist** — a new contributor who copy-pastes them will hit "file not found"
or a missing formula. Second, a handful of **broken intra-repo doc cross-links**
and two **core infrastructure packages (`acp`, `mayor`) that lack package-level
doc comments** alongside undocumented exported functions. None of these break the
first-run install experience, so the overall state is "good with isolated,
fixable staleness."

## Score

score: 0.74

## Critical Findings (P0 — file as beads, fix urgently)

_None._ The primary onboarding/install path verifies correctly; no document that
a first-time user must follow to get running is broken.

## Major Findings (P1 — track but do not auto-bead)

- **README "Example Formula" references files and commands that do not exist.**
  - **Location**: `README.md:291` (and the worked example through `:330`, plus
    the execution commands at `:339` and `:342`)
  - **Detail**: The "Beads Formula Workflow" section presents
    `internal/formula/formulas/release.formula.toml` as the canonical example,
    and its body invokes `./scripts/bump-version.sh` and `./scripts/publish.sh`.
    Verified: no `release.formula.toml` exists in `internal/formula/formulas/`
    (no file matching `*release*` at all), and `scripts/publish.sh` does not
    exist (only `scripts/bump-version.sh` is present). Consequently the follow-on
    examples `bd cook release --var version=1.2.0` (`README.md:339`) and
    `bd mol pour release ...` (`README.md:342`) reference a `release` formula
    that isn't embedded in the binary and would fail.
  - **Impact**: This is the README's flagship illustration of the formula
    system; a reader who copy-pastes it gets errors, undermining trust in the
    docs at exactly the "learn the core abstraction" moment.
  - **Suggested fix**: Replace the example with a formula that actually ships
    (e.g. `code-quality.formula.toml` or `design.formula.toml` in
    `internal/formula/formulas/`), or add a real `release.formula.toml`. Drop the
    `scripts/publish.sh` step or point it at a real script.

- **`RELEASING.md` Option A `cd` target is wrong.**
  - **Location**: `RELEASING.md:17`
  - **Detail**: Option A says `cd gastown/mayor/rig` then
    `./scripts/bump-version.sh X.Y.Z ...`. Verified: `mayor/rig/` contains only
    `.gitignore` and `.kiro/` — there is no `mayor/rig/scripts/` directory. The
    `bump-version.sh` script lives at the repo root (`scripts/bump-version.sh`),
    which is exactly what Option B (`RELEASING.md:22`) correctly invokes. The two
    options contradict each other.
  - **Impact**: The recommended release path fails with "No such file or
    directory"; a maintainer following the runbook stalls on the first step.
  - **Suggested fix**: Remove the `cd gastown/mayor/rig` line so Option A runs
    `./scripts/bump-version.sh X.Y.Z --commit --tag --push --install` from the
    repo root, matching Option B.

- **Broken intra-repo doc cross-links in convoy design docs.**
  - **Location**: `docs/design/convoy/spec.md:7`;
    `docs/design/convoy/mountain-eater.md:7` and `:474`
  - **Detail**: `spec.md` links `[convoy-manager.md](../daemon/convoy-manager.md)`
    but `docs/design/daemon/` does not exist. `mountain-eater.md` links
    `[swarm-architecture.md](../../../docs/swarm-architecture.md)` (twice) but
    `docs/swarm-architecture.md` does not exist.
  - **Impact**: Readers of the convoy architecture docs hit dead links to the
    "related" / referenced design material.
  - **Suggested fix**: Repoint each link to the surviving doc (e.g. the convoy
    `spec.md`/`roadmap.md` already linked alongside) or remove the dead
    references.

- **Core infra packages `acp` and `mayor` lack package-level doc comments and
  document few of their exported functions.**
  - **Location**: `internal/acp/proxy.go` (e.g. `type Proxy` at `:37`,
    `type JSONRPCMessage` at `:88`), `internal/acp/propulsion.go` (`Propeller`,
    `NewPropeller`); `internal/mayor/cleanup.go` (the
    `ACPPidFilePath`/`WriteACPPid`/`GetACPPid`/`IsACPActive`/... cluster starting
    at `:23`)
  - **Detail**: Neither `acp` nor `mayor` has any `// Package ...` doc comment
    (verified: `grep '^// Package acp'` / `'^// Package mayor'` return nothing),
    unlike the 166 files repo-wide that do. These are central packages — `acp` is
    the Agent Control Proxy that carries agent traffic; `mayor` owns
    session/PID/cleanup lifecycle — yet many exported symbols carry no doc
    comment. (Note: `shell` and `wrappers` lack godoc-style package comments too,
    but they at least carry `// ABOUTME:` header comments, so they are a weaker
    case — see Minor.)
  - **Impact**: A contributor reading `go doc` for the agent-transport and
    session-lifecycle layers gets bare signatures with no overview.
  - **Suggested fix**: Add a `// Package acp ...` / `// Package mayor ...` comment
    and doc the exported PID/lifecycle and proxy/message types.

## Minor Findings (P2 — informational)

- **Absolute `~/gt/docs/...` references that resolve to nothing in this
  checkout.** `docs/design/federation.md:46` (`~/gt/docs/hop/GRAPH-ARCHITECTURE.md`),
  `docs/concepts/identity.md:248` (`~/gt/docs/hop/decisions/008-identity-model.md`),
  `docs/design/property-layers.md:411` (`~/gt/docs/hop/PROPERTY-LAYERS.md`), and
  `docs/contrib-harnesses/README.md:14` (`~/gt/docs/PRIMING.md`) all point under
  `~/gt/docs/`, which does not exist in this rig checkout. These may be
  intentional references to a sibling town-root docs tree rather than this repo —
  flagging as uncertain, not confirmed-broken. If they are meant to be repo-local,
  they should be repo-relative paths; if town-root, they should say so explicitly.

- **`shell` and `wrappers` packages have no godoc-discoverable package comment.**
  `internal/shell/integration.go` and `internal/wrappers/wrappers.go` open with
  `// ABOUTME:` header comments (a sparse convention — only 13 files repo-wide use
  it) rather than a `// Package shell ...` comment directly above the `package`
  clause, so `go doc` shows no package overview for these user-facing
  (RC-file-modifying, wrapper-installing) packages.

- **Scattered small exported-doc gaps in otherwise well-documented packages.**
  Representative: `internal/cmd/health.go` (6 result structs — `ServerHealth`,
  `DatabaseHealth`, `PollutionRecord`, `BackupHealth`, `ProcessHealth`,
  `OrphanDB` — undocumented), `internal/beads/exec.go` (`EnvForSubprocessMode`,
  `SubprocessModeForArgs`), `internal/refinery/engineer.go` (`GateConfig` at
  `:94`), and `internal/quota` (~9 utility/stub funcs). Low impact; these are
  incremental cleanups, not gaps in a core public surface.

- **Positive note (no action):** No TODO/FIXME/XXX/HACK/TEMPORARY comments aged
  past the 90-day threshold were found in `internal/`, `cmd/`, or `plugins/`, and
  a wide spot-check found no comment that contradicts its code. Comment hygiene is
  a strength.

## Counts

  counts: critical=0 major=4 minor=3

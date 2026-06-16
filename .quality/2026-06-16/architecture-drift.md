# Architecture Drift Audit

## Summary

The gastown Go module (`github.com/steveyegge/gastown`, 89 packages, ~1,784
`.go` files) remains **structurally healthy at the boundary level but
increasingly concentrated at the center** — the same shape as the 2026-06-04
baseline, with the concentration trend continuing rather than reversing. The
boundary health is real and verifiable: there are **no package import cycles**
(`go build ./...` is green, and Go forbids cycles outright), and there are
**zero downward layering violations** — a scan of every low-level library
(`util`, `config`, `constants`, `beads`, `session`, `tmux`, `rig`, `runtime`,
`liveness`, `git`, `mail`, `nudge`, `style`, `lock`) for imports of any
high-level package (`cmd`, `daemon`, `witness`, `refinery`, `polecat`,
`deacon`, `mayor`) returned nothing. Dependencies still flow one way, from the
command/agent surface down to shared libraries. The leaf utilities still behave
like leaves: `util` (fan-in 28), `config` (28), `constants` (25 in / 0 out),
and `tmux` (18) have high fan-in but ≤3 fan-out — textbook shared-library
shape.

The drift is **coupling concentration, not coupling chaos**, and it is moving
in the wrong direction on two fronts since the baseline. First, the two biggest
load-bearers grew: `internal/cmd` went **523 → 590 files (+12.8%)** and
`internal/daemon` went **132 → 162 files (+22.7%)** — both still single,
un-subdivided packages, so the growth lands inside one shared private namespace
with no new compiler-enforced seams. Neither crossed the ">50% growth" trigger,
so these are tracked, not escalated. Second, the **presentation-layer leak the
baseline flagged is unfixed and has not shrunk**: `internal/style` (a Lipgloss
terminal-rendering package) is still imported by the pure-data packages
`beads`, `doltserver`, and `rig`. The classic "god" packages `beads` (fan-in 24
/ fan-out 9) and `session` (16 / 12) are unchanged in shape — `session`'s file
count is flat at 23, which is the one clearly good signal this cycle. Package
count actually fell (95 → 89) while file count rose (1,642 → 1,784), consistent
with consolidation of small packages alongside growth inside the big ones.

Trend vs. 2026-06-04 (prior `summary.json` present, so deltas are computable
this run): **score 0.72 → 0.71** — essentially flat, a hair lower because the
two named load-bearers grew and the style leak remains open, with no offsetting
structural improvement landed.

## Score

score: 0.71

## Critical Findings (P0 — file as beads, fix urgently)

None. The build proves there are no new circular imports, and the low-level →
high-level scan proves there is no layering violation in any core path. Nothing
this cycle warrants urgent remediation.

## Major Findings (P1 — track but do not auto-bead)

- **`internal/cmd` is now a 590-file flat package and still growing (+12.8%
  since baseline)**
  - **Location**: `internal/cmd/` (largest files: `convoy.go` 4,557 LOC,
    `done.go` 3,398, `polecat.go` 3,102, `rig.go` 2,579, `status.go` 2,182,
    `capacity_dispatch.go` 2,134, `convoy_stage.go` 2,075)
  - **Impact**: Every one of the 590 files shares a single `package cmd`
    namespace (verified: all `internal/cmd/*.go` declare `package cmd`), so all
    unexported identifiers are mutually visible — there is still **no
    compiler-enforced boundary anywhere inside the entire command surface**, and
    that surface grew by 67 files this cycle. Fan-out is 66 internal packages,
    the highest in the tree. This is the package a new teammate drowns in: it
    says "this is the CLI" and nothing about responsibilities within it. The
    largest single file (`convoy.go`) grew from 3,485 to 4,557 LOC since the
    baseline — concentration within the package, not just across it.
  - **Suggested fix**: Same as baseline — carve cohesive command groups into
    sub-packages (`internal/cmd/convoy`, `internal/cmd/sling`,
    `internal/cmd/polecat`, …), each exposing a small `RegisterCommands(root)`
    entry point. Start with the largest self-contained clusters (convoy is now
    the obvious first candidate at 4.5K LOC) to get compiler-enforced seams
    without a big-bang refactor.

- **Presentation layer (`internal/style`) still leaks into core data packages
  (carried over, unfixed)**
  - **Location**: `internal/beads/beads_delegation.go:9,81,103`
    (`style.PrintWarning(...)`); `internal/doltserver/doltserver.go:54`;
    `internal/rig/guards.go:11`, `internal/rig/setuphooks.go:13`,
    `internal/rig/overlay.go:11`
  - **Impact**: `internal/style` is a terminal-rendering package (Lipgloss-based
    "consistent terminal styling"). Pure data/storage packages calling it
    directly couples the domain to the TUI: `beads`, `doltserver`, and `rig`
    drag Lipgloss and the color theme into their dependency graph and cannot be
    cleanly reused in a non-terminal context (a server, a test harness, a future
    API). This was flagged in the 2026-06-04 baseline and is **still present in
    the same packages** — the leak has not been remediated. (`style`'s other
    ~187 importers are user-facing `cmd`/agent code, where it's appropriate; the
    finding is specifically the data-layer leak.)
  - **Suggested fix**: Have the data packages return errors/values and let the
    command layer render them, or route warnings through a small logging/event
    interface rather than calling a styling package directly. The two
    `beads_delegation.go` `PrintWarning` calls are the smallest, most
    self-contained starting point.

- **`internal/beads` and `internal/session` remain god packages (high fan-in
  AND fan-out)**
  - **Location**: `internal/beads/` (fan-in 24, fan-out 9, 58 files incl. tests);
    `internal/session/` (fan-in 16, fan-out 12, 23 files)
  - **Impact**: These two still have the highest fan-in × fan-out product in the
    tree (216 and 192). They are simultaneously depended on by ~20 packages
    *and* reach into ~10 lower-level ones, so a change to either ripples widely.
    `session` spans config, events, git, tmux, workspace, telemetry, and cli.
    The good news: **`session`'s file count is flat (23 → 23)** and beads grew
    only modestly (52 → 58), so neither is accelerating — they are stable
    god packages, not worsening ones. There is no beads↔session interlock
    (neither imports the other).
  - **Suggested fix**: No restructure required today; treat both as
    frozen-interface packages — review new exports critically, and split
    `session` along its seams (process/tmux lifecycle vs. session metadata/state)
    only if it resumes growing.

## Minor Findings (P2 — informational)

- **`internal/daemon` grew 132 → 162 files (+22.7%), fan-out 38** — still the
  second-most-coupled package after `cmd`, and the fastest-growing of the big
  ones this cycle (in percentage terms). Not yet painful, but it is on the same
  trajectory as `cmd`; a sub-package split is worth doing before it crosses
  ~200 files. Watch this delta next run.

- **Two more central hubs surfaced this run: `internal/rig` (fan-in 9 / fan-out
  10) and `internal/runtime` (fan-in 13 / fan-out 6).** Neither is a true "god"
  package by the file-count lens (`rig` 17 files, `runtime` only 2), so these
  are coupling *hubs* rather than oversized packages — flagged so the next run
  can tell whether they accrete files. `internal/polecat` has high fan-out (16)
  but low fan-in (6), which is the expected shape for an agent package, not a
  drift signal.

- **Flat `internal/` layout still offers little responsibility signal.** With
  ~85 packages mostly at one level under `internal/`, the structure conveys the
  *nouns* (beads, session, tmux, refinery…) but not the *layers*. There is still
  no declared package-layering contract in `docs/design/architecture.md` (it
  documents runtime/data architecture, not code layering), so "layering
  violation" is judged only against the implicit lib → agent → cmd gradient.
  Documenting the intended layers would make future drift mechanically
  checkable.

- **84 interface declarations across `internal/` (non-test), up from 27 at
  baseline.** This is a large jump, but much of it is legitimate growth of
  seam interfaces rather than interface-per-struct ceremony; spot checks found
  no egregious single-implementation abstraction that complicates without
  enabling testing/substitution. Flagged for the next run to watch — if the
  count keeps climbing faster than package count, audit for needless
  abstraction.

- **Package count fell while files rose (95 → 89 packages, 1,642 → 1,784
  files).** Consolidation of small packages alongside growth inside large ones.
  Mildly reinforces the central-concentration story but is not itself a problem.

## Counts

  counts: critical=0 major=3 minor=5

---

## Methodology / Reproduction

- Import-graph fan-in/fan-out computed from
  `go list -deps=false -f '{{.ImportPath}} {{range .Imports}}{{.}} {{end}}'
  ./internal/...`, restricted to `internal/*` → `internal/*` edges.
- Cycle absence proven by a green `go build ./...` (Go forbids import cycles).
- Layering scan: `grep` each low-level package directory for imports of any
  high-level package (`cmd`, `daemon`, `witness`, `refinery`, `polecat`,
  `deacon`, `mayor`); zero hits.
- `internal/style` leak: `grep -rn "internal/style\"" internal/beads
  internal/doltserver internal/rig`.
- Size deltas vs. `.quality/2026-06-04/architecture-drift.md` +
  `.quality/2026-06-04/summary.json` (prior score 0.72), using the same
  all-files-incl-tests file-count convention.
- `go vet ./internal/...` clean.

## Sources

- Local repository analysis of `github.com/steveyegge/gastown` at branch
  `polecat/fury/gu-leg-3mjbo--mqgz35it` — accessed 2026-06-16
- [Prior architecture-drift baseline](.quality/2026-06-04/architecture-drift.md) — accessed 2026-06-16
- [Declared architecture](docs/design/architecture.md) — accessed 2026-06-16

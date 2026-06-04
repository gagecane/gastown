# Architecture Drift Audit

## Summary

The gastown Go module (`github.com/steveyegge/gastown`, 95 packages, 1,642 `.go`
files) is structurally **healthy at the boundary level but concentrated at the
center**. The good news first: there are **no package import cycles** (Go's
compiler forbids them and `go build ./...` is green), and there are **no
downward layering violations** — a targeted scan of every low-level library
(`beads`, `session`, `config`, `util`, `tmux`, `rig`, `runtime`, `liveness`,
`git`, `mail`, `nudge`) found *zero* imports of high-level packages (`cmd`,
`daemon`, or any agent package). Dependencies flow one way, from the command/
agent surface down to shared libraries. The leaf utilities behave like leaves:
`util` (fan-in 29), `config` (29), `constants` (26), and `tmux` (20) have high
fan-in but low fan-out (≤3), which is exactly what a well-factored shared
library looks like.

The drift is **coupling concentration**, not coupling chaos. A handful of
packages are becoming load-bearing in ways the flat `internal/` layout doesn't
make legible. `internal/cmd` is a single, un-subdivided package of **523 files
(~199K LOC)** that imports 65 other internal packages — the entire CLI surface
shares one private namespace with no compiler-enforced internal seams.
`internal/beads` and `internal/session` are classic "god" packages with high
fan-in *and* fan-out. And the terminal-presentation package `internal/style`
has leaked into pure data packages (`beads`, `doltserver`, `rig`), coupling the
domain to Lipgloss/TUI rendering. None of this is critical, but it is the
direction worth watching. **No prior `.quality/**/summary.json` exists, so this
is the baseline run — all sizes are absolute; no trend delta can be computed.**

## Score

score: 0.72

## Critical Findings (P0 — file as beads, fix urgently)

None. No new circular imports (the build proves their absence) and no layering
violation in a core path (no low-level package imports a high-level one).

## Major Findings (P1 — track but do not auto-bead)

- **`internal/cmd` is a 523-file, ~199K-LOC flat package with zero
  sub-packages**
  - **Location**: `internal/cmd/` (e.g. `done.go` 3,487 LOC, `convoy.go` 3,485,
    `polecat.go` 3,036, `rig.go` 2,570, `convoy_stage.go` 2,275)
  - **Impact**: Every one of the 523 files shares a single Go package namespace,
    so all unexported identifiers are mutually visible — there is **no
    compiler-enforced boundary anywhere inside the entire command surface**.
    Fan-out is 65 internal packages (the highest in the tree). This is the
    package a new teammate would drown in: it tells you "this is the CLI" and
    nothing about responsibilities within it. It is the single largest structural
    load-bearer in the codebase and the hardest to reason about or test in
    isolation.
  - **Suggested fix**: Carve cohesive command groups into sub-packages
    (`internal/cmd/convoy`, `internal/cmd/sling`, `internal/cmd/polecat`, …),
    each exposing a small `RegisterCommands(root)` entry point. Start with the
    largest, most self-contained clusters (convoy, sling) to get
    compiler-enforced seams without a big-bang refactor.

- **`internal/beads` and `internal/session` are god packages (high fan-in AND
  fan-out)**
  - **Location**: `internal/beads/` (fan-in 24, fan-out 9, 29 non-test files);
    `internal/session/` (fan-in 17, fan-out 12)
  - **Impact**: These two have the highest fan-in × fan-out product in the tree
    (216 and 204 respectively). They are simultaneously depended on by ~20 other
    packages *and* reach into ~10 lower-level ones, so a change to either ripples
    widely and a bug in either is broadly blast-radius'd. `session` in
    particular spans config, events, git, tmux, workspace, telemetry, and cli —
    it is accreting "everything about a running session" rather than a focused
    responsibility.
  - **Suggested fix**: No restructure required today, but treat these as
    frozen-interface packages: review new exports critically, and consider
    splitting `session` along its seams (process/tmux lifecycle vs. session
    metadata/state) if it keeps growing.

- **Presentation layer (`internal/style`) leaks into core data packages**
  - **Location**: `internal/beads/beads_delegation.go:81,103`
    (`style.PrintWarning(...)`); `internal/style` is also imported by
    `internal/doltserver` and `internal/rig`
  - **Impact**: `internal/style` is a terminal-rendering package
    ("consistent terminal styling using Lipgloss", Ayu theme via `internal/ui`).
    Pure data/storage packages calling it directly couples the domain to the TUI:
    `beads` and `doltserver` now drag Lipgloss and the color theme into their
    dependency graph and cannot be cleanly reused in a non-terminal context
    (a server, a test harness, a future API). For `style`'s other 10 importers
    (`cmd`, `doctor`, `dog`, `witness`, `refinery`, `polecat`, `crew`, `acp`)
    this is fine — they are user-facing. The leak is specifically into the data
    layer.
  - **Suggested fix**: Have the data packages return errors/values and let the
    caller (command layer) render them, or route warnings through a small
    logging/event interface rather than calling a styling package directly.

## Minor Findings (P2 — informational)

- **`internal/daemon` is a 62-file (non-test) single package, fan-out 38** — the
  second-most-coupled package after `cmd`. Not yet painful, but it is on the same
  trajectory as `cmd` and worth a sub-package split before it crosses ~100 files.

- **Flat `internal/` layout offers little responsibility signal.** With 95
  packages mostly at one level under `internal/`, the structure tells you the
  *nouns* (beads, session, tmux, refinery…) but not the *layers*. There is no
  declared package-layering contract in `docs/design/architecture.md` (it
  documents runtime/data architecture, not code layering), so "layering
  violation" is currently judged only by the implicit lib→agent→cmd gradient.
  Consider documenting the intended layers so future drift is checkable.

- **27 interface declarations across `internal/` (non-test).** Volume is modest
  and no single-implementation-abstraction stood out as egregious in spot
  checks; flagged only so the next run can watch for interface-per-struct
  ceremony as the codebase grows.

- **Baseline run — no growth/trend data.** No prior `.quality/**/summary.json`
  was found, so the ">50% file-count growth" criterion cannot be evaluated this
  cycle. Recorded absolute sizes (`cmd`=523, `daemon`=132, `beads`=52 incl.
  tests, `session`=23) so the next periodic run can compute deltas.

## Counts

  counts: critical=0 major=3 minor=4

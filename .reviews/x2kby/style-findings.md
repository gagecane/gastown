# Style Review

## Summary

The codebase demonstrates strong style discipline overall: consistent package
structure, clear file-header docstrings linking to design documents, well-chosen
names, and a coherent import organization pattern (stdlib → third-party →
internal). The most impactful style issue is **227 files failing `gofmt`** —
a mechanical fix but a significant lint hygiene gap. Beyond that, several
recently-landed packages (autotestpr, daemon, cmd/upstream) have minor
alignment inconsistencies and a small number of convention drifts that, while
not bugs, reduce the "one style" consistency a contributor expects from a
well-maintained monorepo.

The code is largely self-documenting. Comments are thoughtful and explain
"why" rather than "what." ADR-in-doc.go (sandbox/doc.go) is exemplary
technical writing. The codebase would pass a style guide review with a
B+ grade — the gofmt violations and a few naming inconsistencies are
the gap between "good" and "excellent."

## Critical Issues

(None — no P0 merge-blocking style issues.)

## Major Issues

### P1-1: 227 files fail `gofmt` — widespread formatting violations

**Scope:** `gofmt -l internal/` reports 227 files with formatting drift.

Affected areas include recently-landed code (autotestpr, daemon, cmd) as
well as older code. Common drift patterns:

- Extra alignment padding in struct field declarations (e.g.,
  `restart_tracker.go:325` `PruneResult` fields)
- Extra alignment padding in `var ( ... )` blocks (e.g.,
  `cmd/upstream.go:28` `upstreamRig`, `upstreamJSON`)
- Manual alignment in struct literal maps that gofmt would normalize
  (e.g., `tautology/analyzer.go:488` assert function map)
- Extra space before format verb (e.g., `cmd/tap_guard.go:155`
  `fmt.Fprintf(os.Stderr,  "..."`)

**Impact:** CI currently does not gate on gofmt (or if it does, these
files were merged despite violations). Contributors will replicate the
drift because existing code teaches conventions by example.

**Suggested fix:** Run `gofmt -w internal/` as a single cleanup commit.
Add a `gofmt -l` check to the gate suite to prevent regressions.

### P1-2: `fmt.Fprintf(os.Stderr, ...)` used for operational logging in library packages

**Files:**
- `internal/autotestpr/cycle.go:199,264`

These library functions write directly to stderr rather than accepting a
logger or returning structured diagnostics. The daemon package correctly
uses `d.logger.Printf(...)` throughout (main_ci_break_dog.go, etc.).
The autotestpr package should follow the same pattern.

**Impact:** Library code writing to stderr bypasses structured logging,
makes output untestable without capturing stderr, and will interleave
unpredictably with the daemon's log output when the cycle runs as a
patrol.

**Suggested fix:** Accept a `*log.Logger` (or `io.Writer`) in
`CycleConfig` and route warnings through it. The fprintf calls in
`cycle.go` already format structured messages — they just need a
configurable destination.

## Minor Issues

### P2-1: `sliceContains` reimplemented in daemon package instead of using `slices.Contains`

**File:** `internal/daemon/main_branch_test_runner.go:677`

Go 1.25 (the module version) includes `slices.Contains` in the
standard library. The daemon package defines its own `sliceContains`
and uses it across 3 files. The `cmd` package already uses
`slices.Contains` in test code (`formula_test.go`).

**Impact:** Low — functional duplicate. But new contributors may wonder
which helper to use. The standard library version should be preferred.

**Suggested fix:** Replace `sliceContains` calls with
`slices.Contains` and remove the local helper.

### P2-2: Inconsistent doc.go presence across packages

Only 3 packages in `internal/` have `doc.go` files:
- `internal/autotest/sandbox/doc.go` (exemplary — 150 lines)
- `internal/upstreamsync/doc.go` (good — 10 lines)
- `internal/formula/doc.go`

Meanwhile, larger packages like `internal/cmd` (2700+ line files),
`internal/daemon`, `internal/beads`, `internal/autotestpr`, and
`internal/polecat` lack doc.go entirely. Their package-level docs
live in file-header comments on the first file alphabetically, which
is the Go convention — but for packages with 50+ files, a dedicated
doc.go is more discoverable.

**Impact:** Low — this is convention consistency, not a bug. The
codebase is perfectly valid without doc.go everywhere.

### P2-3: autotestpr/branch_gc.go comment block formatting violates godoc conventions

**File:** `internal/autotestpr/branch_gc.go:3-15`

The file header uses indented list items that gofmt rewrites into
`//  \t` tabbed format (godoc convention for code blocks). The
original intent is a prose list, not a code block. After gofmt, the
indented items display as preformatted text in `go doc`.

**Impact:** Documentation rendering. The comment is clear to human
readers but renders oddly in `go doc` output.

**Suggested fix:** Either remove the indentation (prose flow) or
accept gofmt's reformatting. Both are acceptable — but current
state is "committed source doesn't match gofmt output," which is
the underlying P1-1 issue.

### P2-4: `minInt` helper defined locally in main_ci_break_dog.go

**File:** `internal/daemon/main_ci_break_dog.go:365`

Go 1.25 provides `min()` as a builtin. The local `minInt` helper
predates this and is used only for SHA truncation:
```go
attr.Commit[:minInt(12, len(attr.Commit))]
```

**Impact:** Minimal — two call sites. But it's dead weight now that
`min()` is a builtin.

**Suggested fix:** Replace `minInt(a, b)` with `min(a, b)` and delete
the helper.

### P2-5: File-level comment style varies between packages

Most files use the Go convention of starting with a sentence that
begins with the exported identifier name or with a package-level
comment. However, some recent files use a looser style:

- `internal/cmd/done_rebase.go` opens with no file-level comment
  (only function-level docs)
- `internal/autotestpr/cycle.go` has an excellent 36-line header
- `internal/daemon/main_ci_break_dog.go` has a thorough header
- `internal/cmd/tap_guard.go` has no file-level comment

The inconsistency is mild — Go does not require file-level comments
except on the package clause's file. But within a single package
(`cmd`), some files are heavily documented and others are bare.

### P2-6: Large file sizes in cmd package

Several files in `internal/cmd/` exceed reasonable single-file sizes:

| File | Lines |
|------|-------|
| `done.go` | 2,497 |
| `convoy.go` | 2,711 |
| `sling.go` | 1,571 |

While Go has no formal line limit, files over ~1000 lines are harder
to navigate and review. The sling command already split batch logic
into `sling_batch.go` (383 lines) — the same treatment would benefit
`done.go` (which has clear sub-sections: push logic, checkpoint logic,
cleanup logic) and `convoy.go`.

**Impact:** Maintenance cost. Large files increase merge conflicts and
make it harder for new contributors to find relevant code.

## Observations

- **Import organization is consistent**: stdlib → third-party →
  internal across all reviewed files. No violations found.
- **Error wrapping with `%w` is universal**: all fmt.Errorf calls
  that wrap errors use `%w`. No instances of `%v` for error wrapping
  in recently-landed code.
- **Naming conventions are strong**: camelCase for locals, PascalCase
  for exports, descriptive function names. No single-letter variables
  outside loop counters.
- **nolint annotations are well-justified**: 218 nolint comments all
  include explanatory text (e.g., `//nolint:gosec // G204: args
  constructed internally`). No bare `//nolint` directives.
- **Test naming follows `TestFunctionName_Scenario` pattern**
  consistently.
- **Comment quality is high**: the ratio of "why" comments to "what"
  comments is excellent. Design references (issue IDs, synthesis
  section links) are embedded in code comments for traceability.
- **The codebase does not use `log.Fatal` or `os.Exit` in library
  code** — exits are correctly confined to command handlers.
- **Cobra command structure is consistent**: `init()` registers
  flags, `RunE` handlers return errors. No `Run` handlers that
  call `os.Exit`.

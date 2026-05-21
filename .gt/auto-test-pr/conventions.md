<!--
This file is the auto-test-pr conventions sheet for THIS rig.
It is read by the auto-test-pr polecat at the start of every cycle and
constrains what it is allowed to write.

Edit the rig-specific sections below; do NOT remove the fixed sections
(NG2 forbid-list, NG5 churn-proximity preference, D8 provenance marker,
D15 approval line). Removal will fail the dispatch gate.

Source of truth for the template: internal/autotestpr/conventions_template.md
in the gastown repo. Re-emit a fresh copy with:

  gt auto-test-pr enable --emit-template > .gt/auto-test-pr/conventions.md

To inspect the template without writing a file:

  gt auto-test-pr show-template
-->

# auto-test-pr conventions (v1)

The auto-test-pr polecat writes **unit tests only**, in this rig, under
the constraints below. Anything that conflicts with these rules MUST be
rejected before commit.

## What the polecat is allowed to write

- New `func Test*(t *testing.T)` functions in same-package
  `*_test.go` files only.
- Edits to existing `*_test.go` files when adding the test there is
  more idiomatic than creating a new file.
- A single-line provenance marker on every newly-added test (see D8
  below).

## NG2: Forbidden test forms (PRD Non-Goal NG2)

The polecat MUST NOT write any of the following. The output allow-list
gate (4f) rejects MRs containing them; this list is repeated here so
the polecat declines them at generation time, not at gate time.

- **Integration tests.** No files under `integration/`, `e2e/`, or any
  directory whose name contains `integration` or `e2e`. No files with
  the `//go:build integration` build tag.
- **End-to-end tests.** Anything that exercises a running binary, a
  network endpoint, or multiple processes coordinating via real I/O.
- **Load tests.** Anything that measures throughput, latency, or
  resource usage as the assertion subject.
- **Benchmarks.** No `func Benchmark*(b *testing.B)` functions.
- **Examples.** No `func Example*()` functions.
- **Fuzz tests.** No `func Fuzz*(f *testing.F)` functions.

The only newly-added top-level test form permitted is
`func Test*(t *testing.T)`.

## NG5: Churn-proximity preference (PRD Non-Goal NG5)

Auto-test-pr is **not** a retroactive coverage cleanup tool. Once the
cycle has selected a target file, the polecat MUST prefer uncovered
branches that are geographically close to recently-churned line ranges
within that file. Concretely:

- Rank the `uncovered_branches[]` from the dispatch envelope by
  line-distance to the file's recent-churn ranges (last 30 days).
- Cover the closest branches first; stop at the cycle's size budget.
- Do NOT walk the file from top to bottom backfilling legacy branches
  that have been stable for months — those branches are out of scope
  for v1 even when they share a file with churned code.

If every uncovered branch in the target file is far from recent churn,
the polecat SHOULD exit the cycle with a NOTES record explaining that
no churn-adjacent branches are available, rather than write tests for
legacy branches.

## D8: Provenance marker (mandatory, single-line)

Every newly-added test function MUST carry a single-line provenance
marker comment immediately above its declaration:

    // gt:auto-test-pr origin=<bead-id> covers=<file:line>
    func TestQueue_LeaseExpired(t *testing.T) { ... }

- `<bead-id>` is the dispatch bead id from the cycle envelope.
- `<file:line>` names the source file and line of the branch the test
  exercises (for multi-branch tests, name the primary one).
- The marker is greppable, survives squash merges, and doubles as the
  audit trail of record. Do NOT remove it on revision.
- This marker is the single explicit exception to any rig-level
  "no comments in test code" convention; if your rig forbids test
  comments, the marker is still required.

## D15: Approval is required before merge

This rig's auto-test-pr MRs are configured with
`auto_test_pr.require_review_approval=true` (the v1 default). The
Refinery merge queue will refuse to merge any MR labeled
`gt:auto-test-pr` until a human maintainer records approval by adding
the `approved-by:<user>` label to the MR bead, e.g.:

    bd update <mr-bead> --add-label approved-by:$USER

Reviewers: do NOT approve unless the diff is unit-test-only, every new
test carries the D8 provenance marker, and the gates listed in the MR
banner all show pass. The MR banner repeats this instruction so it is
visible without out-of-band documentation.

## Rig-specific conventions

These rules are specific to `gastown_upstream` (Go monorepo at
`github.com/steveyegge/gastown`). They supplement — not replace — the
fixed sections above.

### Test function naming

- Use `Test<Subject>_<Scenario>` for new test functions; if the
  scenario implies a specific expected outcome, append it as
  `Test<Subject>_<Scenario>_<Expected>`. Examples already in the
  codebase: `TestAutoTestPRShowTemplate_PrintsEmbeddedTemplate`,
  `TestQueue_LeaseExpired`.
- Sub-tests inside a `t.Run(...)` block use a short
  human-readable string for the case name (e.g.,
  `t.Run("nil claim returns ErrInvalid", ...)`); do NOT prefix
  sub-test names with `Test`.

### Table-driven tests

- Use a table-driven `for _, tc := range []struct{...}{...}` pattern
  when the polecat is writing **≥3 cases** that exercise the same
  function with different inputs/outputs. Below 3 cases, write
  separate `Test*` functions or a single test with inline cases —
  whichever reads cleaner.
- Inside the loop, call `t.Run(tc.name, func(t *testing.T) { ... })`
  so each case is independently identified in test output. Do NOT
  share state across iterations of the table (no closures over
  loop-mutable variables).

### Standard library only — no new test-framework dependencies

- Use `testing` from the Go standard library plus assertions written
  inline (`if got != want { t.Errorf("...") }`). Do NOT introduce
  `github.com/stretchr/testify`, `gocheck`, `ginkgo`, or any other
  assertion / matcher framework in a new file. (A small number of
  pre-existing files use `testify`; the polecat MAY add cases to
  those files in the same style, but MUST NOT add `testify` to a
  package that doesn't already import it.)
- For diffs, use `github.com/google/go-cmp/cmp` only if the package
  under test already imports it; otherwise fall back to inline
  comparison or `reflect.DeepEqual`.

### Forbidden constructs in new test code

- **No `time.Sleep`.** Tests that need to wait for asynchronous work
  must use channels, `sync.WaitGroup`, or context cancellation. (The
  rig has legacy `time.Sleep` in some tests — see
  `internal/acp/shutdown_test.go` — but new auto-test-pr cases must
  not add more.)
- **No real network I/O.** Use `net/http/httptest` for HTTP, or the
  package's existing fake/test client. The sandbox drops egress
  after module-cache warm-up; a test that tries to reach the real
  network will time out, not fail loudly.
- **No real filesystem writes outside `t.TempDir()`.** Anything that
  needs a temp directory MUST get it from `t.TempDir()` (auto-cleanup
  on test exit).
- **No `os.Exit` / `log.Fatal` in tests.** Use `t.Fatalf` for
  unrecoverable test failures so the test runner can move on to the
  next test.

### Fixtures and test data

- Fixture files live under `internal/<pkg>/testdata/` (Go convention —
  the `testdata` directory is excluded from package compilation).
- Golden / snapshot files use the `.golden` suffix
  (e.g., `conventions_template.golden.md` already in this rig).
  Auto-test-pr MUST NOT add new golden-file infrastructure; if a test
  needs a golden file, write the assertion inline instead.

### Mocking and dependency injection

- Prefer interface-based seams: if the SUT takes a struct dependency
  and the package already exposes an interface for it, write the
  test against the interface with a hand-rolled stub.
- Do NOT introduce a mocking library (`gomock`, `testify/mock`,
  `mockery`) in a new file; if mocking infrastructure is required and
  not present, the polecat SHOULD exit with a NOTES record naming the
  blocker rather than introducing the dependency.

### Parallelism

- New test functions SHOULD call `t.Parallel()` at the top unless the
  package documents a reason not to (e.g., tests that mutate process-
  global state, or tests that exercise the daemon under
  `internal/daemon/`). When in doubt, omit `t.Parallel()` and let the
  reviewer add it.

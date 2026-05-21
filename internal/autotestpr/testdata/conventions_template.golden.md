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

Edit this section to capture rules that are specific to this rig.
Common items teams add:

- Naming pattern for test functions (e.g.,
  `Test<Subject>_<Scenario>_<Expected>`).
- When to use table-driven tests (e.g., "≥3 cases for the same
  function").
- Forbidden constructs in test code (e.g., "no `time.Sleep`; use
  `synctest` or fake clocks").
- Mocking conventions (e.g., "prefer interfaces in
  `internal/<pkg>/testutil` over hand-rolled mocks").
- Fixture conventions (e.g., "fixture data lives under
  `internal/<pkg>/testdata/`").

Leave a section empty if the rig has no rule of that kind; do not
fabricate constraints to fill space.

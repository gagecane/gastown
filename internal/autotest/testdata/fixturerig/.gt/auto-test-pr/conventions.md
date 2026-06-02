# auto-test-pr conventions (fixturerig)

Conventions sheet for the Phase-0 e2e fixture rig. The cycle's
load-context step reads this file; the dispatch gate hard-fails if it is
missing. Kept deliberately small — the real template lives at
internal/autotestpr/conventions_template.md.

## What the polecat is allowed to write

- New `func Test*(t *testing.T)` functions in same-package `*_test.go`
  files only.

## NG2: Forbidden test forms

- No `Benchmark*`, `Example*`, `Fuzz*`.
- No files under `integration/`, `e2e/`, or `test/`.
- No `//go:build integration` build tag.

## NG5: Churn-proximity preference

Prefer uncovered branches close to recent churn.

## D8: Provenance marker

Every newly-added test MUST carry:

    // gt:auto-test-pr origin=<bead-id> covers=<file:line>

## D15: Maintainer approval

Auto-test-pr MRs merge only after a maintainer applies `approved-by:<user>`.

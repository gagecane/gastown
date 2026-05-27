# Commit Discipline Review

## Summary

The commit history on main demonstrates strong overall discipline. The project
consistently uses conventional commit prefixes (`feat:`, `fix:`, `docs:`,
`test:`, `refactor:`, `perf:`, `chore:`) with meaningful scopes. Most commits
are atomic — a single logical change per commit — and commit messages explain
the "what" clearly. The multi-agent workflow (polecats) naturally produces
well-scoped commits since each agent works on a single bead.

The main issues are: (1) two "WIP" commits that leaked onto main from stranded
polecat recovery, and (2) a few batch-fix commits that combine multiple
unrelated changes under a single message. These are the exception, not the rule.

## Critical Issues

(None — no commit discipline violation blocks landing future work.)

## Major Issues

### P1-1: WIP commits on main

Two commits carry "WIP" in their subject line, signaling incomplete work that
was merged to main:

- `1c5f78a4` — `feat(auto-test-pr): WIP tautology analyzer` (1706 insertions, 6 files)
- `a7215de8` — `feat(auto-test-pr): WIP revise command and town_state_mutators` (300 insertions, 2 files)

Both have the body "Recovered from stranded <polecat> polecat. Uncommitted work
preserved for continuation." While the recovery semantics are understood (these
preserve stranded work), "WIP" commits on the default branch make bisection
unreliable — it's unclear whether the commit represents a working state.

**Impact**: `git bisect` may land on a half-implemented feature that fails
tests for reasons unrelated to the regression being hunted.

**Suggested fix**: The recovery workflow should either (a) squash WIP content
into the next completing commit that finishes the feature, or (b) land WIP
commits on a feature branch rather than main, merging only when complete.

### P1-2: Multi-concern lint-fix commits

- `ac2b8c05` — `fix(lint): clear 3 main-blocking lint failures`

This commit touches 3 files across 3 unrelated packages (`auto_test_pr_pause.go`,
`molecule_await_event.go`, `witness/handlers.go`) for 3 distinct lint rules
(unparam, misspell, unparam again). While each fix is trivial, bundling them
makes revert granularity worse — if one fix introduced a regression, reverting
the commit removes two correct fixes.

**Impact**: Low (trivial changes), but sets a precedent for "batch cleanup"
commits that could grow less trivial over time.

**Suggested fix**: For future lint-fix batches, one commit per package or per
lint rule is sufficient granularity.

## Minor Issues

### P2-1: Inconsistent scope tag for auto-test-pr

The commit history shows both `auto-test-pr` and `autotestpr` as scope tags:

- `feat(auto-test-pr): ...` — 10 occurrences
- `feat(autotestpr): ...` — 4 occurrences

This makes `git log --grep` less reliable when searching for all auto-test-pr
work.

**Suggested fix**: Standardize on one form (recommend `auto-test-pr` since it's
more prevalent and more readable).

### P2-2: Merge commits with minimal context

- `a8b0d423` — `fix: merge upstream/main — resolve formula_test.go conflict`
- `8e22636f` — `fix: merge upstream/main (6 commits behind)`

These use `fix:` type prefix for merge operations. Merges are neither a feature
nor a bug fix — `chore:` or no prefix would be more semantically accurate.

## Observations

- **Bisectability**: Overall excellent. 95%+ of commits represent a single
  logical change with clear before/after semantics.
- **Message quality**: High. Subjects are imperative mood, under 72 chars,
  explain the change clearly. Many have helpful body text with context.
- **Atomicity**: Strong. Feature commits add implementation + tests together.
  Doc commits are separate from code commits.
- **Bead references**: Many commits include bead IDs in the body or subject
  (e.g., `(gu-leg-mtmfg)`), providing excellent traceability.
- **Multi-author flow**: The polecat-per-bead model naturally produces atomic
  commits since each agent works one issue at a time.

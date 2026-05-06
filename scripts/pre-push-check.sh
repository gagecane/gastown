#!/bin/bash
# pre-push-check.sh: run the same gates CI runs, locally, before a push reaches origin.
#
# Purpose: this repo's CI Test + Integration Tests jobs are the trusted signal
# that a change doesn't break main. But CI only runs AFTER push, which means
# broken changes land on main before humans notice. Crew workers push directly
# to main (no feature branches, no PR queue), so there is no merge-queue
# backstop. This script closes that gap by running CI's gates locally.
#
# Gates (in order, fail-fast):
#
#   1. go build ./...            — compiles (catches broken imports, type errs)
#   2. go vet ./...              — static analysis (shadow, printf, unreachable)
#   3. go test ./... -count=1    — full unit test suite with clean env
#
# Env hygiene:
#   This script UNSETS GT_TOWN_ROOT and GT_ROOT before running tests. Some
#   tests call workspace.FindFromCwdOrError which falls back to these env vars
#   if CWD detection fails — a broken test can pass locally (your shell has
#   them set) but fail in CI (clean env). Unsetting here matches CI and
#   catches those tests before push. See commit 77c54398 for the canonical
#   instance of this bug.
#
# Integration tests:
#   This script does NOT run `-tags=integration` tests by default — they
#   require Docker + dolt container and take ~5 minutes. CI's Integration
#   Tests job runs them. To run locally: `make verify-integration`.
#
# Escape hatches (use sparingly; if you're reaching regularly, file a bead):
#   GT_SKIP_PREPUSH=1 git push       — skip this hook
#   git push --no-verify             — skip all hooks (standard git)
#
# Why pre-push, not pre-commit:
#   The existing pre-commit hook already runs go vet and a fast lint scoped
#   to staged files — that's the right granularity for "don't commit obvious
#   garbage." But the full test suite takes ~2min and polecats commit
#   constantly; running it on every commit would be a tax they'd learn to
#   --no-verify. Pre-push runs once per push instead of once per commit, so
#   the cost is amortized and it's the last line of defense before CI.

set -u

# --- Escape hatch ----------------------------------------------------------

if [[ "${GT_SKIP_PREPUSH:-0}" == "1" ]]; then
  echo "pre-push: GT_SKIP_PREPUSH=1, skipping local CI gates." >&2
  exit 0
fi

# --- Locate repo root -----------------------------------------------------

REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo "")
if [[ -z "$REPO_ROOT" ]]; then
  echo "pre-push: not a git repo, skipping." >&2
  exit 0
fi
cd "$REPO_ROOT" || exit 1

# --- Require go ----------------------------------------------------------

if ! command -v go >/dev/null 2>&1; then
  echo "⚠ pre-push: 'go' not found on PATH — skipping local gates." >&2
  echo "  CI will still run them; install Go for faster feedback." >&2
  exit 0
fi

# --- Clean env so tests don't inherit developer's GT_TOWN_ROOT ------------
#
# Tests that need a workspace must create their own marker (mayor/town.json).
# If they rely on GT_TOWN_ROOT/GT_ROOT from the developer's shell, they're
# silently broken — CI has no such env.

unset GT_TOWN_ROOT GT_ROOT GT_SESSION GT_RIG GT_POLECAT

# --- Gate 1: go build -----------------------------------------------------

echo "pre-push: go build ./... (compile check)" >&2
if ! go build ./... 2>&1; then
  cat >&2 <<'EOF'

✗ Push rejected: 'go build ./...' failed.

Fix compile errors before pushing. CI will reject the same build failures
but with a ~5min round-trip cost.

Emergency escape hatch:
  GT_SKIP_PREPUSH=1 git push
EOF
  exit 1
fi

# --- Gate 2: go vet -------------------------------------------------------

echo "pre-push: go vet ./... (static analysis)" >&2
if ! go vet ./... 2>&1; then
  cat >&2 <<'EOF'

✗ Push rejected: 'go vet ./...' reported issues.

Vet catches real bugs (shadow, printf, unreachable). Fix them or use
//nolint:vet on the specific line if the warning is a false positive.

Emergency escape hatch:
  GT_SKIP_PREPUSH=1 git push
EOF
  exit 1
fi

# --- Gate 3: go test (non-integration) ------------------------------------

echo "pre-push: go test ./... -count=1 (unit tests)" >&2
if ! go test ./... -count=1 -timeout 5m 2>&1; then
  cat >&2 <<'EOF'

✗ Push rejected: unit tests failed.

CI would reject the same failures with a ~5min round-trip cost. Fix or
skip the failing tests before pushing.

Tip: tests that relied on GT_TOWN_ROOT/GT_ROOT in the developer's shell
might pass locally without this gate but fail in CI. This script unsets
those vars to match CI — if a test unexpectedly fails here but passes
with those set, the test is the bug, not your change.

Emergency escape hatch:
  GT_SKIP_PREPUSH=1 git push
EOF
  exit 1
fi

echo "pre-push: all gates passed ✓" >&2
exit 0

#!/usr/bin/env bash
#
# curio-proposal-target-guard.sh — air-gap layer 2 (Curio P3 B6).
#
# Rejects a change request (CR) that proposes a rule or threshold TARGETING one
# of Curio's own telemetry series (`curio.*`). This is the mechanical, secondary
# air-gap from design-doc Q5: the Retrospect polecat must never propose a rule
# that detects Curio's own activity (a self-reference loop). Layer 1 (the
# `--emit-digest` substrate filter) keeps Curio's own series out of the agent's
# input in the first place; this guard is the enforcement backstop on the CR the
# agent ultimately produces, so the air-gap is enforcement, not just prose.
#
# What it checks: the lines a CR ADDS (vs. its merge base) for a `curio.*` series
# used as a detection target — i.e. a quoted `"curio.<series>"` literal in a
# non-test source/config file. The canonical hit is a threshold tune that adds a
# `"curio.cycle": 0` key to `daemon.json` `patrols.curio.rate_thresholds`, but
# any added `"curio.<series>"` literal (e.g. a new Go rule keyed on a Curio
# series) is flagged. A CR proposing a rule/threshold for a NON-Curio series adds
# no such literal and passes.
#
# Scope (deliberately precise, low false-positive):
#   - Only ADDED lines are inspected (base..HEAD diff), so pre-existing
#     references already on main never trip the guard.
#   - Test files (`*_test.go`, `*_test.sh`, anything under a `testdata/` or
#     `testfixtures/` path) are excluded: fixtures legitimately contain Curio
#     series to exercise the live air-gap (e.g. internal/curio/loopbreaker_test.go
#     uses a `"curio.cycle"` threshold to prove the rule suppresses it).
#   - The `CurioSeriesPrefix = "curio."` constant does NOT match: the pattern
#     requires at least one character after the dot (a concrete series name).
#
# Bead references: design-doc Q5 also air-gaps proposals that reference Curio's
# OWN beads. Bead IDs are opaque random tokens and cannot be statically
# pattern-matched, so that half of layer 2 is enforced upstream by layer 1's
# substrate filter (`Input.CurioBeads` causal-root exclusion) — see the design
# doc. This guard owns the `curio.*`-series half, which is statically decidable.
#
# Exit codes:
#   0 — Pass (no curio.*-targeting addition found, or nothing to diff)
#   1 — Block (a curio.*-targeting proposal was found in the CR's added lines)
#
# Configuration (environment variables):
#   GT_PROPOSAL_GUARD_BASE   — git ref to diff against (default: origin/main,
#                              falling back to HEAD~1 if origin/main is absent).
#   GT_PROPOSAL_GUARD_DISABLE=1 — disable the guard entirely (exit 0).
#
# Usage:
#   scripts/guards/curio-proposal-target-guard.sh            # diff vs origin/main
#   GT_PROPOSAL_GUARD_BASE=main scripts/guards/curio-proposal-target-guard.sh
#
# Wiring as a live merge-queue gate (runtime config, not committed):
#   gt rig settings set <rig> \
#     merge_queue.gates.curio-proposal-target \
#     '{"cmd":"scripts/guards/curio-proposal-target-guard.sh","phase":"pre-merge","timeout":"1m"}'
#   It is enabled together with the Retrospect lane (B5) so the air-gap is live
#   before any real polecat dispatches.

set -uo pipefail

[[ "${GT_PROPOSAL_GUARD_DISABLE:-}" == "1" ]] && exit 0

# --- Locate repo root -----------------------------------------------------
REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo "")
if [[ -z "$REPO_ROOT" ]]; then
  echo "curio-proposal-target-guard: not a git repo, skipping." >&2
  exit 0
fi
cd "$REPO_ROOT" || exit 1

# --- Resolve the diff base ------------------------------------------------
# Prefer an explicit override, then origin/main, then HEAD~1. If none resolve
# (e.g. a shallow single-commit checkout), there is nothing to diff: pass.
BASE="${GT_PROPOSAL_GUARD_BASE:-}"
if [[ -z "$BASE" ]]; then
  if git rev-parse --verify --quiet origin/main >/dev/null; then
    BASE="origin/main"
  elif git rev-parse --verify --quiet HEAD~1 >/dev/null; then
    BASE="HEAD~1"
  fi
fi
if [[ -z "$BASE" ]] || ! git rev-parse --verify --quiet "$BASE" >/dev/null; then
  echo "curio-proposal-target-guard: no diff base resolvable (base='$BASE'), nothing to check." >&2
  exit 0
fi

# --- Collect ADDED lines in the CR, excluding test/fixture files ----------
# `git diff <base>...HEAD` uses the merge-base so we only see what the CR adds,
# not unrelated commits that landed on the base since the branch forked.
# `-G` would over-match; we filter added lines (`^+`, not the `+++` header)
# ourselves so we can scope the regex and exclude test files by path.
#
# Pattern: a quoted curio.<series> literal — at least one char after the dot, so
# the bare `CurioSeriesPrefix = "curio."` constant never matches.
CURIO_TARGET_RE='"curio\.[A-Za-z0-9_.]+"'

# Path exclusions: test files and fixture trees legitimately reference Curio
# series to exercise the live air-gap.
is_excluded_path() {
  case "$1" in
    *_test.go|*_test.sh)        return 0 ;;
    */testdata/*|*/testfixtures/*) return 0 ;;
    *) return 1 ;;
  esac
}

violations=""
current_file=""
while IFS= read -r line; do
  # Track the current file from the diff's "+++ b/<path>" header.
  if [[ "$line" == "+++ "* ]]; then
    current_file="${line#+++ }"
    current_file="${current_file#b/}"
    continue
  fi
  # Skip the file's own "--- a/<path>" header line.
  [[ "$line" == "--- "* ]] && continue
  # Only added content lines (start with a single '+', not the '+++' header).
  [[ "$line" == +* ]] || continue
  content="${line:1}"
  [[ -n "$current_file" ]] && is_excluded_path "$current_file" && continue
  if [[ "$content" =~ $CURIO_TARGET_RE ]]; then
    violations+="  $current_file: ${content#"${content%%[![:space:]]*}"}"$'\n'
  fi
done < <(git diff "$BASE...HEAD" 2>/dev/null)

if [[ -n "$violations" ]]; then
  cat >&2 <<EOF

✗ CR rejected: it proposes a rule/threshold targeting a Curio-owned series (curio.*).

This is air-gap layer 2 (Curio P3, design-doc Q5): the Retrospect lane must never
propose a rule that detects Curio's OWN activity — that is a self-reference loop.
Curio reasons about Gas Town's failure surface, NOT about itself.

Offending added lines:
$violations
Fix: target a non-Curio series, or drop this proposal. If a curio.* reference is
genuinely needed in non-proposal code, put it in a test/fixture (excluded) or
file a bead explaining why the air-gap should not apply.
EOF
  exit 1
fi

echo "curio-proposal-target-guard: no curio.*-targeting proposal found ✓" >&2
exit 0

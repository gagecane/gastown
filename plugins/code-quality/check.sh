#!/usr/bin/env bash
# Condition gate for the code-quality plugin.
#
# Exits 0 to OPEN the gate (run the plugin), nonzero to keep it CLOSED.
#
# Two conditions must BOTH be true to open the gate:
#
#   1. COOLDOWN: ≥ 6h has elapsed since the last successful code-quality
#      run for this rig (and ≥ 6h since the last failure, so failures
#      don't hot-loop).
#
#   2. NEW COMMITS: The rig's default branch has advanced since the last
#      successful run (no point re-analyzing a codebase that hasn't changed).
#
# This script is invoked by the plugin gate evaluator from the rig's git
# worktree. It reads prior-run receipts via `bd list` (label-filtered) and
# reads the current HEAD via `git rev-parse`.
#
# Tunable via env:
#   COOLDOWN_HOURS  — minimum hours between runs (default 6)
#   RIG             — rig name to evaluate (default: derived from cwd or
#                     $GT_RIG)
#   DEFAULT_BRANCH  — branch whose advancement triggers re-analysis
#                     (default: origin/HEAD's symbolic ref → main)
#
# Diagnostics go to stderr; stdout is reserved for human-readable reasons
# the gate evaluator may surface in `gt plugin status` output.

set -euo pipefail

COOLDOWN_HOURS="${COOLDOWN_HOURS:-6}"
RIG="${RIG:-${GT_RIG:-}}"

# If RIG is not set, derive it from the current working directory.
# Fallback: assume the immediate parent dir of the rig root is named after
# the rig (e.g. /home/canewiw/gt/gastown_upstream/<...> → "gastown_upstream").
if [[ -z "$RIG" ]]; then
    if RIG_GUESS=$(git -C . rev-parse --show-toplevel 2>/dev/null); then
        # Walk up until we hit a directory under ~/gt/<rig>/...
        case "$RIG_GUESS" in
            "$HOME/gt/"*)
                RIG="${RIG_GUESS#"$HOME/gt/"}"
                RIG="${RIG%%/*}"
                ;;
        esac
    fi
fi

if [[ -z "$RIG" ]]; then
    echo "code-quality gate: cannot determine rig (set RIG or GT_RIG)" >&2
    exit 2
fi

# Resolve the rig's default branch ref. Try origin/HEAD first; fall back to main.
DEFAULT_BRANCH="${DEFAULT_BRANCH:-}"
if [[ -z "$DEFAULT_BRANCH" ]]; then
    if ref=$(git symbolic-ref refs/remotes/origin/HEAD 2>/dev/null); then
        DEFAULT_BRANCH="${ref#refs/remotes/origin/}"
    else
        DEFAULT_BRANCH="main"
    fi
fi

# Current HEAD on the default branch.
if ! CURRENT_COMMIT=$(git rev-parse "origin/${DEFAULT_BRANCH}" 2>/dev/null); then
    if ! CURRENT_COMMIT=$(git rev-parse "${DEFAULT_BRANCH}" 2>/dev/null); then
        echo "code-quality gate: cannot resolve ${DEFAULT_BRANCH} HEAD" >&2
        exit 2
    fi
fi

# Query the most recent code-quality receipt for this rig (success OR failure;
# both impose cooldown). The cooldown window is COOLDOWN_HOURS.
WINDOW="${COOLDOWN_HOURS}h"

# `bd list` with label filter; tolerate the case where no rows exist.
# We ask for receipts with this plugin's label and this rig label, in the
# cooldown window. If any exist, the cooldown is still in effect.
COOLDOWN_HITS=$(
    bd list --json --all \
        -l "type:plugin-run,plugin:code-quality,rig:${RIG}" \
        --created-after="-${WINDOW}" 2>/dev/null \
        | jq 'length' 2>/dev/null \
        || echo 0
)

if [[ "${COOLDOWN_HITS:-0}" -gt 0 ]]; then
    echo "cooldown: code-quality ran within ${WINDOW} for rig ${RIG}"
    exit 1
fi

# Cooldown window is clear. Now check whether the codebase has advanced
# since the last successful run. Read the most recent success receipt for
# this rig (any age) and compare its commit:<sha> label to current HEAD.
LAST_COMMIT=$(
    bd list --json --all \
        -l "type:plugin-run,plugin:code-quality,result:success,rig:${RIG}" \
        --limit 1 2>/dev/null \
        | jq -r '.[0].labels // [] | map(select(startswith("commit:"))) | .[0] // ""' 2>/dev/null \
        | sed 's/^commit://' \
        || echo ""
)

if [[ -n "$LAST_COMMIT" && "$LAST_COMMIT" == "$CURRENT_COMMIT" ]]; then
    echo "no-new-commits: ${DEFAULT_BRANCH} unchanged since last run (${LAST_COMMIT:0:8})"
    exit 1
fi

# Both conditions met. Gate is OPEN.
echo "open: cooldown=${WINDOW} elapsed; new commits since ${LAST_COMMIT:-none} → ${CURRENT_COMMIT:0:8}"
exit 0

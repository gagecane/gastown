#!/usr/bin/env bash
# Smoke test for casc-webapp-patrol-dispatch/run.sh.
#
# Purpose: catch `gt sling` API drift before the plugin ships.
#
# Lesson from wiki-patrol-dispatch (gu-fc8h, gu-xd7b): that plugin shipped twice
# with broken `gt sling` invocations, silent until the daemon scheduled the next
# run. This test asserts the API contract the plugin depends on:
#
#   1. `gt sling --formula <formula> <rig> --create --var key=value` is the
#      shape the script invokes. The flags must all be recognized.
#   2. The script passes the vars the formula requires (project_path, target_url).
#   3. `gt sling --help` still documents the flags we use.
#
# We don't actually dispatch (that would create real beads). We grep run.sh for
# the invocation shape and parse `gt sling --help` for the flags.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RUN_SH="$SCRIPT_DIR/run.sh"
FAILURES=0

fail() {
  echo "FAIL: $*"
  FAILURES=$((FAILURES + 1))
}

# --- Assert the plugin script invokes gt sling with --formula ---------------
echo "=== run.sh invocation shape ==="

if ! grep -qE 'gt sling --formula "\$FORMULA" "\$TARGET_RIG"' "$RUN_SH"; then
  fail "run.sh does not invoke 'gt sling --formula \"\$FORMULA\" \"\$TARGET_RIG\"'"
  fail "  History: gu-xd7b — without --formula, gt sling treats the formula"
  fail "  name as a bead-id and fails 'bead not found'"
  grep -nE 'gt sling' "$RUN_SH" | head -5
fi

if ! grep -qE -- '--var "project_path=' "$RUN_SH"; then
  fail "run.sh does not pass --var project_path=... (required by the formula)"
fi

if ! grep -qE -- '--var "target_url=' "$RUN_SH"; then
  fail "run.sh does not pass --var target_url=..."
fi

if ! grep -qE -- '--create' "$RUN_SH"; then
  fail "run.sh does not pass --create (polecat auto-spawn flag)"
fi

# --- Assert single-sling (not a per-stage loop) -----------------------------
echo "=== run.sh single-target shape ==="

# This patrol targets ONE url; it must NOT carry a Beta/Gamma/Prod stage loop
# (that's casc-patrol's shape, not this one).
if grep -qE '"(Beta|Gamma|Prod)"' "$RUN_SH"; then
  fail "run.sh references AWS stages — this is a single-URL web patrol, not a per-stage sweep"
fi

# --- Assert the gt sling API still exposes the flags we use -----------------
echo "=== gt sling --help API contract ==="

if ! command -v gt >/dev/null 2>&1; then
  echo "SKIP: gt CLI not on PATH — can't verify API contract"
else
  help_out=$(gt sling --help 2>&1 || true)
  for flag in --formula --create --var; do
    if ! grep -qE "^\s*${flag}\b" <<<"$help_out"; then
      fail "gt sling --help no longer documents '${flag}' flag — update run.sh to match"
    fi
  done
fi

# --- Summary ----------------------------------------------------------------

echo ""
if [[ $FAILURES -gt 0 ]]; then
  echo "FAILED: $FAILURES check(s) failed"
  exit 1
else
  echo "PASSED: all checks passed"
fi

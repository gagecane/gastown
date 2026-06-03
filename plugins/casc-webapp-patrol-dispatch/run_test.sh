#!/usr/bin/env bash
# Smoke test for casc-webapp-patrol-dispatch/run.sh.
#
# Purpose: catch `gt sling` API drift before the plugin ships.
#
# Lesson from wiki-patrol-dispatch (gu-fc8h, gu-xd7b): that plugin shipped twice
# with broken `gt sling` invocations, silent until the daemon scheduled the next
# run. This test asserts the API contract the plugin depends on:
#
#   1. `gt sling <formula> <rig> --create --var key=value` is the shape the
#      script invokes — the formula is the FIRST POSITIONAL arg, not a flag.
#      The --formula flag form fails "deferred dispatch requires a rig target"
#      (gu-ono8h).
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

# --- Assert the plugin invokes gt sling with the formula POSITIONAL ---------
echo "=== run.sh invocation shape ==="

if ! grep -qE 'gt sling "\$FORMULA" "\$TARGET_RIG"' "$RUN_SH"; then
  fail "run.sh does not invoke 'gt sling \"\$FORMULA\" \"\$TARGET_RIG\"' (formula positional)"
  fail "  History: gu-ono8h — gt sling takes the formula as the FIRST positional"
  fail "  arg: 'gt sling <formula> <rig>'. The flag form broke every patrol run."
  grep -nE 'gt sling' "$RUN_SH" | head -5
fi

# Guard against regressing to the broken --formula flag form (gu-ono8h).
if grep -qE 'gt sling --formula' "$RUN_SH"; then
  fail "run.sh uses the --formula FLAG — that consumes the rig as the bead-to-sling"
  fail "  and fails 'deferred dispatch requires a rig target' (gu-ono8h). Use the"
  fail "  positional form: gt sling \"\$FORMULA\" \"\$TARGET_RIG\""
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
  for flag in --create --var; do
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

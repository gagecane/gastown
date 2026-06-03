#!/usr/bin/env bash
# Smoke test for casc-patrol-dispatch/run.sh.
#
# Purpose: catch `gt sling` API drift before the plugin ships.
#
# Lesson from wiki-patrol-dispatch: that plugin shipped twice with broken
# `gt sling` invocations (gu-fc8h, gu-xd7b). Both failures were silent until
# the daemon scheduled the next run. This test asserts the API contract the
# plugin depends on:
#
#   1. `gt sling <formula> <rig> --create --var key=value` is the shape the
#      script invokes — the formula is the FIRST POSITIONAL arg, not a flag.
#   2. The script does NOT use the --formula flag (gu-ono8h): that flag is a
#      separate apply-on-bead feature; passing it makes gt sling read the rig
#      as the bead-to-sling and fail "deferred dispatch requires a rig target".
#   3. The script loops over the three stages (Beta, Gamma, Prod).
#
# We don't actually dispatch a workflow (that would create real beads). We
# parse `gt sling --help` and grep for the flags, and we grep run.sh for
# the invocation shape.

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

if ! grep -qE -- '--var "stage=' "$RUN_SH"; then
  fail "run.sh does not pass --var stage=..."
fi

if ! grep -qE -- '--var "project_path=' "$RUN_SH"; then
  fail "run.sh does not pass --var project_path=..."
fi

if ! grep -qE -- '--create' "$RUN_SH"; then
  fail "run.sh does not pass --create (polecat auto-spawn flag)"
fi

# --- Assert the script covers all three stages ------------------------------
echo "=== run.sh stage coverage ==="

for stage in Beta Gamma Prod; do
  if ! grep -qE "\"$stage\"" "$RUN_SH"; then
    fail "run.sh does not include stage \"$stage\" in the STAGES array"
  fi
done

# --- Assert the gt sling API still exposes the flags we use -----------------
echo "=== gt sling --help API contract ==="

if ! command -v gt >/dev/null 2>&1; then
  echo "SKIP: gt CLI not on PATH — can't verify API contract"
  echo "      (this test is most useful when run by the daemon or refinery,"
  echo "      which always have gt available)"
else
  help_out=$(gt sling --help 2>&1 || true)

  for flag in --create --var; do
    if ! grep -qE "^\s*${flag}\b" <<<"$help_out"; then
      fail "gt sling --help no longer documents '${flag}' flag"
      fail "  the plugin will break — update run.sh to match the new API"
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

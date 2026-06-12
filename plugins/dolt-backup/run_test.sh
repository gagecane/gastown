#!/usr/bin/env bash
# Tests for dolt-backup/run.sh pure predicate helpers.
#
# Convention (per plugins/orphan-reaper/run_test.sh): the test keeps its OWN
# copy of each pure helper and a drift-guard that sed-extracts the same function
# from run.sh and asserts byte-for-byte equality. If run.sh changes a helper
# without the test being updated, the drift-guard fails — so the assertions
# below always exercise behavior identical to the shipped plugin.
#
# NOT run by CI (.github/workflows/ci.yml runs only `go test`). Run manually:
#   bash plugins/dolt-backup/run_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TEST_FILE="$SCRIPT_DIR/$(basename "$0")"
RUN_SH="$SCRIPT_DIR/run.sh"
FAILURES=0

# === Test's copy of the pure helper (drift-guarded against run.sh below) ===

is_rig_parked() {
  local db="$1" wisp_file="$WISP_CONFIG_DIR/$1.json"
  [[ -f "$wisp_file" ]] || return 1
  grep -Eq '"status"[[:space:]]*:[[:space:]]*"parked"' "$wisp_file"
}

# === Drift-guard: the copied helper must match run.sh verbatim ===

echo "=== drift-guard (test copy vs run.sh) ==="
for fn in is_rig_parked; do
  run_copy=$(sed -n "/^${fn}() {/,/^}/p" "$RUN_SH")
  test_copy=$(sed -n "/^${fn}() {/,/^}/p" "$TEST_FILE")
  if [ -z "$run_copy" ]; then
    echo "FAIL: could not extract '$fn' from run.sh"
    FAILURES=$((FAILURES + 1))
  elif [ "$run_copy" != "$test_copy" ]; then
    echo "DRIFT: '$fn' in run_test.sh does not match run.sh — update the test copy"
    echo "--- run.sh ---"; printf '%s\n' "$run_copy"
    echo "--- run_test.sh ---"; printf '%s\n' "$test_copy"
    FAILURES=$((FAILURES + 1))
  fi
done

# === Test fixtures: a temp wisp config dir with parked + active rigs ===

WISP_CONFIG_DIR="$(mktemp -d)"
trap 'rm -rf "$WISP_CONFIG_DIR"' EXIT

# Parked rig (canonical shape written by `gt rig park`).
cat > "$WISP_CONFIG_DIR/parkedrig.json" <<'JSON'
{
  "rig": "parkedrig",
  "values": {
    "status": "parked"
  },
  "blocked": []
}
JSON

# Active rig: wisp file exists but no status key.
cat > "$WISP_CONFIG_DIR/activerig.json" <<'JSON'
{
  "rig": "activerig",
  "values": {},
  "blocked": []
}
JSON

# Rig explicitly marked operational (not parked).
cat > "$WISP_CONFIG_DIR/oprig.json" <<'JSON'
{
  "rig": "oprig",
  "values": {
    "status": "operational"
  },
  "blocked": []
}
JSON

# === Assertion helpers ===

assert_parked() {
  local db="$1" desc="$2"
  if ! is_rig_parked "$db"; then
    echo "FAIL: $desc: expected PARKED for '$db'"
    FAILURES=$((FAILURES + 1))
  fi
}

assert_not_parked() {
  local db="$1" desc="$2"
  if is_rig_parked "$db"; then
    echo "FAIL: $desc: expected NOT parked for '$db'"
    FAILURES=$((FAILURES + 1))
  fi
}

# === Assertions ===

echo "=== is_rig_parked tests ==="

assert_parked     "parkedrig" "rig with status=parked"
assert_not_parked "activerig" "rig present but no status key"
assert_not_parked "oprig"     "rig with status=operational"
assert_not_parked "ghostrig"  "rig with no wisp config file at all (the gu-otphy case for fresh parked rigs without state)"

# === Result ===

echo ""
if [ "$FAILURES" -gt 0 ]; then
  echo "FAILED: $FAILURES check(s) failed"
  exit 1
else
  echo "PASSED: all checks passed"
fi

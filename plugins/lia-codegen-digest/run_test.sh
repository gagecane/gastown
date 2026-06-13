#!/usr/bin/env bash
# Smoke test for lia-codegen-digest/run.sh.
#
# Purpose: catch contract drift before the plugin ships on its weekly cron.
# These are static assertions on the script + frontmatter (no network), plus
# an optional dry-run integration check when `gh` is authenticated locally.
#
# What we guard:
#   1. run.sh is valid bash and self-contained.
#   2. The gh queries use the field/search shape the metric math depends on.
#   3. The beads window filter uses an ABSOLUTE YYYY-MM-DD date — `bd
#      --created-after` documents that form; a relative "-7d" is silently
#      misparsed (caught during build).
#   4. All four metrics are present.
#   5. plugin.md declares a weekly cron gate and script execution.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RUN_SH="$SCRIPT_DIR/run.sh"
PLUGIN_MD="$SCRIPT_DIR/plugin.md"
FAILURES=0

fail() { echo "FAIL: $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "ok: $*"; }

# --- 1. run.sh is valid bash ------------------------------------------------
echo "=== run.sh structure ==="
if bash -n "$RUN_SH"; then pass "run.sh parses"; else fail "run.sh has syntax errors"; fi
[ -x "$RUN_SH" ] && pass "run.sh is executable" || fail "run.sh is not executable"

# --- 2. gh query shape ------------------------------------------------------
echo "=== gh query shape ==="
grep -q 'gh pr list' "$RUN_SH" && pass "uses gh pr list" \
  || fail "run.sh does not call gh pr list"
grep -q -- '--search "merged:>=' "$RUN_SH" && pass "filters merged PRs by window" \
  || fail "run.sh does not filter PRs with --search merged:>="
for f in number createdAt mergedAt reviews; do
  grep -qE "json[^\"]*$f|,$f|$f," "$RUN_SH" && pass "requests json field: $f" \
    || fail "run.sh does not request gh json field: $f"
done
grep -q 'pulls/.*comments' "$RUN_SH" && pass "fetches inline review comments" \
  || fail "run.sh does not fetch review comments (metric 2)"

# --- 3. absolute date for beads window (gu: -7d is misparsed) ---------------
echo "=== beads window filter ==="
grep -q -- '--created-after="\$SINCE"' "$RUN_SH" \
  && pass "uses absolute \$SINCE (YYYY-MM-DD) for --created-after" \
  || fail "run.sh must pass an absolute YYYY-MM-DD date to bd --created-after"
if grep -qE -- '--created-after="?-[0-9]+d' "$RUN_SH"; then
  fail "run.sh uses a relative -Nd --created-after; bd wants YYYY-MM-DD/RFC3339"
fi

# --- 4. all four metrics present --------------------------------------------
echo "=== four metrics ==="
grep -q 'First-pass approval rate' "$RUN_SH" && pass "metric 1 present" \
  || fail "metric 1 (first-pass approval rate) missing"
grep -q 'Review comments per PR' "$RUN_SH" && pass "metric 2 present" \
  || fail "metric 2 (review comments per PR) missing"
grep -q 'Gate failure rate' "$RUN_SH" && pass "metric 3 present" \
  || fail "metric 3 (gate failure rate) missing"
grep -q 'time-to-merge' "$RUN_SH" && pass "metric 4 present" \
  || fail "metric 4 (time-to-merge) missing"

# --- 5. plugin.md frontmatter -----------------------------------------------
echo "=== plugin.md frontmatter ==="
grep -q 'type = "cron"' "$PLUGIN_MD" && pass "cron gate declared" \
  || fail "plugin.md missing cron gate"
grep -qE 'schedule = "[0-9 *]+\* 1"' "$PLUGIN_MD" && pass "weekly (Monday) schedule" \
  || fail "plugin.md schedule is not weekly-on-Monday (… * 1)"
grep -q 'type = "script"' "$PLUGIN_MD" && pass "script execution declared" \
  || fail "plugin.md missing script execution type"

# --- 6. optional dry-run integration ----------------------------------------
# Opt-in only (DIGEST_TEST_INTEGRATION=1): makes real gh API calls, so it is
# kept out of the default `make test-makefile` path which must be network-free.
echo "=== dry-run integration (opt-in) ==="
if [ -z "${DIGEST_TEST_INTEGRATION:-}" ]; then
  echo "skip: set DIGEST_TEST_INTEGRATION=1 to run the live dry-run"
elif gh auth status >/dev/null 2>&1; then
  OUT=$(DIGEST_DRY_RUN=1 bash "$RUN_SH" 2>&1) || true
  if echo "$OUT" | grep -q 'Codegen Quality Digest'; then
    pass "dry-run emitted a digest"
  else
    fail "dry-run produced no digest output"
    echo "$OUT" | tail -10
  fi
  echo "$OUT" | grep -q 'dry-run: skip' && pass "dry-run wrote no beads" \
    || fail "dry-run did not honor the no-write guard"
else
  echo "skip: gh not authenticated — static checks only"
fi

# --- Summary ----------------------------------------------------------------
echo
if [ "$FAILURES" -eq 0 ]; then
  echo "PASS: lia-codegen-digest smoke test"
  exit 0
fi
echo "FAILED: $FAILURES assertion(s)"
exit 1

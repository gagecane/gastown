#!/usr/bin/env bash
# Test for curio-dispatch-reconcile/run.sh (gu-l84k2 fix #3, parent gu-ac2bu).
#
# Two layers:
#
#   A. STATIC shape assertions — plugin.md gate/timeout + run.sh contract
#      (receipt labels, digest-dir anchoring, the result:warning path exists).
#
#   B. FUNCTIONAL harness — run.sh end-to-end against a temp town root with a
#      stubbed bd/gt on PATH and a hand-written digest file, asserting the four
#      verdict paths plus the in-flight floor:
#        1. no digest in range            -> result:skipped
#        2. digest with 0 clusters         -> result:success (quiet night)
#        3. actionable digest, >=1 filed   -> result:success (write verified)
#        4. actionable digest, 0 filed, all clusters deduped -> result:success
#        5. actionable digest, 0 filed, uncovered -> result:warning + escalate
#        6. digest too FRESH (< min-age)   -> result:skipped (in flight, no warn)
#
# The anti-false-positive invariant (cases 2/4/6 must NOT warn) is the point of
# the bug fix, so the harness asserts the exact result: label, not just "ran".
#
# Run:  bash plugins/curio-dispatch-reconcile/run_test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RUN_SH="$SCRIPT_DIR/run.sh"
PLUGIN_MD="$SCRIPT_DIR/plugin.md"
FAILURES=0

fail() { echo "FAIL: $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "ok: $*"; }

# ============================================================================
# A. STATIC shape assertions
# ============================================================================
echo "=== A. run.sh / plugin.md static shape ==="

# plugin.md frontmatter: cron gate at 09:00 UTC (1h after the 08:00 dispatch),
# 10m timeout, notify_on_failure.
grep -qE '^schedule = "0 9 \* \* \*"' "$PLUGIN_MD" || fail "plugin.md cron schedule is not '0 9 * * *' (1h after the 08:00 dispatch)"
grep -qE '^type = "cron"' "$PLUGIN_MD" || fail "plugin.md gate type is not cron"
grep -qE '^timeout = "10m"' "$PLUGIN_MD" || fail "plugin.md execution timeout is not 10m"

# Receipt label contract: type:plugin-run + plugin:<name> + result:<r>.
grep -qE 'type:plugin-run,plugin:\$\{PLUGIN_NAME\},result:' "$RUN_SH" || \
  fail "run.sh receipt does not carry the type:plugin-run,plugin:,result: label triple"

# Digest dir must be anchored under the town root (same host-shared path B5 wrote).
grep -qE 'DIGEST_DIR=.*TOWN_ROOT' "$RUN_SH" || \
  fail "run.sh digest dir is not anchored under \$TOWN_ROOT"

# The warning path (the actual bug fix) must exist.
grep -qE 'record_receipt "warning"' "$RUN_SH" || \
  fail "run.sh has no result:warning path — the gu-ac2bu silent-drop signal is missing"

# Min-age floor must exceed the 30m formula timeout (so an in-flight run is
# never mis-flagged). Default is 2400s = 40m.
grep -qE 'MIN_AGE_SECS="\$\{CURIO_RECONCILE_MIN_AGE_SECS:-2400\}"' "$RUN_SH" || \
  fail "run.sh min-age floor is not the expected 2400s (40m, > the 30m formula timeout)"

# One-shot escalation deduped on the digest stamp.
grep -qE 'gt escalate' "$RUN_SH" || fail "run.sh does not emit a gt escalate on the gap"
grep -qE -- '--dedup' "$RUN_SH" || fail "run.sh escalate is not deduped (--dedup) — would spam"

# ============================================================================
# B. FUNCTIONAL harness
# ============================================================================
echo ""
echo "=== B. run.sh functional verdict paths ==="

if ! command -v jq >/dev/null 2>&1; then
  echo "SKIP: jq not on PATH — functional harness requires it"
else

STUBS="$(mktemp -d)"
trap 'rm -rf "$STUBS"' EXIT

# gt stub: answer `gt town root`; record `gt escalate` calls to $GT_ESCALATIONS.
cat >"$STUBS/gt" <<'STUB'
#!/usr/bin/env bash
if [[ "${1:-}" == "town" && "${2:-}" == "root" ]]; then
  echo "$GT_TOWN_ROOT"; exit 0
fi
if [[ "${1:-}" == "escalate" ]]; then
  printf '%s\n' "$*" >>"$GT_ESCALATIONS"
  exit 0
fi
exit 0
STUB

# bd stub: `bd create` records the receipt label to $BD_RECEIPTS; `bd list`
# answers from env JSON, branching on --status open (dedup query) vs --all
# (filed-since query). Both proposal/hypothesis labels share fixtures here; the
# harness sets them per-case.
cat >"$STUBS/bd" <<'STUB'
#!/usr/bin/env bash
args="$*"
if [[ "${1:-}" == "create" ]]; then
  # Capture the result: label from the -l triple so the harness can assert it.
  printf '%s\n' "$args" >>"$BD_RECEIPTS"
  exit 0
fi
if [[ "${1:-}" == "list" ]]; then
  if [[ "$args" == *"--status open"* ]]; then
    printf '%s' "${BD_OPEN_JSON:-[]}"; exit 0
  fi
  if [[ "$args" == *"--all"* ]]; then
    printf '%s' "${BD_FILED_JSON:-[]}"; exit 0
  fi
  echo "[]"; exit 0
fi
exit 0
STUB

chmod +x "$STUBS/gt" "$STUBS/bd"

# write_digest TOWN STAMP CLUSTERS_JSON — write a digest-<STAMP>.md whose JSON
# block carries the given clusters array.
write_digest() {
  local town="$1" stamp="$2" clusters="$3"
  local dir="$town/artifacts/curio-retrospect"
  mkdir -p "$dir"
  {
    printf '# Curio digest (test)\n\n## Unresolved candidate clusters\n\n'
    printf '```json\n'
    jq -nc --argjson cl "$clusters" '{cutoff:"x", rules_with_precision:0, rules:[], clusters:$cl}'
    printf '\n```\n'
  } >"$dir/digest-${stamp}.md"
}

# stamp_for SECS_AGO — a digest filename stamp SECS_AGO in the past (UTC).
stamp_for() {
  date -u -d "@$(( $(date -u +%s) - $1 ))" +%Y%m%dT%H%M%SZ
}

# run_case NAME EXPECT_RESULT EXPECT_ESCALATE(yes|no) — set up a town with the
# per-case digest + bd fixtures (already exported), run run.sh, assert the
# recorded receipt's result: label and whether an escalation fired.
run_case() {
  local name="$1" expect_result="$2" expect_esc="$3"
  local town; town="$(mktemp -d)"
  mkdir -p "$town/mayor" "$town/$RIG_DIR_NAME"

  # The per-case setup hook writes the digest into $town.
  CASE_TOWN="$town" eval "$CASE_SETUP"

  local receipts esc; receipts="$(mktemp)"; esc="$(mktemp)"
  GT_TOWN_ROOT="$town" GT_CALLS="$receipts" BD_RECEIPTS="$receipts" GT_ESCALATIONS="$esc" \
    PATH="$STUBS:$PATH" \
    bash "$RUN_SH" >/dev/null 2>&1

  # Extract the result: label from the (single) recorded receipt.
  local got_result; got_result="$(grep -oE 'result:[a-z]+' "$receipts" 2>/dev/null | head -1 | sed 's/result://')"
  [[ -n "$got_result" ]] || got_result="(none)"
  if [[ "$got_result" == "$expect_result" ]]; then
    pass "$name → result:$got_result"
  else
    fail "$name → result:$got_result (expected result:$expect_result)"
  fi

  # Assert escalation presence.
  local got_esc="no"; [[ -s "$esc" ]] && got_esc="yes"
  if [[ "$got_esc" == "$expect_esc" ]]; then
    pass "$name → escalate=$got_esc"
  else
    fail "$name → escalate=$got_esc (expected $expect_esc)"
  fi

  rm -rf "$town" "$receipts" "$esc"
}

RIG_DIR_NAME="gastown_upstream"
TERMINATED_AGO=3600   # 1h ago: past the 40m min-age floor, within 6h lookback.
FRESH_AGO=600         # 10m ago: inside the min-age floor → still in flight.

# --- 1. no digest in range → skipped ----------------------------------------
CASE_SETUP=':'   # write no digest at all
BD_OPEN_JSON='[]'; BD_FILED_JSON='[]'; export BD_OPEN_JSON BD_FILED_JSON
run_case "no digest in range" "skipped" "no"

# --- 2. digest with 0 clusters → success (quiet night) -----------------------
CASE_SETUP='write_digest "$CASE_TOWN" "$(stamp_for '"$TERMINATED_AGO"')" "[]"'
BD_OPEN_JSON='[]'; BD_FILED_JSON='[]'; export BD_OPEN_JSON BD_FILED_JSON
run_case "quiet night (0 clusters)" "success" "no"

# --- 3. actionable digest, >=1 filed since dispatch → success ----------------
CASE_SETUP='write_digest "$CASE_TOWN" "$(stamp_for '"$TERMINATED_AGO"')" "[{\"cluster_id\":\"aaa111\",\"rule_id\":\"r\",\"series\":\"s\",\"occurrences\":3,\"summaries\":[]}]"'
BD_OPEN_JSON='[]'
# A bead filed 30m ago (after the 1h-ago dispatch). created_at must be >= dispatch.
FILED_TS="$(date -u -d "@$(( $(date -u +%s) - 1800 ))" +%Y-%m-%dT%H:%M:%SZ)"
BD_FILED_JSON="$(jq -nc --arg ts "$FILED_TS" '[{"id":"gu-f1","created_at":$ts,"labels":["curio-hypothesis","cluster:aaa111"]}]')"
export BD_OPEN_JSON BD_FILED_JSON
run_case "actionable + filed (write verified)" "success" "no"

# --- 4. actionable digest, 0 filed, all clusters already covered → success ---
CASE_SETUP='write_digest "$CASE_TOWN" "$(stamp_for '"$TERMINATED_AGO"')" "[{\"cluster_id\":\"bbb222\",\"rule_id\":\"r\",\"series\":\"s\",\"occurrences\":3,\"summaries\":[]}]"'
# Open bead already covers cluster bbb222; nothing filed in the window.
BD_OPEN_JSON="$(jq -nc '[{"id":"gu-open1","labels":["curio-proposal","cluster:bbb222"]}]')"
BD_FILED_JSON='[]'; export BD_OPEN_JSON BD_FILED_JSON
run_case "actionable but fully deduped" "success" "no"

# --- 5. actionable digest, 0 filed, uncovered → warning + escalate -----------
CASE_SETUP='write_digest "$CASE_TOWN" "$(stamp_for '"$TERMINATED_AGO"')" "[{\"cluster_id\":\"ccc333\",\"rule_id\":\"r\",\"series\":\"s\",\"occurrences\":9,\"summaries\":[]}]"'
BD_OPEN_JSON='[]'; BD_FILED_JSON='[]'; export BD_OPEN_JSON BD_FILED_JSON
run_case "silent drop (gu-ac2bu signature)" "warning" "yes"

# --- 6. digest too fresh (< min-age) → skipped (in flight, never warn) --------
CASE_SETUP='write_digest "$CASE_TOWN" "$(stamp_for '"$FRESH_AGO"')" "[{\"cluster_id\":\"ddd444\",\"rule_id\":\"r\",\"series\":\"s\",\"occurrences\":9,\"summaries\":[]}]"'
BD_OPEN_JSON='[]'; BD_FILED_JSON='[]'; export BD_OPEN_JSON BD_FILED_JSON
run_case "fresh digest (still in flight)" "skipped" "no"

fi  # jq present

# ============================================================================
echo ""
if [[ $FAILURES -gt 0 ]]; then
  echo "FAILED: $FAILURES check(s) failed"
  exit 1
else
  echo "PASSED: all checks passed"
fi

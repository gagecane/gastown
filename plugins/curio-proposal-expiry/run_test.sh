#!/usr/bin/env bash
# Test for curio-proposal-expiry/run.sh (Curio P3 B8, gu-5zf4t).
#
# Two layers, mirroring curio-retrospect-dispatch/run_test.sh (B5):
#
#   A. STATIC shape assertions — the cron schedule, the ceiling/expiry knobs,
#      the curio-outcome:<code> stamp, and the air-gap invariant (no direct
#      ledger write) are all present and named as documented.
#
#   B. FUNCTIONAL harness — run.sh end-to-end against a temp town root with
#      stubbed bd / gt on PATH, asserting:
#        EXPIRY:
#          1. a stale open proposal (updated_at past the window) is closed, and
#             the close is preceded by a curio-outcome:<code> label stamp;
#          2. a FRESH open proposal is NOT closed;
#          3. an in_progress bead is never swept (only status==open).
#        BREAKER:
#          4. breaker open >= M days with no prior alert → fires exactly one
#             escalation and latches alerted=true;
#          5. breaker open >= M days but already alerted → NO second escalation
#             (exactly once per trip);
#          6. breaker closed → state file cleared (trip clock + latch reset).
#
# Run:  bash plugins/curio-proposal-expiry/run_test.sh

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

# Cron gate: 07:30 UTC, BEFORE the 08:00 dispatch.
grep -qE '^schedule = "30 7 \* \* \*"' "$PLUGIN_MD" || fail "plugin.md cron schedule is not '30 7 * * *'"
grep -qE '^type = "cron"' "$PLUGIN_MD" || fail "plugin.md gate type is not cron"

# Expiry knobs.
grep -qE 'CURIO_PROPOSAL_EXPIRY_DAYS' "$RUN_SH" || fail "run.sh does not read CURIO_PROPOSAL_EXPIRY_DAYS"
grep -qE 'CURIO_EXPIRY_OUTCOME' "$RUN_SH" || fail "run.sh does not read CURIO_EXPIRY_OUTCOME"

# Breaker knobs — must reuse the SAME ceiling var B5 uses.
grep -qE 'CURIO_PROPOSAL_CEILING' "$RUN_SH" || fail "run.sh does not read CURIO_PROPOSAL_CEILING (must match B5)"
grep -qE 'CURIO_BREAKER_ALERT_DAYS' "$RUN_SH" || fail "run.sh does not read CURIO_BREAKER_ALERT_DAYS"

# Outcome stamp: the close must carry a structured curio-outcome:<code> label so
# the B0b reconciler classifies deterministically.
grep -qE 'curio-outcome:' "$RUN_SH" || fail "run.sh does not stamp a curio-outcome:<code> label before close"

# Air-gap invariant: this plugin must NOT write the ledger directly (no SQL / no
# Dolt write). It only closes beads; the reconciler writes the ledger. Match on
# actual write SQL / Dolt-commit verbs (not the word "curio_ledger", which the
# documentation comments legitimately reference).
if grep -qiE 'INSERT (IGNORE )?INTO|UPDATE [a-z_]*ledger|DOLT_COMMIT|DOLT_ADD' "$RUN_SH"; then
  fail "run.sh appears to write the ledger directly — B8 must feed it via bd close + the B0b reconciler"
fi

# Breaker alert is a single escalation deduped on a stable signature.
grep -qE 'gt escalate' "$RUN_SH" || fail "run.sh does not emit a gt escalate for a wedged breaker"
grep -qE -- '--signature' "$RUN_SH" || fail "run.sh escalation lacks a stable --signature (dedup)"

# ============================================================================
# B. FUNCTIONAL harness
# ============================================================================
echo ""
echo "=== B. run.sh functional expiry / breaker paths ==="

if ! command -v jq >/dev/null 2>&1; then
  echo "SKIP: jq not on PATH — functional harness requires it"
else

STUBS="$(mktemp -d)"
trap 'rm -rf "$STUBS"' EXIT

# bd stub: records `bd update`/`bd close` calls to $BD_CALLS; answers `bd list`
# from per-label env JSON so expiry and breaker get distinct fixtures.
cat >"$STUBS/bd" <<'STUB'
#!/usr/bin/env bash
args="$*"
case "$args" in
  create*) exit 0 ;;
  sync*)   exit 0 ;;
  update*) printf 'update %s\n' "$args" >>"$BD_CALLS"; exit 0 ;;
  close*)  printf 'close %s\n'  "$args" >>"$BD_CALLS"; exit 0 ;;
esac
if [[ "$args" == *"curio-hypothesis"* ]]; then
  printf '%s' "${BD_HYP_JSON:-[]}"; exit 0
fi
if [[ "$args" == *"curio-proposal"* ]]; then
  printf '%s' "${BD_PROP_JSON:-[]}"; exit 0
fi
echo "[]"
STUB

# gt stub: answers `gt town root`; logs `gt escalate` invocations to $GT_CALLS.
cat >"$STUBS/gt" <<'STUB'
#!/usr/bin/env bash
if [[ "${1:-}" == "town" && "${2:-}" == "root" ]]; then
  echo "$GT_TOWN_ROOT"; exit 0
fi
if [[ "${1:-}" == "escalate" ]]; then
  printf '%s\n' "$*" >>"$GT_CALLS"
  echo "escalated hq-esc-test01"
  exit 0
fi
exit 0
STUB

chmod +x "$STUBS/bd" "$STUBS/gt"

NOW_TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
STALE_TS=$(date -u -d "20 days ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
  || date -u -v-20d +%Y-%m-%dT%H:%M:%SZ)
FRESH_TS=$(date -u -d "2 days ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
  || date -u -v-2d +%Y-%m-%dT%H:%M:%SZ)

RIG_DIR_NAME="gastown_upstream"

# new_town — make a fresh temp town root with the rig dir; echo its path.
new_town() {
  local town; town="$(mktemp -d)"
  mkdir -p "$town/$RIG_DIR_NAME" "$town/artifacts/curio-retrospect"
  echo "$town"
}

# run_plugin TOWN — run run.sh against TOWN with the stubs + current env.
run_plugin() {
  local town="$1"
  GT_TOWN_ROOT="$town" PATH="$STUBS:$PATH" bash "$RUN_SH" >/dev/null 2>&1
}

# --- 1. stale proposal → closed with outcome stamp --------------------------
{
  town="$(new_town)"
  BD_CALLS="$(mktemp)"; GT_CALLS="$(mktemp)"
  export BD_CALLS GT_CALLS
  BD_PROP_JSON="$(jq -nc --arg ts "$STALE_TS" \
    '[{"id":"gu-stale1","status":"open","updated_at":$ts,"labels":["curio-proposal"]}]')"
  BD_HYP_JSON='[]'
  export BD_PROP_JSON BD_HYP_JSON
  run_plugin "$town"

  if grep -q 'close .*gu-stale1' "$BD_CALLS" 2>/dev/null; then
    pass "stale proposal gu-stale1 was closed"
  else
    fail "stale proposal gu-stale1 was NOT closed"; cat "$BD_CALLS"
  fi
  if grep -q 'update .*gu-stale1.*curio-outcome:' "$BD_CALLS" 2>/dev/null; then
    pass "expiry stamped a curio-outcome:<code> label before close"
  else
    fail "expiry did not stamp curio-outcome:<code> on gu-stale1"; cat "$BD_CALLS"
  fi
  rm -rf "$town" "$BD_CALLS" "$GT_CALLS"
}

# --- 2. fresh proposal → NOT closed -----------------------------------------
{
  town="$(new_town)"
  BD_CALLS="$(mktemp)"; GT_CALLS="$(mktemp)"
  export BD_CALLS GT_CALLS
  BD_PROP_JSON="$(jq -nc --arg ts "$FRESH_TS" \
    '[{"id":"gu-fresh1","status":"open","updated_at":$ts,"labels":["curio-proposal"]}]')"
  BD_HYP_JSON='[]'
  export BD_PROP_JSON BD_HYP_JSON
  run_plugin "$town"

  if grep -q 'close .*gu-fresh1' "$BD_CALLS" 2>/dev/null; then
    fail "fresh proposal gu-fresh1 was closed (should be left alone)"; cat "$BD_CALLS"
  else
    pass "fresh proposal gu-fresh1 was NOT closed"
  fi
  rm -rf "$town" "$BD_CALLS" "$GT_CALLS"
}

# --- 3. in_progress bead → never swept (list returns nothing for status open) -
# The plugin lists with --status open; an in_progress bead is excluded by the
# query. We model that by having the proposal fixture be empty for the open
# query even though a stale-looking bead exists, and assert no close happens.
{
  town="$(new_town)"
  BD_CALLS="$(mktemp)"; GT_CALLS="$(mktemp)"
  export BD_CALLS GT_CALLS
  BD_PROP_JSON='[]'   # status-open query yields nothing (the bead is in_progress)
  BD_HYP_JSON='[]'
  export BD_PROP_JSON BD_HYP_JSON
  run_plugin "$town"

  if [[ -s "$BD_CALLS" ]] && grep -q 'close ' "$BD_CALLS" 2>/dev/null; then
    fail "a close happened with no open beads (in_progress should never be swept)"; cat "$BD_CALLS"
  else
    pass "no open proposals → nothing closed (in_progress excluded by --status open)"
  fi
  rm -rf "$town" "$BD_CALLS" "$GT_CALLS"
}

# --- 4. breaker open >= M days, not alerted → exactly one escalation ----------
{
  town="$(new_town)"
  BD_CALLS="$(mktemp)"; GT_CALLS="$(mktemp)"
  export BD_CALLS GT_CALLS
  # 10 open proposals = ceiling (breaker open). All fresh so expiry closes none.
  BD_PROP_JSON="$(jq -nc --arg ts "$NOW_TS" \
    '[range(0;10) | {"id":("gu-p"+(.|tostring)),"status":"open","updated_at":$ts,"labels":["curio-proposal"]}]')"
  BD_HYP_JSON='[]'
  export BD_PROP_JSON BD_HYP_JSON
  # Pre-seed state: open_since 5 days ago, not yet alerted.
  five_days_ago=$(( $(date -u +%s) - 5*86400 ))
  jq -nc --argjson since "$five_days_ago" '{open_since:$since, alerted:false}' \
    >"$town/artifacts/curio-retrospect/breaker-state.json"
  run_plugin "$town"

  n_esc=$(grep -c 'escalate' "$GT_CALLS" 2>/dev/null); n_esc=${n_esc:-0}
  if [[ "$n_esc" == "1" ]]; then
    pass "breaker open 5d (>=3d), not alerted → exactly one escalation"
  else
    fail "expected exactly 1 escalation, got ${n_esc}"; cat "$GT_CALLS"
  fi
  if jq -e '.alerted == true' "$town/artifacts/curio-retrospect/breaker-state.json" >/dev/null 2>&1; then
    pass "breaker state latched alerted=true after firing"
  else
    fail "breaker state did not latch alerted=true"
    cat "$town/artifacts/curio-retrospect/breaker-state.json" 2>/dev/null
  fi
  rm -rf "$town" "$BD_CALLS" "$GT_CALLS"
}

# --- 5. breaker open >= M days, ALREADY alerted → no second escalation --------
{
  town="$(new_town)"
  BD_CALLS="$(mktemp)"; GT_CALLS="$(mktemp)"
  export BD_CALLS GT_CALLS
  BD_PROP_JSON="$(jq -nc --arg ts "$NOW_TS" \
    '[range(0;10) | {"id":("gu-p"+(.|tostring)),"status":"open","updated_at":$ts,"labels":["curio-proposal"]}]')"
  BD_HYP_JSON='[]'
  export BD_PROP_JSON BD_HYP_JSON
  five_days_ago=$(( $(date -u +%s) - 5*86400 ))
  jq -nc --argjson since "$five_days_ago" '{open_since:$since, alerted:true}' \
    >"$town/artifacts/curio-retrospect/breaker-state.json"
  run_plugin "$town"

  n_esc=$(grep -c 'escalate' "$GT_CALLS" 2>/dev/null); n_esc=${n_esc:-0}
  if [[ "$n_esc" == "0" ]]; then
    pass "breaker already alerted → no second escalation (exactly once per trip)"
  else
    fail "expected 0 escalations on already-alerted trip, got ${n_esc}"; cat "$GT_CALLS"
  fi
  rm -rf "$town" "$BD_CALLS" "$GT_CALLS"
}

# --- 6. breaker closed → state cleared ---------------------------------------
{
  town="$(new_town)"
  BD_CALLS="$(mktemp)"; GT_CALLS="$(mktemp)"
  export BD_CALLS GT_CALLS
  # 2 open proposals < ceiling 10 = breaker closed.
  BD_PROP_JSON="$(jq -nc --arg ts "$NOW_TS" \
    '[range(0;2) | {"id":("gu-p"+(.|tostring)),"status":"open","updated_at":$ts,"labels":["curio-proposal"]}]')"
  BD_HYP_JSON='[]'
  export BD_PROP_JSON BD_HYP_JSON
  # Pre-seed a stale, alerted trip — closing the breaker must wipe it.
  jq -nc '{open_since: 1, alerted: true}' \
    >"$town/artifacts/curio-retrospect/breaker-state.json"
  run_plugin "$town"

  if [[ ! -f "$town/artifacts/curio-retrospect/breaker-state.json" ]]; then
    pass "breaker closed → state file cleared (trip clock + latch reset)"
  else
    fail "breaker closed but state file still present"
    cat "$town/artifacts/curio-retrospect/breaker-state.json" 2>/dev/null
  fi
  n_esc=$(grep -c 'escalate' "$GT_CALLS" 2>/dev/null); n_esc=${n_esc:-0}
  [[ "$n_esc" == "0" ]] && pass "breaker closed → no escalation" || fail "breaker closed but escalated ${n_esc}×"
  rm -rf "$town" "$BD_CALLS" "$GT_CALLS"
}

fi  # jq present

# ============================================================================
echo ""
if [[ $FAILURES -gt 0 ]]; then
  echo "FAILED: $FAILURES check(s) failed"
  exit 1
else
  echo "PASSED: all checks passed"
fi

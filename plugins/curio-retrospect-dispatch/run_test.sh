#!/usr/bin/env bash
# Test for curio-retrospect-dispatch/run.sh (Curio P3 B5, gu-5d8os).
#
# Two layers:
#
#   A. STATIC shape assertions — catch `gt sling` API drift before the plugin
#      ships (the wiki-patrol lesson: gu-fc8h / gu-ono8h shipped broken twice).
#
#   B. FUNCTIONAL harness — run.sh end-to-end against a temp town root with
#      stubbed gt / bd / curio-proposer on PATH, asserting:
#        1. the kill-switch skip path (llm.enabled=false → no sling),
#        2. the single-instance skip path (fresh in-flight marker → no sling),
#        3. the volume-breaker skip path (open proposals ≥ ceiling → no sling),
#        4. the anti-wedge staleness rule (STALE in-flight marker → DOES sling)
#           (gc-i2nb6l Risk #4: a crashed prior run must not wedge the lane),
#        5. the digest path-contract: the path curio-proposer is told to emit
#           to is the exact path passed to `gt sling --var digest_path=`, and it
#           is a host-shared absolute path under the town root (not the CWD).
#
# Run:  bash plugins/curio-retrospect-dispatch/run_test.sh

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

# Sling: formula POSITIONAL, rig POSITIONAL (gu-ono8h).
if grep -qE 'gt sling "\$FORMULA" "\$TARGET_RIG"' "$RUN_SH"; then
  pass "gt sling uses positional <formula> <rig>"
else
  fail "run.sh does not invoke 'gt sling \"\$FORMULA\" \"\$TARGET_RIG\"' (formula positional)"
  fail "  History: gu-ono8h — the formula is the FIRST positional arg, not a flag."
  grep -nE 'gt sling' "$RUN_SH" | head -5
fi

# Guard against the broken --formula FLAG form (gu-ono8h).
if grep -qE 'gt sling --formula' "$RUN_SH"; then
  fail "run.sh uses the --formula FLAG (gu-ono8h) — use the positional form"
fi

# Required sling vars + flags.
grep -qE -- '--var "digest_path=' "$RUN_SH" || fail "run.sh does not pass --var digest_path=..."
grep -qE -- '--var "max_proposals=' "$RUN_SH" || fail "run.sh does not pass --var max_proposals=..."
grep -qE -- '--create' "$RUN_SH" || fail "run.sh does not pass --create"

# Kill-switch: reads patrols.curio.llm.enabled.
grep -qE 'patrols\.curio\.llm\.enabled' "$RUN_SH" || \
  fail "run.sh does not read patrols.curio.llm.enabled (kill switch)"

# Digest path must be anchored under the town root, NOT the plugin CWD.
grep -qE 'DIGEST_DIR=.*TOWN_ROOT' "$RUN_SH" || \
  fail "run.sh digest dir is not anchored under \$TOWN_ROOT (sandbox path-contract)"

# plugin.md frontmatter: cron gate at 08:00 UTC, 30m timeout.
grep -qE '^schedule = "0 8 \* \* \*"' "$PLUGIN_MD" || fail "plugin.md cron schedule is not '0 8 * * *'"
grep -qE '^type = "cron"' "$PLUGIN_MD" || fail "plugin.md gate type is not cron"
grep -qE '^timeout = "30m"' "$PLUGIN_MD" || fail "plugin.md execution timeout is not 30m"

# gt sling API still exposes the flags we use (when gt is on PATH).
if command -v gt >/dev/null 2>&1; then
  help_out=$(gt sling --help 2>&1 || true)
  for flag in --create --var; do
    grep -qE "^\s*${flag}\b" <<<"$help_out" || \
      fail "gt sling --help no longer documents '${flag}' — update run.sh"
  done
  pass "gt sling --help documents --create and --var"
else
  echo "SKIP: gt not on PATH — can't verify live sling API"
fi

# ============================================================================
# B. FUNCTIONAL harness
# ============================================================================
echo ""
echo "=== B. run.sh functional skip/dispatch paths ==="

if ! command -v jq >/dev/null 2>&1; then
  echo "SKIP: jq not on PATH — functional harness requires it"
else

# Build a stub bin dir prepended to PATH. Stubs record sling calls and answer
# bd/curio-proposer deterministically from harness-controlled env vars.
STUBS="$(mktemp -d)"
trap 'rm -rf "$STUBS"' EXIT

cat >"$STUBS/gt" <<'STUB'
#!/usr/bin/env bash
# gt stub: log `gt sling ...` invocations to $GT_CALLS; answer `gt town root`.
if [[ "${1:-}" == "town" && "${2:-}" == "root" ]]; then
  echo "$GT_TOWN_ROOT"; exit 0
fi
if [[ "${1:-}" == "sling" ]]; then
  printf '%s\n' "$*" >>"$GT_CALLS"
  echo "slung gu-wisp-test01"
  exit 0
fi
exit 0
STUB

cat >"$STUBS/bd" <<'STUB'
#!/usr/bin/env bash
# bd stub: `bd create` no-ops (receipts); `bd list` answers from env JSON,
# branching on the query so single-instance and volume-breaker get distinct
# fixtures.
args="$*"
case "$args" in
  create*) exit 0 ;;
esac
if [[ "$args" == *"curio-proposal"* ]]; then
  printf '%s' "${BD_PROPOSAL_JSON:-[]}"; exit 0
fi
if [[ "$args" == *"--status open,in_progress,blocked"* ]]; then
  printf '%s' "${BD_INFLIGHT_JSON:-[]}"; exit 0
fi
echo "[]"
STUB

cat >"$STUBS/curio-proposer" <<'STUB'
#!/usr/bin/env bash
# curio-proposer stub: honor --emit-digest <path> by writing a digest there
# (unless CURIO_EMIT_NOTHING=1, simulating the lane-off no-file contract).
path=""
prev=""
for a in "$@"; do
  [[ "$prev" == "--emit-digest" ]] && path="$a"
  prev="$a"
done
if [[ -n "$path" && "${CURIO_EMIT_NOTHING:-0}" != "1" ]]; then
  printf '# Curio digest (stub)\n\n```json\n{"clusters":[]}\n```\n' >"$path"
fi
echo "curio-proposer: wrote digest to ${path}"
exit 0
STUB

chmod +x "$STUBS/gt" "$STUBS/bd" "$STUBS/curio-proposer"

# fresh / stale RFC3339 timestamps for the single-instance fixtures.
NOW_TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
STALE_TS=$(date -u -d "2 hours ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
  || date -u -v-2H +%Y-%m-%dT%H:%M:%SZ)

# run_case NAME EXPECT(sling|nosling) — sets up a fresh town root, runs run.sh
# with the stubs and the per-case env already exported, and checks whether a
# sling happened. Echoes the recorded sling line for path-contract assertions.
LAST_SLING=""
run_case() {
  local name="$1" expect="$2"
  local town; town="$(mktemp -d)"
  mkdir -p "$town/mayor" "$town/$RIG_DIR_NAME"
  printf '%s' "${DAEMON_JSON_BODY}" >"$town/mayor/daemon.json"

  local calls; calls="$(mktemp)"
  GT_TOWN_ROOT="$town" GT_CALLS="$calls" \
    PATH="$STUBS:$PATH" \
    bash "$RUN_SH" >/dev/null 2>&1

  LAST_SLING="$(cat "$calls" 2>/dev/null || true)"
  local slung="nosling"; [[ -s "$calls" ]] && slung="sling"

  if [[ "$slung" == "$expect" ]]; then
    pass "$name → $slung (expected $expect)"
  else
    fail "$name → $slung (expected $expect)"
  fi
  rm -rf "$town" "$calls"
}

RIG_DIR_NAME="gastown_upstream"

# --- 1. kill-switch off → no sling ------------------------------------------
DAEMON_JSON_BODY='{"patrols":{"curio":{"enabled":true,"llm":{"enabled":false}}}}'
BD_INFLIGHT_JSON='[]'; BD_PROPOSAL_JSON='[]'; export BD_INFLIGHT_JSON BD_PROPOSAL_JSON
unset CURIO_EMIT_NOTHING
run_case "kill-switch off" "nosling"

# --- 2. fresh in-flight marker → no sling -----------------------------------
DAEMON_JSON_BODY='{"patrols":{"curio":{"llm":{"enabled":true}}}}'
BD_INFLIGHT_JSON="$(jq -nc --arg ts "$NOW_TS" \
  '[{"id":"gu-test-inflight","updated_at":$ts,"description":"attached_formula: mol-curio-retrospect"}]')"
BD_PROPOSAL_JSON='[]'; export BD_INFLIGHT_JSON BD_PROPOSAL_JSON
run_case "fresh in-flight marker" "nosling"

# --- 3. volume breaker tripped → no sling -----------------------------------
DAEMON_JSON_BODY='{"patrols":{"curio":{"llm":{"enabled":true}}}}'
BD_INFLIGHT_JSON='[]'
BD_PROPOSAL_JSON="$(jq -nc '[range(0;10) | {"id":("gu-p"+(.|tostring)),"labels":["curio-proposal"]}]')"
export BD_INFLIGHT_JSON BD_PROPOSAL_JSON
run_case "volume breaker (10 ≥ ceiling 10)" "nosling"

# --- 4. STALE in-flight marker → DOES sling (anti-wedge, gc-i2nb6l) ----------
DAEMON_JSON_BODY='{"patrols":{"curio":{"llm":{"enabled":true}}}}'
BD_INFLIGHT_JSON="$(jq -nc --arg ts "$STALE_TS" \
  '[{"id":"gu-test-stale","updated_at":$ts,"description":"attached_formula: mol-curio-retrospect"}]')"
BD_PROPOSAL_JSON='[]'; export BD_INFLIGHT_JSON BD_PROPOSAL_JSON
run_case "stale in-flight marker (does not wedge)" "sling"

# --- 5. happy path → sling + digest path-contract ---------------------------
DAEMON_JSON_BODY='{"patrols":{"curio":{"llm":{"enabled":true}}}}'
BD_INFLIGHT_JSON='[]'; BD_PROPOSAL_JSON='[]'; export BD_INFLIGHT_JSON BD_PROPOSAL_JSON
run_case "happy path (lane on, quiet backlog)" "sling"

# Path-contract: the digest_path passed to gt sling must be the host-shared
# artifacts path under the town root the proposer wrote (run_case's town root is
# gone, but the recorded sling line carries the absolute path — assert shape).
if [[ "$LAST_SLING" == *"--var digest_path="* ]]; then
  dp=$(grep -oE -- '--var digest_path=[^ ]+' <<<"$LAST_SLING" | head -1 | sed 's/--var digest_path=//')
  if [[ "$dp" == /*/artifacts/curio-retrospect/digest-*.md ]]; then
    pass "digest_path is a host-shared absolute artifacts path: $dp"
  else
    fail "digest_path '$dp' is not the expected /…/artifacts/curio-retrospect/digest-*.md host-shared path"
  fi
else
  fail "happy-path sling did not pass --var digest_path="
fi

fi  # jq present

# ============================================================================
echo ""
if [[ $FAILURES -gt 0 ]]; then
  echo "FAILED: $FAILURES check(s) failed"
  exit 1
else
  echo "PASSED: all checks passed"
fi

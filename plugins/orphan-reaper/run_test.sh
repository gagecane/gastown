#!/usr/bin/env bash
# Tests for orphan-reaper/run.sh pure predicate helpers (spec §3, §5).
#
# Convention (per plugins/compactor-dog/run_test.sh): the test keeps its OWN
# copy of each pure helper and a drift-guard that sed-extracts the same function
# from run.sh and asserts byte-for-byte equality. If run.sh changes a helper
# without the test being updated, the drift-guard fails — so the assertions below
# always exercise behavior identical to the shipped reaper.
#
# NOT run by CI (.github/workflows/ci.yml runs only `go test`). Run manually:
#   bash plugins/orphan-reaper/run_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TEST_FILE="$SCRIPT_DIR/$(basename "$0")"
RUN_SH="$SCRIPT_DIR/run.sh"
FAILURES=0

# === Test's copies of the pure helpers (drift-guarded against run.sh below) ===

parse_ppid() {
  local stat="$1" tail
  tail=${stat##*) }
  awk '{print $2}' <<<"$tail"
}

parse_starttime() {
  local stat="$1" tail
  tail=${stat##*) }
  awk '{print $20}' <<<"$tail"
}

compute_age() {
  local uptime="$1" starttime="$2" clk="$3"
  echo $(( uptime - starttime / clk ))
}

matches_signature() {
  local cmdline="$1"
  case "$cmdline" in
    */.toolbox/tools/claude-code/*otelcol-contrib*) return 0 ;;
  esac
  if [ -n "${REAPER_MCP_PATTERNS:-}" ]; then
    printf '%s' "$cmdline" | grep -Eq "${REAPER_MCP_PATTERNS}" && return 0
  fi
  return 1
}

signature_of() {
  local cmdline="$1"
  case "$cmdline" in
    */.toolbox/tools/claude-code/*otelcol-contrib*) echo "otelcol"; return 0 ;;
  esac
  if [ -n "${REAPER_MCP_PATTERNS:-}" ] && printf '%s' "$cmdline" | grep -Eq "${REAPER_MCP_PATTERNS}"; then
    echo "mcp"; return 0
  fi
  echo "none"
}

# Test's copy of run.sh's default REAPER_MCP_PATTERNS (drift-guarded below). The
# positive MCP tests need the real default pattern to be meaningful.
DEFAULT_MCP_PATTERNS='/\.toolbox/tools/[^/ ]*[Mm][Cc][Pp][^/ ]*/|/brazil-pkg-cache/packages/[A-Za-z0-9]*MCP/|DesignInspectorMCP'

# === Drift-guard: every copied helper must match run.sh verbatim ===

echo "=== drift-guard (test copies vs run.sh) ==="
for fn in parse_ppid parse_starttime compute_age matches_signature signature_of; do
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

# Drift-guard the default MCP pattern too (the positive MCP tests depend on it).
run_default=$(grep -m1 '^REAPER_MCP_PATTERNS=' "$RUN_SH" \
  | sed -E 's/^REAPER_MCP_PATTERNS="\$\{REAPER_MCP_PATTERNS-(.*)\}"$/\1/')
if [ "$run_default" != "$DEFAULT_MCP_PATTERNS" ]; then
  echo "DRIFT: default REAPER_MCP_PATTERNS in run_test.sh does not match run.sh"
  echo "  run.sh:      $run_default"
  echo "  run_test.sh: $DEFAULT_MCP_PATTERNS"
  FAILURES=$((FAILURES + 1))
fi

# === Assertion helpers ===

assert_eq() {
  local actual="$1" expected="$2" desc="$3"
  if [ "$actual" != "$expected" ]; then
    echo "FAIL: $desc: expected '$expected', got '$actual'"
    FAILURES=$((FAILURES + 1))
  fi
}

assert_match() {
  local cmdline="$1" desc="$2"
  if ! matches_signature "$cmdline"; then
    echo "FAIL: $desc: expected signature MATCH for: $cmdline"
    FAILURES=$((FAILURES + 1))
  fi
}

assert_nomatch() {
  local cmdline="$1" desc="$2"
  if matches_signature "$cmdline"; then
    echo "FAIL: $desc: expected NO match for: $cmdline"
    FAILURES=$((FAILURES + 1))
  fi
}

# === /proc/<pid>/stat parsing (spec §3 robust-parse) ===

echo "=== stat parse tests ==="

# comm contains BOTH spaces and parens — the strip-through-LAST-')' path. Naive
# whitespace field-splitting on the full stat would pick the wrong fields here.
# Tail layout after '(comm) ': field2=ppid, field20=starttime.
tricky_stat='1234 ((weird) comm) S 1 1234 1234 0 -1 4194560 100 0 0 0 5 5 0 0 20 0 1 0 8000000'
assert_eq "$(parse_ppid "$tricky_stat")"      "1"       "parse_ppid: comm with spaces/parens"
assert_eq "$(parse_starttime "$tricky_stat")" "8000000" "parse_starttime: comm with spaces/parens"

# Simple space-free comm (the common case).
simple_stat='4321 (otelcol-contrib) S 7 4321 4321 0 -1 0 0 0 0 0 1 1 0 0 20 0 1 0 500000'
assert_eq "$(parse_ppid "$simple_stat")"      "7"      "parse_ppid: plain comm"
assert_eq "$(parse_starttime "$simple_stat")" "500000" "parse_starttime: plain comm"

# === Age arithmetic (spec §3: boot-relative, integer-divide ticks first) ===

echo "=== age arithmetic tests ==="
assert_eq "$(compute_age 10000 500000 100)" "5000" "compute_age: uptime-relative"
assert_eq "$(compute_age 100 550 100)"      "95"   "compute_age: integer-divide ticks first"
assert_eq "$(compute_age 100 50 100)"       "100"  "compute_age: freshly-started proc"

# === Signature matching (spec §3 otelcol, §5 MCP allowlist) ===

echo "=== signature match tests ==="

# Positive: otelcol-contrib under the claude-code toolbox path (always-on arm).
otelcol='/home/canewiw/.toolbox/tools/claude-code/2.1.168.364/otelcol-contrib --config /home/canewiw/.toolbox/tools/claude-code/2.1.168.364/otel-config.yaml'

# Positive: the §5 MCP families launched under ~/.toolbox/tools/*mcp*/.
builder_mcp='node /home/canewiw/.toolbox/tools/builder-mcp/2.0.1/dist/index.js'
spec_studio='node /home/canewiw/.toolbox/tools/mcp-spec-studio-server/1.0.0/server.js'
pippin='python3 /home/canewiw/.toolbox/tools/pippin-mcp-server/0.3.0/main.py'

# Negative: out-of-scope / intentional processes that must NEVER be reaped.
serena_indirect='/home/canewiw/.local/bin/uvx --from git+https://github.com/oraios/serena serena start-mcp-server --transport stdio'  # §3 caveat: no matchable path
user_daemon='/usr/bin/python3 /home/canewiw/bin/mydaemon.py --serve --port 8080'                                                     # intentional user daemon
otelcol_offpath='/opt/otel/bin/otelcol-contrib --config /etc/otelcol/config.yaml'                                                    # otelcol NOT under claude-code path

# MCP matching requires the allowlist to be set (as run.sh's default supplies).
export REAPER_MCP_PATTERNS="$DEFAULT_MCP_PATTERNS"

assert_match "$otelcol"     "otelcol under claude-code path"
assert_match "$builder_mcp" "builder-mcp under .toolbox/tools"
assert_match "$spec_studio" "mcp-spec-studio-server under .toolbox/tools"
assert_match "$pippin"      "pippin-mcp-server under .toolbox/tools"

assert_nomatch "$serena_indirect" "indirectly-launched serena (no matchable path, §3 caveat)"
assert_nomatch "$user_daemon"     "intentional user daemon"
assert_nomatch "$otelcol_offpath" "otelcol-contrib outside claude-code path"

# signature_of names the matched signature for logging.
assert_eq "$(signature_of "$otelcol")"         "otelcol" "signature_of: otelcol"
assert_eq "$(signature_of "$builder_mcp")"     "mcp"     "signature_of: mcp"
assert_eq "$(signature_of "$user_daemon")"     "none"    "signature_of: no match"

# === Empty REAPER_MCP_PATTERNS disables MCP matching (spec §5) ===

echo "=== empty REAPER_MCP_PATTERNS disables MCP tests ==="
export REAPER_MCP_PATTERNS=''
assert_nomatch "$builder_mcp" "builder-mcp NOT reaped when MCP patterns empty"
assert_nomatch "$pippin"      "pippin NOT reaped when MCP patterns empty"
assert_match   "$otelcol"     "otelcol STILL reaped when MCP patterns empty (always-on)"
assert_eq "$(signature_of "$builder_mcp")" "none"    "signature_of: mcp disabled -> none"
assert_eq "$(signature_of "$otelcol")"     "otelcol" "signature_of: otelcol still on"

# === Result ===

echo ""
if [ "$FAILURES" -gt 0 ]; then
  echo "FAILED: $FAILURES check(s) failed"
  exit 1
else
  echo "PASSED: all checks passed"
fi

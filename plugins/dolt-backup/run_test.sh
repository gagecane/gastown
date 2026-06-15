#!/usr/bin/env bash
# Test for dolt-backup/run.sh — the no-backup-during-restart store-lock guard
# and escalation dedup (gu-p02zy, root cause of gu-rhvsn).
#
# Two layers:
#
#   A. STATIC shape assertions — run.sh carries the store-lock contract and the
#      escalation-dedup fingerprint, and the lock path mirrors the Go side
#      (internal/doltserver.StoreLockPath).
#
#   B. FUNCTIONAL harness — run.sh end-to-end against a temp town with a stubbed
#      `dolt`/`bd`/`gt` on PATH and one fake DB, asserting that when a Dolt
#      restart already HOLDS the store lock, the DB is DEFERRED (not failed, no
#      exit-1, no HIGH escalation); and when the lock is free it syncs normally.
#
# Run:  bash plugins/dolt-backup/run_test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RUN_SH="$SCRIPT_DIR/run.sh"
FAILURES=0

fail() { echo "FAIL: $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "ok: $*"; }

# ============================================================================
# A. STATIC shape assertions
# ============================================================================
echo "=== A. run.sh static shape ==="

# The per-DB sync must be guarded by `flock -w` on fd 9.
grep -qE 'flock -w "\$STORE_LOCK_WAIT" 9' "$RUN_SH" || \
  fail "run.sh does not flock -w the store lock (fd 9) before sync"

# A contended lock must DEFER, not FAIL (no exit-1 / HIGH escalation for it).
grep -qE 'DEFERRED=\$\(\(DEFERRED \+ 1\)\)' "$RUN_SH" || \
  fail "run.sh has no DEFERRED counter for restart-contended syncs"
grep -qiE 'deferred .* retry next cycle' "$RUN_SH" || \
  fail "run.sh does not log the deferred-retry-next-cycle message"

# Lock path MUST mirror internal/doltserver.StoreLockPath: bare for 3307, else suffixed.
grep -qE 'daemon/dolt-store\.lock"' "$RUN_SH" || \
  fail "run.sh store_lock_path does not use daemon/dolt-store.lock for the canonical port"
grep -qE 'daemon/dolt-store\.lock\.\$DOLT_PORT' "$RUN_SH" || \
  fail "run.sh store_lock_path does not suffix non-canonical ports"

# Escalation dedup rider (mayor/deacon gu-p02zy): signature + dedup + window + related.
grep -qE -- '--signature "dolt-backup-tablefile:' "$RUN_SH" || \
  fail "run.sh FAILED escalate has no stable --signature fingerprint"
grep -qE -- '--dedup --dedup-window 2h' "$RUN_SH" || \
  fail "run.sh FAILED escalate is not deduped with a 2h window"
grep -qE -- '--related gu-p02zy' "$RUN_SH" || \
  fail "run.sh FAILED escalate is not --related gu-p02zy"

# ============================================================================
# B. FUNCTIONAL harness
# ============================================================================
echo "=== B. functional: deferred-when-lock-held vs syncs-when-free ==="

if ! command -v flock >/dev/null 2>&1; then
  echo "SKIP functional: flock(1) not available"
else
  TOWN="$(mktemp -d)"
  trap 'rm -rf "$TOWN"' EXIT
  BIN="$TOWN/bin"
  mkdir -p "$BIN" "$TOWN/.dolt-data/testdb/.dolt" "$TOWN/daemon"

  # Stub `dolt`: `dolt backup ...` succeeds; `dolt log` prints a fake hash.
  cat > "$BIN/dolt" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  log) echo "abc1234 fake" ;;
  backup)
    case "${2:-}" in
      -v|"") echo "testdb-backup file:///dummy" ;;  # list remotes
      add)   exit 0 ;;
      sync)  exit 0 ;;
      *)     exit 0 ;;
    esac ;;
  *) exit 0 ;;
esac
EOF
  chmod +x "$BIN/dolt"

  # Stub `bd` and `gt` so receipt-creation / escalation are no-ops we can grep.
  cat > "$BIN/bd" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "$BIN/bd"
  cat > "$BIN/gt" <<EOF
#!/usr/bin/env bash
echo "GT_CALL: \$*" >> "$TOWN/gt-calls.log"
exit 0
EOF
  chmod +x "$BIN/gt"

  run_backup() {
    PATH="$BIN:$PATH" GT_TOWN_ROOT="$TOWN" STORE_LOCK_WAIT="1" \
      bash "$RUN_SH" --databases testdb 2>&1
  }

  # B1: lock FREE -> testdb syncs (1 synced, 0 deferred, exit 0).
  out_free="$(run_backup)"; rc_free=$?
  if [[ $rc_free -eq 0 ]] && grep -qE "1 synced.*0 deferred" <<<"$out_free"; then
    pass "lock free: testdb synced, 0 deferred, exit 0"
  else
    fail "lock free: expected '1 synced ... 0 deferred' exit 0; got rc=$rc_free: $(grep Backup: <<<"$out_free")"
  fi

  # B2: lock HELD by a simulated restart -> testdb DEFERRED, exit 0, no FAILED.
  LOCK="$TOWN/daemon/dolt-store.lock"
  exec 8>"$LOCK"
  flock -n 8 || { echo "test setup: could not pre-hold lock"; FAILURES=$((FAILURES+1)); }
  out_held="$(run_backup)"; rc_held=$?
  flock -u 8; exec 8>&-

  if [[ $rc_held -eq 0 ]] && grep -qE "0 failed" <<<"$out_held" && grep -qE "1 deferred" <<<"$out_held"; then
    pass "lock held: testdb deferred (not failed), exit 0"
  else
    fail "lock held: expected '0 failed ... 1 deferred' exit 0; got rc=$rc_held: $(grep Backup: <<<"$out_held")"
  fi

  # B3: a deferred run must NOT fire a HIGH escalation.
  if [[ -f "$TOWN/gt-calls.log" ]] && grep -q "escalate" "$TOWN/gt-calls.log"; then
    fail "lock held: deferred run wrongly fired a gt escalate"
  else
    pass "lock held: no escalation fired for a deferred run"
  fi
fi

# ============================================================================
echo
if [[ $FAILURES -eq 0 ]]; then
  echo "ALL PASS"
  exit 0
else
  echo "$FAILURES FAILURE(S)"
  exit 1
fi

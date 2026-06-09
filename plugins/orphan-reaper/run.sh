#!/usr/bin/env bash
# orphan-reaper/run.sh — Reap orphaned (ppid==1) claude-code otelcol/MCP helpers.
#
# claude-code sessions spawn helper children (otelcol-contrib telemetry sidecars,
# MCP servers). When a session dies abruptly the child is reparented to PID 1 and
# runs forever, leaking CPU/PID-table/memory. This is a periodic external sweep
# that terminates those parentless helpers. It is a band-aid for the upstream
# reap-on-exit gap, not a cure (see orphan-reaper-spec.md §1).
#
# Deterministic shell, no AI. Same script backs both the Gas Town daemon-dog
# plugin (preferred) and the standalone-cron fallback (spec §10, §12).
#
# Exit-code contract (spec §12.4):
#   0       success — INCLUDING found-nothing AND killed-N (reaping is success).
#   non-0   operational failure ONLY (cannot read /proc, survivors after SIGKILL).
# Real errors are never masked with `|| true` (dolt-backup scar gu-8xvpw).
# stdout is the digest body (§6 summary line + per-kill lines).

set -euo pipefail

# --- Configuration (env vars w/ defaults, spec §5) ---------------------------

REAPER_DRY_RUN="${REAPER_DRY_RUN:-0}"
REAPER_MIN_AGE="${REAPER_MIN_AGE:-300}"
REAPER_GRACE="${REAPER_GRACE:-10}"
REAPER_LOG="${REAPER_LOG:-$HOME/.local/state/orphan-reaper.log}"
REAPER_LOG_MAX_BYTES="${REAPER_LOG_MAX_BYTES:-1048576}"
# Default MCP allowlist (spec §5). The `.toolbox/tools/*mcp*/` arm fires on this
# host; the brazil-pkg-cache arm is retained for portability. Use `-` (not `:-`)
# so an explicitly-empty value DISABLES MCP reaping while unset gets the default.
REAPER_MCP_PATTERNS="${REAPER_MCP_PATTERNS-/\.toolbox/tools/[^/ ]*[Mm][Cc][Pp][^/ ]*/|/brazil-pkg-cache/packages/[A-Za-z0-9]*MCP/|DesignInspectorMCP}"
export REAPER_MCP_PATTERNS
REAPER_LOCK_FILE="${REAPER_LOCK_FILE:-/tmp/orphan-reaper.lock}"

# --- Pure helpers (spec §3) ---------------------------------------------------
# These are copied + drift-guarded by run_test.sh (bead C). Keep them pure
# (arguments in, no /proc reads) and defined at column 0 so they sed-extract.

# parse_ppid <stat_string> — field 2 of the post-comm tail (overall field 4).
# Strip everything through the LAST ") " first: comm (field 2) is paren-wrapped
# and may itself contain spaces/parens, which breaks naive field-splitting.
parse_ppid() {
  local stat="$1" tail
  tail=${stat##*) }
  awk '{print $2}' <<<"$tail"
}

# parse_starttime <stat_string> — field 20 of the tail (overall field 22),
# in clock ticks since boot.
parse_starttime() {
  local stat="$1" tail
  tail=${stat##*) }
  awk '{print $20}' <<<"$tail"
}

# compute_age <uptime_seconds> <starttime_ticks> <clk_tck> — boot-relative age.
# Both operands are boot-relative; do NOT "fix" this to wall-clock date +%s.
# Integer-divide ticks first (spec §3).
compute_age() {
  local uptime="$1" starttime="$2" clk="$3"
  echo $(( uptime - starttime / clk ))
}

# matches_signature <cmdline> — true iff cmdline matches a known leak signature.
# Matches on cmdline PATH, not comm (comm is truncated to 15 chars and is
# unreliable for MCP servers). otelcol reaping is always on; MCP reaping is
# gated by REAPER_MCP_PATTERNS (empty disables it).
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

# signature_of <cmdline> — name of the matched signature, for logging.
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

# --- /proc readers (impure) ---------------------------------------------------

ME="$(id -u)"
CLK_TCK="$(getconf CLK_TCK)"

# read_cmdline <pid> — argv with NULs and embedded whitespace (newlines/tabs)
# rendered as single spaces, so it stays one logical line for matching + logging
# (empty for kernel threads).
read_cmdline() {
  tr '\0\n\t' '   ' < "/proc/$1/cmdline" 2>/dev/null
}

# is_reapable <pid> — true iff ALL predicates hold (spec §3): owner==me,
# ppid==1, age>=REAPER_MIN_AGE, signature match.
is_reapable() {
  local pid="$1" stat owner ppid starttime age cmdline
  [ -r "/proc/$pid/stat" ] || return 1
  stat=$(cat "/proc/$pid/stat" 2>/dev/null) || return 1
  [ -n "$stat" ] || return 1
  owner=$(stat -c %u "/proc/$pid" 2>/dev/null) || return 1
  [ "$owner" = "$ME" ] || return 1                       # 1. owner
  ppid=$(parse_ppid "$stat")
  [ "$ppid" = "1" ] || return 1                          # 2. orphan
  starttime=$(parse_starttime "$stat")
  case "$starttime" in ''|*[!0-9]*) return 1 ;; esac
  age=$(compute_age "$UPTIME_S" "$starttime" "$CLK_TCK")
  [ "$age" -ge "$REAPER_MIN_AGE" ] || return 1           # 3. age
  cmdline=$(read_cmdline "$pid")
  matches_signature "$cmdline" || return 1               # 4. signature
  return 0
}

# revalidate <pid> — TOCTOU re-check immediately before signalling (spec §3):
# re-verify predicates 1 (owner), 2 (orphan), 4 (signature) against a FRESH read.
# Age (3) is omitted per spec; it only grows for the same process. Never signal
# a PID validated only from a stale snapshot.
revalidate() {
  local pid="$1" stat owner ppid cmdline
  stat=$(cat "/proc/$pid/stat" 2>/dev/null) || return 1
  [ -n "$stat" ] || return 1
  owner=$(stat -c %u "/proc/$pid" 2>/dev/null) || return 1
  [ "$owner" = "$ME" ] || return 1
  ppid=$(parse_ppid "$stat")
  [ "$ppid" = "1" ] || return 1
  cmdline=$(read_cmdline "$pid")
  matches_signature "$cmdline" || return 1
  return 0
}

# proc_comm <pid> — comm from stat (everything up to the last ')').
proc_comm() {
  local stat rest
  stat=$(cat "/proc/$1/stat" 2>/dev/null) || { echo "?"; return; }
  rest=${stat#*(}
  echo "${rest%)*}"
}

# proc_rss_kb <pid> — resident set size in kB.
proc_rss_kb() {
  awk '/^VmRSS:/{print $2; found=1} END{if(!found) print "0"}' "/proc/$1/status" 2>/dev/null || echo "0"
}

# --- Logging / observability (spec §6) ----------------------------------------

# Operational chatter goes to stderr; the digest body (summary + per-kill) goes
# to stdout AND is appended to the self-rotating log file.
log() { echo "[orphan-reaper] $*" >&2; }

rotate_log_if_needed() {
  local dir size
  dir=$(dirname "$REAPER_LOG")
  mkdir -p "$dir" 2>/dev/null || { log "cannot create log dir $dir — logging to stdout only"; REAPER_LOG=""; return 0; }
  [ -f "$REAPER_LOG" ] || return 0
  size=$(stat -c %s "$REAPER_LOG" 2>/dev/null || echo 0)
  if [ "$size" -gt "$REAPER_LOG_MAX_BYTES" ]; then
    mv -f "$REAPER_LOG" "${REAPER_LOG}.1" 2>/dev/null || true
    log "rotated log (${size}B > ${REAPER_LOG_MAX_BYTES}B) -> ${REAPER_LOG}.1"
  fi
}

# emit <line> — to stdout (digest body) and the log file.
emit() {
  local ts line
  ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  line="$ts $*"
  echo "$line"
  [ -n "$REAPER_LOG" ] && echo "$line" >> "$REAPER_LOG" 2>/dev/null || true
}

# --- Single-instance lock (spec §7, standalone-cron fallback) -----------------
# Inside the Gas Town daemon the cooldown gate guarantees single dispatch, so the
# flock is redundant there; it is kept for the standalone path. Set
# REAPER_SKIP_LOCK=1 to bypass (e.g. when the daemon already serializes).
acquire_lock() {
  [ "${REAPER_SKIP_LOCK:-0}" = "1" ] && return 0
  command -v flock >/dev/null 2>&1 || { log "flock unavailable — proceeding without lock"; return 0; }
  exec 9>"$REAPER_LOCK_FILE" 2>/dev/null || { log "cannot open lock $REAPER_LOCK_FILE — proceeding without lock"; return 0; }
  if ! flock -n 9; then
    log "another instance holds $REAPER_LOCK_FILE — exiting cleanly"
    exit 0
  fi
  return 0
}

# --- Main ---------------------------------------------------------------------

main() {
  if [ ! -d /proc ] || [ ! -r /proc ]; then
    log "FATAL: cannot read /proc"
    exit 1
  fi

  acquire_lock
  rotate_log_if_needed

  UPTIME_S=$(cut -d. -f1 /proc/uptime 2>/dev/null) || true
  case "${UPTIME_S:-}" in
    ''|*[!0-9]*) log "FATAL: cannot read /proc/uptime"; exit 1 ;;
  esac

  local dry=0
  [ "$REAPER_DRY_RUN" = "1" ] && dry=1

  # --- Phase 1: enumerate candidates ---
  local -a candidates=()
  local pid
  for d in /proc/[0-9]*; do
    pid=${d#/proc/}
    if is_reapable "$pid"; then
      candidates+=("$pid")
    fi
  done

  local n_found=${#candidates[@]}
  local n_term=0 n_kill=0 n_survivors=0

  # Per-kill / would-kill detail lines (spec §6: pid, comm, age, signature, RSS,
  # truncated cmdline).
  for pid in "${candidates[@]}"; do
    local stat starttime age comm rss cmdline sig
    stat=$(cat "/proc/$pid/stat" 2>/dev/null) || continue
    starttime=$(parse_starttime "$stat")
    age=$(compute_age "$UPTIME_S" "$starttime" "$CLK_TCK")
    comm=$(proc_comm "$pid")
    rss=$(proc_rss_kb "$pid")
    cmdline=$(read_cmdline "$pid")
    sig=$(signature_of "$cmdline")
    local verb="kill"; [ "$dry" = "1" ] && verb="would-kill"
    emit "$verb pid=$pid comm=$comm age=${age}s sig=$sig rss=${rss}kB cmd=${cmdline:0:120}"
  done

  if [ "$dry" = "1" ]; then
    emit "summary candidates=$n_found term=0 kill=0 survivors=0 dry_run=1"
    record_receipt "$n_found" 0 0 0 1
    log "dry run complete — $n_found candidate(s), no signals sent"
    return 0
  fi

  if [ "$n_found" -eq 0 ]; then
    emit "summary candidates=0 term=0 kill=0 survivors=0 dry_run=0"
    record_receipt 0 0 0 0 0
    return 0
  fi

  # --- Phase 2: graceful SIGTERM (re-validate each PID first — TOCTOU) ---
  for pid in "${candidates[@]}"; do
    if revalidate "$pid" && kill -TERM "$pid" 2>/dev/null; then
      n_term=$((n_term + 1))
    fi
  done

  # --- Phase 3: wait grace, re-scan, SIGKILL survivors still matching ---
  sleep "$REAPER_GRACE"

  local -a still_alive=()
  for pid in "${candidates[@]}"; do
    if revalidate "$pid"; then
      still_alive+=("$pid")
    fi
  done

  for pid in "${still_alive[@]}"; do
    if revalidate "$pid" && kill -KILL "$pid" 2>/dev/null; then
      n_kill=$((n_kill + 1))
    fi
  done

  # --- Phase 4: count true survivors (still matching after SIGKILL + grace) ---
  if [ "${#still_alive[@]}" -gt 0 ]; then
    sleep 1
    for pid in "${still_alive[@]}"; do
      if revalidate "$pid"; then
        n_survivors=$((n_survivors + 1))
        emit "survivor pid=$pid still matching after SIGKILL"
      fi
    done
  fi

  emit "summary candidates=$n_found term=$n_term kill=$n_kill survivors=$n_survivors dry_run=0"

  # --- Phase 5: receipt + escalate on operational failure (spec §12.4) ---
  if [ "$n_survivors" -gt 0 ]; then
    record_receipt "$n_found" "$n_term" "$n_kill" "$n_survivors" 0 failure
    log "FAILURE: $n_survivors process(es) survived SIGKILL — escalating"
    gt escalate "orphan-reaper: $n_survivors orphan(s) survived SIGKILL" \
      --severity low \
      --reason "Reaped $n_term via SIGTERM, $n_kill via SIGKILL of $n_found candidates; $n_survivors still match the predicate after SIGKILL+grace — likely uninterruptible-sleep, worth a human look." 2>/dev/null || true
    exit 1
  fi

  record_receipt "$n_found" "$n_term" "$n_kill" 0 0 success
  return 0
}

# record_receipt <found> <term> <kill> <survivors> <dry_run> [result]
# Plugin-run receipt for the dog digest pipeline (modeled on dolt-log-rotate).
record_receipt() {
  local found="$1" term="$2" kill="$3" surv="$4" dry="$5" result="${6:-success}"
  local summary="orphan-reaper: candidates=$found term=$term kill=$kill survivors=$surv dry_run=$dry"
  # Redirect stdout too: bd prints the new bead id, which must not pollute the
  # digest body (stdout is the §6 digest, spec §12.4).
  bd create "$summary" -t chore --ephemeral \
    -l "type:plugin-run,plugin:orphan-reaper,result:$result" \
    -d "$summary" --silent >/dev/null 2>&1 || true
}

# Only run main when executed directly, so run_test.sh can source helpers safely.
# Guard against unset BASH_SOURCE under `set -u` when sourced from a test harness.
if [ "${BASH_SOURCE[0]:-}" = "${0}" ]; then
  main "$@"
fi

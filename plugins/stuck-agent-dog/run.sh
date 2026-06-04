#!/usr/bin/env bash
# stuck-agent-dog/run.sh — Context-aware stuck/crashed agent detection.
#
# SCOPE: Only polecats and deacon. NEVER touches crew, mayor, witness, or refinery.
# The daemon detects; this plugin inspects context before acting.

set -euo pipefail

# Diagnostic ERR trap (gu-6zhl): when a pipeline tripped set -e the script
# exited rc=1 with no clue which line fired (intermittent early-exit after the
# header log line). Print the failing line + command before set -e takes the
# script down so the next regression is debuggable from logs alone.
trap 'rc=$?; echo "[stuck-agent-dog] ERR trap: line $LINENO exited $rc: ${BASH_COMMAND}" >&2' ERR

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
RIGS_JSON_PATH="${TOWN_ROOT}/mayor/rigs.json"

# STALLED_THRESHOLD is the age (seconds) at which an alive-but-idle polecat
# session is considered stalled. Set higher than the 3-min heartbeat stale
# threshold used by the witness (internal/polecat/heartbeat.go) to avoid false
# positives on legitimate long-running operations (builds, tests, LLM calls).
#
# History (gu-9ed0): the original default was 600s (10m). In practice polecat
# work cycles routinely run 20-45 minutes — a single LLM call against a deep
# context, a Brazil install, or a long `go test ./...` run blocks the agent
# loop and prevents heartbeat updates for the duration of the foreground tool
# call. With a 10m threshold the dog flagged these busy-but-alive sessions as
# STALLED, and any 3 simultaneous flags tripped MASS DEATH → spurious
# CRITICAL escalations. Tonight (2026-05-29 → 2026-05-30) alone produced 6
# mass-death false-positives, all closed by mayor as known noise. The dog runs
# on a 5m cycle, so 1800s (30m) gives 6 cycles of grace — comfortably above
# the long tail of legitimate long-running tool calls while still catching
# genuinely-stalled polecats (process pinned in D-state for half an hour+).
# Operators who need the old aggressive behavior can override via env var.
STUCK_STALLED_THRESHOLD="${STUCK_STALLED_THRESHOLD:-1800}"

# STUCK_LOAD_DEFER_RATIO is the 1-minute-load-average-per-CPU threshold
# above which the dog defers RESTART_POLECAT actions entirely (gs-549 fix #3).
# Restarting under high load is what fed the 2026-05-19 mass-death cascade
# in lia_bac: a kill+respawn under sustained load (avg ~25 on 12 CPUs)
# inherits the same load + still-stalling tool calls, the polecat re-stalls
# inside one dog cycle, and the loop drains the slot pool. When the
# environment is overloaded, the right answer is "wait it out" — every
# polecat in the rig is operating under the same pressure, so the failure
# is environmental rather than per-polecat.
#
# Default ratio 2.0 = "load avg > 2x CPU count" — well above the normal
# steady-state for a busy box, below the runaway range observed in the
# incident. Set to 0 to disable the defer entirely.
STUCK_LOAD_DEFER_RATIO="${STUCK_LOAD_DEFER_RATIO:-2.0}"

# STUCK_PROGRESS_SAMPLE_GAP is the gap (seconds) between the two pane captures
# used to corroborate a stale-heartbeat signal with live token/spinner progress
# (gu-wuduc). A working agent mutates its pane between samples (streaming LLM
# tokens, an advancing spinner, an incrementing "esc to interrupt · 1m23s"
# elapsed counter, scrolling tool output); a genuinely frozen agent does not.
# 3s comfortably exceeds the ~1s spinner frame interval so a live op always
# shows movement, while keeping the dog's added latency negligible (this check
# runs only for the single deacon session, not per-polecat).
STUCK_PROGRESS_SAMPLE_GAP="${STUCK_PROGRESS_SAMPLE_GAP:-3}"

# Identity-bead hook anomaly: a polecat should NEVER be hooked to an identity
# bead (refinery/witness/mayor/deacon). Auto-dispatch's filter excludes these,
# but if one leaks through (e.g. manual `gt hook` error, sling-context bug),
# the polecat will loop on a bead that it cannot make progress on. This regex
# matches common identity-bead suffixes used across rigs.
IDENTITY_BEAD_PATTERN='-(refinery|witness|mayor|deacon)$'

log() { echo "[stuck-agent-dog] $*"; }

# heartbeat_status_json fetches the typed liveness report for a session
# from `gt heartbeat status --json`. cv-p3fem Phase 3 plugin contract.
# Echoes the JSON document on stdout, or empty string on error.
heartbeat_status_json() {
  local session_name="$1"
  gt heartbeat status --session="$session_name" --json 2>/dev/null || true
}

# heartbeat_age_seconds returns the age of a polecat session's heartbeat
# (preferring the v3 effective-keepalive age via `gt heartbeat status`).
# Falls back to direct file read with v3-aware effective-freshness when
# `gt heartbeat status` is unavailable (binary rollout window).
heartbeat_age_seconds() {
  local session_name="$1"
  local snap
  snap=$(heartbeat_status_json "$session_name")
  if [ -n "$snap" ] && [ "$snap" != "null" ]; then
    local age
    age=$(echo "$snap" | jq -r '.age_seconds // empty' 2>/dev/null)
    if [ -n "$age" ]; then
      echo "$age"
      return 0
    fi
  fi
  local hb_file="${TOWN_ROOT}/.runtime/heartbeats/${session_name}.json"
  [ -f "$hb_file" ] || return 0
  local hb_ts last_keepalive
  hb_ts=$(jq -r '(.timestamp // empty) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601? // empty' "$hb_file" 2>/dev/null)
  last_keepalive=$(jq -r '(.last_keepalive // empty) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601? // empty' "$hb_file" 2>/dev/null)
  if [ -n "$last_keepalive" ] && { [ -z "$hb_ts" ] || [ "$last_keepalive" -gt "$hb_ts" ]; }; then
    hb_ts="$last_keepalive"
  fi
  [ -n "$hb_ts" ] || return 0
  echo $(( $(date +%s) - hb_ts ))
}

# heartbeat_state returns the agent-reported state from the heartbeat
# ("working", "idle", "exiting", "stuck"), or empty string if unknown.
# Prefers `gt heartbeat status --json`; falls back to direct file read.
heartbeat_state() {
  local session_name="$1"
  local snap
  snap=$(heartbeat_status_json "$session_name")
  if [ -n "$snap" ] && [ "$snap" != "null" ]; then
    local state
    state=$(echo "$snap" | jq -r '.state // empty' 2>/dev/null)
    if [ -n "$state" ]; then
      echo "$state"
      return 0
    fi
  fi
  local hb_file="${TOWN_ROOT}/.runtime/heartbeats/${session_name}.json"
  [ -f "$hb_file" ] || return 0
  jq -r '.state // empty' "$hb_file" 2>/dev/null
}

# heartbeat_verdict returns the typed liveness verdict ("ALIVE",
# "MAYBE_DEAD", "DEAD", "UNKNOWN") for a session. cv-p3fem Phase 3.
# Empty string means `gt heartbeat status` was unavailable.
heartbeat_verdict() {
  local session_name="$1"
  local snap
  snap=$(heartbeat_status_json "$session_name")
  [ -n "$snap" ] && [ "$snap" != "null" ] || return 0
  echo "$snap" | jq -r '.verdict // empty' 2>/dev/null
}

# session_pid_alive echoes "alive" / "dead" / "" (unknown) for a session.
# The unknown state matters: when we can't determine PID liveness we MUST
# NOT count this agent toward MASS_DEAD — single-class signals can't fan
# out (gs-549).
session_pid_alive() {
  local session_name="$1"
  tmux has-session -t "$session_name" 2>/dev/null || { echo "dead"; return 0; }
  local pid
  pid=$(tmux list-panes -t "$session_name" -F '#{pane_pid}' 2>/dev/null | head -1 || true)
  if [ -z "$pid" ]; then
    echo ""
    return 0
  fi
  if kill -0 "$pid" 2>/dev/null; then
    echo "alive"
  else
    echo "dead"
  fi
}

# pane_progressing returns 0 (true) if a session's tmux pane content changes
# between two captures STUCK_PROGRESS_SAMPLE_GAP seconds apart, 1 (false) if it
# is byte-for-byte frozen. Returns 2 (unknown) when the pane cannot be captured
# (session gone mid-check, tmux error) so callers can fail safe.
#
# This is the dog-side liveness signal mandated by gu-wuduc: heartbeat-FILE-AGE
# alone is insufficient because an agent in a single long operation (e.g. the
# deacon's 15-20min `gt patrol report`) writes no heartbeat for the op's
# duration yet is fully alive. A live agent animates its pane — streaming LLM
# tokens, an advancing spinner, the "esc to interrupt · NmNNs" elapsed counter —
# so a changed pane proves liveness even when the heartbeat is stale.
pane_progressing() {
  local session_name="$1"
  local first second
  first=$(tmux capture-pane -p -t "$session_name" -S -40 2>/dev/null) || return 2
  sleep "$STUCK_PROGRESS_SAMPLE_GAP"
  second=$(tmux capture-pane -p -t "$session_name" -S -40 2>/dev/null) || return 2
  if [ "$first" != "$second" ]; then
    return 0
  fi
  return 1
}

# corroborated_dead returns 0 if a session has independently failed BOTH
# the heartbeat liveness check (verdict==DEAD) AND the PID liveness check
# (process gone). cv-p3fem security mitigation #1: each agent in a
# CRITICAL/MASS-DEATH count must independently fail two signal classes
# before counting toward escalation. The 2026-05-19 mass-kill (gs-549)
# fed off three independently stale heartbeats with live PIDs — under
# this rule that scenario yields TOTAL_DEAD=0 and never escalates.
corroborated_dead() {
  local session_name="$1"
  local verdict pid_state
  verdict=$(heartbeat_verdict "$session_name")
  pid_state=$(session_pid_alive "$session_name")
  # No verdict (binary rollout / missing gt) → single-class signal,
  # cannot count.
  if [ -z "$verdict" ] || [ "$verdict" = "UNKNOWN" ]; then
    return 1
  fi
  # PID check must agree.
  if [ "$pid_state" != "dead" ]; then
    return 1
  fi
  if [ "$verdict" = "DEAD" ]; then
    return 0
  fi
  return 1
}

# polecat_agent_bead_id returns the agent bead ID for a polecat using the
# AgentBeadIDWithPrefix collapsed-form rules (internal/beads/agent_ids.go):
# when the rig prefix equals the rig name, the rig component is omitted.
polecat_agent_bead_id() {
  local prefix="$1" rig="$2" pcat="$3"
  if [ "$prefix" = "$rig" ]; then
    echo "${prefix}-polecat-${pcat}"
  else
    echo "${prefix}-${rig}-polecat-${pcat}"
  fi
}

# polecat_clean_exit_signal echoes a non-empty token if the polecat's agent
# bead shows evidence of a deliberate, clean completion. Empty output means
# "no clean-exit signal — proceed with crash detection".
#
# Recognized signals (mirrors witness logic in internal/witness/handlers.go):
#   - exit_type set to COMPLETED, ESCALATED, DEFERRED, or PHASE_COMPLETE:
#     gt done wrote completion metadata before the session exited.
#   - agent_state set to done, nuked, or idle: intentional shutdown.
#   - status closed on the agent bead: polecat was nuked/retired.
polecat_clean_exit_signal() {
  local agent_bead="$1"
  [ -n "$agent_bead" ] || return 0
  local snap
  snap=$(bd show "$agent_bead" --json 2>/dev/null) || return 0
  [ -n "$snap" ] && [ "$snap" != "null" ] && [ "$snap" != "[]" ] || return 0

  local status agent_state exit_type
  status=$(echo "$snap" | jq -r '.[0].status // empty' 2>/dev/null)
  if [ "$status" = "closed" ]; then
    echo "agent_bead_closed"
    return 0
  fi
  agent_state=$(echo "$snap" \
    | jq -r '.[0].description // ""' 2>/dev/null \
    | awk -F': *' '/^agent_state:/ {print $2; exit}')
  case "$agent_state" in
    done|nuked|idle) echo "agent_state_${agent_state}"; return 0 ;;
  esac
  exit_type=$(echo "$snap" \
    | jq -r '.[0].description // ""' 2>/dev/null \
    | awk -F': *' '/^exit_type:/ {print $2; exit}')
  case "$exit_type" in
    COMPLETED|ESCALATED|DEFERRED|PHASE_COMPLETE) echo "exit_type_${exit_type}"; return 0 ;;
  esac
  return 0
}

# polecat_has_open_cleanup_wisp returns 0 if a cleanup wisp is already open for
# this polecat (witness already classified it as POLECAT_DIED), 1 otherwise.
# Mirrors findAnyCleanupWisp in internal/witness/handlers.go so we don't
# re-flag the same dead session on every 5m cycle and trip mass-death.
polecat_has_open_cleanup_wisp() {
  local pcat="$1"
  local out
  out=$(bd list --label "cleanup,polecat:${pcat}" --status open --json 2>/dev/null) || return 1
  [ -n "$out" ] && [ "$out" != "[]" ] && [ "$out" != "null" ] || return 1
  local count
  count=$(echo "$out" | jq 'length' 2>/dev/null)
  [ -n "$count" ] && [ "$count" -gt 0 ] 2>/dev/null
}

heartbeat_epoch() {
  local file="$1"
  local ts=""

  ts=$(jq -r '(.timestamp // empty) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601? // empty' "$file" 2>/dev/null || true)
  if [ -n "$ts" ]; then
    echo "$ts"
    return 0
  fi

  # Fallback for malformed legacy files: use mtime rather than failing open.
  stat -f %m "$file" 2>/dev/null || stat -c %Y "$file" 2>/dev/null
}

has_in_progress_work() {
  local locations=("$TOWN_ROOT")
  local rig=""
  local prefix=""
  local loc=""
  local output=""
  local count=""

  while IFS='|' read -r rig prefix; do
    [ -z "$rig" ] && continue
    [ -d "$TOWN_ROOT/$rig" ] && locations+=("$TOWN_ROOT/$rig")
  done <<< "$RIG_PREFIX_MAP"

  for loc in "${locations[@]}"; do
    output=$(cd "$loc" && bd list --status=in_progress --json --limit=1 2>/dev/null) || return 0
    count=$(printf '%s' "$output" | jq 'length' 2>/dev/null || echo 1)
    if [ "${count:-1}" -gt 0 ]; then
      return 0
    fi
  done

  return 1
}

# --- Enumerate agents ---------------------------------------------------------

log "=== Checking agent health ==="

if [ ! -f "$RIGS_JSON_PATH" ]; then
  log "SKIP: rigs.json not found"
  exit 0
fi

# Build rig_name|prefix mapping
RIG_PREFIX_MAP=$(jq -r '.rigs | to_entries[] | "\(.key)|\(.value.beads.prefix // .key)"' "$RIGS_JSON_PATH" 2>/dev/null)
if [ -z "$RIG_PREFIX_MAP" ]; then
  log "SKIP: no rigs in rigs.json"
  exit 0
fi

# --- Check polecat health ----------------------------------------------------

CRASHED=()
STUCK=()
STALLED=()
IDENTITY_HOOKED=()
HEALTHY=0

# Per-rig skip list (gu-31mpz): when a systemic bug requires preserving stranded
# polecat worktrees as forensic evidence (e.g. gc-hl4lx kept 4 talontriage
# polecats in forensic-hold for hours), the dog otherwise regenerates
# RESTART_POLECAT signals every 5m cycle, forcing the deacon to manually
# suppress each one. Listing a rig name in this file skips all of its polecats
# in the health iteration — neither flagged nor restarted — until the rig is
# removed from the file. Format: newline-separated rig names; blank lines and
# `#` comments are ignored.
SKIP_RIGS_FILE="${TOWN_ROOT}/.runtime/dog-skip-rigs"
SKIP_RIGS=""
if [ -f "$SKIP_RIGS_FILE" ]; then
  # Strip comments and blank lines; trim leading/trailing whitespace per line.
  SKIP_RIGS=$(sed -E 's/[[:space:]]+//g; /^#/d; /^$/d' "$SKIP_RIGS_FILE" 2>/dev/null | sort -u || true)
  if [ -n "$SKIP_RIGS" ]; then
    log "SKIP_RIGS active: $(echo "$SKIP_RIGS" | tr '\n' ',' | sed 's/,$//')"
  fi
fi

rig_is_skipped() {
  local rig="$1"
  [ -n "$SKIP_RIGS" ] || return 1
  printf '%s\n' "$SKIP_RIGS" | grep -qxF "$rig"
}

while IFS='|' read -r RIG PREFIX; do
  [ -z "$RIG" ] && continue
  if rig_is_skipped "$RIG"; then
    log "  SKIP rig $RIG: listed in $SKIP_RIGS_FILE"
    continue
  fi
  POLECAT_DIR="$TOWN_ROOT/$RIG/polecats"
  [ -d "$POLECAT_DIR" ] || continue

  for PCAT_PATH in "$POLECAT_DIR"/*/; do
    [ -d "$PCAT_PATH" ] || continue
    PCAT_NAME=$(basename "$PCAT_PATH")
    SESSION_NAME="${PREFIX}-${PCAT_NAME}"

    if ! tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
      # Session dead — check hook.
      # Uses `gt hook show` (not `gt hook`) because bare `gt hook ` now
      # tries to *attach* the path as a bead after the subcommand refactor.
      # `|| true` so a missing entry doesn't abort under `set -euo pipefail`.
      # Trailing `|| true` also swallows SIGPIPE (rc=141) when `head -1`
      # closes the pipe before `gt hook show` finishes writing — that
      # caused intermittent rc=1 early-exits after only the header line
      # printed (gu-6zhl).
      HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1 || true)
      HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)

      if [ -n "$HOOK_BEAD" ]; then
        # Filter 1: hook bead already closed → polecat completed normally.
        HOOK_STATUS=$(bd show "$HOOK_BEAD" --json 2>/dev/null \
          | jq -r '.[0].status // empty' 2>/dev/null || echo "")
        if [ "$HOOK_STATUS" = "closed" ]; then
          log "  SKIP $SESSION_NAME: hook bead closed (completed normally)"
          continue
        fi

        # Filter 2 (gu-w9or): consult the polecat's own agent bead for clean-exit
        # signals. A polecat that ran `gt done` writes exit_type/completion_time
        # to its agent bead before the session dies; the hook bead may still be
        # open (refinery hasn't merged the MR yet). Without this filter, every
        # legitimate completion within a 5m cycle counts as CRASHED, and 3+ in
        # one cycle trips MASS DEATH → spurious CRITICAL escalation.
        AGENT_BEAD=$(polecat_agent_bead_id "$PREFIX" "$RIG" "$PCAT_NAME")
        CLEAN_SIGNAL=$(polecat_clean_exit_signal "$AGENT_BEAD")
        if [ -n "$CLEAN_SIGNAL" ]; then
          log "  SKIP $SESSION_NAME: clean exit (${CLEAN_SIGNAL}, agent_bead=$AGENT_BEAD)"
          continue
        fi

        # Filter 3 (gu-w9or): witness already filed a cleanup wisp for this
        # polecat — it has been classified, no need to re-CRASH on each cycle.
        if polecat_has_open_cleanup_wisp "$PCAT_NAME"; then
          log "  SKIP $SESSION_NAME: cleanup wisp already open"
          continue
        fi

        CRASHED+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD")
        log "  CRASHED: $SESSION_NAME (hook=$HOOK_BEAD)"
      fi
    else
      # Session alive — check process
      PANE_PID=$(tmux list-panes -t "$SESSION_NAME" -F '#{pane_pid}' 2>/dev/null | head -1)
      if [ -n "$PANE_PID" ]; then
        PROC_COMM=$(ps -o comm= -p "$PANE_PID" 2>/dev/null || true)
        if [ -z "$PROC_COMM" ]; then
          # Zombie: process dead, session alive.
          # Uses `gt hook show` — see note above on subcommand refactor.
          # Trailing `|| true` also masks SIGPIPE from head -1 (gu-6zhl).
          HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1 || true)
          HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)
          if [ -n "$HOOK_BEAD" ]; then
            STUCK+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD|agent_dead")
            log "  ZOMBIE: $SESSION_NAME (pid=$PANE_PID dead, hook=$HOOK_BEAD)"
          fi
        else
          # Process alive — check for stalled-alive case (gu-bfwa):
          # session + process alive, but agent is idle at prompt with hooked work
          # not progressing. Detected by a stale heartbeat on a hooked session.
          # Trailing `|| true` masks SIGPIPE from head -1 (gu-6zhl).
          HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1 || true)
          HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)

          if [ -n "$HOOK_BEAD" ]; then
            # Flag identity-bead hooks as a separate anomaly class. Auto-dispatch
            # is supposed to filter these; if one leaks through we want to know.
            # Use bash's [[ =~ ]] rather than `grep -E "$pat"` because the pattern
            # begins with `-` and grep treats leading-dash patterns as option flags
            # (`grep: invalid option -- '('`), spamming stderr on every cycle (gu-brrm).
            if [[ "$HOOK_BEAD" =~ $IDENTITY_BEAD_PATTERN ]]; then
              IDENTITY_HOOKED+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD")
              log "  IDENTITY-HOOK: $SESSION_NAME hooked to identity bead $HOOK_BEAD"
            fi

            # Check heartbeat staleness.
            HB_AGE=$(heartbeat_age_seconds "$SESSION_NAME")
            if [ -n "$HB_AGE" ] && [ "$HB_AGE" -gt "$STUCK_STALLED_THRESHOLD" ]; then
              # Heartbeat state matters: "exiting" is legitimate (gt done in flight),
              # "idle" means agent reported itself idle, "stuck" means self-escalated.
              # Only flag "working" (or empty/v1) as stalled.
              HB_STATE=$(heartbeat_state "$SESSION_NAME")
              case "$HB_STATE" in
                exiting|idle|stuck)
                  log "  OK $SESSION_NAME: heartbeat stale (${HB_AGE}s) but state=$HB_STATE"
                  HEALTHY=$((HEALTHY + 1))
                  ;;
                *)
                  STALLED+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD|stalled_heartbeat_${HB_AGE}s")
                  log "  STALLED: $SESSION_NAME (heartbeat ${HB_AGE}s stale, hook=$HOOK_BEAD)"
                  ;;
              esac
            else
              HEALTHY=$((HEALTHY + 1))
            fi
          else
            # No hook = not working, nothing to stall on
            HEALTHY=$((HEALTHY + 1))
          fi
        fi
      else
        HEALTHY=$((HEALTHY + 1))
      fi
    fi
  done
done <<< "$RIG_PREFIX_MAP"

log ""
log "Polecat health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, ${#STALLED[@]} stalled, ${#IDENTITY_HOOKED[@]} identity-hooked, $HEALTHY healthy"

# --- Check deacon health -----------------------------------------------------

log ""
log "=== Deacon Health ==="

DEACON_SESSION="hq-deacon"
DEACON_ISSUE=""
DEACON_PROCESS_ALIVE=0

if ! tmux has-session -t "$DEACON_SESSION" 2>/dev/null; then
  log "  CRASHED: Deacon session is dead"
  DEACON_ISSUE="crashed"
else
  DEACON_PID=$(tmux list-panes -t "$DEACON_SESSION" -F '#{pane_pid}' 2>/dev/null | head -1)
  DEACON_COMM=$(ps -o comm= -p "$DEACON_PID" 2>/dev/null || true)
  if [ -z "$DEACON_COMM" ]; then
    log "  ZOMBIE: Deacon process dead (pid=$DEACON_PID), session alive"
    DEACON_ISSUE="zombie"
  else
    log "  Process alive: pid=$DEACON_PID comm=$DEACON_COMM"
    DEACON_PROCESS_ALIVE=1
  fi

  # Primary liveness check: session heartbeat (updated by any gt command, not
  # just patrol cycles). This prevents false positives when deacon is alive and
  # responding to nudges but its patrol cycles aren't firing (gs-peo).
  DEACON_HB_AGE=$(heartbeat_age_seconds "$DEACON_SESSION")
  if [ -n "$DEACON_HB_AGE" ]; then
    if [ "$DEACON_HB_AGE" -gt 1200 ]; then
      # gu-wuduc: heartbeat-age alone is a false-positive flood. The deacon's
      # `gt patrol report` is a single 15-20min op that writes no heartbeat for
      # its duration but is fully alive. Corroborate with live pane progress
      # before escalating: only declare stuck if the pane is FROZEN (or the
      # session is gone). A changed pane (spinner/tokens/elapsed advancing)
      # proves liveness despite the stale heartbeat.
      if pane_progressing "$DEACON_SESSION"; then
        log "  OK: Deacon session heartbeat stale (${DEACON_HB_AGE}s old) but pane progressing — alive (gu-wuduc)"
      else
        PROG_RC=$?
        if [ "$PROG_RC" = "2" ]; then
          log "  STUCK: Deacon session heartbeat stale (${DEACON_HB_AGE}s old, >20m threshold); pane uncapturable — treating as stuck"
        else
          log "  STUCK: Deacon session heartbeat stale (${DEACON_HB_AGE}s old, >20m threshold) AND pane frozen across samples (gu-wuduc)"
        fi
        DEACON_ISSUE="stuck_heartbeat_${DEACON_HB_AGE}s"
      fi
    else
      log "  OK: Deacon session heartbeat ${DEACON_HB_AGE}s old"
    fi
  else
    # Session heartbeat absent: fall back to patrol heartbeat mtime (legacy).
    # mtime lookup: GNU coreutils (Linux) uses `stat -c %Y`, BSD/macOS uses
    # `stat -f %m`. Detect the OS once and call the right flavor.
    PATROL_FILE="$TOWN_ROOT/deacon/heartbeat.json"
    if [ -f "$PATROL_FILE" ]; then
      if stat -c %Y / >/dev/null 2>&1; then
        PATROL_TIME=$(stat -c %Y "$PATROL_FILE")
      else
        PATROL_TIME=$(stat -f %m "$PATROL_FILE")
      fi
      PATROL_AGE=$(( $(date +%s) - PATROL_TIME ))
      if [ "$PATROL_AGE" -gt 1200 ]; then
        # gu-wuduc: corroborate stale patrol heartbeat with live pane progress
        # before escalating (same rationale as the session-heartbeat path).
        if pane_progressing "$DEACON_SESSION"; then
          log "  OK: Deacon patrol heartbeat stale (${PATROL_AGE}s old) but pane progressing — alive (gu-wuduc)"
        else
          PROG_RC=$?
          if [ "$PROG_RC" = "2" ]; then
            log "  STUCK: Deacon patrol heartbeat stale (${PATROL_AGE}s old, >20m threshold, no session heartbeat); pane uncapturable — treating as stuck"
          else
            log "  STUCK: Deacon patrol heartbeat stale (${PATROL_AGE}s old, >20m threshold, no session heartbeat) AND pane frozen across samples (gu-wuduc)"
          fi
          DEACON_ISSUE="stuck_heartbeat_${PATROL_AGE}s"
        fi
      else
        log "  OK: Deacon patrol heartbeat ${PATROL_AGE}s old"
      fi
    fi
  fi
fi

# --- Mass death check ---------------------------------------------------------

# cv-p3fem Phase 3: per-agent independent corroboration before counting any
# agent toward MASS_DEAD. Each candidate must have BOTH (a) verdict==DEAD
# from the typed liveness API AND (b) PID-gone confirmation from tmux.
# Single-class signals (transient FS issue, three corrupt JSONs) can no
# longer fan out to a CRITICAL escalation.
TOTAL_ISSUES=$(( ${#CRASHED[@]} + ${#STUCK[@]} + ${#STALLED[@]} ))
TOTAL_DEAD=0
DEAD_NAMES=""
for ENTRY in ${CRASHED[@]+"${CRASHED[@]}"} ${STUCK[@]+"${STUCK[@]}"} ${STALLED[@]+"${STALLED[@]}"}; do
  IFS='|' read -r SESSION REST <<< "$ENTRY"
  if corroborated_dead "$SESSION"; then
    TOTAL_DEAD=$((TOTAL_DEAD + 1))
    DEAD_NAMES="$DEAD_NAMES $SESSION"
  fi
done

if [ "$TOTAL_DEAD" -ge 3 ]; then
  log ""
  log "MASS DEATH: $TOTAL_DEAD agents corroborated dead — escalating (issues=$TOTAL_ISSUES)"
  gt escalate "Mass agent death detected by stuck-agent-dog" \
    -s CRITICAL \
    --source=stuck-agent-dog --dedup --signature=stuck-agent-dog:mass-death \
    -r "$TOTAL_DEAD agents corroborated dead (verdict=DEAD AND PID gone):$DEAD_NAMES" 2>/dev/null || true
elif [ "$TOTAL_ISSUES" -ge 3 ]; then
  # Counted issues exist but corroboration didn't agree — log so operators
  # can see the gate working without paging Mayor.
  log ""
  log "MASS DEATH GATED: $TOTAL_ISSUES issues, only $TOTAL_DEAD corroborated dead — not escalating (cv-p3fem)"
fi

# --- Take action --------------------------------------------------------------

# load_too_high reports whether the 1-minute load average exceeds
# STUCK_LOAD_DEFER_RATIO × CPU_count. Echoes a non-empty diagnostic string
# (e.g. "load=25.4 cpus=12 ratio=2.12 threshold=2.00") when over the bar;
# empty when under it or when the ratio is disabled (=0).
#
# Falls back to "" (proceed with restart) on any platform we can't query —
# the dog should never WORSEN a situation it can't observe, but it must
# remain functional on unfamiliar systems where /proc/loadavg is absent.
#
# Linux: /proc/loadavg + nproc. macOS: sysctl. Otherwise: silent passthrough.
load_too_high() {
  local ratio="${STUCK_LOAD_DEFER_RATIO}"
  # Disabled.
  awk "BEGIN { exit !($ratio > 0) }" 2>/dev/null || return 0

  local load1=""
  local cpus=""

  if [ -r /proc/loadavg ]; then
    load1=$(awk '{print $1}' /proc/loadavg 2>/dev/null)
    cpus=$(nproc 2>/dev/null || echo "")
  elif command -v sysctl >/dev/null 2>&1; then
    # macOS: vm.loadavg → "{ 1.23 4.56 7.89 }"
    load1=$(sysctl -n vm.loadavg 2>/dev/null | awk '{print $2}')
    cpus=$(sysctl -n hw.ncpu 2>/dev/null || echo "")
  fi

  [ -n "$load1" ] && [ -n "$cpus" ] && [ "$cpus" -gt 0 ] 2>/dev/null || return 0

  # Compare load1/cpus > ratio. Use awk for float math (POSIX shell has none).
  awk -v l="$load1" -v c="$cpus" -v r="$ratio" \
    'BEGIN { lpc = l / c; if (lpc > r) printf("load=%.2f cpus=%d ratio=%.2f threshold=%.2f", l, c, lpc, r); }'
}

# has_recovery_marker checks for an active manual-recovery awareness flag (gu-v5mk).
# When the witness/mayor performs an out-of-band recovery (e.g. manual --no-verify
# push), they set this marker so we don't re-run already-pushed work via
# RESTART_POLECAT and re-hit the original hang. Returns 0 if a fresh marker exists.
has_recovery_marker() {
  local rig="$1" pcat="$2"
  gt polecat is-recovered "$rig/$pcat" >/dev/null 2>&1
}

# gs-549 fix #3: defer RESTART_POLECAT actions under sustained load. The dog
# is the upstream of the cascade that killed 14 polecats in lia_bac on
# 2026-05-19 — restarting under high load inherits the same load and re-stalls
# the new session inside a single dog cycle. When the box is genuinely
# overloaded, EVERY polecat in the rig is stalling, so the per-polecat
# RESTART action is the wrong tool. Wait it out instead.
#
# Computed once and shared across CRASHED/STUCK/STALLED loops so operators
# see a single deferral message rather than one per polecat.
LOAD_DEFER_REASON=$(load_too_high)
if [ -n "$LOAD_DEFER_REASON" ]; then
  TOTAL_DEFERRED=$(( ${#CRASHED[@]} + ${#STUCK[@]} + ${#STALLED[@]} ))
  if [ "$TOTAL_DEFERRED" -gt 0 ]; then
    log "DEFER: load too high, deferring $TOTAL_DEFERRED RESTART_POLECAT action(s) ($LOAD_DEFER_REASON)"
  else
    log "load too high but no restart actions queued ($LOAD_DEFER_REASON)"
  fi
fi

# Crashed polecats: notify witness to restart
# Note: `"${arr[@]:-}"` expands an empty array to a single empty string under
# `set -u`, which would fire a phantom `RESTART_POLECAT: /` notification. The
# `${arr[@]+"${arr[@]}"}` form expands to nothing when the array is empty.
for ENTRY in ${CRASHED[@]+"${CRASHED[@]}"}; do
  if [ -n "$LOAD_DEFER_REASON" ]; then
    continue
  fi
  IFS='|' read -r SESSION RIG PCAT HOOK <<< "$ENTRY"
  if has_recovery_marker "$RIG" "$PCAT"; then
    log "SKIP RESTART for $RIG/polecats/$PCAT: manual-recovery marker active (gu-v5mk)"
    gt mail send "$RIG/witness" -s "NUKE_PENDING: $RIG/$PCAT (manual-recovery marker)" --stdin <<BODY
Polecat $PCAT crashed but has an active manual-recovery marker.
hook_bead: $HOOK
action: skipped RESTART_POLECAT — work already recovered out-of-band; nuke when convenient
BODY
    continue
  fi
  log "Requesting restart for $RIG/polecats/$PCAT (hook=$HOOK)"
  # Routing note (gu-nep2): RESTART_POLECAT mail used to be addressed to
  # "$RIG/witness", but nothing in Go code or the witness formula actually
  # processed the subject — requests piled up and polecats stayed dead
  # until a human ran `gt session start` by hand. The daemon now polls the
  # deacon inbox every heartbeat (processRestartPolecatRequests) and
  # restarts the referenced polecat via witness.RestartPolecatWithBackoff,
  # so addressing the mail to "deacon/" lets the daemon claim the message
  # before any LLM agent touches it. NUKE_PENDING and other informational
  # mail is still addressed to the witness (the audience that needs to see
  # it).
  gt mail send "deacon/" -s "RESTART_POLECAT: $RIG/$PCAT" --stdin <<BODY
Polecat $PCAT crash confirmed by stuck-agent-dog plugin.
rig: $RIG
polecat: $PCAT
hook_bead: $HOOK
action: restart requested
BODY
done

# Zombie polecats: kill zombie session, then request restart
for ENTRY in ${STUCK[@]+"${STUCK[@]}"}; do
  if [ -n "$LOAD_DEFER_REASON" ]; then
    continue
  fi
  IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"
  if has_recovery_marker "$RIG" "$PCAT"; then
    log "SKIP RESTART for $RIG/polecats/$PCAT (zombie): manual-recovery marker active (gu-v5mk)"
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    gt mail send "$RIG/witness" -s "NUKE_PENDING: $RIG/$PCAT (zombie + manual-recovery marker)" --stdin <<BODY
Polecat $PCAT zombie session cleared but has an active manual-recovery marker.
hook_bead: $HOOK
reason: $REASON
action: skipped RESTART_POLECAT — work already recovered out-of-band; nuke when convenient
BODY
    continue
  fi
  log "Killing zombie session $SESSION and requesting restart"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  # gu-nep2: route to deacon inbox so the daemon's
  # processRestartPolecatRequests handler picks it up.
  gt mail send "deacon/" -s "RESTART_POLECAT: $RIG/$PCAT (zombie cleared)" --stdin <<BODY
Polecat $PCAT zombie session cleared by stuck-agent-dog plugin.
rig: $RIG
polecat: $PCAT
hook_bead: $HOOK
reason: $REASON
action: restart requested
BODY
done

# Stalled polecats (gu-bfwa): session + agent process alive but heartbeat stale.
# Agent is likely sitting idle at its prompt with hooked work. Kill the session
# so the witness respawn can give it a fresh session and reload context via
# `gt prime`. The hook and worktree are preserved.
for ENTRY in "${STALLED[@]}"; do
  if [ -n "$LOAD_DEFER_REASON" ]; then
    continue
  fi
  IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"
  if has_recovery_marker "$RIG" "$PCAT"; then
    log "SKIP RESTART for $RIG/polecats/$PCAT (stalled): manual-recovery marker active (gu-v5mk)"
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    gt mail send "$RIG/witness" -s "NUKE_PENDING: $RIG/$PCAT (stalled + manual-recovery marker)" --stdin <<BODY
Polecat $PCAT was stalled-alive but has an active manual-recovery marker.
hook_bead: $HOOK
reason: $REASON
action: session killed; skipped RESTART_POLECAT — nuke when convenient
BODY
    continue
  fi
  log "Killing stalled session $SESSION and requesting restart"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  # gu-nep2: route to deacon inbox so the daemon's
  # processRestartPolecatRequests handler picks it up.
  gt mail send "deacon/" -s "RESTART_POLECAT: $RIG/$PCAT (stalled-alive cleared)" --stdin <<BODY
Polecat $PCAT session was alive but heartbeat was stale — agent idle at prompt.
rig: $RIG
polecat: $PCAT
hook_bead: $HOOK
reason: $REASON
action: restart requested (hook and worktree preserved)
BODY
done

# Identity-bead hooks: auto-dispatch should never have hooked an identity bead
# to a polecat (refinery/witness/mayor/deacon). Escalate rather than restart,
# since respawning the polecat will re-load the bad hook. Mayor/human needs to
# unhook manually and fix the dispatch path.
for ENTRY in "${IDENTITY_HOOKED[@]}"; do
  IFS='|' read -r SESSION RIG PCAT HOOK <<< "$ENTRY"
  log "Escalating identity-bead hook: $RIG/polecats/$PCAT -> $HOOK"
  # Per-slot stable signature: one open escalation per hooked polecat slot.
  SAFE_SIG="stuck-agent-dog:identity-hook:$(echo "$RIG/$PCAT" | tr '/' ':')"
  gt escalate "Polecat hooked to identity bead: $RIG/$PCAT" \
    -s HIGH \
    --source=stuck-agent-dog --dedup --signature="$SAFE_SIG" \
    -r "hook=$HOOK" 2>/dev/null || true
done

# Deacon issues: escalate with a stable signature so repeated detections
# (stuck_heartbeat_1234s, stuck_heartbeat_1250s, ...) map to one open bead.
if [ -n "$DEACON_ISSUE" ]; then
  log "Escalating deacon issue: $DEACON_ISSUE"
  # Normalize: strip trailing numeric counter (_NNNs) to get a stable key.
  DEACON_SIG=$(echo "$DEACON_ISSUE" | sed 's/_[0-9]*s$//')
  gt escalate "Deacon $DEACON_SIG detected by stuck-agent-dog" \
    -s HIGH \
    --source=stuck-agent-dog --dedup --signature="stuck-agent-dog:deacon:$DEACON_SIG" \
    -r "Latest detection: $DEACON_ISSUE" 2>/dev/null || true
fi

# --- Report -------------------------------------------------------------------

SUMMARY="Agent health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, ${#STALLED[@]} stalled, ${#IDENTITY_HOOKED[@]} identity-hooked, $HEALTHY healthy"
[ -n "$DEACON_ISSUE" ] && SUMMARY="$SUMMARY, deacon=$DEACON_ISSUE"
log ""
log "=== $SUMMARY ==="

bd create "stuck-agent-dog: $SUMMARY" -t chore --ephemeral \
  -l type:plugin-run,plugin:stuck-agent-dog,result:success \
  -d "$SUMMARY" --silent 2>/dev/null || true

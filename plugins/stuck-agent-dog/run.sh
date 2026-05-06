#!/usr/bin/env bash
# stuck-agent-dog/run.sh — Context-aware stuck/crashed agent detection.
#
# SCOPE: Only polecats and deacon. NEVER touches crew, mayor, witness, or refinery.
# The daemon detects; this plugin inspects context before acting.

set -euo pipefail

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
RIGS_JSON_PATH="${TOWN_ROOT}/mayor/rigs.json"

# STALLED_THRESHOLD is the age (seconds) at which an alive-but-idle polecat
# session is considered stalled. Set higher than the 3-min heartbeat stale
# threshold used by the witness (internal/polecat/heartbeat.go) to avoid false
# positives on legitimate long-running operations (builds, tests, LLM calls).
# The dog runs every 5m, so 10m gives 2 cycles of grace before flagging.
STUCK_STALLED_THRESHOLD="${STUCK_STALLED_THRESHOLD:-600}"

# Identity-bead hook anomaly: a polecat should NEVER be hooked to an identity
# bead (refinery/witness/mayor/deacon). Auto-dispatch's filter excludes these,
# but if one leaks through (e.g. manual `gt hook` error, sling-context bug),
# the polecat will loop on a bead that it cannot make progress on. This regex
# matches common identity-bead suffixes used across rigs.
IDENTITY_BEAD_PATTERN='-(refinery|witness|mayor|deacon)$'

log() { echo "[stuck-agent-dog] $*"; }

# heartbeat_age_seconds returns the age of a polecat session's heartbeat file
# in seconds, or empty string if the heartbeat file does not exist or cannot
# be parsed. A missing heartbeat is not treated as stale (backward compat with
# IsSessionHeartbeatStale in internal/polecat/heartbeat.go).
heartbeat_age_seconds() {
  local session_name="$1"
  local hb_file="${TOWN_ROOT}/.runtime/heartbeats/${session_name}.json"
  [ -f "$hb_file" ] || return 0
  local hb_ts
  hb_ts=$(jq -r '(.timestamp // empty) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601? // empty' "$hb_file" 2>/dev/null)
  [ -n "$hb_ts" ] || return 0
  echo $(( $(date +%s) - hb_ts ))
}

# heartbeat_state returns the agent-reported state from the heartbeat file
# ("working", "idle", "exiting", "stuck"), or empty string if unknown. A v1
# heartbeat without a state field returns "" — callers should treat that as
# "working" for backward compatibility.
heartbeat_state() {
  local session_name="$1"
  local hb_file="${TOWN_ROOT}/.runtime/heartbeats/${session_name}.json"
  [ -f "$hb_file" ] || return 0
  jq -r '.state // empty' "$hb_file" 2>/dev/null
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

while IFS='|' read -r RIG PREFIX; do
  [ -z "$RIG" ] && continue
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
      HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1)
      HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)

      if [ -n "$HOOK_BEAD" ]; then
        # Check agent_state
        AGENT_STATE=$(bd show "$HOOK_BEAD" --json 2>/dev/null \
          | python3 -c "import json,sys; d=json.load(sys.stdin); print(d[0].get('status',''))" 2>/dev/null || echo "")

        case "$AGENT_STATE" in
          closed) log "  SKIP $SESSION_NAME: bead closed (completed normally)"; continue ;;
        esac

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
          HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1)
          HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)
          if [ -n "$HOOK_BEAD" ]; then
            STUCK+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD|agent_dead")
            log "  ZOMBIE: $SESSION_NAME (pid=$PANE_PID dead, hook=$HOOK_BEAD)"
          fi
        else
          # Process alive — check for stalled-alive case (gu-bfwa):
          # session + process alive, but agent is idle at prompt with hooked work
          # not progressing. Detected by a stale heartbeat on a hooked session.
          HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1)
          HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)

          if [ -n "$HOOK_BEAD" ]; then
            # Flag identity-bead hooks as a separate anomaly class. Auto-dispatch
            # is supposed to filter these; if one leaks through we want to know.
            if echo "$HOOK_BEAD" | grep -Eq "$IDENTITY_BEAD_PATTERN"; then
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
  fi

  # Primary liveness check: session heartbeat (updated by any gt command, not
  # just patrol cycles). This prevents false positives when deacon is alive and
  # responding to nudges but its patrol cycles aren't firing (gs-peo).
  DEACON_HB_AGE=$(heartbeat_age_seconds "$DEACON_SESSION")
  if [ -n "$DEACON_HB_AGE" ]; then
    if [ "$DEACON_HB_AGE" -gt 1200 ]; then
      log "  STUCK: Deacon session heartbeat stale (${DEACON_HB_AGE}s old, >20m threshold)"
      DEACON_ISSUE="stuck_heartbeat_${DEACON_HB_AGE}s"
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
        log "  STUCK: Deacon patrol heartbeat stale (${PATROL_AGE}s old, >20m threshold, no session heartbeat)"
        DEACON_ISSUE="stuck_heartbeat_${PATROL_AGE}s"
      else
        log "  OK: Deacon patrol heartbeat ${PATROL_AGE}s old"
      fi
    fi
  fi
fi

# --- Mass death check ---------------------------------------------------------

TOTAL_ISSUES=$(( ${#CRASHED[@]} + ${#STUCK[@]} + ${#STALLED[@]} ))
if [ "$TOTAL_ISSUES" -ge 3 ]; then
  log ""
  log "MASS DEATH: $TOTAL_ISSUES agents down — escalating instead of restarting"
  gt escalate "Mass agent death: $TOTAL_ISSUES agents down" \
    -s CRITICAL 2>/dev/null || true
fi

# --- Take action --------------------------------------------------------------

# Crashed polecats: notify witness to restart
for ENTRY in "${CRASHED[@]}"; do
  IFS='|' read -r SESSION RIG PCAT HOOK <<< "$ENTRY"
  log "Requesting restart for $RIG/polecats/$PCAT (hook=$HOOK)"
  gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT" --stdin <<BODY
Polecat $PCAT crash confirmed by stuck-agent-dog plugin.
hook_bead: $HOOK
action: restart requested
BODY
done

# Zombie polecats: kill zombie session, then request restart
for ENTRY in "${STUCK[@]}"; do
  IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"
  log "Killing zombie session $SESSION and requesting restart"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT (zombie cleared)" --stdin <<BODY
Polecat $PCAT zombie session cleared by stuck-agent-dog plugin.
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
  IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"
  log "Killing stalled session $SESSION and requesting restart"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT (stalled-alive cleared)" --stdin <<BODY
Polecat $PCAT session was alive but heartbeat was stale — agent idle at prompt.
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
  gt escalate "Polecat hooked to identity bead: $RIG/$PCAT -> $HOOK" \
    -s HIGH 2>/dev/null || true
done

# Deacon issues: escalate
if [ -n "$DEACON_ISSUE" ]; then
  log "Escalating deacon issue: $DEACON_ISSUE"
  gt escalate "Deacon $DEACON_ISSUE detected by stuck-agent-dog" -s HIGH 2>/dev/null || true
fi

# --- Report -------------------------------------------------------------------

SUMMARY="Agent health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, ${#STALLED[@]} stalled, ${#IDENTITY_HOOKED[@]} identity-hooked, $HEALTHY healthy"
[ -n "$DEACON_ISSUE" ] && SUMMARY="$SUMMARY, deacon=$DEACON_ISSUE"
log ""
log "=== $SUMMARY ==="

bd create "stuck-agent-dog: $SUMMARY" -t chore --ephemeral \
  -l type:plugin-run,plugin:stuck-agent-dog,result:success \
  -d "$SUMMARY" --silent 2>/dev/null || true

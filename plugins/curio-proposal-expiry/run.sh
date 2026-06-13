#!/usr/bin/env bash
# curio-proposal-expiry/run.sh — Nightly anti-wedge maintenance for the Curio
# Retrospect lane (Curio P3 B8, epic gu-60sk4, child bead gu-5zf4t). Cron-gated
# (30 7 * * *), it runs 30m BEFORE the 08:00 Retrospect dispatch so a sweep can
# free the volume breaker for the same night's run.
#
# Two independent, best-effort passes (design-doc Q7, child-beads §B8):
#
#   PASS 1 — proposal expiry. Auto-close open curio-proposal / curio-hypothesis
#     beads in the target rig untouched (by updated_at) longer than the expiry
#     window. Each close stamps a structured curio-outcome:<code> label so the
#     B0b reconciler (onCurioBeadClose) classifies it deterministically and feeds
#     (outcome, resolved_at) into curio_ledger for any ledger-tracked bead. This
#     plugin NEVER writes the ledger itself — it only closes beads accurately and
#     lets the trusted reconciler do the write (read/write air-gap).
#
#   PASS 2 — breaker-reset alert. Track how long the volume breaker (open
#     curio-proposal count >= ceiling) has been continuously tripped in a small
#     state file. When it exceeds M days, emit ONE low-severity escalation and
#     latch `alerted` so a wedged lane is visible, not silent — exactly once per
#     trip. Clearing the breaker resets the latch.
#
# This is the maintenance half B5's curio-retrospect-dispatch `## Related` flags:
# the dispatch breaker stops the lane when the backlog is deep, but nothing
# worked the backlog down. Expiry bounds it; the alert surfaces a genuine wedge.

set -uo pipefail
# NOTE: not `set -e` — each pass records its receipt and continues deliberately;
# one pass's failure must not abort the other.

PLUGIN_NAME="curio-proposal-expiry"
TARGET_RIG="gastown_upstream"

# --- tunables (operator-overridable) -----------------------------------------

# Age (days, by updated_at) past which an open proposal/hypothesis is closed.
EXPIRY_DAYS="${CURIO_PROPOSAL_EXPIRY_DAYS:-14}"

# Outcome stamped on an expired bead (curio-outcome:<code>). deferred is
# conservative: human inattention is not the rule's fault, so an expiry must NOT
# decrement the rule's measured precision (only false_positive does). Override
# with CURIO_EXPIRY_OUTCOME=fp|dup|fixed|deferred if a deployment wants otherwise.
EXPIRY_OUTCOME="${CURIO_EXPIRY_OUTCOME:-deferred}"

# Volume-breaker ceiling — the SAME value curio-retrospect-dispatch enforces.
PROPOSAL_CEILING="${CURIO_PROPOSAL_CEILING:-10}"

# Consecutive days the breaker must stay open before the one-shot alert fires.
BREAKER_ALERT_DAYS="${CURIO_BREAKER_ALERT_DAYS:-3}"

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
TOWN_ROOT="${TOWN_ROOT:-$HOME/gt}"

RIG_DIR="${TOWN_ROOT}/${TARGET_RIG}"
STATE_DIR="${TOWN_ROOT}/artifacts/curio-retrospect"
BREAKER_STATE="${STATE_DIR}/breaker-state.json"

NOW_EPOCH=$(date -u +%s)
DAY_SECS=86400

log() { echo "[${PLUGIN_NAME}] $*" >&2; }

record_receipt() {
  # $1=result (success|skipped|failure), $2=title-suffix, $3=description
  local result="$1" title="$2" desc="${3:-}"
  bd create "${PLUGIN_NAME}: ${title}" -t chore --ephemeral \
    -l "type:plugin-run,plugin:${PLUGIN_NAME},result:${result}" \
    -d "${desc}" --silent 2>/dev/null || true
}

# epoch_of RFC3339 -> unix seconds (empty string on parse failure).
epoch_of() {
  date -u -d "$1" +%s 2>/dev/null || true
}

# =============================================================================
# PASS 1 — proposal expiry
# =============================================================================
#
# We sweep open curio-proposal AND curio-hypothesis beads. Both labels are the
# B6 proposal-taxonomy markers (docs/curio-proposals.md). Only status==open is
# eligible: an in_progress bead is being worked, a blocked bead waits for a
# stated reason. Freshness is measured from updated_at, so any human touch (a
# comment, a relabel) resets the clock and protects an actively-discussed bead.

# expired_bead_ids LABEL — echoes the IDs of open beads with LABEL whose
# updated_at is older than the expiry window. Best-effort: any tooling failure
# echoes nothing (we skip rather than mis-close).
expired_bead_ids() {
  local label="$1"
  [[ -d "$RIG_DIR" ]] || return 0
  local open_json
  open_json=$(cd "$RIG_DIR" && bd list --label "$label" --status open --json 2>/dev/null || echo "[]")
  [[ -n "$open_json" && "$open_json" != "[]" && "$open_json" != "null" ]] || return 0

  local cutoff=$(( NOW_EPOCH - EXPIRY_DAYS * DAY_SECS ))
  while IFS=$'\t' read -r id updated; do
    [[ -n "$id" ]] || continue
    local up_epoch
    up_epoch=$(epoch_of "$updated")
    # Unparseable timestamp: skip (conservative — do not close on bad data).
    [[ -n "$up_epoch" ]] || { log "expiry: skipping ${id} (unparseable updated_at=${updated})"; continue; }
    if (( up_epoch < cutoff )); then
      echo "$id"
    fi
  done < <(
    jq -r '.[] | "\(.id)\t\(.updated_at)"' <<<"$open_json" 2>/dev/null || true
  )
}

run_expiry_pass() {
  if [[ ! -d "$RIG_DIR" ]]; then
    log "expiry: rig dir ${RIG_DIR} absent — nothing to sweep, skipping"
    record_receipt "skipped" "expiry: rig dir absent" \
      "Target rig dir ${RIG_DIR} does not exist; no proposal beads to expire."
    return 0
  fi

  # Union of expired IDs across both proposal labels, de-duplicated (a bead can
  # in principle carry both labels; close it at most once).
  local ids
  ids=$( { expired_bead_ids "curio-proposal"; expired_bead_ids "curio-hypothesis"; } | sort -u )

  if [[ -z "$ids" ]]; then
    log "expiry: no open proposal/hypothesis beads older than ${EXPIRY_DAYS}d — nothing to close"
    record_receipt "success" "expiry: nothing stale" \
      "No open curio-proposal/curio-hypothesis beads in ${TARGET_RIG} are older
than the ${EXPIRY_DAYS}-day expiry window. Backlog is fresh; nothing closed."
    return 0
  fi

  local closed=0 failed=0 closed_list=""
  local reason="Auto-closed by ${PLUGIN_NAME}: untouched > ${EXPIRY_DAYS}d (curio proposal expiry, B8). Outcome=${EXPIRY_OUTCOME}. Reopen if still relevant."
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    # Stamp the structured outcome label FIRST so the B0b reconciler classifies
    # the close deterministically (outcome.go classifyOutcomeLabel wins over the
    # free-text heuristic), then close. Both run in the rig dir so the bead
    # resolves to its owning Dolt.
    if (cd "$RIG_DIR" && bd update "$id" --add-label "curio-outcome:${EXPIRY_OUTCOME}" >/dev/null 2>&1) \
       && (cd "$RIG_DIR" && bd close "$id" --reason "$reason" >/dev/null 2>&1); then
      closed=$(( closed + 1 ))
      closed_list+="  ${id}"$'\n'
      log "expiry: closed ${id} (outcome=${EXPIRY_OUTCOME})"
    else
      failed=$(( failed + 1 ))
      log "expiry: FAILED to close ${id} (best-effort, continuing)"
    fi
  done <<<"$ids"

  (cd "$RIG_DIR" && bd sync >/dev/null 2>&1) || true

  if (( failed > 0 )); then
    record_receipt "failure" "expiry: ${closed} closed, ${failed} failed" \
      "Curio proposal expiry swept open proposal/hypothesis beads older than
${EXPIRY_DAYS}d in ${TARGET_RIG}.
Closed ${closed} with outcome=${EXPIRY_OUTCOME}; ${failed} failed to close
(best-effort — they will be retried next run).

Closed:
${closed_list}"
  else
    record_receipt "success" "expiry: ${closed} closed" \
      "Curio proposal expiry closed ${closed} open proposal/hypothesis bead(s)
older than ${EXPIRY_DAYS}d in ${TARGET_RIG}, each stamped
curio-outcome:${EXPIRY_OUTCOME}. The B0b reconciler feeds (outcome, resolved_at)
into curio_ledger for any ledger-tracked bead.

Closed:
${closed_list}"
  fi
  return 0
}

# =============================================================================
# PASS 2 — breaker-reset alert
# =============================================================================
#
# The volume breaker is "open" when open curio-proposal count >= ceiling (the
# same ceiling B5's dispatch enforces). We persist the continuous-open duration
# in a small state file and alert ONCE when it crosses M days. The `alerted`
# latch gives "exactly once per trip"; clearing the breaker resets it.

count_open_proposals() {
  [[ -d "$RIG_DIR" ]] || { echo 0; return 0; }
  local n
  n=$(cd "$RIG_DIR" && bd list --label curio-proposal --status open --json 2>/dev/null \
    | jq -r 'length' 2>/dev/null) || { echo 0; return 0; }
  [[ "$n" =~ ^[0-9]+$ ]] && echo "$n" || echo 0
}

# read_state KEY — echo the value of KEY from the breaker state file ("" if the
# file or key is absent/unparseable).
read_state() {
  [[ -f "$BREAKER_STATE" ]] || return 0
  jq -r --arg k "$1" '.[$k] // empty' "$BREAKER_STATE" 2>/dev/null || true
}

# write_state OPEN_SINCE_EPOCH ALERTED(true|false) — atomically rewrite the file.
write_state() {
  mkdir -p "$STATE_DIR" 2>/dev/null || true
  local tmp="${BREAKER_STATE}.tmp.$$"
  jq -nc --argjson since "$1" --argjson alerted "$2" \
    '{open_since: $since, alerted: $alerted}' >"$tmp" 2>/dev/null \
    && mv -f "$tmp" "$BREAKER_STATE" 2>/dev/null || { rm -f "$tmp" 2>/dev/null; return 1; }
}

clear_state() {
  rm -f "$BREAKER_STATE" 2>/dev/null || true
}

run_breaker_pass() {
  local count
  count=$(count_open_proposals)

  # --- breaker CLOSED: healthy lane, reset the trip clock + alert latch -------
  if (( count < PROPOSAL_CEILING )); then
    if [[ -f "$BREAKER_STATE" ]]; then
      log "breaker: closed (${count} < ${PROPOSAL_CEILING}) — clearing trip state"
      clear_state
    else
      log "breaker: closed (${count} < ${PROPOSAL_CEILING}) — healthy"
    fi
    record_receipt "success" "breaker: healthy (${count}/${PROPOSAL_CEILING})" \
      "Volume breaker is closed: ${count} open curio-proposal beads in
${TARGET_RIG}, below the ceiling of ${PROPOSAL_CEILING}. Trip clock reset; the
next trip re-arms the one-shot alert."
    return 0
  fi

  # --- breaker OPEN -----------------------------------------------------------
  local open_since alerted
  open_since=$(read_state open_since)
  alerted=$(read_state alerted)

  # First time we observe the breaker open this trip: stamp open_since=now.
  if [[ -z "$open_since" || ! "$open_since" =~ ^[0-9]+$ ]]; then
    open_since="$NOW_EPOCH"
    alerted="false"
    write_state "$open_since" false
    log "breaker: open (${count} >= ${PROPOSAL_CEILING}) — first observed, open_since now"
    record_receipt "success" "breaker: tripped (day 0)" \
      "Volume breaker just tripped: ${count} open curio-proposal beads in
${TARGET_RIG} (ceiling ${PROPOSAL_CEILING}). Tracking; will alert if it stays
open >= ${BREAKER_ALERT_DAYS}d."
    return 0
  fi

  [[ "$alerted" == "true" ]] || alerted="false"

  local open_days=$(( (NOW_EPOCH - open_since) / DAY_SECS ))

  # Already alerted this trip: latch holds, no spam.
  if [[ "$alerted" == "true" ]]; then
    log "breaker: open ${open_days}d (>= ${BREAKER_ALERT_DAYS}d) — already alerted this trip, no re-fire"
    record_receipt "skipped" "breaker: alert already fired" \
      "Volume breaker still open (${count} >= ${PROPOSAL_CEILING}) after
${open_days}d; the one-shot alert already fired this trip. No re-fire (exactly
once per trip)."
    return 0
  fi

  # Open long enough, not yet alerted: fire ONCE.
  if (( open_days >= BREAKER_ALERT_DAYS )); then
    log "breaker: open ${open_days}d >= ${BREAKER_ALERT_DAYS}d — firing one-shot alert"
    local esc_desc="The Curio Retrospect volume breaker has been OPEN for ${open_days} day(s): ${count} open curio-proposal beads in ${TARGET_RIG} meet or exceed the ceiling of ${PROPOSAL_CEILING}. While open, curio-retrospect-dispatch (B5) skips dispatch every night — the lane is silently wedged. Work the proposal backlog down (review/close open curio-proposal beads) or raise CURIO_PROPOSAL_CEILING if the ceiling is too low. Expiry (this plugin, pass 1) will age out untouched proposals after ${EXPIRY_DAYS}d, but a persistently-full backlog of fresh proposals needs human attention."
    gt escalate "Curio Retrospect lane wedged: volume breaker open ${open_days}d" \
      --severity low \
      --source "plugin:${PLUGIN_NAME}" \
      --signature "curio:volume-breaker:${TARGET_RIG}" \
      --dedup \
      --reason "$esc_desc" >/dev/null 2>&1 || \
      log "breaker: gt escalate failed (best-effort) — state still latched to avoid spam"
    write_state "$open_since" true
    record_receipt "success" "breaker: alerted (open ${open_days}d)" \
      "Volume breaker open ${open_days}d (>= ${BREAKER_ALERT_DAYS}d). Emitted ONE
low-severity escalation (signature curio:volume-breaker:${TARGET_RIG}) and
latched alerted=true so it fires exactly once per trip. ${count} open
curio-proposal beads >= ceiling ${PROPOSAL_CEILING}."
    return 0
  fi

  # Open but not yet long enough: keep tracking.
  log "breaker: open ${open_days}d (< ${BREAKER_ALERT_DAYS}d) — tracking, no alert yet"
  record_receipt "success" "breaker: tripped (day ${open_days})" \
    "Volume breaker open ${open_days}d (< ${BREAKER_ALERT_DAYS}d alert
threshold): ${count} open curio-proposal beads >= ceiling ${PROPOSAL_CEILING}.
Tracking; alert fires once it crosses ${BREAKER_ALERT_DAYS}d."
  return 0
}

# =============================================================================
# main — run both passes; each is best-effort and independent.
# =============================================================================

if ! command -v jq >/dev/null 2>&1; then
  log "ERROR: jq not on PATH — cannot parse bead JSON; skipping"
  record_receipt "failure" "jq missing" \
    "jq is not on PATH; ${PLUGIN_NAME} needs it to parse bead JSON and the
breaker state file. Nothing swept this run."
  exit 1
fi

run_expiry_pass
run_breaker_pass

log "done"
exit 0

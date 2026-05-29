#!/usr/bin/env bash
# wiki-patrol-dispatch/run.sh — Sling mol-casc-wiki-patrol to the dedicated
# casc_cdk/polecats/wiki-patrol polecat, once per cooldown window.
#
# This is the script implementation of plugins/wiki-patrol-dispatch/plugin.md.
# It exists so the plugin does not depend on LLM cooperation — the daemon
# dog just executes `bash run.sh` and records the result.
#
# Behavior:
#   1. Resolve the casc_cdk project_path (the Brazil package working tree).
#      This is what mol-casc-wiki-patrol's preflight step requires.
#   2. Defense-in-depth single-instance check: if any open bead is already
#      attached to mol-casc-wiki-patrol for casc_cdk/polecats/wiki-patrol,
#      skip — the formula's wiki-patrol polecat holds at most one wisp at a
#      time, but a stale dispatch should not double-queue.
#   3. Sling the formula to casc_cdk/polecats/wiki-patrol with --create
#      (auto-spawn the polecat on first run).
#   4. Record a plugin-run receipt with result:success|skipped|failure.
#
# Idempotency: the cooldown gate (23h) prevents same-day re-runs at the
# daemon level. The single-instance check prevents same-day double-dispatch
# if the cooldown gate is bypassed (e.g. --force). gt sling itself is
# idempotent for already-hooked beads, but we never want a *new* wisp-pair
# while an earlier one is still in flight.

set -uo pipefail
# NOTE: not `set -e` — failure paths should record receipts, not bail silently.

PLUGIN_NAME="wiki-patrol-dispatch"
FORMULA="mol-casc-wiki-patrol"
TARGET_RIG="casc_cdk"
TARGET_POLECAT="casc_cdk/polecats/wiki-patrol"

log() { echo "[${PLUGIN_NAME}] $*" >&2; }

record_receipt() {
  # $1=result (success|skipped|failure), $2=title-suffix, $3=description
  local result="$1" title="$2" desc="${3:-}"
  bd create "${PLUGIN_NAME}: ${title}" -t chore --ephemeral \
    -l "type:plugin-run,plugin:${PLUGIN_NAME},result:${result}" \
    -d "${desc}" --silent 2>/dev/null || true
}

# --- Resolve project_path ----------------------------------------------------
#
# mol-casc-wiki-patrol's preflight step requires `project_path` to point at
# the casc_cdk Brazil package working tree (the one containing
# scripts/wiki-patrol.sh and scripts/wiki-publisher.ts). The canonical
# location is the Brazil workspace under /workplace/<user>/CodegenAgentScheduler.
#
# We resolve in this order:
#   1. $WIKI_PATROL_PROJECT_PATH (operator override)
#   2. /workplace/<user>/CodegenAgentScheduler/src/CodegenAgentSchedulerCDK
#   3. Fallback: skip with a diagnostic — the formula cannot run without it.

resolve_project_path() {
  if [[ -n "${WIKI_PATROL_PROJECT_PATH:-}" ]]; then
    echo "$WIKI_PATROL_PROJECT_PATH"
    return 0
  fi
  local candidate="/workplace/${USER}/CodegenAgentScheduler/src/CodegenAgentSchedulerCDK"
  if [[ -d "$candidate" ]]; then
    echo "$candidate"
    return 0
  fi
  return 1
}

PROJECT_PATH=$(resolve_project_path) || PROJECT_PATH=""

if [[ -z "$PROJECT_PATH" ]]; then
  log "ERROR: could not resolve casc_cdk project_path"
  log "  set WIKI_PATROL_PROJECT_PATH or ensure /workplace/${USER}/CodegenAgentScheduler/src/CodegenAgentSchedulerCDK exists"
  record_receipt "failure" "project_path unresolved" \
    "wiki-patrol-dispatch could not resolve a project_path for mol-casc-wiki-patrol.
Set WIKI_PATROL_PROJECT_PATH or ensure the casc_cdk Brazil package working tree exists at:
  /workplace/\$USER/CodegenAgentScheduler/src/CodegenAgentSchedulerCDK

The formula's preflight step requires this path."
  exit 1
fi

log "resolved project_path: $PROJECT_PATH"

# --- Single-instance check ---------------------------------------------------
#
# Defense in depth. The wiki-patrol polecat is a single-slot resource so the
# scheduler should already prevent overlap, but a slow Dolt round-trip or a
# manual --force could double-dispatch. Look for any open bead attached to
# this formula whose assignee is the wiki-patrol polecat. If one exists, skip.
#
# We query the casc_cdk rig's bead store (formula-attached beads live in the
# rig where the formula was slung, not in gastown_upstream).

TOWN_ROOT="${GT_TOWN_ROOT:-$HOME/gt}"
CASC_DIR="${TOWN_ROOT}/${TARGET_RIG}"

if [[ ! -d "$CASC_DIR" ]]; then
  log "WARN: $CASC_DIR not found — skipping single-instance check (rig not present in this town)"
else
  # bd list filters by label/status; the formula attachment is in the bead
  # description (`attached_formula: mol-casc-wiki-patrol`), not a label, so
  # we list open beads assigned to the wiki-patrol polecat and grep their
  # descriptions. This is best-effort: if jq/grep fails, we proceed (the
  # formula's own scheduler will catch true overlap).
  open_json=$(cd "$CASC_DIR" && bd list --status open --status hooked --status in_progress --json 2>/dev/null || echo "[]")
  if [[ -n "$open_json" && "$open_json" != "[]" && "$open_json" != "null" ]]; then
    in_flight=$(jq -r --arg pcat "$TARGET_POLECAT" --arg formula "$FORMULA" '
      [
        .[]
        | select(.assignee == $pcat)
        | select((.description // "") | contains("attached_formula: " + $formula))
        | .id
      ]
      | .[]
    ' <<<"$open_json" 2>/dev/null || true)
    if [[ -n "$in_flight" ]]; then
      log "single-instance: ${FORMULA} already in flight for ${TARGET_POLECAT}, skipping"
      log "  in-flight beads: $(tr '\n' ' ' <<<"$in_flight")"
      record_receipt "skipped" "in-flight run detected" \
        "Single-instance guard: open beads already attached to ${FORMULA} for ${TARGET_POLECAT}.
Skipping this dispatch to avoid concurrent wiki writes (Phase 1 cadk-xk4: concurrent writes multiply 429s).

In-flight bead IDs:
$(printf '  %s\n' $in_flight)"
      exit 0
    fi
  fi
fi

# --- Sling the formula -------------------------------------------------------

log "slinging ${FORMULA} to ${TARGET_POLECAT} (project_path=$PROJECT_PATH)"

sling_out=$(gt sling "$FORMULA" "$TARGET_POLECAT" \
  --create \
  --var "project_path=$PROJECT_PATH" \
  2>&1) || {
  rc=$?
  log "ERROR: gt sling failed (exit $rc)"
  log "  output: $(head -n5 <<<"$sling_out" | tr '\n' ' ')"
  record_receipt "failure" "sling failed" \
    "gt sling ${FORMULA} ${TARGET_POLECAT} --create --var project_path=${PROJECT_PATH}
exit code: ${rc}

Output (first 30 lines):
$(head -n30 <<<"$sling_out")"
  exit 1
}

log "sling output:"
log "$sling_out"

# Extract the wisp/bead ID for the receipt, if present in the output.
slung_id=$(grep -oE '\b[a-z0-9]+-(wisp-)?[a-z0-9]+\b' <<<"$sling_out" | head -n1 || true)

record_receipt "success" "slung ${FORMULA}" \
  "Dispatched ${FORMULA} to ${TARGET_POLECAT}.
project_path: ${PROJECT_PATH}
slung_id: ${slung_id:-(unknown)}

Sling output:
${sling_out}"

log "done"
exit 0

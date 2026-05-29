#!/usr/bin/env bash
# wiki-patrol-dispatch/run.sh — Sling mol-casc-wiki-patrol into the casc_cdk
# rig once per cooldown window, letting `gt sling` auto-resolve a polecat.
#
# This is the script implementation of plugins/wiki-patrol-dispatch/plugin.md.
# It exists so the plugin does not depend on LLM cooperation — the daemon
# dog just executes `bash run.sh` and records the result.
#
# Behavior:
#   1. Resolve the casc_cdk project_path (the Brazil package working tree).
#      This is what mol-casc-wiki-patrol's preflight step requires.
#   2. Defense-in-depth single-instance check: if any open bead in casc_cdk
#      is already attached to mol-casc-wiki-patrol, skip — the formula's
#      single-molecule guarantee (cadk-xk4: concurrent writes multiply
#      429s) means we never want a *new* wisp-pair while an earlier one is
#      still in flight. We search across ALL casc_cdk polecats since the
#      rig auto-resolves which polecat picks up the work.
#   3. Sling the formula at the casc_cdk rig with --create (auto-spawn /
#      reuse a polecat).
#   4. Record a plugin-run receipt with result:success|skipped|failure.
#
# Why rig target (gu-fc8h): `gt sling <formula> <rig>` is the supported
# syntax under deferred dispatch (scheduler.max_polecats > 0). Targeting
# `<rig>/polecats/<name>` directly is rejected by the deferred sling path
# with "'<target>' is not a known rig". The dedicated `wiki-patrol`
# polecat identity (cadk-xvsz) is informational; per its findings the
# Midway scope-narrowing the identity was meant to enforce isn't
# implementable, so the polecat-name pin no longer adds isolation. The
# formula scheduler still enforces single-instance via the in-flight
# check below + the formula's own single-molecule semantics.
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
# Defense in depth. mol-casc-wiki-patrol must run as a single molecule (Phase
# 1 cadk-xk4: concurrent writes multiply 429s). The formula scheduler should
# already prevent overlap, but a slow Dolt round-trip or a manual --force
# could double-dispatch. Look for any open bead attached to this formula in
# the casc_cdk rig (across ALL polecats, since the rig target auto-resolves
# which polecat picks up the work). If one exists, skip.
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
    # Match any bead attached to this formula whose assignee is a polecat in
    # the casc_cdk rig. Without polecat-name pinning we can't filter by a
    # single assignee, so we use a prefix match on "<rig>/polecats/" and
    # let the formula's attached_formula metadata carry the discriminator.
    in_flight=$(jq -r --arg prefix "${TARGET_RIG}/polecats/" --arg formula "$FORMULA" '
      [
        .[]
        | select((.assignee // "") | startswith($prefix))
        | select((.description // "") | contains("attached_formula: " + $formula))
        | .id
      ]
      | .[]
    ' <<<"$open_json" 2>/dev/null || true)
    if [[ -n "$in_flight" ]]; then
      log "single-instance: ${FORMULA} already in flight in ${TARGET_RIG}, skipping"
      log "  in-flight beads: $(tr '\n' ' ' <<<"$in_flight")"
      record_receipt "skipped" "in-flight run detected" \
        "Single-instance guard: open beads already attached to ${FORMULA} in ${TARGET_RIG}.
Skipping this dispatch to avoid concurrent wiki writes (Phase 1 cadk-xk4: concurrent writes multiply 429s).

In-flight bead IDs:
$(printf '  %s\n' $in_flight)"
      exit 0
    fi
  fi
fi

# --- Sling the formula -------------------------------------------------------

log "slinging ${FORMULA} to ${TARGET_RIG} (project_path=$PROJECT_PATH)"

sling_out=$(gt sling "$FORMULA" "$TARGET_RIG" \
  --create \
  --var "project_path=$PROJECT_PATH" \
  2>&1) || {
  rc=$?
  log "ERROR: gt sling failed (exit $rc)"
  log "  output: $(head -n5 <<<"$sling_out" | tr '\n' ' ')"
  record_receipt "failure" "sling failed" \
    "gt sling ${FORMULA} ${TARGET_RIG} --create --var project_path=${PROJECT_PATH}
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
  "Dispatched ${FORMULA} to ${TARGET_RIG}.
project_path: ${PROJECT_PATH}
slung_id: ${slung_id:-(unknown)}

Sling output:
${sling_out}"

log "done"
exit 0

#!/usr/bin/env bash
# casc-webapp-patrol-dispatch/run.sh — Sling casc-webapp-patrol into casc_webapp
# once per cooldown window.
#
# This is the script implementation of plugins/casc-webapp-patrol-dispatch/plugin.md.
# The daemon dog executes `bash run.sh` and records the result.
#
# Behavior:
#   1. Resolve the casc_webapp project_path (the package working tree with
#      scripts/casc-webapp-patrol.sh).
#   2. Sling casc-webapp-patrol to casc_webapp with --var project_path and
#      --var target_url.
#   3. Record a receipt bead (result:success|failure).
#   4. Exit non-zero if the sling failed.
#
# Single target (not a per-stage loop like casc-patrol): this patrol observes
# one web app URL, so one sling.
#
# Sling syntax: the formula is the FIRST POSITIONAL arg, the target rig is the
# second: `gt sling <formula> <rig>`. The --formula FLAG is a separate feature
# (apply-on-bead, default mol-polecat-work); passing it here makes gt sling
# consume $FORMULA as the flag value and read $TARGET_RIG as the bead to sling,
# which fails "deferred dispatch requires a rig target" (gu-ono8h). run_test.sh
# asserts the positional invocation shape.

set -uo pipefail

PLUGIN_NAME="casc-webapp-patrol-dispatch"
FORMULA="casc-webapp-patrol"
TARGET_RIG="casc_webapp"
TARGET_URL="${CASC_WEBAPP_PATROL_TARGET_URL:-https://codegen-scheduler.beta.harmony.a2z.com/}"

log() { echo "[${PLUGIN_NAME}] $*" >&2; }

record_receipt() {
  # $1=result (success|failure), $2=title-suffix, $3=description
  local result="$1" title="$2" desc="${3:-}"
  bd create "${PLUGIN_NAME}: ${title}" -t chore --ephemeral \
    -l "type:plugin-run,plugin:${PLUGIN_NAME},result:${result}" \
    -d "${desc}" --silent 2>/dev/null || true
}

# --- Resolve project_path ----------------------------------------------------
#
# casc-webapp-patrol's preflight requires project_path to point at the
# casc_webapp package working tree (containing scripts/casc-webapp-patrol.sh).
#
# Resolution order:
#   1. $CASC_WEBAPP_PATROL_PROJECT_PATH (operator override)
#   2. $HOME/gt/casc_webapp/crew/$USER (the rig's crew working tree)
#   3. Fallback: fail with a diagnostic.

resolve_project_path() {
  if [[ -n "${CASC_WEBAPP_PATROL_PROJECT_PATH:-}" ]]; then
    echo "$CASC_WEBAPP_PATROL_PROJECT_PATH"; return 0
  fi
  local candidate="${HOME}/gt/casc_webapp/crew/${USER}"
  if [[ -f "${candidate}/scripts/casc-webapp-patrol.sh" ]]; then
    echo "$candidate"; return 0
  fi
  return 1
}

PROJECT_PATH=$(resolve_project_path) || PROJECT_PATH=""

if [[ -z "$PROJECT_PATH" ]]; then
  log "ERROR: could not resolve casc_webapp project_path"
  log "  set CASC_WEBAPP_PATROL_PROJECT_PATH or ensure scripts/casc-webapp-patrol.sh"
  log "  exists under \$HOME/gt/casc_webapp/crew/\$USER"
  record_receipt "failure" "project_path unresolved" \
    "casc-webapp-patrol-dispatch could not resolve a project_path for casc-webapp-patrol.
Set CASC_WEBAPP_PATROL_PROJECT_PATH or ensure the casc_webapp working tree exists at:
  \$HOME/gt/casc_webapp/crew/\$USER  (with scripts/casc-webapp-patrol.sh)

The formula's preflight step requires this path."
  exit 1
fi

log "resolved project_path: $PROJECT_PATH"
log "slinging ${FORMULA} to ${TARGET_RIG} (target_url=${TARGET_URL})"

# --- Sling -------------------------------------------------------------------
sling_out=$(gt sling "$FORMULA" "$TARGET_RIG" \
  --create \
  --var "project_path=$PROJECT_PATH" \
  --var "target_url=$TARGET_URL" \
  2>&1) || {
  rc=$?
  log "ERROR: gt sling failed (exit $rc)"
  log "  output: $(head -n5 <<<"$sling_out" | tr '\n' ' ')"
  record_receipt "failure" "sling failed" \
    "gt sling ${FORMULA} ${TARGET_RIG} --create --var project_path=${PROJECT_PATH} --var target_url=${TARGET_URL}
exit code: ${rc}

Output (first 30 lines):
$(head -n30 <<<"$sling_out")"
  exit 1
}

log "sling output:"
log "$sling_out"

slung_id=$(grep -oE '\b[a-z0-9]+-(wisp-)?[a-z0-9]+\b' <<<"$sling_out" | head -n1 || true)

record_receipt "success" "slung ${FORMULA}" \
  "Dispatched ${FORMULA} to ${TARGET_RIG}.
project_path: ${PROJECT_PATH}
target_url: ${TARGET_URL}
slung_id: ${slung_id:-(unknown)}

Sling output:
${sling_out}"

log "done (exit 0)"
exit 0

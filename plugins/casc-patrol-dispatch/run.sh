#!/usr/bin/env bash
# casc-patrol-dispatch/run.sh — Sling casc-patrol into the casc_cdk rig
# once per day (cron: 0 9 * * *), once per stage (Beta, Gamma, Prod).
#
# This is the script implementation of plugins/casc-patrol-dispatch/plugin.md.
# The daemon dog executes `bash run.sh` and records the result.
#
# Behavior:
#   1. Resolve the casc_cdk project_path (the Brazil package working tree).
#   2. For each stage in {Beta, Gamma, Prod}:
#        a. Sling casc-patrol to casc_cdk with --var stage=<S> --var project_path=<P>.
#        b. Record a per-stage receipt (result:success|failure).
#        c. Continue to the next stage regardless of this stage's outcome.
#   3. Exit non-zero if any stage failed; zero if all succeeded.
#
# Why no single-instance check (unlike wiki-patrol-dispatch):
# The patrol is read-only and per-stage AWS-profile-isolated. Concurrent
# runs of the same stage are fine; concurrent runs across stages target
# different AWS accounts/profiles and cannot collide. Wiki-patrol's
# 429-multiplication concern (cadk-xk4) does not apply here.
#
# Sling syntax: the formula is the FIRST POSITIONAL arg, the target rig is the
# second: `gt sling <formula> <rig>`. The --formula FLAG is a separate feature
# (apply-on-bead, default mol-polecat-work); passing it here makes gt sling
# consume $FORMULA as the flag value and read $TARGET_RIG as the bead to sling,
# which fails "deferred dispatch requires a rig target" (gu-ono8h).
# run_test.sh asserts the positional invocation shape.

set -uo pipefail
# NOTE: not `set -e` — failure paths should record receipts and continue.

PLUGIN_NAME="casc-patrol-dispatch"
FORMULA="casc-patrol"
TARGET_RIG="casc_cdk"
STAGES=("Beta" "Gamma" "Prod")

log() { echo "[${PLUGIN_NAME}] $*" >&2; }

record_receipt() {
  # $1=result (success|skipped|failure), $2=stage, $3=title-suffix, $4=description
  local result="$1" stage="$2" title="$3" desc="${4:-}"
  bd create "${PLUGIN_NAME}[${stage}]: ${title}" -t chore --ephemeral \
    -l "type:plugin-run,plugin:${PLUGIN_NAME},stage:${stage},result:${result}" \
    -d "${desc}" --silent 2>/dev/null || true
}

# --- Resolve project_path ----------------------------------------------------
#
# casc-patrol's preflight requires project_path to point at the casc_cdk
# Brazil package working tree (the one containing scripts/patrol.ts and
# lib/monitor/policy.ts). The canonical location is the Brazil workspace
# under /workplace/<user>/CodegenAgentScheduler.
#
# Resolution order:
#   1. $CASC_PATROL_PROJECT_PATH (operator override)
#   2. /workplace/<user>/CodegenAgentScheduler/src/CodegenAgentSchedulerCDK
#   3. Fallback: skip with a diagnostic — the formula cannot run without it.

resolve_project_path() {
  if [[ -n "${CASC_PATROL_PROJECT_PATH:-}" ]]; then
    echo "$CASC_PATROL_PROJECT_PATH"
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
  log "  set CASC_PATROL_PROJECT_PATH or ensure /workplace/${USER}/CodegenAgentScheduler/src/CodegenAgentSchedulerCDK exists"
  for stage in "${STAGES[@]}"; do
    record_receipt "failure" "$stage" "project_path unresolved" \
      "casc-patrol-dispatch could not resolve a project_path for casc-patrol (stage=${stage}).
Set CASC_PATROL_PROJECT_PATH or ensure the casc_cdk Brazil package working tree exists at:
  /workplace/\$USER/CodegenAgentScheduler/src/CodegenAgentSchedulerCDK

The formula's preflight step requires this path."
  done
  exit 1
fi

log "resolved project_path: $PROJECT_PATH"

# --- Per-stage sling loop ----------------------------------------------------

EXIT_CODE=0

for STAGE in "${STAGES[@]}"; do
  log "slinging ${FORMULA} to ${TARGET_RIG} (stage=${STAGE}, project_path=${PROJECT_PATH})"

  sling_out=$(gt sling "$FORMULA" "$TARGET_RIG" \
    --create \
    --var "stage=$STAGE" \
    --var "project_path=$PROJECT_PATH" \
    2>&1) || {
    rc=$?
    log "ERROR: gt sling failed for stage=${STAGE} (exit $rc)"
    log "  output: $(head -n5 <<<"$sling_out" | tr '\n' ' ')"
    record_receipt "failure" "$STAGE" "sling failed" \
      "gt sling ${FORMULA} ${TARGET_RIG} --create --var stage=${STAGE} --var project_path=${PROJECT_PATH}
exit code: ${rc}

Output (first 30 lines):
$(head -n30 <<<"$sling_out")"
    EXIT_CODE=1
    continue
  }

  log "sling output (stage=${STAGE}):"
  log "$sling_out"

  # Extract the wisp/bead ID for the receipt, if present in the output.
  slung_id=$(grep -oE '\b[a-z0-9]+-(wisp-)?[a-z0-9]+\b' <<<"$sling_out" | head -n1 || true)

  record_receipt "success" "$STAGE" "slung ${FORMULA}" \
    "Dispatched ${FORMULA} to ${TARGET_RIG} for stage=${STAGE}.
project_path: ${PROJECT_PATH}
slung_id: ${slung_id:-(unknown)}

Sling output:
${sling_out}"
done

log "done (exit ${EXIT_CODE})"
exit "$EXIT_CODE"

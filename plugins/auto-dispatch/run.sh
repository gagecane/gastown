#!/usr/bin/env bash
# auto-dispatch/run.sh — Auto-sling ready tasks to idle polecats across all rigs.
#
# This is the script implementation of plugins/auto-dispatch/plugin.md. It
# exists so the plugin does not depend on LLM cooperation: the in-patrol
# `plugin-run: SKIP` rule and the broader "deacon decides whether to run me"
# class of failures (gs-myq) do not apply to a script that the daemon just
# `bash`-executes.
#
# Behavior mirrors plugin.md:
#   1. Discover rigs from $GT_TOWN_ROOT/mayor/rigs.json.
#   2. For each rig, fetch `bd ready --json` and filter out non-dispatchable
#      beads (identity beads, epics, wisps, agent-owned, workflow-orchestrator
#      beads, etc.).
#   3. For each remaining bead (sorted P1 > P2 > P3 > P4), invoke
#      `gt sling <bead-id> <rig>`. The server-side guards in `gt sling`
#      enforce the same filters defensively; this script's filters are
#      best-effort and exist to avoid noisy errors.
#   4. Print a per-rig summary at the end.
#
# Idempotency: `gt sling` is idempotent — a re-sling of an already-scheduled
# bead returns cleanly. Calling this every cooldown cycle is safe.

set -uo pipefail
# NOTE: not `set -e` — a single rig's failure must not abort dispatch for
# the remaining rigs.

log() { echo "[auto-dispatch] $*" >&2; }

# --- Discover rigs -----------------------------------------------------------

TOWN_ROOT="${GT_TOWN_ROOT:-$HOME/gt}"
RIGS_JSON="${TOWN_ROOT}/mayor/rigs.json"

if [[ ! -f "$RIGS_JSON" ]]; then
  log "ERROR: rigs.json not found at $RIGS_JSON"
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  log "ERROR: jq is required"
  exit 1
fi

mapfile -t RIGS < <(jq -r '.rigs | keys[]' "$RIGS_JSON")

if [[ ${#RIGS[@]} -eq 0 ]]; then
  log "No rigs registered. Nothing to dispatch."
  exit 0
fi

# Known agent role keywords used to detect agent-owned beads. The owner field
# carries an address like "mayor", "deacon", "<rig>/witness", "<rig>/polecats/<name>".
# A human owner is typically an email or a free-form name with no slash.
is_agent_owner() {
  local owner="$1"
  [[ -z "$owner" ]] && return 1
  case "$owner" in
    mayor|mayor/*|deacon|deacon/*) return 0 ;;
  esac
  if [[ "$owner" == */witness || "$owner" == */witness/* ]]; then return 0; fi
  if [[ "$owner" == */refinery || "$owner" == */refinery/* ]]; then return 0; fi
  if [[ "$owner" == */polecats/* ]]; then return 0; fi
  if [[ "$owner" == */crew/* ]]; then return 0; fi
  if [[ "$owner" == */dogs/* ]]; then return 0; fi
  return 1
}

# is_polecat_address <s> — exact match for the canonical polecat address shape
# "<rig>/polecats/<name>" (3 non-empty segments, middle segment is the literal
# "polecats"). Mirrors isPolecatAddress in internal/cmd/sling_helpers.go.
# Used to detect both owner and created_by polecat self-attributions, since
# `bd create` from a polecat session leaves owner as the human's git email
# but stamps the polecat address into created_by via BD_ACTOR (gu-pxxs).
is_polecat_address() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  [[ -z "$s" ]] && return 1
  local seg1 seg2 seg3 seg4
  IFS='/' read -r seg1 seg2 seg3 seg4 <<<"$s"
  if [[ -n "$seg4" || -z "$seg1" || -z "$seg3" || "$seg2" != "polecats" ]]; then
    return 1
  fi
  return 0
}

# is_known_rig <name> — checks $RIGS array for membership.
is_known_rig() {
  local name="$1"
  local r
  for r in "${RIGS[@]}"; do
    [[ "$r" == "$name" ]] && return 0
  done
  return 1
}

# extract_workflow_target <description> — echoes the workflow_target value
# (trimmed) if present in the bead description, empty otherwise.
extract_workflow_target() {
  awk -F: '
    BEGIN { IGNORECASE = 1 }
    /^[[:space:]]*workflow_target[[:space:]]*:/ {
      sub(/^[^:]*:[[:space:]]*/, "")
      gsub(/[[:space:]]+$/, "")
      print
      exit
    }
  ' <<<"$1"
}

# --- Per-rig dispatch loop ---------------------------------------------------

total_slung=0
total_new=0
total_already=0
total_skipped=0
total_failed=0
declare -a RIG_REPORTS

for rig in "${RIGS[@]}"; do
  rig_dir="${TOWN_ROOT}/${rig}"
  if [[ ! -d "$rig_dir" ]]; then
    log "Skipping rig=$rig (no directory at $rig_dir)"
    RIG_REPORTS+=("$rig: skipped (no dir)")
    continue
  fi

  ready_json=$(cd "$rig_dir" && bd ready --json -n 200 2>/dev/null)
  if [[ -z "$ready_json" || "$ready_json" == "null" || "$ready_json" == "[]" ]]; then
    RIG_REPORTS+=("$rig: 0 ready")
    continue
  fi

  # Sort by priority (P1 first; treat missing priority as 99 so it sinks).
  # Output: tab-separated id<TAB>owner<TAB>created_by<TAB>description (description is the rest of the line).
  # We tolerate missing fields by defaulting to "". created_by is needed to
  # detect polecat-filed beads whose owner is a human email (gu-pxxs).
  bead_lines=$(jq -r '
    sort_by(.priority // 99)
    | .[]
    | [.id, (.owner // ""), (.created_by // ""), (.description // "" | gsub("\n"; ""))]
    | @tsv
  ' <<<"$ready_json") || {
    log "rig=$rig: failed to parse bd ready output"
    RIG_REPORTS+=("$rig: parse error")
    continue
  }

  rig_slung=0
  rig_new=0
  rig_already=0
  rig_skipped=0
  rig_failed=0

  while IFS=$'\t' read -r bead_id owner created_by description; do
    [[ -z "$bead_id" ]] && continue

    # Restore newlines that we tunneled through tsv.
    description="${description//$'\x01'/$'\n'}"

    # Client-side filter: agent-owned beads (orchestrator state — owning agent
    # handles them, not a polecat). See gs-myq.
    if is_agent_owner "$owner"; then
      rig_skipped=$((rig_skipped + 1))
      continue
    fi

    # Client-side filter: polecat-filed beads (self-creation contract violation).
    # Polecats execute work, they do not dispatch it. The original gu-gal8 fix
    # caught the owner axis; gu-pxxs added the created_by axis after four
    # polecat-filed beads (gu-grkl, gu-h1fn, gu-2s03, gu-id33) leaked through
    # with a human owner (the polecat’s git email) and a polecat created_by
    # (populated from BD_ACTOR by `bd create`). `gt sling` enforces the same
    # check server-side; this client-side skip just avoids the noisy error.
    if is_polecat_address "$owner" || is_polecat_address "$created_by"; then
      rig_skipped=$((rig_skipped + 1))
      continue
    fi

    # Client-side filter: workflow_target pointing at a non-rig target. These
    # are workflow-orchestrator beads that applyWorkflowStepTargetOverride
    # rewrites to a non-rig target, which the scheduler then rejects with
    # "'<target>' is not a known rig". Skip them up front. See gs-myq.
    target=$(extract_workflow_target "$description")
    if [[ -n "$target" && "$target" != "rig" ]] && ! is_known_rig "$target"; then
      rig_skipped=$((rig_skipped + 1))
      continue
    fi

    # Sling the bead. `gt sling` enforces all the other filters (epic, identity,
    # mayor-only, polecat-owned, sling-context, wisp, plugin-run, etc.)
    # server-side and is idempotent for already-scheduled beads.
    if sling_out=$(gt sling "$bead_id" "$rig" 2>&1); then
      rig_slung=$((rig_slung + 1))
      if grep -qiE "already (hooked|scheduled)" <<<"$sling_out"; then
        rig_already=$((rig_already + 1))
      else
        rig_new=$((rig_new + 1))
      fi
    else
      rig_failed=$((rig_failed + 1))
      # Surface the first line of the failure for observability; do NOT abort.
      first=$(head -n1 <<<"$sling_out")
      log "rig=$rig bead=$bead_id sling failed: $first"
    fi
  done <<<"$bead_lines"

  total_slung=$((total_slung + rig_slung))
  total_new=$((total_new + rig_new))
  total_already=$((total_already + rig_already))
  total_skipped=$((total_skipped + rig_skipped))
  total_failed=$((total_failed + rig_failed))

  RIG_REPORTS+=("$rig: slung=$rig_slung (new=$rig_new, already=$rig_already), skipped=$rig_skipped, failed=$rig_failed")
done

# --- Report ------------------------------------------------------------------

SUMMARY="auto-dispatch: slung $total_slung across ${#RIGS[@]} rigs ($total_new new, $total_already already-scheduled, $total_skipped client-skipped, $total_failed failed)"

log ""
log "=== Done ==="
log "$SUMMARY"
for r in "${RIG_REPORTS[@]}"; do
  log "  $r"
done

# Record a receipt for the cooldown gate / digest pipeline.
RESULT="success"
if [[ $total_failed -gt 0 && $total_slung -eq 0 ]]; then
  RESULT="failure"
fi

bd create "$SUMMARY" -t chore --ephemeral \
  -l "type:plugin-run,plugin:auto-dispatch,result:${RESULT}" \
  --silent 2>/dev/null || true

exit 0

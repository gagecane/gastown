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
#   1. Discover ready work via a single `gt ready --json` call. This resolves
#      each rig's beads dir via rig.BeadsPath()+redirect (NOT a CWD-based
#      `cd $rig && bd ready`, which silently fails for rigs whose Dolt DB
#      doesn't match the directory name — see gu-1ykb1: `cd gastown_upstream &&
#      bd ready` errored with `database "gc" not found`, was swallowed by
#      `2>/dev/null`, and dropped 19 ready beads from dispatch). `gt ready`
#      also pre-applies the identity/epic/wisp/formula-scaffold/route filters
#      in Go, so this script no longer re-implements them in bash.
#   2. Skip the `town` source: its hq-* convoy/cross-rig beads are not per-rig
#      dispatchable work and must not be fed to `gt sling <id> <rig>`.
#   3. For each rig source, filter out the remaining non-dispatchable beads
#      that `gt ready` does NOT cover: agent-assigned beads (with the
#      `*-wfs-*` workflow-step exception), `wrong-rig:<rig>`-labeled beads,
#      and workflow_target-to-non-rig beads.
#   4. For each remaining bead (sorted P1 > P2 > P3 > P4), invoke
#      `gt sling <bead-id> <rig>`. The server-side guards in `gt sling`
#      enforce the same filters defensively; this script's filters are
#      best-effort and exist to avoid noisy errors.
#   5. Print a per-rig summary at the end.
#
# Idempotency: `gt sling` is idempotent — a re-sling of an already-scheduled
# bead returns cleanly. Calling this on every manual dispatch run is safe.

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

# Known agent role keywords used to detect agent-assigned beads. The bead's
# assignee carries an address like "mayor", "deacon", "<rig>/witness",
# "<rig>/polecats/<name>". `gt ready --json` exposes this address in the
# `assignee` field (the `owner` field there is the human email and is not an
# address), so this heuristic is fed `.assignee`.
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

# is_workflow_step_bead <id> — true if the bead is a workflow step bead created
# by `gt formula run` (executeWorkflowFormula stamps them as `<rigPrefix>-wfs-<id>`).
# These are dispatchable polecat work even when owned by the launching agent.
is_workflow_step_bead() {
  [[ "$1" == *-wfs-* ]]
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

# --- Discover ready work (single `gt ready --json` call) ---------------------

# `gt ready --json` resolves each rig's beads dir via rig.BeadsPath()+redirect
# and pre-applies the identity/epic/wisp/formula-scaffold/route filters in Go.
# This replaces the old per-rig `(cd $rig && bd ready --json)` loop, whose
# CWD-based resolution silently failed for rigs whose Dolt DB name differs from
# the directory name (gu-1ykb1: `cd gastown_upstream && bd ready` errored with
# `database "gc" not found`, was swallowed by `2>/dev/null`, and dropped 19
# ready beads from dispatch).
READY_JSON=$(gt ready --json 2>/dev/null)
if [[ -z "$READY_JSON" || "$READY_JSON" == "null" ]]; then
  log "ERROR: gt ready --json returned no output"
  exit 1
fi

# Surface per-source errors that gt ready reported (e.g. a rig whose Dolt DB is
# down). These are no longer silently swallowed.
while IFS=$'\t' read -r err_src err_msg; do
  [[ -z "$err_src" ]] && continue
  log "source=$err_src ready error: $err_msg"
done < <(jq -r '.sources[] | select(.error != null and .error != "") | [.name, .error] | @tsv' <<<"$READY_JSON")

# --- Per-rig dispatch loop ---------------------------------------------------

total_slung=0
total_new=0
total_already=0
total_skipped=0
total_failed=0
declare -a RIG_REPORTS

# Iterate rig sources from gt ready, skipping the `town` source: its hq-*
# convoy/cross-rig beads are not per-rig dispatchable work and must not be fed
# to `gt sling <id> <rig>`.
mapfile -t READY_SOURCES < <(jq -r '.sources[] | select(.name != "town") | .name' <<<"$READY_JSON")

for rig in "${READY_SOURCES[@]}"; do
  [[ -z "$rig" ]] && continue

  rig_dir="${TOWN_ROOT}/${rig}"
  if [[ ! -d "$rig_dir" ]]; then
    log "Skipping rig=$rig (no directory at $rig_dir)"
    RIG_REPORTS+=("$rig: skipped (no dir)")
    continue
  fi

  # Extract this source's issues, sorted by priority (P1 first; treat missing
  # priority as 99 so it sinks).
  # Output: tab-separated id<TAB>assignee<TAB>labels<TAB>description.
  # `gt ready` exposes the agent address in `assignee` (not `owner`).
  # Labels are joined with commas and wrapped in commas (",foo,bar,") so substring
  # checks like ",wrong-rig:${rig}," cannot accidentally match a label prefix.
  # Newlines in the description are tunneled through \x01 so each bead stays on
  # one tsv line (restored below). We tolerate missing fields by defaulting to "".
  bead_lines=$(jq -r --arg rig "$rig" '
    ((.sources[] | select(.name == $rig) | .issues) // [])
    | sort_by(.priority // 99)
    | .[]
    | [.id, (.assignee // ""), ((.labels // []) | join(",")), (.description // "" | gsub("\n"; ""))]
    | @tsv
  ' <<<"$READY_JSON") || {
    log "rig=$rig: failed to parse gt ready output"
    RIG_REPORTS+=("$rig: parse error")
    continue
  }

  if [[ -z "$bead_lines" ]]; then
    RIG_REPORTS+=("$rig: 0 ready")
    continue
  fi

  rig_slung=0
  rig_new=0
  rig_already=0
  rig_skipped=0
  rig_failed=0

  while IFS=$'\t' read -r bead_id assignee labels description; do
    [[ -z "$bead_id" ]] && continue

    # Restore newlines that we tunneled through tsv.
    description="${description//$'\x01'/$'\n'}"

    # Client-side filter: agent-assigned beads (orchestrator state — owning agent
    # handles them, not a polecat). See gs-myq. `gt ready` carries the agent
    # address in `assignee`.
    #
    # Exception: workflow step beads (id matches `*-wfs-*`) are real polecat
    # work, not orchestrator state — `gt formula run` stamps them with the
    # OWNER of whoever launched the run, so a crew-launched workflow produces
    # step beads owned by `<rig>/crew/<name>`. Without this exception the
    # is_agent_owner filter silently drops them from the fast dispatch path,
    # leaving them to advance only on the Deacon's slow stranded-feed cycle
    # (gu-3y6ro). Steps that genuinely route to a specific agent are still
    # caught by the workflow_target filter below, and `gt sling`'s server-side
    # guards remain the backstop.
    if is_agent_owner "$assignee" && ! is_workflow_step_bead "$bead_id"; then
      rig_skipped=$((rig_skipped + 1))
      continue
    fi

    # Client-side filter: bead is labeled wrong-rig:<this-rig> — a polecat
    # in this rig (or an operator) has already asserted that the work does
    # not belong here. Re-routing would bounce the bead right back. See
    # gu-mhfs / cala-7e9 / cala-tl5 (auth-enforcement test mis-routed to
    # casc_lambda twice in 24h). The match is comma-bounded so a label like
    # "wrong-rig:foo_bar" cannot accidentally match rig "foo".
    if [[ ",${labels},"  == *",wrong-rig:${rig},"* ]]; then
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

# Record a receipt for the manual gate / digest pipeline.
RESULT="success"
if [[ $total_failed -gt 0 && $total_slung -eq 0 ]]; then
  RESULT="failure"
fi

bd create "$SUMMARY" -t chore --ephemeral \
  -l "type:plugin-run,plugin:auto-dispatch,result:${RESULT}" \
  --silent 2>/dev/null || true

exit 0

#!/usr/bin/env bash
# lia-codegen-digest/run.sh — Weekly codegen quality digest per lia rig.
#
# Read-only. Instruments nothing new: every metric is derived from data Gas
# Town already has (GitHub via `gh`, beads via `gt rig bd`). For each lia rig
# it computes four metrics over a 7-day window and records a non-ephemeral
# digest bead in that rig's beads db for weekly trend tracking.
#
# Metrics (see plugin.md for full definitions):
#   1. First-pass approval rate     — merged PRs with no CHANGES_REQUESTED / total
#   2. Review comments per PR        — median + max inline review comments
#   3. Gate failure rate             — beads with a build-check/gate-fail signal / total
#   4. PR time-to-merge              — median + max hours, open -> merge
#
# Requires: gh CLI authenticated. Degrades to a skip receipt otherwise.
#
# Usage: ./run.sh

set -uo pipefail
# NOTE: not `set -e` — a single rig's failure must not abort the others; each
# rig records its own receipt and the script continues.

PLUGIN_NAME="lia-codegen-digest"
WINDOW_DAYS="${DIGEST_WINDOW_DAYS:-7}"
MAX_PRS="${DIGEST_MAX_PRS:-100}"
RIG_GLOB="${LIA_RIG_GLOB:-lia*}"
TOWN_ROOT="${GT_TOWN_ROOT:-$HOME/gt}"
DRY_RUN="${DIGEST_DRY_RUN:-}"   # when set, compute + print but write no beads

log() { echo "[${PLUGIN_NAME}] $*" >&2; }

# SINCE in ISO date (UTC) for gh search; portable across GNU/BSD date.
SINCE=$(date -u -d "${WINDOW_DAYS} days ago" +%Y-%m-%d 2>/dev/null \
  || date -u -v-"${WINDOW_DAYS}"d +%Y-%m-%d)

# record_receipt RESULT SUMMARY  — ephemeral plugin-run receipt (town db).
record_receipt() {
  local result="$1" summary="$2"
  [ -n "$DRY_RUN" ] && { log "dry-run: skip receipt (${result}): ${summary}"; return 0; }
  bd create "${PLUGIN_NAME}: ${summary}" -t chore --ephemeral \
    -l "type:plugin-run,plugin:${PLUGIN_NAME},result:${result}" \
    -d "${summary}" --silent 2>/dev/null || true
}

# --- Step 0: require gh auth -------------------------------------------------

if ! gh auth status >/dev/null 2>&1; then
  log "SKIP: gh CLI not authenticated"
  record_receipt skipped "skipped — gh not authenticated"
  exit 0
fi

# --- Step 1: enumerate lia rigs ---------------------------------------------

RIG_JSON=$(gt rig list --json 2>/dev/null) || {
  log "SKIP: could not get rig list"
  record_receipt skipped "skipped — gt rig list failed"
  exit 0
}

# Filter rig names by glob (default lia*).
RIGS=()
while IFS= read -r name; do
  [ -z "$name" ] && continue
  # shellcheck disable=SC2053
  [[ "$name" == $RIG_GLOB ]] && RIGS+=("$name")
done < <(echo "$RIG_JSON" | jq -r '.[].name')

if [ ${#RIGS[@]} -eq 0 ]; then
  log "No rigs match glob '${RIG_GLOB}'"
  record_receipt success "no rigs match '${RIG_GLOB}'"
  exit 0
fi

log "Window: last ${WINDOW_DAYS}d (merged:>=${SINCE}); rigs: ${RIGS[*]}"

# jq helpers: median + max over a numeric array (null/empty -> null).
JQ_STATS='
def median: if length==0 then null
  elif length%2==1 then (sort)[(length-1)/2]
  else ((sort)[length/2-1] + (sort)[length/2]) / 2 end;
def maxn: if length==0 then null else max end;
{ median: (. | median), max: (. | maxn) }'

# round1 NUM -> one decimal, or "n/a" for null/empty.
round1() {
  if [ -z "${1:-}" ] || [ "$1" = "null" ]; then echo "n/a"; else
    printf '%.1f' "$1"; fi
}
pct() {  # pct NUMER DENOM -> "NN.N%" or "n/a"
  local n="$1" d="$2"
  if [ "$d" -eq 0 ] 2>/dev/null; then echo "n/a"; else
    awk -v n="$n" -v d="$d" 'BEGIN{printf "%.1f%%", (n/d)*100}'; fi
}

OVERALL_OK=0
OVERALL_FAIL=0

# --- Step 2: per-rig digest -------------------------------------------------

for RIG in "${RIGS[@]}"; do
  log ""
  log "=== ${RIG} ==="

  CONFIG="${TOWN_ROOT}/${RIG}/config.json"
  if [ ! -f "$CONFIG" ]; then
    log "  skip: no config.json at ${CONFIG}"
    continue
  fi

  GIT_URL=$(jq -r '.git_url // empty' "$CONFIG" 2>/dev/null)
  if [ -z "$GIT_URL" ]; then
    log "  skip: ${RIG} has no git_url in config.json"
    continue
  fi
  # Parse owner/repo from ssh (git@github.com:o/r.git) or https URL.
  REPO=$(echo "$GIT_URL" | sed -E 's|.*github\.com[:/]||; s|\.git$||')
  if [ -z "$REPO" ]; then
    log "  skip: could not parse repo from ${GIT_URL}"
    continue
  fi
  log "  repo: ${REPO}"

  # --- Fetch merged PRs in the window -------------------------------------
  PRS=$(gh pr list --repo "$REPO" --state merged \
    --search "merged:>=${SINCE}" \
    --json number,createdAt,mergedAt,reviews \
    --limit "$MAX_PRS" 2>/dev/null) || PRS="[]"

  TOTAL=$(echo "$PRS" | jq 'length')
  if [ "${TOTAL:-0}" -eq 0 ]; then
    log "  no merged PRs in window"
  fi
  TRUNCATED=""
  [ "${TOTAL:-0}" -ge "$MAX_PRS" ] && TRUNCATED=" (capped at ${MAX_PRS})"

  # --- Metric 1: first-pass approval rate ---------------------------------
  # First-pass = no CHANGES_REQUESTED review on the PR.
  FIRST_PASS=$(echo "$PRS" | jq '[.[] | select(
    ([.reviews[].state] | index("CHANGES_REQUESTED")) == null)] | length')
  M1=$(pct "${FIRST_PASS:-0}" "${TOTAL:-0}")

  # --- Metric 4: time-to-merge (hours) ------------------------------------
  TTM=$(echo "$PRS" | jq "[.[] | ((.mergedAt|fromdate) - (.createdAt|fromdate)) / 3600] | ${JQ_STATS}")
  M4_MED=$(round1 "$(echo "$TTM" | jq -r '.median')")
  M4_MAX=$(round1 "$(echo "$TTM" | jq -r '.max')")

  # --- Metric 2: review comments per PR -----------------------------------
  # One API call per PR (bounded by MAX_PRS). Inline review comments only.
  COMMENT_COUNTS="[]"
  while IFS= read -r N; do
    [ -z "$N" ] && continue
    C=$(gh api "repos/${REPO}/pulls/${N}/comments" --jq 'length' 2>/dev/null) || C=0
    COMMENT_COUNTS=$(echo "$COMMENT_COUNTS" | jq --argjson c "${C:-0}" '. + [$c]')
  done < <(echo "$PRS" | jq -r '.[].number')
  RC=$(echo "$COMMENT_COUNTS" | jq "${JQ_STATS}")
  M2_MED=$(round1 "$(echo "$RC" | jq -r '.median')")
  M2_MAX=$(round1 "$(echo "$RC" | jq -r '.max')")

  # --- Metric 3: gate failure rate (beads) --------------------------------
  # Best-effort: work beads in the window whose label/description carries a
  # build-check / gate-failure signal, over total non-ephemeral work beads.
  BEADS=$(gt rig bd "$RIG" list --json --all --created-after="$SINCE" \
    --limit 1000 2>/dev/null) || BEADS="[]"
  # Drop ephemeral plugin-run/receipt noise from the denominator.
  WORK_BEADS=$(echo "$BEADS" | jq '[.[] | select(
    ([.labels[]?] | index("type:plugin-run")) == null)]' 2>/dev/null || echo "[]")
  BEAD_TOTAL=$(echo "$WORK_BEADS" | jq 'length' 2>/dev/null || echo 0)
  GATE_FAILS=$(echo "$WORK_BEADS" | jq '[.[] | select(
    ((.labels // []) | join(" ") | test("gate.?fail|build.?check"; "i")) or
    ((.title // "") | test("MERGE REJECTION|build-check|gate fail"; "i")) or
    ((.description // "") | test("MERGE REJECTION|build-check fail|gate fail"; "i"))
  )] | length' 2>/dev/null || echo 0)
  M3=$(pct "${GATE_FAILS:-0}" "${BEAD_TOTAL:-0}")

  # --- Render digest -------------------------------------------------------
  DIGEST=$(cat <<EOF
# Codegen Quality Digest — ${RIG} — ${SINCE}..now (${WINDOW_DAYS}d)

Repo: \`${REPO}\` · Merged PRs: ${TOTAL}${TRUNCATED}

| # | Metric | Value |
|---|--------|-------|
| 1 | First-pass approval rate (no change-request cycle) | ${M1} (${FIRST_PASS}/${TOTAL}) |
| 2 | Review comments per PR (median / max) | ${M2_MED} / ${M2_MAX} |
| 3 | Gate failure rate (build-check/gate fail beads) | ${M3} (${GATE_FAILS}/${BEAD_TOTAL}) |
| 4 | PR time-to-merge hours (median / max) | ${M4_MED} / ${M4_MAX} |

_Metric 4 (time-to-merge) is the chosen fourth metric — see plugin.md._
EOF
)

  echo "$DIGEST"
  echo

  # --- Record non-ephemeral digest bead in the rig's db -------------------
  SUMMARY="${RIG}: first-pass ${M1}, review-comments ${M2_MED}/${M2_MAX}, gate-fail ${M3}, ttm ${M4_MED}/${M4_MAX}h (${TOTAL} PRs)"
  if [ -n "$DRY_RUN" ]; then
    OVERALL_OK=$((OVERALL_OK + 1))
    log "  dry-run: skip digest bead"
  elif gt rig bd "$RIG" create "${PLUGIN_NAME}: ${SINCE}..now" -t chore \
      -l "type:digest,plugin:${PLUGIN_NAME},rig:${RIG},category:quality" \
      -d "$DIGEST" --silent >/dev/null 2>&1; then
    OVERALL_OK=$((OVERALL_OK + 1))
    log "  recorded digest bead"
  else
    OVERALL_FAIL=$((OVERALL_FAIL + 1))
    log "  WARNING: failed to record digest bead for ${RIG}"
  fi
  log "  ${SUMMARY}"
done

# --- Step 3: overall receipt ------------------------------------------------

if [ "$OVERALL_FAIL" -gt 0 ]; then
  record_receipt failure "${OVERALL_OK} digest(s) recorded, ${OVERALL_FAIL} failed"
  log "Done (with ${OVERALL_FAIL} failure(s))."
  exit 1
fi

record_receipt success "${OVERALL_OK} lia rig digest(s) recorded"
log "Done."

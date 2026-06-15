#!/usr/bin/env bash
# pr-watcher-poll/run.sh — Poll GitHub PRs per rig, turn unresolved reviewer
# comments into Gas Town work.
#
# This is the script implementation of plugins/pr-watcher-poll/plugin.md. It
# wires the internal/prwatcher/ package + `gt pr-watcher poll` CLI to a periodic
# plugin so it actually runs.
#
# Behavior:
#   1. Discover rigs from $GT_TOWN_ROOT/mayor/rigs.json.
#   2. Filter to rigs whose git_url is on github.com (the only host
#      `gt pr-watcher poll` knows how to query — it shells out to `gh`).
#   3. For each such rig, run `gt pr-watcher poll --rig <rig>` with per-rig
#      failure isolation: a single rig's poll failure does NOT abort the rest.
#   4. Print a per-rig summary and record a plugin-run receipt for the cooldown
#      gate / digest pipeline.
#
# Idempotency: `gt pr-watcher poll` records actioned comments in
# <townRoot>/.runtime/pr-watcher-actioned-<rig> and skips them on subsequent
# invocations. Calling this every cooldown cycle is safe.

set -uo pipefail
# NOTE: not `set -e` — a single rig's failure must not abort polling for
# the remaining rigs.

log() { echo "[pr-watcher-poll] $*" >&2; }

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

# Emit "<rig>\t<git_url>" per line so we can filter to GitHub-hosted rigs.
mapfile -t RIG_LINES < <(jq -r '.rigs | to_entries[] | "\(.key)\t\(.value.git_url // "")"' "$RIGS_JSON")

if [[ ${#RIG_LINES[@]} -eq 0 ]]; then
  log "No rigs registered. Nothing to poll."
  exit 0
fi

# --- Per-rig poll loop -------------------------------------------------------

total_polled=0
total_skipped=0
total_failed=0
total_mechanical=0
total_gated=0
declare -a RIG_REPORTS

for line in "${RIG_LINES[@]}"; do
  rig="${line%%$'\t'*}"
  url="${line#*$'\t'}"

  # Filter: only GitHub-hosted rigs. pr-watcher uses `gh`.
  case "$url" in
    *github.com*) ;;
    *)
      total_skipped=$((total_skipped + 1))
      RIG_REPORTS+=("$rig: skipped (non-github remote)")
      continue
      ;;
  esac

  rig_dir="${TOWN_ROOT}/${rig}"
  if [[ ! -d "$rig_dir" ]]; then
    total_skipped=$((total_skipped + 1))
    RIG_REPORTS+=("$rig: skipped (no dir at $rig_dir)")
    continue
  fi

  # Run the poll. Capture JSON for the summary.
  poll_out=$(gt pr-watcher poll --rig "$rig" --json 2>&1) && poll_rc=0 || poll_rc=$?

  if [[ "$poll_rc" -ne 0 ]]; then
    total_failed=$((total_failed + 1))
    first=$(head -n1 <<<"$poll_out")
    log "rig=$rig poll failed (rc=$poll_rc): $first"
    RIG_REPORTS+=("$rig: failed ($first)")
    continue
  fi

  # A rig with no pollable PRs (repo missing / access denied) returns rc=0 with
  # skipped=true. Report it as a skip, not a successful poll.
  skipped=$(jq -r '.skipped // false' <<<"$poll_out" 2>/dev/null || echo "false")
  if [[ "$skipped" == "true" ]]; then
    total_skipped=$((total_skipped + 1))
    skipreason=$(jq -r '.skip_reason // "no pollable PRs"' <<<"$poll_out" 2>/dev/null || echo "no pollable PRs")
    RIG_REPORTS+=("$rig: skipped ($skipreason)")
    continue
  fi

  total_polled=$((total_polled + 1))

  considered=$(jq -r '.comments_considered // 0' <<<"$poll_out" 2>/dev/null || echo "?")
  actioned=$(jq -r '.comments_actioned // 0' <<<"$poll_out" 2>/dev/null || echo "?")
  mechanical=$(jq -r '.mechanical // 0' <<<"$poll_out" 2>/dev/null || echo "?")
  gated=$(jq -r '.gated // 0' <<<"$poll_out" 2>/dev/null || echo "?")
  coldsupp=$(jq -r '.cold_start_suppressed // 0' <<<"$poll_out" 2>/dev/null || echo "?")

  [[ "$mechanical" =~ ^[0-9]+$ ]] && total_mechanical=$((total_mechanical + mechanical))
  [[ "$gated" =~ ^[0-9]+$ ]] && total_gated=$((total_gated + gated))

  RIG_REPORTS+=("$rig: considered=$considered actioned=$actioned mechanical=$mechanical gated=$gated cold_start_suppressed=$coldsupp")
done

# --- Report ------------------------------------------------------------------

SUMMARY="pr-watcher-poll: polled=$total_polled skipped=$total_skipped failed=$total_failed mechanical=$total_mechanical gated=$total_gated"

log ""
log "=== Done ==="
log "$SUMMARY"
for r in "${RIG_REPORTS[@]}"; do
  log "  $r"
done

# A mechanical/gated dispatch is the correct response, not a plugin failure —
# record success. Mark failure only if every poll attempt failed.
RESULT="success"
if [[ $total_polled -eq 0 && $total_failed -gt 0 ]]; then
  RESULT="failure"
fi

bd create "$SUMMARY" -t chore --ephemeral \
  -l "type:plugin-run,plugin:pr-watcher-poll,result:${RESULT}" \
  --silent 2>/dev/null || true

exit 0

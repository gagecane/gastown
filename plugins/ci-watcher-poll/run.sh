#!/usr/bin/env bash
# ci-watcher-poll/run.sh — Poll GitHub Actions per rig, reopen broke-main
# beads, freeze MQ on failure.
#
# This is the script implementation of plugins/ci-watcher-poll/plugin.md. It
# wires the dormant `internal/ciwatcher/` package + `gt ci-watcher poll` CLI
# (shipped in gu-xuzc / bc950fea) to a periodic plugin so it actually runs.
#
# Behavior:
#   1. Discover rigs from $GT_TOWN_ROOT/mayor/rigs.json.
#   2. Filter to rigs whose git_url is on github.com (the only host
#      `gt ci-watcher poll` knows how to query — it shells out to `gh`).
#   3. For each such rig, run `gt ci-watcher poll --rig <rig>` with per-rig
#      failure isolation: a single rig's poll failure does NOT abort the rest.
#   4. Print a per-rig summary and record a plugin-run receipt for the
#      cooldown gate / digest pipeline.
#
# Idempotency: `gt ci-watcher poll` records processed runs in
# <townRoot>/.runtime/ci-watcher-seen-<rig> and skips them on subsequent
# invocations. Calling this every cooldown cycle is safe.

set -uo pipefail
# NOTE: not `set -e` — a single rig's failure must not abort polling for
# the remaining rigs.

log() { echo "[ci-watcher-poll] $*" >&2; }

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
# gt ci-watcher poll uses the `gh` CLI; non-GitHub remotes (Amazon git,
# file://, etc.) cannot be polled and would just produce noisy errors.
mapfile -t RIG_LINES < <(jq -r '.rigs | to_entries[] | "\(.key)\t\(.value.git_url // "")"' "$RIGS_JSON")

if [[ ${#RIG_LINES[@]} -eq 0 ]]; then
  log "No rigs registered. Nothing to poll."
  exit 0
fi

# --- Per-rig poll loop -------------------------------------------------------

total_polled=0
total_skipped=0
total_failed=0
total_freeze_written=0
total_freeze_cleared=0
declare -a RIG_REPORTS

for line in "${RIG_LINES[@]}"; do
  rig="${line%%$'\t'*}"
  url="${line#*$'\t'}"

  # Filter: only GitHub-hosted rigs. ci-watcher uses `gh` and would fail on
  # Amazon-internal git, file:// remotes, etc.
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

  # Run the poll. Capture JSON so we can extract freeze_written /
  # freeze_cleared for the summary. --json prints a single object.
  poll_out=$(gt ci-watcher poll --rig "$rig" --json 2>&1) && poll_rc=0 || poll_rc=$?

  if [[ "$poll_rc" -ne 0 ]]; then
    total_failed=$((total_failed + 1))
    first=$(head -n1 <<<"$poll_out")
    log "rig=$rig poll failed (rc=$poll_rc): $first"
    RIG_REPORTS+=("$rig: failed ($first)")
    continue
  fi

  total_polled=$((total_polled + 1))

  # Best-effort field extraction from the JSON summary. If parse fails, fall
  # back to a generic "ok" report rather than treating it as a failure — the
  # poll itself succeeded.
  considered=$(jq -r '.runs_considered // 0' <<<"$poll_out" 2>/dev/null || echo "?")
  processed=$(jq -r '.runs_processed // 0' <<<"$poll_out" 2>/dev/null || echo "?")
  failures=$(jq -r '.failures_handled // 0' <<<"$poll_out" 2>/dev/null || echo "?")
  fwritten=$(jq -r '.freeze_written // false' <<<"$poll_out" 2>/dev/null || echo "?")
  fcleared=$(jq -r '.freeze_cleared // false' <<<"$poll_out" 2>/dev/null || echo "?")

  [[ "$fwritten" == "true" ]] && total_freeze_written=$((total_freeze_written + 1))
  [[ "$fcleared" == "true" ]] && total_freeze_cleared=$((total_freeze_cleared + 1))

  RIG_REPORTS+=("$rig: considered=$considered processed=$processed failures=$failures freeze_written=$fwritten freeze_cleared=$fcleared")
done

# --- Report ------------------------------------------------------------------

SUMMARY="ci-watcher-poll: polled=$total_polled skipped=$total_skipped failed=$total_failed freeze_written=$total_freeze_written freeze_cleared=$total_freeze_cleared"

log ""
log "=== Done ==="
log "$SUMMARY"
for r in "${RIG_REPORTS[@]}"; do
  log "  $r"
done

# Determine result for the receipt. A freeze_written event is operationally
# significant but is the *correct* response to a failed CI run, not a plugin
# failure — record as success. We only mark failure if every poll attempt
# failed (and we polled at least one rig).
RESULT="success"
if [[ $total_polled -eq 0 && $total_failed -gt 0 ]]; then
  RESULT="failure"
fi

bd create "$SUMMARY" -t chore --ephemeral \
  -l "type:plugin-run,plugin:ci-watcher-poll,result:${RESULT}" \
  --silent 2>/dev/null || true

exit 0

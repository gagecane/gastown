#!/usr/bin/env bash
# github-sheriff/run.sh — Monitor GitHub CI checks on open PRs across ALL
# GitHub-backed rigs and file ci-failure beads for failing required checks.
#
# Previously this plugin inspected only the gt SOURCE repo (resolved from the
# `gastown` git remote), so a failing CI check on a customer-repo PR
# (lia-health-backend / -web / -iac) was never turned into a fix-CI bead — a
# monitoring blind spot across all lia rigs (gs-xtyg). It now iterates every
# rig with a github.com remote (the same per-rig pattern ci-watcher-poll and
# pr-watcher-poll use via $GT_TOWN_ROOT/mayor/rigs.json) and files ci-failure
# beads routed to the owning rig.
#
# Behavior:
#   1. Discover rigs from $GT_TOWN_ROOT/mayor/rigs.json.
#   2. Filter to rigs whose git_url is on github.com (the only host `gh` can
#      query). Resolve the repo (owner/name) from the rig's git_url.
#   3. For each such rig, list open NON-DRAFT PRs and collect failing required
#      checks from statusCheckRollup. Draft PRs are excluded — they are not yet
#      asking for review/merge.
#   4. File ci-failure beads routed to that rig (bd is run from townRoot/<rig>,
#      mirroring the ciwatcher BeadsAdapter, so prefix routing reaches the
#      rig's database). Dedup against open ci-failure beads already in the rig.
#   5. Per-rig failure isolation: one rig's failure does NOT abort the rest.
#      The 2m execution budget covers a fast `gh pr list` per rig plus bd calls.
#
# require_review parking posture: this plugin only FILES work beads — it never
# merges or slings the PR. A PR that is parked awaiting human review stays
# parked; the ci-failure bead is the standing work item to fix the red check,
# after which the PR resumes its normal review path. Filing a bead does not
# disturb the parking posture.
#
# Idempotency: dedup by bead title against open ci-failure beads in each rig,
# so re-invoking every cooldown cycle is safe.
#
# Requires: gh CLI installed and authenticated, jq.

set -uo pipefail
# NOTE: not `set -e` — a single rig's failure must not abort the rest.

log() { echo "[github-sheriff] $*" >&2; }

# --- Step 0: Detect gh CLI and authenticate ---------------------------------

if ! gh auth status >/dev/null 2>&1; then
  log "SKIP: gh CLI not authenticated"
  bd create "github-sheriff: skipped (no auth)" -t chore --ephemeral \
    -l type:plugin-run,plugin:github-sheriff,result:skipped \
    -d "gh CLI not authenticated" --silent 2>/dev/null || true
  exit 0
fi

if ! command -v jq >/dev/null 2>&1; then
  log "ERROR: jq is required"
  bd create "github-sheriff: FAILED (no jq)" -t chore --ephemeral \
    -l type:plugin-run,plugin:github-sheriff,result:failure \
    -d "jq is required but not installed" --silent 2>/dev/null || true
  exit 1
fi

# --- Discover rigs ----------------------------------------------------------

TOWN_ROOT="${GT_TOWN_ROOT:-$HOME/gt}"
RIGS_JSON="${TOWN_ROOT}/mayor/rigs.json"

if [[ ! -f "$RIGS_JSON" ]]; then
  log "ERROR: rigs.json not found at $RIGS_JSON"
  bd create "github-sheriff: FAILED (no rigs.json)" -t chore --ephemeral \
    -l type:plugin-run,plugin:github-sheriff,result:failure \
    -d "rigs.json not found at $RIGS_JSON" --silent 2>/dev/null || true
  exit 1
fi

# Emit "<rig>\t<git_url>" per line so we can filter to GitHub-hosted rigs.
mapfile -t RIG_LINES < <(jq -r '.rigs | to_entries[] | "\(.key)\t\(.value.git_url // "")"' "$RIGS_JSON")

if [[ ${#RIG_LINES[@]} -eq 0 ]]; then
  log "No rigs registered. Nothing to poll."
  exit 0
fi

# --- Per-rig poll -----------------------------------------------------------

# process_rig polls one rig's repo and files ci-failure beads. Echoes a single
# summary line to stdout; logs detail to stderr. Returns 0 on success.
process_rig() {
  local rig="$1" repo="$2" rig_dir="$3"

  local prs pr_count
  prs=$(gh pr list --repo "$repo" --state open \
    --json number,title,author,isDraft,statusCheckRollup,url \
    --limit 100 2>/dev/null || echo "[]")
  pr_count=$(echo "$prs" | jq 'length')

  if [[ "$pr_count" -eq 0 ]]; then
    echo "$repo: 0 PRs, 0 failure(s), 0 bead(s), 0 already tracked"
    return 0
  fi

  # Collect failing required checks across non-draft PRs.
  # Required-check handling: keep a check when it is failing AND
  # (isRequired == true OR isRequired is absent/null). This honors required
  # status when gh exposes it and falls back to all failing checks otherwise,
  # so a genuinely red PR is never silently ignored.
  local failures
  mapfile -t failures < <(echo "$prs" | jq -r '
    .[] | select(.isDraft != true) as $pr
    | $pr.statusCheckRollup[]?
    | select(
        (.conclusion == "FAILURE" or .conclusion == "CANCELLED" or
         .conclusion == "TIMED_OUT" or .state == "FAILURE" or .state == "ERROR")
        and (.isRequired == true or (has("isRequired") | not) or .isRequired == null)
      )
    | "\($pr.number)|\($pr.title)|\(.name // .context // "check")|\(.detailsUrl // .targetUrl // "")"
  ')

  local failure_count=${#failures[@]}
  local created=0 skipped=0

  if [[ "$failure_count" -gt 0 ]]; then
    local existing
    existing=$( (cd "$rig_dir" && bd list --label ci-failure --status open --json) 2>/dev/null || echo "[]")

    local f pr_num pr_title check_name check_url bead_title description bead_id
    for f in "${failures[@]}"; do
      IFS='|' read -r pr_num pr_title check_name check_url <<< "$f"
      bead_title="CI failure: $check_name on PR #$pr_num"

      if echo "$existing" | jq -e --arg t "$bead_title" '.[] | select(.title == $t)' >/dev/null 2>&1; then
        skipped=$((skipped + 1))
        continue
      fi

      description="CI check \`$check_name\` failed on PR #$pr_num ($pr_title)

Repo: $repo
PR: https://github.com/$repo/pull/$pr_num"
      [[ -n "$check_url" ]] && description="$description
Check: $check_url"

      bead_id=$( (cd "$rig_dir" && bd create "$bead_title" -t task -p 2 \
        -d "$description" -l ci-failure --json) 2>/dev/null | jq -r '.id // empty')

      if [[ -n "$bead_id" ]]; then
        created=$((created + 1))
        log "rig=$rig created $bead_id for: $check_name on PR #$pr_num"
        gt activity emit github_check_failed \
          --message "CI check $check_name failed on PR #$pr_num ($repo), bead $bead_id" \
          2>/dev/null || true
      fi
    done
  fi

  echo "$repo: $pr_count PRs, $failure_count failure(s), $created bead(s), $skipped already tracked"
  return 0
}

total_polled=0
total_skipped=0
total_failed=0
total_created=0
declare -a RIG_REPORTS

for line in "${RIG_LINES[@]}"; do
  rig="${line%%$'\t'*}"
  url="${line#*$'\t'}"

  # Filter: only GitHub-hosted rigs. gh cannot query other hosts.
  case "$url" in
    *github.com*) ;;
    *)
      total_skipped=$((total_skipped + 1))
      RIG_REPORTS+=("$rig: skipped (non-github remote)")
      continue
      ;;
  esac

  repo=$(echo "$url" | sed -E 's|.*github\.com[:/]||; s|\.git$||')
  if [[ -z "$repo" ]]; then
    total_skipped=$((total_skipped + 1))
    RIG_REPORTS+=("$rig: skipped (could not resolve repo from $url)")
    continue
  fi

  rig_dir="${TOWN_ROOT}/${rig}"
  if [[ ! -d "$rig_dir" ]]; then
    total_skipped=$((total_skipped + 1))
    RIG_REPORTS+=("$rig: skipped (no dir at $rig_dir)")
    continue
  fi

  if report=$(process_rig "$rig" "$repo" "$rig_dir"); then
    total_polled=$((total_polled + 1))
    created=$(echo "$report" | sed -E 's|.* ([0-9]+) bead\(s\).*|\1|')
    [[ "$created" =~ ^[0-9]+$ ]] && total_created=$((total_created + created))
    RIG_REPORTS+=("$report")
  else
    total_failed=$((total_failed + 1))
    RIG_REPORTS+=("$rig: poll failed")
  fi
done

# --- Report -----------------------------------------------------------------

SUMMARY="github-sheriff: polled=$total_polled skipped=$total_skipped failed=$total_failed beads_created=$total_created"

log ""
log "=== Done ==="
log "$SUMMARY"
for r in "${RIG_REPORTS[@]}"; do
  log "  $r"
done

RESULT="success"
if [[ $total_polled -eq 0 && $total_failed -gt 0 ]]; then
  RESULT="failure"
fi

bd create "$SUMMARY" -t chore --ephemeral \
  -l "type:plugin-run,plugin:github-sheriff,result:${RESULT}" \
  -d "$SUMMARY" --silent 2>/dev/null || true

exit 0

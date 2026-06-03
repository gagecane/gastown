#!/usr/bin/env bash
# github-sheriff/run.sh — Monitor GitHub CI checks on open PRs and create beads for failures.
#
# Polls GitHub for open pull requests, categorizes them by readiness (easy wins vs needs review),
# and creates ci-failure beads for new CI check failures.
#
# Requires: gh CLI installed and authenticated
#
# Usage: ./run.sh

set -euo pipefail

# --- Configuration -----------------------------------------------------------

REPO_ROOT="${GT_RIG_ROOT:-.}"
SKIP_REASON=""

# --- Helpers -----------------------------------------------------------------

log() {
  echo "[github-sheriff] $*"
}

# --- Step 0: Detect gh CLI and authenticate --------------------------------

if ! gh auth status 2>/dev/null; then
  SKIP_REASON="gh CLI not authenticated"
  log "SKIP: $SKIP_REASON"
  bd create "github-sheriff: skipped (no auth)" -t chore --ephemeral \
    -l type:plugin-run,plugin:github-sheriff,result:skipped \
    -d "$SKIP_REASON" --silent 2>/dev/null || true
  exit 0
fi

# --- Step 1: Detect repository from git remote ----------------------------

# For Gas Town monorepo setups, the git remote is typically at the parent level
# Try multiple locations: rig, parent, or origin (fallback)
REPO=""

# Try direct parent first (most common in monorepo)
REPO=$(git -C "$REPO_ROOT/.." remote get-url gastown 2>/dev/null \
  | sed -E 's|.*github\.com[:/]||; s|\.git$||') || REPO=""

# Try current rig if parent didn't work
if [ -z "$REPO" ]; then
  REPO=$(git -C "$REPO_ROOT" remote get-url gastown 2>/dev/null \
    | sed -E 's|.*github\.com[:/]||; s|\.git$||') || REPO=""
fi

# Try origin as last resort (single-repo setups)
if [ -z "$REPO" ]; then
  REPO=$(git -C "$REPO_ROOT/.." remote get-url origin 2>/dev/null \
    | sed -E 's|.*github\.com[:/]||; s|\.git$||') || REPO=""
fi

if [ -z "$REPO" ]; then
  SKIP_REASON="could not detect GitHub repo from git remotes"
  log "SKIP: $SKIP_REASON"
  bd create "github-sheriff: skipped (no repo)" -t chore --ephemeral \
    -l type:plugin-run,plugin:github-sheriff,result:skipped \
    -d "$SKIP_REASON" --silent 2>/dev/null || true
  exit 0
fi

log "Monitoring PRs for $REPO"

# --- Step 2: Fetch open PRs with full details ----------------------------

PRS=$(gh pr list --repo "$REPO" --state open \
  --json number,title,author,additions,deletions,mergeable,statusCheckRollup,url \
  --limit 100 2>/dev/null || echo "[]")

PR_COUNT=$(echo "$PRS" | jq length)
if [ "$PR_COUNT" -eq 0 ]; then
  log "No open PRs found for $REPO"
  bd create "github-sheriff: $REPO (0 PRs)" -t chore --ephemeral \
    -l type:plugin-run,plugin:github-sheriff,result:success \
    -d "No open PRs found" --silent 2>/dev/null || true
  exit 0
fi

log "Found $PR_COUNT open PR(s)"

# --- Step 3: Categorize each PR and collect failures ----------------------

EASY_WINS=()
NEEDS_REVIEW=()
FAILURES=()

while IFS= read -r PR_JSON; do
  [ -z "$PR_JSON" ] && continue

  PR_NUM=$(echo "$PR_JSON" | jq -r '.number')
  PR_TITLE=$(echo "$PR_JSON" | jq -r '.title')
  AUTHOR=$(echo "$PR_JSON" | jq -r '.author.login')
  ADDITIONS=$(echo "$PR_JSON" | jq -r '.additions // 0')
  DELETIONS=$(echo "$PR_JSON" | jq -r '.deletions // 0')
  MERGEABLE=$(echo "$PR_JSON" | jq -r '.mergeable')
  TOTAL_CHANGES=$((ADDITIONS + DELETIONS))

  # Determine CI status from statusCheckRollup
  TOTAL_CHECKS=$(echo "$PR_JSON" | jq '.statusCheckRollup | length')
  PASSING_CHECKS=$(echo "$PR_JSON" | jq '[.statusCheckRollup[] | select(
    .conclusion == "SUCCESS" or .conclusion == "NEUTRAL" or
    .conclusion == "SKIPPED" or .state == "SUCCESS"
  )] | length')

  if [ "$TOTAL_CHECKS" -gt 0 ] && [ "$TOTAL_CHECKS" -eq "$PASSING_CHECKS" ]; then
    CI_PASS=true
  else
    CI_PASS=false
  fi

  # Collect individual check failures for bead creation
  while IFS= read -r CHECK; do
    [ -z "$CHECK" ] && continue
    CHECK_NAME=$(echo "$CHECK" | jq -r '.name')
    CHECK_URL=$(echo "$CHECK" | jq -r '.detailsUrl // .targetUrl // empty')
    FAILURES+=("$PR_NUM|$PR_TITLE|$CHECK_NAME|$CHECK_URL")
  done < <(echo "$PR_JSON" | jq -c '.statusCheckRollup[] | select(
    .conclusion == "FAILURE" or .conclusion == "CANCELLED" or
    .conclusion == "TIMED_OUT" or .state == "FAILURE" or .state == "ERROR"
  )')

  # Categorize PR
  if [ "$MERGEABLE" = "MERGEABLE" ] && [ "$CI_PASS" = true ] && [ "$TOTAL_CHANGES" -lt 200 ]; then
    EASY_WINS+=("PR #$PR_NUM: $PR_TITLE (by $AUTHOR, +$ADDITIONS/-$DELETIONS)")
  else
    REASONS=""
    [ "$MERGEABLE" != "MERGEABLE" ] && REASONS+="conflicts "
    [ "$CI_PASS" != true ] && REASONS+="ci-failing "
    [ "$TOTAL_CHANGES" -ge 200 ] && REASONS+="large(${TOTAL_CHANGES}loc) "
    NEEDS_REVIEW+=("PR #$PR_NUM: $PR_TITLE (by $AUTHOR, ${REASONS% })")
  fi
done < <(echo "$PRS" | jq -c '.[]')

# Report categorized PRs
if [ ${#EASY_WINS[@]} -gt 0 ]; then
  log "Easy wins (${#EASY_WINS[@]}):"
  printf '[github-sheriff]   %s\n' "${EASY_WINS[@]}"
fi
if [ ${#NEEDS_REVIEW[@]} -gt 0 ]; then
  log "Needs review (${#NEEDS_REVIEW[@]}):"
  printf '[github-sheriff]   %s\n' "${NEEDS_REVIEW[@]}"
fi

# --- Step 4: Deduplicate CI failures and create beads --------------------

CREATED=0
SKIPPED=0

# Only create CI failure beads for repos we own — skip upstream noise
REPO_OWNER=$(echo "$REPO" | cut -d'/' -f1)
if [ "$REPO_OWNER" != "athosmartins" ]; then
  log "Skipping CI failure beads for upstream repo $REPO (not athosmartins)"
  SKIPPED=${#FAILURES[@]}
else
  EXISTING=$(bd list --label ci-failure --status open --json 2>/dev/null || echo "[]")

  for F in "${FAILURES[@]}"; do
    IFS='|' read -r PR_NUM PR_TITLE CHECK_NAME CHECK_URL <<< "$F"
    BEAD_TITLE="CI failure: $CHECK_NAME on PR #$PR_NUM"

    # Check for duplicate (use jq --arg for safe string comparison)
    if echo "$EXISTING" | jq -e --arg t "$BEAD_TITLE" '.[] | select(.title == $t)' > /dev/null 2>&1; then
      SKIPPED=$((SKIPPED + 1))
      continue
    fi

    DESCRIPTION="CI check \`$CHECK_NAME\` failed on PR #$PR_NUM ($PR_TITLE)

PR: https://github.com/$REPO/pull/$PR_NUM"
    [ -n "$CHECK_URL" ] && DESCRIPTION="$DESCRIPTION
Check: $CHECK_URL"

    BEAD_ID=$(bd create "$BEAD_TITLE" -t task -p 2 \
      -d "$DESCRIPTION" \
      -l ci-failure \
      --json 2>/dev/null | jq -r '.id // empty')

    if [ -n "$BEAD_ID" ]; then
      CREATED=$((CREATED + 1))
      log "Created bead $BEAD_ID for check failure: $CHECK_NAME"

      gt activity emit github_check_failed \
        --message "CI check $CHECK_NAME failed on PR #$PR_NUM ($REPO), bead $BEAD_ID" \
        2>/dev/null || true
    fi
  done
fi

# --- Step 5: Record result --------------------------------------------------

SUMMARY="$REPO: $PR_COUNT PRs — ${#EASY_WINS[@]} easy win(s), ${#NEEDS_REVIEW[@]} need review, ${#FAILURES[@]} failure(s), $CREATED bead(s) created, $SKIPPED already tracked"
log "$SUMMARY"

bd create "github-sheriff: $SUMMARY" -t chore --ephemeral \
  -l type:plugin-run,plugin:github-sheriff,result:success \
  -d "$SUMMARY" --silent 2>/dev/null || true

log "Done."

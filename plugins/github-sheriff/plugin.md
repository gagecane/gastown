+++
name = "github-sheriff"
description = "Monitor GitHub CI checks on open PRs across all rigs and file ci-failure beads"
version = 2

[gate]
type = "cooldown"
duration = "2h"

[tracking]
labels = ["plugin:github-sheriff", "category:ci-monitoring"]
digest = true

[execution]
type = "script"
timeout = "2m"
notify_on_failure = true
severity = "low"
+++

# GitHub Sheriff

Monitors GitHub CI checks on open pull requests **across every GitHub-backed
rig** and files `ci-failure` beads for failing required checks, routed to the
owning rig. Implements the PR Sheriff pattern from the
[Gas Town User Manual](https://steve-yegge.medium.com/gas-town-emergency-user-manual-cf0e4556d74b)
as a Deacon plugin.

Requires: `gh` CLI installed and authenticated (`gh auth status`), plus `jq`.

## Why per-rig (the blind spot it closes)

This plugin used to resolve a single repo from the `gastown` git remote — the
gt SOURCE repo only. A failing CI check on a customer-repo PR
(lia-health-backend / -web / -iac) therefore never produced a fix-CI bead: red
PR CI sat unnoticed unless a human caught it (gs-xtyg). It now iterates every
rig with a `github.com` remote — the same per-rig pattern `ci-watcher-poll` and
`pr-watcher-poll` use via `$GT_TOWN_ROOT/mayor/rigs.json`.

The sibling watchers cover adjacent gaps: `ci-watcher-poll` watches
main/post-merge Actions, `pr-watcher-poll` actions review *comments*. This
watcher covers failing CI *checks* on open PRs.

## Implementation

This plugin is `execution.type = "script"` — the daemon runs `run.sh` directly
(it ships a `run.sh`, so the dispatcher executes it verbatim rather than
interpreting this markdown). The script:

1. Discovers rigs from `$GT_TOWN_ROOT/mayor/rigs.json`.
2. Filters to rigs whose `git_url` contains `github.com` (the only host `gh`
   can query) and resolves each repo (`owner/name`) from its `git_url`.
3. For each such rig, lists open **non-draft** PRs and collects failing
   required checks from `statusCheckRollup`. Draft PRs are excluded — they are
   not yet asking for review/merge. A check is treated as required when
   `isRequired` is true or absent (so a genuinely red PR is never silently
   dropped when GitHub omits the flag).
4. Files `ci-failure` beads routed to the owning rig (`bd` runs from
   `townRoot/<rig>`, so prefix routing reaches the rig's database),
   deduplicated by title against the rig's existing open `ci-failure` beads.
5. Isolates per-rig failures: one rig's poll failure does not abort the rest.

## require_review parking posture

The plugin only **files** work beads — it never merges or slings the PR. A PR
parked awaiting human review stays parked; the `ci-failure` bead is the standing
work item to fix the red check, after which the PR resumes its normal review
path. Filing a bead does not disturb the parking posture.

## Result

<<<<<<< HEAD
`run.sh` records its own `type:plugin-run` receipt with the per-rig outcome
(`polled` / `skipped` / `failed` / `beads_created`) for the cooldown gate and
digest pipeline.
=======
Fetch all open PRs in a single GraphQL call via `gh`. This returns additions,
deletions, mergeable status, and CI check results without per-PR API overhead:

```bash
SINCE=$(date -d '7 days ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -v-7d +%Y-%m-%dT%H:%M:%SZ)
PRS=$(gh pr list --repo "$REPO" --state open \
  --json number,title,author,additions,deletions,mergeable,statusCheckRollup,url,updatedAt \
  --limit 100 | jq --arg since "$SINCE" '[.[] | select(.updatedAt >= $since)]')

PR_COUNT=$(echo "$PRS" | jq length)
if [ "$PR_COUNT" -eq 0 ]; then
  echo "No open PRs found for $REPO"
  exit 0
fi
```

### Step 2: Categorize each PR

Process each PR using process substitution (not a pipe) so array modifications
persist after the loop:

```bash
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
  echo "Easy wins (${#EASY_WINS[@]}):"
  printf '  %s\n' "${EASY_WINS[@]}"
fi
if [ ${#NEEDS_REVIEW[@]} -gt 0 ]; then
  echo "Needs review (${#NEEDS_REVIEW[@]}):"
  printf '  %s\n' "${NEEDS_REVIEW[@]}"
fi
```

## Record Result

```bash
SUMMARY="$REPO: $PR_COUNT PRs — ${#EASY_WINS[@]} easy win(s), ${#NEEDS_REVIEW[@]} need review, ${#FAILURES[@]} CI failure(s) detected"
echo "$SUMMARY"
```

On success:
```bash
gt plugin record-run --plugin github-sheriff --result success \
  --title "github-sheriff: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
```

On failure:
```bash
gt plugin record-run --plugin github-sheriff --result failure \
  --title "github-sheriff: FAILED" \
  --description "GitHub sheriff failed: $ERROR" >/dev/null 2>&1 || true

gt escalate "Plugin FAILED: github-sheriff" \
  --severity low \
  --reason "$ERROR"
```
>>>>>>> upstream/main

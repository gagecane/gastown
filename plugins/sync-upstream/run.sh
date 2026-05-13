#!/usr/bin/env bash
# sync-upstream/run.sh — Rebase each rig's gagecane/gt onto origin/main
# during quiescent windows. See plugin.md for safety rails.

set -euo pipefail

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
PLUGIN_DIR="${TOWN_ROOT}/plugins/sync-upstream"
RIGS_JSON="${TOWN_ROOT}/mayor/rigs.json"
INTEGRATION_BRANCH="gagecane/gt"
UPSTREAM_BRANCH="main"

log() { echo "[sync-upstream] $*"; }

# Sentinel: if this file exists, skip the run entirely. Use to stage the
# plugin without triggering work while iterating on the script.
if [ -f "$PLUGIN_DIR/.disabled" ]; then
  log "sentinel $PLUGIN_DIR/.disabled present, skipping run"
  bd create "sync-upstream: disabled by sentinel" -t chore --ephemeral \
    -l "type:plugin-run,plugin:sync-upstream,result:skipped" \
    -d "Sentinel file .disabled present in plugin dir; remove to enable." --silent 2>/dev/null || true
  exit 0
fi

record_receipt() {
  # $1=result (success|skipped|failure), $2=title-suffix, $3=description
  local result="$1" title="$2" desc="${3:-}"
  bd create "sync-upstream: $title" -t chore --ephemeral \
    -l "type:plugin-run,plugin:sync-upstream,result:$result" \
    -d "$desc" --silent 2>/dev/null || true
}

if [ ! -f "$RIGS_JSON" ]; then
  log "rigs.json not found at $RIGS_JSON, skipping"
  exit 0
fi

# Discover rigs from rigs.json
RIGS=$(python3 -c "import json; print('\n'.join(json.load(open('$RIGS_JSON'))['rigs'].keys()))" 2>/dev/null) || {
  log "Failed to parse $RIGS_JSON"
  exit 1
}

OVERALL_RESULT="success"
SUMMARY_LINES=()

for RIG in $RIGS; do
  CREW_DIR="${TOWN_ROOT}/${RIG}/crew/gagecane"
  RIG_PREFIX=$(python3 -c "import json; print(json.load(open('$RIGS_JSON'))['rigs']['$RIG']['beads']['prefix'])" 2>/dev/null || echo "")

  log "=== ${RIG} ==="

  # Guard 1: crew checkout exists
  if [ ! -d "$CREW_DIR/.git" ]; then
    log "  no crew/gagecane checkout, skipping"
    SUMMARY_LINES+=("$RIG: skipped (no crew checkout)")
    continue
  fi

  # Guard 2: not parked/docked or explicitly disabled
  if [ -n "$RIG_PREFIX" ]; then
    RIG_BEAD_ID="${RIG_PREFIX}-rig-${RIG}"
    PARKED=$(bd show "$RIG_BEAD_ID" --json 2>/dev/null | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin); b=d[0] if isinstance(d,list) else d
    labels=b.get('labels') or []
    print('yes' if any(l in ('rig:parked','rig:docked','sync-upstream:disabled') for l in labels) else 'no')
except Exception:
    print('no')
" 2>/dev/null || echo "no")
    if [ "$PARKED" = "yes" ]; then
      log "  rig is parked/docked or sync-upstream disabled, skipping"
      SUMMARY_LINES+=("$RIG: skipped (parked/disabled)")
      continue
    fi
  fi

  # Guard 3: working tree clean
  DIRTY=$(git -C "$CREW_DIR" status --porcelain 2>/dev/null)
  if [ -n "$DIRTY" ]; then
    log "  crew working tree is dirty, skipping"
    SUMMARY_LINES+=("$RIG: skipped (dirty worktree)")
    continue
  fi

  # Guard 4: on gagecane/gt
  CUR_BRANCH=$(git -C "$CREW_DIR" branch --show-current 2>/dev/null || echo "")
  if [ "$CUR_BRANCH" != "$INTEGRATION_BRANCH" ]; then
    log "  not on $INTEGRATION_BRANCH (on $CUR_BRANCH), skipping"
    SUMMARY_LINES+=("$RIG: skipped (on $CUR_BRANCH)")
    continue
  fi

  # Guard 5: merge queue empty
  QUEUE_COUNT=$(gt refinery queue "$RIG" 2>/dev/null | grep -cE "^\s*[0-9]+\." || echo 0)
  if [ "$QUEUE_COUNT" -gt 0 ]; then
    log "  merge queue not empty ($QUEUE_COUNT pending), skipping"
    SUMMARY_LINES+=("$RIG: skipped (merge queue: $QUEUE_COUNT)")
    continue
  fi

  # Guard 6: no polecat with hook_bead or active_mr
  POLECAT_BUSY=$(bd list --json 2>/dev/null | python3 -c "
import json,sys
try:
    beads=json.load(sys.stdin)
    for b in beads:
        if 'polecat-' in b.get('id','') and '$RIG' in b.get('id',''):
            desc=b.get('description','')
            if 'hook_bead: null' not in desc or 'active_mr: null' not in desc:
                print('yes'); break
    else:
        print('no')
except Exception:
    print('no')
" 2>/dev/null || echo "no")
  if [ "$POLECAT_BUSY" = "yes" ]; then
    log "  polecat has hook_bead or active_mr, skipping"
    SUMMARY_LINES+=("$RIG: skipped (polecat in-flight)")
    continue
  fi

  # Fetch — use explicit destination refspecs because crew clones often
  # have a narrow default refspec that only fetches gagecane/gt. Without
  # explicit refs, origin/main wouldn't exist locally.
  log "  fetching origin/${UPSTREAM_BRANCH} and origin/${INTEGRATION_BRANCH}"
  if ! git -C "$CREW_DIR" fetch origin \
    "+refs/heads/${UPSTREAM_BRANCH}:refs/remotes/origin/${UPSTREAM_BRANCH}" \
    "+refs/heads/${INTEGRATION_BRANCH}:refs/remotes/origin/${INTEGRATION_BRANCH}" 2>&1 | tail -3; then
    log "  fetch failed, skipping"
    SUMMARY_LINES+=("$RIG: skipped (fetch failed)")
    continue
  fi

  # Sanity: both refs must exist locally now
  if ! git -C "$CREW_DIR" rev-parse --verify "origin/${UPSTREAM_BRANCH}" >/dev/null 2>&1; then
    log "  origin/${UPSTREAM_BRANCH} ref missing after fetch (unexpected), skipping"
    SUMMARY_LINES+=("$RIG: skipped (no origin/$UPSTREAM_BRANCH ref)")
    continue
  fi
  if ! git -C "$CREW_DIR" rev-parse --verify "origin/${INTEGRATION_BRANCH}" >/dev/null 2>&1; then
    log "  origin/${INTEGRATION_BRANCH} ref missing after fetch, skipping"
    SUMMARY_LINES+=("$RIG: skipped (no origin/$INTEGRATION_BRANCH ref)")
    continue
  fi

  # Guard 7: check ancestry
  if git -C "$CREW_DIR" merge-base --is-ancestor "origin/$UPSTREAM_BRANCH" "origin/$INTEGRATION_BRANCH" 2>/dev/null; then
    log "  origin/$UPSTREAM_BRANCH is already ancestor of origin/$INTEGRATION_BRANCH, no sync needed"
    SUMMARY_LINES+=("$RIG: clean (already up to date)")
    continue
  fi

  if git -C "$CREW_DIR" merge-base --is-ancestor "origin/$INTEGRATION_BRANCH" "origin/$UPSTREAM_BRANCH" 2>/dev/null; then
    # gagecane/gt is strict ancestor of main — fast-forward (no merge commit needed)
    log "  origin/$INTEGRATION_BRANCH is ancestor of origin/$UPSTREAM_BRANCH; fast-forwarding"
    OLD_SHA=$(git -C "$CREW_DIR" rev-parse "origin/$INTEGRATION_BRANCH")
    NEW_SHA=$(git -C "$CREW_DIR" rev-parse "origin/$UPSTREAM_BRANCH")
    git -C "$CREW_DIR" reset --hard "origin/$UPSTREAM_BRANCH" >/dev/null 2>&1 || {
      log "  reset failed, skipping"
      SUMMARY_LINES+=("$RIG: skipped (reset failed)")
      continue
    }
    if git -C "$CREW_DIR" push --force-with-lease="$INTEGRATION_BRANCH:$OLD_SHA" origin "HEAD:$INTEGRATION_BRANCH" 2>&1 | tail -3; then
      log "  fast-forwarded $OLD_SHA -> $NEW_SHA"
      SUMMARY_LINES+=("$RIG: fast-forwarded (${OLD_SHA:0:8} -> ${NEW_SHA:0:8})")
    else
      log "  push failed (race?), skipping"
      SUMMARY_LINES+=("$RIG: failure (push race)")
      OVERALL_RESULT="failure"
    fi
    continue
  fi

  # Diverged — merge needed
  log "  diverged. attempting merge of origin/$UPSTREAM_BRANCH into $INTEGRATION_BRANCH"
  OLD_SHA=$(git -C "$CREW_DIR" rev-parse "origin/$INTEGRATION_BRANCH")

  # Make sure local gagecane/gt matches origin
  git -C "$CREW_DIR" reset --hard "origin/$INTEGRATION_BRANCH" >/dev/null 2>&1 || true

  # Run merge, capture output. Don't abort yet — we need to capture the
  # conflict file list BEFORE aborting (post-abort the index is clean).
  MERGE_OUT=$(git -C "$CREW_DIR" merge --no-edit "origin/$UPSTREAM_BRANCH" 2>&1) && MERGE_RC=0 || MERGE_RC=$?

  if [ "$MERGE_RC" = 0 ]; then
    NEW_SHA=$(git -C "$CREW_DIR" rev-parse HEAD)
    log "  merge clean. pushing"
    if git -C "$CREW_DIR" push origin "HEAD:$INTEGRATION_BRANCH" 2>&1 | tail -3; then
      log "  merged ($OLD_SHA -> $NEW_SHA)"
      SUMMARY_LINES+=("$RIG: merged (${OLD_SHA:0:8} -> ${NEW_SHA:0:8})")
    else
      log "  push failed (concurrent push?). leaving local merged state alone."
      SUMMARY_LINES+=("$RIG: failure (push race after merge)")
      OVERALL_RESULT="failure"
    fi
  else
    # Capture conflicts BEFORE abort. diff-filter=U = unmerged paths.
    CONFLICTS=$(git -C "$CREW_DIR" diff --name-only --diff-filter=U 2>/dev/null | head -20)
    if [ -z "$CONFLICTS" ]; then
      # Fallback: parse merge output for CONFLICT or error: lines
      CONFLICTS=$(echo "$MERGE_OUT" | grep -E "^CONFLICT|^error:" | head -20)
    fi
    log "  merge conflicted. files:"
    echo "$CONFLICTS" | sed 's/^/    /'
    git -C "$CREW_DIR" merge --abort >/dev/null 2>&1 || true
    SUMMARY_LINES+=("$RIG: conflict (escalated)")
    OVERALL_RESULT="failure"
    gt escalate "sync-upstream: merge conflict in $RIG" -s medium \
      --reason "Merging origin/$UPSTREAM_BRANCH into $INTEGRATION_BRANCH conflicted on:
$CONFLICTS

Manual intervention needed:
  cd $CREW_DIR
  git fetch origin $UPSTREAM_BRANCH:refs/remotes/origin/$UPSTREAM_BRANCH
  git merge origin/$UPSTREAM_BRANCH" 2>/dev/null || true
  fi
done

# Final receipt
RIG_COUNT=$(echo "$RIGS" | wc -w | tr -d ' ')
SUMMARY=$(printf '%s\n' "${SUMMARY_LINES[@]}")
record_receipt "$OVERALL_RESULT" "$OVERALL_RESULT across $RIG_COUNT rigs" "$SUMMARY"

log "Done."
log "$SUMMARY"

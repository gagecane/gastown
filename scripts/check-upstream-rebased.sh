#!/usr/bin/env bash
# Fails if upstream/main is not an ancestor of HEAD.
# Enforces that this fork stays rebased on its upstream.
set -euo pipefail

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
UPSTREAM_BRANCH="${UPSTREAM_BRANCH:-main}"
UPSTREAM_URL="https://github.com/gastownhall/gastown.git"

# Origin tracking refs used to detect the *fork-wide* divergence condition
# (origin/main itself behind upstream/main), as opposed to a single feature
# branch that simply hasn't rebased yet. Only the fork-wide condition warrants
# an auto-filed tracking bead. Override-able for tests / non-standard remotes.
ORIGIN_REMOTE="${ORIGIN_REMOTE:-origin}"
ORIGIN_BRANCH="${ORIGIN_BRANCH:-main}"

# Auto-file a deduped [fork-sync] tracking bead when the fork-wide gate goes
# red (gu-sf0vo). Best-effort and opt-out: set GT_FORKSYNC_AUTOFILE=0 to skip
# (used by tests and any context that must not touch the beads DB).
GT_FORKSYNC_AUTOFILE="${GT_FORKSYNC_AUTOFILE:-1}"

# Background heartbeat keepalive: a slow `git fetch` over the network can
# stall this gate well past the witness/dog stale threshold (3m), which
# previously forced an artificial 30m STUCK_STALLED_THRESHOLD bump
# (gu-9ed0). Bump the heartbeat every 30s while the gate runs so the
# witness stops mistaking a slow fetch for a dead agent (cv-p3fem Phase 2).
#
# Best-effort: if `gt heartbeat keepalive` fails (no GT_SESSION, gt missing,
# etc.) the warn-and-no-op path keeps this script from breaking CI.
if command -v gt >/dev/null 2>&1; then
  ( while sleep 30; do gt heartbeat keepalive --op=check-upstream-rebased >/dev/null 2>&1 || true; done ) &
  __KEEPALIVE_PID=$!
  trap 'kill "$__KEEPALIVE_PID" 2>/dev/null || true' EXIT
fi

# Auto-add upstream remote if missing, so polecat worktrees work without manual setup.
if ! git remote get-url "$UPSTREAM_REMOTE" >/dev/null 2>&1; then
  echo "Adding '$UPSTREAM_REMOTE' remote -> $UPSTREAM_URL"
  git remote add "$UPSTREAM_REMOTE" "$UPSTREAM_URL"
fi

# auto_file_fork_sync_bead files (or updates) a single deduped tracking bead
# when the fork-wide divergence condition holds: origin/main is behind
# upstream/main. This is the recurring rig-coordination condition that always
# needs the same response (a coordinated fork-sync), so it should self-track
# rather than wait on a witness escalation + manual mayor triage (gu-sf0vo).
#
# Best-effort by construction: every failure mode (no bd, no origin ref, bd
# error) warns and returns 0 so it can never break the gate. The gate's own
# exit status is decided by the caller, not this function.
auto_file_fork_sync_bead() {
  [ "$GT_FORKSYNC_AUTOFILE" = "1" ] || return 0
  command -v bd >/dev/null 2>&1 || return 0

  local origin_ref="$ORIGIN_REMOTE/$ORIGIN_BRANCH"
  git rev-parse --verify --quiet "$origin_ref" >/dev/null 2>&1 || return 0

  # Fork-wide condition: is origin/main itself behind upstream? A stale feature
  # branch (origin current, only HEAD behind) does NOT warrant a tracking bead.
  if git merge-base --is-ancestor "$UPSTREAM_REF" "$origin_ref" 2>/dev/null; then
    return 0
  fi

  local behind
  behind=$(git rev-list --count "$origin_ref..$UPSTREAM_REF" 2>/dev/null) || return 0
  [ -n "$behind" ] && [ "$behind" != "0" ] || return 0

  local rig="${GT_RIG:-fork}"
  local title="[fork-sync] $rig $origin_ref is $behind commits behind $UPSTREAM_REF"
  local body
  body="check-upstream-rebased gate is RED fork-wide: $origin_ref is $behind commits behind $UPSTREAM_REF.

Auto-filed by scripts/check-upstream-rebased.sh (gu-sf0vo). The fork-wide
rebase-check failure is a known, recurring rig-coordination condition that
always needs the same response: a coordinated fork-sync of $origin_ref from
$UPSTREAM_REF. This bead's existence == the gate is red.

DIVERGENCE: git rev-list --count $origin_ref..$UPSTREAM_REF = $behind

ACTION: coordinated fork-sync (operator/mayor lane — do NOT autonomously
rebase the live source repo). Related: gu-czolf (auto-prioritize fork-sync
MRs to P0)."

  # Dedup: reuse an existing OPEN [fork-sync] tracking bead rather than filing a
  # duplicate every gate run. Match on the label we stamp below.
  local existing
  existing=$(bd list --label fork-sync --status open,in_progress,blocked \
    --limit 0 --json 2>/dev/null | jq -r '.[0].id // empty' 2>/dev/null) || existing=""

  if [ -n "$existing" ]; then
    echo "↻ fork-sync tracking bead $existing already open; updating divergence ($behind behind)." >&2
    bd update "$existing" --title "$title" \
      --append-notes "$(date -u +%Y-%m-%dT%H:%M:%SZ): gate red, $origin_ref $behind commits behind $UPSTREAM_REF." \
      >/dev/null 2>&1 || echo "  (bd update failed; non-fatal)" >&2
    return 0
  fi

  echo "✚ filing fork-sync tracking bead ($behind commits behind)." >&2
  local new_id
  new_id=$(bd create "$title" -t task -p 0 -l fork-sync,gt:queue-unblocker \
    --description "$body" --silent 2>/dev/null) || new_id=""
  if [ -n "$new_id" ]; then
    echo "  filed $new_id" >&2
  else
    echo "  (bd create failed; non-fatal — gate still reports failure)" >&2
  fi
  return 0
}

git fetch --quiet "$UPSTREAM_REMOTE" "$UPSTREAM_BRANCH"
UPSTREAM_REF="$UPSTREAM_REMOTE/$UPSTREAM_BRANCH"

if git merge-base --is-ancestor "$UPSTREAM_REF" HEAD; then
  echo "✓ Fork is rebased on $UPSTREAM_REF"
  exit 0
fi

# Gate failed. If this reflects a fork-wide divergence, self-file/update a
# deduped tracking bead (best-effort; never alters the gate's exit status).
auto_file_fork_sync_bead

echo "✗ $UPSTREAM_REF is NOT an ancestor of HEAD." >&2
echo "  Your fork has fallen behind upstream. Rebase or merge upstream/main before merging." >&2
echo "" >&2
echo "  Fix:" >&2
echo "    git fetch $UPSTREAM_REMOTE" >&2
echo "    git rebase $UPSTREAM_REF   # or: git merge $UPSTREAM_REF" >&2
echo "" >&2
echo "  Commits in upstream but not here:" >&2
git log --oneline --max-count=20 HEAD.."$UPSTREAM_REF" >&2
exit 1

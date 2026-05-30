#!/usr/bin/env bash
# Fails if upstream/main is not an ancestor of HEAD.
# Enforces that this fork stays rebased on its upstream.
set -euo pipefail

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
UPSTREAM_BRANCH="${UPSTREAM_BRANCH:-main}"
UPSTREAM_URL="https://github.com/gastownhall/gastown.git"

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

git fetch --quiet "$UPSTREAM_REMOTE" "$UPSTREAM_BRANCH"
UPSTREAM_REF="$UPSTREAM_REMOTE/$UPSTREAM_BRANCH"

if git merge-base --is-ancestor "$UPSTREAM_REF" HEAD; then
  echo "✓ Fork is rebased on $UPSTREAM_REF"
  exit 0
fi

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

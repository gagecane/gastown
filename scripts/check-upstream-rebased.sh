#!/usr/bin/env bash
# Fails if upstream/main is not an ancestor of HEAD.
# Enforces that this fork stays rebased on its upstream.
set -euo pipefail

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
UPSTREAM_BRANCH="${UPSTREAM_BRANCH:-main}"

if ! git remote get-url "$UPSTREAM_REMOTE" >/dev/null 2>&1; then
  echo "ERROR: No '$UPSTREAM_REMOTE' remote configured. Add it with:" >&2
  echo "  git remote add $UPSTREAM_REMOTE https://github.com/gastownhall/gastown.git" >&2
  exit 2
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
git log --oneline HEAD.."$UPSTREAM_REF" | head -20 >&2
exit 1

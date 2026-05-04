#!/usr/bin/env bash
# rebuild-gt/run.sh — Rebuild gt binary from gastown source if stale.
#
# SAFETY: Only rebuilds forward (binary is ancestor of HEAD) and only
# from main branch. A bad rebuild caused a crash loop (every session's
# startup hook failed, witness respawned, loop repeated every 1-2 min).

set -euo pipefail

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"

# Locate the gastown source rig. The fork ("gastown_upstream") is preferred
# over the vanilla name for backward compat with pre-fork towns. (gu-1rae)
RIG_ROOT=""
RIG_NAME=""
for candidate in gastown_upstream gastown; do
  if [ -f "${TOWN_ROOT}/${candidate}/mayor/rig/cmd/gt/main.go" ]; then
    RIG_ROOT="${TOWN_ROOT}/${candidate}/mayor/rig"
    RIG_NAME="${candidate}"
    break
  fi
  if [ -f "${TOWN_ROOT}/${candidate}/cmd/gt/main.go" ]; then
    RIG_ROOT="${TOWN_ROOT}/${candidate}"
    RIG_NAME="${candidate}"
    break
  fi
done

log() { echo "[rebuild-gt] $*"; }

# Rig label used in plugin-run tracking wisps. Falls back to "unknown" so we
# never emit a malformed label if rig discovery failed.
RIG_LABEL="rig:${RIG_NAME:-unknown}"

# --- Sync local checkout with origin/main -----------------------------------
#
# `gt stale` compares the binary's embedded commit to the *local* HEAD of the
# source rig. If the local checkout is behind origin/main, stale reports
# "fresh" even when upstream has merged fixes (gu-wcxv). Fetch origin and
# fast-forward main BEFORE calling gt stale so the staleness check sees the
# real picture. Refuses dirty trees and non-ff-merge to preserve existing
# safety guarantees.
if [ -n "$RIG_ROOT" ] && [ -d "$RIG_ROOT" ]; then
  PREFLIGHT_DIRTY=$(git -C "$RIG_ROOT" status --porcelain 2>/dev/null || echo "DIRTY_CHECK_FAILED")
  if [ -z "$PREFLIGHT_DIRTY" ]; then
    PREFLIGHT_BRANCH=$(git -C "$RIG_ROOT" branch --show-current 2>/dev/null || echo "")
    if [ "$PREFLIGHT_BRANCH" = "main" ]; then
      if git -C "$RIG_ROOT" fetch origin main --quiet 2>/dev/null; then
        LOCAL_SHA=$(git -C "$RIG_ROOT" rev-parse main 2>/dev/null || echo "")
        REMOTE_SHA=$(git -C "$RIG_ROOT" rev-parse origin/main 2>/dev/null || echo "")
        if [ -n "$LOCAL_SHA" ] && [ -n "$REMOTE_SHA" ] && [ "$LOCAL_SHA" != "$REMOTE_SHA" ]; then
          if git -C "$RIG_ROOT" merge-base --is-ancestor "$LOCAL_SHA" "$REMOTE_SHA" 2>/dev/null; then
            if git -C "$RIG_ROOT" merge --ff-only origin/main --quiet 2>/dev/null; then
              log "Pulled origin/main: ${LOCAL_SHA:0:8} -> ${REMOTE_SHA:0:8}"
            else
              log "ff-merge of origin/main failed (will let stale check proceed against local HEAD)"
            fi
          else
            log "Local main diverged from origin/main — not fast-forwardable. Skipping pull."
            bd create "Plugin: rebuild-gt [skipped]" -t chore --ephemeral \
              -l "type:plugin-run,plugin:rebuild-gt,${RIG_LABEL},result:skipped" \
              -d "Skipped: local main diverged from origin/main (not fast-forwardable). Needs manual attention." \
              --silent 2>/dev/null || true
            exit 0
          fi
        fi
      else
        log "git fetch origin main failed (network?); proceeding with local state"
      fi
    fi
    # If not on main, the later pre-flight check will skip with a proper wisp.
  fi
  # If dirty, the later pre-flight check will skip with a proper wisp.
fi

# --- Detection ---------------------------------------------------------------

log "Checking binary staleness..."
STALE_JSON=$(gt stale --json 2>/dev/null) || {
  log "gt stale --json failed, skipping"
  exit 0
}

IS_STALE=$(echo "$STALE_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin).get('stale', False))" 2>/dev/null || echo "False")
SAFE=$(echo "$STALE_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin).get('safe_to_rebuild', False))" 2>/dev/null || echo "False")

if [ "$IS_STALE" != "True" ]; then
  log "Binary is fresh. Nothing to do."
  bd create "rebuild-gt: binary is fresh" -t chore --ephemeral \
    -l "type:plugin-run,plugin:rebuild-gt,${RIG_LABEL},result:success" \
    --silent 2>/dev/null || true
  exit 0
fi

if [ "$SAFE" != "True" ]; then
  log "Not safe to rebuild (not on main or would be a downgrade). Skipping."
  bd create "Plugin: rebuild-gt [skipped]" -t chore --ephemeral \
    -l "type:plugin-run,plugin:rebuild-gt,${RIG_LABEL},result:skipped" \
    -d "Skipped: not safe to rebuild" --silent 2>/dev/null || true
  exit 0
fi

# --- Pre-flight checks -------------------------------------------------------

log "Pre-flight checks..."

if [ -z "$RIG_ROOT" ] || [ ! -d "$RIG_ROOT" ]; then
  log "Could not locate gastown source rig under $TOWN_ROOT (tried: gastown_upstream, gastown). Skipping."
  bd create "Plugin: rebuild-gt [skipped]" -t chore --ephemeral \
    -l "type:plugin-run,plugin:rebuild-gt,${RIG_LABEL},result:skipped" \
    -d "Skipped: gastown source rig not found under $TOWN_ROOT" --silent 2>/dev/null || true
  exit 0
fi

log "Using source rig: $RIG_ROOT (rig=$RIG_NAME)"

DIRTY=$(git -C "$RIG_ROOT" status --porcelain 2>/dev/null)
if [ -n "$DIRTY" ]; then
  log "Repo is dirty, skipping rebuild."
  bd create "Plugin: rebuild-gt [skipped]" -t chore --ephemeral \
    -l "type:plugin-run,plugin:rebuild-gt,${RIG_LABEL},result:skipped" \
    -d "Skipped: repo has uncommitted changes" --silent 2>/dev/null || true
  exit 0
fi

BRANCH=$(git -C "$RIG_ROOT" branch --show-current 2>/dev/null)
if [ "$BRANCH" != "main" ]; then
  log "Not on main branch (on $BRANCH), skipping rebuild."
  bd create "Plugin: rebuild-gt [skipped]" -t chore --ephemeral \
    -l "type:plugin-run,plugin:rebuild-gt,${RIG_LABEL},result:skipped" \
    -d "Skipped: not on main branch (on $BRANCH)" --silent 2>/dev/null || true
  exit 0
fi

# --- Build -------------------------------------------------------------------

OLD_VER=$(gt version 2>/dev/null | head -1 || echo "unknown")
log "Rebuilding gt from $RIG_ROOT..."

if (cd "$RIG_ROOT" && make build && make safe-install) 2>&1; then
  NEW_VER=$(gt version 2>/dev/null | head -1 || echo "unknown")
  log "Rebuilt: $OLD_VER -> $NEW_VER"
  bd create "rebuild-gt: $OLD_VER -> $NEW_VER" -t chore --ephemeral \
    -l "type:plugin-run,plugin:rebuild-gt,${RIG_LABEL},result:success" \
    --silent 2>/dev/null || true

  # The on-disk binary is new, but the running daemon is still executing its
  # old in-memory image. main_branch_test (and other daemon-resident logic)
  # will keep running the old code until the daemon restarts. File a bead
  # asking mayor to restart. (gu-wcxv Part B)
  DAEMON_PID_FILE="${GT_TOWN_ROOT:-$HOME/gt}/daemon/daemon.pid"
  DAEMON_PID="unknown"
  if [ -f "$DAEMON_PID_FILE" ]; then
    DAEMON_PID=$(head -1 "$DAEMON_PID_FILE" 2>/dev/null || echo "unknown")
  fi
  bd create "daemon-restart-pending: gt binary upgraded to $NEW_VER" \
    -t chore -p 2 \
    -l "type:daemon-restart-pending,plugin:rebuild-gt,${RIG_LABEL}" \
    -d "rebuild-gt upgraded the on-disk binary from $OLD_VER to $NEW_VER. The running daemon process (pid $DAEMON_PID) is still on the old binary. Operator action: gt daemon stop && gt daemon start" \
    --silent 2>/dev/null || true
else
  ERROR="make build/safe-install failed"
  log "FAILED: $ERROR"
  bd create "Plugin: rebuild-gt [failure]" -t chore --ephemeral \
    -l "type:plugin-run,plugin:rebuild-gt,${RIG_LABEL},result:failure" \
    -d "Build failed: $ERROR" --silent 2>/dev/null || true
  gt escalate "Plugin FAILED: rebuild-gt" -s medium 2>/dev/null || true
  exit 1
fi

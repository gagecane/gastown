#!/usr/bin/env bash
# gobuild-tmp-sweep/run.sh — Sweep stale go-build/go-link temp dirs from /tmp.
#
# Go's toolchain leaves go-build*/go-link* working dirs under $TMPDIR when a
# build/gate run is killed (SIGTERM during a pre-push stall, or a concurrent
# gate losing a race). On this host /tmp is a 16G tmpfs shared by every rig's
# merge gate, so leaked dirs accumulate until /tmp fills and gates fail with
# ENOSPC — a flaky, town-wide false gate failure. This periodic sweep removes
# dirs older than a threshold (live builds keep a fresh mtime, so they are
# never touched). See gu-vzkyh / gu-2bvvz.
#
# Deterministic shell, no AI. Exit-code contract:
#   0       success — INCLUDING found-nothing AND deleted-N (sweeping is success).
#   non-0   operational failure ONLY (sweep dir unreadable).

set -euo pipefail

# --- Configuration -----------------------------------------------------------

# Sweep dir: explicit override, else the build TMPDIR, else /tmp (matches where
# the Go toolchain writes by default).
SWEEP_DIR="${GT_GOBUILD_SWEEP_TMPDIR:-${TMPDIR:-/tmp}}"
SWEEP_DIR="${SWEEP_DIR%/}"  # strip trailing slash
MIN_AGE_MIN="${GT_GOBUILD_SWEEP_MIN_AGE_MIN:-30}"
DRY_RUN="${GT_GOBUILD_SWEEP_DRY_RUN:-0}"

log() { echo "[gobuild-tmp-sweep] $*"; }

# --- Preflight ---------------------------------------------------------------

if [[ ! -d "$SWEEP_DIR" ]]; then
  log "Sweep dir $SWEEP_DIR does not exist or is not a directory. Nothing to do."
  exit 1
fi

usage_before=$(df -h "$SWEEP_DIR" 2>/dev/null | awk 'NR==2 {print $5}')
log "Sweeping $SWEEP_DIR (usage ${usage_before:-?}, min age ${MIN_AGE_MIN}m, dry_run=${DRY_RUN})"

# --- Find stale dirs ----------------------------------------------------------

# Only the top level: the Go toolchain creates go-build*/go-link* directly under
# $TMPDIR. -mmin +N selects dirs whose mtime is older than N minutes; a live
# build's working dir is freshly modified, so it never matches.
mapfile -d '' STALE < <(
  find "$SWEEP_DIR" -maxdepth 1 -type d \
    \( -name 'go-build*' -o -name 'go-link*' \) \
    -mmin "+${MIN_AGE_MIN}" -print0 2>/dev/null
)

found=${#STALE[@]}

if [[ $found -eq 0 ]]; then
  log "No stale go-build/go-link dirs found. Nothing to do."
  SUMMARY="gobuild-tmp-sweep: 0 stale dirs (usage ${usage_before:-?})"
  log "=== Done === $SUMMARY"
  bd create "$SUMMARY" -t chore --ephemeral \
    -l type:plugin-run,plugin:gobuild-tmp-sweep,result:success \
    --silent 2>/dev/null || true
  exit 0
fi

# --- Sweep --------------------------------------------------------------------

deleted=0
for dir in "${STALE[@]}"; do
  [[ -z "$dir" ]] && continue
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY-RUN would delete: $dir"
    deleted=$((deleted + 1))
    continue
  fi
  if rm -rf "$dir" 2>/dev/null; then
    deleted=$((deleted + 1))
  else
    log "WARN: failed to remove $dir"
  fi
done

usage_after=$(df -h "$SWEEP_DIR" 2>/dev/null | awk 'NR==2 {print $5}')

# --- Report -------------------------------------------------------------------

if [[ "$DRY_RUN" == "1" ]]; then
  SUMMARY="gobuild-tmp-sweep: DRY-RUN, ${deleted}/${found} stale dirs eligible (usage ${usage_before:-?})"
else
  SUMMARY="gobuild-tmp-sweep: removed ${deleted}/${found} stale dirs (usage ${usage_before:-?} -> ${usage_after:-?})"
fi
log "=== Done === $SUMMARY"

bd create "$SUMMARY" -t chore --ephemeral \
  -l type:plugin-run,plugin:gobuild-tmp-sweep,result:success \
  --silent 2>/dev/null || true

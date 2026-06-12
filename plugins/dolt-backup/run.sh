#!/usr/bin/env bash
# dolt-backup/run.sh — Deterministic Dolt database backup.
#
# Syncs production databases to filesystem backups via `dolt backup sync`.
# Skips databases that haven't changed since last backup (hash check).
# Only escalates when actual backup operations fail — not on ping failures.
# After each run, prunes old .darc files keeping at least BACKUP_SAFETY_FLOOR
# most-recent archives and deleting any older than BACKUP_RETENTION_DAYS.
#
# Usage: ./run.sh [--databases db1,db2,...] [--dry-run]

set -euo pipefail

# --- Configuration -----------------------------------------------------------

# Honor GT_TOWN_ROOT first (set by daemon when invoking plugins). The
# earlier hardcoded ~/gt fallback caused "No databases found" for towns
# rooted elsewhere (hq-huub).
if [[ -z "${DOLT_DATA_DIR:-}" ]]; then
  if [[ -n "${GT_TOWN_ROOT:-}" && -d "$GT_TOWN_ROOT/.dolt-data" ]]; then
    DOLT_DATA_DIR="$GT_TOWN_ROOT/.dolt-data"
  else
    DOLT_DATA_DIR="$HOME/gt/.dolt-data"
  fi
fi
if [[ -z "${DOLT_BACKUP_DIR:-}" ]]; then
  if [[ -n "${GT_TOWN_ROOT:-}" ]]; then
    DOLT_BACKUP_DIR="$GT_TOWN_ROOT/.dolt-backup"
  else
    DOLT_BACKUP_DIR="$HOME/gt/.dolt-backup"
  fi
fi
BACKUP_DIR="$DOLT_BACKUP_DIR"
BACKUP_TIMEOUT=60
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-7}"
BACKUP_SAFETY_FLOOR="${BACKUP_SAFETY_FLOOR:-3}"

# --- Argument parsing ---------------------------------------------------------

DRY_RUN=false
EXPLICIT_DBS=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --databases) EXPLICIT_DBS="$2"; shift 2 ;;
    --dry-run)   DRY_RUN=true; shift ;;
    --help|-h)
      echo "Usage: $0 [--databases db1,db2,...] [--dry-run]"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# --- Helpers ------------------------------------------------------------------

log() {
  echo "[dolt-backup] $*"
}

# town_root resolves the town directory for locating town-relative state
# (heartbeat, wisp config). Prefer GT_TOWN_ROOT (set by the daemon when invoking
# the plugin); fall back to the parent of DOLT_DATA_DIR so a manual run still works.
town_root() {
  if [[ -n "${GT_TOWN_ROOT:-}" ]]; then
    echo "$GT_TOWN_ROOT"
  else
    dirname "$DOLT_DATA_DIR"
  fi
}

# Heartbeat: resolve the town runtime dir for the durable completion signal.
heartbeat_path() {
  echo "$(town_root)/.runtime/dolt-backup-heartbeat.json"
}

# is_parked <db> reports (exit 0) whether the rig backing a database is PARKED.
# A backup DB directory name equals its rig name, and "gt rig park" writes
# status=parked into the wisp config at <town>/.beads-wisp/config/<rig>.json —
# the same Layer-1 fast path internal/rig.IsRigParkedOrDocked checks first.
#
# Parked rigs have their agents stopped and may never have been given a backup
# remote BY DESIGN, so a parked rig's "no remote" / sync failure is the expected
# steady state, NOT a data-loss event (gu-qwe7q/gu-otphy). Skipping them before
# provision/sync stops the per-cycle HIGH false-positive escalations. Docked
# rigs (status=docked) are skipped too — same rationale.
#
# Wisp-only by design: this plugin runs in plain bash with no access to the
# beads DB, so we cannot consult the persistent bead-label layer. The wisp layer
# is what "gt rig park" writes and covers the observed parked rigs; a rig parked
# only via bead label (wisp state lost) would not be skipped here and would fall
# through to the existing no-remote SKIP, still a low-severity warning rather
# than a HIGH failure.
is_parked() {
  local db="$1" wisp_file status
  wisp_file="$(town_root)/.beads-wisp/config/${db}.json"
  [[ -f "$wisp_file" ]] || return 1
  status="$(python3 -c "
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    print((d.get('values') or {}).get('status', ''))
except Exception:
    pass
" "$wisp_file" 2>/dev/null || true)"
  [[ "$status" == "parked" || "$status" == "docked" ]]
}

# write_heartbeat <status> <synced> <skipped> <failed> <total> <retention_dirs> <retention_failed> <detail>
# Emits a durable positive completion signal on EVERY run (gu-8xvpw). A watcher
# (gu-8xvpw CR2) alarms when this heartbeat is absent for longer than the backup
# interval — catching the silent failure modes a failure-only model misses
# (process killed, hung, never scheduled, or crashed before signaling). Written
# atomically (temp + mv) so a concurrent reader never sees a partial file.
# Best-effort: a heartbeat write failure is logged and never aborts the run.
write_heartbeat() {
  local status="$1" synced="$2" skipped="$3" failed="$4" total="$5"
  local retention_dirs="$6" retention_failed="$7" detail="$8"
  local hb_file hb_tmp ts
  hb_file="$(heartbeat_path)"
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  if ! mkdir -p "$(dirname "$hb_file")" 2>/dev/null; then
    log "  heartbeat: could not create $(dirname "$hb_file") — skipping signal"
    return 0
  fi

  hb_tmp="${hb_file}.tmp.$$"
  # detail is plain ASCII summary text; escape backslash and double-quote for JSON.
  local detail_json="${detail//\\/\\\\}"
  detail_json="${detail_json//\"/\\\"}"
  if printf '{"schema":1,"timestamp":"%s","status":"%s","synced":%d,"skipped":%d,"failed":%d,"total":%d,"retention_dirs":%d,"retention_failed":%d,"detail":"%s"}\n' \
      "$ts" "$status" "$synced" "$skipped" "$failed" "$total" "$retention_dirs" "$retention_failed" "$detail_json" \
      > "$hb_tmp" 2>/dev/null && mv -f "$hb_tmp" "$hb_file" 2>/dev/null; then
    log "  heartbeat: wrote $status signal to $hb_file"
  else
    rm -f "$hb_tmp" 2>/dev/null || true
    log "  heartbeat: write failed for $hb_file — skipping signal"
  fi
}

# retention_cleanup <backup_path>
# Deletes .darc files older than BACKUP_RETENTION_DAYS, always keeping the
# BACKUP_SAFETY_FLOOR most-recent files. Non-fatal: errors are logged and skipped.
retention_cleanup() {
  local backup_path="$1"
  [[ -d "$backup_path" ]] || return 0

  # Collect all .darc files sorted newest-first (ls -t is portable on macOS+Linux)
  local -a darcs=()
  while IFS= read -r f; do
    darcs+=("$f")
  done < <(ls -t "$backup_path"/*.darc 2>/dev/null || true)

  local total=${#darcs[@]}
  [[ $total -eq 0 ]] && return 0

  local deleted=0
  local freed_kb=0
  local errors=0
  local cutoff=$(( $(date +%s) - BACKUP_RETENTION_DAYS * 86400 ))

  for (( i=0; i<total; i++ )); do
    local f="${darcs[$i]}"
    # Safety floor: never delete the BACKUP_SAFETY_FLOOR most-recent archives
    (( i < BACKUP_SAFETY_FLOOR )) && continue

    # mtime: try GNU stat -c "%Y" first (Linux daemon), macOS stat -f "%m"
    # as fallback. Order matters: `stat -f` is GNU's FILESYSTEM-info mode and on
    # Linux prints a multiline `  File: ...` blob to stdout while returning
    # non-zero, which previously leaked into $mtime and crashed the arithmetic
    # below under `set -u` ("File: unbound variable"), aborting the whole run in
    # Step 3 even though all backups had already synced (gu-t9xgf). The numeric
    # guard makes a non-numeric result skip the file rather than crash or delete.
    local mtime
    mtime=$(stat -c "%Y" "$f" 2>/dev/null || stat -f "%m" "$f" 2>/dev/null || echo 0)
    if ! [[ "$mtime" =~ ^[0-9]+$ ]]; then
      log "    retention: could not read mtime of $(basename "$f") — skipping"
      errors=$(( errors + 1 ))
      continue
    fi

    if (( mtime < cutoff )); then
      local age_days=$(( ( $(date +%s) - mtime ) / 86400 ))
      local kb
      kb=$(du -k "$f" 2>/dev/null | cut -f1 || echo 0)
      if rm -f "$f" 2>/dev/null; then
        deleted=$(( deleted + 1 ))
        freed_kb=$(( freed_kb + kb ))
        log "    retention: removed $(basename "$f") (${age_days}d old, ${kb}KB)"
      else
        log "    retention: could not remove $(basename "$f") — skipping"
        errors=$(( errors + 1 ))
      fi
    fi
  done

  if (( deleted > 0 )); then
    log "  retention: freed ${freed_kb}KB across ${deleted} file(s) (${BACKUP_RETENTION_DAYS}d policy, floor ${BACKUP_SAFETY_FLOOR})"
  fi

  # Signal a non-fatal retention error to the caller so it can be counted and
  # surfaced as a low-severity warning (gu-8xvpw) — distinct from a backup-sync
  # failure. Returning explicitly also avoids inheriting the exit status of the
  # `(( deleted > 0 ))` test above (non-zero whenever nothing was pruned), which
  # the caller's `if retention_cleanup` would otherwise misread as a failure.
  (( errors > 0 )) && return 1
  return 0
}

# --- Step 1: Discover databases -----------------------------------------------

# Use explicit list if provided, otherwise auto-discover by scanning
# DOLT_DATA_DIR for directories that contain a .dolt subdirectory,
# excluding system and test databases.
if [[ -n "$EXPLICIT_DBS" ]]; then
  IFS=',' read -ra PROD_DBS <<< "$EXPLICIT_DBS"
else
  PROD_DBS=()
  while IFS= read -r line; do
    PROD_DBS+=("$line")
  done < <(
    for d in "$DOLT_DATA_DIR"/*/; do
      name="$(basename "$d")"
      [[ -d "$d/.dolt" ]] || continue
      [[ "$name" =~ ^(testdb_|beads_t|beads_pt|doctest_) ]] && continue
      echo "$name"
    done | sort
  )
  if [[ ${#PROD_DBS[@]} -eq 0 ]]; then
    log "ERROR: No databases found in $DOLT_DATA_DIR"
    exit 1
  fi
fi

log "Databases to backup (${#PROD_DBS[@]}): ${PROD_DBS[*]}"

# --- Step 2: Backup each database ---------------------------------------------

SYNCED=0
SKIPPED=0
FAILED=0
NO_REMOTE=0
PARKED=0
FAILED_DBS=""
NO_REMOTE_DBS=""
PARKED_DBS=""

for DB in "${PROD_DBS[@]}"; do
  DB_DIR="$DOLT_DATA_DIR/$DB"
  BACKUP_NAME="${DB}-backup"
  HASH_FILE="$BACKUP_DIR/${DB}/.last-backup-hash"

  # Parked/docked rig skip (gu-qwe7q/gu-otphy): a parked rig's agents are
  # stopped and it may have no backup remote BY DESIGN. Treat it as a benign
  # skip BEFORE provisioning/syncing so it never counts as an exit-1 failure
  # and never drives a HIGH escalation every cycle. Honors explicit --databases
  # too: an operator naming a parked DB still gets the safe skip.
  if is_parked "$DB"; then
    log "  $DB: rig is parked/docked — skipping backup (expected, not a failure)"
    PARKED=$((PARKED + 1))
    PARKED_DBS="$PARKED_DBS $DB"
    continue
  fi

  # Check DB dir exists
  if [[ ! -d "$DB_DIR/.dolt" ]]; then
    log "  $DB: no .dolt directory, skipping"
    FAILED=$((FAILED + 1))
    FAILED_DBS="$FAILED_DBS $DB(no-dir)"
    continue
  fi

  # Auto-provision the "<db>-backup" remote if it is missing (gc-wjy7m).
  #
  # Neither this plugin nor `gt maintain` ever ran `dolt backup add`, so the
  # "<db>-backup" remote never actually existed on any DB — `dolt backup sync`
  # returned "backup '<db>-backup' not found" on every cycle. The old plugin's
  # `|| true` PIPESTATUS bug (gu-8xvpw #3) masked this as success, so the backup
  # safety net had in fact NEVER worked. CR2's no-remote guard is what finally
  # surfaced it. The fix is to provision the remote idempotently here, pointing
  # at this plugin's own BACKUP_DIR/<db> — the same path its size reporting and
  # retention already target (Step 3). `dolt backup add` is additive (it creates
  # a backup remote; it deletes nothing), so this is safe to run every cycle.
  #
  # We still keep a genuine no-remote SKIP for DBs we cannot provision (the add
  # itself fails) — e.g. a corrupt/misnested DB like 'embeddeddolt' — so a real
  # configuration gap is reported as a low-severity warning, not a HIGH
  # data-loss failure.
  JUST_PROVISIONED=false
  if ! (cd "$DB_DIR" && dolt backup 2>/dev/null | grep -qx "$BACKUP_NAME"); then
    mkdir -p "$BACKUP_DIR/$DB"
    if (cd "$DB_DIR" && dolt backup add "$BACKUP_NAME" "file://$BACKUP_DIR/$DB" 2>/dev/null); then
      log "  $DB: provisioned '$BACKUP_NAME' remote -> $BACKUP_DIR/$DB"
      JUST_PROVISIONED=true
    else
      log "  $DB: no '$BACKUP_NAME' remote and could not provision one — skipping (not a data-loss failure)"
      NO_REMOTE=$((NO_REMOTE + 1))
      NO_REMOTE_DBS="$NO_REMOTE_DBS $DB"
      continue
    fi
  fi

  # Get current HEAD hash
  CURRENT_HASH=$(cd "$DB_DIR" && dolt log -n 1 --oneline 2>/dev/null | head -1 | cut -d' ' -f1 || true)
  if [[ -z "$CURRENT_HASH" ]]; then
    log "  $DB: could not get HEAD hash, will sync anyway"
    CURRENT_HASH="unknown"
  fi

  # Check last backed-up hash
  LAST_HASH=""
  if [[ -f "$HASH_FILE" ]]; then
    LAST_HASH=$(cat "$HASH_FILE")
  fi

  # Skip only when the hash matches AND we did not just provision the remote.
  # A freshly-provisioned remote holds zero backups regardless of what the
  # .last-backup-hash file says — and those hash files may be stale/bogus,
  # written by the old false-positive path (gu-8xvpw #3) without any real sync
  # ever landing. Forcing the first sync after provisioning guarantees the
  # backup actually exists before we trust the hash-skip optimization again.
  if [[ "$CURRENT_HASH" = "$LAST_HASH" ]] && [[ "$CURRENT_HASH" != "unknown" ]] && ! $JUST_PROVISIONED; then
    log "  $DB: unchanged ($CURRENT_HASH), skipping"
    SKIPPED=$((SKIPPED + 1))
    # Signal liveness to the daemon's dir-mtime freshness check even when
    # there is nothing to sync — otherwise checkBackupFreshness reports the
    # backup patrol as stalled forever and pours doctor molecules.
    touch "$BACKUP_DIR/$DB" 2>/dev/null || true
    continue
  fi

  if $DRY_RUN; then
    log "  $DB: DRY RUN would sync ($LAST_HASH -> $CURRENT_HASH)"
    SYNCED=$((SYNCED + 1))
    continue
  fi

  # Ensure the backup remote exists before syncing. Without this, towns
  # that never ran `dolt backup add` fail every sync (historically masked
  # by the SYNC_RC bug below).
  if ! (cd "$DB_DIR" && dolt backup -v 2>/dev/null | awk '{print $1}' | grep -qx "$BACKUP_NAME"); then
    log "  $DB: backup remote $BACKUP_NAME missing, adding -> file://$BACKUP_DIR/$DB/$BACKUP_NAME"
    if ! (cd "$DB_DIR" && dolt backup add "$BACKUP_NAME" "file://$BACKUP_DIR/$DB/$BACKUP_NAME" 2>&1); then
      FAILED=$((FAILED + 1))
      FAILED_DBS="$FAILED_DBS $DB(add-remote)"
      log "  $DB: FAILED to add backup remote"
      continue
    fi
  fi

  # Sync backup with timeout
  log "  $DB: syncing ($LAST_HASH -> $CURRENT_HASH)..."
  SYNC_START=$(date +%s)

  # Capture the real exit code. The previous `... ) || true` form forced the
  # status to 0 before SYNC_RC could read it (PIPESTATUS reflected `true`, not
  # dolt), so EVERY sync — including hard failures and timeouts — was counted
  # as success and never escalated (gu-8xvpw). Disable errexit around the
  # assignment instead, then read $? directly.
  set +e
  SYNC_OUTPUT=$(cd "$DB_DIR" && timeout "$BACKUP_TIMEOUT" dolt backup sync "$BACKUP_NAME" 2>&1)
  SYNC_RC=$?
  set -e
  SYNC_ELAPSED=$(( $(date +%s) - SYNC_START ))

  if [[ $SYNC_RC -eq 0 ]]; then
    # Record the hash we just backed up
    mkdir -p "$(dirname "$HASH_FILE")"
    echo "$CURRENT_HASH" > "$HASH_FILE"
    # Bump dir mtime for the daemon's freshness check (see skip branch).
    touch "$BACKUP_DIR/$DB" 2>/dev/null || true

    DB_SIZE=$(du -sh "$BACKUP_DIR/$DB" 2>/dev/null | cut -f1 || echo "?")
    SYNCED=$((SYNCED + 1))
    log "  $DB: synced in ${SYNC_ELAPSED}s ($DB_SIZE)"
  elif [[ $SYNC_RC -eq 124 ]]; then
    FAILED=$((FAILED + 1))
    FAILED_DBS="$FAILED_DBS $DB(timeout)"
    log "  $DB: TIMEOUT after ${BACKUP_TIMEOUT}s"
  else
    FAILED=$((FAILED + 1))
    FAILED_DBS="$FAILED_DBS $DB(exit-$SYNC_RC)"
    log "  $DB: FAILED (exit $SYNC_RC): $SYNC_OUTPUT"
  fi
done

# --- Step 3: Retention — prune old .darc files --------------------------------
# Read each DB's configured backup URL from repo_state.json. Only file:// remotes
# are pruned (remote/cloud remotes manage their own retention).

RETENTION_CLEANED=0
RETENTION_FAILED=0
for DB in "${PROD_DBS[@]}"; do
  DB_DIR="$DOLT_DATA_DIR/$DB"
  REPO_STATE="$DB_DIR/.dolt/repo_state.json"
  [[ -f "$REPO_STATE" ]] || continue

  # Extract first backup URL (python3 is available on all Gas Town nodes)
  BACKUP_URL=$(python3 -c "
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    bk = d.get('backups', {})
    if bk:
        print(list(bk.values())[0]['url'])
except Exception:
    pass
" "$REPO_STATE" 2>/dev/null || true)

  if [[ "$BACKUP_URL" == file://* ]]; then
    BACKUP_PATH="${BACKUP_URL#file://}"
    if $DRY_RUN; then
      DARC_COUNT=$(ls "$BACKUP_PATH"/*.darc 2>/dev/null | wc -l | tr -d ' ')
      log "  $DB: DRY RUN retention — $DARC_COUNT .darc in $BACKUP_PATH"
    else
      # Retention is best-effort maintenance, NOT a backup-sync operation: a
      # failure here must never be reported as data-loss (gu-t9xgf/gu-8xvpw).
      # retention_cleanup is internally non-fatal; guard the call anyway so a
      # future change can't let it abort the run under set -e.
      if retention_cleanup "$BACKUP_PATH"; then
        RETENTION_CLEANED=$(( RETENTION_CLEANED + 1 ))
      else
        RETENTION_FAILED=$(( RETENTION_FAILED + 1 ))
        log "  $DB: retention cleanup reported an error (non-fatal) — continuing"
      fi
    fi
  fi
done

# --- Step 4: Report results ---------------------------------------------------

SUMMARY="Backup: $SYNCED synced, $SKIPPED unchanged, $FAILED failed, $NO_REMOTE no-remote, $PARKED parked (of ${#PROD_DBS[@]} DBs); retention pruned ${RETENTION_CLEANED} dir(s), ${RETENTION_FAILED} retention error(s)"
log "$SUMMARY"
if [[ "$NO_REMOTE" -gt 0 ]]; then
  log "  no-remote DBs (enumerated but unconfigured — likely orphan/stray):$NO_REMOTE_DBS"
fi
if [[ "$PARKED" -gt 0 ]]; then
  log "  parked DBs (rig parked/docked — skipped by design, not a failure):$PARKED_DBS"
fi

# --- Step 5: Heartbeat, record result, escalate if needed ---------------------
#
# Severity is decoupled by failure class (gu-8xvpw):
#   - BACKUP-SYNC failure (FAILED>0): real data-safety risk → HIGH + exit 1.
#   - RETENTION-only failure (FAILED==0, RETENTION_FAILED>0): backups all
#     succeeded; only best-effort pruning hit an error → WARNING, exit 0. This
#     is exactly the gu-t9xgf class: a downstream maintenance bug must NOT
#     masquerade as a town-wide data-loss failure and page HIGH every cycle.
# The heartbeat is written on ALL paths so the absence-watcher (gu-8xvpw CR2)
# can distinguish "ran and succeeded" from "never ran / died silently".

if [[ "$FAILED" -eq 0 ]]; then
  # All remote-configured DBs synced. Retention errors and no-remote skips are
  # non-data-loss conditions → low-severity warning, not a HIGH page (gu-8xvpw).
  if [[ "$RETENTION_FAILED" -eq 0 && "$NO_REMOTE" -eq 0 ]]; then
    HB_STATUS="success"
  else
    HB_STATUS="success_with_warning"
  fi
  write_heartbeat "$HB_STATUS" "$SYNCED" "$SKIPPED" "$FAILED" "${#PROD_DBS[@]}" \
    "$RETENTION_CLEANED" "$RETENTION_FAILED" "$SUMMARY"

  # Backups succeeded — record quietly.
  bd create --title "dolt-backup: $SUMMARY" -t chore --ephemeral \
    -l type:plugin-run,plugin:dolt-backup,result:success \
    -d "$SUMMARY" --silent 2>/dev/null || true

  if [[ "$RETENTION_FAILED" -gt 0 || "$NO_REMOTE" -gt 0 ]]; then
    # Visible but low-severity: live backups are safe; config/maintenance needs a look.
    log "WARNING: backups succeeded but ${RETENTION_FAILED} retention error(s), ${NO_REMOTE} no-remote DB(s) — review (not data-loss)"
    gt escalate "dolt-backup warning: $SUMMARY" \
      --severity low \
      --reason "Backups succeeded ($SYNCED synced, $SKIPPED unchanged); ${RETENTION_FAILED} retention error(s), ${NO_REMOTE} DB(s) with no backup remote (${NO_REMOTE_DBS# }). Config/maintenance issue, not a data-loss failure." 2>/dev/null || true
  fi
else
  # Backup-sync failure — real data-safety risk. Heartbeat records the failure
  # so the watcher sees a recent (failed) run rather than silence.
  FAIL_MSG="$SUMMARY. Failed:$FAILED_DBS"
  write_heartbeat "failed" "$SYNCED" "$SKIPPED" "$FAILED" "${#PROD_DBS[@]}" \
    "$RETENTION_CLEANED" "$RETENTION_FAILED" "$FAIL_MSG"

  bd create --title "dolt-backup: FAILED - $FAIL_MSG" -t chore --ephemeral \
    -l type:plugin-run,plugin:dolt-backup,result:failure \
    -d "$FAIL_MSG" --silent 2>/dev/null || true

  gt escalate "dolt-backup FAILED: $FAIL_MSG" \
    --severity high \
    --reason "$FAIL_MSG" 2>/dev/null || true

  exit 1
fi

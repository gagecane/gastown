#!/usr/bin/env bash
# curio-dispatch-reconcile/run.sh — Nightly reconcile of the Curio Retrospect
# lane's dispatch against its actual output (gu-l84k2 fix #3, epic gu-60sk4,
# parent bug gu-ac2bu). Cron-gated (0 9 * * *), it runs 1h AFTER the 08:00
# Retrospect dispatch — past that run's 30m formula timeout — so the slung run
# has definitely terminated and its filed proposals (if any) are durable.
#
# THE GAP IT CLOSES (gu-ac2bu): B5's curio-retrospect-dispatch records
# result:success the instant `gt sling` succeeds, then exits. It cannot observe
# the polecat's terminal write. A polecat that reasons correctly but dies before
# its final `bd create` lands leaves the dispatch receipt reading success while
# the run's only deliverable — the proposal — was silently dropped.
#
# WHY NOT THE SLUNG WISP: a dead polecat's molecule wisp is reaped within minutes
# by the daemon's orphan-molecule reconcile pass, and the agent bead is compacted
# away — so the slung wisp no longer resolves by the time this runs. The reconcile
# therefore correlates two signals that SURVIVE:
#   1. the digest the dispatch rendered (artifacts/curio-retrospect/digest-*.md):
#      durable on disk, filename carries the dispatch stamp, JSON block lists the
#      actionable candidate clusters — what the run was EXPECTED to act on;
#   2. curio-proposal / curio-hypothesis beads filed since that stamp: durable,
#      carry created_at + cluster:<id> labels — what the run ACTUALLY landed.
#
# VERDICT (anti-false-positive: a quiet night must NEVER warn):
#   - no digest in range            -> result:skipped  (lane off / still in flight)
#   - digest has 0 actionable        -> result:success  (quiet night, correct)
#   - >=1 bead filed since dispatch  -> result:success  (terminal write verified;
#                                       fix #1 files a bead before any CR)
#   - 0 filed but all clusters deduped-> result:success  (correctly skipped)
#   - 0 filed + uncovered actionable -> result:warning + ONE-SHOT escalation
#                                       (the gu-ac2bu silent-drop signature)

set -uo pipefail
# NOTE: not `set -e` — the reconcile records its receipt and exits deliberately
# on every path, not bail silently mid-script.

PLUGIN_NAME="curio-dispatch-reconcile"
TARGET_RIG="gastown_upstream"

# --- tunables (operator-overridable) -----------------------------------------

# How far back to look for the dispatch's digest. A digest older than this is
# ignored — a missed daemon cycle is caught the next day rather than warned on.
LOOKBACK_SECS="${CURIO_RECONCILE_LOOKBACK_SECS:-21600}"   # 6h

# Minimum digest age before we reconcile it. MUST exceed the formula's 30m
# timeout so a still-in-flight run is never mis-flagged as a silent drop.
MIN_AGE_SECS="${CURIO_RECONCILE_MIN_AGE_SECS:-2400}"      # 40m

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
TOWN_ROOT="${TOWN_ROOT:-$HOME/gt}"

RIG_DIR="${TOWN_ROOT}/${TARGET_RIG}"
DIGEST_DIR="${TOWN_ROOT}/artifacts/curio-retrospect"

NOW_EPOCH=$(date -u +%s)

log() { echo "[${PLUGIN_NAME}] $*" >&2; }

record_receipt() {
  # $1=result (success|skipped|warning|failure), $2=title-suffix, $3=description
  local result="$1" title="$2" desc="${3:-}"
  bd create "${PLUGIN_NAME}: ${title}" -t chore --ephemeral \
    -l "type:plugin-run,plugin:${PLUGIN_NAME},result:${result}" \
    -d "${desc}" --silent 2>/dev/null || true
}

# digest_stamp_epoch FILENAME — parse a digest-YYYYMMDDTHHMMSSZ.md filename into
# unix seconds. Echoes nothing on a name that does not match the contract.
digest_stamp_epoch() {
  local base="${1##*/}"          # strip dir
  local s="${base#digest-}"      # strip prefix
  s="${s%.md}"                   # strip suffix
  # Expect 20260613T211225Z (8 digits, T, 6 digits, Z).
  [[ "$s" =~ ^[0-9]{8}T[0-9]{6}Z$ ]] || return 0
  date -u -d "${s:0:4}-${s:4:2}-${s:6:2}T${s:9:2}:${s:11:2}:${s:13:2}Z" +%s 2>/dev/null || true
}

# epoch_of RFC3339 -> unix seconds (empty on parse failure).
epoch_of() {
  date -u -d "$1" +%s 2>/dev/null || true
}

# =============================================================================
# 1. Locate the dispatch's digest
# =============================================================================
#
# The newest digest whose stamp is within [now-LOOKBACK, now-MIN_AGE]. The
# min-age floor keeps us safely past the 30m formula timeout so an in-flight run
# is never reconciled. Filenames sort lexically = chronologically, so newest
# last.

find_target_digest() {
  [[ -d "$DIGEST_DIR" ]] || return 0
  local f stamp newest="" newest_stamp=0
  # Iterate newest-first; pick the first in-window candidate.
  while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    stamp=$(digest_stamp_epoch "$f")
    [[ -n "$stamp" ]] || continue
    local age=$(( NOW_EPOCH - stamp ))
    # Too fresh (run may still be in flight) — skip.
    (( age < MIN_AGE_SECS )) && continue
    # Too old (outside lookback) — since we iterate newest-first, nothing older
    # will qualify either; stop.
    (( age > LOOKBACK_SECS )) && break
    newest="$f"; newest_stamp="$stamp"
    break
  done < <(ls -1 "$DIGEST_DIR"/digest-*.md 2>/dev/null | sort -r)
  [[ -n "$newest" ]] || return 0
  echo "${newest_stamp}	${newest}"
}

TARGET=$(find_target_digest)
if [[ -z "$TARGET" ]]; then
  log "no digest in [now-${LOOKBACK_SECS}s, now-${MIN_AGE_SECS}s] — no terminated dispatch to reconcile, skipping"
  record_receipt "skipped" "no dispatch in range" \
    "No Curio Retrospect digest found under ${DIGEST_DIR} with a stamp between
${MIN_AGE_SECS}s and ${LOOKBACK_SECS}s ago. The lane either did not dispatch (kill
switch off, a guard skipped it), or the most recent run is still within its
formula timeout. Nothing to reconcile this cycle."
  exit 0
fi

DISPATCH_EPOCH="${TARGET%%	*}"
DIGEST_PATH="${TARGET#*	}"
DISPATCH_TS=$(date -u -d "@${DISPATCH_EPOCH}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "@${DISPATCH_EPOCH}")
log "reconciling dispatch digest ${DIGEST_PATH} (dispatched ${DISPATCH_TS})"

# =============================================================================
# 2. Read what the run was EXPECTED to act on (digest actionable clusters)
# =============================================================================
#
# The digest's fenced JSON block (B1 digestDoc) carries clusters[].cluster_id.
# Extract the block, then the set of actionable cluster keys. An empty set means
# the run had nothing to propose — a quiet night, which is a correct result.

digest_json() {
  awk '/^```json/{f=1;next} /^```/{if(f){f=0}} f' "$DIGEST_PATH" 2>/dev/null
}

ACTIONABLE_CLUSTERS=$(digest_json | jq -r '.clusters[]?.cluster_id // empty' 2>/dev/null | sort -u)
ACTIONABLE_COUNT=$(grep -c . <<<"$ACTIONABLE_CLUSTERS" 2>/dev/null || echo 0)
[[ -n "$ACTIONABLE_CLUSTERS" ]] || ACTIONABLE_COUNT=0

if (( ACTIONABLE_COUNT == 0 )); then
  log "digest had 0 actionable clusters — quiet night, nothing was expected; success"
  record_receipt "success" "quiet night (0 actionable clusters)" \
    "Reconciled Curio Retrospect dispatch of ${DISPATCH_TS}.
The digest (${DIGEST_PATH##*/}) listed ZERO actionable candidate clusters, so the
run correctly had nothing to propose. Filing zero proposals on a quiet night is
the expected, correct result (mol-curio-retrospect finish step) — not a dropped
output. proposal-filed reconciled: OK."
  exit 0
fi

log "digest had ${ACTIONABLE_COUNT} actionable cluster(s)"

# =============================================================================
# 3. Read what the run ACTUALLY landed (proposal/hypothesis beads since dispatch)
# =============================================================================
#
# Count curio-proposal / curio-hypothesis beads created at/after the dispatch
# stamp, and collect the cluster:<id> labels they carry. We include closed beads
# (--all): a bead filed then closed (e.g. by expiry, or a fast human triage)
# still proves the terminal write path worked. Best-effort: tooling failure
# echoes nothing and we treat the run conservatively.

# filed_beads_json — JSON array of proposal/hypothesis beads (open + closed)
# created at/after the dispatch stamp. Union of both labels, de-duplicated by id.
filed_beads_json() {
  [[ -d "$RIG_DIR" ]] || { echo "[]"; return 0; }
  local prop hyp
  prop=$(cd "$RIG_DIR" && bd list --label curio-proposal --all --json 2>/dev/null || echo "[]")
  hyp=$(cd "$RIG_DIR" && bd list --label curio-hypothesis --all --json 2>/dev/null || echo "[]")
  jq -s --argjson since "$DISPATCH_EPOCH" '
    (.[0] // []) + (.[1] // [])
    | unique_by(.id)
    | map(select(
        (.created_at // "" | if . == "" then 0
         else (try fromdateiso8601 catch 0) end) >= $since))
  ' <<<"$prop"$'\n'"$hyp" 2>/dev/null || echo "[]"
}

FILED_JSON=$(filed_beads_json)
FILED_COUNT=$(jq -r 'length' <<<"$FILED_JSON" 2>/dev/null || echo 0)
[[ "$FILED_COUNT" =~ ^[0-9]+$ ]] || FILED_COUNT=0

if (( FILED_COUNT >= 1 )); then
  log "${FILED_COUNT} proposal/hypothesis bead(s) filed since dispatch — terminal write path verified; success"
  record_receipt "success" "proposal-filed verified (${FILED_COUNT} filed)" \
    "Reconciled Curio Retrospect dispatch of ${DISPATCH_TS}.
The digest listed ${ACTIONABLE_COUNT} actionable cluster(s); ${FILED_COUNT}
curio-proposal/curio-hypothesis bead(s) were filed at/after the dispatch stamp.
At least one durable proposal landed, so the polecat's terminal write path
completed — the gu-ac2bu silent-drop (success-of-dispatch != proposal-filed) did
NOT occur this run. proposal-filed reconciled: OK."
  exit 0
fi

# =============================================================================
# 4. Zero filed against an actionable digest — disambiguate dedup vs silent drop
# =============================================================================
#
# Zero filings is only a GAP if there was genuinely-new work. Subtract the
# clusters already covered by an OPEN proposal/hypothesis bead (the B6 dedup
# query the formula itself runs): if every actionable cluster was already
# covered, the run correctly filed nothing. Only uncovered actionable clusters
# with zero filings is the gu-ac2bu death signature.

covered_cluster_keys() {
  [[ -d "$RIG_DIR" ]] || return 0
  {
    cd "$RIG_DIR" && bd list --label curio-proposal --status open --json 2>/dev/null
    cd "$RIG_DIR" && bd list --label curio-hypothesis --status open --json 2>/dev/null
  } | jq -r '.[]?.labels[]? | select(startswith("cluster:")) | sub("^cluster:";"")' 2>/dev/null | sort -u
}

COVERED=$(covered_cluster_keys)

# Uncovered actionable = actionable clusters not in the covered set.
UNCOVERED=$(comm -23 <(echo "$ACTIONABLE_CLUSTERS") <(echo "$COVERED") 2>/dev/null | grep -c . 2>/dev/null || echo 0)
[[ "$UNCOVERED" =~ ^[0-9]+$ ]] || UNCOVERED=0

if (( UNCOVERED == 0 )); then
  log "0 filed, but all ${ACTIONABLE_COUNT} actionable cluster(s) already covered by open beads — correctly deduped; success"
  record_receipt "success" "0 filed, all clusters deduped" \
    "Reconciled Curio Retrospect dispatch of ${DISPATCH_TS}.
The digest listed ${ACTIONABLE_COUNT} actionable cluster(s) and zero new beads
were filed — but every actionable cluster is already covered by an OPEN
curio-proposal/curio-hypothesis bead (B6 dedup). Filing nothing was the correct,
deduped result, not a dropped output. proposal-filed reconciled: OK."
  exit 0
fi

# --- the gu-ac2bu silent-drop signature -------------------------------------
log "GAP: ${UNCOVERED} uncovered actionable cluster(s) and ZERO proposals filed since dispatch — gu-ac2bu silent-drop signature"

DIGEST_BASE="${DIGEST_PATH##*/}"
ESC_DESC="The Curio Retrospect dispatch of ${DISPATCH_TS} rendered a digest (${DIGEST_BASE}) with ${UNCOVERED} actionable candidate cluster(s) NOT already covered by an open proposal, yet ZERO curio-proposal/curio-hypothesis beads were filed in the dispatch window. The dispatch receipt reads success (the sling succeeded), but no proposal materialized — the gu-ac2bu pattern: a polecat session that died before its terminal bd-create landed, silently dropping the run's only output. The mol-curio-retrospect formula files a bead FIRST (fix #1, file-first discipline), so a non-empty actionable run should always land >=1 bead; zero filings means the run did not reach even its first durable write. Check for a dead/orphaned Retrospect polecat around ${DISPATCH_TS} and the townwide Dolt/teardown health (gu-mxupc). The next nightly dispatch will re-attempt (B5 STALE_AFTER_SECS guard); this alert only makes the drop visible."

gt escalate "Curio Retrospect dropped its output: actionable dispatch filed 0 proposals" \
  --severity low \
  --source "plugin:${PLUGIN_NAME}" \
  --signature "curio:dispatch-drop:${DIGEST_BASE}" \
  --dedup \
  --reason "$ESC_DESC" >/dev/null 2>&1 || \
  log "gt escalate failed (best-effort) — recording the warning receipt regardless"

record_receipt "warning" "silent drop: ${UNCOVERED} actionable, 0 filed" \
  "Reconciled Curio Retrospect dispatch of ${DISPATCH_TS} — OBSERVABILITY GAP DETECTED.

The digest (${DIGEST_BASE}) listed ${ACTIONABLE_COUNT} actionable cluster(s),
${UNCOVERED} of them NOT covered by any open proposal, yet ZERO
curio-proposal/curio-hypothesis beads were filed at/after the dispatch stamp.

This is the gu-ac2bu signature: success-of-dispatch != proposal-filed. The sling
succeeded and the dispatch receipt reads success, but the polecat's only
deliverable never landed — the run almost certainly died before its first
durable bd-create (fix #1 files a bead before any CR, so an actionable run should
land >=1 bead).

Recovery is already in place (next-night re-dispatch via B5's STALE_AFTER_SECS
guard; the orphan-molecule pass reaps the dead wisp). This receipt + a one-shot
low-severity escalation (signature curio:dispatch-drop:${DIGEST_BASE}) make the
drop VISIBLE rather than silent — which is this plugin's job."

log "done (warning recorded)"
exit 0

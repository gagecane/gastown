#!/usr/bin/env bash
# curio-retrospect-dispatch/run.sh — Nightly dispatch of the Curio Retrospect
# polecat (the LLM hypothesizer lane), cron-gated (0 8 * * *).
#
# This is the script implementation of plugins/curio-retrospect-dispatch/plugin.md
# (Curio P3 B5, epic gu-60sk4, child bead gu-5d8os). The daemon dog executes
# `bash run.sh` on schedule and records the result; the script is the source of
# truth and records its own plugin-run receipt.
#
# Behavior (design-doc Q1 + Q7):
#   1. Kill-switch pre-check: read mayor/daemon.json patrols.curio.llm.enabled;
#      if false/absent -> result:skipped, exit 0 (live Patrol untouched).
#   2. Single-instance guard: skip if a prior Retrospect run is still in flight
#      (an open bead attached to mol-curio-retrospect in the target rig). A
#      marker older than the formula timeout is treated as dead and ignored.
#   3. Volume circuit breaker: skip if open curio-proposal beads exceed the
#      ceiling (default 10) — the backlog must be worked down first.
#   4. Render the digest with `curio-proposer --emit-digest <path>`, then
#      `gt sling mol-curio-retrospect <rig> --var digest_path=<path>`.
#   5. Record a type:plugin-run receipt (result:success|skipped|failure).
#
# Sling syntax (gu-ono8h, gu-fc8h): the formula is the FIRST POSITIONAL arg, the
# rig is the second: `gt sling <formula> <rig>`. The --formula FLAG is a separate
# apply-on-bead feature; passing it makes gt sling consume $FORMULA as the flag
# value and read the rig as the bead-to-sling, failing "deferred dispatch
# requires a rig target". run_test.sh asserts the positional invocation shape.
#
# Digest-path sandbox contract (design-doc, child-beads.md §B5): polecats run in
# isolated git worktrees, but those worktrees live on the SAME host filesystem
# under the town root — there is no chroot/container boundary. So a host-shared
# absolute path under the town root is readable from inside the slung polecat's
# worktree without staging. We render the digest to
# $GT_TOWN_ROOT/artifacts/curio-retrospect/digest-<UTCstamp>.md (NOT the plugin's
# CWD, which is the plugin dir and is not visible to the polecat) and pass that
# exact path as --var digest_path=. The formula's step-1 `cat {{digest_path}}`
# then reads the same file. run_test.sh asserts this path-contract: the path
# emitted to and the path slung are one and the same host-shared variable.

set -uo pipefail
# NOTE: not `set -e` — failure/skip paths should record receipts and exit
# deliberately, not bail silently mid-script.

PLUGIN_NAME="curio-retrospect-dispatch"
FORMULA="mol-curio-retrospect"
TARGET_RIG="gastown_upstream"

# Volume circuit breaker ceiling (design-doc Q7.3): skip dispatch when the count
# of open curio-proposal beads is at or above this. Operator-overridable.
PROPOSAL_CEILING="${CURIO_PROPOSAL_CEILING:-10}"

# Staleness horizon for the single-instance guard (gc-i2nb6l Should-Fix / Risk
# #4). An in-flight marker older than the formula timeout is treated as dead and
# ignored, mirroring the witness MAYBE_DEAD discipline — a polecat that died
# after slinging but before its bead closed must NOT wedge the lane forever.
# Matches plugin.md [execution] timeout = "30m".
STALE_AFTER_SECS="${CURIO_RETROSPECT_STALE_SECS:-1800}"

# Per-run proposal cap forwarded to the formula (advisory at the agent layer;
# the enforced guard is the volume breaker above). Operator-overridable.
MAX_PROPOSALS="${CURIO_MAX_PROPOSALS:-3}"

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
TOWN_ROOT="${TOWN_ROOT:-$HOME/gt}"

log() { echo "[${PLUGIN_NAME}] $*" >&2; }

record_receipt() {
  # $1=result (success|skipped|failure), $2=title-suffix, $3=description
  local result="$1" title="$2" desc="${3:-}"
  bd create "${PLUGIN_NAME}: ${title}" -t chore --ephemeral \
    -l "type:plugin-run,plugin:${PLUGIN_NAME},result:${result}" \
    -d "${desc}" --silent 2>/dev/null || true
}

# --- 1. Kill-switch pre-check ------------------------------------------------
#
# Read mayor/daemon.json patrols.curio.llm.enabled. This is the same projection
# cmd/curio-proposer/config.go reads: absent file OR absent key reads as OFF, so
# the lane is opt-in. Toggling this flag OR uninstalling the plugin both disable
# the lane (defense in depth). A disabled lane is a graceful skip, not an
# escalation: rules just stay frozen (plugin.md severity=low).

DAEMON_JSON="${TOWN_ROOT}/mayor/daemon.json"

llm_enabled() {
  [[ -f "$DAEMON_JSON" ]] || return 1
  local v
  v=$(jq -r '.patrols.curio.llm.enabled // false' "$DAEMON_JSON" 2>/dev/null) || return 1
  [[ "$v" == "true" ]]
}

if ! llm_enabled; then
  log "kill-switch: patrols.curio.llm.enabled is false/absent in ${DAEMON_JSON} — Retrospect lane disabled, skipping"
  record_receipt "skipped" "lane disabled (llm.enabled=false)" \
    "patrols.curio.llm.enabled is false or absent in ${DAEMON_JSON}.
The Curio Retrospect lane is OFF; nothing dispatched. This is the expected,
graceful posture when the lane is disabled — not a failure. The live Curio
Patrol is untouched (this gate reads curio.llm.enabled ONLY, never
curio.enabled)."
  exit 0
fi

log "kill-switch: patrols.curio.llm.enabled=true — lane is on"

# --- 2. Single-instance guard ------------------------------------------------
#
# Defense in depth (design-doc Q1.3). The cron gate + DispatchGrace already
# suppress a same-day re-fire at the daemon level, but a slow review or a manual
# --force could stack a second polecat on a still-in-flight run. Look for any
# open/in-flight bead attached to this formula in the target rig.
#
# Staleness (gc-i2nb6l Risk #4): a polecat that died after its bead was created
# but before the convoy closed leaves an in-flight marker that would make every
# subsequent night skip — the lane wedges. So a marker whose bead.updated_at is
# older than STALE_AFTER_SECS (the formula timeout) is treated as DEAD and
# ignored, mirroring the witness MAYBE_DEAD discipline. Only a FRESH in-flight
# marker blocks dispatch.

RIG_DIR="${TOWN_ROOT}/${TARGET_RIG}"
NOW_EPOCH=$(date -u +%s)

# epoch_of RFC3339 -> unix seconds (empty string on parse failure).
epoch_of() {
  date -u -d "$1" +%s 2>/dev/null || true
}

fresh_in_flight() {
  # Echoes the IDs of FRESH (non-stale) open beads attached to $FORMULA in the
  # target rig. Best-effort: any tooling failure echoes nothing (we proceed and
  # let the formula's own scheduler catch true overlap rather than wedge).
  [[ -d "$RIG_DIR" ]] || return 0
  local open_json
  open_json=$(cd "$RIG_DIR" && bd list --status open,in_progress,blocked --json 2>/dev/null || echo "[]")
  [[ -n "$open_json" && "$open_json" != "[]" && "$open_json" != "null" ]] || return 0

  # Emit "<id> <updated_at>" for each bead attached to this formula, then filter
  # by staleness here (jq has no portable "now" we can trust across versions).
  while IFS=$'\t' read -r id updated; do
    [[ -n "$id" ]] || continue
    local up_epoch
    up_epoch=$(epoch_of "$updated")
    if [[ -z "$up_epoch" ]]; then
      # Unparseable timestamp: treat as fresh (conservative — prefer skipping a
      # dispatch over double-slinging).
      echo "$id"
      continue
    fi
    if (( NOW_EPOCH - up_epoch < STALE_AFTER_SECS )); then
      echo "$id"
    else
      log "single-instance: ignoring STALE marker ${id} (updated ${updated}, older than ${STALE_AFTER_SECS}s — treated as dead)"
    fi
  done < <(
    jq -r --arg formula "$FORMULA" '
      .[]
      | select((.description // "") | contains("attached_formula: " + $formula))
      | "\(.id)\t\(.updated_at)"
    ' <<<"$open_json" 2>/dev/null || true
  )
}

IN_FLIGHT=$(fresh_in_flight)
if [[ -n "$IN_FLIGHT" ]]; then
  log "single-instance: ${FORMULA} already in flight (fresh), skipping"
  log "  in-flight beads: $(tr '\n' ' ' <<<"$IN_FLIGHT")"
  record_receipt "skipped" "in-flight run detected" \
    "Single-instance guard: a fresh open bead attached to ${FORMULA} is still in
flight in ${TARGET_RIG}. Skipping to avoid stacking a second Retrospect polecat
on an unfinished review (design-doc Q1.3).

In-flight bead IDs:
$(printf '  %s\n' $IN_FLIGHT)

(Markers older than ${STALE_AFTER_SECS}s are treated as dead and do NOT block —
a crashed prior run cannot wedge the lane.)"
  exit 0
fi

# --- 3. Volume circuit breaker -----------------------------------------------
#
# design-doc Q7.3: if the count of open curio-proposal beads is at/above the
# ceiling, the backlog of unreviewed proposals is too deep — adding more would
# grow it unboundedly. Record result:skipped and do NOT dispatch. Counted in the
# target rig where the formula files its proposal beads.

count_open_proposals() {
  [[ -d "$RIG_DIR" ]] || { echo 0; return 0; }
  local n
  n=$(cd "$RIG_DIR" && bd list --label curio-proposal --status open --json 2>/dev/null \
    | jq -r 'length' 2>/dev/null) || { echo 0; return 0; }
  [[ "$n" =~ ^[0-9]+$ ]] && echo "$n" || echo 0
}

OPEN_PROPOSALS=$(count_open_proposals)
if (( OPEN_PROPOSALS >= PROPOSAL_CEILING )); then
  log "volume-breaker: ${OPEN_PROPOSALS} open curio-proposal beads >= ceiling ${PROPOSAL_CEILING}, skipping"
  record_receipt "skipped" "volume breaker tripped" \
    "Volume circuit breaker (design-doc Q7.3): ${OPEN_PROPOSALS} open
curio-proposal beads in ${TARGET_RIG} meets or exceeds the ceiling of
${PROPOSAL_CEILING}. The unreviewed-proposal backlog must be worked down before
the lane dispatches again. No polecat slung this run.

Override the ceiling with CURIO_PROPOSAL_CEILING if intentional."
  exit 0
fi

log "volume-breaker: ${OPEN_PROPOSALS} open curio-proposal beads (ceiling ${PROPOSAL_CEILING}) — clear"

# --- 4a. Render the digest ---------------------------------------------------
#
# The digest MUST land on a host-shared path the slung polecat's worktree can
# read. Polecat worktrees live under the town root on the same filesystem, so an
# absolute path under ${TOWN_ROOT}/artifacts is readable from inside any of
# them. We deliberately do NOT write into the plugin's CWD (the plugin dir),
# which the formula polecat never sees.
#
# curio-proposer is its own write-incapable binary (cmd/curio-proposer). It is
# not installed to a stable PATH location, so we resolve it: prefer a binary on
# PATH or at the source-rig root, else fall back to `go run ./cmd/curio-proposer`
# from the discovered gastown source rig (mirrors rebuild-gt's source discovery).

DIGEST_DIR="${TOWN_ROOT}/artifacts/curio-retrospect"
DIGEST_STAMP=$(date -u +%Y%m%dT%H%M%SZ)
DIGEST_PATH="${DIGEST_DIR}/digest-${DIGEST_STAMP}.md"

mkdir -p "$DIGEST_DIR" 2>/dev/null || {
  log "ERROR: could not create digest dir ${DIGEST_DIR}"
  record_receipt "failure" "digest dir uncreatable" \
    "Could not create the host-shared digest directory ${DIGEST_DIR}.
The digest must live on a path the slung polecat's worktree can read; without
it the formula's step-1 read would fail. Nothing dispatched."
  exit 1
}

# Locate the gastown source rig that contains cmd/curio-proposer (rebuild-gt
# precedent: prefer the "gastown_upstream" fork name, fall back to "gastown",
# and to the mayor/rig sub-checkout layout).
find_proposer_runner() {
  if command -v curio-proposer >/dev/null 2>&1; then
    echo "curio-proposer"
    return 0
  fi
  local candidate src
  for candidate in gastown_upstream gastown; do
    for src in "${TOWN_ROOT}/${candidate}/mayor/rig" "${TOWN_ROOT}/${candidate}"; do
      if [[ -x "${src}/curio-proposer" ]]; then
        echo "${src}/curio-proposer"
        return 0
      fi
    done
  done
  for candidate in gastown_upstream gastown; do
    for src in "${TOWN_ROOT}/${candidate}/mayor/rig" "${TOWN_ROOT}/${candidate}"; do
      if [[ -f "${src}/cmd/curio-proposer/main.go" ]]; then
        echo "go-run:${src}"
        return 0
      fi
    done
  done
  return 1
}

PROPOSER_RUNNER=$(find_proposer_runner) || {
  log "ERROR: could not locate curio-proposer binary or source"
  record_receipt "failure" "curio-proposer unresolved" \
    "Could not find the curio-proposer binary on PATH, at a gastown source-rig
root, or as buildable source (cmd/curio-proposer/main.go) under ${TOWN_ROOT}.
The digest cannot be rendered; nothing dispatched. Build curio-proposer
(go build ./cmd/curio-proposer) or ensure the gastown source rig is present."
  exit 1
}

log "rendering digest with: ${PROPOSER_RUNNER} (-> ${DIGEST_PATH})"

emit_digest() {
  if [[ "$PROPOSER_RUNNER" == go-run:* ]]; then
    local src="${PROPOSER_RUNNER#go-run:}"
    (cd "$src" && go run ./cmd/curio-proposer \
      -town-root "$TOWN_ROOT" --emit-digest "$DIGEST_PATH")
  else
    "$PROPOSER_RUNNER" -town-root "$TOWN_ROOT" --emit-digest "$DIGEST_PATH"
  fi
}

digest_out=$(emit_digest 2>&1) || {
  rc=$?
  log "ERROR: curio-proposer --emit-digest failed (exit $rc)"
  log "  output: $(head -n5 <<<"$digest_out" | tr '\n' ' ')"
  record_receipt "failure" "digest render failed" \
    "curio-proposer --emit-digest ${DIGEST_PATH} failed (exit ${rc}).

Output (first 30 lines):
$(head -n30 <<<"$digest_out")"
  exit 1
}

# Kill-switch parity: curio-proposer ALSO gates on llm.enabled and writes NO file
# when the lane is off. We already checked the switch above, but if for any
# reason the digest is absent, there is nothing to sling — skip gracefully
# rather than slinging a polecat at a missing file.
if [[ ! -s "$DIGEST_PATH" ]]; then
  log "digest absent/empty at ${DIGEST_PATH} after emit — nothing to dispatch, skipping"
  record_receipt "skipped" "no digest emitted" \
    "curio-proposer --emit-digest produced no (or an empty) file at ${DIGEST_PATH}.
The lane is configured on but the proposer emitted nothing this run; there is
no digest to hand a polecat. Skipping.

Proposer output:
${digest_out}"
  exit 0
fi

log "digest rendered: ${DIGEST_PATH} ($(wc -c <"$DIGEST_PATH") bytes)"

# --- 4b. Sling the Retrospect polecat ----------------------------------------
#
# Positional shape: gt sling <formula> <rig>. digest_path is the SAME
# host-shared path we just wrote — the path-contract the formula's step-1 read
# depends on. max_proposals forwards the per-run cap.

log "slinging ${FORMULA} to ${TARGET_RIG} (digest_path=${DIGEST_PATH}, max_proposals=${MAX_PROPOSALS})"

sling_out=$(gt sling "$FORMULA" "$TARGET_RIG" \
  --create \
  --var "digest_path=$DIGEST_PATH" \
  --var "max_proposals=$MAX_PROPOSALS" \
  2>&1) || {
  rc=$?
  log "ERROR: gt sling failed (exit $rc)"
  log "  output: $(head -n5 <<<"$sling_out" | tr '\n' ' ')"
  record_receipt "failure" "sling failed" \
    "gt sling ${FORMULA} ${TARGET_RIG} --create --var digest_path=${DIGEST_PATH} --var max_proposals=${MAX_PROPOSALS}
exit code: ${rc}

Output (first 30 lines):
$(head -n30 <<<"$sling_out")"
  exit 1
}

log "sling output:"
log "$sling_out"

# Extract the wisp/bead ID for the receipt, if present in the output.
slung_id=$(grep -oE '\b[a-z0-9]+-(wisp-)?[a-z0-9]+\b' <<<"$sling_out" | head -n1 || true)

# --- 5. Record success receipt -----------------------------------------------

record_receipt "success" "slung ${FORMULA}" \
  "Dispatched ${FORMULA} to ${TARGET_RIG}.
digest_path: ${DIGEST_PATH}
max_proposals: ${MAX_PROPOSALS}
open curio-proposal beads at dispatch: ${OPEN_PROPOSALS} (ceiling ${PROPOSAL_CEILING})
slung_id: ${slung_id:-(unknown)}

Sling output:
${sling_out}"

log "done"
exit 0

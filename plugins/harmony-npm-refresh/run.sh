#!/usr/bin/env bash
# harmony-npm-refresh/run.sh — Refresh Amazon Harmony CodeArtifact npm token.
#
# CodeArtifact tokens expire every 22 hours. When expired, npm install
# returns E401 and blocks main_branch_test for every Node-based rig.
# This plugin runs `harmony npm` on an 18-hour cadence so the token stays
# fresh ahead of expiry.
#
# Midway auth is a prerequisite: `harmony npm` calls into Amazon's
# federated auth, which requires a live Midway/Kerberos session. If
# Midway is expired the plugin escalates HIGH — only a human with a
# YubiKey/FIDO2 can run `mwinit`.
#
# Usage: ./run.sh

set -euo pipefail

log() { echo "[harmony-npm-refresh] $*"; }

# Record a plugin-run receipt for dashboards/digests.
# $1 = result (success|failure|warning), $2 = summary
record_run() {
  local result="$1" summary="$2"
  bd create "harmony-npm-refresh: $summary" -t chore --ephemeral \
    -l "type:plugin-run,plugin:harmony-npm-refresh,result:$result" \
    --silent 2>/dev/null || true
}

# --- Preflight: harmony CLI -------------------------------------------------

if ! command -v harmony >/dev/null 2>&1; then
  log "ERROR: 'harmony' not on PATH. Cannot refresh CodeArtifact token."
  record_run failure "harmony CLI missing from PATH"
  gt escalate "harmony-npm-refresh: 'harmony' CLI missing" \
    --severity high \
    --source "plugin:harmony-npm-refresh" \
    --reason "The 'harmony' CLI is not on PATH; CodeArtifact token cannot be refreshed. Operator must install Harmony CLI (https://builderhub.corp.amazon.com/docs/codeartifact/user-guide/)." \
    2>/dev/null || true
  exit 1
fi

# --- Preflight: Midway auth -------------------------------------------------

# `mwinit -l` lists active Midway cookies. When Midway has expired it prints
# nothing (or an error) on stdout. `harmony npm` will fail without it and
# we can't refresh Midway non-interactively.
if command -v mwinit >/dev/null 2>&1; then
  MW_OUT=$(mwinit -l 2>&1 || true)
  if [[ -z "${MW_OUT// }" ]] || echo "$MW_OUT" | grep -qiE "no (valid )?(cookies|credentials)|expired|not found"; then
    log "ERROR: Midway auth appears expired or missing."
    log "mwinit -l output: ${MW_OUT:-<empty>}"
    record_run failure "Midway auth expired — cannot refresh without human"
    gt escalate "harmony-npm-refresh: Midway auth expired" \
      --severity high \
      --source "plugin:harmony-npm-refresh" \
      --reason "Midway session is expired or missing; 'harmony npm' cannot authenticate. A human must run 'mwinit' (YubiKey/FIDO2 required) to restore auth. Until then CodeArtifact token will not refresh and npm install will break in Node-based rigs." \
      2>/dev/null || true
    exit 1
  fi
  log "Midway auth OK"
else
  # mwinit not installed — may be a non-Amazon dev box. Continue and let
  # `harmony npm` surface the real error.
  log "WARN: 'mwinit' not on PATH; skipping Midway preflight"
fi

# --- Refresh CodeArtifact token --------------------------------------------

log "Running 'harmony npm' to refresh CodeArtifact token..."

# Capture output so we can include it in escalation if needed. `harmony npm`
# configures ~/.npmrc and mints a fresh 22h auth token.
if HARMONY_OUT=$(harmony npm 2>&1); then
  log "harmony npm succeeded"
  echo "$HARMONY_OUT" | sed 's/^/  /'
else
  RC=$?
  log "ERROR: 'harmony npm' exited $RC"
  echo "$HARMONY_OUT" | sed 's/^/  /'
  record_run failure "harmony npm failed (exit $RC)"
  # Truncate output to keep escalation reason readable.
  REASON_TAIL=$(echo "$HARMONY_OUT" | tail -5 | tr '\n' ' ' | cut -c1-500)
  gt escalate "harmony-npm-refresh: 'harmony npm' failed (exit $RC)" \
    --severity high \
    --source "plugin:harmony-npm-refresh" \
    --reason "harmony npm exited $RC. Tail of output: $REASON_TAIL" \
    2>/dev/null || true
  exit 1
fi

# --- Verify the new token actually works -----------------------------------

# `npm ping` hits the configured registry and prints a PONG line. A fresh
# token should respond in <1s from *.codeartifact.*.amazonaws.com.
log "Verifying token via 'npm ping'..."
if NPM_PING=$(npm ping 2>&1); then
  echo "$NPM_PING" | sed 's/^/  /'
  if echo "$NPM_PING" | grep -qi "codeartifact"; then
    log "npm ping hit CodeArtifact — token is live"
  else
    log "WARN: npm ping succeeded but registry doesn't look like CodeArtifact"
    log "      (got: $(echo "$NPM_PING" | grep -i PING | head -1))"
    record_run warning "npm ping succeeded but hit non-CodeArtifact registry"
    gt escalate "harmony-npm-refresh: npm registry is not CodeArtifact" \
      --severity medium \
      --source "plugin:harmony-npm-refresh" \
      --reason "After 'harmony npm', npm ping did not report a codeartifact.*.amazonaws.com URL. Something (env var HARMONY_NPM_DISABLE, custom ~/.npmrc, or project-local .npmrc) is overriding the registry. Token refresh succeeded but may not be used by subsequent npm installs." \
      2>/dev/null || true
    # Not fatal — the token IS refreshed, just the registry config is off.
  fi
else
  RC=$?
  log "ERROR: 'npm ping' failed (exit $RC)"
  echo "$NPM_PING" | sed 's/^/  /'
  record_run failure "npm ping failed (exit $RC) after token refresh"
  REASON_TAIL=$(echo "$NPM_PING" | tail -5 | tr '\n' ' ' | cut -c1-500)
  gt escalate "harmony-npm-refresh: npm ping failed after token refresh" \
    --severity high \
    --source "plugin:harmony-npm-refresh" \
    --reason "harmony npm succeeded but npm ping failed (exit $RC). Registry may be unreachable. Tail: $REASON_TAIL" \
    2>/dev/null || true
  exit 1
fi

# --- Success ---------------------------------------------------------------

SUMMARY="CodeArtifact token refreshed ($(date -u +%Y-%m-%dT%H:%MZ))"
log "$SUMMARY"
record_run success "$SUMMARY"

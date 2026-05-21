#!/usr/bin/env bash
#
# Apply (or update) the auto-test-pr branch-protection ruleset on a target
# GitHub repository. This implements Phase 0 task 13 of the Auto-Test-PR
# design (R11 / C-SEC-6): only the cycle-agent / Refinery service identity
# may push to refs/heads/auto-test/*/*.
#
# Usage:
#   templates/auto-test-pr/apply-branch-protection.sh <owner/repo> [bypass-actor-id]
#
# Arguments:
#   <owner/repo>          Required. Target GitHub repository (e.g. gagecane/gastown).
#   [bypass-actor-id]     Optional. Numeric actor_id to allow as the bypass actor.
#                         If omitted, defaults to the "admin" RepositoryRole
#                         (actor_id=5), which matches v1's single-identity model
#                         where the rig owner IS the cycle-agent.
#                         For multi-rig v2, pass the rig's dedicated service
#                         Integration / DeployKey actor_id.
#
# Behavior:
#   - Idempotent: if the ruleset named "auto-test-pr-branch-namespace" already
#     exists on the repo, this script PATCHes it; otherwise it POSTs a new one.
#   - Verifies the rule is "active" after apply.
#   - Exits non-zero on any failure.
#
# Requires: gh (>= 2.30) authenticated with admin scope on the target repo.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RULESET_TEMPLATE="${SCRIPT_DIR}/branch-protection-ruleset.json"
RULESET_NAME="auto-test-pr-branch-namespace"

usage() {
  sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 1
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" || $# -lt 1 ]]; then
  usage
fi

REPO="${1}"
BYPASS_ACTOR_ID="${2:-5}"

if [[ ! -f "${RULESET_TEMPLATE}" ]]; then
  echo "FATAL: ruleset template not found at ${RULESET_TEMPLATE}" >&2
  exit 2
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "FATAL: gh CLI is required but not installed" >&2
  exit 2
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "FATAL: jq is required but not installed" >&2
  exit 2
fi

# Substitute the bypass actor_id into a temp payload.
PAYLOAD="$(mktemp)"
trap 'rm -f "${PAYLOAD}"' EXIT
jq --argjson actor_id "${BYPASS_ACTOR_ID}" \
  '.bypass_actors[0].actor_id = $actor_id' \
  "${RULESET_TEMPLATE}" > "${PAYLOAD}"

# Look up an existing ruleset of this name on the repo.
EXISTING_ID="$(gh api "repos/${REPO}/rulesets" --jq \
  ".[] | select(.name == \"${RULESET_NAME}\") | .id" 2>/dev/null || true)"

if [[ -n "${EXISTING_ID}" ]]; then
  echo "==> Updating existing ruleset id=${EXISTING_ID} on ${REPO}"
  gh api "repos/${REPO}/rulesets/${EXISTING_ID}" \
    -X PUT \
    --input "${PAYLOAD}" >/dev/null
else
  echo "==> Creating new ruleset on ${REPO}"
  gh api "repos/${REPO}/rulesets" \
    -X POST \
    --input "${PAYLOAD}" >/dev/null
fi

# Verify.
APPLIED="$(gh api "repos/${REPO}/rulesets" --jq \
  ".[] | select(.name == \"${RULESET_NAME}\") | {id, enforcement, target}")"
if [[ -z "${APPLIED}" ]]; then
  echo "FATAL: ruleset ${RULESET_NAME} not found on ${REPO} after apply" >&2
  exit 3
fi

ENFORCEMENT="$(echo "${APPLIED}" | jq -r '.enforcement')"
if [[ "${ENFORCEMENT}" != "active" ]]; then
  echo "FATAL: ruleset enforcement is '${ENFORCEMENT}', expected 'active'" >&2
  exit 4
fi

echo "==> Applied:"
echo "${APPLIED}" | jq .
echo
echo "Verify by listing all rulesets on ${REPO}:"
echo "  gh api repos/${REPO}/rulesets"

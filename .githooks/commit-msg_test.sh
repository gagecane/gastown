#!/bin/bash
# Test suite for the commit-msg conventional-commits hook.
#
# Usage: bash .githooks/commit-msg_test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOOK="$SCRIPT_DIR/commit-msg"
PASS=0
FAIL=0
TMPDIR=""

cleanup() {
  cd /tmp
  if [[ -n "$TMPDIR" && -d "$TMPDIR" ]]; then
    rm -rf "$TMPDIR"
  fi
  TMPDIR=""
}
trap cleanup EXIT

setup() {
  TMPDIR=$(mktemp -d)
}

run_hook() {
  # $1 = commit message content (first arg, possibly multiline)
  # $2 (optional) = env var assignment like "GT_SKIP_COMMIT_MSG=1"
  local msg=$1 env_prefix=${2:-}
  local msgfile="$TMPDIR/COMMIT_EDITMSG.$$"
  printf '%s' "$msg" > "$msgfile"
  if [[ -n "$env_prefix" ]]; then
    env $env_prefix bash "$HOOK" "$msgfile"
  else
    bash "$HOOK" "$msgfile"
  fi
  local rc=$?
  rm -f "$msgfile"
  return $rc
}

assert_pass() {
  local name=$1 msg=$2 env_prefix=${3:-}
  if run_hook "$msg" "$env_prefix" >/dev/null 2>&1; then
    echo "PASS: $name"
    PASS=$((PASS+1))
  else
    echo "FAIL: $name — hook rejected a message it should accept"
    echo "       msg: $msg"
    FAIL=$((FAIL+1))
  fi
}

assert_reject() {
  local name=$1 msg=$2
  if run_hook "$msg" >/dev/null 2>&1; then
    echo "FAIL: $name — hook accepted a message it should reject"
    echo "       msg: $msg"
    FAIL=$((FAIL+1))
  else
    echo "PASS: $name"
    PASS=$((PASS+1))
  fi
}

setup

# --- Valid conventional commits ---
assert_pass "feat" "feat: add sling dispatch guardrails"
assert_pass "fix" "fix: handle nil hook bead"
assert_pass "fix with scope" "fix(sling): handle nil hook bead (gu-abcd)"
assert_pass "feat with scope" "feat(polecat): add capability ledger"
assert_pass "breaking change with !" "refactor(polecat)!: drop legacy identity injection"
assert_pass "docs" "docs: clarify formula resolution order"
assert_pass "test" "test(cmd): cover polecat identity extraction"
assert_pass "chore" "chore: bump go.mod to 1.23"
assert_pass "ci" "ci: pin golangci-lint action"
assert_pass "build" "build(deps): bump charmbracelet/bubbletea"
assert_pass "perf" "perf: cache rig clone lookups"
assert_pass "style" "style: run gofmt"
assert_pass "refactor" "refactor: extract findRigClones"
assert_pass "revert-type" "revert: undo gu-abcd"

# --- Auto-generated / tool-managed messages (should pass) ---
assert_pass "merge commit" "Merge branch 'main' into polecat/chrome/foo"
assert_pass "merge pull request" "Merge pull request #1234 from user/branch"
assert_pass "revert quoted" "Revert \"feat: add thing\""
assert_pass "fixup" "fixup! feat: add thing"
assert_pass "squash" "squash! fix: typo"
assert_pass "amend" "amend! feat: add thing"
assert_pass "bd backup" "bd: backup 2026-03-06 01:17"

# --- Escape hatch ---
assert_pass "GT_SKIP_COMMIT_MSG=1 overrides bad subject" \
  "garbage not conventional" "GT_SKIP_COMMIT_MSG=1"

# --- Comments and blank lines are ignored; body doesn't matter ---
assert_pass "comments stripped" "$(printf '# please enter a commit message\n\nfeat: real subject\n')"
assert_pass "body ignored" "$(printf 'fix(sling): short subject\n\nLonger body\nwith multiple lines.\n')"

# --- Rejections ---
assert_reject "no type" "just a plain message"
assert_reject "unknown type" "wibble: do a thing"
assert_reject "uppercase type" "FIX: do a thing"
assert_reject "missing colon" "feat add thing"
assert_reject "missing space after colon" "feat:add thing"
assert_reject "empty description" "feat: "
assert_reject "empty scope" "feat(): do a thing"
assert_reject "uppercase scope" "feat(Sling): do a thing"

# --- Edge: empty file lets git handle it ---
assert_pass "empty message body" ""

# --- Soft length warning still passes ---
long_subject="feat(really-long-scope): $(printf 'x%.0s' {1..120})"
assert_pass "long subject warns but passes" "$long_subject"

echo ""
echo "Results: $PASS passed, $FAIL failed"
if (( FAIL > 0 )); then
  exit 1
fi
exit 0

#!/bin/bash
# pre-push-check_test.sh — verify scripts/pre-push-check.sh scrubs the env
# variables that would otherwise redirect test git subprocesses onto the
# ambient repo.
#
# Context: bead gu-h2ru. A pre-push hook inherits GIT_DIR / GIT_WORK_TREE
# pointing at the pushing repo and forwards them to `go test`. Tests that
# spawn git via os/exec then accidentally operate on the real repo instead
# of their t.TempDir fixtures. pre-push-check.sh must unset these vars
# before running tests; this test asserts the unset survives future edits.
#
# Usage: bash scripts/pre-push-check_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/pre-push-check.sh"

if [[ ! -f "$SCRIPT" ]]; then
  echo "FAIL: $SCRIPT not found" >&2
  exit 1
fi

PASS=0
FAIL=0

check_unset() {
  local var=$1
  # Match either:
  #   unset ... <VAR> ...
  # allowing VAR to appear anywhere on a multi-line unset statement joined by
  # continuations. We use grep -E with -z to treat the file as a single line.
  if grep -zoE "unset[^#]*\b${var}\b" "$SCRIPT" >/dev/null 2>&1; then
    PASS=$((PASS + 1))
  else
    echo "FAIL: pre-push-check.sh does not unset $var" >&2
    FAIL=$((FAIL + 1))
  fi
}

# Core repo-pointing env vars that let GIT_DIR et al override cmd.Dir.
# If any of these can leak from a pre-push hook into `go test`, a test can
# silently corrupt the real repo. See gu-h2ru.
check_unset GIT_DIR
check_unset GIT_WORK_TREE
check_unset GIT_INDEX_FILE
check_unset GIT_OBJECT_DIRECTORY
check_unset GIT_ALTERNATE_OBJECT_DIRECTORIES
check_unset GIT_COMMON_DIR
check_unset GIT_CEILING_DIRECTORIES
check_unset GIT_NAMESPACE

# Workspace env vars that tests might lean on instead of creating fixtures.
check_unset GT_TOWN_ROOT
check_unset GT_ROOT

# Functional test: run pre-push-check.sh with GIT_DIR set to a fake path in
# a subshell, and capture the env it would use during `go build`. We can't
# easily observe the env mid-script, but we can assert the script exits
# successfully (the early-out for missing `go` keeps this fast) AND that
# there's no leaked GIT_DIR output.
#
# This test runs the script with `go` stubbed out so the actual build gates
# don't execute — we only care about env hygiene up to that point.

functional_test() {
  local stubbed
  stubbed=$(mktemp -d)
  trap "rm -rf $stubbed" RETURN
  # Stub `go` as a no-op that prints its inherited GIT_DIR if any.
  cat > "$stubbed/go" <<'EOF'
#!/bin/bash
if [[ -n "${GIT_DIR:-}" ]]; then
  echo "LEAK: GIT_DIR=$GIT_DIR" >&2
  exit 1
fi
if [[ -n "${GIT_WORK_TREE:-}" ]]; then
  echo "LEAK: GIT_WORK_TREE=$GIT_WORK_TREE" >&2
  exit 1
fi
exit 0
EOF
  chmod +x "$stubbed/go"

  # Run pre-push-check.sh with GIT_DIR and GIT_WORK_TREE set. Use a clean
  # PATH that prefers the stubbed `go`. The script should unset these vars
  # before invoking `go`, so the stub sees a clean env and exits 0.
  local out rc
  out=$(
    PATH="$stubbed:$PATH" \
    GIT_DIR="/should/be/unset" \
    GIT_WORK_TREE="/should/be/unset" \
    GIT_INDEX_FILE="/should/be/unset" \
    GIT_OBJECT_DIRECTORY="/should/be/unset" \
    GIT_COMMON_DIR="/should/be/unset" \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: pre-push-check.sh leaked git env to `go` subprocess:" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1))
  else
    PASS=$((PASS + 1))
  fi
}

# Only run the functional test if we have a real `go` on PATH — otherwise
# pre-push-check.sh short-circuits before the unset matters.
if command -v go >/dev/null 2>&1; then
  functional_test
fi

# --- gu-zy57: GT_SKIP_PREPUSH must require a reason and emit an audit event ---
#
# Prior to gu-zy57 the script honoured GT_SKIP_PREPUSH=1 with no questions
# asked, leaving zero audit trail when CI gates were bypassed. The fix:
#
#   1. GT_SKIP_PREPUSH=1 alone is rejected with a non-zero exit
#   2. GT_SKIP_PREPUSH=1 + GT_SKIP_PREPUSH_REASON=<text> succeeds
#   3. The honoured skip appends one JSON line to .runtime/prepush-skips.jsonl
#
# These tests run the script in a temp git repo so the audit file lands in
# an isolated location instead of polluting the real repo's runtime dir.

skip_audit_test() {
  local repo
  repo=$(mktemp -d)
  trap "rm -rf $repo" RETURN

  # Minimal git repo with one commit so `git rev-parse HEAD` succeeds inside
  # the script's audit-event emitter.
  (
    cd "$repo"
    git init -q
    git config user.email "test@example.com"
    git config user.name "test"
    git commit --allow-empty -q -m "init"
  )

  # Case 1: skip without REASON must be rejected (exit non-zero, no audit line)
  local rc out
  out=$(GT_SKIP_PREPUSH=1 bash -c "cd $repo && bash $SCRIPT" 2>&1) && rc=0 || rc=$?
  if [[ $rc -eq 0 ]]; then
    echo "FAIL: GT_SKIP_PREPUSH=1 without REASON should be rejected, but script exited 0" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1))
  else
    PASS=$((PASS + 1))
  fi
  if [[ -f "$repo/.runtime/prepush-skips.jsonl" ]]; then
    echo "FAIL: rejected skip should NOT write an audit event" >&2
    FAIL=$((FAIL + 1))
  else
    PASS=$((PASS + 1))
  fi

  # Case 2: skip WITH REASON must succeed and append a JSON audit line
  out=$(GT_SKIP_PREPUSH=1 GT_SKIP_PREPUSH_REASON="pre-verified" \
        bash -c "cd $repo && bash $SCRIPT" 2>&1) && rc=0 || rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: GT_SKIP_PREPUSH=1 + REASON should succeed, exit=$rc" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1))
  else
    PASS=$((PASS + 1))
  fi
  local audit="$repo/.runtime/prepush-skips.jsonl"
  if [[ ! -s "$audit" ]]; then
    echo "FAIL: audit file $audit was not written" >&2
    FAIL=$((FAIL + 1))
  else
    if grep -q '"reason":"pre-verified"' "$audit" && \
       grep -q '"ts":"' "$audit" && \
       grep -q '"sha":"' "$audit"; then
      PASS=$((PASS + 1))
    else
      echo "FAIL: audit line missing expected fields:" >&2
      cat "$audit" >&2
      FAIL=$((FAIL + 1))
    fi
  fi

  # Case 3: a second honoured skip APPENDS rather than overwrites
  GT_SKIP_PREPUSH=1 GT_SKIP_PREPUSH_REASON="emergency: gu-test" \
    bash -c "cd $repo && bash $SCRIPT" >/dev/null 2>&1 || true
  local lines
  lines=$(wc -l < "$audit")
  if [[ $lines -ne 2 ]]; then
    echo "FAIL: audit file should have 2 lines after 2 skips, got $lines" >&2
    cat "$audit" >&2
    FAIL=$((FAIL + 1))
  else
    PASS=$((PASS + 1))
  fi
}

skip_audit_test

echo ""
echo "pre-push-check_test.sh: $PASS passed, $FAIL failed"
if [[ $FAIL -gt 0 ]]; then
  exit 1
fi

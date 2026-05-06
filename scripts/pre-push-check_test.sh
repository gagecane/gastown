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

echo ""
echo "pre-push-check_test.sh: $PASS passed, $FAIL failed"
if [[ $FAIL -gt 0 ]]; then
  exit 1
fi

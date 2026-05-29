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

# --- Fast/slow gate split (gu-7f0v) --------------------------------------
#
# `gt done --pre-verified` sets GT_SKIP_PREPUSH=1 to avoid re-running tests
# the polecat already ran. The script must still enforce FAST gates
# (build/vet/gofmt) under that env, because they're cheap and catch landing
# failures that pre-verification commonly misses (e.g. trailing-newline gofmt
# regressions on 2026-05-29 — bffac8f7, ced30a88).
#
# These tests stub `go` and `gofmt` so we can observe which gates ran
# without depending on a real Go toolchain or a buildable workspace.

# Set up an isolated stub dir and a temp git repo, run the script, and
# return its exit code + stdout/stderr in globals OUT and RC.
run_with_stubs() {
  local stub_go=$1   # path to go stub script body
  local stub_fmt=$2  # path to gofmt stub script body
  local skip_slow=$3 # "1" to set GT_SKIP_PREPUSH=1, else "0"

  local stubdir tmprepo
  stubdir=$(mktemp -d)
  tmprepo=$(mktemp -d)

  cat > "$stubdir/go" <<EOF
#!/bin/bash
$stub_go
EOF
  chmod +x "$stubdir/go"

  cat > "$stubdir/gofmt" <<EOF
#!/bin/bash
$stub_fmt
EOF
  chmod +x "$stubdir/gofmt"

  ( cd "$tmprepo" && git init -q && git commit -q --allow-empty -m init ) >/dev/null 2>&1

  local rc=0
  OUT=$(
    cd "$tmprepo" && \
    PATH="$stubdir:$PATH" \
    GT_SKIP_PREPUSH="$skip_slow" \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  RC=$rc

  rm -rf "$stubdir" "$tmprepo"
}

# Test: under GT_SKIP_PREPUSH=1, fast gates still run (build/vet/gofmt invoked).
test_fast_gates_run_under_skip() {
  run_with_stubs \
    'echo "go-called: $*" >&2; exit 0' \
    'exit 0' \
    1
  if [[ $RC -ne 0 ]]; then
    echo "FAIL: GT_SKIP_PREPUSH=1 should pass when fast gates pass (got rc=$RC)" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    return
  fi
  if ! echo "$OUT" | grep -q "go-called: build"; then
    echo "FAIL: GT_SKIP_PREPUSH=1 did not invoke 'go build'" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    return
  fi
  if ! echo "$OUT" | grep -q "go-called: vet"; then
    echo "FAIL: GT_SKIP_PREPUSH=1 did not invoke 'go vet'" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    return
  fi
  PASS=$((PASS + 1))
}

# Test: under GT_SKIP_PREPUSH=1, slow gate (go test) is skipped.
test_slow_gate_skipped_under_skip() {
  run_with_stubs \
    'echo "go-called: $*" >&2; exit 0' \
    'exit 0' \
    1
  if echo "$OUT" | grep -q "go-called: test"; then
    echo "FAIL: GT_SKIP_PREPUSH=1 should NOT invoke 'go test' but did" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    return
  fi
  PASS=$((PASS + 1))
}

# Test: gofmt failure rejects the push EVEN under GT_SKIP_PREPUSH=1.
# This is the gu-7f0v acceptance criterion — pre-verified must not bypass gofmt.
test_gofmt_blocks_under_skip() {
  run_with_stubs \
    'exit 0' \
    'echo "bad.go"; exit 0' \
    1
  if [[ $RC -eq 0 ]]; then
    echo "FAIL: GT_SKIP_PREPUSH=1 with unformatted file should reject (got rc=0)" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    return
  fi
  if ! echo "$OUT" | grep -qi "gofmt"; then
    echo "FAIL: rejection message did not mention gofmt" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    return
  fi
  PASS=$((PASS + 1))
}

# Test: build failure rejects the push EVEN under GT_SKIP_PREPUSH=1.
test_build_blocks_under_skip() {
  run_with_stubs \
    'if [[ "$1" == "build" ]]; then echo "build broken" >&2; exit 1; fi; exit 0' \
    'exit 0' \
    1
  if [[ $RC -eq 0 ]]; then
    echo "FAIL: GT_SKIP_PREPUSH=1 with broken build should reject (got rc=0)" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    return
  fi
  PASS=$((PASS + 1))
}

# Test: without GT_SKIP_PREPUSH, slow gate runs.
test_slow_gate_runs_by_default() {
  run_with_stubs \
    'echo "go-called: $*" >&2; exit 0' \
    'exit 0' \
    0
  if [[ $RC -ne 0 ]]; then
    echo "FAIL: default run should pass when all stubs pass (got rc=$RC)" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    return
  fi
  if ! echo "$OUT" | grep -q "go-called: test"; then
    echo "FAIL: default run did not invoke 'go test'" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    return
  fi
  PASS=$((PASS + 1))
}

# Only run the functional test if we have a real `go` on PATH — otherwise
# pre-push-check.sh short-circuits before the unset matters.
if command -v go >/dev/null 2>&1; then
  functional_test
fi

# The fast/slow split tests use stubbed `go` and `gofmt`, so they don't
# require a real toolchain — but they do require git.
if command -v git >/dev/null 2>&1; then
  test_fast_gates_run_under_skip
  test_slow_gate_skipped_under_skip
  test_gofmt_blocks_under_skip
  test_build_blocks_under_skip
  test_slow_gate_runs_by_default
fi

echo ""
echo "pre-push-check_test.sh: $PASS passed, $FAIL failed"
if [[ $FAIL -gt 0 ]]; then
  exit 1
fi

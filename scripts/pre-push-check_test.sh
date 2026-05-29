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
#
# When skip_slow=1, sets GT_SKIP_PREPUSH_REASON="pre-verified" alongside
# GT_SKIP_PREPUSH=1 — the production hook now requires a reason (gu-zy57).
# Tests that need to exercise the reason-missing reject path use
# run_with_stubs_no_reason instead.
run_with_stubs() {
  local stub_go=$1   # path to go stub script body
  local stub_fmt=$2  # path to gofmt stub script body
  local skip_slow=$3 # "1" to set GT_SKIP_PREPUSH=1 + REASON, else "0"

  local stubdir tmprepo
  stubdir=$(mktemp -d)
  tmprepo=$(mktemp -d)
  # Track for inspection by audit tests. Tests that need the audit file MUST
  # NOT call rm -rf on this path — they're cleaned up at end of run.
  LAST_TMPREPO=$tmprepo
  LAST_STUBDIR=$stubdir

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

  ( cd "$tmprepo" && git init -q && \
      git config user.email "test@example.com" && \
      git config user.name "test" && \
      git commit -q --allow-empty -m init ) >/dev/null 2>&1

  local reason_env=""
  if [[ "$skip_slow" == "1" ]]; then
    reason_env='GT_SKIP_PREPUSH_REASON=pre-verified'
  fi

  local rc=0
  OUT=$(
    cd "$tmprepo" && \
    PATH="$stubdir:$PATH" \
    env GT_SKIP_PREPUSH="$skip_slow" $reason_env \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  RC=$rc
}

# Cleanup helper: tests that don't need to inspect the audit file can call
# this immediately; audit tests call it after their assertions.
cleanup_last_run() {
  [[ -n "${LAST_STUBDIR:-}" ]] && rm -rf "$LAST_STUBDIR"
  [[ -n "${LAST_TMPREPO:-}" ]] && rm -rf "$LAST_TMPREPO"
  LAST_STUBDIR=""
  LAST_TMPREPO=""
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
    cleanup_last_run
    return
  fi
  if ! echo "$OUT" | grep -q "go-called: build"; then
    echo "FAIL: GT_SKIP_PREPUSH=1 did not invoke 'go build'" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    cleanup_last_run
    return
  fi
  if ! echo "$OUT" | grep -q "go-called: vet"; then
    echo "FAIL: GT_SKIP_PREPUSH=1 did not invoke 'go vet'" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    cleanup_last_run
    return
  fi
  PASS=$((PASS + 1))
  cleanup_last_run
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
    cleanup_last_run
    return
  fi
  PASS=$((PASS + 1))
  cleanup_last_run
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
    cleanup_last_run
    return
  fi
  if ! echo "$OUT" | grep -qi "gofmt"; then
    echo "FAIL: rejection message did not mention gofmt" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    cleanup_last_run
    return
  fi
  PASS=$((PASS + 1))
  cleanup_last_run
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
    cleanup_last_run
    return
  fi
  PASS=$((PASS + 1))
  cleanup_last_run
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
    cleanup_last_run
    return
  fi
  if ! echo "$OUT" | grep -q "go-called: test"; then
    echo "FAIL: default run did not invoke 'go test'" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    cleanup_last_run
    return
  fi
  PASS=$((PASS + 1))
  cleanup_last_run
}

# --- gu-zy57: REASON requirement + audit event ---------------------------
#
# When GT_SKIP_PREPUSH=1 is set without GT_SKIP_PREPUSH_REASON, the script
# must reject the push outright (no fast gates, no audit line) — that's the
# misconfiguration this guard exists to catch. When REASON is set, the
# script must honour the skip AND append a JSON line to
# .runtime/prepush-skips.jsonl recording who/why/what.

# Test: GT_SKIP_PREPUSH=1 without REASON is rejected, and no audit line is
# written. Run directly (not via run_with_stubs which sets a default REASON)
# so we exercise the missing-reason path.
test_skip_without_reason_rejected() {
  local stubdir tmprepo
  stubdir=$(mktemp -d)
  tmprepo=$(mktemp -d)
  cat > "$stubdir/go" <<'EOF'
#!/bin/bash
exit 0
EOF
  chmod +x "$stubdir/go"
  cat > "$stubdir/gofmt" <<'EOF'
#!/bin/bash
exit 0
EOF
  chmod +x "$stubdir/gofmt"
  ( cd "$tmprepo" && git init -q && \
      git config user.email "test@example.com" && \
      git config user.name "test" && \
      git commit -q --allow-empty -m init ) >/dev/null 2>&1

  local rc=0 out
  out=$(
    cd "$tmprepo" && \
    PATH="$stubdir:$PATH" \
    env GT_SKIP_PREPUSH=1 \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}

  if [[ $rc -eq 0 ]]; then
    echo "FAIL: GT_SKIP_PREPUSH=1 without REASON should be rejected (got rc=0)" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1))
    rm -rf "$stubdir" "$tmprepo"
    return
  fi
  if ! echo "$out" | grep -q "GT_SKIP_PREPUSH_REASON"; then
    echo "FAIL: rejection message should mention GT_SKIP_PREPUSH_REASON" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1))
    rm -rf "$stubdir" "$tmprepo"
    return
  fi
  if [[ -f "$tmprepo/.runtime/prepush-skips.jsonl" ]]; then
    echo "FAIL: rejected skip should NOT write an audit event" >&2
    cat "$tmprepo/.runtime/prepush-skips.jsonl" >&2
    FAIL=$((FAIL + 1))
    rm -rf "$stubdir" "$tmprepo"
    return
  fi
  PASS=$((PASS + 1))
  rm -rf "$stubdir" "$tmprepo"
}

# Test: GT_SKIP_PREPUSH=1 + REASON appends a JSON audit line and the line
# contains the expected fields (ts, actor, reason, branch, sha).
test_skip_with_reason_audited() {
  run_with_stubs \
    'exit 0' \
    'exit 0' \
    1

  if [[ $RC -ne 0 ]]; then
    echo "FAIL: skip with REASON should succeed when fast gates pass (rc=$RC)" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    cleanup_last_run
    return
  fi

  local audit="$LAST_TMPREPO/.runtime/prepush-skips.jsonl"
  if [[ ! -s "$audit" ]]; then
    echo "FAIL: audit file $audit was not written" >&2
    FAIL=$((FAIL + 1))
    cleanup_last_run
    return
  fi
  if ! grep -q '"reason":"pre-verified"' "$audit"; then
    echo "FAIL: audit line missing reason=pre-verified:" >&2
    cat "$audit" >&2
    FAIL=$((FAIL + 1))
    cleanup_last_run
    return
  fi
  for field in '"ts":"' '"actor":"' '"branch":"' '"sha":"'; do
    if ! grep -q "$field" "$audit"; then
      echo "FAIL: audit line missing field $field:" >&2
      cat "$audit" >&2
      FAIL=$((FAIL + 1))
      cleanup_last_run
      return
    fi
  done
  PASS=$((PASS + 1))
  cleanup_last_run
}

# Test: a second honoured skip APPENDS rather than overwrites the audit file.
test_skip_audit_appends() {
  local stubdir tmprepo
  stubdir=$(mktemp -d)
  tmprepo=$(mktemp -d)
  cat > "$stubdir/go" <<'EOF'
#!/bin/bash
exit 0
EOF
  chmod +x "$stubdir/go"
  cat > "$stubdir/gofmt" <<'EOF'
#!/bin/bash
exit 0
EOF
  chmod +x "$stubdir/gofmt"
  ( cd "$tmprepo" && git init -q && \
      git config user.email "test@example.com" && \
      git config user.name "test" && \
      git commit -q --allow-empty -m init ) >/dev/null 2>&1

  local r=0
  ( cd "$tmprepo" && \
    PATH="$stubdir:$PATH" \
    env GT_SKIP_PREPUSH=1 GT_SKIP_PREPUSH_REASON="first" \
    bash "$SCRIPT" >/dev/null 2>&1 ) || r=$?
  ( cd "$tmprepo" && \
    PATH="$stubdir:$PATH" \
    env GT_SKIP_PREPUSH=1 GT_SKIP_PREPUSH_REASON="second" \
    bash "$SCRIPT" >/dev/null 2>&1 ) || r=$?

  local audit="$tmprepo/.runtime/prepush-skips.jsonl"
  local lines
  lines=$(wc -l < "$audit" 2>/dev/null || echo 0)
  if [[ "$lines" -ne 2 ]]; then
    echo "FAIL: audit should have 2 lines after 2 honoured skips, got $lines" >&2
    cat "$audit" 2>/dev/null >&2
    FAIL=$((FAIL + 1))
    rm -rf "$stubdir" "$tmprepo"
    return
  fi
  if ! grep -q '"reason":"first"' "$audit" || ! grep -q '"reason":"second"' "$audit"; then
    echo "FAIL: both reasons should be present in audit log:" >&2
    cat "$audit" >&2
    FAIL=$((FAIL + 1))
    rm -rf "$stubdir" "$tmprepo"
    return
  fi
  PASS=$((PASS + 1))
  rm -rf "$stubdir" "$tmprepo"
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
  # gu-zy57: REASON requirement + audit event
  test_skip_without_reason_rejected
  test_skip_with_reason_audited
  test_skip_audit_appends
fi

echo ""
echo "pre-push-check_test.sh: $PASS passed, $FAIL failed"
if [[ $FAIL -gt 0 ]]; then
  exit 1
fi

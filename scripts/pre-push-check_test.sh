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

# Neutralize the ambient town root so the gate-slot concurrency cap (gs-orsm)
# is a no-op in tests that don't explicitly exercise it. Without this, a dev
# (or agent) running this suite from a shell with GT_TOWN_ROOT set would engage
# the REAL host-wide semaphore — acquiring real slots and, if both are held by
# a live gt-done gate run, blocking the suite for the full wait. The cap tests
# below set GT_TOWN_ROOT explicitly in their own `env` line, overriding this.
export GT_TOWN_ROOT=

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

  # Sterile PATH: only the stubs + coreutils/git, NOT the developer's full
  # PATH. The full PATH leaks a real golangci-lint (e.g. ~/.local/bin) into a
  # suite that documents itself as using "only go and gofmt stubs" — the real
  # linter then runs against the stub `go` and chokes. GT_PREPUSH_PROBE_DIRS is
  # pinned to the stubdir so the script's standard-dir probe (gs-812) doesn't
  # re-introduce /usr/local/go/bin or ~/.local/bin.
  local rc=0
  OUT=$(
    cd "$tmprepo" && \
    env PATH="$stubdir:/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$stubdir" \
    GT_SKIP_PREPUSH="$skip_slow" $reason_env \
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

# gu-lint-fastgate: when golangci-lint is NOT on PATH, the script must
# print a "not installed locally — skipping" warning and continue (CI is
# the authoritative gate). No push rejection.
test_lint_gate_skipped_when_not_installed() {
  # run_with_stubs uses an isolated PATH containing only `go` and `gofmt`
  # stubs — golangci-lint is absent there, so the command -v check fires
  # the skip path. We then assert the script ran to completion AND emitted
  # the expected warning string.
  run_with_stubs \
    'echo "go-called: $*" >&2; exit 0' \
    'exit 0' \
    1
  if [[ $RC -ne 0 ]]; then
    echo "FAIL: missing golangci-lint should not reject push (got rc=$RC)" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    cleanup_last_run
    return
  fi
  if ! echo "$OUT" | grep -qi "golangci-lint not installed"; then
    echo "FAIL: missing golangci-lint should print the install hint warning" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1))
    cleanup_last_run
    return
  fi
  PASS=$((PASS + 1))
  cleanup_last_run
}

# gu-lint-fastgate: when golangci-lint IS on PATH and it returns non-zero
# (lint findings), the push is rejected EVEN under GT_SKIP_PREPUSH=1. This
# is the symmetry to test_gofmt_blocks_under_skip — fast gates can't be
# bypassed by --pre-verified.
test_lint_gate_blocks_under_skip() {
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
  cat > "$stubdir/golangci-lint" <<'EOF'
#!/bin/bash
echo "internal/foo.go:1:1: simulated lint finding" >&2
exit 1
EOF
  chmod +x "$stubdir/golangci-lint"

  ( cd "$tmprepo" && git init -q && \
      git config user.email "test@example.com" && \
      git config user.name "test" && \
      git commit -q --allow-empty -m init ) >/dev/null 2>&1

  local rc=0
  local out
  out=$(
    cd "$tmprepo" && \
    PATH="$stubdir:$PATH" \
    env GT_SKIP_PREPUSH=1 GT_SKIP_PREPUSH_REASON=pre-verified \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  if [[ $rc -eq 0 ]]; then
    echo "FAIL: lint findings should reject push under GT_SKIP_PREPUSH=1 (got rc=0)" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1))
    rm -rf "$stubdir" "$tmprepo"
    return
  fi
  if ! echo "$out" | grep -qi "golangci-lint"; then
    echo "FAIL: rejection message did not mention golangci-lint" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1))
    rm -rf "$stubdir" "$tmprepo"
    return
  fi
  PASS=$((PASS + 1))
  rm -rf "$stubdir" "$tmprepo"
}

# --- gs-812: fail closed in a Go repo + standard-dir probe ----------------
#
# A Go repo is identified by a go.mod at the repo root. When the toolchain is
# missing there, the gate must BLOCK the push (not silently exit 0) and record
# the block in .runtime/prepush-skips.jsonl. Non-Go checkouts still skip
# gracefully. The standard-dir probe folds tool install dirs onto PATH before
# declaring a tool absent; GT_PREPUSH_PROBE_DIRS overrides the probe list.
#
# These tests run with a STERILE PATH (no developer PATH) and pin
# GT_PREPUSH_PROBE_DIRS so a real go/golangci-lint can't leak in.

# Helper: make a temp git repo. Pass "go" to add a go.mod (Go repo), anything
# else for a non-Go checkout. Echoes the repo path.
make_test_repo() {
  local kind=$1
  local repo
  repo=$(mktemp -d)
  ( cd "$repo" && git init -q && \
      git config user.email "test@example.com" && \
      git config user.name "test"
    if [[ "$kind" == "go" ]]; then
      printf 'module testmod\n\ngo 1.21\n' > go.mod
      git add go.mod && git commit -q -m init
    else
      git commit -q --allow-empty -m init
    fi ) >/dev/null 2>&1
  printf '%s' "$repo"
}

# Test: 'go' missing in a Go repo blocks the push and writes a BLOCKED audit.
test_go_absent_blocks_in_go_repo() {
  local repo emptydir
  repo=$(make_test_repo go)
  emptydir=$(mktemp -d)   # probe dir with no tools
  local rc=0 out
  out=$(
    cd "$repo" && \
    env PATH="/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$emptydir" \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}
  if [[ $rc -eq 0 ]]; then
    echo "FAIL: missing 'go' in a Go repo should reject the push (got rc=0)" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$repo" "$emptydir"; return
  fi
  local audit="$repo/.runtime/prepush-skips.jsonl"
  if [[ ! -s "$audit" ]] || ! grep -q 'BLOCKED' "$audit"; then
    echo "FAIL: blocked push should write a BLOCKED audit line" >&2
    [[ -f "$audit" ]] && cat "$audit" >&2
    FAIL=$((FAIL + 1)); rm -rf "$repo" "$emptydir"; return
  fi
  PASS=$((PASS + 1)); rm -rf "$repo" "$emptydir"
}

# Test: 'go' missing in a NON-Go checkout skips gracefully (rc=0, no audit).
test_go_absent_skips_in_non_go_repo() {
  local repo emptydir
  repo=$(make_test_repo plain)
  emptydir=$(mktemp -d)
  local rc=0 out
  out=$(
    cd "$repo" && \
    env PATH="/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$emptydir" \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: missing 'go' in a non-Go checkout should skip gracefully (got rc=$rc)" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$repo" "$emptydir"; return
  fi
  if [[ -f "$repo/.runtime/prepush-skips.jsonl" ]]; then
    echo "FAIL: graceful non-Go skip should NOT write an audit line" >&2
    FAIL=$((FAIL + 1)); rm -rf "$repo" "$emptydir"; return
  fi
  PASS=$((PASS + 1)); rm -rf "$repo" "$emptydir"
}

# Test: golangci-lint missing in a Go repo (go present) blocks + audits.
test_lint_absent_blocks_in_go_repo() {
  local repo stubdir
  repo=$(make_test_repo go)
  stubdir=$(mktemp -d)   # go + gofmt present, golangci-lint absent
  printf '#!/bin/bash\nexit 0\n' > "$stubdir/go";    chmod +x "$stubdir/go"
  printf '#!/bin/bash\nexit 0\n' > "$stubdir/gofmt"; chmod +x "$stubdir/gofmt"
  local rc=0 out
  out=$(
    cd "$repo" && \
    env PATH="$stubdir:/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$stubdir" \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}
  if [[ $rc -eq 0 ]]; then
    echo "FAIL: missing golangci-lint in a Go repo should reject the push (got rc=0)" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$repo" "$stubdir"; return
  fi
  if ! echo "$out" | grep -qi "golangci-lint"; then
    echo "FAIL: rejection message should mention golangci-lint" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$repo" "$stubdir"; return
  fi
  local audit="$repo/.runtime/prepush-skips.jsonl"
  if [[ ! -s "$audit" ]] || ! grep -q 'BLOCKED' "$audit"; then
    echo "FAIL: blocked lint gate should write a BLOCKED audit line" >&2
    FAIL=$((FAIL + 1)); rm -rf "$repo" "$stubdir"; return
  fi
  PASS=$((PASS + 1)); rm -rf "$repo" "$stubdir"
}

# Test: the standard-dir probe finds a toolchain that's off the base PATH but
# present in a probed dir — the gate then runs to completion instead of
# blocking. This is the core gs-812 mechanism: "absent" must mean genuinely
# not installed, not merely off this shell's PATH.
test_probe_finds_tools_off_base_path() {
  local repo probedir
  repo=$(make_test_repo go)
  probedir=$(mktemp -d)   # all tools live ONLY here, not on the base PATH
  printf '#!/bin/bash\nexit 0\n' > "$probedir/go";            chmod +x "$probedir/go"
  printf '#!/bin/bash\nexit 0\n' > "$probedir/gofmt";         chmod +x "$probedir/gofmt"
  printf '#!/bin/bash\nexit 0\n' > "$probedir/golangci-lint"; chmod +x "$probedir/golangci-lint"
  local rc=0 out
  out=$(
    cd "$repo" && \
    env PATH="/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$probedir" \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: probe should find tools in GT_PREPUSH_PROBE_DIRS and pass (got rc=$rc)" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$repo" "$probedir"; return
  fi
  PASS=$((PASS + 1)); rm -rf "$repo" "$probedir"
}

# --- gs-orsm: host-wide gate-slot concurrency cap ------------------------
#
# pre-push-check.sh acquires one slot from the shared FlockSemaphore pool
# (<townRoot>/.runtime/locks/gate-slots) before the heavy gates, bounding how
# many full-suite go runs execute at once host-wide. These tests exercise the
# bash flock(1) slot scan with GT_GATE_CONCURRENCY=1 so a single pre-held slot
# fills the pool.

# Helper: build a stubdir (go/gofmt that exit 0) + a town root with a slot dir.
# Echoes "stubdir tmprepo townroot".
make_cap_fixture() {
  local stubdir tmprepo townroot
  stubdir=$(mktemp -d)
  tmprepo=$(mktemp -d)
  townroot=$(mktemp -d)
  printf '#!/bin/bash\nexit 0\n' > "$stubdir/go";    chmod +x "$stubdir/go"
  printf '#!/bin/bash\nexit 0\n' > "$stubdir/gofmt"; chmod +x "$stubdir/gofmt"
  mkdir -p "$townroot/.runtime/locks/gate-slots"
  ( cd "$tmprepo" && git init -q && \
      git config user.email "test@example.com" && \
      git config user.name "test" && \
      git commit -q --allow-empty -m init ) >/dev/null 2>&1
  printf '%s %s %s\n' "$stubdir" "$tmprepo" "$townroot"
}

# Test: when the only slot is already held, the script waits out
# GT_GATE_SLOT_WAIT_SECONDS and then proceeds unthrottled (push still passes),
# printing the "proceeding without the host-wide concurrency cap" notice.
test_gate_slot_cap_proceeds_when_full() {
  local stubdir tmprepo townroot
  read -r stubdir tmprepo townroot < <(make_cap_fixture)
  local slot="$townroot/.runtime/locks/gate-slots/slot-0.flock"
  local ready; ready=$(mktemp -u)

  # Hold the single slot for the duration of the script run.
  ( exec 9>"$slot"; flock 9; : > "$ready"; sleep 30 ) &
  local holder=$!
  # Wait until the holder has actually taken the lock.
  local tries=0
  while [[ ! -f "$ready" && $tries -lt 50 ]]; do sleep 0.1; tries=$((tries + 1)); done

  local rc=0 out
  out=$(
    cd "$tmprepo" && \
    env PATH="$stubdir:/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$stubdir" \
    GT_TOWN_ROOT="$townroot" GT_GATE_CONCURRENCY=1 GT_GATE_SLOT_WAIT_SECONDS=2 \
    GT_SKIP_PREPUSH=1 GT_SKIP_PREPUSH_REASON=pre-verified \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}

  kill "$holder" 2>/dev/null || true
  wait "$holder" 2>/dev/null || true
  rm -f "$ready"

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: full gate pool should proceed unthrottled, not fail the push (got rc=$rc)" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot"; return
  fi
  if ! echo "$out" | grep -qi "proceeding without the host-wide concurrency cap"; then
    echo "FAIL: a full pool past the wait should print the unthrottled-proceed notice" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot"; return
  fi
  PASS=$((PASS + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot"
}

# Test: when a slot is free, the script acquires it (does NOT print the
# unthrottled-proceed notice) and the push passes — and while the gates run the
# slot is genuinely locked, so an external non-blocking flock fails.
test_gate_slot_cap_acquires_when_free() {
  local stubdir tmprepo townroot
  read -r stubdir tmprepo townroot < <(make_cap_fixture)
  local slot="$townroot/.runtime/locks/gate-slots/slot-0.flock"
  local probed; probed=$(mktemp -u)

  # `go` stub blocks on a fifo so we can probe the lock mid-run. Replace the
  # plain stub with one that, on its first call, signals readiness and waits.
  local fifo="$townroot/go-gate.fifo"
  mkfifo "$fifo"
  # Only the FIRST `go` invocation (build) blocks on the fifo so we can probe
  # the lock mid-run; later invocations (vet, ...) return immediately, else
  # they'd block forever on a fifo we only feed once.
  cat > "$stubdir/go" <<EOF
#!/bin/bash
if [[ ! -f "$probed" ]]; then
  echo running > "$probed"
  read -r _ < "$fifo"
fi
exit 0
EOF
  chmod +x "$stubdir/go"

  local rc=0 out
  (
    cd "$tmprepo" && \
    env PATH="$stubdir:/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$stubdir" \
    GT_TOWN_ROOT="$townroot" GT_GATE_CONCURRENCY=1 GT_GATE_SLOT_WAIT_SECONDS=2 \
    GT_SKIP_PREPUSH=1 GT_SKIP_PREPUSH_REASON=pre-verified \
    bash "$SCRIPT" > "$townroot/out.txt" 2>&1
    echo $? > "$townroot/rc.txt"
  ) &
  local runner=$!

  # Wait until a gate is running (slot held), then probe the lock.
  local tries=0
  while [[ ! -f "$probed" && $tries -lt 100 ]]; do sleep 0.1; tries=$((tries + 1)); done
  local lock_busy=1
  if ( exec 9>"$slot"; flock -n 9 ); then
    lock_busy=0   # we got the lock — the script did NOT hold it (bug)
  fi
  # Release the blocked gate stub so the script finishes.
  echo go > "$fifo"
  wait "$runner" 2>/dev/null || true
  out=$(cat "$townroot/out.txt" 2>/dev/null)
  rc=$(cat "$townroot/rc.txt" 2>/dev/null || echo 1)

  if [[ "$lock_busy" -ne 1 ]]; then
    echo "FAIL: while gates run the slot should be locked (external flock -n should fail)" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot" "$probed"; return
  fi
  if [[ "$rc" -ne 0 ]]; then
    echo "FAIL: with a free slot the push should pass (got rc=$rc)" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot" "$probed"; return
  fi
  if echo "$out" | grep -qi "proceeding without the host-wide concurrency cap"; then
    echo "FAIL: with a free slot the script should acquire it, not proceed unthrottled" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot" "$probed"; return
  fi
  PASS=$((PASS + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot" "$probed"
}

# Test (gu-40xsf): the slow gate's child must NOT leak the gate-slot fd to a
# daemonizing grandchild. bash's `exec {fd}>file` is not close-on-exec, so a
# test that backgrounds a long-lived process (historically a tmux server,
# gt-test-sentinel) would inherit the slot fd and pin the flock after the
# script exits — permanently exhausting the slot. This stubs `go` so its
# "test" subcommand spawns a backgrounded child that sleeps well past the
# script's lifetime, then asserts the slot is FREE once the script returns
# (an external non-blocking flock must succeed). Before the fix the leaked
# child held the fd and the flock would fail.
test_gate_slot_cap_no_fd_leak_to_daemon_child() {
  local stubdir tmprepo townroot
  read -r stubdir tmprepo townroot < <(make_cap_fixture)
  local slot="$townroot/.runtime/locks/gate-slots/slot-0.flock"
  local childpid_file="$townroot/daemon.pid"

  # `go test ...` stub: background a child that outlives the script and would
  # inherit any non-cloexec fd. setsid + redirecting std fds to /dev/null
  # daemonizes it like a real tmux server. Record its pid so we can reap it.
  cat > "$stubdir/go" <<EOF
#!/bin/bash
if [[ "\$1" == "test" ]]; then
  setsid bash -c 'sleep 30' </dev/null >/dev/null 2>&1 &
  echo \$! > "$childpid_file"
fi
exit 0
EOF
  chmod +x "$stubdir/go"

  local rc=0 out
  out=$(
    cd "$tmprepo" && \
    env PATH="$stubdir:/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$stubdir" \
    GT_TOWN_ROOT="$townroot" GT_GATE_CONCURRENCY=1 GT_GATE_SLOT_WAIT_SECONDS=2 \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}

  # After the script exits, the slot MUST be free even though the daemon child
  # is still alive — proving the child did not inherit the slot fd.
  local lock_free=0
  if ( exec 9>"$slot"; flock -n 9 ); then
    lock_free=1
  fi

  # Reap the lingering daemon child.
  local cpid; cpid=$(cat "$childpid_file" 2>/dev/null || echo "")
  [[ -n "$cpid" ]] && kill "$cpid" 2>/dev/null

  if [[ "$rc" -ne 0 ]]; then
    echo "FAIL: push should pass with a free slot (got rc=$rc)" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot"; return
  fi
  if [[ "$lock_free" -ne 1 ]]; then
    echo "FAIL: gate slot still held after script exit — daemon child leaked the slot fd (gu-40xsf)" >&2
    echo "$out" >&2; FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot"; return
  fi
  PASS=$((PASS + 1)); rm -rf "$stubdir" "$tmprepo" "$townroot"
}

# Test: with no town root known (no GT_TOWN_ROOT, no mayor/town.json), the cap
# is a silent no-op and the push passes normally.
test_gate_slot_cap_skips_without_town_root() {
  run_with_stubs \
    'echo "go-called: $*" >&2; exit 0' \
    'exit 0' \
    0
  if [[ $RC -ne 0 ]]; then
    echo "FAIL: no town root should skip the cap and pass (got rc=$RC)" >&2
    echo "$OUT" >&2; FAIL=$((FAIL + 1)); cleanup_last_run; return
  fi
  if echo "$OUT" | grep -qi "concurrency cap"; then
    echo "FAIL: no town root should not mention the concurrency cap at all" >&2
    echo "$OUT" >&2; FAIL=$((FAIL + 1)); cleanup_last_run; return
  fi
  PASS=$((PASS + 1)); cleanup_last_run
}

# --- gu-zadrb: slow-gate hard wall-clock group timeout -------------------
#
# The slow gate ('go test ./...') has been observed to hang PAST go's own
# -timeout because a test spawned network children (git-remote-https) that
# go's deadline doesn't reap. The orphaned tree pins the polecat worktree dir,
# blocking post-merge nuke (STUCK_NUKE). The fix runs the slow gate in its own
# process group under a wall-clock timeout and kills the WHOLE group on expiry.
#
# These tests stub `go` so the "test" subcommand hangs (and forks a child that
# outlives its parent), then assert: (1) the push is rejected with the timeout
# message, and (2) no forked child survives the group-kill.

# Test: a hanging slow gate is killed by the wall-clock group timeout and the
# push is rejected (rc != 0, timeout message printed).
test_slow_gate_wall_clock_timeout_rejects() {
  local stubdir tmprepo marker
  stubdir=$(mktemp -d)
  tmprepo=$(mktemp -d)
  marker=$(mktemp -u)   # a path the forked grandchild creates then would keep alive
  # `go`: pass build/vet fast, but the "test" subcommand spawns a long-lived
  # child (writing its PID to $marker) and then hangs — simulating the
  # hang-past-go-timeout failure mode.
  cat > "$stubdir/go" <<EOF
#!/bin/bash
if [[ "\$1" == "test" ]]; then
  # Spawn a child that outlives this process (the orphan-that-pins-worktree).
  ( sleep 300 & echo "\$!" > "$marker"; sleep 300 ) &
  # Hang forever — go's -timeout would normally fire, but we model the case
  # where it does NOT (children keep the tree alive).
  sleep 300
  exit 0
fi
exit 0
EOF
  chmod +x "$stubdir/go"
  printf '#!/bin/bash\nexit 0\n' > "$stubdir/gofmt"; chmod +x "$stubdir/gofmt"

  ( cd "$tmprepo" && git init -q && \
      git config user.email "test@example.com" && \
      git config user.name "test" && \
      git commit -q --allow-empty -m init ) >/dev/null 2>&1

  local rc=0 out
  out=$(
    cd "$tmprepo" && \
    env PATH="$stubdir:/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$stubdir" \
    GT_PREPUSH_TEST_WALL_SECONDS=2 \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}

  if [[ $rc -eq 0 ]]; then
    echo "FAIL: a hanging slow gate should reject the push (got rc=0)" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$marker"; return
  fi
  if ! echo "$out" | grep -qi "wall-clock timeout"; then
    echo "FAIL: rejection should mention the wall-clock timeout" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$marker"; return
  fi

  # The forked grandchild must NOT survive the group-kill. Give the kill a
  # moment to land, then check the PID recorded in $marker is gone.
  sleep 2
  if [[ -f "$marker" ]]; then
    local child_pid
    child_pid=$(cat "$marker" 2>/dev/null)
    if [[ -n "$child_pid" ]] && kill -0 "$child_pid" 2>/dev/null; then
      echo "FAIL: forked test child $child_pid survived the group-kill (orphan would pin worktree)" >&2
      kill -KILL -- "-$child_pid" 2>/dev/null
      kill -KILL "$child_pid" 2>/dev/null
      FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo" "$marker"; return
    fi
  fi
  PASS=$((PASS + 1)); rm -rf "$stubdir" "$tmprepo" "$marker"
}

# Test: a fast slow-gate (returns before the wall) is NOT killed and the push
# passes — the wall-clock must not penalize healthy test runs.
test_slow_gate_under_wall_passes() {
  local stubdir tmprepo
  stubdir=$(mktemp -d)
  tmprepo=$(mktemp -d)
  printf '#!/bin/bash\nexit 0\n' > "$stubdir/go";    chmod +x "$stubdir/go"
  printf '#!/bin/bash\nexit 0\n' > "$stubdir/gofmt"; chmod +x "$stubdir/gofmt"

  ( cd "$tmprepo" && git init -q && \
      git config user.email "test@example.com" && \
      git config user.name "test" && \
      git commit -q --allow-empty -m init ) >/dev/null 2>&1

  local rc=0 out
  out=$(
    cd "$tmprepo" && \
    env PATH="$stubdir:/usr/bin:/bin" GT_PREPUSH_PROBE_DIRS="$stubdir" \
    GT_PREPUSH_TEST_WALL_SECONDS=30 \
    bash "$SCRIPT" 2>&1
  ) || rc=$?
  rc=${rc:-0}
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: a fast slow-gate under the wall should pass (got rc=$rc)" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo"; return
  fi
  if ! echo "$out" | grep -q "all gates passed"; then
    echo "FAIL: healthy run should report all gates passed" >&2
    echo "$out" >&2
    FAIL=$((FAIL + 1)); rm -rf "$stubdir" "$tmprepo"; return
  fi
  PASS=$((PASS + 1)); rm -rf "$stubdir" "$tmprepo"
}

# --- gu-enqh0: upfront banner before gates -------------------------------
#
# A backgrounded / non-tty push runs the gates for ~2 min before contacting
# origin, with no output until the first gate prints — which reads as a hung
# push and invites a spurious retry. The script must announce the gate run
# (and, on the default path, the ~2 min cost + the skip hint) BEFORE the first
# gate executes, so even a backgrounded push shows immediate progress.

# Test: default run prints the banner announcing gates + the skip hint, and it
# appears BEFORE the first gate ('go build') output.
test_banner_printed_by_default() {
  run_with_stubs \
    'echo "go-called: $*" >&2; exit 0' \
    'exit 0' \
    0
  if [[ $RC -ne 0 ]]; then
    echo "FAIL: default run should pass when all stubs pass (got rc=$RC)" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1)); cleanup_last_run; return
  fi
  if ! echo "$OUT" | grep -qi "before contacting origin"; then
    echo "FAIL: default run should print an upfront banner before gates" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1)); cleanup_last_run; return
  fi
  if ! echo "$OUT" | grep -q "GT_SKIP_PREPUSH=1"; then
    echo "FAIL: default banner should include the slow-tier skip hint" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1)); cleanup_last_run; return
  fi
  # The banner must precede the first gate's output.
  local banner_line build_line
  banner_line=$(echo "$OUT" | grep -n "before contacting origin" | head -1 | cut -d: -f1)
  build_line=$(echo "$OUT" | grep -n "go-called: build" | head -1 | cut -d: -f1)
  if [[ -z "$banner_line" || -z "$build_line" ]] || (( banner_line >= build_line )); then
    echo "FAIL: banner (line $banner_line) should precede 'go build' (line $build_line)" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1)); cleanup_last_run; return
  fi
  PASS=$((PASS + 1)); cleanup_last_run
}

# Test: under GT_SKIP_PREPUSH=1 the banner announces the fast-gate-only run
# (and does NOT promise the ~2 min slow tier, which is skipped).
test_banner_printed_under_skip() {
  run_with_stubs \
    'echo "go-called: $*" >&2; exit 0' \
    'exit 0' \
    1
  if [[ $RC -ne 0 ]]; then
    echo "FAIL: skip run should pass when fast gates pass (got rc=$RC)" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1)); cleanup_last_run; return
  fi
  if ! echo "$OUT" | grep -qi "running fast gates"; then
    echo "FAIL: skip run should print a fast-gates-only banner before gates" >&2
    echo "$OUT" >&2
    FAIL=$((FAIL + 1)); cleanup_last_run; return
  fi
  PASS=$((PASS + 1)); cleanup_last_run
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
  # gu-lint-fastgate: golangci-lint as a fast gate
  test_lint_gate_skipped_when_not_installed
  test_lint_gate_blocks_under_skip
  # gs-812: fail closed in a Go repo + standard-dir probe
  test_go_absent_blocks_in_go_repo
  test_go_absent_skips_in_non_go_repo
  test_lint_absent_blocks_in_go_repo
  test_probe_finds_tools_off_base_path
  # gs-orsm: host-wide gate-slot concurrency cap
  test_gate_slot_cap_proceeds_when_full
  test_gate_slot_cap_acquires_when_free
  test_gate_slot_cap_skips_without_town_root
  # gu-40xsf: slow-gate child must not leak the gate-slot fd to a daemon
  test_gate_slot_cap_no_fd_leak_to_daemon_child
  # gu-zadrb: slow-gate hard wall-clock group timeout
  test_slow_gate_wall_clock_timeout_rejects
  test_slow_gate_under_wall_passes
  # gu-enqh0: upfront banner before gates
  test_banner_printed_by_default
  test_banner_printed_under_skip
fi

echo ""
echo "pre-push-check_test.sh: $PASS passed, $FAIL failed"
if [[ $FAIL -gt 0 ]]; then
  exit 1
fi

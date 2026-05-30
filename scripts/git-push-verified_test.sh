#!/usr/bin/env bash
# git-push-verified_test.sh — tests for scripts/git-push-verified.sh
#
# These tests stub `git` on PATH so they run with no network and no real
# repo state. The stub responds to `git push <case>...` by emitting one of
# several canned outputs and exit codes that mirror the four classification
# rules in the script under test.
#
# Usage: bash scripts/git-push-verified_test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/git-push-verified.sh"

if [[ ! -x "$SCRIPT" ]]; then
  echo "FAIL: $SCRIPT not executable" >&2
  exit 1
fi

PASS=0
FAIL=0

# Build a tmp PATH with a stub `git` that selects its behavior by reading
# the FIRST positional arg AFTER `push` (the test's "case key").
# stdout/stderr/exit-code of the stub are controlled by that key.
make_git_stub() {
  local stubdir
  stubdir=$(mktemp -d)
  cat > "$stubdir/git" <<'STUB'
#!/usr/bin/env bash
# Test stub: only handles `git push <case-key> ...`. Anything else exits 0
# silently so the script under test can call its preflight if it ever
# adds one.
if [ "${1:-}" != "push" ]; then
  exit 0
fi
shift
case_key="${1:-}"
case "$case_key" in
  ok-fastforward)
    cat <<EOF
To https://github.com/example/repo.git
   1772a631..aa88ed10  temp -> main
EOF
    exit 0
    ;;
  ok-newbranch)
    cat <<EOF
To https://github.com/example/repo.git
 * [new branch]      polecat/foo -> polecat/foo
EOF
    exit 0
    ;;
  ok-forced)
    cat <<EOF
To https://github.com/example/repo.git
 + 1772a631...aa88ed10 temp -> main (forced update)
EOF
    exit 0
    ;;
  ok-uptodate)
    echo "Everything up-to-date"
    exit 0
    ;;
  ok-with-noisy-hook)
    # Pre-push hook printed something containing the word "reject" but the
    # push itself succeeded. The script must classify this as PUSH_OK
    # (not PUSH_FAILED) — this is the gu-vph7 hallucination case.
    cat <<EOF
pre-push: running gates
linter: 0 errors, 0 warnings (rejected-tokens-allowlist ok)
pre-push: all gates passed ✓
To https://github.com/example/repo.git
   1772a631..aa88ed10  temp -> main
EOF
    exit 0
    ;;
  fail-rejected)
    cat <<EOF
To https://github.com/example/repo.git
 ! [rejected]        main -> main (non-fast-forward)
error: failed to push some refs to 'https://github.com/example/repo.git'
hint: Updates were rejected because the tip of your current branch is behind
EOF
    exit 1
    ;;
  fail-remote-rejected)
    cat <<EOF
To https://github.com/example/repo.git
 ! [remote rejected] main -> main (pre-receive hook declined)
error: failed to push some refs to 'https://github.com/example/repo.git'
EOF
    exit 1
    ;;
  fail-network)
    cat <<EOF
fatal: unable to access 'https://github.com/example/repo.git/': Could not resolve host: github.com
EOF
    exit 128
    ;;
  fail-zero-exit-rejected)
    # Hostile defense-in-depth case: git emits a rejection line but exits 0.
    # Should still be classified as PUSH_FAILED.
    cat <<EOF
To https://github.com/example/repo.git
 ! [rejected]        main -> main (non-fast-forward)
EOF
    exit 0
    ;;
  *)
    echo "stub: unknown case key '$case_key'" >&2
    exit 99
    ;;
esac
STUB
  chmod +x "$stubdir/git"
  echo "$stubdir"
}

run_case() {
  local name=$1
  local case_key=$2
  local expect_exit=$3
  local expect_marker=$4   # e.g. PUSH_OK or PUSH_FAILED
  local expect_substr=$5   # substring expected in stdout (or empty for any)

  local stubdir
  stubdir=$(make_git_stub)

  local out rc
  out=$(PATH="$stubdir:$PATH" bash "$SCRIPT" "$case_key" 2>/dev/null)
  rc=$?

  local ok=1
  if [ "$rc" -ne "$expect_exit" ]; then
    echo "FAIL[$name]: expected exit $expect_exit, got $rc" >&2
    echo "  stdout: $out" >&2
    ok=0
  fi
  if ! printf '%s\n' "$out" | grep -q "^$expect_marker:"; then
    echo "FAIL[$name]: expected stdout to start with '$expect_marker:', got: $out" >&2
    ok=0
  fi
  if [ -n "$expect_substr" ] && ! printf '%s\n' "$out" | grep -qF "$expect_substr"; then
    echo "FAIL[$name]: expected stdout to contain '$expect_substr', got: $out" >&2
    ok=0
  fi

  rm -rf "$stubdir"

  if [ "$ok" -eq 1 ]; then
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
  fi
}

# --- Test cases ---

# Successful pushes must produce PUSH_OK.
run_case "fast-forward push -> PUSH_OK"      ok-fastforward     0 PUSH_OK "1772a631..aa88ed10  temp -> main"
run_case "new-branch push -> PUSH_OK"        ok-newbranch       0 PUSH_OK "[new branch]"
run_case "forced push -> PUSH_OK"            ok-forced          0 PUSH_OK "1772a631...aa88ed10 temp -> main"
run_case "up-to-date push -> PUSH_OK"        ok-uptodate        0 PUSH_OK "up-to-date"

# Critical regression case: noisy pre-push hook output containing "reject"
# tokens must NOT be misclassified as PUSH_FAILED. This is the gu-vph7
# scenario the bead was filed for.
run_case "noisy hook + ok push -> PUSH_OK"   ok-with-noisy-hook 0 PUSH_OK "1772a631..aa88ed10  temp -> main"

# Genuine failures must produce PUSH_FAILED with exit 1.
run_case "rejected push -> PUSH_FAILED"      fail-rejected      1 PUSH_FAILED "rejected"
run_case "remote-rejected -> PUSH_FAILED"    fail-remote-rejected 1 PUSH_FAILED "rejected"
run_case "network failure -> PUSH_FAILED"    fail-network       1 PUSH_FAILED "exit-128"

# Defense-in-depth: zero exit but rejection line still classifies as failure.
run_case "zero-exit + rejection -> FAILED"   fail-zero-exit-rejected 1 PUSH_FAILED "rejected"

# Usage error path — script invoked with no args.
out=$(bash "$SCRIPT" 2>/dev/null)
rc=$?
if [ "$rc" -eq 1 ] && printf '%s\n' "$out" | grep -q "^PUSH_FAILED: usage"; then
  PASS=$((PASS + 1))
else
  echo "FAIL[no-args -> usage error]: expected exit 1 + PUSH_FAILED: usage, got rc=$rc out='$out'" >&2
  FAIL=$((FAIL + 1))
fi

echo ""
echo "git-push-verified_test.sh: $PASS passed, $FAIL failed"
if (( FAIL > 0 )); then
  exit 1
fi

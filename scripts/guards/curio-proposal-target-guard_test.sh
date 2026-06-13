#!/usr/bin/env bash
#
# Tests for curio-proposal-target-guard.sh (Curio P3 B6, air-gap layer 2).
#
# Each test builds a throwaway git repo, commits a base, then a "CR" commit, and
# runs the guard with GT_PROPOSAL_GUARD_BASE pointed at the base. This exercises
# the real base..HEAD diff path the live merge-queue gate uses.
#
# Run: bash scripts/guards/curio-proposal-target-guard_test.sh
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GUARD="$SCRIPT_DIR/curio-proposal-target-guard.sh"
PASS=0
FAIL=0

assert_exit() {
  local test_name="$1" expected="$2" actual="$3"
  if [[ "$actual" == "$expected" ]]; then
    echo "  PASS: $test_name (exit $actual)"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name (expected exit $expected, got $actual)"
    FAIL=$((FAIL + 1))
  fi
}

# new_repo — create a temp git repo with one base commit, echo its path.
new_repo() {
  local d
  d=$(mktemp -d)
  git -C "$d" init -q
  git -C "$d" config user.email "t@test"
  git -C "$d" config user.name "t"
  git -C "$d" config commit.gpgsign false
  # A realistic base daemon.json with a NON-curio threshold present.
  cat > "$d/daemon.json" <<'JSON'
{
  "patrols": {
    "curio": {
      "rate_thresholds": {
        "sling": 350,
        "done": 1300
      }
    }
  }
}
JSON
  git -C "$d" add -A
  git -C "$d" commit -qm "base"
  echo "$d"
}

# run_guard_in <repo> — run the guard inside <repo> diffing against the base
# commit (HEAD~1 after the CR commit), in a clean GT_* env.
run_guard_in() {
  local repo="$1" code=0
  ( cd "$repo" && env -u GT_PROPOSAL_GUARD_DISABLE GT_PROPOSAL_GUARD_BASE="HEAD~1" \
      bash "$GUARD" ) >/dev/null 2>&1 || code=$?
  echo "$code"
}

echo "=== curio-proposal-target-guard tests ==="
echo ""

# Test 1: a CR that tunes a NON-curio series passes.
echo "Test: non-curio threshold tune passes"
R=$(new_repo)
cat > "$R/daemon.json" <<'JSON'
{
  "patrols": {
    "curio": {
      "rate_thresholds": {
        "sling": 400,
        "done": 1300
      }
    }
  }
}
JSON
git -C "$R" commit -qam "tune sling threshold"
assert_exit "non-curio tune passes" "0" "$(run_guard_in "$R")"
rm -rf "$R"

# Test 2: a CR that adds a curio.* threshold target is REJECTED.
echo "Test: curio.* threshold target rejected"
R=$(new_repo)
cat > "$R/daemon.json" <<'JSON'
{
  "patrols": {
    "curio": {
      "rate_thresholds": {
        "sling": 350,
        "done": 1300,
        "curio.cycle": 5
      }
    }
  }
}
JSON
git -C "$R" commit -qam "propose curio.cycle rate rule"
assert_exit "curio.cycle target rejected" "1" "$(run_guard_in "$R")"
rm -rf "$R"

# Test 3: a curio.* literal added in a Go SOURCE file (a new rule) is rejected.
echo "Test: curio.* in Go source rejected"
R=$(new_repo)
cat > "$R/newrule.go" <<'GO'
package curio

var bad = map[string]int{"curio.dispatch": 1}
GO
git -C "$R" add -A
git -C "$R" commit -qm "add rule keyed on curio.dispatch"
assert_exit "curio.* in source rejected" "1" "$(run_guard_in "$R")"
rm -rf "$R"

# Test 4: a curio.* literal added in a TEST file is allowed (fixtures exercise
# the live air-gap legitimately, e.g. loopbreaker_test.go).
echo "Test: curio.* in test file allowed"
R=$(new_repo)
cat > "$R/rule_test.go" <<'GO'
package curio

var fixture = map[string]int{"curio.cycle": 0}
GO
git -C "$R" add -A
git -C "$R" commit -qm "test fixture using curio.cycle"
assert_exit "curio.* in test file allowed" "0" "$(run_guard_in "$R")"
rm -rf "$R"

# Test 5: a curio.* literal added under a testdata/ path is allowed.
echo "Test: curio.* under testdata/ allowed"
R=$(new_repo)
mkdir -p "$R/internal/curio/testdata"
echo '{"series": "curio.mail", "observed": 9}' > "$R/internal/curio/testdata/fixture.json"
git -C "$R" add -A
git -C "$R" commit -qm "add replay fixture with curio.mail"
assert_exit "curio.* under testdata allowed" "0" "$(run_guard_in "$R")"
rm -rf "$R"

# Test 6: the bare CurioSeriesPrefix constant ("curio.") does NOT match — the
# pattern requires a concrete series name after the dot.
echo "Test: bare 'curio.' prefix constant not matched"
R=$(new_repo)
cat > "$R/record.go" <<'GO'
package curio

const CurioSeriesPrefix = "curio."
GO
git -C "$R" add -A
git -C "$R" commit -qm "add CurioSeriesPrefix constant"
assert_exit "bare prefix constant passes" "0" "$(run_guard_in "$R")"
rm -rf "$R"

# Test 7: disabled guard exits 0 even on a violating CR.
echo "Test: disabled guard passes"
R=$(new_repo)
cat > "$R/daemon.json" <<'JSON'
{"patrols":{"curio":{"rate_thresholds":{"curio.cycle":1}}}}
JSON
git -C "$R" commit -qam "violating but guard disabled"
code=0
( cd "$R" && GT_PROPOSAL_GUARD_DISABLE=1 GT_PROPOSAL_GUARD_BASE="HEAD~1" \
    bash "$GUARD" ) >/dev/null 2>&1 || code=$?
assert_exit "disabled guard exits 0" "0" "$code"
rm -rf "$R"

# Test 8: a pre-existing curio.* on the BASE (not added by the CR) does NOT trip
# the guard — only added lines are inspected.
echo "Test: pre-existing curio.* on base not flagged"
R=$(mktemp -d)
git -C "$R" init -q
git -C "$R" config user.email "t@test"; git -C "$R" config user.name "t"
git -C "$R" config commit.gpgsign false
# Base already contains a curio.* (e.g. a sanctioned legacy reference).
echo 'var x = map[string]int{"curio.cycle": 0}' > "$R/legacy_test.go"
echo 'unrelated' > "$R/other.txt"
git -C "$R" add -A; git -C "$R" commit -qm "base with pre-existing curio ref"
# CR only edits an unrelated non-curio file.
echo 'changed' > "$R/other.txt"
git -C "$R" commit -qam "unrelated change"
assert_exit "pre-existing base curio.* not flagged" "0" "$(run_guard_in "$R")"
rm -rf "$R"

echo ""
echo "Results: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1

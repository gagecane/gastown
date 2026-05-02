#!/bin/bash
# Test suite for the pre-commit go-vet/golangci-lint hook.
#
# Usage: bash .githooks/pre-commit_test.sh
#
# Each test spins up a throwaway git repo in $TMPDIR, stages some files, and
# invokes the hook with a controlled PATH so we can mock/hide 'go' and
# 'golangci-lint' as needed. We never run the hook against the repo we live in.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOOK="$SCRIPT_DIR/pre-commit"
PASS=0
FAIL=0
TMPROOT=""

cleanup() {
  cd /tmp
  if [[ -n "$TMPROOT" && -d "$TMPROOT" ]]; then
    rm -rf "$TMPROOT"
  fi
  TMPROOT=""
}
trap cleanup EXIT

setup_root() {
  TMPROOT=$(mktemp -d)
}

# make_repo <name> — initialize a git repo under $TMPROOT/<name> and echo its path.
# Prints nothing on failure; caller checks $?.
make_repo() {
  local name=$1
  local repo="$TMPROOT/$name"
  mkdir -p "$repo"
  (
    cd "$repo"
    git init -q -b main
    git config user.email "test@gastown.test"
    git config user.name "Test"
    git config commit.gpgsign false
    # Create an empty initial commit so 'HEAD' exists (needed for --new-from-rev=HEAD).
    git commit --allow-empty -q -m "init"
  )
  printf '%s' "$repo"
}

# make_go_repo <name> — repo with a minimal go.mod so 'go vet' can resolve packages.
make_go_repo() {
  local name=$1
  local repo
  repo=$(make_repo "$name")
  (
    cd "$repo"
    cat >go.mod <<'MOD'
module example.com/test

go 1.21
MOD
    git add go.mod
    git commit -q -m "add go.mod"
  )
  printf '%s' "$repo"
}

run_hook() {
  # $1 = repo dir
  # remaining = env var assignments like "GIT_SKIP_VET=1" or "PATH=..."
  local repo=$1; shift
  (
    cd "$repo"
    if (( $# > 0 )); then
      env "$@" bash "$HOOK"
    else
      bash "$HOOK"
    fi
  )
}

assert_pass() {
  local name=$1 repo=$2; shift 2
  local output rc
  output=$(run_hook "$repo" "$@" 2>&1)
  rc=$?
  if (( rc == 0 )); then
    echo "PASS: $name"
    PASS=$((PASS+1))
  else
    echo "FAIL: $name — hook exited $rc (expected 0)"
    echo "       output: $output"
    FAIL=$((FAIL+1))
  fi
}

assert_reject() {
  local name=$1 repo=$2; shift 2
  local output rc
  output=$(run_hook "$repo" "$@" 2>&1)
  rc=$?
  if (( rc != 0 )); then
    echo "PASS: $name"
    PASS=$((PASS+1))
  else
    echo "FAIL: $name — hook exited 0 (expected non-zero)"
    echo "       output: $output"
    FAIL=$((FAIL+1))
  fi
}

setup_root

# ------------------------------------------------------------------
# Test 1: No staged changes at all → pass quickly
# ------------------------------------------------------------------
repo=$(make_repo "no-staged")
assert_pass "no staged files" "$repo"

# ------------------------------------------------------------------
# Test 2: Staged non-Go files only → pass (nothing to check)
# ------------------------------------------------------------------
repo=$(make_repo "non-go-only")
(
  cd "$repo"
  echo "# readme" > README.md
  git add README.md
)
assert_pass "staged non-Go files only" "$repo"

# ------------------------------------------------------------------
# Test 3: Escape hatch GIT_SKIP_VET=1 bypasses everything
# ------------------------------------------------------------------
repo=$(make_repo "escape-hatch")
(
  cd "$repo"
  # Intentionally broken Go — should still pass with escape hatch.
  cat >broken.go <<'GO'
package main

func main() { this is not go }
GO
  git add broken.go
)
assert_pass "GIT_SKIP_VET=1 bypasses even broken Go" "$repo" "GIT_SKIP_VET=1"

# ------------------------------------------------------------------
# Test 4: Merge in progress → skip
# ------------------------------------------------------------------
repo=$(make_repo "merge-in-progress")
(
  cd "$repo"
  # Fake a merge state. The hook only checks for MERGE_HEAD existence.
  git rev-parse HEAD > "$(git rev-parse --git-dir)/MERGE_HEAD"
  # Stage something broken to prove the hook doesn't look at it.
  cat >broken.go <<'GO'
package main
func main() { garbage }
GO
  git add broken.go
)
assert_pass "MERGE_HEAD present skips checks" "$repo"

# ------------------------------------------------------------------
# Test 5: Cherry-pick in progress → skip
# ------------------------------------------------------------------
repo=$(make_repo "cherry-pick-in-progress")
(
  cd "$repo"
  git rev-parse HEAD > "$(git rev-parse --git-dir)/CHERRY_PICK_HEAD"
  cat >broken.go <<'GO'
package main
func main() { garbage }
GO
  git add broken.go
)
assert_pass "CHERRY_PICK_HEAD present skips checks" "$repo"

# ------------------------------------------------------------------
# Test 6: Rebase in progress → skip
# ------------------------------------------------------------------
repo=$(make_repo "rebase-in-progress")
(
  cd "$repo"
  mkdir -p "$(git rev-parse --git-dir)/rebase-merge"
  cat >broken.go <<'GO'
package main
func main() { garbage }
GO
  git add broken.go
)
assert_pass "rebase-merge dir present skips checks" "$repo"

# ------------------------------------------------------------------
# Test 7: Revert in progress → skip
# ------------------------------------------------------------------
repo=$(make_repo "revert-in-progress")
(
  cd "$repo"
  git rev-parse HEAD > "$(git rev-parse --git-dir)/REVERT_HEAD"
  cat >broken.go <<'GO'
package main
func main() { garbage }
GO
  git add broken.go
)
assert_pass "REVERT_HEAD present skips checks" "$repo"

# ------------------------------------------------------------------
# Test 8: 'go' not on PATH → warn, pass (graceful degradation)
# ------------------------------------------------------------------
repo=$(make_go_repo "no-go-tool")
(
  cd "$repo"
  echo "package main" > main.go
  git add main.go
)
# Supply an empty PATH so neither go nor golangci-lint is reachable. We still
# need basic shell utilities (git, dirname, etc.) — keep /usr/bin and /bin in
# the path but nothing that resolves 'go'.
empty_tools_dir="$TMPROOT/empty-bin"
mkdir -p "$empty_tools_dir"
assert_pass "missing 'go' tool warns and passes" "$repo" "PATH=$empty_tools_dir:/usr/bin:/bin"

# ------------------------------------------------------------------
# Test 9: Staged valid Go + 'go' available → vet passes; if
#         golangci-lint is not on PATH it's skipped with a warning.
# ------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  repo=$(make_go_repo "valid-go")
  (
    cd "$repo"
    cat >main.go <<'GO'
package main

import "fmt"

func main() {
    fmt.Println("hello")
}
GO
    git add main.go
  )
  # Mask any system golangci-lint so Test 9 exercises the "missing lint" branch.
  masked_dir="$TMPROOT/masked-bin"
  mkdir -p "$masked_dir"
  # Link only 'go' (and generic tools) into masked_dir; omit golangci-lint.
  ln -s "$(command -v go)" "$masked_dir/go"
  # sh utilities the hook relies on come from standard PATH.
  assert_pass "valid Go passes vet; missing lint warns and passes" \
    "$repo" "PATH=$masked_dir:/usr/bin:/bin"
else
  echo "SKIP: 'go' not installed — cannot test vet path"
fi

# ------------------------------------------------------------------
# Test 10: Staged broken Go + 'go' available → vet rejects.
# ------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  repo=$(make_go_repo "vet-broken")
  (
    cd "$repo"
    # Printf directive / argument count mismatch: a classic vet catch.
    cat >bad.go <<'GO'
package main

import "fmt"

func main() {
    fmt.Printf("%s %s\n", "only-one-arg")
}
GO
    git add bad.go
  )
  masked_dir="$TMPROOT/masked-bin-10"
  mkdir -p "$masked_dir"
  ln -s "$(command -v go)" "$masked_dir/go"
  assert_reject "go vet catches printf arg mismatch" \
    "$repo" "PATH=$masked_dir:/usr/bin:/bin"
else
  echo "SKIP: 'go' not installed — cannot test vet rejection"
fi

# ------------------------------------------------------------------
# Test 11: Deletion-only commit of Go files → pass (no pkg_dirs to vet).
# ------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  repo=$(make_go_repo "delete-only")
  (
    cd "$repo"
    cat >old.go <<'GO'
package main

func main() {}
GO
    git add old.go
    git commit -q -m "add old.go"
    git rm -q old.go
  )
  masked_dir="$TMPROOT/masked-bin-11"
  mkdir -p "$masked_dir"
  ln -s "$(command -v go)" "$masked_dir/go"
  assert_pass "deletion-only Go commit passes" \
    "$repo" "PATH=$masked_dir:/usr/bin:/bin"
else
  echo "SKIP: 'go' not installed — cannot test deletion path"
fi

# ------------------------------------------------------------------
# Test 12: Vendored .go files are ignored even if broken.
# ------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  repo=$(make_go_repo "vendor-ignored")
  (
    cd "$repo"
    mkdir -p vendor/example.com/bad
    cat >vendor/example.com/bad/bad.go <<'GO'
package bad

import "fmt"

func Trigger() {
    fmt.Printf("%s %s\n", "only-one-arg")
}
GO
    git add vendor/example.com/bad/bad.go
  )
  masked_dir="$TMPROOT/masked-bin-12"
  mkdir -p "$masked_dir"
  ln -s "$(command -v go)" "$masked_dir/go"
  assert_pass "vendored files skipped by hook scoping" \
    "$repo" "PATH=$masked_dir:/usr/bin:/bin"
else
  echo "SKIP: 'go' not installed — cannot test vendor exclusion"
fi

# ------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------
echo ""
echo "Results: $PASS passed, $FAIL failed"
if (( FAIL > 0 )); then
  exit 1
fi
exit 0

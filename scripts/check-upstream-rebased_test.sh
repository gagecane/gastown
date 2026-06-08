#!/usr/bin/env bash
# check-upstream-rebased_test.sh — tests for scripts/check-upstream-rebased.sh
#
# Focus: the auto-file-fork-sync-bead behavior added for gu-sf0vo. These tests
# run in CI and locally with no network: each builds a tiny local git repo with
# `upstream` and `origin` remotes wired to local bare repos, and stubs `bd` on
# PATH so no real beads DB is touched. The keepalive `gt` call and the upstream
# auto-add are disabled by pointing the remotes at local paths.
#
# Usage: bash scripts/check-upstream-rebased_test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/check-upstream-rebased.sh"

if [[ ! -x "$SCRIPT" ]]; then
  echo "FAIL: $SCRIPT not executable" >&2
  exit 1
fi

PASS=0
FAIL=0

assert_eq() {
  local label=$1 expected=$2 actual=$3
  if [[ "$expected" == "$actual" ]]; then
    PASS=$((PASS + 1))
  else
    echo "FAIL: $label" >&2
    echo "  expected: $expected" >&2
    echo "  actual:   $actual" >&2
    FAIL=$((FAIL + 1))
  fi
}

assert_contains() {
  local label=$1 needle=$2 haystack=$3
  if [[ "$haystack" == *"$needle"* ]]; then
    PASS=$((PASS + 1))
  else
    echo "FAIL: $label" >&2
    echo "  expected to contain: $needle" >&2
    echo "  got:" >&2
    printf '    %s\n' "$haystack" >&2
    FAIL=$((FAIL + 1))
  fi
}

assert_not_contains() {
  local label=$1 needle=$2 haystack=$3
  if [[ "$haystack" != *"$needle"* ]]; then
    PASS=$((PASS + 1))
  else
    echo "FAIL: $label" >&2
    echo "  expected NOT to contain: $needle" >&2
    echo "  got:" >&2
    printf '    %s\n' "$haystack" >&2
    FAIL=$((FAIL + 1))
  fi
}

git_q() { git -C "$1" "${@:2}" >/dev/null 2>&1; }

# Build a repo layout:
#   upstream.git (bare)  <- origin.git (bare)  <- work (clone)
# All commits authored deterministically; no network.
#
# Args: $1 = scenario, one of:
#   fork_behind   — origin/main behind upstream/main, HEAD == origin/main
#   branch_stale  — origin/main current with upstream, HEAD behind (feature
#                   branch hasn't rebased) — must NOT auto-file
#   fork_synced   — everything in sync (gate passes, no file)
# Echoes the work dir path.
make_repo() {
  local scenario=$1 base up org work
  base=$(mktemp -d)
  up="$base/upstream.git"
  org="$base/origin.git"
  work="$base/work"

  git init --quiet --bare -b main "$up"
  git init --quiet --bare -b main "$org"
  git init --quiet -b main "$work"
  git_q "$work" config user.email t@t
  git_q "$work" config user.name t
  git_q "$work" remote add upstream "$up"
  git_q "$work" remote add origin "$org"

  # Commit 1 — shared base.
  echo a > "$work/f"
  git_q "$work" add f
  git_q "$work" commit -m c1

  case "$scenario" in
    fork_synced)
      # Both remotes get the same single commit; HEAD on it too.
      git_q "$work" push upstream main
      git_q "$work" push origin main
      ;;
    branch_stale)
      # origin == upstream (both have c2), but local HEAD stays at c1.
      git_q "$work" push origin main      # origin has c1
      echo b > "$work/f"
      git_q "$work" add f
      git_q "$work" commit -m c2
      git_q "$work" push upstream main     # upstream has c1+c2
      git_q "$work" push origin main       # origin also c1+c2
      git_q "$work" reset --hard HEAD~1    # HEAD back to c1 (stale branch)
      ;;
    fork_behind)
      # origin frozen at c1; upstream advances to c2+c3; HEAD == origin (c1).
      git_q "$work" push origin main       # origin has c1
      echo b > "$work/f"; git_q "$work" add f; git_q "$work" commit -m c2
      echo c > "$work/f"; git_q "$work" add f; git_q "$work" commit -m c3
      git_q "$work" push upstream main     # upstream c1+c2+c3
      git_q "$work" reset --hard HEAD~2    # HEAD back to c1 == origin
      ;;
    *)
      echo "unknown scenario: $scenario" >&2; exit 1 ;;
  esac

  # Refresh remote-tracking refs the script reads (origin/main, upstream/main).
  git_q "$work" fetch origin main
  git_q "$work" fetch upstream main
  echo "$work"
}

# Stub `bd` recording invocations to $BD_LOG. Behavior controlled by env:
#   STUB_BD_EXISTING=<id>  -> `bd list` returns a JSON array with that id.
# Otherwise `bd list` returns [] (no open tracking bead).
make_bd_stub() {
  local dir=$1
  cat > "$dir/bd" <<'EOF'
#!/usr/bin/env bash
echo "bd $*" >> "$BD_LOG"
case "$1" in
  list)
    if [[ -n "${STUB_BD_EXISTING:-}" ]]; then
      printf '[{"id":"%s","title":"x"}]' "$STUB_BD_EXISTING"
    else
      printf '[]'
    fi
    ;;
  create)
    echo "gu-stubnew"   # --silent contract: print only the id
    ;;
  update) : ;;
esac
exit 0
EOF
  chmod +x "$dir/bd"
}

# Run the gate in a work dir with a stubbed bd on PATH, forwarding the
# requested env. Output is captured via a file (not command substitution): the
# gate spawns a backgrounded `gt heartbeat keepalive` subshell that inherits
# stdout, so a `$(...)` capture would block ~30s on the orphaned `sleep` after
# the gate logically exits. A file redirect avoids that wait.
run_gate() {
  local work=$1; shift
  local stub_dir; stub_dir=$(mktemp -d)
  make_bd_stub "$stub_dir"
  local out_file; out_file=$(mktemp)
  local rc=0
  (
    cd "$work" || exit 99
    PATH="$stub_dir:$PATH" \
    UPSTREAM_URL="file://$work/.git" \
    "$@" \
    bash "$SCRIPT"
  ) >"$out_file" 2>&1 || rc=$?
  LAST_OUT="$(cat "$out_file")"
  LAST_RC="$rc"
  rm -rf "$stub_dir" "$out_file"
}

# ── Test 1: synced fork passes, files nothing ──────────────────────────────
test_synced_passes() {
  local work; work=$(make_repo fork_synced)
  local log; log=$(mktemp)
  run_gate "$work" env "BD_LOG=$log"
  assert_eq "synced: exit 0" "0" "$LAST_RC"
  assert_contains "synced: success line" "Fork is rebased" "$LAST_OUT"
  assert_eq "synced: no bd calls" "" "$(cat "$log")"
  rm -rf "$work" "$log"
}

# ── Test 2: fork-wide divergence files a P0 fork-sync bead, gate still red ──
test_fork_behind_files_bead() {
  local work; work=$(make_repo fork_behind)
  local log; log=$(mktemp)
  run_gate "$work" env "BD_LOG=$log" "GT_RIG=testrig"
  assert_eq "fork_behind: exit 1 (gate still red)" "1" "$LAST_RC"
  assert_contains "fork_behind: not-ancestor msg" "is NOT an ancestor of HEAD" "$LAST_OUT"
  assert_contains "fork_behind: filing message" "filing fork-sync tracking bead" "$LAST_OUT"
  local calls; calls=$(cat "$log")
  assert_contains "fork_behind: bd list dedup check" "bd list --label fork-sync" "$calls"
  assert_contains "fork_behind: bd create called" "bd create" "$calls"
  assert_contains "fork_behind: P0 priority" "-p 0" "$calls"
  assert_contains "fork_behind: fork-sync label" "fork-sync" "$calls"
  assert_contains "fork_behind: divergence in title" "2 commits behind" "$calls"
  assert_contains "fork_behind: rig in title" "testrig" "$calls"
  assert_not_contains "fork_behind: did not update" "bd update" "$calls"
  rm -rf "$work" "$log"
}

# ── Test 3: dedup — existing open bead is updated, not duplicated ──────────
test_dedup_updates_existing() {
  local work; work=$(make_repo fork_behind)
  local log; log=$(mktemp)
  run_gate "$work" env "BD_LOG=$log" "STUB_BD_EXISTING=gu-exist1"
  assert_eq "dedup: exit 1" "1" "$LAST_RC"
  assert_contains "dedup: update message" "already open; updating" "$LAST_OUT"
  local calls; calls=$(cat "$log")
  assert_contains "dedup: bd update on existing id" "bd update gu-exist1" "$calls"
  assert_not_contains "dedup: no new create" "bd create" "$calls"
  rm -rf "$work" "$log"
}

# ── Test 4: stale feature branch (origin current) does NOT auto-file ───────
test_branch_stale_no_file() {
  local work; work=$(make_repo branch_stale)
  local log; log=$(mktemp)
  run_gate "$work" env "BD_LOG=$log"
  assert_eq "branch_stale: exit 1 (gate red)" "1" "$LAST_RC"
  assert_contains "branch_stale: not-ancestor msg" "is NOT an ancestor of HEAD" "$LAST_OUT"
  # origin/main is NOT behind upstream, so no tracking bead should be filed.
  assert_eq "branch_stale: no bd calls" "" "$(cat "$log")"
  rm -rf "$work" "$log"
}

# ── Test 5: opt-out via GT_FORKSYNC_AUTOFILE=0 ─────────────────────────────
test_optout_disables_autofile() {
  local work; work=$(make_repo fork_behind)
  local log; log=$(mktemp)
  run_gate "$work" env "BD_LOG=$log" "GT_FORKSYNC_AUTOFILE=0"
  assert_eq "optout: exit 1 (gate red)" "1" "$LAST_RC"
  assert_eq "optout: no bd calls" "" "$(cat "$log")"
  rm -rf "$work" "$log"
}

test_synced_passes
test_fork_behind_files_bead
test_dedup_updates_existing
test_branch_stale_no_file
test_optout_disables_autofile

echo ""
echo "check-upstream-rebased_test.sh: $PASS passed, $FAIL failed"
if (( FAIL > 0 )); then
  exit 1
fi

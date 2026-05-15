#!/usr/bin/env bash
# check-lockfile-mirror_test.sh — tests for scripts/check-lockfile-mirror.sh
#
# These tests run in CI and locally without any network access. We use a
# stub `curl` on PATH plus the script's NPM_MIRROR_DRY_RUN mode to cover
# both the URL-rewriting logic and the failure-detection logic.
#
# Usage: bash scripts/check-lockfile-mirror_test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/check-lockfile-mirror.sh"

if [[ ! -x "$SCRIPT" ]]; then
  echo "FAIL: $SCRIPT not executable" >&2
  exit 1
fi

PASS=0
FAIL=0

# Each test runs in its own tmpdir so fixtures don't bleed between cases.
make_fixture_lockfile() {
  local dir=$1
  cat > "$dir/package-lock.json" <<'JSON'
{
  "name": "fixture",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "fixture",
      "version": "1.0.0"
    },
    "node_modules/yaml": {
      "version": "2.8.4",
      "resolved": "https://registry.npmjs.org/yaml/-/yaml-2.8.4.tgz",
      "integrity": "sha512-fake"
    },
    "node_modules/@smithy/middleware-compression": {
      "version": "4.4.1",
      "resolved": "https://registry.npmjs.org/@smithy/middleware-compression/-/middleware-compression-4.4.1.tgz",
      "integrity": "sha512-fake"
    },
    "node_modules/local-link": {
      "version": "1.0.0",
      "resolved": "file:../local-link"
    }
  }
}
JSON
}

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

# ── Test 1: no-op when NPM_MIRROR_URL unset ────────────────────────────────
test_noop_without_mirror_url() {
  local out rc=0
  out=$(unset NPM_MIRROR_URL; bash "$SCRIPT" 2>&1) || rc=$?
  assert_eq "noop_without_mirror_url: exit 0" "0" "$rc"
  assert_eq "noop_without_mirror_url: silent" "" "$out"
}

# ── Test 2: dry-run rewrites registry URLs onto the mirror ─────────────────
test_dry_run_rewrites() {
  if ! command -v jq >/dev/null 2>&1; then
    echo "SKIP: jq not on PATH (test_dry_run_rewrites)" >&2
    return
  fi
  local tmp out rc=0
  tmp=$(mktemp -d)
  make_fixture_lockfile "$tmp"

  out=$(
    NPM_MIRROR_URL="https://mirror.example.com/npm" \
    NPM_MIRROR_DRY_RUN=1 \
    bash "$SCRIPT" "$tmp/package-lock.json" 2>/dev/null
  ) || rc=$?

  assert_eq "dry_run: exit 0" "0" "$rc"
  assert_contains "dry_run: yaml URL rewritten" \
    "https://mirror.example.com/npm/yaml/-/yaml-2.8.4.tgz" \
    "$out"
  assert_contains "dry_run: scoped pkg rewritten" \
    "https://mirror.example.com/npm/@smithy/middleware-compression/-/middleware-compression-4.4.1.tgz" \
    "$out"
  # file: URLs are not rewritten — they should not appear in stdout.
  if [[ "$out" == *"file:"* ]]; then
    echo "FAIL: dry_run: file: URL leaked into rewrite output" >&2
    FAIL=$((FAIL + 1))
  else
    PASS=$((PASS + 1))
  fi

  rm -rf "$tmp"
}

# ── Test 3: trailing slash on NPM_MIRROR_URL is normalized ─────────────────
test_trailing_slash_normalized() {
  if ! command -v jq >/dev/null 2>&1; then
    echo "SKIP: jq not on PATH (test_trailing_slash_normalized)" >&2
    return
  fi
  local tmp out
  tmp=$(mktemp -d)
  make_fixture_lockfile "$tmp"

  out=$(
    NPM_MIRROR_URL="https://mirror.example.com/npm/" \
    NPM_MIRROR_DRY_RUN=1 \
    bash "$SCRIPT" "$tmp/package-lock.json" 2>/dev/null
  )

  # Must NOT contain a double slash between the host and path.
  if [[ "$out" == *"npm//"* ]]; then
    echo "FAIL: trailing_slash: produced double-slash in rewritten URL" >&2
    echo "  output:" >&2
    printf '    %s\n' "$out" >&2
    FAIL=$((FAIL + 1))
  else
    PASS=$((PASS + 1))
  fi

  rm -rf "$tmp"
}

# ── Test 4: 404 from mirror surfaces as exit 1 ─────────────────────────────
# Stub curl on PATH to return 404 for any URL — this simulates the mirror
# missing a tarball. Confirms the script collects failures and exits non-zero.
test_404_fails() {
  if ! command -v jq >/dev/null 2>&1; then
    echo "SKIP: jq not on PATH (test_404_fails)" >&2
    return
  fi
  local tmp stub_dir out rc=0
  tmp=$(mktemp -d)
  stub_dir=$(mktemp -d)
  make_fixture_lockfile "$tmp"

  cat > "$stub_dir/curl" <<'EOF'
#!/usr/bin/env bash
# Stub curl: emit 404 for any HEAD request, with the format the script
# expects from `curl -sS --head -o /dev/null -w '%{http_code}'`.
printf '404'
exit 0
EOF
  chmod +x "$stub_dir/curl"

  out=$(
    PATH="$stub_dir:$PATH" \
    NPM_MIRROR_URL="https://mirror.example.com/npm" \
    NPM_MIRROR_PARALLEL=1 \
    bash "$SCRIPT" "$tmp/package-lock.json" 2>&1
  ) || rc=$?

  assert_eq "404_fails: exit 1" "1" "$rc"
  assert_contains "404_fails: reports failure" "FAILED on mirror" "$out"
  assert_contains "404_fails: shows status code" "[404]" "$out"

  rm -rf "$tmp" "$stub_dir"
}

# ── Test 5: 200 from mirror passes ─────────────────────────────────────────
test_200_passes() {
  if ! command -v jq >/dev/null 2>&1; then
    echo "SKIP: jq not on PATH (test_200_passes)" >&2
    return
  fi
  local tmp stub_dir out rc=0
  tmp=$(mktemp -d)
  stub_dir=$(mktemp -d)
  make_fixture_lockfile "$tmp"

  cat > "$stub_dir/curl" <<'EOF'
#!/usr/bin/env bash
printf '200'
exit 0
EOF
  chmod +x "$stub_dir/curl"

  out=$(
    PATH="$stub_dir:$PATH" \
    NPM_MIRROR_URL="https://mirror.example.com/npm" \
    NPM_MIRROR_PARALLEL=1 \
    bash "$SCRIPT" "$tmp/package-lock.json" 2>&1
  ) || rc=$?

  assert_eq "200_passes: exit 0" "0" "$rc"
  assert_contains "200_passes: success line" "URL(s) reachable" "$out"

  rm -rf "$tmp" "$stub_dir"
}

# ── Test 6: missing jq fails with exit 2 ───────────────────────────────────
# We can't actually remove jq, but we can simulate it by pointing PATH to an
# empty dir. Skip if the host environment makes that impossible.
test_missing_jq() {
  local stub_dir out rc=0 bash_bin
  stub_dir=$(mktemp -d)
  # Capture absolute path to bash before we sterilize PATH.
  bash_bin=$(command -v bash)

  out=$(
    PATH="$stub_dir" \
    NPM_MIRROR_URL="https://mirror.example.com/npm" \
    "$bash_bin" "$SCRIPT" 2>&1
  ) || rc=$?

  assert_eq "missing_jq: exit 2" "2" "$rc"
  assert_contains "missing_jq: error message" "jq is required" "$out"

  rm -rf "$stub_dir"
}

# ── Test 7: missing curl in non-dry-run mode fails with exit 2 ─────────────
test_missing_curl() {
  if ! command -v jq >/dev/null 2>&1; then
    echo "SKIP: jq not on PATH (test_missing_curl)" >&2
    return
  fi
  local stub_dir jq_path bash_bin out rc=0
  stub_dir=$(mktemp -d)
  # Provide jq but not curl.
  jq_path=$(command -v jq)
  bash_bin=$(command -v bash)
  ln -s "$jq_path" "$stub_dir/jq"

  out=$(
    PATH="$stub_dir" \
    NPM_MIRROR_URL="https://mirror.example.com/npm" \
    "$bash_bin" "$SCRIPT" 2>&1
  ) || rc=$?

  assert_eq "missing_curl: exit 2" "2" "$rc"
  assert_contains "missing_curl: error message" "curl is required" "$out"

  rm -rf "$stub_dir"
}

# ── Test 8: custom NPM_MIRROR_PUBLIC_PREFIX is respected ───────────────────
test_custom_public_prefix() {
  if ! command -v jq >/dev/null 2>&1; then
    echo "SKIP: jq not on PATH (test_custom_public_prefix)" >&2
    return
  fi
  local tmp out
  tmp=$(mktemp -d)
  cat > "$tmp/package-lock.json" <<'JSON'
{
  "name": "fixture",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "": { "name": "fixture", "version": "1.0.0" },
    "node_modules/foo": {
      "version": "1.0.0",
      "resolved": "https://other-registry.example.com/foo/-/foo-1.0.0.tgz"
    }
  }
}
JSON

  out=$(
    NPM_MIRROR_URL="https://mirror.example.com/npm" \
    NPM_MIRROR_PUBLIC_PREFIX="https://other-registry.example.com" \
    NPM_MIRROR_DRY_RUN=1 \
    bash "$SCRIPT" "$tmp/package-lock.json" 2>/dev/null
  )

  assert_contains "custom_public_prefix: rewrite uses prefix" \
    "https://mirror.example.com/npm/foo/-/foo-1.0.0.tgz" \
    "$out"

  rm -rf "$tmp"
}

test_noop_without_mirror_url
test_dry_run_rewrites
test_trailing_slash_normalized
test_404_fails
test_200_passes
test_missing_jq
test_missing_curl
test_custom_public_prefix

echo ""
echo "check-lockfile-mirror_test.sh: $PASS passed, $FAIL failed"
if (( FAIL > 0 )); then
  exit 1
fi

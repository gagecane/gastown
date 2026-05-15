#!/usr/bin/env bash
# check-lockfile-mirror.sh — audit package-lock.json against an npm mirror.
#
# Why this exists
# ---------------
# Internal npm mirrors (Amazon NpmPrettyMuch / CodeArtifact, Nexus, Verdaccio,
# Artifactory, etc.) lag public npm by hours-to-days. A package-lock.json
# regenerated against public npm on a dev host can contain transitive
# dependency versions the mirror has not yet indexed. The build sandbox then
# fails with `npm install ETARGET` when it tries to resolve those versions.
#
# This script HEAD-checks every `resolved` tarball URL in package-lock.json
# against a configured mirror. Any 404 means the build will break. Run it as
# a pre-commit hook, a pre-push hook, or a CI gate — wherever you want the
# feedback loop.
#
# Usage
# -----
#   scripts/check-lockfile-mirror.sh [<path-to-package-lock.json>...]
#
# If no paths are given, every tracked package-lock.json (excluding
# node_modules) is checked.
#
# Configuration (environment variables)
# -------------------------------------
#   NPM_MIRROR_URL   Mirror base URL — script is a no-op if unset.
#                    Example: https://npm-pretty-much.example.com/repository/npm-public
#                    URLs of the form https://registry.npmjs.org/<pkg>/-/<tarball>
#                    are rewritten to <NPM_MIRROR_URL>/<pkg>/-/<tarball>.
#
#   NPM_MIRROR_PUBLIC_PREFIX
#                    Public registry prefix to rewrite (default:
#                    https://registry.npmjs.org). Useful when locks resolve
#                    against a non-default registry that the mirror also fronts.
#
#   NPM_MIRROR_CURL_TIMEOUT
#                    Per-request curl timeout in seconds (default: 10).
#
#   NPM_MIRROR_PARALLEL
#                    Parallelism for HEAD checks (default: 8). Set 1 for
#                    sequential — useful when debugging or rate-limited.
#
#   NPM_MIRROR_AUTH  Optional auth header value (passed verbatim to curl -H
#                    "Authorization: $NPM_MIRROR_AUTH"). Use for mirrors that
#                    require a Bearer token or basic auth.
#
#   NPM_MIRROR_DRY_RUN=1
#                    Print the rewritten URLs that *would* be checked, but
#                    skip the network calls. Used by the script's own tests.
#
# Exit codes
# ----------
#   0  All checked URLs reachable, or NPM_MIRROR_URL is not set (no-op).
#   1  At least one URL returned 404 (or another non-2xx/3xx) on the mirror.
#   2  Usage / dependency / config error (jq missing, lockfile not found, etc.).
#
# Why not just check upstream npm?
# --------------------------------
# Upstream npm always serves the latest packages — it would never 404. The
# whole point is that the *mirror* is the source of truth for the build. We
# rewrite registry.npmjs.org URLs to the mirror equivalent and HEAD-check
# *those*, because that's what the build environment will fetch.

set -uo pipefail

# ── No-op fast path when not configured ─────────────────────────────────────
# Most contributors don't have an internal mirror. The script must be a
# silent no-op for them so it can be wired into hooks unconditionally without
# friction. Only contributors with NPM_MIRROR_URL set actually run it.
if [[ -z "${NPM_MIRROR_URL:-}" ]]; then
  if [[ "${NPM_MIRROR_VERBOSE:-0}" == "1" ]]; then
    echo "check-lockfile-mirror: NPM_MIRROR_URL not set, skipping." >&2
  fi
  exit 0
fi

# ── Dependencies ────────────────────────────────────────────────────────────
if ! command -v jq >/dev/null 2>&1; then
  echo "check-lockfile-mirror: jq is required but not on PATH." >&2
  echo "  Install jq, or unset NPM_MIRROR_URL to disable this check." >&2
  exit 2
fi

# Curl is only needed for the actual HEAD checks; in dry-run mode we skip
# network entirely so the script can be tested without a real mirror.
if [[ "${NPM_MIRROR_DRY_RUN:-0}" != "1" ]] && ! command -v curl >/dev/null 2>&1; then
  echo "check-lockfile-mirror: curl is required but not on PATH." >&2
  exit 2
fi

# ── Configuration with defaults ─────────────────────────────────────────────
MIRROR_URL="${NPM_MIRROR_URL%/}"  # strip trailing slash for predictable join
PUBLIC_PREFIX="${NPM_MIRROR_PUBLIC_PREFIX:-https://registry.npmjs.org}"
PUBLIC_PREFIX="${PUBLIC_PREFIX%/}"
CURL_TIMEOUT="${NPM_MIRROR_CURL_TIMEOUT:-10}"
PARALLEL="${NPM_MIRROR_PARALLEL:-8}"

# ── Locate lockfiles to check ───────────────────────────────────────────────
lockfiles=()
if (( $# > 0 )); then
  lockfiles=("$@")
else
  # Default: every tracked package-lock.json. We use git ls-files so we don't
  # accidentally walk into node_modules or other ignored trees.
  if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    while IFS= read -r f; do
      lockfiles+=("$f")
    done < <(git ls-files '*package-lock.json' ':!:**/node_modules/**' 2>/dev/null)
  fi
fi

if (( ${#lockfiles[@]} == 0 )); then
  echo "check-lockfile-mirror: no package-lock.json files to check." >&2
  exit 0
fi

# ── Extract resolved tarball URLs from a lockfile ───────────────────────────
# package-lock.json schema:
#   - lockfileVersion 1: top-level "dependencies" map keyed by name, each
#                        entry has "resolved" plus nested "dependencies".
#   - lockfileVersion 2/3: top-level "packages" map keyed by path
#                          (e.g. "node_modules/foo"); same "resolved" field.
# We walk both shapes and emit unique resolved URLs. Empty/null values are
# filtered out (the root package and bundled deps don't have a resolved URL).
extract_resolved_urls() {
  local lockfile=$1
  jq -r '
    [
      ( .packages // {} | to_entries[] | .value.resolved? ),
      ( [.. | objects | .resolved? // empty ] | .[] )
    ]
    | map(select(. != null and . != ""))
    | unique
    | .[]
  ' "$lockfile" 2>/dev/null
}

# ── Rewrite a public-registry URL onto the mirror ───────────────────────────
# We only rewrite URLs whose prefix matches PUBLIC_PREFIX. Anything else
# (git+ssh, file:, http(s) to a different host) is left alone — the consumer's
# install step already knows how to fetch those, and a HEAD against the
# mirror would be meaningless. We *report* skipped URLs so the user can
# notice if their lockfile is using an unexpected registry.
rewrite_to_mirror() {
  local url=$1
  if [[ "$url" == "$PUBLIC_PREFIX"/* ]]; then
    printf '%s%s\n' "$MIRROR_URL" "${url#$PUBLIC_PREFIX}"
  else
    return 1
  fi
}

# ── HEAD check a single URL ─────────────────────────────────────────────────
# Output one line: "<url>\t<status>" where status is the HTTP code or "ERR"
# for transport failures. We use --fail-with-body=0 (default) so the curl
# exit code reflects only transport errors, and parse the printed status.
check_url() {
  local url=$1
  local code
  local args=(
    -sS
    --head
    --max-time "$CURL_TIMEOUT"
    -o /dev/null
    -w '%{http_code}'
  )
  if [[ -n "${NPM_MIRROR_AUTH:-}" ]]; then
    args+=( -H "Authorization: $NPM_MIRROR_AUTH" )
  fi
  if code=$(curl "${args[@]}" "$url" 2>/dev/null); then
    printf '%s\t%s\n' "$url" "$code"
  else
    printf '%s\tERR\n' "$url"
  fi
}

# Export for xargs -P parallel invocation.
export PUBLIC_PREFIX MIRROR_URL CURL_TIMEOUT
export -f check_url
# NPM_MIRROR_AUTH is read by check_url via the parent env; export so the
# subshells xargs spawns inherit it.
[[ -n "${NPM_MIRROR_AUTH:-}" ]] && export NPM_MIRROR_AUTH

# ── Main loop ───────────────────────────────────────────────────────────────
total_checked=0
total_failed=0
failures_file=$(mktemp)
skipped_file=$(mktemp)
trap 'rm -f "$failures_file" "$skipped_file"' EXIT

for lockfile in "${lockfiles[@]}"; do
  if [[ ! -f "$lockfile" ]]; then
    echo "check-lockfile-mirror: $lockfile not found, skipping." >&2
    continue
  fi

  echo "check-lockfile-mirror: scanning $lockfile" >&2

  urls_to_check=()
  while IFS= read -r resolved; do
    if mirror_url=$(rewrite_to_mirror "$resolved"); then
      urls_to_check+=("$mirror_url")
    else
      printf '%s\n' "$resolved" >> "$skipped_file"
    fi
  done < <(extract_resolved_urls "$lockfile")

  if (( ${#urls_to_check[@]} == 0 )); then
    echo "check-lockfile-mirror: $lockfile — no URLs match $PUBLIC_PREFIX." >&2
    continue
  fi

  echo "check-lockfile-mirror: $lockfile — ${#urls_to_check[@]} URL(s) to check." >&2
  total_checked=$(( total_checked + ${#urls_to_check[@]} ))

  if [[ "${NPM_MIRROR_DRY_RUN:-0}" == "1" ]]; then
    # Dry run: just print the rewritten URLs and consider them all OK.
    printf '%s\n' "${urls_to_check[@]}"
    continue
  fi

  # Parallel HEAD checks via xargs -P. Output is one "url<TAB>status" per line.
  results=$(printf '%s\n' "${urls_to_check[@]}" \
              | xargs -n 1 -P "$PARALLEL" -I {} bash -c 'check_url "$@"' _ {})

  while IFS=$'\t' read -r url status; do
    case "$status" in
      2*|3*)
        : # OK (200, 301, 302, 304, …)
        ;;
      *)
        printf '%s\t%s\n' "$url" "$status" >> "$failures_file"
        total_failed=$(( total_failed + 1 ))
        ;;
    esac
  done <<< "$results"
done

# ── Report ──────────────────────────────────────────────────────────────────
if [[ -s "$skipped_file" ]] && [[ "${NPM_MIRROR_VERBOSE:-0}" == "1" ]]; then
  skipped_count=$(wc -l < "$skipped_file" | tr -d ' ')
  echo "check-lockfile-mirror: skipped $skipped_count URL(s) outside $PUBLIC_PREFIX." >&2
fi

if (( total_failed > 0 )); then
  echo "" >&2
  echo "✗ check-lockfile-mirror: $total_failed/$total_checked URL(s) FAILED on mirror $MIRROR_URL" >&2
  echo "" >&2
  while IFS=$'\t' read -r url status; do
    printf '  [%s] %s\n' "$status" "$url" >&2
  done < "$failures_file"
  echo "" >&2
  echo "These resolved versions are absent from the mirror. The build will fail." >&2
  echo "Either wait for the mirror to catch up, downgrade the affected dep to a" >&2
  echo "version the mirror has, or pin the dep in package.json." >&2
  exit 1
fi

echo "✓ check-lockfile-mirror: $total_checked URL(s) reachable on $MIRROR_URL" >&2
exit 0

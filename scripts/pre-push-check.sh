#!/bin/bash
# pre-push-check.sh: run the same gates CI runs, locally, before a push reaches origin.
#
# Purpose: this repo's CI Test + Integration Tests jobs are the trusted signal
# that a change doesn't break main. But CI only runs AFTER push, which means
# broken changes land on main before humans notice. Crew workers push directly
# to main (no feature branches, no PR queue), so there is no merge-queue
# backstop. This script closes that gap by running CI's gates locally.
#
# Gates are split into two tiers:
#
#   FAST gates (always run, even under GT_SKIP_PREPUSH=1):
#     1. go build ./...            — compiles (catches broken imports, type errs)
#     2. go vet ./...              — static analysis (shadow, printf, unreachable)
#     3. gofmt -l                  — formatting check (catches trailing newlines etc.)
#     4. golangci-lint run         — misspell, errcheck, gosec, unconvert,
#                                    unparam (catches lint failures that
#                                    only CI's Lint job sees today). In a Go
#                                    repo a missing golangci-lint fails the
#                                    push CLOSED (gs-812); non-Go checkouts
#                                    skip it. See gu-lint-fastgate.
#
#   SLOW gates (skipped when GT_SKIP_PREPUSH=1):
#     5. go test ./... -count=1    — full unit test suite with clean env (~2min)
#
# Why split? `gt done --pre-verified` sets GT_SKIP_PREPUSH=1 to avoid re-running
# the slow test suite that the polecat already ran during pre-verification. But
# tests are the only slow gate — build/vet/gofmt are seconds and there's no
# legitimate reason to skip them. A polecat that lies about pre-verification,
# runs gates against a stale base, or simply forgets to gofmt can still land
# broken code on origin. The fast tier closes that gap. See gu-7f0v.
#
# Env hygiene:
#   This script UNSETS GT_TOWN_ROOT and GT_ROOT before running tests. Some
#   tests call workspace.FindFromCwdOrError which falls back to these env vars
#   if CWD detection fails — a broken test can pass locally (your shell has
#   them set) but fail in CI (clean env). Unsetting here matches CI and
#   catches those tests before push. See commit 77c54398 for the canonical
#   instance of this bug.
#
# Integration tests:
#   This script does NOT run `-tags=integration` tests by default — they
#   require Docker + dolt container and take ~5 minutes. CI's Integration
#   Tests job runs them. To run locally: `make verify-integration`.
#
# Escape hatches (use sparingly; if you're reaching regularly, file a bead):
#   GT_SKIP_PREPUSH=1 GT_SKIP_PREPUSH_REASON="<text>" git push
#                                    — skip SLOW gates (fast gates still run);
#                                      REASON is required and audited (gu-zy57)
#   git push --no-verify             — skip ALL hooks (standard git, NOT audited)
#
# Audit trail (gu-zy57):
#   When GT_SKIP_PREPUSH=1 is honoured the script appends one JSON line to
#   <repo>/.runtime/prepush-skips.jsonl recording who skipped the slow tier,
#   why, and what was pushed. The empty-REASON case is rejected outright so
#   a misconfigured caller can't silently bypass the slow tier without record.
#
# Why pre-push, not pre-commit:
#   The existing pre-commit hook already runs go vet and a fast lint scoped
#   to staged files — that's the right granularity for "don't commit obvious
#   garbage." But the full test suite takes ~2min and polecats commit
#   constantly; running it on every commit would be a tax they'd learn to
#   --no-verify. Pre-push runs once per push instead of once per commit, so
#   the cost is amortized and it's the last line of defense before CI.

set -u

# --- Tier selection -------------------------------------------------------
#
# GT_SKIP_PREPUSH=1 (set by `gt done --pre-verified`) means the caller already
# ran the full gate suite on a rebased branch, so skip the SLOW gates (tests).
# Fast gates (build, vet, gofmt) ALWAYS run — they're cheap and catch the
# most common landing failures (gu-7f0v: trailing-newline gofmt landings under
# --pre-verified that briefly broke main between push and CI catch).
#
# Audit (gu-zy57): a slow-tier skip MUST carry GT_SKIP_PREPUSH_REASON=<text>
# and is recorded as one JSON line in <repo>/.runtime/prepush-skips.jsonl.
# Without the reason the skip is rejected outright — misconfigured callers
# that set the flag but not the reason fail closed instead of silently
# bypassing tests.

# json_escape <var> — escape a string for embedding inside a JSON value.
# Handles backslashes, double quotes, and the control characters we expect
# to see in branch names / actor strings. Anything more exotic still produces
# parseable JSON because we never embed user input in a key, only in a value.
json_escape() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\r'/\\r}
  s=${s//$'\t'/\\t}
  printf '%s' "$s"
}

# emit_skip_event — append one JSON line to .runtime/prepush-skips.jsonl
# describing this slow-tier skip. Best-effort: failures here MUST NOT block
# the push, because the only thing worse than an unaudited skip is a script
# that refuses to push because audit logging broke.
emit_skip_event() {
  local reason=$1
  local repo_root
  repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || return 0
  local events_dir="$repo_root/.runtime"
  mkdir -p "$events_dir" 2>/dev/null || return 0
  local events_file="$events_dir/prepush-skips.jsonl"

  local ts actor branch sha
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  actor="${BD_ACTOR:-${GT_ROLE:-unknown}}"
  branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
  sha=$(git rev-parse HEAD 2>/dev/null || echo "unknown")

  local r a b
  r=$(json_escape "$reason")
  a=$(json_escape "$actor")
  b=$(json_escape "$branch")

  printf '{"ts":"%s","actor":"%s","reason":"%s","branch":"%s","sha":"%s"}\n' \
    "$ts" "$a" "$r" "$b" "$sha" >> "$events_file" 2>/dev/null || true
}

SKIP_SLOW=0
if [[ "${GT_SKIP_PREPUSH:-0}" == "1" ]]; then
  if [[ -z "${GT_SKIP_PREPUSH_REASON:-}" ]]; then
    cat >&2 <<'EOF'
✗ Push rejected: GT_SKIP_PREPUSH=1 requires GT_SKIP_PREPUSH_REASON=<text>.

Skipping the slow tier without recording why is what let unexplained
test-suite bypasses through unaudited. Set a reason explaining the skip:

  GT_SKIP_PREPUSH=1 \
  GT_SKIP_PREPUSH_REASON="pre-verified: gates ran in formula step 7" \
  git push

Legitimate reasons include "pre-verified" (polecat already ran gates),
"emergency: <bead>", or "cherry-pick: <context>". The reason is appended
to .runtime/prepush-skips.jsonl so witness tooling can audit it.

Note: fast gates (build/vet/gofmt) still run unconditionally — this only
gates the SLOW tier (test suite). See gu-7f0v + gu-zy57.
EOF
    exit 1
  fi
  SKIP_SLOW=1
  emit_skip_event "${GT_SKIP_PREPUSH_REASON}"
  echo "pre-push: GT_SKIP_PREPUSH=1, skipping SLOW gates (tests; reason: ${GT_SKIP_PREPUSH_REASON}). Fast gates still run." >&2
fi

# --- Locate repo root -----------------------------------------------------

REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo "")
if [[ -z "$REPO_ROOT" ]]; then
  echo "pre-push: not a git repo, skipping." >&2
  exit 0
fi
cd "$REPO_ROOT" || exit 1

# --- Tool discovery: probe standard install dirs --------------------------
#
# gs-812: a daemon restarted from a sterile shell, a polecat worktree with a
# thinner PATH, or a cron-launched hook can run with a PATH that omits the
# dirs Go tools install to. Before declaring a tool "absent" (and skipping or
# blocking on its gate), fold the standard install locations onto PATH so
# "not found" means genuinely-not-installed, not just not-on-this-PATH. The
# probe list is overridable via GT_PREPUSH_PROBE_DIRS (colon-separated) for
# testing.

PROBE_DIRS="${GT_PREPUSH_PROBE_DIRS:-/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin}"
_old_ifs=$IFS
IFS=':'
for _d in $PROBE_DIRS; do
  [[ -n "$_d" && -d "$_d" ]] || continue
  case ":$PATH:" in
    *":$_d:"*) ;;            # already on PATH
    *) PATH="$PATH:$_d" ;;
  esac
done
IFS=$_old_ifs
export PATH

# --- Is this a Go repo? ---------------------------------------------------
#
# The gates only mean something in a Go repo, identified by a go.mod at the
# repo root. In a Go repo the gates ARE the contract, so a missing toolchain
# must FAIL CLOSED (block + audit) rather than silently skip — the old
# silent exit-0 skip is exactly what let ungated pushes reach main (gs-812).
# Genuine non-Go checkouts still skip gracefully.

IS_GO_REPO=0
[[ -f "$REPO_ROOT/go.mod" ]] && IS_GO_REPO=1

# --- Require go (fail closed in a Go repo) -------------------------------

if ! command -v go >/dev/null 2>&1; then
  if [[ "$IS_GO_REPO" == "1" ]]; then
    emit_skip_event "BLOCKED: 'go' not found on PATH in a Go repo (probed: ${PROBE_DIRS})"
    cat >&2 <<EOF

✗ Push rejected: 'go' not found on PATH, but this is a Go repo (go.mod present).

The pre-push gates (build/vet/gofmt/lint/test) are the only backstop between a
broken change and main — crew workers push directly, with no merge-queue. The
old behavior skipped every gate when 'go' fell off PATH, landing ungated code
silently. This gate now fails CLOSED, and the block is recorded in
.runtime/prepush-skips.jsonl.

Probed for 'go' in: ${PROBE_DIRS}

Fix: install Go or add it to PATH, then re-push. To bypass ALL hooks anyway
(not recommended on a Go repo): git push --no-verify.
EOF
    exit 1
  fi
  echo "⚠ pre-push: 'go' not found and no go.mod at repo root — non-Go checkout, skipping gates." >&2
  exit 0
fi

# --- Clean env so tests don't inherit developer's GT_TOWN_ROOT ------------
#
# Tests that need a workspace must create their own marker (mayor/town.json).
# If they rely on GT_TOWN_ROOT/GT_ROOT from the developer's shell, they're
# silently broken — CI has no such env.

unset GT_TOWN_ROOT GT_ROOT GT_SESSION GT_RIG GT_POLECAT

# --- Scrub git-repo env vars so hook-inherited GIT_DIR doesn't leak -------
#
# When git runs a hook (e.g. pre-push), it exports GIT_DIR, GIT_WORK_TREE,
# GIT_INDEX_FILE, etc. pointing at the pushing repo (see githooks(5) and
# git(1) "Discussion" on environment). Those vars are inherited by every
# child process this script spawns — including `go test ./...`, whose tests
# run `git` via os/exec. When a test creates its own bare repo in t.TempDir()
# and runs `git push <fixture_remote> main`, git reads GIT_DIR from the
# environment instead of the test's cmd.Dir and silently operates on the
# REAL pushing repo. The test's push then lands on the real .repo.git, and
# the subsequent outer `git push origin main` propagates the pollution all
# the way to the remote.
#
# This is how gu-h2ru's test-fixture commits ("add dirty.txt" by user "Test")
# reached https://github.com/gagecane/gastown main. See the bead for the
# full forensic trail. Unsetting these vars here matches the environment
# that `go test ./...` sees when invoked from a plain shell.

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE \
      GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
      GIT_COMMON_DIR GIT_CEILING_DIRECTORIES GIT_NAMESPACE \
      GIT_PREFIX GIT_LITERAL_PATHSPECS GIT_GLOB_PATHSPECS \
      GIT_NOGLOB_PATHSPECS GIT_ICASE_PATHSPECS

# --- FAST GATE 1: go build ------------------------------------------------

echo "pre-push: [fast] go build ./... (compile check)" >&2
if ! go build ./... 2>&1; then
  cat >&2 <<'EOF'

✗ Push rejected: 'go build ./...' failed.

Fix compile errors before pushing. CI will reject the same build failures
but with a ~5min round-trip cost.

This is a FAST gate — it runs even under --pre-verified / GT_SKIP_PREPUSH=1.
There is no escape hatch for build failures; fix the build.
EOF
  exit 1
fi

# --- FAST GATE 2: go vet --------------------------------------------------

echo "pre-push: [fast] go vet ./... (static analysis)" >&2
if ! go vet ./... 2>&1; then
  cat >&2 <<'EOF'

✗ Push rejected: 'go vet ./...' reported issues.

Vet catches real bugs (shadow, printf, unreachable). Fix them or use
//nolint:vet on the specific line if the warning is a false positive.

This is a FAST gate — it runs even under --pre-verified / GT_SKIP_PREPUSH=1.
EOF
  exit 1
fi

# --- FAST GATE 3: gofmt ---------------------------------------------------
#
# CI's gofmt gate caught two consecutive landings (bffac8f7, ced30a88, both
# 2026-05-29) with trailing-newline failures, briefly breaking main between
# each push and the CI catch. Running gofmt here moves detection in front of
# the push, eliminating the broken-main window. See gu-7f0v.

echo "pre-push: [fast] gofmt -l (formatting check)" >&2
unformatted=$(gofmt -l . 2>/dev/null)
if [[ -n "$unformatted" ]]; then
  cat >&2 <<EOF

✗ Push rejected: gofmt found unformatted files:
$unformatted

Run \`gofmt -w <file>\` (or \`gofmt -w .\`) to fix formatting, then re-push.

This is a FAST gate — it runs even under --pre-verified / GT_SKIP_PREPUSH=1.
EOF
  exit 1
fi

# --- FAST GATE 4: golangci-lint -------------------------------------------
#
# CI's Lint job (golangci-lint with .golangci.yml) catches misspell, errcheck,
# gosec, unconvert, unparam findings that go vet does NOT. Same broken-main
# window as gofmt: polecats pass build/vet/gofmt locally, push, and only THEN
# does golangci-lint catch issues — by which point main is briefly broken.
#
# Mirroring the gofmt gate: runs as a fast gate so it fires even under
# --pre-verified. The check is fast (~10-30s on this codebase) and catches
# the failure modes that pre-verification often misses.
#
# If golangci-lint isn't installed locally, behavior splits on repo type
# (gs-812): in a Go repo this gate fails CLOSED (block + audit), because a
# missing linter is exactly how lint failures slipped onto main in the window
# between push and CI catch. Non-Go checkouts skip gracefully. Install requires
# `go install` + the version lock from .github/workflows/ci.yml. (gu-lint-fastgate)

if command -v golangci-lint >/dev/null 2>&1; then
  echo "pre-push: [fast] golangci-lint run (static analysis)" >&2
  if ! golangci-lint run --timeout=5m 2>&1; then
    cat >&2 <<EOF

✗ Push rejected: golangci-lint found findings.

Fix the issues above (or add a .golangci.yml exclusion if the linter is
mechanically wrong about an external API string), then re-push.

This is a FAST gate — it runs even under --pre-verified / GT_SKIP_PREPUSH=1.
The full lint suite is configured in .golangci.yml; the same gate runs in CI.
EOF
    exit 1
  fi
elif [[ "$IS_GO_REPO" == "1" ]]; then
  emit_skip_event "BLOCKED: golangci-lint not found on PATH in a Go repo (probed: ${PROBE_DIRS})"
  cat >&2 <<EOF

✗ Push rejected: golangci-lint not found on PATH, but this is a Go repo.

CI's Lint job runs it regardless; skipping it locally let lint failures land on
main in the window between push and CI catch — the exact churn gs-812 tracks.
In a Go repo this gate now fails CLOSED. The block is recorded in
.runtime/prepush-skips.jsonl.

Probed for golangci-lint in: ${PROBE_DIRS}

Fix: install it, then re-push:
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4
To bypass ALL hooks anyway (not recommended): git push --no-verify.
EOF
  exit 1
else
  echo "pre-push: [fast] golangci-lint not installed and no go.mod — non-Go checkout, skipping lint gate" >&2
fi

# --- SLOW GATE: go test ---------------------------------------------------
#
# Skipped under GT_SKIP_PREPUSH=1. The contract: callers setting that env
# (e.g. `gt done --pre-verified`) already ran tests on a rebased branch.
# Build/vet/gofmt above are not skippable because they're cheap and catch
# the failure modes that pre-verification often misses.

if [[ "$SKIP_SLOW" == "1" ]]; then
  echo "pre-push: [slow] tests skipped (GT_SKIP_PREPUSH=1)" >&2
  echo "pre-push: fast gates passed ✓" >&2
  exit 0
fi

echo "pre-push: [slow] go test ./... -count=1 (unit tests)" >&2
if ! go test ./... -count=1 -timeout=5m 2>&1; then
  cat >&2 <<'EOF'

✗ Push rejected: unit tests failed.

CI would reject the same failures with a ~5min round-trip cost. Fix or
skip the failing tests before pushing.

Tip: tests that relied on GT_TOWN_ROOT/GT_ROOT in the developer's shell
might pass locally without this gate but fail in CI. This script unsets
those vars to match CI — if a test unexpectedly fails here but passes
with those set, the test is the bug, not your change.

Emergency escape hatch (skips SLOW gates only — fast gates still run;
REASON is required and recorded in .runtime/prepush-skips.jsonl):
  GT_SKIP_PREPUSH=1 GT_SKIP_PREPUSH_REASON="<text>" git push
EOF
  exit 1
fi

echo "pre-push: all gates passed ✓" >&2
exit 0

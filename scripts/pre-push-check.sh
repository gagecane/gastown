#!/bin/bash
# pre-push-check.sh: run the same FAST gates CI runs, locally, before a push
# reaches origin.
#
# Purpose: this repo's CI Test + Integration Tests jobs are the trusted signal
# that a change doesn't break main. But CI only runs AFTER push, which means
# broken changes land on main before humans notice. Crew workers push directly
# to main (no feature branches, no PR queue), so there is no merge-queue
# backstop for them. This script closes that gap by running CI's FAST gates
# locally:
#
#   1. go build ./...            — compiles (catches broken imports, type errs)
#   2. go vet ./...              — static analysis (shadow, printf, unreachable)
#   3. gofmt -l                  — formatting check (catches trailing newlines etc.)
#   4. golangci-lint run         — misspell, errcheck, gosec, unconvert,
#                                  unparam (catches lint failures that
#                                  only CI's Lint job sees today). In a Go
#                                  repo a missing golangci-lint fails the
#                                  push CLOSED (gs-812); non-Go checkouts
#                                  skip it. See gu-lint-fastgate.
#
# These four gates all complete in well under a minute and catch the most
# common landing failures.
#
# No slow tier (gs-4s06):
#   This script intentionally does NOT run the full `go test ./...` suite. On
#   some hosts the suite needs >470s, which always blew past the pre-push hook's
#   360s hard wall — the slow tier failed BY CONSTRUCTION and the only way to
#   push was GT_SKIP_PREPUSH=1, training the bypass habit and burning ~6 min per
#   push attempt. The Refinery merge queue re-runs the full gates on every merge,
#   so a pre-push test tier was redundant defense. The full unit suite still runs
#   locally on demand via `make test` (no wall) and in CI on every merge.
#
# Env hygiene:
#   This script UNSETS GT_TOWN_ROOT and GT_ROOT before running gates. Some
#   gates (and any subprocess they spawn) call workspace.FindFromCwdOrError
#   which falls back to these env vars if CWD detection fails — a check can
#   pass locally (your shell has them set) but fail in CI (clean env).
#   Unsetting here matches CI. See commit 77c54398 for the canonical bug.
#
# Integration tests:
#   This script does NOT run `-tags=integration` tests — they require Docker +
#   dolt container and take ~5 minutes. They run NIGHTLY in CI
#   (nightly-integration.yml). To run locally: `make verify-integration`.
#
# Escape hatch (use sparingly):
#   git push --no-verify             — skip ALL hooks (standard git, NOT audited)
#
# Why pre-push, not pre-commit:
#   The existing pre-commit hook already runs go vet and a fast lint scoped
#   to staged files — that's the right granularity for "don't commit obvious
#   garbage." Pre-push runs these repo-wide gates once per push instead of
#   once per commit, so the cost is amortized and it's the last local line of
#   defense before CI.

set -u

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
# describing a gate event that bypassed normal verification. Today this only
# records fail-closed BLOCKS (a missing go/golangci-lint toolchain in a Go
# repo, gs-812) so witness tooling can audit ungated pushes. Best-effort:
# failures here MUST NOT block the push, because the only thing worse than an
# unaudited block is a script that refuses to push because audit logging broke.
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

The pre-push gates (build/vet/gofmt/lint) are the only backstop between a
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

# --- Capture town root for the gate-slot semaphore (gs-orsm) --------------
#
# The host-wide concurrency cap below locates its shared slot dir under the
# town root. Capture it from GT_TOWN_ROOT BEFORE the env scrub unsets it; fall
# back to walking up for the mayor/town.json marker so a manual push without
# the agent env still finds the shared dir. Empty means "no town root known" —
# the cap then skips (best-effort). See acquire_gate_slot below.

GATE_SEM_TOWN_ROOT="${GT_TOWN_ROOT:-}"
if [[ -z "$GATE_SEM_TOWN_ROOT" ]]; then
  _walk="$REPO_ROOT"
  while [[ -n "$_walk" && "$_walk" != "/" ]]; do
    if [[ -f "$_walk/mayor/town.json" ]]; then
      GATE_SEM_TOWN_ROOT="$_walk"
      break
    fi
    _walk=$(dirname "$_walk")
  done
fi

# --- Clean env so gates don't inherit developer's GT_TOWN_ROOT ------------
#
# Checks (and any subprocess they spawn) that need a workspace must create
# their own marker (mayor/town.json). If they rely on GT_TOWN_ROOT/GT_ROOT
# from the developer's shell, they're silently broken — CI has no such env.

unset GT_TOWN_ROOT GT_ROOT GT_SESSION GT_RIG GT_POLECAT

# --- Scrub git-repo env vars so hook-inherited GIT_DIR doesn't leak -------
#
# When git runs a hook (e.g. pre-push), it exports GIT_DIR, GIT_WORK_TREE,
# GIT_INDEX_FILE, etc. pointing at the pushing repo (see githooks(5) and
# git(1) "Discussion" on environment). Those vars are inherited by every
# child process this script spawns. A subprocess that runs `git` via os/exec
# and creates its own bare repo in a temp dir would otherwise read GIT_DIR
# from the environment instead of its own cmd.Dir and silently operate on the
# REAL pushing repo — how gu-h2ru's test-fixture commits reached the real
# remote. Unsetting these vars here matches the environment a plain shell sees.

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE \
      GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
      GIT_COMMON_DIR GIT_CEILING_DIRECTORIES GIT_NAMESPACE \
      GIT_PREFIX GIT_LITERAL_PATHSPECS GIT_GLOB_PATHSPECS \
      GIT_NOGLOB_PATHSPECS GIT_ICASE_PATHSPECS

# --- Route gate TMPDIR off a small /tmp tmpfs (gu-l4aue) ------------------
#
# On hosts where /tmp is a small tmpfs (16G on the Gas Town build host) shared
# by every rig gate, the heavy `go build`/`go vet`/`golangci-lint` runs below
# write their go-build/go-link working dirs there. When several gate runs
# execute concurrently their live dirs fill tmpfs and the linker dies mid-link
# with "no space left on device" — a flaky, town-wide false gate failure
# (matching the `go test ./...` ENOSPC the gt-done/refinery gates hit). The
# stale-dir sweep (gu-vzkyh) can't help: these dirs are live by definition.
#
# Fix: point TMPDIR/GOTMPDIR at disk-backed storage (the root fs, ~850G free)
# instead of the tmpfs, the same workaround the reporter applied by hand. Mirrors
# internal/util.GateTmpDir so the Go gate paths and this shell gate agree on the
# location. Honors the same knobs: GT_GATE_TMPDIR=off opts out; GT_GATE_TMPDIR_BASE
# overrides the base (default: the user cache dir, e.g. $HOME/.cache).
if [[ "${GT_GATE_TMPDIR:-}" != "off" ]]; then
  _gate_tmp_base="${GT_GATE_TMPDIR_BASE:-${XDG_CACHE_HOME:-$HOME/.cache}}"
  if [[ -n "$_gate_tmp_base" ]]; then
    _gate_tmp_dir="$_gate_tmp_base/gt-gate-tmp"
    if mkdir -p "$_gate_tmp_dir" 2>/dev/null; then
      export TMPDIR="$_gate_tmp_dir"
      export GOTMPDIR="$_gate_tmp_dir"
    fi
  fi
fi

# --- Host-wide concurrency cap on heavy go gate runs (gs-orsm) ------------
#
# The 2026-06-09 load-742 estop: many polecat/refinery worktrees ran this
# script's heavy go build/vet/lint gates CONCURRENTLY across all rigs with NO
# global cap. 25+ simultaneous Go linker processes exhausted swap and risked
# OOM (deacon CRITICAL hq-tghws). The `gt done --pre-verified` path was already
# capped by a cross-process counting semaphore (gu-0iyrn:
# internal/lock.FlockSemaphore over <townRoot>/.runtime/locks/gate-slots), but
# this pre-push path was uncapped — the recurrence vector.
#
# Fix: acquire one slot from that SAME semaphore pool before running the heavy
# gates, so pre-push and gt-done share a single host-wide bound on concurrent
# go gate runs. We reimplement the slot scan with bash flock(1), which takes
# the same flock(2) advisory lock as the Go side (syscall.Flock), so the two
# implementations interoperate on the identical slot-N.flock files. Cap size
# honors the same GT_GATE_CONCURRENCY knob the Go side reads (default 2);
# GT_GATE_SLOT_WAIT_SECONDS bounds the wait (default 600, matching the Go
# side's 10m) and lets tests shrink it.
#
# SYNC INVARIANT (gu-ym89r): the slot dir ($GT_TOWN_ROOT/.runtime/locks/gate-slots),
# the GT_GATE_CONCURRENCY env knob, and its default (2) are owned canonically by
# internal/lock/gateslot.go (GateSlotDir / GateSlotEnvVar / DefaultGateConcurrency).
# Every Go consumer — the polecat gt-done path AND the refinery merge gate — now
# acquires through that one helper. This bash hook computes the same path/knob
# inline so it joins the identical flock pool; if you change either side, change
# both or they silently split into two unsynchronized caps.
#
# Best-effort: if flock is unavailable, no town root is known, or all slots
# stay held past the wait, we proceed UNTHROTTLED rather than block a push. The
# cap is an overload guard, not a correctness gate. The held slot is released
# on script exit (the EXIT trap closes the fd; process exit would anyway).

GATE_SLOT_FD=""

release_gate_slot() {
  if [[ -n "$GATE_SLOT_FD" ]]; then
    # Group-scope the stderr redirect: a bare `exec FD>&- 2>/dev/null` would
    # redirect THIS shell's stderr to /dev/null permanently.
    { exec {GATE_SLOT_FD}>&-; } 2>/dev/null || true
    GATE_SLOT_FD=""
  fi
}
trap release_gate_slot EXIT

acquire_gate_slot() {
  command -v flock >/dev/null 2>&1 || return 0
  [[ -n "$GATE_SEM_TOWN_ROOT" ]] || return 0

  local slot_dir="$GATE_SEM_TOWN_ROOT/.runtime/locks/gate-slots"
  mkdir -p "$slot_dir" 2>/dev/null || return 0

  local n="${GT_GATE_CONCURRENCY:-2}"
  [[ "$n" =~ ^[0-9]+$ && "$n" -ge 1 ]] || n=2

  local wait_s="${GT_GATE_SLOT_WAIT_SECONDS:-600}"
  local waited=0 i fd
  while :; do
    for (( i=0; i<n; i++ )); do
      # Group-scope the stderr redirect — a bare `exec {fd}>file 2>/dev/null`
      # would redirect THIS shell's stderr to /dev/null for the rest of the
      # run (the `2>/dev/null` applies to the current shell via exec), silently
      # swallowing every later gate message. The group restores stderr after.
      if { exec {fd}>"$slot_dir/slot-$i.flock"; } 2>/dev/null; then
        if flock -n "$fd"; then
          GATE_SLOT_FD=$fd
          return 0
        fi
        { exec {fd}>&-; } 2>/dev/null || true
      fi
    done
    if (( waited >= wait_s )); then
      echo "pre-push: all $n gate slots held for ${wait_s}s — proceeding without the host-wide concurrency cap (gs-orsm)." >&2
      return 0
    fi
    sleep 2
    waited=$((waited + 2))
  done
}

# Take a slot before the heavy gates below. Held through the gates and released
# on exit. Bounds concurrent build/vet/lint runs host-wide.
acquire_gate_slot

# --- Upfront banner (gu-enqh0) -------------------------------------------
#
# A push launched in a background / non-tty context runs these gates before
# contacting origin, with no network activity and (until the first gate prints)
# no visible output — so it reads as a hung push and invites a spurious retry
# that just re-runs the same gates. Announce the work the instant it begins,
# before the first gate, so even a backgrounded push shows immediate progress
# and the caller knows the wait is expected.
echo "pre-push: running fast gates (build/vet/gofmt/lint) before contacting origin — expected, the push is not hung." >&2

# --- FAST GATE 1: go build ------------------------------------------------

echo "pre-push: [fast] go build ./... (compile check)" >&2
if ! go build ./... 2>&1; then
  cat >&2 <<'EOF'

✗ Push rejected: 'go build ./...' failed.

Fix compile errors before pushing. CI will reject the same build failures
but with a ~5min round-trip cost.

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
EOF
  exit 1
fi

# --- FAST GATE 4: golangci-lint -------------------------------------------
#
# CI's Lint job (golangci-lint with .golangci.yml) catches misspell, errcheck,
# gosec, unconvert, unparam findings that go vet does NOT. Same broken-main
# window as gofmt: a push passes build/vet/gofmt locally, lands, and only THEN
# does golangci-lint catch issues — by which point main is briefly broken.
#
# The check is fast (~10-30s on this codebase) and catches the failure modes
# the other gates miss.
#
# If golangci-lint isn't installed locally, behavior splits on repo type
# (gs-812): in a Go repo this gate fails CLOSED (block + audit), because a
# missing linter is exactly how lint failures slipped onto main in the window
# between push and CI catch. Non-Go checkouts skip gracefully. Install via
# `mise use -g golangci-lint@2.11.4` (corp-proxy-safe) at the version lock from
# .github/workflows/ci.yml; the block message lists the install routes.
# (gu-lint-fastgate, gu-8z815)

if command -v golangci-lint >/dev/null 2>&1; then
  echo "pre-push: [fast] golangci-lint run (static analysis)" >&2
  if ! golangci-lint run --timeout=5m 2>&1; then
    cat >&2 <<EOF

✗ Push rejected: golangci-lint found findings.

Fix the issues above (or add a .golangci.yml exclusion if the linter is
mechanically wrong about an external API string), then re-push.

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

Fix: install golangci-lint v2.11.4 (matches CI), then re-push.

  Preferred (works behind the corp Go-proxy DNS sinkhole — pulls the GitHub
  release binary directly, no proxy.golang.org):
    mise use -g golangci-lint@2.11.4

  Or download the pinned release binary straight from GitHub:
    https://github.com/golangci/golangci-lint/releases/tag/v2.11.4
    (extract and put 'golangci-lint' on your PATH, e.g. ~/.local/bin)

  Fallback (only if your Go module proxy reaches proxy.golang.org —
  this FAILS on standard Amazon dev hosts where it is DNS-sinkholed):
    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4

To bypass ALL hooks anyway (not recommended): git push --no-verify.
EOF
  exit 1
else
  echo "pre-push: [fast] golangci-lint not installed and no go.mod — non-Go checkout, skipping lint gate" >&2
fi

echo "pre-push: all gates passed ✓" >&2
exit 0

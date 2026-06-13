+++
name = "verify-build"
description = "Run workspace-wide Brazil build when a casc_* polecat completes work"
version = 1

[gate]
type = "cooldown"
duration = "30m"

[tracking]
labels = ["plugin:verify-build", "category:build"]
digest = true

[execution]
timeout = "30m"
notify_on_failure = true
severity = "high"
+++

# Verify Build

Run `brazil-recursive-cmd --allPackages brazil-build` across the CodegenAgentScheduler workspace when any package changes. Catches cross-package breakage before it reaches the pipeline.

## Trigger

This plugin uses a cooldown gate (30m) so rapid successive polecat completions are batched into a single build. It can also be triggered manually:

```bash
gt plugin run verify-build
gt plugin run verify-build --force  # bypass cooldown
```

**Integration with polecat workflow:** Add to each `casc_*` polecat's post-done flow:

```bash
gt plugin run verify-build --force
```

Or trigger from MeshClaw cron / witness callback after `POLECAT_DONE`.

## Step 1: Pull Latest

Ensure the Brazil workspace has the latest from all packages.

**Historical failure** (fixed by gu-qt47 / gt-i4fht): the original loop was
`git pull --ff-only origin mainline 2>/dev/null || true`. When a package
checkout had divergent local commits, `--ff-only` failed, the failure was
silently swallowed, and the stale checkout persisted across verify-build runs
— producing false build failures for hours (e.g. casw-09w / commit 7ce12d3:
14 commits behind origin with 2 divergent local commits).

The hardened loop below:

1. Fetches origin first (so we can compare local vs origin HEAD).
2. Attempts `git pull --ff-only`; captures stderr instead of suppressing it.
3. On failure, classifies the divergence:
   - **Already up-to-date / ahead only** → no action, log `[OK]`.
   - **Behind only (fast-forwardable but pull still failed)** → log `[WARN]`
     and surface the stderr.
   - **Diverged, but all local commits are already on origin by patch-id**
     → hard-reset to `origin/mainline` (self-heal). Log `[HEAL]`.
   - **Diverged with real local-only work** → log `[STALE]`, append to the
     divergent-package list, and DO NOT silently continue.
4. After the loop, if any package is truly divergent, file a P1 bead in
   `codegen_ws` (with dedup) and abort Step 2 — the build would use stale
   code anyway, so running it wastes time and emits misleading failures.

```bash
cd /workplace/canewiw/CodegenAgentScheduler || { echo "[WARN] workspace missing, skipping verify-build"; exit 0; }
DIVERGENT_PKGS=()
HEALED_PKGS=()
WARN_PKGS=()

for pkg in src/*/; do
  pkg_name=$(basename "$pkg")
  (
    cd "$pkg" || exit 0

    # Fetch first so we can reason about local vs origin.
    if ! git fetch origin mainline 2>/dev/null; then
      echo "[WARN] $pkg_name: git fetch origin mainline failed"
      exit 2
    fi

    # Try the fast-forward pull. Capture stderr so we can surface it.
    pull_err=$(git pull --ff-only origin mainline 2>&1 >/dev/null)
    pull_rc=$?
    if [ $pull_rc -eq 0 ]; then
      echo "[OK] $pkg_name: up to date"
      exit 0
    fi

    # Pull failed. Classify.
    local_head=$(git rev-parse HEAD 2>/dev/null)
    origin_head=$(git rev-parse origin/mainline 2>/dev/null)
    merge_base=$(git merge-base HEAD origin/mainline 2>/dev/null)

    if [ -z "$local_head" ] || [ -z "$origin_head" ] || [ -z "$merge_base" ]; then
      echo "[WARN] $pkg_name: could not resolve refs; pull stderr: $pull_err"
      exit 2
    fi

    if [ "$local_head" = "$origin_head" ]; then
      # Pull reported failure but we're actually in sync. Rare; log and continue.
      echo "[OK] $pkg_name: already at origin/mainline"
      exit 0
    fi

    if [ "$merge_base" = "$local_head" ]; then
      # Fast-forwardable but pull still failed (dirty worktree, lock, etc.)
      echo "[WARN] $pkg_name: behind origin but pull failed: $pull_err"
      exit 2
    fi

    # We have local-only commits. Check whether each is already on origin
    # by patch-id. If all are, hard-reset is safe (self-heal).
    # Note: use tformat (not format) so the last SHA has a trailing newline;
    # otherwise `while read` silently drops the final record and a single
    # local-only commit would be mis-classified as HEAL.
    safe_to_reset=1
    while read -r local_sha; do
      [ -z "$local_sha" ] && continue
      local_pid=$(git show "$local_sha" 2>/dev/null | git patch-id --stable 2>/dev/null | awk '{print $1}')
      if [ -z "$local_pid" ]; then
        safe_to_reset=0
        break
      fi
      # Search origin/mainline for a matching patch-id in the window since merge-base.
      match=$(git log "$merge_base..origin/mainline" --pretty=tformat:'%H' 2>/dev/null \
        | while read -r origin_sha; do
            origin_pid=$(git show "$origin_sha" 2>/dev/null | git patch-id --stable 2>/dev/null | awk '{print $1}')
            [ "$origin_pid" = "$local_pid" ] && { echo "$origin_sha"; break; }
          done)
      if [ -z "$match" ]; then
        safe_to_reset=0
        break
      fi
    done < <(git log "$merge_base..HEAD" --pretty=tformat:'%H' 2>/dev/null)

    if [ "$safe_to_reset" = "1" ]; then
      echo "[HEAL] $pkg_name: all local commits already on origin by patch-id; hard-resetting to origin/mainline"
      git reset --hard origin/mainline >/dev/null 2>&1
      exit 3
    fi

    echo "[STALE] $pkg_name: divergent local branch with unique commits; skipping"
    echo "         local_head=$local_head origin_head=$origin_head merge_base=$merge_base"
    echo "         pull stderr: $pull_err"
    exit 4
  )
  rc=$?
  case $rc in
    0) : ;;
    2) WARN_PKGS+=("$pkg_name") ;;
    3) HEALED_PKGS+=("$pkg_name") ;;
    4) DIVERGENT_PKGS+=("$pkg_name") ;;
  esac
done

echo ""
echo "=== Step 1 summary ==="
echo "  healed (hard-reset to origin): ${#HEALED_PKGS[@]} — ${HEALED_PKGS[*]:-none}"
echo "  warnings:                      ${#WARN_PKGS[@]} — ${WARN_PKGS[*]:-none}"
echo "  truly divergent:               ${#DIVERGENT_PKGS[@]} — ${DIVERGENT_PKGS[*]:-none}"

if [ ${#DIVERGENT_PKGS[@]} -gt 0 ]; then
  # File a bead and abort — Step 2 would build stale code.
  divergent_list=$(printf '%s\n' "${DIVERGENT_PKGS[@]}")

  # Dedup: reuse an existing open divergent bead if present.
  existing=$(cd ~/gt/codegen_ws && bd list -l stale-checkout,plugin:verify-build --status open --json 2>/dev/null \
    | jq -r '.[0].id // empty' 2>/dev/null)

  if [ -n "$existing" ]; then
    echo "[BEAD] appending to existing divergent-checkout bead $existing"
    cd ~/gt/codegen_ws && bd update "$existing" --comment "Divergent packages on verify-build $(date -u +%FT%TZ):
$divergent_list" 2>/dev/null || true
  else
    echo "[BEAD] filing new divergent-checkout bead"
    cd ~/gt/codegen_ws && bd create "verify-build: divergent local checkouts blocking build" \
      -p P1 \
      -t bug \
      -l stale-checkout,plugin:verify-build \
      -d "One or more packages in /workplace/canewiw/CodegenAgentScheduler/src
have local commits that are NOT on origin/mainline. Step 1 'Pull Latest' cannot
self-heal these safely, so Step 2 would build stale code.

Divergent packages:
$divergent_list

Manual remediation per package:
  cd /workplace/canewiw/CodegenAgentScheduler/src/<pkg>
  git status
  git log --oneline origin/mainline..HEAD   # local-only commits
  # Either rebase/push or discard; then:
  git fetch origin mainline && git reset --hard origin/mainline

After all divergent packages are reconciled, rerun:
  gt plugin run verify-build --force
" 2>/dev/null || true
  fi

  echo ""
  echo "ABORT: divergent checkouts present; skipping Step 2 (build would use stale code)."
  exit 1
fi
```

## Step 1.5: Probe for Stale Untracked Test Files

**Historical failure** (gu-ja4e / codegen_ws cws-25z, 2026-05-29): Step 1 only
reconciles divergent *committed* state. It does not touch untracked files.
Stale `*.test.ts` / `*.test.tsx` files left in `src/<pkg>/src/__tests__/` —
typically strays from another rig that referenced now-deleted source modules
— persisted across 28+ verify-build runs and produced repeated false build
failures (TypeScript "Cannot find module" errors), masking real signal.

This step scans each package for untracked test files and classifies them:

- **Stray** — no corresponding source file under `src/` (excluding
  `__tests__/`). The test references a module that does not exist;
  almost certainly a leftover from another rig. Auto-removed (`rm` of the
  specific file — never `git clean -fdx`, which would nuke `node_modules` /
  `dist`).
- **Keep** — matching source file exists. Could be legitimate WIP. Logged
  and surfaced in a P2 triage bead so a human can decide.

```bash
STRAY_REMOVED=()
LEFTOVER_TESTS=()

for pkg in src/*/; do
  pkg_name=$(basename "$pkg")
  (
    cd "$pkg" || exit 0

    # Untracked test files under any __tests__/ directory in this package.
    untracked=$(git ls-files --others --exclude-standard 2>/dev/null \
      | grep -E '(^|/)__tests__/.*\.(test|spec)\.(ts|tsx|js|jsx)$' || true)
    [ -z "$untracked" ] && exit 0

    strays=""
    keeps=""
    while IFS= read -r f; do
      [ -z "$f" ] && continue

      # Resolve probable matching source file: foo.test.ts → foo.ts (etc.)
      # under the package's src/, excluding the __tests__ tree itself.
      fname=$(basename "$f")
      stem=$(echo "$fname" | sed -E 's/\.(test|spec)\.(ts|tsx|js|jsx)$//')
      match=$(find src -type f \
                \( -name "$stem.ts"  -o -name "$stem.tsx" \
                -o -name "$stem.js"  -o -name "$stem.jsx" \) \
                -not -path '*/__tests__/*' 2>/dev/null | head -1)

      if [ -z "$match" ]; then
        echo "[STRAY] $pkg_name: $f (no matching source $stem.{ts,tsx,js,jsx})"
        # Targeted removal — single file, never recursive.
        rm -f "$f"
        strays+="${pkg_name}/${f}"$'\n'
      else
        echo "[KEEP]  $pkg_name: $f (matches $match — leaving for triage)"
        keeps+="${pkg_name}/${f}"$'\n'
      fi
    done <<< "$untracked"

    # Emit per-package summary on a single line each so the parent shell
    # can collect them without subshell variable scope issues.
    [ -n "$strays" ] && printf 'STRAY_BLOCK_BEGIN\n%sSTRAY_BLOCK_END\n' "$strays"
    [ -n "$keeps" ]  && printf 'KEEP_BLOCK_BEGIN\n%sKEEP_BLOCK_END\n'  "$keeps"
  )
done > /tmp/verify-build-untracked.$$.log 2>&1
cat /tmp/verify-build-untracked.$$.log

# Collect leftovers (untracked tests with matching source) for the bead.
LEFTOVERS=$(awk '/^KEEP_BLOCK_BEGIN$/{f=1;next} /^KEEP_BLOCK_END$/{f=0} f' \
  /tmp/verify-build-untracked.$$.log)
STRAYS=$(awk '/^STRAY_BLOCK_BEGIN$/{f=1;next} /^STRAY_BLOCK_END$/{f=0} f' \
  /tmp/verify-build-untracked.$$.log)
rm -f /tmp/verify-build-untracked.$$.log

echo ""
echo "=== Step 1.5 summary ==="
if [ -n "$STRAYS" ]; then
  echo "  strays auto-removed:"
  printf '    %s\n' $STRAYS
else
  echo "  strays auto-removed: none"
fi
if [ -n "$LEFTOVERS" ]; then
  echo "  untracked tests retained (need triage):"
  printf '    %s\n' $LEFTOVERS
else
  echo "  untracked tests retained: none"
fi

# File a P2 triage bead only for the LEFTOVERS — strays are handled.
# Do NOT abort on leftovers; the build will catch real problems and the
# bead carries the breadcrumb for follow-up.
if [ -n "$LEFTOVERS" ]; then
  leftover_list=$(printf '%s\n' $LEFTOVERS)
  existing=$(cd ~/gt/codegen_ws && bd list -l untracked-tests,plugin:verify-build --status open --json 2>/dev/null \
    | jq -r '.[0].id // empty' 2>/dev/null)

  if [ -n "$existing" ]; then
    echo "[BEAD] appending to existing untracked-tests bead $existing"
    cd ~/gt/codegen_ws && bd update "$existing" --comment "Untracked test files seen on verify-build $(date -u +%FT%TZ):
$leftover_list" 2>/dev/null || true
  else
    echo "[BEAD] filing new untracked-tests triage bead"
    cd ~/gt/codegen_ws && bd create "verify-build: untracked test files in src/__tests__/ need triage" \
      -p P2 \
      -t task \
      -l untracked-tests,plugin:verify-build \
      -d "Step 1.5 of verify-build found untracked *.test.* / *.spec.* files
under src/<pkg>/src/__tests__/ that have a matching source file — i.e. they
are NOT obvious strays from another rig and may be legitimate WIP. They were
left in place; please decide whether to commit, stash, or delete them.

Files (package/path):
$leftover_list

Inspect:
  cd /workplace/canewiw/CodegenAgentScheduler/src/<pkg>
  git status
  git diff --no-index /dev/null <path>   # see what's in the file

Then either:
  git add <path> && git commit            # commit it
  git stash push -- <path>                # stash for later
  rm <path>                               # discard

After triage, rerun:
  gt plugin run verify-build --force
" 2>/dev/null || true
  fi
fi
```

## Step 2: Run Workspace Build

Run the workspace-wide build with `--continue` so a single package failure does
not mask others, and capture the full log + the list of packages that failed.
`--continue` is essential: without it the run stops at the first failure and we
cannot tell whether the rest of the workspace is healthy.

```bash
cd /workplace/canewiw/CodegenAgentScheduler
BUILD_LOG=/tmp/verify-build.$$.log
brazil-recursive-cmd --allPackages --continue brazil-build > "$BUILD_LOG" 2>&1
BUILD_RC=$?

# Identify which packages actually failed. brazil-recursive-cmd prints a
# per-package failure banner; capture the package names from it. Fall back to
# the generic "in package <name>" / "Command failed" markers if the banner
# format differs across brazil versions.
FAILED_PKGS=$(grep -oE 'Recursive command failed in [^ ]+|failed in package [^ ]+|^FAILED: [^ ]+' "$BUILD_LOG" \
  | sed -E 's/.* (in package |in |FAILED: )//' \
  | sed -E 's/-[0-9].*$//' \
  | sort -u)

echo "=== Step 2 summary ==="
echo "  exit code: $BUILD_RC"
echo "  failed packages: ${FAILED_PKGS:-none}"
```

If `BUILD_RC` is 0 → build is green → go to **Step 4** (record success).
If `BUILD_RC` is non-zero → **DO NOT file a P1 yet.** Go to **Step 2.5** to
disambiguate a real source defect from a `--allPackages` concurrency-harness
race (shared `/tmp`, concurrent `cdk synth` writing `cdk.out`, resource
exhaustion). This is the gu-s57lj fix: per-flake P1 dispatch on a harness race
burns a full polecat+refinery cycle and finds no source defect.

## Step 2.5: Standalone Re-Verification (Flake vs. Real Defect)

The `--allPackages` harness builds every package concurrently. The recurring
false failures (cws-87im reserved-build-path, cws-xqvr resource-exhaustion
race, cws-m4jr cdk.out assets.json ENOENT) all share one signature: the package
**builds green when built alone**. A real source defect fails both ways.

So before filing anything, rebuild each failed package **standalone**, in its
own process, after pre-cleaning its `cdk.out` (the concurrency hot-spot). A
package that passes standalone is a harness flake — record a low-noise digest,
do NOT file a P1, do NOT dispatch a polecat. Only packages that ALSO fail
standalone are real defects worth a P1.

```bash
cd /workplace/canewiw/CodegenAgentScheduler

REAL_DEFECTS=()      # failed --allPackages AND failed standalone → real
HARNESS_FLAKES=()    # failed --allPackages but PASSED standalone → flake

for pkg_name in $FAILED_PKGS; do
  pkg_dir="src/$pkg_name"
  [ -d "$pkg_dir" ] || { echo "[SKIP] $pkg_name: no src dir"; continue; }

  # Pre-clean this package's cdk.out so a stale/locked artifact from the
  # concurrent run can't poison the standalone retry (same race as cws-m4jr).
  cdk_out="$pkg_dir/build/cdk.out"
  if [ -d "$cdk_out" ]; then
    trash="${cdk_out}.retry.$$"
    if mv "$cdk_out" "$trash" 2>/dev/null; then
      rm -rf "$trash" &
    else
      rm -rf "$cdk_out" 2>/dev/null || true
    fi
  fi
  wait

  STANDALONE_LOG="/tmp/verify-build-standalone.$pkg_name.$$.log"
  ( cd "$pkg_dir" && brazil-build ) > "$STANDALONE_LOG" 2>&1
  standalone_rc=$?

  if [ "$standalone_rc" -eq 0 ]; then
    echo "[FLAKE] $pkg_name: failed under --allPackages but builds GREEN standalone"
    HARNESS_FLAKES+=("$pkg_name")
  else
    echo "[DEFECT] $pkg_name: fails standalone too (rc=$standalone_rc) — real source defect"
    REAL_DEFECTS+=("$pkg_name")
  fi
done

echo ""
echo "=== Step 2.5 summary ==="
echo "  real defects (file P1):     ${REAL_DEFECTS[*]:-none}"
echo "  harness flakes (no P1):     ${HARNESS_FLAKES[*]:-none}"
```

- If `REAL_DEFECTS` is non-empty → go to **Step 3** (file a P1 for the real
  defects only).
- If `REAL_DEFECTS` is empty and `HARNESS_FLAKES` is non-empty → the
  `--allPackages` build failed purely on harness races. **Skip Step 3.** Record
  a deduped flake digest (Step 3.5) and finish at Step 4. No P1, no polecat.
- If both are empty (build failed but no package name parsed) → treat as a
  harness/infra failure: record the flake digest (Step 3.5) with the raw log
  tail; do not file a P1.

## Step 3: File Failure Bead — Real Defects Only

Only reached when Step 2.5 confirmed at least one package fails **standalone**.
File a P1 bead in codegen_ws scoped to the real defect(s), and include the
standalone failure tail (not the concurrent log — the standalone log is the
clean reproduction).

```bash
defect_list=$(printf '%s\n' "${REAL_DEFECTS[@]}")
standalone_tail=$(for p in "${REAL_DEFECTS[@]}"; do
  echo "--- $p (standalone) ---"; tail -n 30 "/tmp/verify-build-standalone.$p.$$.log"
done)

# Dedup: reuse an existing open build-failed bead if present.
existing=$(cd ~/gt/codegen_ws && bd list -l build-failed,plugin:verify-build --status open --json 2>/dev/null \
  | jq -r '.[0].id // empty' 2>/dev/null)

if [ -n "$existing" ]; then
  cd ~/gt/codegen_ws && bd update "$existing" --comment "Reconfirmed real defect $(date -u +%FT%TZ) — fails standalone:
$defect_list

$standalone_tail"
else
  cd ~/gt/codegen_ws && bd create "Workspace build failed (confirmed standalone)" \
    -p P1 \
    -l build-failed,plugin:verify-build \
    -d "These packages failed under brazil-recursive-cmd --allPackages AND fail
when built standalone — a real source defect, not a harness race:

$defect_list

Standalone build output (clean reproduction):
$standalone_tail"
fi
```

## Step 3.5: Record Harness-Flake Digest (No P1, No Dispatch)

Reached when the `--allPackages` build failed but every failed package builds
green standalone (or no package could be isolated). This is the gu-s57lj
class: a concurrency-harness race, NOT a source defect. We must NOT file a P1
(it would dispatch a polecat that finds nothing to fix). Instead append to a
single low-priority, deduped digest so the pattern stays observable for whoever
owns the harness concurrency fix — without burning a refinery cycle per flake.

```bash
flake_list=$(printf '%s\n' "${HARNESS_FLAKES[@]:-<unparsed>}")
concurrent_tail=$(tail -n 30 "$BUILD_LOG")

existing=$(cd ~/gt/codegen_ws && bd list -l harness-flake,plugin:verify-build --status open --json 2>/dev/null \
  | jq -r '.[0].id // empty' 2>/dev/null)

if [ -n "$existing" ]; then
  cd ~/gt/codegen_ws && bd update "$existing" --comment "verify-build --allPackages harness flake $(date -u +%FT%TZ):
packages green standalone, failed only under concurrent build:
$flake_list

Concurrent log tail:
$concurrent_tail"
else
  cd ~/gt/codegen_ws && bd create "verify-build: --allPackages concurrency-harness flakes (no source defect)" \
    -p P3 \
    -t bug \
    -l harness-flake,plugin:verify-build \
    -d "The brazil-recursive-cmd --allPackages verify-build harness failed, but
the failed package(s) build GREEN standalone — a concurrency race (shared /tmp,
concurrent cdk synth on cdk.out, resource exhaustion), not a source defect.

This bead is the deduped digest for these flakes; it intentionally does NOT
dispatch a polecat per occurrence (see gu-s57lj). The durable fix lives in the
harness: per-package isolated cdk.out paths, a concurrency throttle, or
retry-on-known-flake.

Packages seen flaking standalone-green:
$flake_list

Most recent concurrent log tail:
$concurrent_tail"
fi
```

## Step 4: Record Result

```bash
# result = success | failure (real defect) | flake (harness race, no P1)
cd ~/gt/codegen_ws && bd create "verify-build: <result>" \
  -t chore --ephemeral \
  -l type:plugin-run,plugin:verify-build,result:<success|failure|flake> \
  --silent

rm -f "$BUILD_LOG" /tmp/verify-build-standalone.*.$$.log 2>/dev/null || true
```

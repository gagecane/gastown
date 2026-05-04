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
timeout = "15m"
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

## Step 2: Run Workspace Build

```bash
cd /workplace/canewiw/CodegenAgentScheduler
brazil-recursive-cmd --allPackages brazil-build
```

If the build succeeds → go to Step 4.
If the build fails → go to Step 3.

## Step 3: File Failure Bead

On build failure, file a P1 bead in codegen_ws:

```bash
cd ~/gt/codegen_ws && bd create "Workspace build failed" \
  -p P1 \
  -l build-failed,plugin:verify-build \
  -d "brazil-recursive-cmd --allPackages brazil-build failed.
Exit code: <code>
Last 30 lines of output:
<tail of build log>"
```

**Dedup:** Before creating, check for existing open build-failed beads:

```bash
cd ~/gt/codegen_ws && bd list -l build-failed --status open
```

If one exists, add a comment instead of creating a duplicate.

## Step 4: Record Result

```bash
cd ~/gt/codegen_ws && bd create "verify-build: <result>" \
  -t chore --ephemeral \
  -l type:plugin-run,plugin:verify-build,result:<success|failure> \
  --silent
```

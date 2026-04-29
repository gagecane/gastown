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

Ensure the Brazil workspace has the latest from all packages:

```bash
cd /workplace/canewiw/CodegenAgentScheduler
for pkg in src/*/; do
  (cd "$pkg" && git pull --ff-only origin mainline 2>/dev/null || true)
done
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

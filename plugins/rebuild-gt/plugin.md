+++
name = "rebuild-gt"
description = "Rebuild stale gt binary from gastown source"
version = 3

[gate]
type = "cooldown"
duration = "1h"

[tracking]
labels = ["plugin:rebuild-gt", "rig:gastown", "category:maintenance"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "medium"
+++

# Rebuild gt Binary

Checks if the gt binary is stale (built from older commit than HEAD) and rebuilds.

**SAFETY**: This plugin MUST only rebuild forward (binary ancestor of HEAD) and
only from the main branch. Rebuilding to an older or diverged commit caused a
crash loop where every new session's startup hook failed, the witness respawned
it, and the loop repeated every 1-2 minutes.

## Gate Check

The Deacon evaluates this before dispatch. If gate closed, skip.

## Detection

Check binary staleness:

```bash
gt stale --json
```

Parse the JSON output and check these fields:
- If `"stale": false` → record success wisp and exit early (binary is fresh)
- If `"safe_to_rebuild": false` → **DO NOT REBUILD**. Record a skip wisp and exit.
  This means the repo is on a non-main branch or HEAD is not a descendant of the
  binary commit (would be a downgrade).
- If `"safe_to_rebuild": true` → proceed to build

If `safe_to_rebuild` is false, record a skip wisp:
```bash
gt plugin record-run --plugin rebuild-gt --result skipped --rig gastown \
  --title "Plugin: rebuild-gt [skipped]" \
  --description "Skipped: not safe to rebuild (forward=$FORWARD, main=$ON_MAIN)" >/dev/null 2>&1 || true
```

## Pre-flight Checks

Before building, verify the source repo is clean and on main:

```bash
cd ~/gt/gastown_upstream/mayor/rig    # or ~/gt/gastown/mayor/rig for pre-fork towns
git status --porcelain  # Must be clean
git branch --show-current  # Must be "main"
```

If either check fails, skip the rebuild and record a wisp.

## Sync Local Checkout Before Staleness Check

`gt stale` compares the binary's embedded commit to the *local* HEAD of the
source rig. If the local checkout is behind origin/main, stale incorrectly
reports "fresh". Before calling `gt stale`, fetch origin and fast-forward
main (only if the repo is clean, on main, and the local tip is an ancestor
of origin/main). If local main has diverged from origin/main, skip the
rebuild and record a wisp — manual attention required.

This was added after gu-wcxv: the plugin missed gu-j1f7 / gu-pcm5 / gu-j98v
for hours because the local checkout lagged origin/main.

## Action

Rebuild from source (the mayor/rig directory is the canonical source). The
plugin auto-discovers the rig name, preferring `gastown_upstream` (the fork)
over `gastown` for backward compat with pre-fork towns:

```bash
cd ~/gt/gastown_upstream/mayor/rig && make build && make safe-install
```

**IMPORTANT**: Use `make safe-install` (not `make install`) to avoid restarting
the daemon while sessions are active. safe-install replaces the binary but does
NOT restart the daemon — sessions will pick up the new binary on their next cycle.

**Daemon restart**: The running daemon process continues executing its old
in-memory binary until restarted. On successful install, the plugin files a
bead with label `type:daemon-restart-pending` so mayor (or a dedicated
daemon-restart-dog plugin) can coordinate a safe restart. Without this,
daemon-resident logic (e.g. main_branch_test) keeps running the old code
even after the on-disk binary is upgraded. Added for gu-wcxv.

## Record Result

On success:
```bash
gt plugin record-run --plugin rebuild-gt --result success --rig gastown \
  --title "Plugin: rebuild-gt [success]" \
  --description "Rebuilt gt: $OLD → $NEW ($N commits)" >/dev/null 2>&1 || true
```

On failure:
```bash
gt plugin record-run --plugin rebuild-gt --result failure --rig gastown \
  --title "Plugin: rebuild-gt [failure]" \
  --description "Build failed: $ERROR" >/dev/null 2>&1 || true

gt escalate --severity=medium \
  --subject="Plugin FAILED: rebuild-gt" \
  --body="$ERROR" \
  --source="plugin:rebuild-gt"
```

+++
name = "gobuild-tmp-sweep"
description = "Sweep stale go-build/go-link temp dirs from /tmp to prevent ENOSPC gate failures"
version = 1

[gate]
type = "cooldown"
duration = "1h"

[tracking]
labels = ["plugin:gobuild-tmp-sweep", "category:cleanup"]
digest = true

[execution]
type = "script"
timeout = "2m"
notify_on_failure = true
severity = "low"
+++

# Go-build /tmp Sweep

Go's toolchain creates `go-build*` (and, during linking, `go-link*`) working
directories under `$TMPDIR` (`/tmp` by default). On a normal exit the toolchain
removes them — but when a gate run is **killed** (SIGTERM/SIGKILL during a
pre-push stall or a concurrent gate that loses a race), the directory is
orphaned. On this host `/tmp` is a 16G tmpfs shared by every rig's merge gate,
so leaked dirs accumulate until `/tmp` fills and gates start failing with
`no space left on device` — a flaky, town-wide false build/gate failure.

This plugin runs every hour and deletes `go-build*`/`go-link*` directories under
`$TMPDIR` older than a threshold. The age cutoff (default 30 minutes) is long
enough that an in-flight build is never touched: a live gate's working dir is
freshly mtime'd, while a leaked one stops being modified the moment its build
dies.

Related: gu-2bvvz (killed gate runs were a major leak source). Discovered from
refinery gate ops (two ENOSPC-induced false gate failures in one session,
2026-06-12).

## Configuration

- `GT_GOBUILD_SWEEP_TMPDIR` — directory to sweep (default: `$TMPDIR`, else `/tmp`)
- `GT_GOBUILD_SWEEP_MIN_AGE_MIN` — minimum age in minutes before a dir is eligible (default: `30`)
- `GT_GOBUILD_SWEEP_DRY_RUN` — set to `1` to log what would be deleted without deleting

## What it does

Execution is a deterministic `run.sh` — no AI interpretation. It finds
`go-build*` and `go-link*` directories at the top level of the sweep dir whose
mtime is older than the age cutoff and removes them, then reports the count
reclaimed and current `/tmp` usage.

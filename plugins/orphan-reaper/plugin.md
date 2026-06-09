+++
name = "orphan-reaper"
description = "Reap orphaned (ppid==1) claude-code otelcol/MCP helper processes"
version = 1

[gate]
type = "cooldown"
duration = "30m"

[tracking]
labels = ["plugin:orphan-reaper", "category:cleanup"]
digest = true

[execution]
timeout = "2m"
notify_on_failure = true
severity = "low"
+++

# Orphan Reaper

`claude-code` sessions spawn helper children — `otelcol-contrib` telemetry
sidecars and MCP servers. When a session dies abruptly those children are
reparented to PID 1 (`ppid == 1`) and run forever, leaking CPU, PID-table
slots, and memory (~388 procs / ~15 GB observed on this host 2026-06-08/09).

This plugin is a periodic external sweep that terminates those parentless
helpers. It is a band-aid for the upstream reap-on-exit gap in `claude-code`,
not a cure (see `orphan-reaper-spec.md` §1, §11 Q2).

The dog runs `run.sh` verbatim — deterministic shell, no AI interpretation
(a pure `/proc` scan plus signals). The same script backs both this Gas Town
daemon-dog plugin (preferred) and the standalone-cron fallback (spec §10, §12).

## Gate: cooldown (active)

This manifest is **active** on a 30-minute cooldown gate (spec §7, §12.3): the
daemon auto-dispatches the reaper at most once per 30-minute window.

It originally shipped with `[gate] type = "manual"` as a code-enforced safety
gate — the daemon's `filterDispatchablePlugins` (`internal/daemon/handler.go`)
**never** auto-dispatches manual-gate plugins, so the reaper landed discoverable
and runnable on demand but could not auto-kill anything until an operator
promoted it. (A `[execution.env]` dry-run default would have been inert: the
`Execution` struct has no env field and `FormatMailBody` dispatches a bare
`cd … && bash run.sh` with no env prefix — see follow-up gu-jqrwk.) The manual
gate was promoted to cooldown in operator step E (bead gu-ilbht) after live
validation confirmed dry-run parity, a clean synthesized-orphan reap, and a
successful end-to-end daemon dispatch.

Trigger a controlled out-of-cycle run on demand:

```bash
gt dog dispatch --plugin orphan-reaper            # one manual run
gt dog dispatch --plugin orphan-reaper --dry-run  # show what would dispatch
```

The dry-run behavior lives in `run.sh` itself (`REAPER_DRY_RUN`, default per
spec §5); the script handles the safety default, not this manifest.

## Cadence (once promoted)

Leaks accrue over hours-to-days and a run is sub-second, so 30 min is cheap and
reclaims faster during session churn (spec §11 Q3). The daemon guarantees
single-dispatch per cooldown window, so the `flock` inside `run.sh` is redundant
under the daemon and matters only for the standalone-cron fallback.

## Exit contract (spec §12.4)

Communicates purely through exit code + stdout:

- **Exit 0** — success, *including* the common "found nothing" run *and* the
  "killed N orphans" run. Reaping orphans is the success path, not an error.
- **Exit non-zero** — genuine operational failure only (cannot read `/proc`,
  or processes survive `SIGKILL` + grace). Because `notify_on_failure = true`
  at `severity = low`, the daemon escalates these — kept rare by design.

`run.sh` never masks the real exit code with `|| true` — the `dolt-backup`
scar (gu-8xvpw) is exactly that mistake swallowing a real failure. `stdout` is
the digest body: the one-line summary
(`candidates=N term=N kill=N survivors=N dry_run=0`) plus per-kill lines, fed
to the dog digest pipeline via `digest = true`.

## Rollback

Delete `plugins/orphan-reaper/` — the daemon stops dispatching next cycle.
No persistent system state, nothing else to undo (spec §10).

+++
name = "orphan-reaper"
description = "Reap orphaned (ppid==1) claude-code otelcol/MCP helper processes"
version = 1

[gate]
type = "manual"

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

## Safety: manual gate (ships disabled)

This manifest ships with `[gate] type = "manual"` on purpose. The daemon's
`filterDispatchablePlugins` (`internal/daemon/handler.go`) **never** auto-dispatches
manual-gate plugins, so the reaper lands discoverable and runnable on demand but
**cannot auto-kill anything** until an operator promotes it. This is the
code-enforced version of the dry-run safety gate: a `[execution.env]` table in
`plugin.md` would be inert (the `Execution` struct has no env field and
`FormatMailBody` dispatches a bare `cd … && bash run.sh` with no env prefix), so
relying on it for safety would let the first auto-discovery dispatch run live.
The manual gate removes that risk entirely.

Trigger a controlled run on demand:

```bash
gt dog dispatch --plugin orphan-reaper          # one manual run
gt dog dispatch --plugin orphan-reaper --dry-run  # show what would dispatch
```

## Activation (operator step E — bead gu-ilbht)

After the live validation step confirms dry-run parity and clean digests, the
operator promotes the gate to a 30-minute cooldown (spec §7, §12.3):

```toml
[gate]
type = "cooldown"
duration = "30m"
```

Until then, the operator runs the reaper manually — first with `REAPER_DRY_RUN=1`
to confirm the candidate set (spec §12.6 step 2), then live. The dry-run
behavior lives in `run.sh` itself (`REAPER_DRY_RUN`, default per spec §5); the
script handles the safety default, not this manifest.

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

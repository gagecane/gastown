package daemon

import (
	"fmt"
	"time"
)

// Boot-storm backpressure (gu-xrkoq).
//
// On reboot the daemon's heartbeat fans out witness + refinery + dog session
// starts through the rig worker pool (concurrency 10). Each session launches
// MCP servers (builder-mcp ~250MB, serena ~70MB), so an unthrottled fan-out of
// ~27 sessions drove the user slice past 100GB within minutes and tripped
// systemd-oomd — twice on 2026-06-11.
//
// The spawn gate bounds how fast NEW sessions start:
//
//   1. Per-heartbeat cap (SpawnMaxPerHeartbeat): at most N new sessions admitted
//      per heartbeat. The rest defer to the next heartbeat, so the fleet ramps
//      up over a few cycles instead of stampeding. Already-running sessions are
//      never gated — only fresh starts consume a slot.
//   2. Stagger (SpawnStagger): admitted starts reserve time slots spaced apart,
//      giving each session's MCP servers time to finish loading before the next
//      start begins. This serializes the burst town-wide.
//   3. Pressure re-check (checkPressure, incl. the default-on memory budget):
//      every admission re-evaluates system memory, so as the fleet grows and RAM
//      drops, later heartbeats defer new starts before the OOM killer engages.
//
// All gate state lives as zero-valued fields on Daemon, so test literals that
// construct a bare &Daemon{} keep working without explicit initialization.

// resetSpawnGate clears the per-heartbeat spawn budget. Called once at the top
// of each heartbeat so the cap applies per cycle, not for the daemon's lifetime.
func (d *Daemon) resetSpawnGate() {
	d.spawnGateMu.Lock()
	d.spawnsThisHeartbeat = 0
	d.spawnGateMu.Unlock()
}

// admitSpawn decides whether a NEW agent session of the given role/rig may start
// now. It must be called only when the caller has already determined a fresh
// start is needed (i.e. no healthy session is running). Returns true if the
// caller should proceed to start the session; false if the start is deferred to
// a later heartbeat.
//
// On admission the call blocks for the stagger interval relative to the previous
// admitted spawn, so callers run their Start() back-to-back with spacing. The
// brief critical section reserves the slot; the stagger wait happens outside the
// lock so concurrent rig-pool workers do not serialize on the mutex for the full
// delay.
func (d *Daemon) admitSpawn(role, rigName string) bool {
	cfg := d.loadOperationalConfig().GetDaemonConfig()
	maxPerHeartbeat := cfg.SpawnMaxPerHeartbeatV()
	stagger := cfg.SpawnStaggerD()

	// Pressure re-check (default-on memory budget). Deferring here is what gives
	// the multi-heartbeat memory feedback loop its teeth: as the fleet grows and
	// available RAM drops below the budget, new starts defer before OOM.
	if p := d.checkPressure(role); !p.OK {
		if d.logger != nil {
			d.logger.Printf("spawn_gate: deferring %s/%s: %s", role, rigName, p.Reason)
		}
		return false
	}

	d.spawnGateMu.Lock()

	if maxPerHeartbeat > 0 && d.spawnsThisHeartbeat >= maxPerHeartbeat {
		d.spawnGateMu.Unlock()
		if d.logger != nil {
			d.logger.Printf("spawn_gate: deferring %s/%s: per-heartbeat cap reached (%d), retry next heartbeat",
				role, rigName, maxPerHeartbeat)
		}
		return false
	}

	// Reserve a staggered time slot. Each admitted spawn waits until its slot so
	// Start() calls are spaced ~stagger apart even though the lock is brief.
	now := time.Now()
	waitUntil := d.nextSpawnAllowed
	if waitUntil.Before(now) {
		waitUntil = now
	}
	if stagger > 0 {
		d.nextSpawnAllowed = waitUntil.Add(stagger)
	} else {
		d.nextSpawnAllowed = waitUntil
	}
	d.spawnsThisHeartbeat++
	count := d.spawnsThisHeartbeat
	d.spawnGateMu.Unlock()

	if wait := time.Until(waitUntil); wait > 0 {
		if d.logger != nil {
			d.logger.Printf("spawn_gate: admitting %s/%s (%d this heartbeat), staggering %s",
				role, rigName, count, wait.Round(time.Second))
		}
		d.sleep(wait)
	}
	return true
}

// sleep blocks for d, returning early if the daemon context is canceled. A test
// seam-free helper so a shutdown does not get stuck behind a stagger delay.
func (d *Daemon) sleep(dur time.Duration) {
	if d.ctx == nil {
		time.Sleep(dur)
		return
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-d.ctx.Done():
	case <-t.C:
	}
}

// spawnGateStatus renders a one-line summary of the gate for diagnostics.
func (d *Daemon) spawnGateStatus() string {
	d.spawnGateMu.Lock()
	defer d.spawnGateMu.Unlock()
	return fmt.Sprintf("spawns_this_heartbeat=%d next_allowed=%s",
		d.spawnsThisHeartbeat, d.nextSpawnAllowed.Format(time.RFC3339))
}

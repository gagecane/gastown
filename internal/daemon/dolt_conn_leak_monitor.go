package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

// Dolt connection-leak rate monitor (gu-d1r8g). Follow-up to the convoy
// auto-close connection-storm subsystem (gu-g7q6z and its ~10 re-files since
// 2026-05-29: gu-kawd → gu-f0gq → gu-4cxuv → gu-urwg6 → gu-g7q6z → gc-m3ya3y →
// gu-eafok …).
//
// THE GAP IT CLOSES: every prior fix was a POINT fix in the convoy auto-close
// hot-loop (close this connection, fix this loop condition), and the regression
// recurred ~10× because nothing watched the PATTERN. The connection-storm
// signature is unambiguous in the archive: the daemon's held-connection count
// climbs MONOTONICALLY (818→843 in 30s ≈ 50/min in one storm) from the normal
// 1-19 baseline toward the 1000 cap, and the only thing that "fixed" it was an
// operator noticing and restarting Dolt ~15 min later — after 18 agents had each
// filed a [CRITICAL] escalation about it.
//
// This monitor watches the one signal that actually moved: the active Dolt
// connection count, sampled every heartbeat (it is already fetched for the OTel
// gauges). A single elevated sample is normal back-pressure (a dispatch wave, a
// brief latency spike); a SUSTAINED climb above a rate threshold, while the count
// is already well above normal concurrency, is the leak signature. On each
// climbing sample the DAEMON self-mitigates by recycling its own store pools
// (releasing the parked/leaked connections it holds) — acting BEFORE saturation
// instead of waiting for the after-the-fact escalation storm. If the climb stays
// past the consecutive threshold despite the self-heal, it escalates ONCE
// (deduped) so a human/agent knows the auto-mitigation is not holding.
//
// It mirrors the proven evaluateFeedStorm state machine (pure fn + JSON state
// persisted across daemon restarts + threshold + single deduped escalate).

const (
	// connLeakRateThreshold is the connection-growth rate (connections/minute)
	// above which a sample is considered "climbing". Normal town concurrency is
	// flat at 1-19 connections; the storms grew at ~50/min. 20/min is well clear
	// of normal noise (a legitimate dispatch wave settles within a sample) but
	// trips long before the ~15-min runaway-to-saturation seen in the archive.
	connLeakRateThreshold = 20.0

	// connLeakFloor is the connection count below which a climb is ignored. With
	// observed normal concurrency of 1-19 and a 1000 cap, a climb that has not yet
	// cleared 50 connections is not a saturation risk and is far more likely a
	// legitimate burst than the leak. Gating on the floor keeps the monitor silent
	// during normal operation while still catching the leak with ~950 connections
	// of runway before the cap.
	connLeakFloor = 50

	// connLeakConsecutiveThreshold is the number of CONSECUTIVE climbing samples
	// (above both the rate threshold and the floor) before escalating. The
	// recovery heartbeat runs ~every 5 min, so 3 consecutive climbing samples is
	// ~15 min of sustained, self-heal-resistant growth — long enough to rule out a
	// transient burst, short enough that the escalation lands while there is still
	// runway below the cap. Self-mitigation (pool recycle) happens on EVERY
	// climbing sample, not just at this threshold.
	connLeakConsecutiveThreshold = 3
)

// connLeakState persists across daemon restarts in
// <town>/.runtime/daemon/dolt-conn-leak.json so a leak that survives a restart
// keeps its climb history (LastSample/LastSampleAt) and escalation marker rather
// than resetting every restart.
type connLeakState struct {
	// LastSample is the connection count observed on the previous sample, used to
	// compute the growth rate against the current sample.
	LastSample int `json:"last_sample"`
	// LastSampleAt is the RFC3339 timestamp of the previous sample.
	LastSampleAt string `json:"last_sample_at,omitempty"`
	// Consecutive counts how many consecutive samples have been climbing.
	Consecutive int `json:"consecutive"`
	// FirstSeen is when the current climbing episode began.
	FirstSeen string `json:"first_seen,omitempty"`
	// PeakSample is the highest connection count seen during the episode.
	PeakSample int `json:"peak_sample,omitempty"`
	// Escalated marks that this episode has already escalated (escalate once).
	Escalated bool `json:"escalated"`
}

// connLeakAction is the outcome of evaluating one connection-count sample.
type connLeakAction struct {
	// Mitigate is true when the daemon should self-heal (recycle store pools)
	// this sample — set on every climbing sample.
	Mitigate bool
	// Escalate is true only on the sample that first crosses the consecutive
	// threshold within an episode (single deduped escalation).
	Escalate bool
	// Rate is the computed growth rate (connections/minute) for this sample,
	// for logging/escalation context.
	Rate float64
}

// evaluateConnLeak is the pure state machine: given the prior state, this
// sample's connection count, and the sample time, it returns the next state and
// the action to take.
//
// A sample is "climbing" when the connection count is above connLeakFloor AND it
// grew faster than connLeakRateThreshold connections/minute since the previous
// sample. The first sample of any episode (no previous sample, or a previous
// sample of zero) only seeds the trajectory — a leak needs a measured rate
// across two samples, so it never trips on a single cold-start reading.
//
// A non-climbing sample means the count has returned toward baseline (the
// convoy-close cycle released its connections), which RE-ARMS the monitor: the
// consecutive counter and escalation marker reset, so a recovered town
// immediately re-arms to catch the next episode. This is the return-to-baseline
// contract the regression guard asserts.
func evaluateConnLeak(prev connLeakState, count int, now time.Time) (connLeakState, connLeakAction) {
	next := connLeakState{LastSample: count, LastSampleAt: now.Format(time.RFC3339)}

	// Need a prior sample with a parseable timestamp and a positive count to
	// measure a rate. Without one, seed the trajectory and take no action.
	if prev.LastSample <= 0 || prev.LastSampleAt == "" {
		return next, connLeakAction{}
	}
	prevAt, err := time.Parse(time.RFC3339, prev.LastSampleAt)
	if err != nil {
		return next, connLeakAction{}
	}
	elapsed := now.Sub(prevAt).Minutes()
	if elapsed <= 0 {
		// Clock did not advance (or went backwards): preserve the prior episode
		// counters but take no action this sample.
		next.Consecutive = prev.Consecutive
		next.FirstSeen = prev.FirstSeen
		next.PeakSample = prev.PeakSample
		next.Escalated = prev.Escalated
		return next, connLeakAction{}
	}

	rate := float64(count-prev.LastSample) / elapsed
	climbing := count > connLeakFloor && rate > connLeakRateThreshold

	if !climbing {
		// Returned toward baseline (or never left it) → re-arm. next already holds
		// the fresh sample with zeroed counters.
		return next, connLeakAction{Rate: rate}
	}

	// Climbing: carry the episode forward.
	next.Consecutive = prev.Consecutive + 1
	next.FirstSeen = prev.FirstSeen
	if next.FirstSeen == "" {
		next.FirstSeen = now.Format(time.RFC3339)
	}
	next.PeakSample = prev.PeakSample
	if count > next.PeakSample {
		next.PeakSample = count
	}
	next.Escalated = prev.Escalated

	action := connLeakAction{Mitigate: true, Rate: rate}
	if next.Consecutive >= connLeakConsecutiveThreshold && !next.Escalated {
		action.Escalate = true
		next.Escalated = true
	}
	return next, action
}

func connLeakStatePath(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "daemon", "dolt-conn-leak.json")
}

func loadConnLeakState(path string) connLeakState {
	var st connLeakState
	data, err := os.ReadFile(path) //nolint:gosec // G304: path constructed internally from townRoot
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

func saveConnLeakState(path string, st connLeakState) {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	if data, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(path, data, 0644) //nolint:gosec // G306: non-secret monitor state
	}
}

// monitorConnLeak advances the connection-leak state machine for this heartbeat's
// connection-count sample. On a climbing sample it recycles the daemon's store
// pools to release held connections (self-mitigation before saturation); when the
// climb is sustained past the consecutive threshold it escalates once (deduped).
// Best-effort: state, mitigation, and escalation failures never block the
// heartbeat.
func (d *Daemon) monitorConnLeak(count int) {
	path := connLeakStatePath(d.config.TownRoot)
	prev := loadConnLeakState(path)
	next, action := evaluateConnLeak(prev, count, time.Now().UTC())

	if action.Mitigate {
		d.logger.Printf("Dolt conn-leak: connection count climbing (%d conns, ~%.0f/min, consecutive=%d) — recycling daemon store pools to release held connections (gu-d1r8g)",
			count, action.Rate, next.Consecutive)
		d.recycleStorePools()
	}

	if action.Escalate {
		msg := fmt.Sprintf("Dolt connection leak: count climbing to %d (~%.0f/min, peak %d) sustained for %d consecutive samples since %s. The convoy auto-close hot-loop leak signature (gu-g7q6z); daemon pool-recycle self-heal did not hold. Capture diagnostics before any restart: gt dolt dump; gt dolt status. Then inspect convoy auto-close (internal/daemon/convoy_manager.go) for a not-closed connection.",
			count, action.Rate, next.PeakSample, next.Consecutive, next.FirstSeen)
		if err := d.escalate("dolt_conn_leak", msg); err != nil {
			// Escalation failed — clear the marker so the next sustained sample
			// retries instead of silently burying the leak (gu-nid89.43). The
			// consecutive counter and FirstSeen are preserved so the episode keeps
			// building toward the next escalation attempt.
			d.logger.Printf("Dolt conn-leak escalation failed, will retry: %v", err)
			next.Escalated = false
		}
	}

	saveConnLeakState(path, next)
}

// recycleStorePools releases the connections the daemon's long-lived beads-store
// pools are holding by momentarily collapsing each pool's idle-connection cap to
// zero (which makes database/sql close every parked connection) and restoring it.
// This is the bounded, safe self-heal for the connection climb: it drops the
// daemon's own held/leaked-into-idle connections without tearing down the stores
// or disturbing in-flight queries (database/sql closes only idle connections when
// MaxIdleConns is lowered). Best-effort: stores without a raw-pool accessor are
// skipped.
//
// It recycles BOTH d.beadsStores AND the convoy manager's live store pools
// (deduped by underlying *sql.DB). This matters because the convoy event poll —
// the documented leaker (gu-g7q6z) — queries every store every 5s, and those
// pools are NOT always d.beadsStores: when Dolt is not ready at boot,
// openBeadsStores() returns nil (d.beadsStores stays nil) and the convoy manager
// lazily opens its OWN pools into m.stores via the storeOpener callback.
// d.beadsStores is never reassigned afterward, so ranging it alone recycled zero
// pools in exactly the post-restart recovery window where the leak recurs —
// making the self-heal a silent no-op. Recycling the convoy manager's live pools
// closes that gap (gu-mxupc).
// Returns the number of distinct pools recycled (for logging/tests).
func (d *Daemon) recycleStorePools() int {
	idleTimeout := daemonStoreIdleTimeout(d.config.TownRoot)

	// Dedup by underlying *sql.DB: when d.beadsStores and the convoy manager
	// share the same pools (Dolt-ready-at-boot path) we must not collapse the
	// same pool's idle cap twice.
	seen := make(map[*sql.DB]bool)
	recycled := 0
	recycle := func(store beadsdk.Storage) {
		accessor, ok := store.(beadsDBAccessor)
		if !ok {
			return
		}
		db := accessor.DB()
		if db == nil || seen[db] {
			return
		}
		seen[db] = true
		recyclePoolDB(db, idleTimeout)
		recycled++
	}

	for _, store := range d.beadsStores {
		recycle(store)
	}
	for _, store := range d.convoyManager.storesSnapshot() {
		recycle(store)
	}

	if recycled > 0 {
		d.logger.Printf("Dolt conn-leak: recycled %d store pool(s) (closed idle connections, restored symmetric pool)", recycled)
	}
	return recycled
}

// recyclePoolDB drops every idle connection a pool is holding and restores the
// gu-g7q6z symmetric-pool tuning. Lowering MaxIdleConns to 0 makes database/sql
// close all currently-idle connections immediately; restoring it (via tunePoolDB)
// re-applies the symmetric MaxIdleConns == MaxOpenConns + ConnMaxIdleTime
// hardening so the pool behaves identically afterwards, just with the stale
// connections released. Split out from recycleStorePools so the pool surgery is
// unit-testable without faking the full beadsdk.Storage interface. Returns the
// restored MaxIdleConns (for logging/tests). No-op on a nil pool.
func recyclePoolDB(db *sql.DB, idleTimeout time.Duration) int {
	if db == nil {
		return 0
	}
	// Collapse idle cap to 0 → database/sql closes all parked connections now.
	db.SetMaxIdleConns(0)
	// Restore the symmetric-pool + idle-retire hardening.
	return tunePoolDB(db, idleTimeout)
}

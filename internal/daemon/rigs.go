package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/wisp"
)

// This file contains rig-registry and per-rig operational-state queries.
// The rig-concurrency helper (RigWorkerPool) lives in its own package-private
// file; this is just the metadata/filter layer that callers use to decide
// which rigs to touch during a heartbeat.

// getKnownRigs returns list of registered rig names.
// Results are memoized per heartbeat tick to coalesce the ~10 per-tick callers
// into a single mayor/rigs.json read. The cache is invalidated at the start of
// each heartbeat.
func (d *Daemon) getKnownRigs() []string {
	if d.knownRigsCacheValid {
		return d.knownRigsCache
	}
	rigs := d.readKnownRigsFromDisk()
	d.knownRigsCache = rigs
	d.knownRigsCacheValid = true
	return rigs
}

// invalidateKnownRigsCache clears the per-tick cache so the next
// getKnownRigs() call re-reads mayor/rigs.json from disk.
func (d *Daemon) invalidateKnownRigsCache() {
	d.knownRigsCache = nil
	d.knownRigsCacheValid = false
}

// readKnownRigsFromDisk reads and parses mayor/rigs.json.
func (d *Daemon) readKnownRigsFromDisk() []string {
	rigsPath := filepath.Join(d.config.TownRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return nil
	}

	var parsed struct {
		Rigs map[string]interface{} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}

	var rigs []string
	for name := range parsed.Rigs {
		rigs = append(rigs, name)
	}
	return rigs
}

// getPatrolRigs returns the list of operational rigs for a patrol.
// If the patrol config specifies a rigs filter, only those rigs are returned.
// Otherwise, all known rigs are returned. In both cases, non-operational
// rigs (parked/docked) are filtered out at list-building time. (Fixes upstream #2082)
func (d *Daemon) getPatrolRigs(patrol string) []string {
	configRigs := GetPatrolRigs(d.patrolConfig, patrol)
	var candidates []string
	if len(configRigs) > 0 {
		candidates = configRigs
	} else {
		candidates = d.getKnownRigs()
	}

	// Filter out non-operational rigs early to avoid per-rig skip noise
	var operational []string
	for _, rigName := range candidates {
		if ok, reason := d.isRigOperational(rigName); ok {
			operational = append(operational, rigName)
		} else {
			d.logger.Printf("Excluding %s from %s patrol: %s", rigName, patrol, reason)
		}
	}
	return operational
}

// isRigOperational checks if a rig is in an operational state.
// Returns true if the rig can have agents auto-started.
// Returns false (with reason) if the rig is parked, docked, or has auto_restart blocked/disabled.
//
// Parked/docked detection is delegated to rig.IsRigParkedOrDockedE (shared
// with cmd package). This daemon-specific wrapper adds fail-safe semantics
// (returns false when status can't be verified) and auto_restart checking.
func (d *Daemon) isRigOperational(rigName string) (bool, string) {
	cfg := wisp.NewConfig(d.config.TownRoot, rigName)

	// Note: a missing wisp config file is the normal state for rigs that have
	// never been parked or had a wisp-level override set. Parked/docked state
	// is authoritatively tracked via rig bead labels (see IsRigParkedOrDockedE
	// below), so the absence of a wisp config does NOT indicate lost state.
	// An earlier version of this code printed a warning here on every heartbeat
	// cycle per rig, which at 15 rigs × ~8/min produced ~260K log lines/day
	// (52% of daemon.log). The warning was never actionable — removed in gu-66xp.

	// Check parked/docked via the shared helper. The error variant lets us
	// implement fail-safe semantics: when the rig bead can't be read
	// (e.g., Dolt down, prefix missing), we refuse to start agents rather
	// than waste API credits on a potentially-docked rig.
	blocked, reason, err := rig.IsRigParkedOrDockedE(d.config.TownRoot, rigName)
	if err != nil {
		// FAIL-SAFE: Can't verify docked status (Dolt down, prefix missing,
		// network issue, etc.). Assume NOT operational to avoid wasting
		// credits on potentially-docked rigs. Better to delay work than
		// burn credits unnecessarily.
		d.logger.Printf("Warning: failed to check rig %s for docked/parked status: %v (assuming not operational)", rigName, err)
		return false, "cannot verify rig status (Dolt unavailable)"
	}
	if blocked {
		// Match existing reason strings for compatibility with log scrapers
		// and tests.
		switch reason {
		case "parked":
			// Wisp-sourced parked uses "rig is parked"; bead-sourced parked
			// historically used "rig is parked (global)". We can no longer
			// distinguish the two from inside the shared helper, so we
			// return the generic form. Callers that care about source can
			// query wisp directly.
			return false, "rig is parked"
		case "docked":
			return false, "rig is docked"
		default:
			return false, fmt.Sprintf("rig is %s", reason)
		}
	}

	// Check auto_restart config
	// If explicitly blocked (nil), auto-restart is disabled
	if cfg.IsBlocked("auto_restart") {
		return false, "auto_restart is blocked"
	}

	// If explicitly set to false, auto-restart is disabled
	// Note: GetBool returns false for unset keys, so we need to check if it's explicitly set
	val := cfg.Get("auto_restart")
	if val != nil {
		if autoRestart, ok := val.(bool); ok && !autoRestart {
			return false, "auto_restart is disabled"
		}
	}

	return true, ""
}

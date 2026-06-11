package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/reaper"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	// defaultWispReaperInterval is the patrol interval. Set to 1h since reaping
	// is cleanup work, not latency-sensitive. Was 30m before Dog-driven refactor.
	defaultWispReaperInterval = 1 * time.Hour
	// Wisps older than this are reaped (closed). Configurable via formula var max_age.
	defaultWispMaxAge = 24 * time.Hour
	// Closed wisps older than this are permanently deleted. Formula var: purge_age.
	defaultWispDeleteAge = 7 * 24 * time.Hour
	// Alert threshold: if open wisp count exceeds this, the Dog should escalate.
	// Shared with `gt reaper run` warning. See reaper.DefaultAlertThreshold.
	wispAlertThreshold = reaper.DefaultAlertThreshold
	// Closed mail older than this is permanently deleted. Formula var: mail_delete_age.
	defaultMailDeleteAge = 7 * 24 * time.Hour
	// Issues stale longer than this are auto-closed. Formula var: stale_issue_age.
	defaultStaleIssueAge = 7 * 24 * time.Hour
	// defaultHookedMolTTL is the short TTL for closing stale hooked dispatch mols
	// (mol-dog-* wisps in 'hooked' status that no dog consumed). Two reaper cycles
	// gives a running dog time to pick up the mol; beyond that it is an orphan.
	defaultHookedMolTTL = 2 * time.Hour
)

// WispReaperConfig holds configuration for the wisp_reaper patrol.
type WispReaperConfig struct {
	Enabled      bool     `json:"enabled"`
	DryRun       bool     `json:"dry_run,omitempty"`
	IntervalStr  string   `json:"interval,omitempty"`
	MaxAgeStr    string   `json:"max_age,omitempty"`
	DeleteAgeStr string   `json:"delete_age,omitempty"`
	Databases    []string `json:"databases,omitempty"`
}

// wispReaperInterval returns the configured interval, or the default (1h).
func wispReaperInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispReaperInterval
}

// wispReaperMaxAge returns the configured max age, or the default (24h).
func wispReaperMaxAge(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.MaxAgeStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.MaxAgeStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispMaxAge
}

// wispDeleteAge returns the configured delete age, or the default (7 days).
func wispDeleteAge(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.DeleteAgeStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.DeleteAgeStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispDeleteAge
}

// reapWisps is the thin orchestrator for the wisp_reaper patrol.
// It pours a mol-dog-reaper molecule, then dispatches a Dog to execute it.
// The Dog reads the formula steps and calls `gt reaper` CLI helpers.
// Falls back to inline execution if Dog dispatch fails.
func (d *Daemon) reapWisps() {
	if !d.isPatrolActive("wisp_reaper") {
		return
	}

	config := d.patrolConfig.Patrols.WispReaper
	maxAge := wispReaperMaxAge(d.patrolConfig)
	deleteAge := wispDeleteAge(d.patrolConfig)

	vars := map[string]string{
		"max_age":         maxAge.String(),
		"purge_age":       deleteAge.String(),
		"stale_issue_age": defaultStaleIssueAge.String(),
		"mail_delete_age": defaultMailDeleteAge.String(),
		"alert_threshold": fmt.Sprintf("%d", wispAlertThreshold),
		"dolt_port":       fmt.Sprintf("%d", d.doltServerPort()),
	}

	if config.DryRun {
		vars["dry_run"] = "true"
	}
	if len(config.Databases) > 0 {
		vars["databases"] = strings.Join(config.Databases, ",")
	}

	// Pour the molecule for observability tracking.
	mol := d.pourDogMolecule(constants.MolDogReaper, vars)
	defer mol.close()

	if config.DryRun {
		d.logger.Printf("wisp_reaper: DRY RUN — reporting only, no changes will be made")
	}

	// Guard: skip Dog dispatch if no dog sessions are running. gt sling creates
	// a hooked bead that sits orphaned forever if no dog consumes it. Running
	// inline avoids the accumulation of phantom dispatch wisps. GH#3767.
	if !d.kennelHasRunningDogs() {
		d.logger.Printf("wisp_reaper: no dog sessions running, running inline")
		d.reapWispsInline(config, maxAge, deleteAge, mol)
		return
	}

	// Try dispatching to a Dog for formula-driven execution.
	if err := d.dispatchReaperDog(vars); err != nil {
		d.logger.Printf("wisp_reaper: Dog dispatch failed (%v), running inline fallback", err)
		d.reapWispsInline(config, maxAge, deleteAge, mol)
		return
	}

	d.logger.Printf("wisp_reaper: dispatched to Dog for formula-driven execution")
}

// kennelHasRunningDogs returns true if at least one dog tmux session is running.
// Dogs run as tmux sessions named "hq-dog-{name}". The check is a lightweight
// filesystem + tmux scan that avoids dispatching to a phantom group address
// when the kennel is empty or all sessions are dead. See: GH#3767.
func (d *Daemon) kennelHasRunningDogs() bool {
	kennelPath := filepath.Join(d.config.TownRoot, "deacon", "dogs")
	entries, err := os.ReadDir(kennelPath)
	if err != nil || len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Only count valid dogs (those with a .dog.json state file).
		stateFile := filepath.Join(kennelPath, entry.Name(), ".dog.json")
		if _, err := os.Stat(stateFile); os.IsNotExist(err) {
			continue
		}
		sessionName := fmt.Sprintf("hq-dog-%s", entry.Name())
		if running, err := d.tmux.HasSession(sessionName); err == nil && running {
			return true
		}
	}
	return false
}

// dispatchReaperDog dispatches the mol-dog-reaper formula to a Dog via gt sling.
func (d *Daemon) dispatchReaperDog(vars map[string]string) error {
	args := []string{"sling", constants.MolDogReaper, "deacon/dogs"}
	for k, v := range vars {
		args = append(args, "--var", fmt.Sprintf("%s=%s", k, v))
	}

	cmd := exec.Command(d.gtPath, args...) //nolint:gosec // G204: d.gtPath resolved at daemon init via LookPath
	cmd.Dir = d.config.TownRoot
	// Inherit os.Environ() (cmd.Env left nil) — gt sling performs WRITES
	// (creates wisps, dispatches dogs) so it must NOT carry
	// BD_DOLT_AUTO_COMMIT=off from bdReadOnlyEnv(). PATH augmentation at
	// daemon startup (PATCH-007) ensures the inherited env still finds
	// gt/bd via os.Environ()'s PATH.
	util.SetDetachedProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gt sling: %w", err)
	}
	return nil
}

// reapWispsInline is the fallback that runs the reaper cycle inline when
// Dog dispatch is unavailable. Delegates to the reaper package for SQL execution.
func (d *Daemon) reapWispsInline(config *WispReaperConfig, maxAge, deleteAge time.Duration, mol *dogMol) {
	databases := config.Databases
	if len(databases) == 0 {
		databases = reaper.DiscoverDatabases("127.0.0.1", d.doltServerPort())
	}
	if len(databases) == 0 {
		d.logger.Printf("wisp_reaper: no databases to reap")
		mol.failStep("scan", "no databases found")
		return
	}
	d.logger.Printf("wisp_reaper: scanning %d databases (inline fallback)", len(databases))
	mol.closeStep("scan")

	port := d.doltServerPort()
	dryRun := config.DryRun
	var totalReaped, totalOpen, totalPurged, totalMailPurged, totalAutoClosed int

	// Step 2: Reap
	reapErrors := 0
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 10*time.Second, 10*time.Second)
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: connect error: %v", dbName, err)
			reapErrors++
			continue
		}
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			d.logger.Printf("wisp_reaper: %s: skipped (no reaper schema)", dbName)
			db.Close()
			continue
		}
		result, err := reaper.Reap(db, dbName, maxAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: reap error: %v", dbName, err)
			reapErrors++
			continue
		}
		totalReaped += result.Reaped
		totalOpen += result.OpenRemain
		if result.Reaped > 0 {
			d.logger.Printf("wisp_reaper: %s: reaped %d stale wisps, %d open remain", dbName, result.Reaped, result.OpenRemain)
		}
	}
	if reapErrors > 0 {
		mol.failStep("reap", fmt.Sprintf("%d databases had reap errors", reapErrors))
	} else {
		mol.closeStep("reap")
	}

	// Step 3: Purge
	purgeErrors := 0
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 30*time.Second, 30*time.Second)
		if err != nil {
			purgeErrors++
			continue
		}
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			db.Close()
			continue
		}
		result, err := reaper.Purge(db, dbName, deleteAge, defaultMailDeleteAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: purge error: %v", dbName, err)
			purgeErrors++
			continue
		}
		totalPurged += result.WispsPurged
		totalMailPurged += result.MailPurged
		for _, a := range result.Anomalies {
			d.logger.Printf("wisp_reaper: %s: ANOMALY: %s", dbName, a.Message)
		}
	}
	if purgeErrors > 0 {
		mol.failStep("purge", fmt.Sprintf("%d databases had purge errors", purgeErrors))
	} else {
		mol.closeStep("purge")
	}

	// Step 3a: Flush the dolt_ignored wisp_* working set to HEAD (gu-tqtwt).
	// bd never commits the wisp tables (they are dolt_ignored), so their churn
	// accumulates unbounded in the Dolt working set until bd's pre-migration
	// dirty-table guard deadlocks on the oldest/highest-volume rigs and every
	// --json/capacity query starts failing. The reaper's raw connection bypasses
	// that guard, so flushing here bounds the backlog every cycle.
	flushErrors := 0
	var totalWispFlushed int
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 30*time.Second, 30*time.Second)
		if err != nil {
			flushErrors++
			continue
		}
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			db.Close()
			continue
		}
		result, err := reaper.FlushWispWorkingSet(db, dbName, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: wisp flush error: %v", dbName, err)
			flushErrors++
			continue
		}
		totalWispFlushed += result.Flushed
		if result.Flushed > 0 {
			d.logger.Printf("wisp_reaper: %s: flushed %d pending wisp row change(s) across %v",
				dbName, result.Flushed, result.Tables)
		}
		for _, a := range result.Anomalies {
			d.logger.Printf("wisp_reaper: %s: ANOMALY: %s", dbName, a.Message)
		}
	}
	if flushErrors > 0 {
		mol.failStep("wisp-flush", fmt.Sprintf("%d databases had wisp flush errors", flushErrors))
	} else {
		mol.closeStep("wisp-flush")
	}

	// Step 3b: Close plugin receipts (fast-track — 1h instead of 7d stale age)
	pluginReceiptAge := 1 * time.Hour
	var totalPluginClosed int
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 10*time.Second, 10*time.Second)
		if err != nil {
			continue
		}
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			db.Close()
			continue
		}
		result, err := reaper.ClosePluginReceipts(db, dbName, pluginReceiptAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: plugin receipt close error: %v", dbName, err)
			continue
		}
		totalPluginClosed += result.Closed
		if result.Closed > 0 {
			d.logger.Printf("wisp_reaper: %s: closed %d plugin receipts", dbName, result.Closed)
		}
	}

	// Step 3c: Close plugin dispatch mails (daemon→dog instruction beads that are never closed)
	pluginDispatchAge := 1 * time.Hour
	var totalDispatchClosed int
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 10*time.Second, 10*time.Second)
		if err != nil {
			continue
		}
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			db.Close()
			continue
		}
		result, err := reaper.ClosePluginDispatches(db, dbName, pluginDispatchAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: plugin dispatch close error: %v", dbName, err)
			continue
		}
		totalDispatchClosed += result.Closed
		if result.Closed > 0 {
			d.logger.Printf("wisp_reaper: %s: closed %d plugin dispatches", dbName, result.Closed)
		}
	}

	// Step 3d: Close stale hooked mols — dispatch wisps that sat in 'hooked'
	// status beyond defaultHookedMolTTL because no dog was alive to consume them.
	var totalHookedClosed int
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 10*time.Second, 10*time.Second)
		if err != nil {
			continue
		}
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			db.Close()
			continue
		}
		result, err := reaper.CloseStaleHookedMols(db, dbName, defaultHookedMolTTL, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: hooked mol close error: %v", dbName, err)
			continue
		}
		totalHookedClosed += result.Closed
		if result.Closed > 0 {
			d.logger.Printf("wisp_reaper: %s: closed %d stale hooked mols", dbName, result.Closed)
		}
	}

	// Step 4: Auto-close
	autoCloseErrors := 0
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 10*time.Second, 10*time.Second)
		if err != nil {
			autoCloseErrors++
			continue
		}
		// Auto-close operates on the issues table, not wisps, but if the database
		// has no beads schema at all we should skip it too.
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			db.Close()
			continue
		}
		result, err := reaper.AutoClose(db, dbName, defaultStaleIssueAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: auto-close error: %v", dbName, err)
			autoCloseErrors++
			continue
		}
		totalAutoClosed += result.Closed
	}
	if autoCloseErrors > 0 {
		mol.failStep("auto-close", fmt.Sprintf("%d databases had auto-close errors", autoCloseErrors))
	} else {
		mol.closeStep("auto-close")
	}

	// Step 4a: Reconcile post-merge orphans (gu-7igu8).
	// The refinery's post-merge sequence (close MR → close source → unhook →
	// reap) is non-atomic; an interrupted reconcile can leave a source issue
	// non-terminal (HOOKED on a dead polecat) even though the MR merged. Detect
	// that signature — agent bead whose active_mr points at a proven-merged MR
	// with a still-non-terminal source — and complete the reconcile by closing
	// the source issue. Runs BEFORE the active_mr scrub (4b) so the source is
	// terminal in time for the same-cycle scrub to clear the dangling active_mr.
	var reconScanned, reconReconciled, reconPreservedWIP int
	if d.config.TownRoot == "" {
		d.logger.Printf("wisp_reaper: post-merge orphan reconcile skipped (no town root)")
	} else {
		bd := beads.New(d.config.TownRoot).ForAgentBead()
		reconResult, err := reaper.ReconcileMergedOrphans(bd, dryRun)
		if err != nil {
			d.logger.Printf("wisp_reaper: post-merge orphan reconcile error: %v", err)
		} else {
			reconScanned = reconResult.Scanned
			reconReconciled = reconResult.Reconciled
			reconPreservedWIP = reconResult.PreservedWIP
			for _, entry := range reconResult.ReconciledEntries {
				d.logger.Printf("wisp_reaper: reconciled post-merge orphan: agent=%s active_mr=%s source=%s closed",
					entry.AgentBeadID, entry.ActiveMR, entry.SourceIssue)
			}
			for _, a := range reconResult.Anomalies {
				d.logger.Printf("wisp_reaper: post-merge orphan reconcile ANOMALY: %s", a.Message)
			}
		}
	}

	// Step 4b: Scrub stale active_mr refs on agent beads (gu-dhqm).
	// Re-evaluate every agent bead's active_mr through polecat.AssessActiveMR
	// and clear refs whose MR + source issue are both terminal. Preserves
	// polecats with cleanup_status indicating human WIP (gc-eysed). Operates
	// on the town database only.
	var scrubScanned, scrubCleared, scrubPreservedWIP, scrubStillPending int
	if d.config.TownRoot == "" {
		d.logger.Printf("wisp_reaper: active_mr scrub skipped (no town root)")
	} else {
		bd := beads.New(d.config.TownRoot).ForAgentBead()
		scrubResult, err := reaper.ScrubStaleActiveMR(bd, dryRun)
		if err != nil {
			d.logger.Printf("wisp_reaper: active_mr scrub error: %v", err)
		} else {
			scrubScanned = scrubResult.Scanned
			scrubCleared = scrubResult.Cleared
			scrubPreservedWIP = scrubResult.PreservedWIP
			scrubStillPending = scrubResult.StillPending
			for _, entry := range scrubResult.ClearedEntries {
				d.logger.Printf("wisp_reaper: cleared active_mr on %s: mr=%s status=%s source=%s",
					entry.AgentBeadID, entry.ActiveMR, entry.MRStatus, entry.SourceIssue)
			}
			for _, a := range scrubResult.Anomalies {
				d.logger.Printf("wisp_reaper: active_mr scrub ANOMALY: %s", a.Message)
			}
		}
	}

	// Step 4c: Scrub dangling mr_id/hook_bead refs on agent beads (gu-96uxo).
	// Clear FK fields whose referent wisp was reaped/purged this cycle (now
	// missing). Complements the active_mr scrub above (gu-dhqm), which the
	// existence-only check here deliberately does not duplicate. Operates on
	// the town database only.
	var fkScanned, fkClearedMRID, fkClearedHook, fkPreservedWIP int
	if d.config.TownRoot == "" {
		d.logger.Printf("wisp_reaper: dangling_fk scrub skipped (no town root)")
	} else {
		bd := beads.New(d.config.TownRoot).ForAgentBead()
		fkResult, err := reaper.ScrubDanglingFKRefs(bd, dryRun)
		if err != nil {
			d.logger.Printf("wisp_reaper: dangling_fk scrub error: %v", err)
		} else {
			fkScanned = fkResult.Scanned
			fkClearedMRID = fkResult.ClearedMRID
			fkClearedHook = fkResult.ClearedHookBead
			fkPreservedWIP = fkResult.PreservedWIP
			for _, entry := range fkResult.ClearedEntries {
				d.logger.Printf("wisp_reaper: cleared %s on %s: referent=%s (missing)",
					entry.Field, entry.AgentBeadID, entry.Referent)
			}
			for _, a := range fkResult.Anomalies {
				d.logger.Printf("wisp_reaper: dangling_fk scrub ANOMALY: %s", a.Message)
			}
		}
	}

	// Step 5: Report
	if totalOpen > wispAlertThreshold {
		d.logger.Printf("wisp_reaper: WARNING: %d open wisps exceed threshold %d — investigate wisp lifecycle",
			totalOpen, wispAlertThreshold)
	}
	d.logger.Printf("wisp_reaper: cycle complete — reaped=%d purged=%d wisp_flushed=%d mail_purged=%d plugin_closed=%d dispatch_closed=%d hooked_closed=%d auto_closed=%d orphan_recon_scanned=%d orphan_reconciled=%d orphan_recon_preserved=%d active_mr_scanned=%d active_mr_cleared=%d active_mr_preserved=%d active_mr_pending=%d dangling_fk_scanned=%d dangling_fk_cleared_mr_id=%d dangling_fk_cleared_hook=%d dangling_fk_preserved=%d open=%d databases=%d dryRun=%v",
		totalReaped, totalPurged, totalWispFlushed, totalMailPurged, totalPluginClosed, totalDispatchClosed, totalHookedClosed, totalAutoClosed,
		reconScanned, reconReconciled, reconPreservedWIP,
		scrubScanned, scrubCleared, scrubPreservedWIP, scrubStillPending,
		fkScanned, fkClearedMRID, fkClearedHook, fkPreservedWIP,
		totalOpen, len(databases), dryRun)
	mol.closeStep("report")
}

// doltServerPort returns the configured Dolt server port.
func (d *Daemon) doltServerPort() int {
	if d.doltServer != nil {
		return d.doltServer.config.Port
	}
	return 3307
}

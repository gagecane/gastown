package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/plugin"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultDoltBackupInterval = 15 * time.Minute
	doltBackupTimeout         = 120 * time.Second
	// doltBackupPluginTimeout bounds the full run.sh execution, which iterates
	// over every production database (not a single sync). Matches the plugin's
	// declared [execution] timeout of 5m in plugins/dolt-backup/plugin.md.
	doltBackupPluginTimeout = 5 * time.Minute
)

// doltBackupInterval returns the configured backup interval, or the default (15m).
func doltBackupInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.DoltBackup != nil {
		if config.Patrols.DoltBackup.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.DoltBackup.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultDoltBackupInterval
}

// syncDoltBackups syncs each production database to its configured backup location.
// Non-fatal: errors are logged but don't stop the daemon.
//
// Platform split (gu-a727o):
//   - Linux: the native sync below uses iCloud Drive for offsite sync and the
//     <db>-backup remote convention, neither of which apply. Instead we execute
//     the dolt-backup plugin's run.sh in-process — the canonical deterministic
//     backup path — decoupled from deacon-dog availability so freshness no
//     longer drifts during deacon session-handoff gaps. See runDoltBackupPlugin.
//   - macOS: keep the native sync + iCloud offsite replication.
func (d *Daemon) syncDoltBackups() {
	if runtime.GOOS != "darwin" {
		d.runDoltBackupPlugin()
		return
	}
	if !d.isPatrolActive("dolt_backup") {
		return
	}

	// Pour molecule for observability (nil-safe — all methods are no-ops on nil).
	mol := d.pourDogMolecule(constants.MolDogBackup, nil)
	defer mol.close()

	// Resolve data dir: use DoltServerManager if available, else conventional path.
	var dataDir string
	if d.doltServer != nil && d.doltServer.IsEnabled() && d.doltServer.config.DataDir != "" {
		dataDir = d.doltServer.config.DataDir
	} else {
		dataDir = filepath.Join(d.config.TownRoot, ".dolt-data")
	}
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		d.logger.Printf("dolt_backup: data dir %s does not exist, skipping", dataDir)
		mol.failStep("sync", "data dir does not exist")
		return
	}

	config := d.patrolConfig.Patrols.DoltBackup
	databases := config.Databases
	if len(databases) == 0 {
		databases = d.discoverDatabasesWithBackups(dataDir)
	}

	if len(databases) == 0 {
		d.logger.Printf("dolt_backup: no databases with backup remotes found")
		mol.failStep("sync", "no databases with backup remotes")
		return
	}

	d.logger.Printf("dolt_backup: syncing %d database(s)", len(databases))

	synced := 0
	var failures []string
	for _, db := range databases {
		backupName := db + "-backup"
		if err := d.syncBackup(dataDir, db, backupName); err != nil {
			d.logger.Printf("dolt_backup: %s: sync failed: %v", db, err)
			failures = append(failures, db)
		} else {
			synced++
		}
	}

	d.logger.Printf("dolt_backup: synced %d/%d database(s)", synced, len(databases))

	if len(failures) > 0 {
		mol.failStep("sync", fmt.Sprintf("synced %d/%d, failures: %s", synced, len(databases), strings.Join(failures, "; ")))
	} else {
		mol.closeStep("sync")
	}

	// Offsite sync: rsync local backups to iCloud Drive for cloud replication.
	// This is a stopgap until proper dolt remote push is configured.
	if synced > 0 {
		d.syncOffsiteBackup()
		mol.closeStep("offsite")
	} else {
		mol.closeStep("offsite")
	}

	mol.closeStep("report")
}

// runDoltBackupPlugin executes the dolt-backup plugin's run.sh in-process on the
// daemon's backup tick. This is the Linux backup path (gu-a727o).
//
// Why in-process instead of dispatching to a deacon dog: the dog-dispatch path
// (handler.dispatchPlugins) can only fire when an idle deacon dog is available.
// During deacon session-handoff gaps no dog is dispatchable, so the backup is
// deferred and freshness drifts (~23m observed, 32.5m breach). Running run.sh
// directly on the daemon's own 15m tick removes that deacon-liveness coupling —
// the daemon heartbeat keeps ticking regardless of dog/deacon state.
//
// Idempotent with the dog path via the shared plugin cooldown ledger: we skip if
// a dolt-backup run was recorded within the backup interval, and run.sh records
// its own run bead on completion (same type:plugin-run / plugin:dolt-backup
// labels the cooldown gate queries). Whichever path runs first wins the window;
// the other no-ops. run.sh is itself hash-deduped per database and only escalates
// on real backup failure, so periodic ticking does not spam escalations.
func (d *Daemon) runDoltBackupPlugin() {
	if !d.isPatrolActive("dolt_backup") {
		return
	}

	interval := doltBackupInterval(d.patrolConfig)

	// Cooldown gate: skip if a dolt-backup run was recorded within the interval.
	// Shared ledger with handler.dispatchPlugins keeps the two paths idempotent.
	recorder := plugin.NewRecorder(d.config.TownRoot)
	if count, err := recorder.CountRunsSince("dolt-backup", interval.String()); err != nil {
		d.logger.Printf("dolt_backup: cooldown check failed (proceeding): %v", err)
	} else if count > 0 {
		return // Still in cooldown — a recent run (dog or daemon) covered it.
	}

	// Locate the dolt-backup plugin's run.sh.
	rigNames := d.rigNamesForPluginScan()
	scanner := plugin.NewScanner(d.config.TownRoot, rigNames)
	p, err := scanner.GetPlugin("dolt-backup")
	if err != nil {
		d.logger.Printf("dolt_backup: plugin not found, skipping: %v", err)
		return
	}
	runScript := filepath.Join(p.Path, "run.sh")
	if _, statErr := os.Stat(runScript); statErr != nil {
		d.logger.Printf("dolt_backup: run.sh not found at %s, skipping: %v", runScript, statErr)
		return
	}

	d.logger.Printf("dolt_backup: running plugin run.sh (in-process, decoupled from dogs)")

	ctx, cancel := context.WithTimeout(context.Background(), doltBackupPluginTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", runScript)
	cmd.Dir = p.Path
	cmd.Env = append(os.Environ(), "GT_TOWN_ROOT="+d.config.TownRoot)
	util.SetDetachedProcessGroup(cmd)

	// run.sh records its own run bead and escalates on failure; the daemon only
	// logs the outcome here (escalating again would double-report).
	output, runErr := cmd.CombinedOutput()
	if runErr != nil {
		d.logger.Printf("dolt_backup: plugin run.sh failed: %v (%s)", runErr, strings.TrimSpace(string(output)))
		return
	}
	d.logger.Printf("dolt_backup: plugin run.sh completed: %s", lastNonEmptyLine(string(output)))
}

// rigNamesForPluginScan returns the rig names for plugin discovery, mirroring
// handler.dispatchPlugins. Returns nil (town-level only) if rigs config is
// unavailable — dolt-backup is a town-level plugin, so this still resolves it.
func (d *Daemon) rigNamesForPluginScan() []string {
	rigsConfig, err := d.loadRigsConfig()
	if err != nil || rigsConfig == nil {
		return nil
	}
	rigNames := make([]string, 0, len(rigsConfig.Rigs))
	for name := range rigsConfig.Rigs {
		rigNames = append(rigNames, name)
	}
	return rigNames
}

// lastNonEmptyLine returns the last non-empty line of s, trimmed. Used to
// surface run.sh's summary line in the daemon log without dumping all output.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// syncBackup runs `dolt backup sync <backup-name>` for a single database.
func (d *Daemon) syncBackup(dataDir, db, backupName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), doltBackupTimeout)
	defer cancel()

	dbDir := dataDir + "/" + db
	cmd := exec.CommandContext(ctx, "dolt", "backup", "sync", backupName)
	cmd.Dir = dbDir
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}

	d.logger.Printf("dolt_backup: %s: synced to %s", db, backupName)
	return nil
}

// syncOffsiteBackup rsyncs the local backup directory to iCloud Drive.
// iCloud automatically syncs to Apple's cloud, providing offsite replication.
// Non-fatal: if iCloud is unavailable or rsync fails, we just log and continue.
func (d *Daemon) syncOffsiteBackup() {
	backupDir := filepath.Join(d.config.TownRoot, ".dolt-backup")
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		return
	}

	// iCloud Drive path (macOS)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	icloudDir := filepath.Join(homeDir, "Library", "Mobile Documents", "com~apple~CloudDocs", "gt-dolt-backup")
	if err := os.MkdirAll(icloudDir, 0755); err != nil {
		d.logger.Printf("dolt_backup: offsite: cannot create iCloud dir: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "rsync", "-a", "--delete", backupDir+"/", icloudDir+"/")
	util.SetDetachedProcessGroup(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		d.logger.Printf("dolt_backup: offsite sync failed: %v (%s)", err, strings.TrimSpace(string(output)))
	} else {
		d.logger.Printf("dolt_backup: offsite synced to iCloud")
	}
}

// discoverDatabasesWithBackups lists databases in the data directory
// that have a <name>-backup backup remote configured.
func (d *Daemon) discoverDatabasesWithBackups(dataDir string) []string {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		d.logger.Printf("dolt_backup: error reading data dir: %v", err)
		return nil
	}

	var databases []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Check if this directory has a <name>-backup configured
		backupName := name + "-backup"
		if d.hasBackupRemote(dataDir, name, backupName) {
			databases = append(databases, name)
		}
	}

	return databases
}

// hasBackupRemote checks if a database has the specified backup remote configured.
func (d *Daemon) hasBackupRemote(dataDir, db, backupName string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dbDir := dataDir + "/" + db
	cmd := exec.CommandContext(ctx, "dolt", "backup")
	cmd.Dir = dbDir
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		return false
	}

	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == backupName {
			return true
		}
	}
	return false
}

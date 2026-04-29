package daemon

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/doltserver"
)

// This file contains Dolt-server health checks, startup validation, and
// log rotation. The related beads-store helpers live in beads_store.go;
// backup/remote push helpers live in dolt_backup.go and dolt_remotes.go.

// rotateOversizedLogs checks Dolt server log files and rotates any that exceed
// the size threshold. Uses copytruncate which is safe for logs held open by
// child processes. Runs every heartbeat but is cheap (just stat calls).
func (d *Daemon) rotateOversizedLogs() {
	result := RotateLogs(d.config.TownRoot)
	for _, path := range result.Rotated {
		d.logger.Printf("log_rotation: rotated %s", path)
	}
	for _, err := range result.Errors {
		d.logger.Printf("log_rotation: error: %v", err)
	}
}

// ensureDoltServerRunning ensures the Dolt SQL server is running if configured.
// This provides the backend for beads database access in server mode.
// Option B throttling: pours a mol-dog-doctor molecule only when health check
// warnings are detected, with a 5-minute cooldown to avoid wisp spam.
func (d *Daemon) ensureDoltServerRunning() {
	if d.doltServer == nil || !d.doltServer.IsEnabled() {
		return
	}

	if err := d.doltServer.EnsureRunning(); err != nil {
		d.logger.Printf("Error ensuring Dolt server is running: %v", err)
	}

	// Option B throttling: pour mol-dog-doctor only on anomaly with cooldown.
	if warnings := d.doltServer.LastWarnings(); len(warnings) > 0 {
		if time.Since(d.lastDoctorMolTime) >= doctorMolCooldown {
			d.lastDoctorMolTime = time.Now()
			go d.pourDoctorMolecule(warnings)
		}
	}

	// Update OTel gauges with the latest Dolt health snapshot.
	if d.metrics != nil {
		h := doltserver.GetHealthMetrics(d.config.TownRoot)
		d.metrics.updateDoltHealth(
			int64(h.Connections),
			int64(h.MaxConnections),
			float64(h.QueryLatency.Milliseconds()),
			h.DiskUsageBytes,
			h.Healthy,
		)
	}
}

// pourDoctorMolecule creates a mol-dog-doctor molecule to track a health anomaly.
// Runs asynchronously — molecule lifecycle is observability, not control flow.
func (d *Daemon) pourDoctorMolecule(warnings []string) {
	mol := d.pourDogMolecule(constants.MolDogDoctor, map[string]string{
		"port": strconv.Itoa(d.doltServer.config.Port),
	})
	defer mol.close()

	// Step 1: probe — connectivity was already checked (we got here because it passed).
	mol.closeStep("probe")

	// Step 2: inspect — resource checks produced the warnings.
	mol.closeStep("inspect")

	// Step 3: report — log the warning summary.
	summary := strings.Join(warnings, "; ")
	d.logger.Printf("Doctor molecule: %d warning(s): %s", len(warnings), summary)
	mol.closeStep("report")
}

// checkAllRigsDolt verifies all rigs are using the Dolt backend.
func (d *Daemon) checkAllRigsDolt() error {
	var problems []string

	// Check town-level beads
	townBeadsDir := filepath.Join(d.config.TownRoot, ".beads")
	if backend := readBeadsBackend(townBeadsDir); backend != "" && backend != "dolt" {
		problems = append(problems, fmt.Sprintf(
			"Rig %q is using %s backend.\n  Gas Town requires Dolt. Run: cd %s && bd migrate dolt",
			"town-root", backend, d.config.TownRoot))
	}

	// Check each registered rig
	for _, rigName := range d.getKnownRigs() {
		rigBeadsDir := filepath.Join(d.config.TownRoot, rigName, "mayor", "rig", ".beads")
		if backend := readBeadsBackend(rigBeadsDir); backend != "" && backend != "dolt" {
			rigPath := filepath.Join(d.config.TownRoot, rigName)
			problems = append(problems, fmt.Sprintf(
				"Rig %q is using %s backend.\n  Gas Town requires Dolt. Run: cd %s && bd migrate dolt",
				rigName, backend, rigPath))
		}
	}

	if len(problems) == 0 {
		return nil
	}

	return fmt.Errorf("daemon startup blocked: %d rig(s) not on Dolt backend\n\n  %s",
		len(problems), strings.Join(problems, "\n\n  "))
}

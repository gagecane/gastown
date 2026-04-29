package daemon

// Beads store helpers: Dolt-backed storage management, compatibility
// probing, and lifecycle (open/close). These helpers are shared by
// the daemon's convoy manager, heartbeat, and startup gates.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/util"
)

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

// readBeadsBackend reads the backend field from metadata.json in a beads directory.
// Returns empty string if the directory or metadata doesn't exist.
func readBeadsBackend(beadsDir string) string {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return ""
	}

	var metadata struct {
		Backend string `json:"backend"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return ""
	}

	return metadata.Backend
}

type beadsMetadataReader interface {
	GetMetadata(ctx context.Context, key string) (string, error)
}

type beadsDBAccessor interface {
	DB() *sql.DB
}

// embeddedBeadsVersion returns the semver of the beads module linked into this binary.
// Empty string means build info did not include a parseable module version.
func embeddedBeadsVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path != beadsModulePath {
			continue
		}
		if dep.Replace != nil {
			if version := normalizeSemver(dep.Replace.Version); version != "" {
				return version
			}
		}
		return normalizeSemver(dep.Version)
	}
	return ""
}

func normalizeSemver(version string) string {
	matches := semverPattern.FindStringSubmatch(version)
	if len(matches) != 2 {
		return ""
	}
	return matches[1]
}

func checkBeadsStoreCompatibility(ctx context.Context, stores map[string]beadsdk.Storage, binaryBeadsVersion string) error {
	if len(stores) == 0 {
		return nil
	}

	names := make([]string, 0, len(stores))
	for name := range stores {
		names = append(names, name)
	}
	sort.Strings(names)

	var problems []string
	for _, name := range names {
		problem := checkSingleBeadsStoreCompatibility(ctx, name, stores[name], binaryBeadsVersion)
		if problem != "" {
			problems = append(problems, problem)
		}
	}
	if len(problems) == 0 {
		return nil
	}

	remediation := "Upgrade or rebuild `gt` against a newer beads release, or switch to a workspace created by a matching release, then retry `gt daemon start`."
	if binaryBeadsVersion == "" {
		remediation = "Rebuild `gt` or use a release whose embedded beads version matches this workspace, then retry `gt daemon start`."
	}

	return fmt.Errorf("daemon startup blocked: incompatible beads workspace / gt binary combination\n\n  %s\n\n%s",
		strings.Join(problems, "\n  "), remediation)
}

func checkSingleBeadsStoreCompatibility(ctx context.Context, name string, store beadsdk.Storage, binaryBeadsVersion string) string {
	if store == nil {
		return ""
	}

	label := displayBeadsStoreName(name)
	var reasons []string

	if workspaceVersion, err := readStoreBDVersion(ctx, store); err != nil {
		reasons = append(reasons, fmt.Sprintf("cannot read bd_version metadata: %v", err))
	} else if workspaceVersion != "" && binaryBeadsVersion != "" && deps.CompareVersions(workspaceVersion, binaryBeadsVersion) > 0 {
		reasons = append(reasons, fmt.Sprintf("workspace bd_version %s is newer than embedded beads %s", workspaceVersion, binaryBeadsVersion))
	}

	if err := probeStoreEventSchema(ctx, store); err != nil {
		reasons = append(reasons, fmt.Sprintf("event polling probe failed: %v", err))
	}

	if len(reasons) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: %s", label, strings.Join(reasons, "; "))
}

func readStoreBDVersion(ctx context.Context, store beadsdk.Storage) (string, error) {
	if metadataStore, ok := store.(beadsMetadataReader); ok {
		return metadataStore.GetMetadata(ctx, "bd_version")
	}

	dbAccessor, ok := store.(beadsDBAccessor)
	if !ok || dbAccessor.DB() == nil {
		return "", nil
	}

	var version string
	err := dbAccessor.DB().QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = 'bd_version'").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return version, nil
}

func probeStoreEventSchema(ctx context.Context, store beadsdk.Storage) error {
	if dbAccessor, ok := store.(beadsDBAccessor); ok && dbAccessor.DB() != nil {
		for _, table := range []string{"events", "wisp_events"} {
			if err := probeEventTable(ctx, dbAccessor.DB(), table); err != nil {
				return err
			}
		}
		return nil
	}

	// Fall back to the typed API if the store doesn't expose raw SQL.
	_, err := store.GetAllEventsSince(ctx, time.Now().Add(24*time.Hour).UTC())
	return err
}

func probeEventTable(ctx context.Context, db *sql.DB, table string) error {
	query := fmt.Sprintf("SELECT id, created_at FROM %s ORDER BY created_at DESC LIMIT 1", table)

	var (
		id        string
		createdAt time.Time
	)
	err := db.QueryRowContext(ctx, query).Scan(&id, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s table probe: %w", table, err)
	}
	return nil
}

func displayBeadsStoreName(name string) string {
	if name == "hq" {
		return "town-root beads store"
	}
	return fmt.Sprintf("rig %q beads store", name)
}

func closeBeadsStores(logger *log.Logger, stores map[string]beadsdk.Storage) {
	for name, store := range stores {
		if store == nil {
			continue
		}
		if err := store.Close(); err != nil {
			if logger != nil {
				logger.Printf("Convoy: error closing beads store (%s): %v", name, err)
			}
			continue
		}
		if logger != nil {
			logger.Printf("Convoy: closed beads store (%s)", name)
		}
	}
}

// openBeadsStores opens the town-level and per-rig beads stores, applying
// compatibility checks before returning. If any store is incompatible with
// the embedded beads version, all opened stores are closed and an error is
// returned so the caller can abort startup.
func (d *Daemon) openBeadsStores() (map[string]beadsdk.Storage, error) {
	stores := make(map[string]beadsdk.Storage)

	// Town-level store (hq)
	hqBeadsDir := filepath.Join(d.config.TownRoot, ".beads")
	if store, err := beadsdk.OpenFromConfig(d.ctx, hqBeadsDir); err == nil {
		stores["hq"] = store
	} else {
		d.logger.Printf("Convoy: hq beads store unavailable: %s", util.FirstLine(err.Error()))
	}

	// Per-rig stores
	for _, rigName := range d.getKnownRigs() {
		beadsDir := doltserver.FindRigBeadsDir(d.config.TownRoot, rigName)
		if beadsDir == "" {
			continue
		}
		store, err := beadsdk.OpenFromConfig(d.ctx, beadsDir)
		if err != nil {
			d.logger.Printf("Convoy: %s beads store unavailable: %s", rigName, util.FirstLine(err.Error()))
			continue
		}
		stores[rigName] = store
	}

	if len(stores) == 0 {
		d.logger.Printf("Convoy: no beads stores available, event polling disabled")
		return nil, nil
	}

	if err := checkBeadsStoreCompatibility(d.ctx, stores, embeddedBeadsVersion()); err != nil {
		closeBeadsStores(d.logger, stores)
		return nil, err
	}

	names := make([]string, 0, len(stores))
	for name := range stores {
		names = append(names, name)
	}
	d.logger.Printf("Convoy: opened %d beads store(s): %v", len(stores), names)
	return stores, nil
}

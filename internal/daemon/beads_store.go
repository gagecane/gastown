package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/util"
)

// This file contains beads-store compatibility checking and lifecycle helpers
// used at daemon startup and by the convoy manager. See daemon.go for the main
// Daemon type and Run loop.

const beadsModulePath = "github.com/steveyegge/beads"

var semverPattern = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)

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

// openBeadsStores opens beads stores for the town (hq) and all known rigs.
// Returns a map keyed by "hq" for town-level and rig names for per-rig stores.
// Stores that fail to open are logged and skipped. Successfully opened stores
// are compatibility-checked before being returned to Convoy polling.
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

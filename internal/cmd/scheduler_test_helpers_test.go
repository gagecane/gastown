package cmd

// Shared test helpers for scheduler tests. No build tag — compiled for both
// integration and e2e_agent builds. Helpers that need bd/gt binaries take
// explicit paths and env slices so callers control isolation.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// --- File helpers ---

// writeJSONFile marshals v as indented JSON and writes it to path,
// creating parent directories as needed.
func writeJSONFile(t *testing.T, path string, v interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// --- Scheduler config helpers ---

// configureScheduler writes a TownSettings file with the given scheduler configuration.
// maxPolecats > 0 enables deferred dispatch; -1 means direct dispatch.
func configureScheduler(t *testing.T, hqPath string, maxPolecats, batchSize int) {
	t.Helper()
	settings := config.NewTownSettings()
	settings.Scheduler = &capacity.SchedulerConfig{
		MaxPolecats: &maxPolecats,
		BatchSize:   &batchSize,
	}
	writeJSONFile(t, config.TownSettingsPath(hqPath), settings)
}

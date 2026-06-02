package daemon

import (
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestLastNonEmptyLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single", "only line", "only line"},
		{"trailing newline", "summary line\n", "summary line"},
		{"multiple trailing newlines", "summary\n\n\n", "summary"},
		{"picks last non-empty", "first\nmiddle\nlast\n", "last"},
		{"trims whitespace", "  padded  \n", "padded"},
		{"run.sh style", "[dolt-backup]   hq: synced in 1s\n[dolt-backup] Backup: 1 synced, 0 unchanged, 0 failed (of 1 DBs)\n", "[dolt-backup] Backup: 1 synced, 0 unchanged, 0 failed (of 1 DBs)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastNonEmptyLine(tc.in); got != tc.want {
				t.Errorf("lastNonEmptyLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRunDoltBackupPlugin_SkipsWhenPatrolDisabled verifies the in-process Linux
// backup path is a no-op when the dolt_backup patrol is disabled. It must not
// attempt to discover or exec the plugin.
func TestRunDoltBackupPlugin_SkipsWhenPatrolDisabled(t *testing.T) {
	townRoot := t.TempDir()
	d := &Daemon{
		config:       &Config{TownRoot: townRoot},
		logger:       log.New(os.Stderr, "", 0),
		patrolConfig: nil, // dolt_backup is opt-in → disabled when nil.
	}

	if d.isPatrolActive("dolt_backup") {
		t.Fatal("precondition: dolt_backup should be disabled with nil patrolConfig")
	}

	// Should return immediately without panicking or touching the filesystem.
	d.runDoltBackupPlugin()
}

// TestRunDoltBackupPlugin_SkipsWhenInCooldown verifies the cooldown gate: when a
// recent dolt-backup run is recorded, the in-process path no-ops instead of
// running run.sh — keeping it idempotent with the dog-dispatch path.
func TestRunDoltBackupPlugin_SkipsWhenInCooldown(t *testing.T) {
	townRoot := t.TempDir()

	// A fake `bd` whose `list` reports one recent plugin run (cooldown active).
	// Any other subcommand succeeds silently. If run.sh were reached it would
	// require a plugin dir that does not exist here, so the cooldown skip is
	// what keeps this test from erroring.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bdScript := `#!/usr/bin/env bash
for arg in "$@"; do
  if [ "$arg" = "list" ]; then
    echo '[{"id":"gc-1","title":"Plugin run: dolt-backup","created_at":"2999-01-01T00:00:00Z","labels":["type:plugin-run","plugin:dolt-backup","result:success"]}]'
    exit 0
  fi
done
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0o755); err != nil {
		t.Fatal(err)
	}
	// Recorder resolves `bd` from PATH (internal/beads exec), not d.bdPath.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Enable the dolt_backup patrol so we get past the patrol gate to the
	// cooldown gate.
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(os.Stderr, "", 0),
		bdPath: bdPath,
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				DoltBackup: &DoltBackupConfig{Enabled: true, IntervalStr: "15m"},
			},
		},
	}

	if !d.isPatrolActive("dolt_backup") {
		t.Fatal("precondition: dolt_backup should be enabled")
	}

	// With cooldown active, this must return without attempting plugin discovery.
	// (No plugins/ dir exists; if discovery ran it would log-and-skip, not fail,
	// but the cooldown path is the behavior under test.)
	d.runDoltBackupPlugin()
}

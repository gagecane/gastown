package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureRigsDoltAutoCommit verifies the gs-onu startup self-heal: every
// rig's resolved beads config (and the town config) gets dolt.auto-commit=on so
// ephemeral MR beads from `gt done` commit to shared main instead of stranding,
// while an operator's explicit value is preserved and missing dirs are skipped.
func TestEnsureRigsDoltAutoCommit(t *testing.T) {
	town := t.TempDir()

	// town-level beads config: no auto-commit key → should be added.
	writeBeadsConfig(t, filepath.Join(town, ".beads"), "storage.backend: dolt\n")

	// rig "fresh": resolved config (mayor/rig/.beads) missing the key → added.
	writeBeadsConfig(t, filepath.Join(town, "fresh", "mayor", "rig", ".beads"),
		"dolt.idle-timeout: \"0\"\n")

	// rig "explicit": operator set auto-commit=off → must be preserved.
	writeBeadsConfig(t, filepath.Join(town, "explicit", "mayor", "rig", ".beads"),
		"dolt.auto-commit: \"off\"\n")

	// rig "norig": no beads dir at all → must be skipped without error.

	d := &Daemon{
		config:              &Config{TownRoot: town},
		logger:              log.New(io.Discard, "", 0),
		knownRigsCache:      []string{"fresh", "explicit", "norig"},
		knownRigsCacheValid: true,
	}

	d.ensureRigsDoltAutoCommit()

	assertAutoCommit(t, filepath.Join(town, ".beads"), "on")
	assertAutoCommit(t, filepath.Join(town, "fresh", "mayor", "rig", ".beads"), "on")
	assertAutoCommit(t, filepath.Join(town, "explicit", "mayor", "rig", ".beads"), "off")

	// norig had no beads dir — nothing should have been created.
	if _, err := os.Stat(filepath.Join(town, "norig")); !os.IsNotExist(err) {
		t.Errorf("rig with no beads dir should be left untouched")
	}
}

func writeBeadsConfig(t *testing.T, beadsDir, content string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", beadsDir, err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("write config %s: %v", beadsDir, err)
	}
}

func assertAutoCommit(t *testing.T, beadsDir, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config %s: %v", beadsDir, err)
	}
	wantLine := "dolt.auto-commit: \"" + want + "\""
	if !strings.Contains(string(data), wantLine) {
		t.Errorf("config at %s: expected %q, got:\n%s", beadsDir, wantLine, data)
	}
}

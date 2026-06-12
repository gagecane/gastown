package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProcessAlive_CurrentProcess(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("current process should be detected as running")
	}
}

func TestProcessAlive_InvalidPID(t *testing.T) {
	if processAlive(99999999) {
		t.Error("invalid PID should not be detected as running")
	}
}

func TestProcessAlive_MaxPID(t *testing.T) {
	if processAlive(2147483647) {
		t.Error("max PID should not be running")
	}
}

func TestEmbeddedBeadsDoltDBs(t *testing.T) {
	// Single-database layout: the .dolt lives directly in the dir.
	single := t.TempDir()
	mustMkdir(t, filepath.Join(single, ".dolt"))
	if got := embeddedBeadsDoltDBs(single); len(got) != 1 || got[0] != single {
		t.Errorf("single-db layout: got %v, want [%s]", got, single)
	}

	// Multi-database layout: each subdir is its own database; .dolt and
	// .doltcfg at the top level are not databases.
	multi := t.TempDir()
	mustMkdir(t, filepath.Join(multi, ".doltcfg"))
	mustMkdir(t, filepath.Join(multi, "hq", ".dolt"))
	mustMkdir(t, filepath.Join(multi, "gastown", ".dolt"))
	mustMkdir(t, filepath.Join(multi, "config.yaml-not-a-dir")) // dir without .dolt is ignored
	got := embeddedBeadsDoltDBs(multi)
	if len(got) != 2 {
		t.Fatalf("multi-db layout: got %d dbs %v, want 2", len(got), got)
	}

	// No databases at all (empty dir).
	if got := embeddedBeadsDoltDBs(t.TempDir()); len(got) != 0 {
		t.Errorf("empty dir: got %v, want none", got)
	}
}

func TestStrandedBeadsDolt(t *testing.T) {
	dolt := requireDolt(t)

	town := t.TempDir()
	// Canonical store holds hq-1 and hq-2.
	canonHQ := filepath.Join(town, ".dolt-data", "hq")
	mustInitDoltIssues(t, dolt, canonHQ, "hq-1", "hq-2")

	canonical := &canonicalBeadIndex{townRoot: town}

	// Case 1: legacy copy whose beads are all already migrated → safe.
	migrated := filepath.Join(t.TempDir(), ".beads", "dolt", "hq")
	mustInitDoltIssues(t, dolt, migrated, "hq-1") // subset of canonical
	if s := strandedBeadsDolt(filepath.Dir(migrated), canonical); len(s) != 0 {
		t.Errorf("already-migrated copy should be safe, got stranded %+v", s)
	}

	// Case 2: legacy copy with a bead absent from canonical → stranded.
	stranded := filepath.Join(t.TempDir(), ".beads", "dolt", "hq")
	mustInitDoltIssues(t, dolt, stranded, "hq-1", "hq-az8") // hq-az8 not in canonical
	got := strandedBeadsDolt(filepath.Dir(stranded), canonical)
	if len(got) != 1 {
		t.Fatalf("expected 1 stranded database, got %d: %+v", len(got), got)
	}
	if got[0].count != 1 || got[0].sample != "hq-az8" {
		t.Errorf("stranded report = %+v, want count=1 sample=hq-az8", got[0])
	}
	if isSafeToRemoveBeadsDolt(filepath.Dir(stranded), canonical) {
		t.Error("directory with unmigrated bead should not be safe to remove")
	}

	// Case 3: empty init scaffold (no issues table) → safe.
	scaffold := filepath.Join(t.TempDir(), ".beads", "dolt")
	mustMkdir(t, scaffold)
	runDolt(t, dolt, scaffold, "init")
	if s := strandedBeadsDolt(scaffold, canonical); len(s) != 0 {
		t.Errorf("empty scaffold should be safe, got stranded %+v", s)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func requireDolt(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("dolt")
	if err != nil {
		t.Skip("dolt binary not available")
	}
	// Isolate dolt's global config so the test never reads or writes the
	// operator's real config.
	root := t.TempDir()
	t.Setenv("DOLT_ROOT_PATH", root)
	runDolt(t, path, root, "config", "--global", "--add", "user.name", "gt-test")
	runDolt(t, path, root, "config", "--global", "--add", "user.email", "gt-test@example.com")
	return path
}

func runDolt(t *testing.T, dolt, dir string, args ...string) {
	t.Helper()
	mustMkdir(t, dir)
	cmd := exec.Command(dolt, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt %v in %s: %v\n%s", args, dir, err, out)
	}
}

func mustInitDoltIssues(t *testing.T, dolt, dir string, ids ...string) {
	t.Helper()
	runDolt(t, dolt, dir, "init")
	runDolt(t, dolt, dir, "sql", "-q", "CREATE TABLE issues (id varchar(64) primary key, title varchar(255))")
	for _, id := range ids {
		runDolt(t, dolt, dir, "sql", "-q", "INSERT INTO issues (id) VALUES ('"+id+"')")
	}
}

package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewTrackedIgnoredBeadsBackupCheck(t *testing.T) {
	c := NewTrackedIgnoredBeadsBackupCheck()
	if c.Name() != "tracked-ignored-beads-backup" {
		t.Errorf("Name() = %q, want tracked-ignored-beads-backup", c.Name())
	}
	if c.CanFix() {
		t.Error("CanFix() = true, want false (warn-only check)")
	}
}

// initBackupRig creates a rig git repo with a .beads/ dir that ignores backup/.
// When trackBackup is true, it force-adds a .beads/backup/issues.jsonl file so
// it becomes tracked-but-ignored.
func initBackupRig(t *testing.T, rigDir string, trackBackup bool) {
	t.Helper()
	beadsDir := filepath.Join(rigDir, ".beads", "backup")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, rigDir, "init", "-b", "main")

	// .gitignore excludes the backup dir.
	if err := os.WriteFile(filepath.Join(rigDir, ".gitignore"), []byte(".beads/backup/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write backup file: %v", err)
	}
	runGit(t, rigDir, "add", ".gitignore")
	if trackBackup {
		// Force-add the ignored file to reproduce the tracked-but-ignored state.
		runGit(t, rigDir, "add", "-f", ".beads/backup/issues.jsonl")
	}
	runGit(t, rigDir, "commit", "-m", "initial")
}

func TestTrackedIgnoredBeadsBackupCheck_DetectsTracked(t *testing.T) {
	town := t.TempDir()
	rig := filepath.Join(town, "myrig")
	initBackupRig(t, rig, true)

	res := NewTrackedIgnoredBeadsBackupCheck().Run(&CheckContext{TownRoot: town})
	if res.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; msg=%q details=%v", res.Status, res.Message, res.Details)
	}
	if len(res.Details) != 1 {
		t.Fatalf("Details = %v, want exactly 1 entry", res.Details)
	}
}

func TestTrackedIgnoredBeadsBackupCheck_CleanWhenUntracked(t *testing.T) {
	town := t.TempDir()
	rig := filepath.Join(town, "myrig")
	initBackupRig(t, rig, false)

	res := NewTrackedIgnoredBeadsBackupCheck().Run(&CheckContext{TownRoot: town})
	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details=%v", res.Status, res.Details)
	}
}

func TestTrackedIgnoredBackupFiles(t *testing.T) {
	rig := t.TempDir()
	initBackupRig(t, rig, true)

	files := trackedIgnoredBackupFiles(rig)
	if len(files) != 1 || files[0] != ".beads/backup/issues.jsonl" {
		t.Fatalf("trackedIgnoredBackupFiles = %v, want [.beads/backup/issues.jsonl]", files)
	}

	// A non-git directory yields no results without erroring.
	if got := trackedIgnoredBackupFiles(t.TempDir()); got != nil {
		t.Errorf("non-git dir = %v, want nil", got)
	}
}

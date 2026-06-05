package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMayorRigExclude creates a mayor/rig/.git/info/exclude file with the given
// content and returns the rig dir and exclude path. The check inspects mayor/rig
// as the authoritative clone.
func writeMayorRigExclude(t *testing.T, content string) (rigDir, excludePath string) {
	t.Helper()
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir = filepath.Join(tmpDir, rigName)
	infoDir := filepath.Join(rigDir, "mayor", "rig", ".git", "info")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		t.Fatal(err)
	}
	excludePath = filepath.Join(infoDir, "exclude")
	if err := os.WriteFile(excludePath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return tmpDir, excludePath
}

func TestGitExcludeConfiguredCheck_MigratesBareClaude(t *testing.T) {
	// Legacy clone: all required dirs present, but a bare .claude/ line that
	// kills the commands/skills negations (gu-w1bge).
	content := "/polecats/\n/witness/\n/refinery/\n/mayor/\n.runtime/\n.claude/\n!.claude/commands/\n!.claude/skills/\n"
	tmpDir, excludePath := writeMayorRigExclude(t, content)

	check := NewGitExcludeConfiguredCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "testrig"}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning for stale .claude/ exclude, got %v: %s", result.Status, result.Message)
	}
	if !check.staleClaudeExclude {
		t.Fatal("expected staleClaudeExclude to be flagged")
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error = %v", err)
	}

	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	// Bare .claude/ must be gone; .claude/* + negations must remain.
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == ".claude/" {
			t.Error("bare .claude/ should be migrated away")
		}
	}
	if !strings.Contains(got, ".claude/*") {
		t.Error(".claude/* should be present after migration")
	}
	if !strings.Contains(got, "!.claude/commands/") || !strings.Contains(got, "!.claude/skills/") {
		t.Error("negations should be preserved after migration")
	}

	// Re-running Run should now be clean.
	check2 := NewGitExcludeConfiguredCheck()
	if res := check2.Run(ctx); res.Status != StatusOK {
		t.Errorf("expected StatusOK after migration, got %v: %s", res.Status, res.Message)
	}
}

func TestGitExcludeConfiguredCheck_NoMigrationWhenAlreadyCorrect(t *testing.T) {
	content := "/polecats/\n/witness/\n/refinery/\n/mayor/\n.runtime/\n.claude/*\n!.claude/commands/\n!.claude/skills/\n"
	tmpDir, excludePath := writeMayorRigExclude(t, content)

	check := NewGitExcludeConfiguredCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "testrig"}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK when exclude is already correct, got %v: %s", result.Status, result.Message)
	}
	if check.staleClaudeExclude {
		t.Error("staleClaudeExclude should be false for .claude/* form")
	}

	// File must be untouched by a no-op Fix.
	before, _ := os.ReadFile(excludePath)
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error = %v", err)
	}
	after, _ := os.ReadFile(excludePath)
	if string(before) != string(after) {
		t.Error("exclude file should be unchanged when already correct")
	}
}

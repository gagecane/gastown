package rig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyOverlay_NoOverlayDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	destDir := t.TempDir()

	// No overlay directory exists
	err := CopyOverlay(tmpDir, destDir)
	if err != nil {
		t.Errorf("CopyOverlay() with no overlay directory should return nil, got %v", err)
	}
}

func TestCopyOverlay_CopiesFiles(t *testing.T) {
	rigDir := t.TempDir()
	destDir := t.TempDir()

	// Create overlay directory with test files
	overlayDir := filepath.Join(rigDir, ".runtime", "overlay")
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		t.Fatalf("Failed to create overlay dir: %v", err)
	}

	// Create test files
	testFile1 := filepath.Join(overlayDir, "test1.txt")
	testFile2 := filepath.Join(overlayDir, "test2.txt")

	if err := os.WriteFile(testFile1, []byte("content1"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	if err := os.WriteFile(testFile2, []byte("content2"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Copy overlay
	err := CopyOverlay(rigDir, destDir)
	if err != nil {
		t.Fatalf("CopyOverlay() error = %v", err)
	}

	// Verify files were copied
	destFile1 := filepath.Join(destDir, "test1.txt")
	destFile2 := filepath.Join(destDir, "test2.txt")

	content1, err := os.ReadFile(destFile1)
	if err != nil {
		t.Errorf("File test1.txt was not copied: %v", err)
	}
	if string(content1) != "content1" {
		t.Errorf("test1.txt content = %q, want %q", string(content1), "content1")
	}

	content2, err := os.ReadFile(destFile2)
	if err != nil {
		t.Errorf("File test2.txt was not copied: %v", err)
	}
	if string(content2) != "content2" {
		t.Errorf("test2.txt content = %q, want %q", string(content2), "content2")
	}
}

func TestCopyOverlay_PreservesPermissions(t *testing.T) {
	rigDir := t.TempDir()
	destDir := t.TempDir()

	// Create overlay directory with a file
	overlayDir := filepath.Join(rigDir, ".runtime", "overlay")
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		t.Fatalf("Failed to create overlay dir: %v", err)
	}

	testFile := filepath.Join(overlayDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0755); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Copy overlay
	err := CopyOverlay(rigDir, destDir)
	if err != nil {
		t.Fatalf("CopyOverlay() error = %v", err)
	}

	// Verify permissions were preserved
	srcInfo, _ := os.Stat(testFile)
	destInfo, err := os.Stat(filepath.Join(destDir, "test.txt"))
	if err != nil {
		t.Fatalf("Failed to stat destination file: %v", err)
	}

	if srcInfo.Mode().Perm() != destInfo.Mode().Perm() {
		t.Errorf("Permissions not preserved: src=%v, dest=%v", srcInfo.Mode(), destInfo.Mode())
	}
}

func TestCopyOverlay_SkipsSubdirectories(t *testing.T) {
	rigDir := t.TempDir()
	destDir := t.TempDir()

	// Create overlay directory with a subdirectory
	overlayDir := filepath.Join(rigDir, ".runtime", "overlay")
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		t.Fatalf("Failed to create overlay dir: %v", err)
	}

	// Create a subdirectory
	subDir := filepath.Join(overlayDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdirectory: %v", err)
	}

	// Create a file in the overlay root
	testFile := filepath.Join(overlayDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create a file in the subdirectory
	subFile := filepath.Join(subDir, "sub.txt")
	if err := os.WriteFile(subFile, []byte("subcontent"), 0644); err != nil {
		t.Fatalf("Failed to create sub file: %v", err)
	}

	// Copy overlay
	err := CopyOverlay(rigDir, destDir)
	if err != nil {
		t.Fatalf("CopyOverlay() error = %v", err)
	}

	// Verify root file was copied
	if _, err := os.Stat(filepath.Join(destDir, "test.txt")); err != nil {
		t.Error("Root file should be copied")
	}

	// Verify subdirectory was NOT copied
	if _, err := os.Stat(filepath.Join(destDir, "subdir")); err == nil {
		t.Error("Subdirectory should not be copied")
	}
	if _, err := os.Stat(filepath.Join(destDir, "subdir", "sub.txt")); err == nil {
		t.Error("File in subdirectory should not be copied")
	}
}

func TestCopyOverlay_EmptyOverlay(t *testing.T) {
	rigDir := t.TempDir()
	destDir := t.TempDir()

	// Create empty overlay directory
	overlayDir := filepath.Join(rigDir, ".runtime", "overlay")
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		t.Fatalf("Failed to create overlay dir: %v", err)
	}

	// Copy overlay
	err := CopyOverlay(rigDir, destDir)
	if err != nil {
		t.Fatalf("CopyOverlay() error = %v", err)
	}

	// Should succeed without errors
}

func TestCopyOverlay_OverwritesExisting(t *testing.T) {
	rigDir := t.TempDir()
	destDir := t.TempDir()

	// Create overlay directory with test file
	overlayDir := filepath.Join(rigDir, ".runtime", "overlay")
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		t.Fatalf("Failed to create overlay dir: %v", err)
	}

	testFile := filepath.Join(overlayDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("new content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create existing file in destination with different content
	destFile := filepath.Join(destDir, "test.txt")
	if err := os.WriteFile(destFile, []byte("old content"), 0644); err != nil {
		t.Fatalf("Failed to create dest file: %v", err)
	}

	// Copy overlay
	err := CopyOverlay(rigDir, destDir)
	if err != nil {
		t.Fatalf("CopyOverlay() error = %v", err)
	}

	// Verify file was overwritten
	content, err := os.ReadFile(destFile)
	if err != nil {
		t.Fatalf("Failed to read dest file: %v", err)
	}
	if string(content) != "new content" {
		t.Errorf("File content = %q, want %q", string(content), "new content")
	}
}

func TestCopyFilePreserveMode(t *testing.T) {
	tmpDir := t.TempDir()

	// Create source file
	srcFile := filepath.Join(tmpDir, "src.txt")
	if err := os.WriteFile(srcFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create src file: %v", err)
	}

	// Copy file
	dstFile := filepath.Join(tmpDir, "dst.txt")
	err := copyFilePreserveMode(srcFile, dstFile)
	if err != nil {
		t.Fatalf("copyFilePreserveMode() error = %v", err)
	}

	// Verify content
	content, err := os.ReadFile(dstFile)
	if err != nil {
		t.Errorf("Failed to read dst file: %v", err)
	}
	if string(content) != "test content" {
		t.Errorf("Content = %q, want %q", string(content), "test content")
	}

	// Verify permissions
	srcInfo, _ := os.Stat(srcFile)
	dstInfo, err := os.Stat(dstFile)
	if err != nil {
		t.Fatalf("Failed to stat dst file: %v", err)
	}
	if srcInfo.Mode().Perm() != dstInfo.Mode().Perm() {
		t.Errorf("Permissions not preserved: src=%v, dest=%v", srcInfo.Mode(), dstInfo.Mode())
	}
}

func TestCopyFilePreserveMode_NonexistentSource(t *testing.T) {
	tmpDir := t.TempDir()

	srcFile := filepath.Join(tmpDir, "nonexistent.txt")
	dstFile := filepath.Join(tmpDir, "dst.txt")

	err := copyFilePreserveMode(srcFile, dstFile)
	if err == nil {
		t.Error("copyFilePreserveMode() with nonexistent source should return error")
	}
}

func TestEnsureGitignorePatterns_CreatesNewFile(t *testing.T) {
	tmpDir := t.TempDir()

	err := EnsureGitignorePatterns(tmpDir)
	if err != nil {
		t.Fatalf("EnsureGitignorePatterns() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	// Check all required patterns are present (.beads/ intentionally excluded — see overlay.go).
	// .claude is now emitted as ".claude/*" + negations so the nested commands/skills
	// re-inclusions are live (gu-w1bge).
	patterns := []string{".runtime/", ".claude/*", "!.claude/commands/", "!.claude/skills/", ".opencode/", ".logs/", "__pycache__/", "state.json"}
	for _, pattern := range patterns {
		if !containsLine(string(content), pattern) {
			t.Errorf(".gitignore missing pattern %q", pattern)
		}
	}

	// The buggy bare ".claude/" form must NOT be emitted — it kills the negations.
	if containsLine(string(content), ".claude/") {
		t.Error(".gitignore must not contain bare .claude/ (kills commands/skills negations)")
	}
}

func TestEnsureGitignorePatterns_AppendsToExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Create existing .gitignore with some content
	existing := "node_modules/\n*.log\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}

	err := EnsureGitignorePatterns(tmpDir)
	if err != nil {
		t.Fatalf("EnsureGitignorePatterns() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	// Should preserve existing content
	if !containsLine(string(content), "node_modules/") {
		t.Error("Existing pattern node_modules/ was removed")
	}

	// Should add header
	if !containsLine(string(content), "# Gas Town (added by gt)") {
		t.Error("Missing Gas Town header comment")
	}

	// Should add required patterns (.beads/ intentionally excluded — see overlay.go)
	patterns := []string{".runtime/", ".claude/*", "!.claude/commands/", "!.claude/skills/", ".opencode/", ".logs/", "__pycache__/", "state.json"}
	for _, pattern := range patterns {
		if !containsLine(string(content), pattern) {
			t.Errorf(".gitignore missing pattern %q", pattern)
		}
	}
}

func TestEnsureGitignorePatterns_SkipsExistingPatterns(t *testing.T) {
	tmpDir := t.TempDir()

	// Create existing .gitignore with some Gas Town patterns already, including
	// the legacy bare ".claude/" line. EnsureGitignorePatterns must migrate it
	// to ".claude/*" in place (gu-w1bge) and add the negations.
	existing := ".runtime/\n.claude/\n.opencode/\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}

	err := EnsureGitignorePatterns(tmpDir)
	if err != nil {
		t.Fatalf("EnsureGitignorePatterns() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	// Should not duplicate existing patterns
	count := countOccurrences(string(content), ".runtime/")
	if count != 1 {
		t.Errorf(".runtime/ appears %d times, expected 1", count)
	}

	// The legacy bare ".claude/" must be migrated away (zero occurrences) and
	// replaced by the traversable ".claude/*" form plus negations.
	if containsLine(string(content), ".claude/") {
		t.Error("bare .claude/ should be migrated to .claude/* (kills negations)")
	}
	wildcardCount := countOccurrences(string(content), ".claude/*")
	if wildcardCount != 1 {
		t.Errorf(".claude/* appears %d times, expected 1", wildcardCount)
	}
	if !containsLine(string(content), "!.claude/commands/") {
		t.Error(".gitignore missing negation !.claude/commands/")
	}
	if !containsLine(string(content), "!.claude/skills/") {
		t.Error(".gitignore missing negation !.claude/skills/")
	}
	opencodeCount := countOccurrences(string(content), ".opencode/")
	if opencodeCount != 1 {
		t.Errorf(".opencode/ appears %d times, expected 1", opencodeCount)
	}

	// Should add missing patterns
	if !containsLine(string(content), ".logs/") {
		t.Error(".gitignore missing pattern .logs/")
	}
	if !containsLine(string(content), "__pycache__/") {
		t.Error(".gitignore missing pattern __pycache__/")
	}
	if !containsLine(string(content), "state.json") {
		t.Error(".gitignore missing pattern state.json")
	}

	// Regression guard: .beads/ must NOT be in required patterns.
	// Beads manages its own .beads/.gitignore via bd init.
	// Adding .beads/ here breaks bd sync. This has regressed twice
	// (PR #753, #966). If this test fails, you're about to break polecats.
	if containsLine(string(content), ".beads/") {
		t.Error(".gitignore must NOT contain .beads/ - beads manages its own .gitignore (see overlay.go comment)")
	}
}

func TestEnsureGitignorePatterns_RecognizesVariants(t *testing.T) {
	tmpDir := t.TempDir()

	// Create existing .gitignore with variant patterns (without trailing slash).
	// ".runtime"/"/.opencode" variants should still be recognized (no duplicate),
	// while the bare "/.claude" form is a legacy claude pattern that must be
	// migrated to ".claude/*" + negations (gu-w1bge).
	existing := ".runtime\n/.claude\n/.opencode\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}

	err := EnsureGitignorePatterns(tmpDir)
	if err != nil {
		t.Fatalf("EnsureGitignorePatterns() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	// Should recognize variants and not add duplicates
	// .runtime (no slash) should count as .runtime/
	runtimeCount := countOccurrences(string(content), ".runtime")
	if runtimeCount > 1 {
		t.Errorf(".runtime appears %d times (variant detection failed)", runtimeCount)
	}
	if containsLine(string(content), ".opencode/") {
		t.Error(".opencode/ should not be added when /.opencode already covers it")
	}

	// The bare /.claude form should be migrated to .claude/* + negations.
	if containsLine(string(content), "/.claude") || containsLine(string(content), ".claude/") {
		t.Error("bare /.claude should be migrated to .claude/*")
	}
	if !containsLine(string(content), ".claude/*") {
		t.Error(".claude/* should be present after migration of /.claude")
	}
	if !containsLine(string(content), "!.claude/commands/") || !containsLine(string(content), "!.claude/skills/") {
		t.Error("negations should be added after migrating /.claude")
	}
}

func TestEnsureGitignorePatterns_AllPatternsPresent(t *testing.T) {
	tmpDir := t.TempDir()

	// Create existing .gitignore with all required patterns in their correct
	// (post-gu-w1bge) form: .claude/* + negations, not bare .claude/.
	existing := ".runtime/\n.claude/*\n!.claude/commands/\n!.claude/skills/\n.opencode/\n.beads/\n.logs/\n__pycache__/\nstate.json\nCLAUDE.md\nCLAUDE.local.md\nGEMINI.md\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}

	err := EnsureGitignorePatterns(tmpDir)
	if err != nil {
		t.Fatalf("EnsureGitignorePatterns() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	// File should be unchanged (no header added)
	if containsLine(string(content), "# Gas Town") {
		t.Error("Should not add header when all patterns already present")
	}

	// Content should match original
	if string(content) != existing {
		t.Errorf("File was modified when it shouldn't be.\nGot: %q\nWant: %q", string(content), existing)
	}
}

func TestEnsureGitignorePatterns_NarrowPatternPresent(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .gitignore with the exact required patterns (post-gu-w1bge form).
	existing := ".runtime/\n.claude/*\n!.claude/commands/\n!.claude/skills/\n.opencode/\n.logs/\n__pycache__/\nstate.json\nCLAUDE.md\nCLAUDE.local.md\nGEMINI.md\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}

	err := EnsureGitignorePatterns(tmpDir)
	if err != nil {
		t.Fatalf("EnsureGitignorePatterns() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	// File should be unchanged
	if string(content) != existing {
		t.Errorf("File was modified when it shouldn't be.\nGot: %q\nWant: %q", string(content), existing)
	}
}

func TestEnsureGitignorePatterns_OldNarrowClaudeUpgraded(t *testing.T) {
	tmpDir := t.TempDir()

	// Simulate old installation with the narrow .claude/commands/ ignore pattern
	// (note: this is an *ignore* of commands, not a negation). After upgrade,
	// .claude/* + negations should be added since .claude/commands/ does NOT
	// cover them.
	existing := ".runtime/\n.claude/commands/\n.logs/\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}

	err := EnsureGitignorePatterns(tmpDir)
	if err != nil {
		t.Fatalf("EnsureGitignorePatterns() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	// .claude/* should be added (old .claude/commands/ doesn't cover it)
	if !containsLine(string(content), ".claude/*") {
		t.Error(".claude/* should be added when only .claude/commands/ was present")
	}
	if !containsLine(string(content), "!.claude/commands/") || !containsLine(string(content), "!.claude/skills/") {
		t.Error("negations should be added")
	}

	// __pycache__/ should be added
	if !containsLine(string(content), "__pycache__/") {
		t.Error("__pycache__/ should be added")
	}
}

func TestEnsureGitignorePatterns_UpgradeMigratesBareClaude(t *testing.T) {
	tmpDir := t.TempDir()

	// Simulate an existing installation that has the legacy bare .claude/ line
	// plus other Gas Town patterns but is missing __pycache__/ (added later).
	// After upgrade, __pycache__/ should be appended AND the bare .claude/ line
	// should be migrated to .claude/* + negations (gu-w1bge).
	existing := "# Gas Town (added by gt)\n.runtime/\n.claude/\n.logs/\n"
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}

	err := EnsureGitignorePatterns(tmpDir)
	if err != nil {
		t.Fatalf("EnsureGitignorePatterns() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	// __pycache__/ should be appended
	if !containsLine(string(content), "__pycache__/") {
		t.Error("__pycache__/ should be added during upgrade")
	}

	// Existing patterns should be preserved
	if !containsLine(string(content), ".runtime/") {
		t.Error(".runtime/ should be preserved")
	}

	// Bare .claude/ must be migrated to .claude/* + negations.
	if containsLine(string(content), ".claude/") {
		t.Error("bare .claude/ should be migrated away during upgrade")
	}
	if !containsLine(string(content), ".claude/*") {
		t.Error(".claude/* should be present after migration")
	}
	if !containsLine(string(content), "!.claude/commands/") || !containsLine(string(content), "!.claude/skills/") {
		t.Error("negations should be present after migration")
	}
}

// TestMigrateClaudeIgnorePattern covers the in-place migration of the legacy
// bare ".claude/" line to the traversable ".claude/*" form (gu-w1bge).
func TestMigrateClaudeIgnorePattern(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantChanged bool
		wantOut     string
	}{
		{
			name:        "bare trailing-slash form is rewritten in place",
			in:          ".runtime/\n.claude/\n!.claude/commands/\n",
			wantChanged: true,
			wantOut:     ".runtime/\n.claude/*\n!.claude/commands/\n",
		},
		{
			name:        "bare no-slash form is rewritten",
			in:          ".claude\n",
			wantChanged: true,
			wantOut:     ".claude/*\n",
		},
		{
			name:        "leading-slash bare form is rewritten",
			in:          "/.claude\n",
			wantChanged: true,
			wantOut:     ".claude/*\n",
		},
		{
			name:        "already-correct wildcard form is untouched",
			in:          ".claude/*\n!.claude/commands/\n",
			wantChanged: false,
			wantOut:     ".claude/*\n!.claude/commands/\n",
		},
		{
			name:        "no claude line at all is untouched",
			in:          ".runtime/\n.opencode/\n",
			wantChanged: false,
			wantOut:     ".runtime/\n.opencode/\n",
		},
		{
			name:        "bare line dropped when wildcard already present (preserve wildcard position)",
			in:          ".claude/*\n!.claude/commands/\n.claude/\n",
			wantChanged: true,
			wantOut:     ".claude/*\n!.claude/commands/\n",
		},
		{
			name:        "negations are never touched",
			in:          ".claude/\n!.claude/commands/\n!.claude/skills/\n",
			wantChanged: true,
			wantOut:     ".claude/*\n!.claude/commands/\n!.claude/skills/\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := MigrateClaudeIgnorePattern(tt.in)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if got != tt.wantOut {
				t.Errorf("output mismatch:\ngot:  %q\nwant: %q", got, tt.wantOut)
			}
		})
	}
}

// TestGasTownLocalExcludePatterns_IncludesBeads verifies that the local exclude
// patterns include .beads/ (defense-in-depth for gas-7vg) while the gitignore
// patterns do NOT include .beads/ (regression guard).
func TestGasTownLocalExcludePatterns_IncludesBeads(t *testing.T) {
	localPatterns := gasTownLocalExcludePatterns()
	found := false
	for _, p := range localPatterns {
		if p == ".beads/" {
			found = true
			break
		}
	}
	if !found {
		t.Error("gasTownLocalExcludePatterns() must include .beads/ (gas-7vg defense-in-depth)")
	}

	// Regression guard: .gitignore patterns must NOT include .beads/
	gitignorePatterns := gasTownIgnorePatterns()
	for _, p := range gitignorePatterns {
		if p == ".beads/" {
			t.Error("gasTownIgnorePatterns() must NOT include .beads/ - that breaks bd sync (see overlay.go)")
		}
	}
}

// Helper functions

func containsLine(content, pattern string) bool {
	for _, line := range splitLines(content) {
		if line == pattern {
			return true
		}
	}
	return false
}

func countOccurrences(content, pattern string) int {
	count := 0
	for _, line := range splitLines(content) {
		if line == pattern {
			count++
		}
	}
	return count
}

func splitLines(content string) []string {
	var lines []string
	start := 0
	for i, c := range content {
		if c == '\n' {
			lines = append(lines, content[start:i])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}

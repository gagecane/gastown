package rig

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/style"
)

func gasTownIgnorePatterns() []string {
	return []string{
		".runtime/",
		// Use ".claude/*" (not ".claude/") so git still descends into the
		// directory and the negations below can re-include tracked nested
		// files. A bare ".claude/" excludes the directory itself, which makes
		// any "!.claude/..." negation dead — git never looks inside an
		// excluded directory. The negations MUST follow ".claude/*" (gu-w1bge).
		".claude/*",
		"!.claude/commands/",
		"!.claude/skills/",
		".opencode/",
		".logs/",
		"__pycache__/",
		"state.json",
		"CLAUDE.md",
		"CLAUDE.local.md",
		"GEMINI.md",
	}
}

// MigrateClaudeIgnorePattern rewrites a legacy bare ".claude/" ignore line (in
// any of its variant forms: ".claude", ".claude/", "/.claude", "/.claude/") to
// the traversable ".claude/*" form, in place. It returns the (possibly)
// rewritten content and whether any change was made.
//
// In-place replacement is required, not appending: a bare ".claude/" line
// excludes the directory itself, so git never descends into it and the
// "!.claude/commands/" / "!.claude/skills/" negations are dead — even when
// ".claude/*" is also present elsewhere in the file. The bad line must be
// removed for the negations to take effect (gu-w1bge).
//
// If the file already contains ".claude/*", any bare lines are simply dropped
// (the existing wildcard line is preserved in its position). Otherwise the
// first bare line is replaced with ".claude/*" so its position — typically
// ahead of the negations — is preserved.
func MigrateClaudeIgnorePattern(content string) (string, bool) {
	lines := strings.Split(content, "\n")

	hasBare := false
	hasWildcard := false
	for _, line := range lines {
		switch normalizeClaudeIgnoreLine(line) {
		case ".claude":
			hasBare = true
		case ".claude/*":
			hasWildcard = true
		}
	}
	if !hasBare {
		return content, false
	}

	out := make([]string, 0, len(lines))
	insertedWildcard := false
	for _, line := range lines {
		if normalizeClaudeIgnoreLine(line) == ".claude" {
			// Replace the first bare line with the wildcard form (unless the
			// file already has one elsewhere); drop any further bare lines.
			if !hasWildcard && !insertedWildcard {
				out = append(out, ".claude/*")
				insertedWildcard = true
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), true
}

// normalizeClaudeIgnoreLine classifies a .gitignore line relative to the
// ".claude" patterns. It returns ".claude" for the legacy bare directory form
// (with or without leading/trailing slash), ".claude/*" for the traversable
// form, or "" for anything else (including the negations, which must be
// preserved verbatim).
func normalizeClaudeIgnoreLine(line string) string {
	norm := strings.TrimPrefix(strings.TrimSpace(line), "/")
	switch norm {
	case ".claude", ".claude/":
		return ".claude"
	case ".claude/*":
		return ".claude/*"
	default:
		return ""
	}
}

// CopyOverlay copies files from <rigPath>/.runtime/overlay/ to the destination path.
// This allows storing gitignored files (like .env) that services need at their root.
// The overlay is copied non-recursively - only files, not subdirectories.
// File permissions from the source are preserved.
//
// Structure:
//
//	rig/
//	  .runtime/
//	    overlay/
//	      .env          <- Copied to destPath
//	      config.json   <- Copied to destPath
//
// Returns nil if the overlay directory doesn't exist (nothing to copy).
// Individual file copy failures are logged as warnings but don't stop the process.
func CopyOverlay(rigPath, destPath string) error {
	overlayDir := filepath.Join(rigPath, ".runtime", "overlay")

	// Check if overlay directory exists
	entries, err := os.ReadDir(overlayDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No overlay directory - not an error, just nothing to copy
			return nil
		}
		return fmt.Errorf("reading overlay dir: %w", err)
	}

	// Copy each file (not directories) from overlay to destination
	for _, entry := range entries {
		if entry.IsDir() {
			// Skip subdirectories - only copy files at overlay root
			continue
		}

		srcPath := filepath.Join(overlayDir, entry.Name())
		dstPath := filepath.Join(destPath, entry.Name())

		if err := copyFilePreserveMode(srcPath, dstPath); err != nil {
			// Log warning but continue - don't fail spawn for overlay issues
			style.PrintWarning("could not copy overlay file %s: %v", entry.Name(), err)
			continue
		}
	}

	return nil
}

// EnsureGitignorePatterns ensures the .gitignore has required Gas Town patterns.
// This is called after cloning to add patterns that may be missing from the source repo.
func EnsureGitignorePatterns(worktreePath string) error {
	gitignorePath := filepath.Join(worktreePath, ".gitignore")

	// Required patterns for Gas Town worktrees.
	// DO NOT add ".beads/" here. Beads manages its own .beads/.gitignore
	// (created by bd init) which selectively ignores runtime files.
	// Adding .beads/ here overrides that and breaks bd sync.
	// This has regressed twice (PR #753 added it, #891 removed it,
	// #966 re-added it). See overlay_test.go for a regression guard.
	//
	// .claude is ignored via ".claude/*" + "!.claude/commands/" + "!.claude/skills/".
	// Settings are installed in gastown-managed parent directories via --settings flag,
	// but Cursor still creates .claude/ inside worktrees at runtime. A bare ".claude/"
	// pattern excludes the directory itself so git never descends into it, making the
	// commands/skills negations dead (gu-w1bge). ".claude/*" ignores the contents while
	// keeping the directory traversable so nested skill/command files can be re-included.
	requiredPatterns := gasTownIgnorePatterns()

	// Read existing gitignore content
	var existingContent string
	if data, err := os.ReadFile(gitignorePath); err == nil {
		existingContent = string(data)
	}

	// Migrate any legacy bare ".claude/" line to ".claude/*" in place. Appending
	// the new patterns is not enough — a surviving bare line keeps the negations
	// dead regardless of order (gu-w1bge).
	if migrated, changed := MigrateClaudeIgnorePattern(existingContent); changed {
		if err := os.WriteFile(gitignorePath, []byte(migrated), 0644); err != nil {
			return fmt.Errorf("migrating .claude ignore pattern in .gitignore: %w", err)
		}
		existingContent = migrated
	}

	// Find missing patterns
	var missing []string
	for _, pattern := range requiredPatterns {
		found := false
		for _, line := range strings.Split(existingContent, "\n") {
			line = strings.TrimSpace(line)
			if matchesGitignorePattern(line, pattern) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, pattern)
		}
	}

	if len(missing) == 0 {
		return nil // All patterns present
	}

	// Append missing patterns
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening .gitignore: %w", err)
	}
	defer f.Close()

	// Add header if appending to existing file
	if existingContent != "" && !strings.HasSuffix(existingContent, "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	if existingContent != "" {
		if _, err := f.WriteString("\n# Gas Town (added by gt)\n"); err != nil {
			return err
		}
	}

	for _, pattern := range missing {
		if _, err := f.WriteString(pattern + "\n"); err != nil {
			return err
		}
	}

	return nil
}

// gasTownLocalExcludePatterns returns the patterns to write to the worktree-local
// .git/info/exclude file. This is a superset of gasTownIgnorePatterns() and
// includes .beads/ — which is safe here because .git/info/exclude is per-worktree
// and never committed to the repo (unlike .gitignore, where .beads/ must NOT appear
// because Beads manages its own .beads/.gitignore via bd init).
func gasTownLocalExcludePatterns() []string {
	patterns := gasTownIgnorePatterns()
	// .beads/ is excluded from gasTownIgnorePatterns() to avoid breaking bd sync
	// (see EnsureGitignorePatterns comment). The local exclude file is safe to
	// include it — it's per-worktree and invisible to `git status` without affecting
	// the tracked .gitignore (gas-7vg defense-in-depth).
	return append(patterns, ".beads/")
}

// EnsureLocalExcludePatterns writes the standard Gas Town ignore patterns to the
// worktree-local git exclude file so the worktree stays clean without mutating a
// tracked .gitignore.
func EnsureLocalExcludePatterns(worktreePath string) error {
	excludePath, err := gitLocalExcludePath(worktreePath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return fmt.Errorf("creating local exclude dir: %w", err)
	}

	var existingContent string
	if data, err := os.ReadFile(excludePath); err == nil {
		existingContent = string(data)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading local exclude: %w", err)
	}

	// Migrate any legacy bare ".claude/" line to ".claude/*" in place so the
	// commands/skills negations are live. Existing clones written before the
	// gu-w1bge fix carry the bad line in .git/info/exclude.
	if migrated, changed := MigrateClaudeIgnorePattern(existingContent); changed {
		if err := os.WriteFile(excludePath, []byte(migrated), 0644); err != nil {
			return fmt.Errorf("migrating .claude ignore pattern in local exclude: %w", err)
		}
		existingContent = migrated
	}

	var missing []string
	for _, pattern := range gasTownLocalExcludePatterns() {
		found := false
		for _, line := range strings.Split(existingContent, "\n") {
			line = strings.TrimSpace(line)
			if matchesGitignorePattern(line, pattern) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, pattern)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening local exclude: %w", err)
	}
	defer f.Close()

	if existingContent != "" && !strings.HasSuffix(existingContent, "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	if existingContent != "" {
		if _, err := f.WriteString("\n# Gas Town (added by gt)\n"); err != nil {
			return err
		}
	}

	for _, pattern := range missing {
		if _, err := f.WriteString(pattern + "\n"); err != nil {
			return err
		}
	}

	return nil
}

func gitLocalExcludePath(worktreePath string) (string, error) {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-dir")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolving git dir: %w: %s", err, strings.TrimSpace(string(out)))
	}

	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", fmt.Errorf("empty git dir for %s", worktreePath)
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}
	return filepath.Join(gitDir, "info", "exclude"), nil
}

// matchesGitignorePattern checks if a gitignore line covers the required pattern.
// Handles variant forms (with/without trailing slash, leading slash) and recognizes
// that a broader directory pattern (e.g., ".runtime/") covers more specific paths
// (e.g., ".runtime/setup-hooks/").
//
// Negation and wildcard patterns (e.g. "!.claude/commands/", ".claude/*") are
// matched by exact/variant equality only — the directory-coverage heuristic
// below MUST NOT claim a broad directory line "covers" a negation, or the
// negation would be wrongly treated as already present and never written,
// silently re-killing the re-inclusion of nested .claude files (gu-w1bge).
func matchesGitignorePattern(line, pattern string) bool {
	// Strip leading slash for comparison
	normLine := strings.TrimPrefix(line, "/")
	normPattern := strings.TrimPrefix(pattern, "/")

	// Exact match or trailing-slash variants
	if normLine == normPattern ||
		normLine == strings.TrimSuffix(normPattern, "/") ||
		normLine+"/" == normPattern {
		return true
	}

	// Negation and wildcard patterns have no "broader covers narrower"
	// relationship — only exact/variant equality (handled above) counts.
	if strings.HasPrefix(normPattern, "!") || strings.Contains(normPattern, "*") {
		return false
	}

	// A broader directory pattern covers more specific paths underneath it.
	// e.g., ".runtime/" covers ".runtime/setup-hooks/". A negation or wildcard
	// LINE is never a broad directory cover, so exclude those forms.
	if strings.HasSuffix(normLine, "/") && !strings.HasPrefix(normLine, "!") &&
		strings.HasPrefix(normPattern, normLine) {
		return true
	}
	// Also handle directory pattern without trailing slash
	if !strings.Contains(normLine, "/") && strings.HasPrefix(normPattern, normLine+"/") {
		return true
	}

	return false
}

// copyFilePreserveMode copies a file from src to dst, preserving the source file's permissions.
func copyFilePreserveMode(src, dst string) error {
	// Get source file info for permissions
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	// Open source file
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	// Create destination file with same permissions
	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	// Copy contents
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		return fmt.Errorf("copy contents: %w", err)
	}

	// Explicitly check Close() — on many filesystems, buffered data is flushed
	// at Close() time, so a full-disk error surfaces here, not during Write.
	if err := dstFile.Close(); err != nil {
		return fmt.Errorf("closing destination: %w", err)
	}

	return nil
}

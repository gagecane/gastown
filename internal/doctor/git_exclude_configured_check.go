package doctor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GitExcludeConfiguredCheck verifies .git/info/exclude has Gas Town directories.
type GitExcludeConfiguredCheck struct {
	FixableCheck
	missingEntries []string
	excludePath    string
}

// NewGitExcludeConfiguredCheck creates a new git exclude check.
func NewGitExcludeConfiguredCheck() *GitExcludeConfiguredCheck {
	return &GitExcludeConfiguredCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "git-exclude-configured",
				CheckDescription: "Check .git/info/exclude has Gas Town directories",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// requiredExcludes returns the directories that should be excluded.
func (c *GitExcludeConfiguredCheck) requiredExcludes() []string {
	return []string{"/polecats/", "/witness/", "/refinery/", "/mayor/"}
}

// Run checks if .git/info/exclude contains required entries.
func (c *GitExcludeConfiguredCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	// Check mayor/rig/ which is the authoritative clone
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	gitDir := filepath.Join(mayorRigPath, ".git")
	info, err := os.Stat(gitDir)
	if os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No mayor/rig clone found",
			FixHint: "Run rig-is-git-repo check first",
		}
	}

	// If .git is a file (worktree), read the actual git dir
	if info.Mode().IsRegular() {
		content, err := os.ReadFile(gitDir)
		if err != nil {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: fmt.Sprintf("Cannot read .git file: %v", err),
			}
		}
		// Format: "gitdir: /path/to/actual/git/dir"
		line := strings.TrimSpace(string(content))
		if strings.HasPrefix(line, "gitdir: ") {
			gitDir = strings.TrimPrefix(line, "gitdir: ")
			// Resolve relative paths
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(rigPath, gitDir)
			}
		}
	}

	c.excludePath = filepath.Join(gitDir, "info", "exclude")

	// Read existing excludes
	existing := make(map[string]bool)
	if file, err := os.Open(c.excludePath); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				existing[line] = true
			}
		}
		_ = file.Close() //nolint:gosec // G104: best-effort close
	}

	// Check for missing entries. Accept either anchored (/refinery/) or
	// legacy un-anchored (refinery/) forms — the un-anchored form is overly
	// broad but still covers the required directory.
	c.missingEntries = nil
	for _, required := range c.requiredExcludes() {
		unanchored := strings.TrimPrefix(required, "/")
		if !existing[required] && !existing[unanchored] {
			c.missingEntries = append(c.missingEntries, required)
		}
	}

	if len(c.missingEntries) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Git exclude properly configured",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d Gas Town directories not excluded", len(c.missingEntries)),
		Details: []string{fmt.Sprintf("Missing: %s", strings.Join(c.missingEntries, ", "))},
		FixHint: "Run 'gt doctor --fix' to add missing entries",
	}
}

// Fix appends missing entries to .git/info/exclude.
func (c *GitExcludeConfiguredCheck) Fix(ctx *CheckContext) error {
	if len(c.missingEntries) == 0 {
		return nil
	}

	// Ensure info directory exists
	infoDir := filepath.Dir(c.excludePath)
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		return fmt.Errorf("failed to create info directory: %w", err)
	}

	// Append missing entries
	f, err := os.OpenFile(c.excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open exclude file: %w", err)
	}
	defer f.Close()

	// Add a header comment if file is empty or new
	info, _ := f.Stat()
	if info.Size() == 0 {
		if _, err := f.WriteString("# Gas Town directories\n"); err != nil {
			return err
		}
	} else {
		// Add newline before new entries
		if _, err := f.WriteString("\n# Gas Town directories\n"); err != nil {
			return err
		}
	}

	for _, entry := range c.missingEntries {
		if _, err := f.WriteString(entry + "\n"); err != nil {
			return err
		}
	}

	return nil
}

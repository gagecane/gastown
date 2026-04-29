package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HooksPathConfiguredCheck verifies all clones have core.hooksPath set to .githooks.
// This ensures the pre-push hook blocks pushes to invalid branches (no internal PRs).
type HooksPathConfiguredCheck struct {
	FixableCheck
	unconfiguredClones []string
}

// NewHooksPathConfiguredCheck creates a new hooks path check.
func NewHooksPathConfiguredCheck() *HooksPathConfiguredCheck {
	return &HooksPathConfiguredCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "hooks-path-configured",
				CheckDescription: "Check core.hooksPath is set for all clones",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if all clones have core.hooksPath configured.
func (c *HooksPathConfiguredCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	c.unconfiguredClones = nil

	// Check all clone locations
	clonePaths := []string{
		filepath.Join(rigPath, "mayor", "rig"),
		filepath.Join(rigPath, "refinery", "rig"),
	}

	// Add crew clones
	crewDir := filepath.Join(rigPath, "crew")
	if entries, err := os.ReadDir(crewDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				clonePaths = append(clonePaths, filepath.Join(crewDir, entry.Name()))
			}
		}
	}

	// Add polecat clones
	polecatDir := filepath.Join(rigPath, "polecats")
	if entries, err := os.ReadDir(polecatDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				clonePaths = append(clonePaths, filepath.Join(polecatDir, entry.Name()))
			}
		}
	}

	for _, clonePath := range clonePaths {
		// Skip if not a git repo
		if _, err := os.Stat(filepath.Join(clonePath, ".git")); os.IsNotExist(err) {
			continue
		}

		// Skip if no .githooks directory exists
		if _, err := os.Stat(filepath.Join(clonePath, ".githooks")); os.IsNotExist(err) {
			continue
		}

		// Check core.hooksPath
		cmd := exec.Command("git", "-C", clonePath, "config", "--get", "core.hooksPath")
		output, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(output)) != ".githooks" {
			// Get relative path for cleaner output
			relPath, _ := filepath.Rel(rigPath, clonePath)
			if relPath == "" {
				relPath = clonePath
			}
			c.unconfiguredClones = append(c.unconfiguredClones, clonePath)
		}
	}

	if len(c.unconfiguredClones) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "All clones have hooks configured",
		}
	}

	// Build details with relative paths
	var details []string
	for _, clonePath := range c.unconfiguredClones {
		relPath, _ := filepath.Rel(rigPath, clonePath)
		if relPath == "" {
			relPath = clonePath
		}
		details = append(details, relPath)
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d clone(s) missing hooks configuration", len(c.unconfiguredClones)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to configure hooks",
	}
}

// Fix configures core.hooksPath for all unconfigured clones.
func (c *HooksPathConfiguredCheck) Fix(ctx *CheckContext) error {
	for _, clonePath := range c.unconfiguredClones {
		cmd := exec.Command("git", "-C", clonePath, "config", "core.hooksPath", ".githooks")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to configure hooks for %s: %w", clonePath, err)
		}
	}
	return nil
}

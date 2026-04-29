package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// PolecatClonesValidCheck verifies each polecat directory is a valid clone.
type PolecatClonesValidCheck struct {
	BaseCheck
}

// NewPolecatClonesValidCheck creates a new polecat clones check.
func NewPolecatClonesValidCheck() *PolecatClonesValidCheck {
	return &PolecatClonesValidCheck{
		BaseCheck: BaseCheck{
			CheckName:        "polecat-clones-valid",
			CheckDescription: "Verify polecat directories are valid git clones",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run checks if each polecat directory is a valid git clone.
func (c *PolecatClonesValidCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	polecatsDir := filepath.Join(rigPath, "polecats")
	entries, err := os.ReadDir(polecatsDir)
	if os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No polecats/ directory (none deployed)",
		}
	}
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot read polecats/: %v", err),
		}
	}

	var issues []string
	var warnings []string
	validCount := 0

	// Get rig name for new structure path detection
	rigName := ctx.RigName

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		polecatName := entry.Name()

		// Determine worktree path (handle both new and old structures)
		// New structure: polecats/<name>/<rigname>/
		// Old structure: polecats/<name>/
		polecatPath := filepath.Join(polecatsDir, polecatName, rigName)
		if _, err := os.Stat(polecatPath); os.IsNotExist(err) {
			polecatPath = filepath.Join(polecatsDir, polecatName)
		}

		// Check if it's a git clone
		gitPath := filepath.Join(polecatPath, ".git")
		if _, err := os.Stat(gitPath); os.IsNotExist(err) {
			issues = append(issues, fmt.Sprintf("%s: not a git clone", polecatName))
			continue
		}

		// Verify git status works and check for uncommitted changes
		cmd := exec.Command("git", "-C", polecatPath, "status", "--porcelain")
		output, err := cmd.Output()
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: git status failed", polecatName))
			continue
		}

		if len(output) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: has uncommitted changes", polecatName))
		}

		// Check if on a polecat branch
		cmd = exec.Command("git", "-C", polecatPath, "branch", "--show-current")
		branchOutput, err := cmd.Output()
		if err == nil {
			branch := strings.TrimSpace(string(branchOutput))
			if !strings.HasPrefix(branch, constants.BranchPolecatPrefix) {
				warnings = append(warnings, fmt.Sprintf("%s: on branch '%s' (expected %s*)", polecatName, branch, constants.BranchPolecatPrefix))
			}
		}

		validCount++
	}

	if len(issues) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("%d polecat(s) invalid", len(issues)),
			Details: append(issues, warnings...),
			FixHint: "Cannot auto-fix (data loss risk)",
		}
	}

	if len(warnings) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d polecat(s) valid, %d warning(s)", validCount, len(warnings)),
			Details: warnings,
		}
	}

	if validCount == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No polecats deployed",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("%d polecat(s) valid", validCount),
	}
}

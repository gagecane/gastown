package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultBranchAllRigsCheck validates default_branch for all rigs in the workspace.
// Unlike DefaultBranchExistsCheck (which requires --rig), this runs globally.
type DefaultBranchAllRigsCheck struct {
	BaseCheck
}

// NewDefaultBranchAllRigsCheck creates a new global default branch check.
func NewDefaultBranchAllRigsCheck() *DefaultBranchAllRigsCheck {
	return &DefaultBranchAllRigsCheck{
		BaseCheck: BaseCheck{
			CheckName:        "default-branch-all-rigs",
			CheckDescription: "Verify default_branch exists on remote for all rigs",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run checks default_branch for every discovered rig.
func (c *DefaultBranchAllRigsCheck) Run(ctx *CheckContext) *CheckResult {
	entries, err := os.ReadDir(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot read town root: %v", err),
		}
	}

	var errors []string
	rigsChecked := 0

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "mayor" || entry.Name() == "docs" || entry.Name() == "scripts" {
			continue
		}

		rigPath := filepath.Join(ctx.TownRoot, entry.Name())
		configPath := filepath.Join(rigPath, "config.json")

		data, err := os.ReadFile(configPath)
		if err != nil {
			continue // No config.json = not a rig or no default_branch configured
		}

		type rigConfig struct {
			DefaultBranch string `json:"default_branch"`
		}
		var cfg rigConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}

		if cfg.DefaultBranch == "" {
			continue // Using default "main", skip
		}

		rigsChecked++

		// Check bare repo for the ref
		bareRepoPath := filepath.Join(rigPath, ".repo.git")
		if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
			continue // No bare repo, skip
		}

		ref := fmt.Sprintf("refs/remotes/origin/%s", cfg.DefaultBranch)
		cmd := exec.Command("git", "-C", bareRepoPath, "rev-parse", "--verify", ref)
		if err := cmd.Run(); err != nil {
			errors = append(errors, fmt.Sprintf("%s: default_branch %q not found on remote", entry.Name(), cfg.DefaultBranch))
		}
	}

	if len(errors) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("%d rig(s) with invalid default_branch", len(errors)),
			Details: errors,
			FixHint: "Fix the branch name in <rig>/config.json, or create the branch on the remote",
		}
	}

	if rigsChecked == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs with custom default_branch configured",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("All %d rig(s) with custom default_branch validated", rigsChecked),
	}
}

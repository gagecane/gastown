package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// DefaultBranchExistsCheck verifies that the configured default_branch exists
// as a remote tracking ref in the bare repo.
type DefaultBranchExistsCheck struct {
	BaseCheck
}

// NewDefaultBranchExistsCheck creates a new default branch exists check.
func NewDefaultBranchExistsCheck() *DefaultBranchExistsCheck {
	return &DefaultBranchExistsCheck{
		BaseCheck: BaseCheck{
			CheckName:        "default-branch-exists",
			CheckDescription: "Verify configured default_branch exists on remote",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run checks if the configured default_branch exists as origin/<branch> in the bare repo.
func (c *DefaultBranchExistsCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	// Load rig config to get default_branch
	configPath := filepath.Join(rigPath, "config.json")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No config.json found",
		}
	}
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot read config.json: %v", err),
		}
	}

	// Parse just the default_branch field
	type rigConfig struct {
		DefaultBranch string `json:"default_branch"`
	}
	var cfg rigConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot parse config.json: %v", err),
		}
	}

	if cfg.DefaultBranch == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No default_branch configured (will use 'main')",
		}
	}

	// Check bare repo for the ref
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No shared bare repo (skipping ref check)",
		}
	}

	ref := fmt.Sprintf("refs/remotes/origin/%s", cfg.DefaultBranch)
	cmd := exec.Command("git", "-C", bareRepoPath, "rev-parse", "--verify", ref)
	if err := cmd.Run(); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("default_branch %q not found on remote", cfg.DefaultBranch),
			Details: []string{
				fmt.Sprintf("Ref %s does not exist in bare repo", ref),
				"Polecat spawn will fail with a cryptic git error",
			},
			FixHint: fmt.Sprintf("Fix the branch name in %s/config.json, or create the branch on the remote", rigPath),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("default_branch %q exists on remote", cfg.DefaultBranch),
	}
}

package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// RigIsGitRepoCheck verifies the rig has a valid mayor/rig git clone.
// Note: The rig directory itself is not a git repo - it contains clones.
type RigIsGitRepoCheck struct {
	BaseCheck
}

// NewRigIsGitRepoCheck creates a new rig git repo check.
func NewRigIsGitRepoCheck() *RigIsGitRepoCheck {
	return &RigIsGitRepoCheck{
		BaseCheck: BaseCheck{
			CheckName:        "rig-is-git-repo",
			CheckDescription: "Verify rig has a valid mayor/rig git clone",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run checks if the rig has a valid mayor/rig git clone.
func (c *RigIsGitRepoCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	// Check mayor/rig/ which is the authoritative clone for the rig
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	gitPath := filepath.Join(mayorRigPath, ".git")
	info, err := os.Stat(gitPath)
	if os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No mayor/rig clone found",
			Details: []string{fmt.Sprintf("Missing: %s", gitPath)},
			FixHint: "Clone the repository to mayor/rig/",
		}
	}
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot access mayor/rig/.git: %v", err),
		}
	}

	// Verify git status works
	cmd := exec.Command("git", "-C", mayorRigPath, "status", "--porcelain")
	if err := cmd.Run(); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "git status failed on mayor/rig",
			Details: []string{fmt.Sprintf("Error: %v", err)},
			FixHint: "Check git configuration and repository integrity",
		}
	}

	gitType := "clone"
	if info.Mode().IsRegular() {
		gitType = "worktree"
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("Valid mayor/rig %s", gitType),
	}
}

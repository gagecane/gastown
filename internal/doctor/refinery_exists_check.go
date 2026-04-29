package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/git"
)

// RefineryExistsCheck verifies the refinery directory structure exists.
type RefineryExistsCheck struct {
	FixableCheck
	rigPath     string
	needsCreate bool
	needsClone  bool
	needsMail   bool
}

// NewRefineryExistsCheck creates a new refinery exists check.
func NewRefineryExistsCheck() *RefineryExistsCheck {
	return &RefineryExistsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "refinery-exists",
				CheckDescription: "Verify refinery/ directory structure exists",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if the refinery directory structure exists.
func (c *RefineryExistsCheck) Run(ctx *CheckContext) *CheckResult {
	c.rigPath = ctx.RigPath()
	if c.rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	refineryDir := filepath.Join(c.rigPath, "refinery")
	rigClone := filepath.Join(refineryDir, "rig")
	mailInbox := filepath.Join(refineryDir, "mail", "inbox.jsonl")

	var issues []string
	c.needsCreate = false
	c.needsClone = false
	c.needsMail = false

	// Check refinery/ directory
	if _, err := os.Stat(refineryDir); os.IsNotExist(err) {
		issues = append(issues, "Missing: refinery/")
		c.needsCreate = true
	} else {
		// Check refinery/rig/ clone
		rigGit := filepath.Join(rigClone, ".git")
		if _, err := os.Stat(rigGit); os.IsNotExist(err) {
			issues = append(issues, "Missing: refinery/rig/ (git clone)")
			c.needsClone = true
		}

		// Check refinery/mail/inbox.jsonl
		if _, err := os.Stat(mailInbox); os.IsNotExist(err) {
			issues = append(issues, "Missing: refinery/mail/inbox.jsonl")
			c.needsMail = true
		}
	}

	if len(issues) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Refinery structure exists",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: "Refinery structure incomplete",
		Details: issues,
		FixHint: "Run 'gt doctor --fix' to create missing structure",
	}
}

// Fix creates missing refinery structure.
func (c *RefineryExistsCheck) Fix(ctx *CheckContext) error {
	refineryDir := filepath.Join(c.rigPath, "refinery")

	if c.needsCreate {
		if err := os.MkdirAll(refineryDir, 0755); err != nil {
			return fmt.Errorf("failed to create refinery/: %w", err)
		}
	}

	if c.needsMail {
		mailDir := filepath.Join(refineryDir, "mail")
		if err := os.MkdirAll(mailDir, 0755); err != nil {
			return fmt.Errorf("failed to create refinery/mail/: %w", err)
		}
		inboxPath := filepath.Join(mailDir, "inbox.jsonl")
		if err := os.WriteFile(inboxPath, []byte{}, 0644); err != nil {
			return fmt.Errorf("failed to create inbox.jsonl: %w", err)
		}
	}

	// Auto-repair refinery worktree from shared bare repo (.repo.git).
	// The refinery/rig is a worktree (not a full clone), so we don't need
	// the repo URL -- we just create a worktree from the local bare repo.
	if c.needsClone {
		bareRepoPath := filepath.Join(c.rigPath, ".repo.git")
		if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
			return fmt.Errorf("cannot auto-create refinery/rig/ worktree: bare repo not found at %s", bareRepoPath)
		}

		bareGit := git.NewGitWithDir(bareRepoPath, "")
		_ = bareGit.WorktreePrune()

		rigClone := filepath.Join(refineryDir, "rig")
		// Detect default branch from rig config
		rigCfgPath := filepath.Join(c.rigPath, "settings", "rig.json")
		defaultBranch := "main"
		if data, err := os.ReadFile(rigCfgPath); err == nil {
			var cfg struct {
				DefaultBranch string `json:"default_branch"`
			}
			if json.Unmarshal(data, &cfg) == nil && cfg.DefaultBranch != "" {
				defaultBranch = cfg.DefaultBranch
			}
		}

		if err := bareGit.WorktreeAddExisting(rigClone, defaultBranch); err != nil {
			return fmt.Errorf("creating refinery worktree from bare repo: %w", err)
		}

		// Configure hooks path
		refineryGit := git.NewGit(rigClone)
		_ = refineryGit.ConfigureHooksPath()
	}

	return nil
}

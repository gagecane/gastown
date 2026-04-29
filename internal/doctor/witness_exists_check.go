package doctor

import (
	"fmt"
	"os"
	"path/filepath"
)

// WitnessExistsCheck verifies the witness directory structure exists.
type WitnessExistsCheck struct {
	FixableCheck
	rigPath     string
	needsCreate bool
	needsClone  bool
	needsMail   bool
}

// NewWitnessExistsCheck creates a new witness exists check.
func NewWitnessExistsCheck() *WitnessExistsCheck {
	return &WitnessExistsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "witness-exists",
				CheckDescription: "Verify witness/ directory structure exists",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if the witness directory structure exists.
func (c *WitnessExistsCheck) Run(ctx *CheckContext) *CheckResult {
	c.rigPath = ctx.RigPath()
	if c.rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	witnessDir := filepath.Join(c.rigPath, "witness")
	rigClone := filepath.Join(witnessDir, "rig")
	mailInbox := filepath.Join(witnessDir, "mail", "inbox.jsonl")

	var issues []string
	c.needsCreate = false
	c.needsClone = false
	c.needsMail = false

	// Check witness/ directory
	if _, err := os.Stat(witnessDir); os.IsNotExist(err) {
		issues = append(issues, "Missing: witness/")
		c.needsCreate = true
	} else {
		// Check witness/rig/ clone
		rigGit := filepath.Join(rigClone, ".git")
		if _, err := os.Stat(rigGit); os.IsNotExist(err) {
			issues = append(issues, "Missing: witness/rig/ (git clone)")
			c.needsClone = true
		}

		// Check witness/mail/inbox.jsonl
		if _, err := os.Stat(mailInbox); os.IsNotExist(err) {
			issues = append(issues, "Missing: witness/mail/inbox.jsonl")
			c.needsMail = true
		}
	}

	if len(issues) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Witness structure exists",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: "Witness structure incomplete",
		Details: issues,
		FixHint: "Run 'gt doctor --fix' to create missing structure",
	}
}

// Fix creates missing witness structure.
func (c *WitnessExistsCheck) Fix(ctx *CheckContext) error {
	witnessDir := filepath.Join(c.rigPath, "witness")

	if c.needsCreate {
		if err := os.MkdirAll(witnessDir, 0755); err != nil {
			return fmt.Errorf("failed to create witness/: %w", err)
		}
	}

	if c.needsMail {
		mailDir := filepath.Join(witnessDir, "mail")
		if err := os.MkdirAll(mailDir, 0755); err != nil {
			return fmt.Errorf("failed to create witness/mail/: %w", err)
		}
		inboxPath := filepath.Join(mailDir, "inbox.jsonl")
		if err := os.WriteFile(inboxPath, []byte{}, 0644); err != nil {
			return fmt.Errorf("failed to create inbox.jsonl: %w", err)
		}
	}

	// Note: Cannot auto-fix clone without knowing the repo URL
	if c.needsClone {
		return fmt.Errorf("cannot auto-create witness/rig/ clone (requires repo URL)")
	}

	return nil
}

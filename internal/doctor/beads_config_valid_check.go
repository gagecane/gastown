package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// BeadsConfigValidCheck verifies beads configuration if .beads/ exists.
type BeadsConfigValidCheck struct {
	FixableCheck
	rigPath   string
	needsSync bool
}

// NewBeadsConfigValidCheck creates a new beads config check.
func NewBeadsConfigValidCheck() *BeadsConfigValidCheck {
	return &BeadsConfigValidCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "beads-config-valid",
				CheckDescription: "Verify beads configuration if .beads/ exists",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if beads is properly configured.
func (c *BeadsConfigValidCheck) Run(ctx *CheckContext) *CheckResult {
	c.rigPath = ctx.RigPath()
	if c.rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	beadsDir := filepath.Join(c.rigPath, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No .beads/ directory (beads not configured)",
		}
	}

	// Check if bd command works
	cmd := exec.Command("bd", "stats", "--json")
	cmd.Dir = c.rigPath
	if err := cmd.Run(); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "bd command failed",
			Details: []string{fmt.Sprintf("Error: %v", err)},
			FixHint: "Check beads installation and .beads/ configuration",
		}
	}

	// Note: With Dolt backend, there's no sync status to check.
	// Beads changes are persisted immediately.

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "Beads configured and accessible",
	}
}

// Fix is a no-op with Dolt backend (no sync needed).
func (c *BeadsConfigValidCheck) Fix(ctx *CheckContext) error {
	// With Dolt backend, beads changes are persisted immediately - no sync needed
	return nil
}

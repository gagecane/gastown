package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
)

// BeadsRedirectCheck verifies that rig-level beads redirect exists for tracked beads.
// When a repo has .beads/ tracked in git (at mayor/rig/.beads), the rig root needs
// a redirect file pointing to that location.
type BeadsRedirectCheck struct {
	FixableCheck
}

// NewBeadsRedirectCheck creates a new beads redirect check.
func NewBeadsRedirectCheck() *BeadsRedirectCheck {
	return &BeadsRedirectCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "beads-redirect",
				CheckDescription: "Verify rig-level beads redirect for tracked beads",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if the rig-level beads redirect exists when needed.
func (c *BeadsRedirectCheck) Run(ctx *CheckContext) *CheckResult {
	// Only applies when checking a specific rig
	if ctx.RigName == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rig specified (skipping redirect check)",
		}
	}

	rigPath := ctx.RigPath()
	mayorRigBeads := filepath.Join(rigPath, "mayor", "rig", ".beads")
	rigBeadsDir := filepath.Join(rigPath, ".beads")
	redirectPath := filepath.Join(rigBeadsDir, "redirect")

	// Check if this rig has tracked beads (mayor/rig/.beads exists)
	if _, err := os.Stat(mayorRigBeads); os.IsNotExist(err) {
		// No tracked beads - check if rig/.beads exists (local beads)
		if _, err := os.Stat(rigBeadsDir); os.IsNotExist(err) {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: "No .beads directory found at rig root",
				Details: []string{
					"Beads database not initialized for this rig",
					"This prevents issue tracking for this rig",
				},
				FixHint: "Run 'gt doctor --fix --rig " + ctx.RigName + "' to initialize beads",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Rig uses local beads (no redirect needed)",
		}
	}

	// Tracked beads exist - check for conflicting local beads
	hasLocalData := hasBeadsData(rigBeadsDir)
	redirectExists := false
	if _, err := os.Stat(redirectPath); err == nil {
		redirectExists = true
	}

	// Case: Local beads directory has actual data (not just redirect)
	if hasLocalData && !redirectExists {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Conflicting local beads found with tracked beads",
			Details: []string{
				"Tracked beads exist at: mayor/rig/.beads",
				"Local beads with data exist at: .beads/",
				"Fix will remove local beads and create redirect to tracked beads",
			},
			FixHint: "Run 'gt doctor --fix --rig " + ctx.RigName + "' to fix",
		}
	}

	// Case: No redirect file (but no conflicting data)
	if !redirectExists {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Missing rig-level beads redirect for tracked beads",
			Details: []string{
				"Tracked beads exist at: mayor/rig/.beads",
				"Missing redirect at: .beads/redirect",
				"Without this redirect, bd commands from rig root won't find beads",
			},
			FixHint: "Run 'gt doctor --fix' to create the redirect",
		}
	}

	// Verify redirect points to correct location
	content, err := os.ReadFile(redirectPath)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not read redirect file: %v", err),
		}
	}

	target := strings.TrimSpace(string(content))
	if target != "mayor/rig/.beads" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Redirect points to %q, expected mayor/rig/.beads", target),
			FixHint: "Run 'gt doctor --fix --rig " + ctx.RigName + "' to correct the redirect",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "Rig-level beads redirect is correctly configured",
	}
}

// Fix creates or corrects the rig-level beads redirect, or initializes beads if missing.
func (c *BeadsRedirectCheck) Fix(ctx *CheckContext) error {
	if ctx.RigName == "" {
		return nil
	}

	rigPath := ctx.RigPath()
	mayorRigBeads := filepath.Join(rigPath, "mayor", "rig", ".beads")
	rigBeadsDir := filepath.Join(rigPath, ".beads")
	redirectPath := filepath.Join(rigBeadsDir, "redirect")

	// Check if tracked beads exist
	hasTrackedBeads := true
	if _, err := os.Stat(mayorRigBeads); os.IsNotExist(err) {
		hasTrackedBeads = false
	}

	// Check if local beads exist
	hasLocalBeads := true
	if _, err := os.Stat(rigBeadsDir); os.IsNotExist(err) {
		hasLocalBeads = false
	}

	// Case 1: No beads at all - initialize with bd init
	if !hasTrackedBeads && !hasLocalBeads {
		// Get the rig's beads prefix from rigs.json (falls back to "gt" if not found)
		prefix := config.GetRigPrefix(ctx.TownRoot, ctx.RigName)

		// Create .beads directory
		if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
			return fmt.Errorf("creating .beads directory: %w", err)
		}

		// Run bd init with the configured prefix (Dolt is the only backend since bd v0.51.0).
		// Gas Town rigs use Dolt server mode via the shared town Dolt sql-server.
		initArgs := []string{"init"}
		if prefix != "" {
			initArgs = append(initArgs, "--prefix", prefix)
		}
		initArgs = append(initArgs, "--server")
		cmd := exec.Command("bd", initArgs...)
		cmd.Dir = rigPath
		if output, err := cmd.CombinedOutput(); err != nil {
			// bd might not be installed — create config.yaml via shared helper.
			if writeErr := beads.EnsureConfigYAML(rigBeadsDir, prefix); writeErr != nil {
				return fmt.Errorf("bd init failed (%v) and fallback config creation failed: %w", err, writeErr)
			}
			// Continue - minimal config created
		} else {
			_ = output // bd init succeeded
			// Configure custom types for Gas Town (beads v0.46.0+)
			configCmd := exec.Command("bd", "config", "set", "types.custom", constants.BeadsCustomTypes)
			configCmd.Dir = rigPath
			_, _ = configCmd.CombinedOutput() // Ignore errors - older beads don't need this
		}
		return nil
	}

	// Case 2: Tracked beads exist - create redirect (may need to remove conflicting local beads)
	if hasTrackedBeads {
		// Check if local beads have conflicting data
		if hasLocalBeads && hasBeadsData(rigBeadsDir) {
			// Remove conflicting local beads directory
			if err := os.RemoveAll(rigBeadsDir); err != nil {
				return fmt.Errorf("removing conflicting local beads: %w", err)
			}
		}

		// Create .beads directory if needed
		if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
			return fmt.Errorf("creating .beads directory: %w", err)
		}

		// Write redirect file
		if err := os.WriteFile(redirectPath, []byte("mayor/rig/.beads\n"), 0644); err != nil {
			return fmt.Errorf("writing redirect file: %w", err)
		}
	}

	return nil
}

// hasBeadsData checks if a beads directory has actual data (issues.db, config.yaml)
// as opposed to just being a redirect-only directory.
func hasBeadsData(beadsDir string) bool {
	// Check for actual beads data files (Dolt-only — issues.jsonl is no longer supported)
	dataFiles := []string{"issues.db", "config.yaml"}
	for _, f := range dataFiles {
		if _, err := os.Stat(filepath.Join(beadsDir, f)); err == nil {
			return true
		}
	}
	return false
}

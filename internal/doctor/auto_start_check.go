package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// AutoStartCheck verifies that all rigs have dolt.auto-start set to "false"
// to prevent bd from spawning a rig-local embedded Dolt server that hijacks
// the shared centralized port (3307) and serves a single rig's database to
// every other rig — a town-wide "database not found" outage. Gas Town owns
// the centralized Dolt server lifecycle; bd auto-start must stay off. See
// gu-hvw2a.
type AutoStartCheck struct {
	FixableCheck
}

// NewAutoStartCheck creates a new auto-start check.
func NewAutoStartCheck() *AutoStartCheck {
	return &AutoStartCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "dolt-auto-start-config",
				CheckDescription: "Verify all rigs have dolt.auto-start set to \"false\" (centralized Dolt)",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// autoStartRigSet builds the unique rig list from town routes.
func autoStartRigSet(townRoot string) (map[string]string, error) {
	townBeadsDir := filepath.Join(townRoot, ".beads")
	routes, err := beads.LoadRoutes(townBeadsDir)
	if err != nil {
		return nil, err
	}

	rigSet := make(map[string]string) // rigName -> beadsPath
	for _, r := range routes {
		parts := strings.Split(r.Path, "/")
		if len(parts) >= 1 && parts[0] != "." {
			rigName := parts[0]
			if _, exists := rigSet[rigName]; !exists {
				rigSet[rigName] = r.Path
			}
		}
	}
	return rigSet, nil
}

// configDisablesAutoStart reports whether config.yaml content explicitly
// sets dolt.auto-start to "false" (ignoring comments and quoting style).
func configDisablesAutoStart(content string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "dolt.auto-start:") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "dolt.auto-start:"))
			value = strings.Trim(value, `"'`)
			return strings.EqualFold(value, "false")
		}
	}
	return false
}

// Run checks if all rigs have dolt.auto-start set to "false".
func (c *AutoStartCheck) Run(ctx *CheckContext) *CheckResult {
	rigSet, err := autoStartRigSet(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not load routes.jsonl",
		}
	}

	if len(rigSet) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs to check",
		}
	}

	var missing []string
	var checked int

	for rigName, beadsPath := range rigSet {
		configPath := filepath.Join(ctx.TownRoot, beadsPath, ".beads", "config.yaml")
		data, err := os.ReadFile(configPath)
		if err != nil {
			// Config file missing - will be created by EnsureConfigYAML
			missing = append(missing, fmt.Sprintf("%s (config.yaml missing)", rigName))
			checked++
			continue
		}
		if !configDisablesAutoStart(string(data)) {
			missing = append(missing, rigName)
		}
		checked++
	}

	if len(missing) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("All %d rigs have dolt.auto-start set to \"false\"", checked),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d rig(s) missing dolt.auto-start: \"false\"", len(missing)),
		Details: missing,
		FixHint: "Run 'gt doctor --fix' to disable bd Dolt auto-start in all rigs",
	}
}

// Fix sets dolt.auto-start: "false" in all rig config.yaml files.
func (c *AutoStartCheck) Fix(ctx *CheckContext) error {
	rigSet, err := autoStartRigSet(ctx.TownRoot)
	if err != nil {
		return fmt.Errorf("loading routes.jsonl: %w", err)
	}

	for rigName, beadsPath := range rigSet {
		rigBeadsPath := filepath.Join(ctx.TownRoot, beadsPath, ".beads")
		// Derive the rig's prefix from metadata so EnsureConfigYAML does not
		// blank the existing prefix line (it rewrites "prefix:" to the value
		// passed in). EnsureConfigYAML then forces dolt.auto-start: "false"
		// idempotently.
		prefix := beads.ConfigDefaultsFromMetadata(rigBeadsPath, "")
		if err := beads.EnsureConfigYAML(rigBeadsPath, prefix); err != nil {
			return fmt.Errorf("fixing %s: %w", rigName, err)
		}
	}

	return nil
}

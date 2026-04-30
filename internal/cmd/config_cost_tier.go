package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Cost-tier subcommand

var configCostTierCmd = &cobra.Command{
	Use:   "cost-tier [tier]",
	Short: "Get or set cost optimization tier",
	Long: `Get or set the cost optimization tier for model selection.

With no arguments, shows the current cost tier and role assignments.
With an argument, applies the specified tier preset.

Tiers control which AI model each role uses:
  standard  All roles use Opus (highest quality, default)
  economy   Patrol roles use Sonnet/Haiku, workers use Opus
  budget    Patrol roles use Haiku, workers use Sonnet

Examples:
  gt config cost-tier              # Show current tier
  gt config cost-tier economy      # Switch to economy tier
  gt config cost-tier standard     # Reset to all-Opus`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigCostTier,
}

func runConfigCostTier(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	settingsPath := config.TownSettingsPath(townRoot)
	townSettings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("loading town settings: %w", err)
	}

	if len(args) == 0 {
		// Show current tier and role assignments
		current := config.GetCurrentTier(townSettings)
		if current == "" {
			fmt.Println("Cost tier: " + style.Bold.Render("custom") + " (manual role_agents configuration)")
		} else {
			tier := config.CostTier(current)
			fmt.Printf("Cost tier: %s\n", style.Bold.Render(current))
			fmt.Printf("  %s\n\n", config.TierDescription(tier))
			fmt.Println("Role assignments:")
			fmt.Println(config.FormatTierRoleTable(tier))
		}
		return nil
	}

	// Apply tier
	tierName := args[0]
	if !config.IsValidTier(tierName) {
		return fmt.Errorf("invalid cost tier %q (valid: %s)", tierName, strings.Join(config.ValidCostTiers(), ", "))
	}

	tier := config.CostTier(tierName)

	// Warn if overwriting custom role_agents
	currentTier := config.GetCurrentTier(townSettings)
	if currentTier == "" && len(townSettings.RoleAgents) > 0 {
		fmt.Println("Warning: overwriting custom role_agents configuration")
	}

	if err := config.ApplyCostTier(townSettings, tier); err != nil {
		return fmt.Errorf("applying cost tier: %w", err)
	}

	if err := config.SaveTownSettings(settingsPath, townSettings); err != nil {
		return fmt.Errorf("saving town settings: %w", err)
	}

	fmt.Printf("Cost tier set to %s\n", style.Bold.Render(tierName))
	fmt.Printf("  %s\n\n", config.TierDescription(tier))
	fmt.Println("Role assignments:")
	fmt.Println(config.FormatTierRoleTable(tier))
	return nil
}

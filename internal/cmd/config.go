// Package cmd provides CLI commands for the gt tool.
//
// Configuration commands are organized across multiple files:
//   - config.go              — root configCmd, init() wiring
//   - config_agent.go        — agent (list/get/set/remove) + agent-email-domain
//   - config_cost_tier.go    — cost-tier subcommand
//   - config_default_agent.go— default-agent subcommand
//   - config_set_get.go      — dot-notation set/get + parseBool helper
//   - config_maintenance.go  — maintenance.* helpers (daemon.json)
//   - config_lifecycle.go    — lifecycle.* helpers (daemon.json)
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
)

var configCmd = &cobra.Command{
	Use:     "config",
	GroupID: GroupConfig,
	Short:   "Manage Gas Town configuration",
	RunE:    requireSubcommand,
	Long: `Manage Gas Town configuration settings.

This command allows you to view and modify configuration settings
for your Gas Town workspace, including agent aliases and defaults.

Commands:
  gt config agent list              List all agents (built-in and custom)
  gt config agent get <name>         Show agent configuration
  gt config agent set <name> <cmd>   Set custom agent command
  gt config agent remove <name>      Remove custom agent
  gt config default-agent [name]     Get or set default agent
  gt config default-agent list       List available agents`,
}

func init() {
	presets := config.BuiltInAgentPresetSummary()

	configAgentListCmd.Long = fmt.Sprintf(`List all available agents (built-in and custom).

Shows all built-in agent presets (%s) and any
custom agents defined in your town settings.

Examples:
  gt config agent list           # Text output
  gt config agent list --json    # JSON output`, presets)

	configAgentRemoveCmd.Long = fmt.Sprintf(`Remove a custom agent definition from town settings.

This removes a custom agent from your town settings. Built-in agents
(%s) cannot be removed.

Examples:
  gt config agent remove claude-glm`, presets)

	configDefaultAgentCmd.Long = fmt.Sprintf(`Get or set the default agent for the town.

With no arguments, shows the current default agent.
With an argument, sets the default agent to the specified name.

The default agent is used when a rig doesn't specify its own agent
setting. Can be a built-in preset (%s) or a
custom agent name.

Use 'gt config default-agent list' to see all available agents.

Examples:
  gt config default-agent           # Show current default
  gt config default-agent list      # List available agents
  gt config default-agent claude    # Set to claude
  gt config default-agent gemini    # Set to gemini
  gt config default-agent my-custom # Set to custom agent`, presets)

	// Add flags
	configAgentListCmd.Flags().BoolVar(&configAgentListJSON, "json", false, "Output as JSON")
	configDefaultAgentListCmd.Flags().BoolVar(&configDefaultAgentListJSON, "json", false, "Output as JSON")
	configAgentSetCmd.Flags().StringVar(&configAgentSetProvider, "provider", "", fmt.Sprintf("Agent provider preset (e.g. %s); inferred from command name if not set", presets))

	// Add agent subcommands
	configAgentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage agent configuration",
		Long: `Manage per-agent configuration settings.

Subcommands allow listing, getting, setting, and removing agent-specific
config values such as the default AI model or provider.`,
		RunE: requireSubcommand,
	}
	configAgentCmd.AddCommand(configAgentListCmd)
	configAgentCmd.AddCommand(configAgentGetCmd)
	configAgentCmd.AddCommand(configAgentSetCmd)
	configAgentCmd.AddCommand(configAgentRemoveCmd)

	// Add default-agent subcommands
	configDefaultAgentCmd.AddCommand(configDefaultAgentListCmd)

	// Add subcommands to config
	configCmd.AddCommand(configAgentCmd)
	configCmd.AddCommand(configCostTierCmd)
	configCmd.AddCommand(configDefaultAgentCmd)
	configCmd.AddCommand(configAgentEmailDomainCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configGetCmd)

	// Register with root
	rootCmd.AddCommand(configCmd)
}

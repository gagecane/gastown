// Town and town-settings configuration types.
//
// This file contains the top-level town identity (TownConfig), mayor
// behavioral settings (MayorConfig), and town-wide agent/behavior settings
// (TownSettings). It was extracted from types.go as part of the config
// refactor to split domain-specific types into focused files.
package config

import (
	"time"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// TownConfig represents the main town identity (mayor/town.json).
type TownConfig struct {
	Type       string    `json:"type"`                  // "town"
	Version    int       `json:"version"`               // schema version
	Name       string    `json:"name"`                  // town identifier (internal)
	Owner      string    `json:"owner,omitempty"`       // owner email (entity identity)
	PublicName string    `json:"public_name,omitempty"` // public display name
	CreatedAt  time.Time `json:"created_at"`
}

// MayorConfig represents town-level behavioral configuration (mayor/config.json).
// This is separate from TownConfig (identity) to keep configuration concerns distinct.
type MayorConfig struct {
	Type            string           `json:"type"`                        // "mayor-config"
	Version         int              `json:"version"`                     // schema version
	Theme           *TownThemeConfig `json:"theme,omitempty"`             // global theme settings
	Daemon          *DaemonConfig    `json:"daemon,omitempty"`            // daemon settings
	Deacon          *DeaconConfig    `json:"deacon,omitempty"`            // deacon settings
	DefaultCrewName string           `json:"default_crew_name,omitempty"` // default crew name for new rigs
}

// CurrentTownSettingsVersion is the current schema version for TownSettings.
const CurrentTownSettingsVersion = 1

// TownSettings represents town-level behavioral configuration (settings/config.json).
// This contains agent configuration that applies to all rigs unless overridden.
type TownSettings struct {
	Type    string `json:"type"`    // "town-settings"
	Version int    `json:"version"` // schema version

	// CLITheme controls CLI output color scheme.
	// Values: "dark", "light", "auto" (default).
	// "auto" lets the terminal emulator's background color guide the choice.
	// Can be overridden by GT_THEME environment variable.
	CLITheme string `json:"cli_theme,omitempty"`

	// DefaultAgent is the name of the agent preset to use by default.
	// Can be a built-in preset ("claude", "gemini", "codex", "cursor", "auggie", "amp", "opencode", "copilot")
	// or a custom agent name defined in settings/agents.json.
	// Default: "claude"
	DefaultAgent string `json:"default_agent,omitempty"`

	// Agents defines custom agent configurations or overrides.
	// Keys are agent names that can be referenced by DefaultAgent or rig settings.
	// Values override or extend the built-in presets.
	// Example: {"gemini": {"command": "/custom/path/to/gemini"}}
	Agents map[string]*RuntimeConfig `json:"agents,omitempty"`

	// RoleAgents maps role names to agent aliases for per-role model selection.
	// Keys are role names: "mayor", "deacon", "witness", "refinery", "polecat", "crew".
	// Values are agent names (built-in presets or custom agents defined in Agents).
	// This allows cost optimization by using different models for different roles.
	// Example: {"mayor": "claude-opus", "witness": "claude-haiku", "polecat": "claude-sonnet"}
	RoleAgents map[string]string `json:"role_agents,omitempty"`

	// CrewAgents maps individual crew worker names to agent aliases at the town level.
	// This allows town-wide per-crew agent assignment without modifying each rig's config.
	// Resolution: --agent flag > rig WorkerAgents > town CrewAgents > role agents > defaults.
	// Example: {"bob": "codex", "alice": "claude"}
	CrewAgents map[string]string `json:"crew_agents,omitempty"`

	// AgentEmailDomain is the domain used for agent git identity emails.
	// Agent addresses like "gastown/crew/jack" become "gastown.crew.jack@{domain}".
	// Default: "gastown.local"
	AgentEmailDomain string `json:"agent_email_domain,omitempty"`

	// WebTimeouts configures command execution timeouts for the web dashboard.
	WebTimeouts *WebTimeoutsConfig `json:"web_timeouts,omitempty"`

	// WorkerStatus configures activity-age thresholds for worker status classification.
	WorkerStatus *WorkerStatusConfig `json:"worker_status,omitempty"`

	// FeedCurator configures event deduplication and aggregation windows.
	FeedCurator *FeedCuratorConfig `json:"feed_curator,omitempty"`

	// Convoy configures convoy behavior settings.
	Convoy *ConvoyConfig `json:"convoy,omitempty"`

	// RoleEffort maps role names to effort levels for per-role effort configuration.
	// Keys are role names: "mayor", "deacon", "witness", "refinery", "polecat", "crew", "boot", "dog".
	// Values are effort levels: "low", "medium", "high", "max".
	// Allows cost/speed optimization by using lower effort for simpler roles.
	// Managed by cost-tier presets alongside RoleAgents.
	RoleEffort map[string]string `json:"role_effort,omitempty"`

	// CostTier tracks which cost tier preset was applied (informational).
	// Actual model assignments live in RoleAgents and Agents.
	// Values: "standard", "economy", "budget", or empty for custom configs.
	CostTier string `json:"cost_tier,omitempty"`

	// Scheduler configures the capacity scheduler for polecat dispatch.
	Scheduler *capacity.SchedulerConfig `json:"scheduler,omitempty"`

	// Operational configures operational thresholds (timeouts, retries, intervals).
	// These were previously hardcoded as Go constants throughout the codebase.
	// All values are optional — omitted values use compiled-in defaults.
	Operational *OperationalConfig `json:"operational,omitempty"`

	// DisabledPatrols lists patrol names to disable at the town level.
	// This provides a simple way to turn off individual daemon patrol dogs
	// without editing mayor/daemon.json. Patrol names match the keys used
	// in daemon.json patrols section (e.g., "deacon", "witness", "refinery",
	// "doctor_dog", "compactor_dog", "checkpoint_dog", "wisp_reaper",
	// "dolt_remotes", "dolt_backup", "jsonl_git_backup", "scheduled_maintenance",
	// "main_branch_test", "handler").
	// Example: ["doctor_dog", "compactor_dog"]
	DisabledPatrols []string `json:"disabled_patrols,omitempty"`
}

// NewTownSettings creates a new TownSettings with defaults.
func NewTownSettings() *TownSettings {
	return &TownSettings{
		Type:         "town-settings",
		Version:      CurrentTownSettingsVersion,
		DefaultAgent: "claude",
		Agents:       make(map[string]*RuntimeConfig),
		RoleAgents:   make(map[string]string),
	}
}

// CurrentTownVersion is the current schema version for TownConfig.
// Version 2: Added Owner and PublicName fields for federation identity.
const CurrentTownVersion = 2

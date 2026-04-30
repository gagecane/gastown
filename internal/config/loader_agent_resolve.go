package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// ResolveAgentConfig resolves the agent configuration for a rig.
// It looks up the agent by name in town settings (custom agents) and built-in presets.
//
// Resolution order:
//  1. If rig has Runtime set directly, use it (backwards compatibility)
//  2. If rig has Agent set, look it up in:
//     a. Town's custom agents (from TownSettings.Agents)
//     b. Built-in presets (claude, gemini, codex)
//  3. If rig has no Agent set, use town's default_agent
//  4. Fall back to claude defaults
//
// townRoot is the path to the town directory (e.g., ~/gt).
// rigPath is the path to the rig directory (e.g., ~/gt/gastown).
func ResolveAgentConfig(townRoot, rigPath string) *RuntimeConfig {
	resolveConfigMu.Lock()
	defer resolveConfigMu.Unlock()
	return resolveAgentConfigInternal(townRoot, rigPath)
}

// resolveAgentConfigInternal is the lock-free version of ResolveAgentConfig.
// Caller must hold resolveConfigMu.
func resolveAgentConfigInternal(townRoot, rigPath string) *RuntimeConfig {
	// Load rig settings
	rigSettings, err := LoadRigSettings(RigSettingsPath(rigPath))
	if err != nil {
		rigSettings = nil
	}

	// Backwards compatibility: if Runtime is set directly, use it
	if rigSettings != nil && rigSettings.Runtime != nil {
		rc := fillRuntimeDefaults(rigSettings.Runtime)
		if rc.ResolvedAgent == "" {
			rc.ResolvedAgent = inferAgentName(rc)
		}
		return rc
	}

	// Load town settings for agent lookup
	townSettings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot))
	if err != nil {
		townSettings = NewTownSettings()
	}

	// Load custom agent registry if it exists
	_ = LoadAgentRegistry(DefaultAgentRegistryPath(townRoot))

	// Load rig-level custom agent registry if it exists (for per-rig custom agents)
	_ = LoadRigAgentRegistry(RigAgentRegistryPath(rigPath))

	// Determine which agent name to use
	agentName := ""
	if rigSettings != nil && rigSettings.Agent != "" {
		agentName = rigSettings.Agent
	} else if townSettings.DefaultAgent != "" {
		agentName = townSettings.DefaultAgent
	} else {
		agentName = "claude" // ultimate fallback
	}

	rc := lookupAgentConfig(agentName, townSettings, rigSettings)
	rc.ResolvedAgent = agentName
	return rc
}

// ResolveAgentConfigWithOverride resolves the agent configuration for a rig, with an optional override.
// If agentOverride is non-empty, it is used instead of rig/town defaults.
// Returns the resolved RuntimeConfig, the selected agent name, and an error if the override name
// does not exist in town custom agents or built-in presets.
func ResolveAgentConfigWithOverride(townRoot, rigPath, agentOverride string) (*RuntimeConfig, string, error) {
	resolveConfigMu.Lock()
	defer resolveConfigMu.Unlock()
	return resolveAgentConfigWithOverrideInternal(townRoot, rigPath, agentOverride)
}

// resolveAgentConfigWithOverrideInternal is the lock-free version.
// Caller must hold resolveConfigMu.
func resolveAgentConfigWithOverrideInternal(townRoot, rigPath, agentOverride string) (*RuntimeConfig, string, error) {
	// Load rig settings
	rigSettings, err := LoadRigSettings(RigSettingsPath(rigPath))
	if err != nil {
		rigSettings = nil
	}

	// Backwards compatibility: if Runtime is set directly, use it (but still report agentOverride if present)
	if rigSettings != nil && rigSettings.Runtime != nil && agentOverride == "" {
		rc := fillRuntimeDefaults(rigSettings.Runtime)
		if rc.ResolvedAgent == "" {
			rc.ResolvedAgent = inferAgentName(rc)
		}
		return rc, "", nil
	}

	// Load town settings for agent lookup
	townSettings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot))
	if err != nil {
		townSettings = NewTownSettings()
	}

	// Load custom agent registry if it exists
	_ = LoadAgentRegistry(DefaultAgentRegistryPath(townRoot))

	// Load rig-level custom agent registry if it exists (for per-rig custom agents)
	_ = LoadRigAgentRegistry(RigAgentRegistryPath(rigPath))

	// Determine which agent name to use
	agentName := ""
	var extraArgs []string
	if agentOverride != "" {
		// Handle agent overrides with subcommands (e.g., "opencode acp")
		parts := strings.Fields(agentOverride)
		if len(parts) > 0 {
			agentName = parts[0]
			if len(parts) > 1 {
				extraArgs = parts[1:]
			}
		}
	} else if rigSettings != nil && rigSettings.Agent != "" {
		agentName = rigSettings.Agent
	} else if townSettings.DefaultAgent != "" {
		agentName = townSettings.DefaultAgent
	} else {
		agentName = "claude" // ultimate fallback
	}

	// If an override is requested, validate it exists
	if agentOverride != "" {
		var rc *RuntimeConfig
		// Check rig-level custom agents first
		if rigSettings != nil && rigSettings.Agents != nil {
			if custom, ok := rigSettings.Agents[agentName]; ok && custom != nil {
				rc = fillRuntimeDefaults(custom)
			}
		}
		// Then check town-level custom agents
		if rc == nil && townSettings.Agents != nil {
			if custom, ok := townSettings.Agents[agentName]; ok && custom != nil {
				rc = fillRuntimeDefaults(custom)
			}
		}
		// Then check built-in presets
		if rc == nil {
			if preset := GetAgentPresetByName(agentName); preset != nil {
				rc = RuntimeConfigFromPreset(AgentPreset(agentName))
			}
		}

		if rc == nil {
			return nil, "", fmt.Errorf("agent '%s' not found", agentName)
		}

		// Append extra arguments from the override
		if len(extraArgs) > 0 {
			rc.Args = append(rc.Args, extraArgs...)
		}
		return rc, agentName, nil
	}

	// Normal lookup path (no override)
	rc := lookupAgentConfig(agentName, townSettings, rigSettings)
	rc.ResolvedAgent = agentName

	// If we have extra arguments from the override, append them to the config
	if len(extraArgs) > 0 {
		rc.Args = append(rc.Args, extraArgs...)
	}

	return rc, agentName, nil
}

// ValidateAgentConfig checks if an agent configuration is valid and the binary exists.
// Returns an error describing the issue, or nil if valid.
func ValidateAgentConfig(agentName string, townSettings *TownSettings, rigSettings *RigSettings) error {
	// Check if agent exists in config
	rc := lookupAgentConfigIfExists(agentName, townSettings, rigSettings)
	if rc == nil {
		return fmt.Errorf("agent %q not found in config or built-in presets", agentName)
	}

	// Check if binary exists on system
	if _, err := exec.LookPath(rc.Command); err != nil {
		return fmt.Errorf("agent %q binary %q not found in PATH", agentName, rc.Command)
	}

	return nil
}

// lookupAgentConfigIfExists looks up an agent by name but returns nil if not found
// (instead of falling back to default). Used for validation.
func lookupAgentConfigIfExists(name string, townSettings *TownSettings, rigSettings *RigSettings) *RuntimeConfig {
	// Check rig's custom agents
	if rigSettings != nil && rigSettings.Agents != nil {
		if custom, ok := rigSettings.Agents[name]; ok && custom != nil {
			return fillRuntimeDefaults(custom)
		}
	}

	// Check town's custom agents
	if townSettings != nil && townSettings.Agents != nil {
		if custom, ok := townSettings.Agents[name]; ok && custom != nil {
			return fillRuntimeDefaults(custom)
		}
	}

	// Check built-in presets
	if preset := GetAgentPresetByName(name); preset != nil {
		return RuntimeConfigFromPreset(AgentPreset(name))
	}

	return nil
}

// ResolveRoleAgentConfig resolves the agent configuration for a specific role.
// It checks role-specific agent assignments before falling back to the default agent.
//
// Resolution order:
//  1. Rig's RoleAgents[role] - if set, look up that agent
//  2. Town's RoleAgents[role] - if set, look up that agent
//  3. Fall back to ResolveAgentConfig (rig's Agent → town's DefaultAgent → "claude")
//
// If a configured agent is not found or its binary doesn't exist, a warning is
// printed to stderr and it falls back to the default agent.
//
// role is one of: "mayor", "deacon", "witness", "refinery", "polecat", "crew", "boot".
// townRoot is the path to the town directory (e.g., ~/gt).
// rigPath is the path to the rig directory (e.g., ~/gt/gastown), or empty for town-level roles.
func ResolveRoleAgentConfig(role, townRoot, rigPath string) *RuntimeConfig {
	resolveConfigMu.Lock()
	defer resolveConfigMu.Unlock()
	rc := resolveRoleAgentConfigCore(role, townRoot, rigPath)
	return withRoleSettingsFlag(rc, role, rigPath)
}

// tryResolveNamedAgent attempts to resolve a named agent through the custom agent
// and standard lookup pipelines. Returns the resolved config with ResolvedAgent set,
// or nil if validation fails. The warnPrefix is used in the fallback warning message
// (e.g., "worker_agents[denali]" or "crew_agents[denali]").
func tryResolveNamedAgent(agentName, warnPrefix string, townSettings *TownSettings, rigSettings *RigSettings) *RuntimeConfig {
	if rc := lookupCustomAgentConfig(agentName, townSettings, rigSettings); rc != nil {
		rc.ResolvedAgent = agentName
		return rc
	}
	if err := ValidateAgentConfig(agentName, townSettings, rigSettings); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s=%s - %v, falling back\n", warnPrefix, agentName, err)
		return nil
	}
	rc := lookupAgentConfig(agentName, townSettings, rigSettings)
	rc.ResolvedAgent = agentName
	return rc
}

// ResolveWorkerAgentConfig resolves the agent configuration for a named crew worker.
// Resolution order:
//  1. Rig's WorkerAgents[workerName] — per-worker override
//  2. Town's CrewAgents[workerName] — town-wide per-crew override
//  3. Falls back to ResolveRoleAgentConfig("crew", ...) for remaining resolution
//
// workerName is the crew member name (e.g., "denali").
func ResolveWorkerAgentConfig(workerName, townRoot, rigPath string) *RuntimeConfig {
	resolveConfigMu.Lock()
	defer resolveConfigMu.Unlock()

	// Tier 1: rig's per-worker override
	if workerName != "" && rigPath != "" {
		if rigSettings, err := LoadRigSettings(RigSettingsPath(rigPath)); err == nil && rigSettings != nil {
			if agentName, ok := rigSettings.WorkerAgents[workerName]; ok && agentName != "" {
				townSettings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot))
				if err != nil {
					townSettings = NewTownSettings()
				}
				_ = LoadAgentRegistry(DefaultAgentRegistryPath(townRoot))
				_ = LoadRigAgentRegistry(RigAgentRegistryPath(rigPath))
				if rc := tryResolveNamedAgent(agentName, fmt.Sprintf("worker_agents[%s]", workerName), townSettings, rigSettings); rc != nil {
					return withRoleSettingsFlag(rc, "crew", rigPath)
				}
			}
		}
	}

	// Tier 2: town's per-crew override
	if workerName != "" && townRoot != "" {
		townSettings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot))
		if err == nil && townSettings != nil {
			if agentName, ok := townSettings.CrewAgents[workerName]; ok && agentName != "" {
				var rigSettings *RigSettings
				if rigPath != "" {
					rigSettings, _ = LoadRigSettings(RigSettingsPath(rigPath))
				}
				_ = LoadAgentRegistry(DefaultAgentRegistryPath(townRoot))
				if rigPath != "" {
					_ = LoadRigAgentRegistry(RigAgentRegistryPath(rigPath))
				}
				if rc := tryResolveNamedAgent(agentName, fmt.Sprintf("crew_agents[%s]", workerName), townSettings, rigSettings); rc != nil {
					return withRoleSettingsFlag(rc, "crew", rigPath)
				}
			}
		}
	}

	// Tier 3: fall back to crew role resolution (already holds lock; use core function)
	rc := resolveRoleAgentConfigCore("crew", townRoot, rigPath)
	return withRoleSettingsFlag(rc, "crew", rigPath)
}

// ResolveRoleEffort resolves the effort level for a role.
// Resolution order:
//  1. Rig's RoleEffort[role]
//  2. Town's RoleEffort[role]
//  3. Returns "" (caller falls back to env var / default "high")
//
// Invalid effort levels are warned about and skipped.
func ResolveRoleEffort(role, townRoot, rigPath string) string {
	// Tier 1: ephemeral cost tier override (mirrors agent resolution)
	if tierName := os.Getenv("GT_COST_TIER"); tierName != "" && IsValidTier(tierName) {
		if roleEffort := CostTierRoleEffort(CostTier(tierName)); roleEffort != nil {
			if effort, ok := roleEffort[role]; ok {
				return effort
			}
		}
	}

	// Tier 2: rig-level override
	if rigPath != "" {
		if rigSettings, err := LoadRigSettings(RigSettingsPath(rigPath)); err == nil && rigSettings != nil {
			if effort, ok := rigSettings.RoleEffort[role]; ok && effort != "" {
				if !IsValidEffortLevel(effort) {
					fmt.Fprintf(os.Stderr, "warning: rig role_effort[%s]=%q is not a valid effort level, ignoring\n", role, effort)
				} else {
					return effort
				}
			}
		}
	}

	// Tier 3: town-level setting
	if townRoot != "" {
		if townSettings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot)); err == nil && townSettings != nil {
			if effort, ok := townSettings.RoleEffort[role]; ok && effort != "" {
				if !IsValidEffortLevel(effort) {
					fmt.Fprintf(os.Stderr, "warning: town role_effort[%s]=%q is not a valid effort level, ignoring\n", role, effort)
				} else {
					return effort
				}
			}
		}
	}

	return "" // Caller uses env var fallback, then "high" default
}

// IsResolvedAgentClaude returns true if the RuntimeConfig represents a Claude agent.
// Exported for use in witness/daemon code that needs to skip hardcoded
// Claude start commands when a non-Claude agent is configured.
func IsResolvedAgentClaude(rc *RuntimeConfig) bool {
	if rc == nil {
		return true // Default to Claude when config is unavailable
	}
	return isClaudeAgent(rc)
}

// isClaudeAgent returns true if the RuntimeConfig represents a Claude agent.
// When Provider is explicitly set, it's authoritative. When empty, the Command
// is checked: bare "claude", a path ending in "/claude" (or "\claude" on Windows),
// or an empty command (the default) all indicate Claude.
func isClaudeAgent(rc *RuntimeConfig) bool {
	if rc.Provider != "" {
		return rc.Provider == "claude"
	}
	if rc.Command == "" || rc.Command == "claude" {
		return true
	}
	base := filepath.Base(rc.Command)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return base == "claude"
}

// withRoleSettingsFlag appends --settings to the Args for Claude agents whose
// settings directory differs from the session working directory. Claude Code
// resolves project-level settings from its working directory only; the --settings
// flag tells it where to find them when they live in a parent directory.
func withRoleSettingsFlag(rc *RuntimeConfig, role, rigPath string) *RuntimeConfig {
	if rc == nil || rigPath == "" {
		return rc
	}

	if !isClaudeAgent(rc) {
		return rc
	}

	// Guard against double-adding (ResolveRoleAgentConfig already calls this)
	for _, arg := range rc.Args {
		if arg == "--settings" {
			return rc
		}
	}

	settingsDir := RoleSettingsDir(role, rigPath)
	if settingsDir == "" {
		return rc
	}

	hooksDir := ".claude"
	settingsFile := "settings.json"
	if rc.Hooks != nil {
		if rc.Hooks.Dir != "" {
			hooksDir = rc.Hooks.Dir
		}
		if rc.Hooks.SettingsFile != "" {
			settingsFile = rc.Hooks.SettingsFile
		}
	}

	rc.Args = append(rc.Args, "--settings", filepath.Join(settingsDir, hooksDir, settingsFile))
	return rc
}

// RoleSettingsDir returns the shared settings directory for roles whose session
// working directory differs from their settings location. Returns empty for
// roles where settings and session directory are the same (mayor, deacon).
func RoleSettingsDir(role, rigPath string) string {
	switch role {
	case constants.RoleCrew, constants.RoleWitness, constants.RoleRefinery:
		return filepath.Join(rigPath, role)
	case constants.RolePolecat:
		return filepath.Join(rigPath, "polecats")
	default:
		return ""
	}
}

// tryResolveFromEphemeralTier checks the GT_COST_TIER environment variable
// and returns the appropriate RuntimeConfig for the given role if an ephemeral
// cost tier is set.
//
// Returns:
//   - (rc, true)  — tier is active and has spoken for this role. rc may be nil
//     if the tier says "use default" (empty agent mapping).
//   - (nil, false) — no ephemeral tier active, or role is not tier-managed.
//
// The caller must respect handled=true even when rc is nil: it means the tier
// explicitly wants the default agent for this role, and persisted RoleAgents
// should be skipped to prevent stale config from leaking through.
func tryResolveFromEphemeralTier(role string) (*RuntimeConfig, bool) {
	tierName := os.Getenv("GT_COST_TIER")
	if tierName == "" || !IsValidTier(tierName) {
		return nil, false
	}

	tier := CostTier(tierName)
	roleAgents := CostTierRoleAgents(tier)
	if roleAgents == nil {
		return nil, false
	}

	agentName, ok := roleAgents[role]
	if !ok {
		return nil, false // Role not managed by tiers
	}

	// Empty agent name means "use default (opus)" — signal handled but no override
	if agentName == "" {
		return nil, true
	}

	// Look up the agent config from the tier's agent definitions
	agents := CostTierAgents(tier)
	if agents != nil {
		if rc, found := agents[agentName]; found && rc != nil {
			filled := fillRuntimeDefaults(rc)
			filled.ResolvedAgent = agentName
			return filled, true
		}
	}

	return nil, false
}

// hasExplicitNonClaudeOverride checks if there is an explicit non-Claude agent
// assignment either specifically for the role (in rig or town RoleAgents) or
// globally (via rig Agent or town DefaultAgent). This prevents fallback logic
// and cost tiers from silently replacing intentional non-Claude agent selections.
func hasExplicitNonClaudeOverride(role string, townSettings *TownSettings, rigSettings *RigSettings) bool {
	// Check rig's RoleAgents
	if rigSettings != nil && rigSettings.RoleAgents != nil {
		if agentName, ok := rigSettings.RoleAgents[role]; ok && agentName != "" {
			if rc := lookupAgentConfigIfExists(agentName, townSettings, rigSettings); rc != nil && !isClaudeAgent(rc) {
				return true
			}
		}
	}
	// Check town's RoleAgents
	if townSettings != nil && townSettings.RoleAgents != nil {
		if agentName, ok := townSettings.RoleAgents[role]; ok && agentName != "" {
			if rc := lookupAgentConfigIfExists(agentName, townSettings, rigSettings); rc != nil && !isClaudeAgent(rc) {
				return true
			}
		}
	}
	// Check rig's global Agent
	if rigSettings != nil && rigSettings.Agent != "" {
		if rc := lookupAgentConfigIfExists(rigSettings.Agent, townSettings, rigSettings); rc != nil && !isClaudeAgent(rc) {
			return true
		}
	}
	// Check town's DefaultAgent
	if townSettings != nil && townSettings.DefaultAgent != "" {
		if rc := lookupAgentConfigIfExists(townSettings.DefaultAgent, townSettings, rigSettings); rc != nil && !isClaudeAgent(rc) {
			return true
		}
	}
	return false
}

func resolveRoleAgentConfigCore(role, townRoot, rigPath string) *RuntimeConfig {
	// Load rig settings (may be nil for town-level roles like mayor/deacon)
	var rigSettings *RigSettings
	if rigPath != "" {
		var err error
		rigSettings, err = LoadRigSettings(RigSettingsPath(rigPath))
		if err != nil {
			rigSettings = nil
		}
	}

	// Load town settings
	townSettings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot))
	if err != nil {
		townSettings = NewTownSettings()
	}

	// Load custom agent registries
	_ = LoadAgentRegistry(DefaultAgentRegistryPath(townRoot))
	if rigPath != "" {
		_ = LoadRigAgentRegistry(RigAgentRegistryPath(rigPath))
	}

	// Dogs default to Haiku (cheap infrastructure workers), but respect
	// explicit non-Claude overrides (e.g., RoleAgents["dog"] = "opencode").
	if role == "dog" {
		if hasExplicitNonClaudeOverride(role, townSettings, rigSettings) {
			// Fall through to normal resolution below
		} else {
			return claudeHaikuPreset()
		}
	}

	// Check ephemeral cost tier (GT_COST_TIER env var)
	tierRC, tierHandled := tryResolveFromEphemeralTier(role)
	if tierHandled {
		if tierRC != nil {
			// Tier wants a specific Claude model for this role.
			// But if there's an explicit non-Claude rig/town override, respect it —
			// cost tiers only manage Claude model selection, not agent platform choice.
			if hasExplicitNonClaudeOverride(role, townSettings, rigSettings) {
				// Fall through to normal resolution below
			} else {
				return tierRC
			}
		} else {
			// Tier says "use default" for this role — but if there's an explicit
			// non-Claude override, respect it (cost tiers only manage Claude models).
			if hasExplicitNonClaudeOverride(role, townSettings, rigSettings) {
				// Fall through to normal resolution below
			} else {
				// Skip persisted RoleAgents to prevent stale config from leaking
				// through, go straight to default resolution
				// (rig's Agent → town's DefaultAgent → "claude").
				return resolveAgentConfigInternal(townRoot, rigPath)
			}
		}
	}

	// Check rig's RoleAgents first
	if rigSettings != nil && rigSettings.RoleAgents != nil {
		if agentName, ok := rigSettings.RoleAgents[role]; ok && agentName != "" {
			if rc := lookupCustomAgentConfig(agentName, townSettings, rigSettings); rc != nil {
				rc.ResolvedAgent = agentName
				return rc
			}
			if err := ValidateAgentConfig(agentName, townSettings, rigSettings); err != nil {
				fmt.Fprintf(os.Stderr, "warning: role_agents[%s]=%s - %v, falling back to default\n", role, agentName, err)
			} else {
				rc := lookupAgentConfig(agentName, townSettings, rigSettings)
				rc.ResolvedAgent = agentName
				return rc
			}
		}
	}

	// Check town's RoleAgents
	if townSettings.RoleAgents != nil {
		if agentName, ok := townSettings.RoleAgents[role]; ok && agentName != "" {
			if rc := lookupCustomAgentConfig(agentName, townSettings, rigSettings); rc != nil {
				rc.ResolvedAgent = agentName
				return rc
			}
			if err := ValidateAgentConfig(agentName, townSettings, rigSettings); err != nil {
				fmt.Fprintf(os.Stderr, "warning: role_agents[%s]=%s - %v, falling back to default\n", role, agentName, err)
			} else {
				rc := lookupAgentConfig(agentName, townSettings, rigSettings)
				rc.ResolvedAgent = agentName
				return rc
			}
		}
	}

	// Fall back to existing resolution (rig's Agent → town's DefaultAgent → "claude")
	// Use internal version — caller already holds resolveConfigMu.
	return resolveAgentConfigInternal(townRoot, rigPath)
}

// ResolveRoleAgentName returns the agent name that would be used for a specific role.
// This is useful for logging and diagnostics.
// Returns the agent name and whether it came from role-specific configuration.
//
// NOTE: This function does not account for ephemeral cost tier overrides
// (GT_COST_TIER env var). It reflects persisted config only. For the actual
// runtime agent config, use ResolveRoleAgentConfig.
func ResolveRoleAgentName(role, townRoot, rigPath string) (agentName string, isRoleSpecific bool) {
	// Load rig settings
	var rigSettings *RigSettings
	if rigPath != "" {
		var err error
		rigSettings, err = LoadRigSettings(RigSettingsPath(rigPath))
		if err != nil {
			rigSettings = nil
		}
	}

	// Load town settings
	townSettings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot))
	if err != nil {
		townSettings = NewTownSettings()
	}

	// Check rig's RoleAgents first
	if rigSettings != nil && rigSettings.RoleAgents != nil {
		if name, ok := rigSettings.RoleAgents[role]; ok && name != "" {
			return name, true
		}
	}

	// Check town's RoleAgents
	if townSettings.RoleAgents != nil {
		if name, ok := townSettings.RoleAgents[role]; ok && name != "" {
			return name, true
		}
	}

	// Fall back to existing resolution
	if rigSettings != nil && rigSettings.Agent != "" {
		return rigSettings.Agent, false
	}
	if townSettings.DefaultAgent != "" {
		return townSettings.DefaultAgent, false
	}
	return "claude", false
}

// ResolveAgentConfigByName looks up an agent's RuntimeConfig by name without requiring
// the agent binary to be installed. Checks custom agents first, then built-in presets.
// Returns nil if the agent name is unknown. Used by hooks sync, which needs the preset's
// hooks metadata regardless of whether the binary is installed on this machine.
func ResolveAgentConfigByName(name, townRoot, rigPath string) *RuntimeConfig {
	resolveConfigMu.Lock()
	defer resolveConfigMu.Unlock()

	var rigSettings *RigSettings
	if rigPath != "" {
		if rs, err := LoadRigSettings(RigSettingsPath(rigPath)); err == nil {
			rigSettings = rs
		}
	}

	townSettings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot))
	if err != nil {
		townSettings = NewTownSettings()
	}

	_ = LoadAgentRegistry(DefaultAgentRegistryPath(townRoot))
	if rigPath != "" {
		_ = LoadRigAgentRegistry(RigAgentRegistryPath(rigPath))
	}

	return lookupAgentConfigIfExists(name, townSettings, rigSettings)
}

// HasExplicitRoleAgent returns true if role_agents (rig or town level)
// explicitly maps this role to a named agent. This distinguishes between
// "role_agents says use claude-sonnet" and "no role_agents entry, falling
// back to defaults". When an explicit mapping exists, the TOML start_command
// should be skipped in favor of BuildStartupCommandFromConfig which honors
// the model/settings from the mapped agent definition.
func HasExplicitRoleAgent(role, townRoot, rigPath string) bool {
	_, isRoleSpecific := ResolveRoleAgentName(role, townRoot, rigPath)
	return isRoleSpecific
}

// lookupAgentConfig looks up an agent by name.
// Checks rig-level custom agents first, then town's custom agents, then built-in presets from agents.go.
// Falls back to DefaultRuntimeConfig() if no match is found.
func lookupAgentConfig(name string, townSettings *TownSettings, rigSettings *RigSettings) *RuntimeConfig {
	if rc := lookupAgentConfigIfExists(name, townSettings, rigSettings); rc != nil {
		return rc
	}
	return DefaultRuntimeConfig()
}

// lookupCustomAgentConfig looks up custom agents only (rig or town).
// It skips binary validation so tests and config resolution can proceed
// even if the command isn't on PATH yet.
func lookupCustomAgentConfig(name string, townSettings *TownSettings, rigSettings *RigSettings) *RuntimeConfig {
	if rigSettings != nil && rigSettings.Agents != nil {
		if custom, ok := rigSettings.Agents[name]; ok && custom != nil {
			return fillRuntimeDefaults(custom)
		}
	}

	if townSettings != nil && townSettings.Agents != nil {
		if custom, ok := townSettings.Agents[name]; ok && custom != nil {
			return fillRuntimeDefaults(custom)
		}
	}

	return nil
}

// fillRuntimeDefaults fills in default values for empty RuntimeConfig fields.
// It creates a deep copy to prevent mutation of the original config.
//
// Default behavior:
//   - Command defaults to "claude" if empty
//   - Args defaults to ["--dangerously-skip-permissions"] if nil
//   - Empty Args slice ([]string{}) means "no args" and is preserved as-is
//
// All fields are deep-copied: modifying the returned config will not affect
// the input config, including nested structs and slices.
func fillRuntimeDefaults(rc *RuntimeConfig) *RuntimeConfig {
	if rc == nil {
		return DefaultRuntimeConfig()
	}

	// Create result with scalar fields (strings are immutable in Go)
	result := &RuntimeConfig{
		Provider:      rc.Provider,
		Command:       rc.Command,
		InitialPrompt: rc.InitialPrompt,
		PromptMode:    rc.PromptMode,
		ResolvedAgent: rc.ResolvedAgent,
	}

	// Deep copy Args slice to avoid sharing backing array
	if rc.Args != nil {
		result.Args = make([]string, len(rc.Args))
		copy(result.Args, rc.Args)
	}

	// Deep copy ExecWrapper slice
	if rc.ExecWrapper != nil {
		result.ExecWrapper = make([]string, len(rc.ExecWrapper))
		copy(result.ExecWrapper, rc.ExecWrapper)
	}

	// Deep copy Env map
	if len(rc.Env) > 0 {
		result.Env = make(map[string]string, len(rc.Env))
		for k, v := range rc.Env {
			result.Env[k] = v
		}
	}

	// Deep copy nested structs (nil checks prevent panic on access)
	if rc.Session != nil {
		result.Session = &RuntimeSessionConfig{
			SessionIDEnv: rc.Session.SessionIDEnv,
			ConfigDirEnv: rc.Session.ConfigDirEnv,
		}
	}

	if rc.Hooks != nil {
		result.Hooks = &RuntimeHooksConfig{
			Provider:     rc.Hooks.Provider,
			Dir:          rc.Hooks.Dir,
			SettingsFile: rc.Hooks.SettingsFile,
		}
	}

	if rc.Tmux != nil {
		result.Tmux = &RuntimeTmuxConfig{
			ReadyPromptPrefix: rc.Tmux.ReadyPromptPrefix,
			ReadyDelayMs:      rc.Tmux.ReadyDelayMs,
		}
		// Deep copy ProcessNames slice
		if rc.Tmux.ProcessNames != nil {
			result.Tmux.ProcessNames = make([]string, len(rc.Tmux.ProcessNames))
			copy(result.Tmux.ProcessNames, rc.Tmux.ProcessNames)
		}
	}

	if rc.Instructions != nil {
		result.Instructions = &RuntimeInstructionsConfig{
			File: rc.Instructions.File,
		}
	}

	// Deep copy ACP config
	if rc.ACP != nil {
		result.ACP = &ACPConfig{
			Mode:    rc.ACP.Mode,
			Command: rc.ACP.Command,
		}
		if rc.ACP.Args != nil {
			result.ACP.Args = make([]string, len(rc.ACP.Args))
			copy(result.ACP.Args, rc.ACP.Args)
		}
	}

	// Resolve preset for data-driven defaults.
	// Use provider if set, otherwise try to match by command name.
	presetName := result.Provider
	if presetName == "" && result.Command != "" {
		presetName = result.Command
	}
	preset := GetAgentPresetByName(presetName)
	if preset == nil {
		preset = GetAgentPreset(AgentClaude) // fall back to Claude defaults
	}

	// Apply defaults for required fields from preset
	if result.Command == "" && preset != nil {
		result.Command = preset.Command
	}
	if result.Args == nil && preset != nil {
		result.Args = append([]string(nil), preset.Args...)
	}

	// Auto-fill Hooks defaults from preset for agents that support hooks.
	if result.Hooks == nil && preset != nil && preset.HooksProvider != "" {
		result.Hooks = &RuntimeHooksConfig{
			Provider:     preset.HooksProvider,
			Dir:          preset.HooksDir,
			SettingsFile: preset.HooksSettingsFile,
		}
	}

	// Auto-fill Session defaults from preset.
	if result.Session == nil && preset != nil && (preset.SessionIDEnv != "" || preset.ConfigDirEnv != "") {
		result.Session = &RuntimeSessionConfig{
			SessionIDEnv: preset.SessionIDEnv,
			ConfigDirEnv: preset.ConfigDirEnv,
		}
	}

	// Auto-fill Tmux defaults from preset (process detection, readiness).
	if result.Tmux == nil && preset != nil && (len(preset.ProcessNames) > 0 || preset.ReadyPromptPrefix != "" || preset.ReadyDelayMs > 0) {
		result.Tmux = &RuntimeTmuxConfig{
			ProcessNames:      append([]string(nil), preset.ProcessNames...),
			ReadyPromptPrefix: preset.ReadyPromptPrefix,
			ReadyDelayMs:      preset.ReadyDelayMs,
		}
	}

	// Auto-fill PromptMode from preset.
	if result.PromptMode == "" && preset != nil && preset.PromptMode != "" {
		result.PromptMode = preset.PromptMode
	}

	// Auto-fill Instructions defaults from preset.
	if result.Instructions == nil && preset != nil && preset.InstructionsFile != "" {
		result.Instructions = &RuntimeInstructionsConfig{
			File: preset.InstructionsFile,
		}
	}

	// Auto-fill Session defaults from preset when not explicitly set.
	// Custom agents (e.g., "claude-opus" with Command:"claude") inherit
	// SessionIDEnv/ConfigDirEnv from the matched preset, enabling session
	// resume and GT_SESSION_ID_ENV propagation in handoffs.
	if result.Session == nil && preset != nil && (preset.SessionIDEnv != "" || preset.ConfigDirEnv != "") {
		result.Session = &RuntimeSessionConfig{
			SessionIDEnv: preset.SessionIDEnv,
			ConfigDirEnv: preset.ConfigDirEnv,
		}
	}

	// Auto-fill Tmux defaults from preset for process detection and readiness.
	// Custom agents matching a known preset by command (e.g., "claude-opus" →
	// claude preset) get ProcessNames and ReadyPromptPrefix needed for
	// WaitForRuntimeReady to detect agent startup correctly.
	if result.Tmux == nil && preset != nil && (len(preset.ProcessNames) > 0 || preset.ReadyPromptPrefix != "" || preset.ReadyDelayMs > 0) {
		result.Tmux = &RuntimeTmuxConfig{
			ReadyPromptPrefix: preset.ReadyPromptPrefix,
			ReadyDelayMs:      preset.ReadyDelayMs,
		}
		if len(preset.ProcessNames) > 0 {
			result.Tmux.ProcessNames = append([]string(nil), preset.ProcessNames...)
		}
	}

	// Auto-fill Env defaults from preset.
	if preset != nil && len(preset.Env) > 0 {
		if result.Env == nil {
			result.Env = make(map[string]string)
		}
		for k, v := range preset.Env {
			if _, ok := result.Env[k]; !ok {
				result.Env[k] = v
			}
		}
	}

	// Auto-fill ACP config from preset if not explicitly set.
	// This allows custom agents to inherit ACP support from their base preset.
	if result.ACP == nil && preset != nil && preset.ACP != nil {
		result.ACP = &ACPConfig{
			Mode:    preset.ACP.Mode,
			Command: preset.ACP.Command,
		}
		if preset.ACP.Args != nil {
			result.ACP.Args = make([]string, len(preset.ACP.Args))
			copy(result.ACP.Args, preset.ACP.Args)
		}
	}

	return result
}

// inferAgentName determines the agent name from a legacy RuntimeConfig.
// It mirrors the preset resolution logic in fillRuntimeDefaults:
// use Provider if set, otherwise Command, falling back to "claude".
func inferAgentName(rc *RuntimeConfig) string {
	if rc.Provider != "" {
		return rc.Provider
	}
	if rc.Command != "" {
		return rc.Command
	}
	return "claude"
}

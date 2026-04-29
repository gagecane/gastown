// Rig and crew configuration types. Extracted from types.go.
package config

import "time"

// RigsConfig represents the rigs registry (mayor/rigs.json).
type RigsConfig struct {
	Version int                 `json:"version"`
	Rigs    map[string]RigEntry `json:"rigs"`
}

// RigEntry represents a single rig in the registry.
type RigEntry struct {
	GitURL      string       `json:"git_url"`
	PushURL     string       `json:"push_url,omitempty"`
	UpstreamURL string       `json:"upstream_url,omitempty"` // optional upstream URL (for fork workflows)
	LocalRepo   string       `json:"local_repo,omitempty"`
	AddedAt     time.Time    `json:"added_at"`
	BeadsConfig *BeadsConfig `json:"beads,omitempty"`
}

// BeadsConfig represents beads configuration for a rig.
type BeadsConfig struct {
	Repo   string `json:"repo"`   // "local" | path | git-url
	Prefix string `json:"prefix"` // issue prefix
}

// CurrentRigsVersion is the current schema version for RigsConfig.
const CurrentRigsVersion = 1

// CurrentRigConfigVersion is the current schema version for RigConfig.
const CurrentRigConfigVersion = 1

// CurrentRigSettingsVersion is the current schema version for RigSettings.
const CurrentRigSettingsVersion = 1

// RigConfig represents per-rig identity (rig/config.json).
// This contains only identity - behavioral config is in settings/config.json.
type RigConfig struct {
	Type        string       `json:"type"`                   // "rig"
	Version     int          `json:"version"`                // schema version
	Name        string       `json:"name"`                   // rig name
	GitURL      string       `json:"git_url"`                // git repository URL
	PushURL     string       `json:"push_url,omitempty"`     // optional push URL (fork for read-only upstreams)
	UpstreamURL string       `json:"upstream_url,omitempty"` // optional upstream URL (for fork workflows)
	LocalRepo   string       `json:"local_repo,omitempty"`
	CreatedAt   time.Time    `json:"created_at"` // when the rig was created
	Beads       *BeadsConfig `json:"beads,omitempty"`
}

// WorkflowConfig represents workflow settings for a rig.
type WorkflowConfig struct {
	// DefaultFormula is the formula to use when `gt formula run` is called without arguments.
	// If empty, no default is set and a formula name must be provided.
	DefaultFormula string `json:"default_formula,omitempty"`
}

// RigSettings represents per-rig behavioral configuration (settings/config.json).
type RigSettings struct {
	Type       string            `json:"type"`                  // "rig-settings"
	Version    int               `json:"version"`               // schema version
	MergeQueue *MergeQueueConfig `json:"merge_queue,omitempty"` // merge queue settings
	Theme      *ThemeConfig      `json:"theme,omitempty"`       // tmux theme settings
	Namepool   *NamepoolConfig   `json:"namepool,omitempty"`    // polecat name pool settings
	Crew       *CrewConfig       `json:"crew,omitempty"`        // crew startup settings
	Workflow   *WorkflowConfig   `json:"workflow,omitempty"`    // workflow settings
	Runtime    *RuntimeConfig    `json:"runtime,omitempty"`     // LLM runtime settings (deprecated: use Agent)

	// Agent selects which agent preset to use for this rig.
	// Can be a built-in preset ("claude", "gemini", "codex", "cursor", "auggie", "amp", "opencode", "copilot")
	// or a custom agent defined in settings/agents.json.
	// If empty, uses the town's default_agent setting.
	// Takes precedence over Runtime if both are set.
	Agent string `json:"agent,omitempty"`

	// Agents defines custom agent configurations or overrides for this rig.
	// Similar to TownSettings.Agents but applies to this rig only.
	// Allows per-rig custom agents for polecats and crew members.
	Agents map[string]*RuntimeConfig `json:"agents,omitempty"`

	// RoleAgents maps role names to agent aliases for per-role model selection.
	// Keys are role names: "witness", "refinery", "polecat", "crew".
	// Values are agent names (built-in presets or custom agents).
	// Overrides TownSettings.RoleAgents for this specific rig.
	// Example: {"witness": "claude-haiku", "polecat": "claude-sonnet"}
	RoleAgents map[string]string `json:"role_agents,omitempty"`

	// WorkerAgents maps individual crew worker names to agent aliases.
	// Allows per-worker agent selection, overriding RoleAgents["crew"].
	// Takes precedence over RoleAgents["crew"] but is overridden by explicit --agent flags.
	// Example: {"denali": "codex", "glacier": "gemini"}
	WorkerAgents map[string]string `json:"worker_agents,omitempty"`

	// RoleEffort maps role names to effort levels, overriding TownSettings.RoleEffort for this rig.
	// Keys are role names: "witness", "refinery", "polecat", "crew".
	// Values are effort levels: "low", "medium", "high", "max".
	// Example: {"crew": "max", "witness": "low"}
	RoleEffort map[string]string `json:"role_effort,omitempty"`
}

// CrewConfig represents crew workspace settings for a rig.
type CrewConfig struct {
	// Startup is a natural language instruction for which crew to start on boot.
	// Interpreted by AI during startup. Examples:
	//   "max"                    - start only max
	//   "joe and max"            - start joe and max
	//   "all"                    - start all crew members
	//   "pick one"               - start any one crew member
	//   "none"                   - don't auto-start any crew
	//   "max, but not emma"      - start max, skip emma
	// If empty, defaults to starting no crew automatically.
	Startup string `json:"startup,omitempty"`
}

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// GetRuntimeCommand is a convenience function that returns the full command string
// for starting an LLM session. It resolves the agent config and builds the command.
func GetRuntimeCommand(rigPath string) string {
	if rigPath == "" {
		// Try to detect town root from cwd for town-level agents (mayor, deacon)
		townRoot, err := findTownRootFromCwd()
		if err != nil {
			return DefaultRuntimeConfig().BuildCommand()
		}
		return ResolveAgentConfig(townRoot, "").BuildCommand()
	}
	// Derive town root from rig path (rig is typically ~/gt/<rigname>)
	townRoot := filepath.Dir(rigPath)
	return ResolveAgentConfig(townRoot, rigPath).BuildCommand()
}

// GetRuntimeCommandWithAgentOverride returns the full command for starting an LLM session,
// using agentOverride if non-empty.
func GetRuntimeCommandWithAgentOverride(rigPath, agentOverride string) (string, error) {
	if rigPath == "" {
		townRoot, err := findTownRootFromCwd()
		if err != nil {
			return DefaultRuntimeConfig().BuildCommand(), nil
		}
		rc, _, resolveErr := ResolveAgentConfigWithOverride(townRoot, "", agentOverride)
		if resolveErr != nil {
			return "", resolveErr
		}
		return rc.BuildCommand(), nil
	}

	townRoot := filepath.Dir(rigPath)
	rc, _, err := ResolveAgentConfigWithOverride(townRoot, rigPath, agentOverride)
	if err != nil {
		return "", err
	}
	return rc.BuildCommand(), nil
}

// GetRuntimeCommandWithPrompt returns the full command with an initial prompt.
func GetRuntimeCommandWithPrompt(rigPath, prompt string) string {
	if rigPath == "" {
		// Try to detect town root from cwd for town-level agents (mayor, deacon)
		townRoot, err := findTownRootFromCwd()
		if err != nil {
			return DefaultRuntimeConfig().BuildCommandWithPrompt(prompt)
		}
		return ResolveAgentConfig(townRoot, "").BuildCommandWithPrompt(prompt)
	}
	townRoot := filepath.Dir(rigPath)
	return ResolveAgentConfig(townRoot, rigPath).BuildCommandWithPrompt(prompt)
}

// GetRuntimeCommandWithPromptAndAgentOverride returns the full command with an initial prompt,
// using agentOverride if non-empty.
func GetRuntimeCommandWithPromptAndAgentOverride(rigPath, prompt, agentOverride string) (string, error) {
	if rigPath == "" {
		townRoot, err := findTownRootFromCwd()
		if err != nil {
			return DefaultRuntimeConfig().BuildCommandWithPrompt(prompt), nil
		}
		rc, _, resolveErr := ResolveAgentConfigWithOverride(townRoot, "", agentOverride)
		if resolveErr != nil {
			return "", resolveErr
		}
		return rc.BuildCommandWithPrompt(prompt), nil
	}

	townRoot := filepath.Dir(rigPath)
	rc, _, err := ResolveAgentConfigWithOverride(townRoot, rigPath, agentOverride)
	if err != nil {
		return "", err
	}
	return rc.BuildCommandWithPrompt(prompt), nil
}

// BuildStartupCommand builds a full startup command with environment exports.
// envVars is a map of environment variable names to values.
// rigPath is optional - if empty, uses envVars["GT_ROOT"] to find town root,
// falling back to cwd detection if GT_ROOT is not set.
// prompt is optional - if provided, appended as the initial prompt.
//
// If envVars contains GT_ROLE, the function uses role-based agent resolution
// (ResolveRoleAgentConfig) to select the appropriate agent for the role.
// This enables per-role model selection via role_agents in settings.
func BuildStartupCommand(envVars map[string]string, rigPath, prompt string) string {
	var rc *RuntimeConfig
	var townRoot string

	// Extract role from envVars for role-based agent resolution.
	// GT_ROLE may be compound format (e.g., "rig/refinery") so we extract
	// the simple role name for role_agents lookup.
	role := ExtractSimpleRole(envVars["GT_ROLE"])

	if rigPath != "" {
		// Derive town root from rig path
		townRoot = filepath.Dir(rigPath)
		if role == "crew" && envVars["GT_CREW"] != "" {
			// Per-worker agent resolution: check worker_agents before role_agents
			rc = ResolveWorkerAgentConfig(envVars["GT_CREW"], townRoot, rigPath)
		} else if role != "" {
			// Use role-based agent resolution for per-role model selection
			rc = ResolveRoleAgentConfig(role, townRoot, rigPath)
		} else {
			rc = ResolveAgentConfig(townRoot, rigPath)
		}
	} else {
		// For town-level agents (mayor, deacon), prefer GT_ROOT from envVars
		// (set by AgentEnv) over cwd detection. This ensures role_agents config
		// is respected even when the daemon runs outside the town hierarchy.
		townRoot = envVars["GT_ROOT"]
		if townRoot == "" {
			var err error
			townRoot, err = findTownRootFromCwd()
			if err != nil {
				rc = DefaultRuntimeConfig()
			}
		}
		if rc == nil {
			if role != "" {
				rc = ResolveRoleAgentConfig(role, townRoot, "")
			} else {
				rc = ResolveAgentConfig(townRoot, "")
			}
		}
	}

	// Apply exec wrapper from rig/town settings if not already set on the resolved config.
	// ExecWrapper is a deployment-level setting (sandbox/container) independent of agent choice.
	if len(rc.ExecWrapper) == 0 {
		rc.ExecWrapper = resolveExecWrapper(rigPath)
	}

	// Copy env vars to avoid mutating caller map
	resolvedEnv := make(map[string]string, len(envVars)+2)
	for k, v := range envVars {
		resolvedEnv[k] = v
	}
	// Add GT_ROOT so agents can find town-level resources (formulas, etc.)
	if townRoot != "" {
		resolvedEnv["GT_ROOT"] = townRoot
	}
	if rc.Session != nil && rc.Session.SessionIDEnv != "" {
		resolvedEnv["GT_SESSION_ID_ENV"] = rc.Session.SessionIDEnv
	}
	// Set GT_AGENT from resolved agent name so IsAgentAlive can detect
	// non-Claude processes (e.g., opencode). Without this, witness patrol
	// falls back to ["node", "claude"] process detection and auto-nukes
	// polecats running non-Claude agents. See: gt-agent-role-agents.
	if rc.ResolvedAgent != "" {
		resolvedEnv["GT_AGENT"] = rc.ResolvedAgent
	}
	// Set GT_PROCESS_NAMES for accurate liveness detection. Custom agents may
	// shadow built-in preset names (e.g., custom "codex" running "opencode"),
	// so we resolve process names from both agent name and actual command.
	processNames := ResolveProcessNames(rc.ResolvedAgent, rc.Command)
	resolvedEnv["GT_PROCESS_NAMES"] = strings.Join(processNames, ",")
	// Merge agent-specific env vars (e.g., OPENCODE_PERMISSION for yolo mode)
	for k, v := range rc.Env {
		resolvedEnv[k] = v
	}

	SanitizeAgentEnv(resolvedEnv, envVars)

	var cmd string
	if runtime.GOOS == "windows" {
		// On Windows, tmux (psmux) uses PowerShell and send-keys has line length
		// limits. Write env vars + agent command to a temp .ps1 script and invoke
		// that instead. This avoids send-keys corrupting long commands.
		var scriptLines []string
		keys := make([]string, 0, len(resolvedEnv))
		for k := range resolvedEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			scriptLines = append(scriptLines, fmt.Sprintf("$env:%s=%s", k, psQuote(resolvedEnv[k])))
		}

		var agentCmd string
		if len(rc.ExecWrapper) > 0 {
			agentCmd = strings.Join(rc.ExecWrapper, " ") + " "
		}
		if prompt != "" {
			agentCmd += "& " + rc.BuildCommandWithPrompt(prompt)
		} else {
			agentCmd += "& " + rc.BuildCommand()
		}
		scriptLines = append(scriptLines, agentCmd)

		// Write script to temp file in town's daemon dir
		townRoot := resolvedEnv["GT_ROOT"]
		if townRoot == "" {
			townRoot = os.TempDir()
		}
		scriptDir := filepath.Join(townRoot, "daemon", "scripts")
		_ = os.MkdirAll(scriptDir, 0755)
		role := resolvedEnv["GT_ROLE"]
		if role == "" {
			role = "agent"
		}
		// Sanitize role for filename (replace / with -)
		safeRole := strings.ReplaceAll(role, "/", "-")
		scriptPath := filepath.Join(scriptDir, safeRole+"-startup.ps1")
		scriptContent := strings.Join(scriptLines, "\n") + "\n"
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
			// Fallback: inline command (may fail if too long)
			cmd = strings.Join(scriptLines, "; ")
		} else {
			cmd = "& " + psQuote(scriptPath)
		}
	} else {
		// Build environment export prefix (POSIX shell)
		var exports []string
		for k, v := range resolvedEnv {
			exports = append(exports, fmt.Sprintf("%s=%s", k, ShellQuote(v)))
		}

		// Sort for deterministic output
		sort.Strings(exports)

		if len(exports) > 0 {
			// Use 'exec env' instead of 'export ... &&' so the agent process
			// replaces the shell. This allows WaitForCommand to detect the
			// running agent via pane_current_command (which shows the direct
			// process, not child processes).
			cmd = "exec env " + strings.Join(exports, " ") + " "
		}

		// Insert exec wrapper between env vars and agent command if configured.
		// Example: exec env VAR=val ... exitbox run --profile=foo -- claude ...
		if len(rc.ExecWrapper) > 0 {
			cmd += strings.Join(rc.ExecWrapper, " ") + " "
		}

		// Add runtime command
		if prompt != "" {
			cmd += rc.BuildCommandWithPrompt(prompt)
		} else {
			cmd += rc.BuildCommand()
		}
	}

	return cmd
}

// SanitizeAgentEnv clears environment variables that are known to break agent
// startup when inherited from the parent shell/tmux environment.
//
// This is a SUPPLEMENTAL guard for paths that don't use AgentEnv() (which is
// the primary guard — see env.go). It protects: lifecycle.go's default path
// (non-polecat/non-crew roles) and handoff.go's manual export building.
// For callers that pass AgentEnv()-produced maps, this is a no-op since
// AgentEnv() already sets NODE_OPTIONS="".
//
// callerEnv is the original env map from the caller (before rc.Env merging).
// resolvedEnv is the post-merge map that may also contain values from rc.Env.
// NODE_OPTIONS is only cleared if neither callerEnv nor resolvedEnv (via rc.Env)
// explicitly provides it.
func SanitizeAgentEnv(resolvedEnv, callerEnv map[string]string) {
	// NODE_OPTIONS may contain debugger flags (e.g., --inspect from VSCode)
	// that cause Claude's Node.js runtime to crash with "Debugger attached" errors.
	// Only clear if not explicitly provided by the caller or agent config (rc.Env).
	if _, ok := callerEnv["NODE_OPTIONS"]; !ok {
		// Inner guard: preserve if rc.Env already set it in resolvedEnv
		if _, ok := resolvedEnv["NODE_OPTIONS"]; !ok {
			resolvedEnv["NODE_OPTIONS"] = ""
		}
	}

	// CLAUDECODE is set by Claude Code v2.x on startup and triggers nested session
	// detection. When gt sling is invoked from within a Claude Code session, tmux
	// inherits this variable into its global environment, causing new polecat sessions
	// to fail with "Nested sessions share runtime resources and will crash all active
	// sessions." Clear it unless the caller explicitly provides it.
	// See: https://github.com/steveyegge/gastown/issues/1666
	if _, ok := callerEnv["CLAUDECODE"]; !ok {
		resolvedEnv["CLAUDECODE"] = ""
	}
}

// PrependEnv prepends export statements to a command string.
// Values containing special characters are properly shell-quoted.
// On Windows, uses PowerShell $env: syntax.
func PrependEnv(command string, envVars map[string]string) string {
	if len(envVars) == 0 {
		return command
	}

	var exports []string
	for k, v := range envVars {
		if runtime.GOOS == "windows" {
			exports = append(exports, fmt.Sprintf("$env:%s=%s", k, psQuote(v)))
		} else {
			exports = append(exports, fmt.Sprintf("%s=%s", k, ShellQuote(v)))
		}
	}

	sort.Strings(exports)
	if runtime.GOOS == "windows" {
		return strings.Join(exports, "; ") + "; " + command
	}
	return "export " + strings.Join(exports, " ") + " && " + command
}

// BuildStartupCommandWithAgentOverride builds a startup command like BuildStartupCommand,
// but uses agentOverride if non-empty.
//
// Resolution priority:
//  1. agentOverride (explicit override)
//  2. role_agents[GT_ROLE] (if GT_ROLE is in envVars)
//  3. Default agent resolution (rig's Agent → town's DefaultAgent → "claude")
func BuildStartupCommandWithAgentOverride(envVars map[string]string, rigPath, prompt, agentOverride string) (string, error) {
	var rc *RuntimeConfig
	var townRoot string

	// Extract role from envVars for role-based agent resolution (when no override)
	role := ExtractSimpleRole(envVars["GT_ROLE"])

	if rigPath != "" {
		townRoot = filepath.Dir(rigPath)
		if agentOverride != "" {
			var err error
			rc, _, err = ResolveAgentConfigWithOverride(townRoot, rigPath, agentOverride)
			if err != nil {
				return "", err
			}
		} else if role == "crew" && envVars["GT_CREW"] != "" {
			// Per-worker agent resolution: check worker_agents before role_agents
			rc = ResolveWorkerAgentConfig(envVars["GT_CREW"], townRoot, rigPath)
		} else if role != "" {
			// No override, use role-based agent resolution
			rc = ResolveRoleAgentConfig(role, townRoot, rigPath)
		} else {
			rc = ResolveAgentConfig(townRoot, rigPath)
		}
	} else {
		// For town-level agents (mayor, deacon), prefer GT_ROOT from envVars
		// (set by AgentEnv) over cwd detection. This ensures role_agents config
		// is respected even when the daemon runs outside the town hierarchy.
		townRoot = envVars["GT_ROOT"]
		if townRoot == "" {
			var err error
			townRoot, err = findTownRootFromCwd()
			if err != nil {
				// Can't find town root from cwd - but if agentOverride is specified,
				// try to use the preset directly. This allows `gt deacon start --agent codex`
				// to work even when run from outside the town directory.
				if agentOverride != "" {
					if preset := GetAgentPresetByName(agentOverride); preset != nil {
						rc = RuntimeConfigFromPreset(AgentPreset(agentOverride))
					} else {
						return "", fmt.Errorf("agent '%s' not found", agentOverride)
					}
				} else {
					rc = DefaultRuntimeConfig()
				}
			}
		}
		if rc == nil {
			if agentOverride != "" {
				var resolveErr error
				rc, _, resolveErr = ResolveAgentConfigWithOverride(townRoot, "", agentOverride)
				if resolveErr != nil {
					return "", resolveErr
				}
			} else if role != "" {
				rc = ResolveRoleAgentConfig(role, townRoot, "")
			} else {
				rc = ResolveAgentConfig(townRoot, "")
			}
		}
	}

	// Ensure Claude agents get --settings when their settings directory
	// differs from the session working directory. This must run for ALL
	// resolution paths (including agent overrides) — previously only the
	// non-override ResolveRoleAgentConfig path included it, causing hooks
	// to silently not fire for polecats launched with --agent.
	rc = withRoleSettingsFlag(rc, role, rigPath)

	// Apply exec wrapper from rig/town settings if not already set on the resolved config.
	if len(rc.ExecWrapper) == 0 {
		rc.ExecWrapper = resolveExecWrapper(rigPath)
	}

	// Copy env vars to avoid mutating caller map
	resolvedEnv := make(map[string]string, len(envVars)+2)
	for k, v := range envVars {
		resolvedEnv[k] = v
	}
	// Add GT_ROOT so agents can find town-level resources (formulas, etc.)
	if townRoot != "" {
		resolvedEnv["GT_ROOT"] = townRoot
	}
	if rc.Session != nil && rc.Session.SessionIDEnv != "" {
		resolvedEnv["GT_SESSION_ID_ENV"] = rc.Session.SessionIDEnv
	}
	// Record agent name so IsAgentAlive can detect the running process.
	// Explicit override takes priority; fall back to resolved agent name.
	agentForProcess := rc.ResolvedAgent
	if agentOverride != "" {
		resolvedEnv["GT_AGENT"] = agentOverride
		agentForProcess = agentOverride
	} else if rc.ResolvedAgent != "" {
		resolvedEnv["GT_AGENT"] = rc.ResolvedAgent
	}
	// Set GT_PROCESS_NAMES for accurate liveness detection of custom agents.
	processNamesOverride := ResolveProcessNames(agentForProcess, rc.Command)
	resolvedEnv["GT_PROCESS_NAMES"] = strings.Join(processNamesOverride, ",")
	// Merge agent-specific env vars (e.g., OPENCODE_PERMISSION for yolo mode)
	for k, v := range rc.Env {
		resolvedEnv[k] = v
	}

	SanitizeAgentEnv(resolvedEnv, envVars)

	var cmd string
	if runtime.GOOS == "windows" {
		// Write env vars + agent command to a temp .ps1 script to avoid
		// send-keys line length limits in psmux.
		var scriptLines []string
		keys := make([]string, 0, len(resolvedEnv))
		for k := range resolvedEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			scriptLines = append(scriptLines, fmt.Sprintf("$env:%s=%s", k, psQuote(resolvedEnv[k])))
		}

		var agentCmd string
		if len(rc.ExecWrapper) > 0 {
			agentCmd = strings.Join(rc.ExecWrapper, " ") + " "
		}
		if prompt != "" {
			agentCmd += "& " + rc.BuildCommandWithPrompt(prompt)
		} else {
			agentCmd += "& " + rc.BuildCommand()
		}
		scriptLines = append(scriptLines, agentCmd)

		townRoot := resolvedEnv["GT_ROOT"]
		if townRoot == "" {
			townRoot = os.TempDir()
		}
		scriptDir := filepath.Join(townRoot, "daemon", "scripts")
		_ = os.MkdirAll(scriptDir, 0755)
		role := resolvedEnv["GT_ROLE"]
		if role == "" {
			role = "agent"
		}
		safeRole := strings.ReplaceAll(role, "/", "-")
		scriptPath := filepath.Join(scriptDir, safeRole+"-startup.ps1")
		scriptContent := strings.Join(scriptLines, "\n") + "\n"
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
			cmd = strings.Join(scriptLines, "; ")
		} else {
			cmd = "& " + psQuote(scriptPath)
		}
	} else {
		// Build environment export prefix (POSIX shell)
		var exports []string
		for k, v := range resolvedEnv {
			exports = append(exports, fmt.Sprintf("%s=%s", k, ShellQuote(v)))
		}
		sort.Strings(exports)

		if len(exports) > 0 {
			cmd = "exec env " + strings.Join(exports, " ") + " "
		}

		if len(rc.ExecWrapper) > 0 {
			cmd += strings.Join(rc.ExecWrapper, " ") + " "
		}

		if prompt != "" {
			cmd += rc.BuildCommandWithPrompt(prompt)
		} else {
			cmd += rc.BuildCommand()
		}
	}

	return cmd, nil
}

// BuildStartupCommandFromConfig builds a startup command from a complete AgentEnvConfig.
// Use this (instead of Build*StartupCommand helpers) when you need full OTEL context:
// Issue (gt.issue), Topic (gt.topic), SessionName (gt.session), etc.
// The rigPath, prompt, and agentOverride are passed through directly.
func BuildStartupCommandFromConfig(cfg AgentEnvConfig, rigPath, prompt, agentOverride string) (string, error) {
	envVars := AgentEnv(cfg)
	return BuildStartupCommandWithAgentOverride(envVars, rigPath, prompt, agentOverride)
}

// BuildAgentStartupCommand is a convenience function for starting agent sessions.
// It uses AgentEnv to set all standard environment variables.
// For rig-level roles (witness, refinery), pass the rig name and rigPath.
// For town-level roles (mayor, deacon, boot), pass empty rig and rigPath, but provide townRoot.
func BuildAgentStartupCommand(role, rig, townRoot, rigPath, prompt string) string {
	envVars := AgentEnv(AgentEnvConfig{
		Role:     role,
		Rig:      rig,
		TownRoot: townRoot,
		Prompt:   prompt,
	})
	return BuildStartupCommand(envVars, rigPath, prompt)
}

// BuildAgentStartupCommandWithAgentOverride is like BuildAgentStartupCommand, but uses agentOverride if non-empty.
func BuildAgentStartupCommandWithAgentOverride(role, rig, townRoot, rigPath, prompt, agentOverride string) (string, error) {
	envVars := AgentEnv(AgentEnvConfig{
		Role:     role,
		Rig:      rig,
		TownRoot: townRoot,
		Prompt:   prompt,
	})
	return BuildStartupCommandWithAgentOverride(envVars, rigPath, prompt, agentOverride)
}

// BuildPolecatStartupCommand builds the startup command for a polecat.
// Sets GT_ROLE, GT_RIG, GT_POLECAT, BD_ACTOR, GIT_AUTHOR_NAME, and GT_ROOT.
func BuildPolecatStartupCommand(rigName, polecatName, rigPath, prompt string) string {
	var townRoot string
	if rigPath != "" {
		townRoot = filepath.Dir(rigPath)
	}
	envVars := AgentEnv(AgentEnvConfig{
		Role:      constants.RolePolecat,
		Rig:       rigName,
		AgentName: polecatName,
		TownRoot:  townRoot,
		Prompt:    prompt,
	})
	return BuildStartupCommand(envVars, rigPath, prompt)
}

// BuildPolecatStartupCommandWithAgentOverride is like BuildPolecatStartupCommand, but uses agentOverride if non-empty.
func BuildPolecatStartupCommandWithAgentOverride(rigName, polecatName, rigPath, prompt, agentOverride string) (string, error) {
	var townRoot string
	if rigPath != "" {
		townRoot = filepath.Dir(rigPath)
	}
	envVars := AgentEnv(AgentEnvConfig{
		Role:      constants.RolePolecat,
		Rig:       rigName,
		AgentName: polecatName,
		TownRoot:  townRoot,
		Prompt:    prompt,
	})
	return BuildStartupCommandWithAgentOverride(envVars, rigPath, prompt, agentOverride)
}

// BuildCrewStartupCommand builds the startup command for a crew member.
// Sets GT_ROLE, GT_RIG, GT_CREW, BD_ACTOR, GIT_AUTHOR_NAME, and GT_ROOT.
func BuildCrewStartupCommand(rigName, crewName, rigPath, prompt string) string {
	var townRoot string
	if rigPath != "" {
		townRoot = filepath.Dir(rigPath)
	}
	envVars := AgentEnv(AgentEnvConfig{
		Role:      constants.RoleCrew,
		Rig:       rigName,
		AgentName: crewName,
		TownRoot:  townRoot,
		Prompt:    prompt,
	})
	return BuildStartupCommand(envVars, rigPath, prompt)
}

// BuildCrewStartupCommandWithAgentOverride is like BuildCrewStartupCommand, but uses agentOverride if non-empty.
func BuildCrewStartupCommandWithAgentOverride(rigName, crewName, rigPath, prompt, agentOverride string) (string, error) {
	var townRoot string
	if rigPath != "" {
		townRoot = filepath.Dir(rigPath)
	}
	envVars := AgentEnv(AgentEnvConfig{
		Role:      constants.RoleCrew,
		Rig:       rigName,
		AgentName: crewName,
		TownRoot:  townRoot,
		Prompt:    prompt,
	})
	return BuildStartupCommandWithAgentOverride(envVars, rigPath, prompt, agentOverride)
}

// resolveExecWrapper loads the exec_wrapper from rig settings.
// ExecWrapper is a deployment-level setting (sandbox/container) that wraps the agent binary.
// It is independent of agent choice — exitbox wraps Claude, Codex, or any other runtime.
func resolveExecWrapper(rigPath string) []string {
	if rigPath != "" {
		if rigSettings, err := LoadRigSettings(RigSettingsPath(rigPath)); err == nil && rigSettings != nil {
			if rigSettings.Runtime != nil && len(rigSettings.Runtime.ExecWrapper) > 0 {
				return rigSettings.Runtime.ExecWrapper
			}
		}
	}
	return nil
}

// ExpectedPaneCommands returns tmux pane command names that indicate the runtime is running.
// Claude can report as "node" (older versions) or "claude" (newer versions).
// Other runtimes typically report their executable name.
func ExpectedPaneCommands(rc *RuntimeConfig) []string {
	if rc == nil || rc.Command == "" {
		return nil
	}
	if filepath.Base(rc.Command) == "claude" {
		return []string{"node", "claude"}
	}
	return []string{filepath.Base(rc.Command)}
}

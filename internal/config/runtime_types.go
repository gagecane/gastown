// Runtime (LLM agent) configuration types and builders. Extracted from types.go.
//
// These types define how Gas Town invokes LLM agent binaries (claude, codex,
// gemini, etc.) and normalize defaults from the agent preset registry in
// agents.go. Helpers like BuildCommand and BuildArgsWithPrompt are also
// defined here since they are methods on RuntimeConfig.
package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// RuntimeConfig represents LLM runtime configuration for agent sessions.
// This allows switching between different LLM backends (claude, aider, etc.)
// without modifying startup code.
type RuntimeConfig struct {
	// Provider selects runtime-specific defaults and integration behavior.
	// Known values: "claude", "codex", "generic". Default: "claude".
	Provider string `json:"provider,omitempty"`

	// Command is the CLI command to invoke (e.g., "claude", "aider").
	// Default: "claude"
	Command string `json:"command,omitempty"`

	// Args are additional command-line arguments.
	// Default: ["--dangerously-skip-permissions"] for built-in agents.
	// Empty array [] means no args (not "use defaults").
	Args []string `json:"args"`

	// Env are environment variables to set when starting the agent.
	// These are merged with the standard GT_* variables.
	// Used for agent-specific configuration like OPENCODE_PERMISSION.
	Env map[string]string `json:"env,omitempty"`

	// InitialPrompt is an optional first message to send after startup.
	// For claude, this is passed as the prompt argument.
	// Empty by default (hooks handle context).
	InitialPrompt string `json:"initial_prompt,omitempty"`

	// PromptMode controls how prompts are passed to the runtime.
	// Supported values: "arg" (append prompt arg), "none" (ignore prompt).
	// Default: "arg" for claude/generic, "none" for codex.
	PromptMode string `json:"prompt_mode,omitempty"`

	// Session config controls environment integration for runtime session IDs.
	Session *RuntimeSessionConfig `json:"session,omitempty"`

	// Hooks config controls runtime hook installation (if supported).
	Hooks *RuntimeHooksConfig `json:"hooks,omitempty"`

	// Tmux config controls process detection and readiness heuristics.
	Tmux *RuntimeTmuxConfig `json:"tmux,omitempty"`

	// Instructions controls the per-workspace instruction file name.
	Instructions *RuntimeInstructionsConfig `json:"instructions,omitempty"`

	// ACP configures ACP (Agent Communication Protocol) support.
	// When set, the agent can run in ACP mode. If nil, ACP support is
	// determined by matching the Command to a known preset with ACP config.
	ACP *ACPConfig `json:"acp,omitempty"`

	// ExecWrapper is a command prefix inserted between environment variables
	// and the agent binary in the startup command. Used for sandboxed execution.
	// Example: ["exitbox", "run", "--profile=gastown-polecat", "--"]
	// Produces: exec env VAR=val ... exitbox run --profile=gastown-polecat -- claude ...
	ExecWrapper []string `json:"exec_wrapper,omitempty"`

	// ResolvedAgent is the agent name that was resolved during config lookup.
	// Set by ResolveRoleAgentConfig / resolveAgentConfigInternal so that
	// BuildStartupCommand can export GT_AGENT for process detection.
	// Not serialized — this is a runtime-only field.
	ResolvedAgent string `json:"-"`
}

// RuntimeSessionConfig configures how Gas Town discovers runtime session IDs.
type RuntimeSessionConfig struct {
	// SessionIDEnv is the environment variable set by the runtime to identify a session.
	// Default: "CLAUDE_SESSION_ID" for claude, empty for codex/generic.
	SessionIDEnv string `json:"session_id_env,omitempty"`

	// ConfigDirEnv is the environment variable that selects a runtime account/config dir.
	// Default: "CLAUDE_CONFIG_DIR" for claude, empty for codex/generic.
	ConfigDirEnv string `json:"config_dir_env,omitempty"`
}

// RuntimeHooksConfig configures runtime hook installation.
type RuntimeHooksConfig struct {
	// Provider controls which hook templates to install: "claude", "opencode", "copilot", or "none".
	Provider string `json:"provider,omitempty"`

	// Dir is the settings directory (e.g., ".claude").
	Dir string `json:"dir,omitempty"`

	// SettingsFile is the settings file name (e.g., "settings.json").
	SettingsFile string `json:"settings_file,omitempty"`

	// Informational indicates the hooks provider installs instructions files only,
	// not executable lifecycle hooks. When true, Gas Town sends startup fallback
	// commands (gt prime) via nudge since hooks won't run automatically.
	// Defaults to false (backwards compatible with claude/opencode which have real hooks).
	Informational bool `json:"informational,omitempty"`
}

// RuntimeTmuxConfig controls tmux heuristics for detecting runtime readiness.
type RuntimeTmuxConfig struct {
	// ProcessNames are tmux pane commands that indicate the runtime is running.
	ProcessNames []string `json:"process_names,omitempty"`

	// ReadyPromptPrefix is the prompt prefix to detect readiness (e.g., "> ").
	ReadyPromptPrefix string `json:"ready_prompt_prefix,omitempty"`

	// ReadyDelayMs is a fixed delay used when prompt detection is unavailable.
	ReadyDelayMs int `json:"ready_delay_ms,omitempty"`
}

// RuntimeInstructionsConfig controls the name of the role instruction file.
type RuntimeInstructionsConfig struct {
	// File is the instruction filename (e.g., "CLAUDE.md", "AGENTS.md").
	File string `json:"file,omitempty"`
}

// DefaultRuntimeConfig returns a RuntimeConfig with sensible defaults.
func DefaultRuntimeConfig() *RuntimeConfig {
	return normalizeRuntimeConfig(&RuntimeConfig{Provider: "claude"})
}

// BuildCommand returns the full command line string.
// For use with tmux SendKeys and respawn-pane, where the string is
// interpreted by the user's shell. Args containing shell-special
// characters (e.g., brackets in "sonnet[1m]") are quoted to prevent
// glob expansion.
func (rc *RuntimeConfig) BuildCommand() string {
	resolved := normalizeRuntimeConfig(rc)

	cmd := resolved.Command
	args := resolved.Args

	// Combine command and args, quoting any that contain shell metacharacters
	if len(args) > 0 {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = ShellQuote(a)
		}
		return cmd + " " + strings.Join(quoted, " ")
	}
	return cmd
}

// BuildCommandWithPrompt returns the full command line with an initial prompt.
// If the config has an InitialPrompt, it's appended as a quoted argument.
// If prompt is provided, it overrides the config's InitialPrompt.
// For opencode, uses --prompt flag; for other agents, uses positional argument.
func (rc *RuntimeConfig) BuildCommandWithPrompt(prompt string) string {
	resolved := normalizeRuntimeConfig(rc)
	base := resolved.BuildCommand()

	// Use provided prompt or fall back to config
	p := prompt
	if p == "" {
		p = resolved.InitialPrompt
	}

	if p == "" || resolved.PromptMode == "none" {
		return base
	}

	// OpenCode requires --prompt flag for initial prompt in interactive mode.
	// Positional argument causes opencode to exit immediately.
	// Match both "opencode" and full paths like "/home/user/.opencode/bin/opencode".
	if resolved.Command == "opencode" || filepath.Base(resolved.Command) == "opencode" {
		return base + " --prompt " + quoteForShell(p)
	}

	// Copilot requires -i flag for initial prompt in interactive mode.
	if resolved.Command == "copilot" || filepath.Base(resolved.Command) == "copilot" {
		return base + " -i " + quoteForShell(p)
	}

	// Gemini requires -i (--prompt-interactive) to auto-execute the prompt
	// while staying in interactive mode. Positional args populate the input
	// field but don't execute, and -p runs headless (exits after completion).
	if resolved.Command == "gemini" || filepath.Base(resolved.Command) == "gemini" {
		return base + " -i " + quoteForShell(p)
	}

	// Quote the prompt for shell safety (positional arg for claude and others)
	return base + " " + quoteForShell(p)
}

// BuildArgsWithPrompt returns the runtime command and args suitable for exec.
func (rc *RuntimeConfig) BuildArgsWithPrompt(prompt string) []string {
	resolved := normalizeRuntimeConfig(rc)
	args := append([]string{resolved.Command}, resolved.Args...)

	p := prompt
	if p == "" {
		p = resolved.InitialPrompt
	}

	if p != "" && resolved.PromptMode != "none" {
		switch resolved.Command {
		case "opencode":
			args = append(args, "--prompt", p)
		case "copilot", "gemini":
			args = append(args, "-i", p)
		default:
			args = append(args, p)
		}
	}

	return args
}

func normalizeRuntimeConfig(rc *RuntimeConfig) *RuntimeConfig {
	if rc == nil {
		rc = &RuntimeConfig{}
	}

	// Shallow copy to avoid mutating the input
	copy := *rc
	rc = &copy

	// Deep copy nested structs to avoid shared references
	if rc.Session != nil {
		s := *rc.Session
		rc.Session = &s
	}
	if rc.Hooks != nil {
		h := *rc.Hooks
		rc.Hooks = &h
	}
	if rc.Tmux != nil {
		t := *rc.Tmux
		rc.Tmux = &t
	}
	if rc.Instructions != nil {
		i := *rc.Instructions
		rc.Instructions = &i
	}

	if rc.Provider == "" {
		rc.Provider = "claude"
	}

	if rc.Command == "" {
		rc.Command = defaultRuntimeCommand(rc.Provider)
	}

	if rc.Args == nil {
		rc.Args = defaultRuntimeArgs(rc.Provider)
	}

	if rc.PromptMode == "" {
		rc.PromptMode = defaultPromptMode(rc.Provider)
	}

	if rc.Session == nil {
		rc.Session = &RuntimeSessionConfig{}
	}

	if rc.Session.SessionIDEnv == "" {
		rc.Session.SessionIDEnv = defaultSessionIDEnv(rc.Provider)
	}

	if rc.Session.ConfigDirEnv == "" {
		rc.Session.ConfigDirEnv = defaultConfigDirEnv(rc.Provider)
	}

	if rc.Hooks == nil {
		rc.Hooks = &RuntimeHooksConfig{}
	}

	if rc.Hooks.Provider == "" {
		rc.Hooks.Provider = defaultHooksProvider(rc.Provider)
	}

	if rc.Hooks.Dir == "" {
		rc.Hooks.Dir = defaultHooksDir(rc.Provider)
	}

	if rc.Hooks.SettingsFile == "" {
		rc.Hooks.SettingsFile = defaultHooksFile(rc.Provider)
	}

	// Set informational flag for providers whose "hooks" are instructions files,
	// not executable lifecycle hooks. This tells startup fallback logic to send
	// gt prime via nudge since hooks won't run automatically.
	if !rc.Hooks.Informational {
		rc.Hooks.Informational = defaultHooksInformational(rc.Provider)
	}

	if rc.Tmux == nil {
		rc.Tmux = &RuntimeTmuxConfig{}
	}

	if rc.Tmux.ProcessNames == nil {
		rc.Tmux.ProcessNames = defaultProcessNames(rc.Provider, rc.Command)
	}

	if rc.Tmux.ReadyPromptPrefix == "" {
		rc.Tmux.ReadyPromptPrefix = defaultReadyPromptPrefix(rc.Provider)
	}

	if rc.Tmux.ReadyDelayMs == 0 {
		rc.Tmux.ReadyDelayMs = defaultReadyDelayMs(rc.Provider)
	}

	if rc.Instructions == nil {
		rc.Instructions = &RuntimeInstructionsConfig{}
	}

	if rc.Instructions.File == "" {
		rc.Instructions.File = defaultInstructionsFile(rc.Provider)
	}

	return rc
}

func defaultRuntimeCommand(provider string) string {
	if provider == "generic" {
		return ""
	}
	if preset := GetAgentPresetByName(provider); preset != nil {
		cmd := preset.Command
		// Resolve claude path for Claude preset (handles alias installations)
		if preset.Name == AgentClaude && cmd == "claude" {
			return resolveClaudePath()
		}
		return cmd
	}
	return resolveClaudePath() // fallback for unknown providers
}

// resolveClaudePath finds the claude binary, checking PATH first then common installation locations.
// This handles the case where claude is installed as an alias (not in PATH) which doesn't work
// in non-interactive shells spawned by tmux.
func resolveClaudePath() string {
	// First, try to find claude in PATH
	if path, err := exec.LookPath("claude"); err == nil {
		return path
	}

	// Check common Claude Code installation locations
	home, err := os.UserHomeDir()
	if err != nil {
		return "claude" // Fall back to bare command
	}

	// Standard Claude Code installation path
	claudePath := filepath.Join(home, ".claude", "local", "claude")
	if _, err := os.Stat(claudePath); err == nil {
		return claudePath
	}

	// Fall back to bare command (might work if PATH is set differently in tmux)
	return "claude"
}

func defaultRuntimeArgs(provider string) []string {
	if preset := GetAgentPresetByName(provider); preset != nil && preset.Args != nil {
		return append([]string(nil), preset.Args...) // copy to avoid mutation
	}
	return nil
}

func defaultPromptMode(provider string) string {
	if preset := GetAgentPresetByName(provider); preset != nil && preset.PromptMode != "" {
		return preset.PromptMode
	}
	return "arg"
}

func defaultSessionIDEnv(provider string) string {
	if preset := GetAgentPresetByName(provider); preset != nil {
		return preset.SessionIDEnv
	}
	return ""
}

func defaultConfigDirEnv(provider string) string {
	if preset := GetAgentPresetByName(provider); preset != nil {
		return preset.ConfigDirEnv
	}
	return ""
}

func defaultHooksProvider(provider string) string {
	if preset := GetAgentPresetByName(provider); preset != nil && preset.HooksProvider != "" {
		return preset.HooksProvider
	}
	return "none"
}

func defaultHooksDir(provider string) string {
	if preset := GetAgentPresetByName(provider); preset != nil {
		return preset.HooksDir
	}
	return ""
}

func defaultHooksFile(provider string) string {
	if preset := GetAgentPresetByName(provider); preset != nil {
		return preset.HooksSettingsFile
	}
	return ""
}

// defaultHooksInformational returns true for providers whose hooks are instructions
// files only (not executable lifecycle hooks). For these providers, Gas Town sends
// startup fallback commands (gt prime) via nudge since hooks won't auto-run.
func defaultHooksInformational(provider string) bool {
	if preset := GetAgentPresetByName(provider); preset != nil {
		return preset.HooksInformational
	}
	return false
}

func defaultProcessNames(provider, command string) []string {
	if preset := GetAgentPresetByName(provider); preset != nil && len(preset.ProcessNames) > 0 {
		return append([]string(nil), preset.ProcessNames...) // copy to avoid mutation
	}
	if command != "" {
		return []string{filepath.Base(command)}
	}
	return nil
}

func defaultReadyPromptPrefix(provider string) string {
	if preset := GetAgentPresetByName(provider); preset != nil {
		return preset.ReadyPromptPrefix
	}
	return ""
}

func defaultReadyDelayMs(provider string) int {
	if preset := GetAgentPresetByName(provider); preset != nil {
		return preset.ReadyDelayMs
	}
	return 0
}

func defaultInstructionsFile(provider string) string {
	if preset := GetAgentPresetByName(provider); preset != nil && preset.InstructionsFile != "" {
		return preset.InstructionsFile
	}
	return "AGENTS.md"
}

// quoteForShell quotes a string for safe shell usage.
func quoteForShell(s string) string {
	if runtime.GOOS == "windows" {
		// PowerShell: use single quotes (no interpolation). Double embedded single quotes.
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	// POSIX shell: wrap in double quotes, escaping special characters.
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	escaped = strings.ReplaceAll(escaped, "`", "\\`")
	escaped = strings.ReplaceAll(escaped, "$", `\$`)
	return `"` + escaped + `"`
}

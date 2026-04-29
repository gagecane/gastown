package cmd

// handoff_restart.go — restart command + environment variable building used
// by the handoff command. Split out of handoff.go to isolate the relatively
// large buildRestartCommandWithOpts plumbing (gu-a1q).
//
// These helpers assemble the shell command that tmux respawn-pane will run
// when a session is cycled. They read agent/role/rig configuration and
// produce an env-prefixed command string suitable for `tmux respawn-pane`.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// claudeEnvVars lists the Claude-related environment variables to propagate
// during handoff. These vars aren't inherited by tmux respawn-pane's fresh shell.
var claudeEnvVars = []string{
	// Claude API and config
	"ANTHROPIC_API_KEY",
	"CLAUDE_CODE_USE_BEDROCK",
	// AWS vars for Bedrock
	"AWS_PROFILE",
	"AWS_REGION",
	// OTEL telemetry — propagate so Claude keeps sending metrics after handoff
	// (tmux respawn-pane starts a fresh shell that doesn't inherit these)
	"CLAUDE_CODE_ENABLE_TELEMETRY",
	"OTEL_METRICS_EXPORTER",
	"OTEL_METRIC_EXPORT_INTERVAL",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	"OTEL_LOGS_EXPORTER",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
	"OTEL_LOG_TOOL_DETAILS",
	"OTEL_LOG_TOOL_CONTENT",
	"OTEL_LOG_USER_PROMPTS",
	"OTEL_RESOURCE_ATTRIBUTES",
	// bd telemetry — so `bd` calls inside Claude emit to VictoriaMetrics/Logs
	"BD_OTEL_METRICS_URL",
	"BD_OTEL_LOGS_URL",
	// GT telemetry source vars — needed to recompute derived vars after handoff
	"GT_OTEL_METRICS_URL",
	"GT_OTEL_LOGS_URL",
}

// buildRestartCommand creates the command to run when respawning a session's pane.
// This needs to be the actual command to execute (e.g., claude), not a session attach command.
// The command includes a cd to the correct working directory for the role.
//
// buildRestartCommandOpts controls restart command generation.
type buildRestartCommandOpts struct {
	// ContinueSession adds --continue and omits the beacon prompt,
	// so the agent resumes its previous conversation silently.
	ContinueSession bool
	// ContinuePrompt overrides the default continuation prompt when
	// ContinueSession is true. If empty, falls back to a generic
	// continuation message.
	ContinuePrompt string
}

func buildRestartCommand(sessionName string) (string, error) {
	return buildRestartCommandWithOpts(sessionName, buildRestartCommandOpts{})
}

func buildRestartCommandWithOpts(sessionName string, opts buildRestartCommandOpts) (string, error) {
	// Detect town root from current directory
	townRoot := detectTownRootFromCwd()
	if townRoot == "" {
		return "", fmt.Errorf("cannot detect town root - run from within a Gas Town workspace")
	}

	// Determine the working directory for this session type
	workDir, err := sessionWorkDir(sessionName, townRoot)
	if err != nil {
		return "", err
	}

	// Parse the session name to get the identity (used for GT_ROLE and beacon)
	identity, err := session.ParseSessionName(sessionName)
	if err != nil {
		return "", fmt.Errorf("cannot parse session name %q: %w", sessionName, err)
	}
	gtRole := identity.GTRole()
	simpleRole := config.ExtractSimpleRole(gtRole)

	// Derive rigPath from session identity for --settings flag resolution
	rigPath := ""
	if identity.Rig != "" {
		rigPath = filepath.Join(townRoot, identity.Rig)
	}

	// Build startup beacon for predecessor discovery via /resume.
	// When ContinueSession is set, use a continuation prompt instead of
	// the full handoff beacon — the agent resumes its previous context.
	beacon := ""
	if opts.ContinueSession {
		if opts.ContinuePrompt != "" {
			beacon = opts.ContinuePrompt
		} else {
			beacon = "Your account was rotated to avoid a rate limit. Continue your previous task."
		}
	} else if isPatrolRole(simpleRole) {
		// Patrol roles (refinery, witness, deacon) must re-enter their patrol
		// loop on handoff, not "wait for instructions." Without this, idle
		// patrol agents cycle through handoff→prime→no-work→handoff burning
		// CPU and tokens indefinitely. The patrol instruction ensures they
		// reach the await-event idle state in their burn-or-loop step.
		beacon = session.BuildStartupPrompt(session.BeaconConfig{
			Recipient: identity.BeaconAddress(),
			Sender:    "self",
			Topic:     "patrol",
		}, "Run `"+cli.Name()+" prime --hook` and begin patrol.")
	} else {
		beacon = session.FormatStartupBeacon(session.BeaconConfig{
			Recipient: identity.BeaconAddress(),
			Sender:    "self",
			Topic:     "handoff",
		})
	}

	// For respawn-pane, we:
	// 1. cd to the right directory (role's canonical home)
	// 2. export GT_ROLE and BD_ACTOR so role detection works correctly
	// 3. export Claude-related env vars (not inherited by fresh shell)
	// 4. run claude with the startup beacon (triggers immediate context loading)
	// Use exec to ensure clean process replacement.
	//
	// Check if current session is using a non-default agent (GT_AGENT env var).
	// If so, preserve it across handoff by using the override variant.
	// Fall back to tmux session environment if process env doesn't have it,
	// since exec env vars may not propagate through all agent runtimes.
	currentAgent, agentInEnv := os.LookupEnv("GT_AGENT")
	if !agentInEnv {
		// GT_AGENT not in process env at all — try tmux session environment
		// as fallback, since exec env vars may not propagate through all runtimes.
		t := tmux.NewTmux()
		if val, err := t.GetEnvironment(sessionName, "GT_AGENT"); err == nil && val != "" {
			currentAgent = val
		}
	}
	var runtimeCmd string
	if currentAgent != "" {
		var err error
		runtimeCmd, err = config.GetRuntimeCommandWithPromptAndAgentOverride(rigPath, beacon, currentAgent)
		if err != nil {
			return "", fmt.Errorf("resolving agent config: %w", err)
		}
	} else if simpleRole != "" {
		// Preserve role_agents model selection across self-handoff by resolving
		// runtime command via role-aware config (instead of default-agent lookup).
		runtimeCmd = config.ResolveRoleAgentConfig(simpleRole, townRoot, rigPath).BuildCommandWithPrompt(beacon)
	} else {
		runtimeCmd = config.GetRuntimeCommandWithPrompt(rigPath, beacon)
	}

	// Add --continue flag to resume the most recent session.
	// Note: runtimeCmd starts with the command name (e.g., "claude --settings ..."),
	// not "exec claude" — the "exec" prefix is added later in the Sprintf.
	if opts.ContinueSession {
		// Handle both Unix ("claude ") and Windows ("claude.exe ") binary names
		if n := strings.Replace(runtimeCmd, "claude.exe ", "claude.exe --continue ", 1); n != runtimeCmd {
			runtimeCmd = n
		} else {
			runtimeCmd = strings.Replace(runtimeCmd, "claude ", "claude --continue ", 1)
		}
	}

	// Build environment variables map — role vars first, then Claude vars.
	// Uses config.PrependEnv for OS-aware export syntax (bash export on
	// Unix, $env: on Windows).
	envMap := make(map[string]string)
	var agentEnv map[string]string // agent config Env (rc.toml [agents.X.env])
	if gtRole != "" {
		// When GT_AGENT is set, resolve config with the override so we pick up
		// the active agent's env (e.g., NODE_OPTIONS from [agents.X.env]).
		// Otherwise, fall back to role-based resolution.
		var runtimeConfig *config.RuntimeConfig
		if currentAgent != "" {
			rc, _, err := config.ResolveAgentConfigWithOverride(townRoot, rigPath, currentAgent)
			if err == nil {
				runtimeConfig = rc
			} else {
				runtimeConfig = config.ResolveRoleAgentConfig(simpleRole, townRoot, rigPath)
			}
		} else if simpleRole != "" {
			runtimeConfig = config.ResolveRoleAgentConfig(simpleRole, townRoot, rigPath)
		} else {
			runtimeConfig = config.ResolveAgentConfig(townRoot, rigPath)
		}
		agentEnv = runtimeConfig.Env
		envMap["GT_ROLE"] = gtRole
		envMap["BD_ACTOR"] = gtRole
		envMap["GIT_AUTHOR_NAME"] = gtRole
		if runtimeConfig.Session != nil && runtimeConfig.Session.SessionIDEnv != "" {
			envMap["GT_SESSION_ID_ENV"] = runtimeConfig.Session.SessionIDEnv
		}
	}

	// Propagate GT_ROOT so subsequent handoffs can use it as fallback
	// when cwd-based detection fails (broken state recovery)
	envMap["GT_ROOT"] = townRoot

	// Preserve GT_AGENT across handoff so agent override persists
	if currentAgent != "" {
		envMap["GT_AGENT"] = currentAgent
	}

	// Preserve GT_PROCESS_NAMES across handoff for accurate liveness detection.
	// Without this, custom agents that shadow built-in presets (e.g., custom
	// "codex" running "opencode") would revert to GT_AGENT-based lookup after
	// handoff, causing false liveness failures.
	if processNames := os.Getenv("GT_PROCESS_NAMES"); processNames != "" {
		envMap["GT_PROCESS_NAMES"] = processNames
	} else if currentAgent != "" {
		resolved := config.ResolveProcessNames(currentAgent, "")
		envMap["GT_PROCESS_NAMES"] = strings.Join(resolved, ",")
	}

	// Add Claude-related env vars from current environment
	for _, name := range claudeEnvVars {
		if val := os.Getenv(name); val != "" {
			envMap[name] = val
		}
	}

	// Clear NODE_OPTIONS to prevent debugger flags (e.g., --inspect from VSCode)
	// from being inherited through tmux into Claude's Node.js runtime.
	// When the agent's runtime config explicitly sets NODE_OPTIONS (e.g., for
	// memory tuning via --max-old-space-size in rc.toml [agents.X.env]), export
	// that value so it survives handoff. Otherwise clear it.
	// Note: agentEnv is intentionally nil when gtRole is empty (non-role handoffs),
	// which causes the nil map lookup to return ("", false) — clearing NODE_OPTIONS.
	if val, hasNodeOpts := agentEnv["NODE_OPTIONS"]; hasNodeOpts {
		envMap["NODE_OPTIONS"] = val
	} else {
		envMap["NODE_OPTIONS"] = ""
	}

	// Build the full command with OS-appropriate env prefix
	var cdPrefix string
	if runtime.GOOS == "windows" {
		cdPrefix = fmt.Sprintf("cd %s; ", workDir)
	} else {
		cdPrefix = fmt.Sprintf("cd %s && ", workDir)
	}

	var execPrefix string
	if runtime.GOOS != "windows" {
		execPrefix = "exec "
	}

	envCmd := config.PrependEnv(execPrefix+runtimeCmd, envMap)
	return cdPrefix + envCmd, nil
}

// updateSessionEnvForHandoff updates the tmux session environment with the
// agent name and process names for liveness detection. IsAgentAlive reads
// GT_PROCESS_NAMES from the tmux session env (via tmux show-environment), not
// from shell exports in the pane. Without this, post-handoff liveness checks
// would use stale values from the previous agent.
func updateSessionEnvForHandoff(t *tmux.Tmux, sessionName, agentOverride string) {
	// Resolve current agent using the same priority as buildRestartCommandWithAgent
	var currentAgent string
	if agentOverride != "" {
		currentAgent = agentOverride
	} else {
		currentAgent = os.Getenv("GT_AGENT")
		if currentAgent == "" {
			if val, err := t.GetEnvironment(sessionName, "GT_AGENT"); err == nil && val != "" {
				currentAgent = val
			}
		}
	}

	if currentAgent == "" {
		return
	}

	// Update GT_AGENT in session env
	_ = t.SetEnvironment(sessionName, "GT_AGENT", currentAgent)

	// Resolve and update GT_PROCESS_NAMES in session env
	// When switching agents, recompute from config. When preserving, use env value.
	var processNames string
	if agentOverride != "" {
		// Agent is changing — resolve config to get the command for process name resolution
		townRoot := detectTownRootFromCwd()
		if townRoot != "" {
			identity, err := session.ParseSessionName(sessionName)
			rigPath := ""
			if err == nil && identity.Rig != "" {
				rigPath = filepath.Join(townRoot, identity.Rig)
			}
			rc, _, err := config.ResolveAgentConfigWithOverride(townRoot, rigPath, currentAgent)
			if err == nil {
				resolved := config.ResolveProcessNames(currentAgent, rc.Command)
				processNames = strings.Join(resolved, ",")
			}
		}
	}
	if processNames == "" {
		// Preserve existing value or compute from current agent
		if pn := os.Getenv("GT_PROCESS_NAMES"); pn != "" {
			processNames = pn
		} else {
			resolved := config.ResolveProcessNames(currentAgent, "")
			processNames = strings.Join(resolved, ",")
		}
	}

	_ = t.SetEnvironment(sessionName, "GT_PROCESS_NAMES", processNames)
}

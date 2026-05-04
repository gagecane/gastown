// Package config provides configuration loading and environment variable management.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// IdentityEnvVars are agent identity env vars that must not leak across
// process or session boundaries. Used by daemon sanitization (clearing
// inherited vars), tmux global cleanup, and prime session env repair.
// See GH#3006.
var IdentityEnvVars = []string{
	"GT_ROLE", "GT_RIG", "GT_CREW", "GT_POLECAT", "GT_DOG_NAME",
	"GT_SESSION", "GT_AGENT", "BD_ACTOR", "GIT_AUTHOR_NAME", "BEADS_AGENT_NAME",
}

// AgentEnvConfig specifies the configuration for generating agent environment variables.
// This is the single source of truth for all agent environment configuration.
type AgentEnvConfig struct {
	// Role is the agent role: mayor, deacon, witness, refinery, crew, polecat, dog, boot
	Role string

	// Rig is the rig name (empty for town-level agents like mayor/deacon)
	Rig string

	// AgentName is the specific agent name (empty for singletons like witness/refinery)
	// For polecats, this is the polecat name. For crew, this is the crew member name.
	AgentName string

	// TownRoot is the root of the Gas Town workspace.
	// Sets GT_ROOT environment variable.
	TownRoot string

	// RuntimeConfigDir is the optional CLAUDE_CONFIG_DIR path
	RuntimeConfigDir string

	// SessionIDEnv is the environment variable name that holds the session ID.
	// Sets GT_SESSION_ID_ENV so the runtime knows where to find the session ID.
	SessionIDEnv string

	// Agent is the agent override (e.g., "codex", "gemini").
	// If set, GT_AGENT is written to the tmux session table via SetEnvironment
	// so that IsAgentAlive and waitForPolecatReady can read it via GetEnvironment.
	// Without this, GetEnvironment returns empty (tmux show-environment reads the
	// session table, not the process env set via exec env in the startup command).
	Agent string

	// Prompt is the initial startup prompt/beacon given to the agent.
	// When set, the first line (truncated) is added as gt.prompt to OTEL_RESOURCE_ATTRIBUTES
	// so logs can be correlated to the specific task the agent was working on.
	Prompt string

	// Issue is the molecule/bead ID being worked (e.g., "gt-abc12").
	// Added as gt.issue to OTEL_RESOURCE_ATTRIBUTES for filtering by ticket.
	Issue string

	// Topic is the beacon topic describing why the session was started.
	// Examples: "assigned", "patrol", "start", "restart", "handoff".
	// Added as gt.topic to OTEL_RESOURCE_ATTRIBUTES for filtering by work type.
	Topic string

	// SessionName is the tmux session name for this agent (e.g., "hq-mayor", "gt-witness").
	// Added as gt.session to OTEL_RESOURCE_ATTRIBUTES so all Claude logs from a
	// single GT session can be correlated, and as GT_SESSION env var.
	SessionName string
}

// AgentEnv returns all environment variables for an agent based on the config.
// This is the single source of truth for agent environment variables.
func AgentEnv(cfg AgentEnvConfig) map[string]string {
	env := make(map[string]string)

	// Set role-specific variables
	// GT_ROLE is set in compound format (e.g., "beads/crew/jane") so that
	// beads can parse it without knowing about Gas Town role types.
	switch cfg.Role {
	case constants.RoleMayor:
		env["GT_ROLE"] = constants.RoleMayor
		env["BD_ACTOR"] = constants.RoleMayor
		env["GIT_AUTHOR_NAME"] = constants.RoleMayor

	case constants.RoleDeacon:
		env["GT_ROLE"] = constants.RoleDeacon
		env["BD_ACTOR"] = constants.RoleDeacon
		env["GIT_AUTHOR_NAME"] = constants.RoleDeacon

	case "boot":
		env["GT_ROLE"] = "deacon/boot"
		env["BD_ACTOR"] = "deacon-boot"
		env["GIT_AUTHOR_NAME"] = "boot"

	case constants.RoleWitness:
		env["GT_ROLE"] = fmt.Sprintf("%s/witness", cfg.Rig)
		env["GT_RIG"] = cfg.Rig
		env["BD_ACTOR"] = fmt.Sprintf("%s/witness", cfg.Rig)
		env["GIT_AUTHOR_NAME"] = fmt.Sprintf("%s/witness", cfg.Rig)

	case constants.RoleRefinery:
		env["GT_ROLE"] = fmt.Sprintf("%s/refinery", cfg.Rig)
		env["GT_RIG"] = cfg.Rig
		env["BD_ACTOR"] = fmt.Sprintf("%s/refinery", cfg.Rig)
		env["GIT_AUTHOR_NAME"] = fmt.Sprintf("%s/refinery", cfg.Rig)

	case constants.RolePolecat:
		env["GT_ROLE"] = fmt.Sprintf("%s/polecats/%s", cfg.Rig, cfg.AgentName)
		env["GT_RIG"] = cfg.Rig
		env["GT_POLECAT"] = cfg.AgentName
		env["BD_ACTOR"] = fmt.Sprintf("%s/polecats/%s", cfg.Rig, cfg.AgentName)
		env["GIT_AUTHOR_NAME"] = cfg.AgentName
		// Disable Dolt auto-commit for polecats. With branch-per-polecat,
		// individual commits are pointless — all changes merge at gt done time
		// via DOLT_MERGE. Without this, concurrent polecats cause manifest
		// contention leading to Dolt read-only mode (gt-5cc2p).
		env["BD_DOLT_AUTO_COMMIT"] = "off"

	case constants.RoleCrew:
		env["GT_ROLE"] = fmt.Sprintf("%s/crew/%s", cfg.Rig, cfg.AgentName)
		env["GT_RIG"] = cfg.Rig
		env["GT_CREW"] = cfg.AgentName
		env["BD_ACTOR"] = fmt.Sprintf("%s/crew/%s", cfg.Rig, cfg.AgentName)
		env["GIT_AUTHOR_NAME"] = cfg.AgentName

	case "dog":
		// Dogs are town-level workers with role_agents key "dog".
		// GT_ROLE must be set so startup command resolution can honor role_agents.dog.
		env["GT_ROLE"] = "dog"
		if cfg.AgentName != "" {
			env["GT_DOG_NAME"] = cfg.AgentName
			env["BD_ACTOR"] = fmt.Sprintf("deacon/dogs/%s", cfg.AgentName)
			env["GIT_AUTHOR_NAME"] = cfg.AgentName
		} else {
			env["BD_ACTOR"] = "dog"
			env["GIT_AUTHOR_NAME"] = "dog"
		}
	}

	// Only set GT_ROOT if provided
	// Empty values would override tmux session environment
	if cfg.TownRoot != "" {
		// Resolve symlinks so GT_ROOT is always the canonical path.
		// Without this, sessions started from /home/user/gt (symlink) get a
		// different GT_ROOT than sessions started from /local/home/user/gt
		// (real path), causing gt doctor env-vars warnings on every cycle.
		townRoot := cfg.TownRoot
		if resolved, err := filepath.EvalSymlinks(townRoot); err == nil {
			townRoot = resolved
		}
		env["GT_ROOT"] = townRoot
		// Prevent git from walking up to umbrella repo when running in rig worktrees.
		// This stops accidental commits to the umbrella when running git commands from
		// intermediate directories (e.g., polecats/) that don't have their own .git.
		env["GIT_CEILING_DIRECTORIES"] = townRoot
	}

	// Set BEADS_AGENT_NAME for polecat/crew (uses same format as BD_ACTOR)
	if cfg.Role == constants.RolePolecat || cfg.Role == constants.RoleCrew {
		env["BEADS_AGENT_NAME"] = fmt.Sprintf("%s/%s", cfg.Rig, cfg.AgentName)
	}

	// Add optional runtime config directory
	if cfg.RuntimeConfigDir != "" {
		env["CLAUDE_CONFIG_DIR"] = cfg.RuntimeConfigDir
	}

	// Add session ID env var name if provided
	if cfg.SessionIDEnv != "" {
		env["GT_SESSION_ID_ENV"] = cfg.SessionIDEnv
	}

	// Set GT_SESSION when a session name is provided, so gt commands and
	// cost reports can correlate activity to a specific tmux session.
	if cfg.SessionName != "" {
		env["GT_SESSION"] = cfg.SessionName
	}

	// Set GT_AGENT when an agent override is in use.
	// This makes the override visible via tmux show-environment so that
	// IsAgentAlive and waitForPolecatReady use the correct process names.
	if cfg.Agent != "" {
		env["GT_AGENT"] = cfg.Agent
	}

	// Disable bd's per-repo JSONL auto-backup for all Gas Town agents.
	// bd auto-enables backup when a git remote exists, then force-adds
	// .beads/backup/ files (bypassing .gitignore) and commits/pushes them
	// to the project repo. In Gas Town, Dolt is the persistent data store
	// and the daemon provides centralized backup patrols (dolt_backup,
	// jsonl_git_backup), making per-repo backup redundant and harmful —
	// it pollutes rig git history on both main and feature branches.
	// See: https://github.com/steveyegge/beads/issues/2241
	env["BD_BACKUP_ENABLED"] = "false"

	// Clear NODE_OPTIONS to prevent debugger flags (e.g., --inspect from VSCode)
	// from being inherited through tmux into Claude's Node.js runtime.
	// This is the PRIMARY guard: setting it here (the single source of truth
	// for agent env) protects all AgentEnv-based paths automatically — tmux
	// SetEnvironment, EnvForExecCommand, PrependEnv. SanitizeAgentEnv provides
	// a SUPPLEMENTAL guard for non-AgentEnv paths (lifecycle default, handoff).
	// In BuildStartupCommand, rc.Env is merged after AgentEnv and can override
	// this empty value with intentional settings like --max-old-space-size.
	env["NODE_OPTIONS"] = ""

	// Resolve effort level from per-role config (role_effort in town/rig settings,
	// or cost-tier presets). Falls back to "high" when no config exists.
	// The CLAUDE_CODE_EFFORT_LEVEL env var is deprecated — effort is now configured
	// per-role through config, matching the pattern used for model selection.
	rigPath := ""
	if cfg.Rig != "" && cfg.TownRoot != "" {
		rigPath = filepath.Join(cfg.TownRoot, cfg.Rig)
	}
	effort := ResolveRoleEffort(cfg.Role, cfg.TownRoot, rigPath)
	if effort == "" {
		effort = "high"
	}
	env["CLAUDE_CODE_EFFORT_LEVEL"] = effort
	if shellEffort := os.Getenv("CLAUDE_CODE_EFFORT_LEVEL"); shellEffort != "" {
		fmt.Fprintf(os.Stderr,
			"notice: CLAUDE_CODE_EFFORT_LEVEL=%s env var is deprecated and ignored; "+
				"%s effort resolved to %q via config. "+
				"Set per-role effort with role_effort in settings or gt config cost-tier.\n",
			shellEffort, cfg.Role, effort)
	}

	// Clear CLAUDECODE to prevent nested session detection in Claude Code v2.x.
	// When gt sling is invoked from within a Claude Code session, CLAUDECODE=1
	// leaks through tmux's global environment into new polecat sessions, causing
	// Claude Code to refuse to start with a "nested sessions" error.
	// See: https://github.com/steveyegge/gastown/issues/1666
	env["CLAUDECODE"] = ""

	// Suppress interactive TUI prompts that block headless agent sessions.
	// Without these, the Claude CLI may render surveys, update notices, or other
	// prompts that capture the input loop and prevent the agent from processing
	// heartbeats or dispatched work. Root cause of the deacon crash-loop incident
	// (2026-05-03): feedback survey captured deacon's input for ~5 days (gs-4hk).
	//
	// CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY: the specific prompt that caused the
	//   crash-loop ("How is Claude doing this session? 1: Bad / 2: Fine / 3: Good")
	// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: suppresses auto-title Haiku calls,
	//   release notes fetching, and usage metrics non-essential to agent function
	// DISABLE_AUTOUPDATER: prevents auto-updater from rendering update prompts or
	//   starting background downloads that interfere with agent sessions
	env["CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY"] = "1"
	env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	env["DISABLE_AUTOUPDATER"] = "1"

	// Read daemon.json's env block fresh at call time so newly spawned sessions
	// pick up config updates without requiring a daemon restart (gu-kj7c). For
	// config-sourced vars (OTEL URLs, Dolt port/host, etc.), prefer this over
	// the daemon's os.Environ() snapshot which may be stale if daemon.json was
	// edited after the daemon started.
	daemonEnv := readDaemonJSONEnv(cfg.TownRoot)

	// Propagate Claude Code's own OTEL telemetry when GT telemetry is enabled.
	// Reuses the same VictoriaMetrics endpoint as gastown's telemetry so all
	// metrics (gt + claude) land in the same store.
	// Opt-in: only active when GT_OTEL_METRICS_URL is explicitly set.
	if metricsURL, ok := lookupConfigEnv(daemonEnv, "GT_OTEL_METRICS_URL"); ok {
		env["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
		env["OTEL_METRICS_EXPORTER"] = "otlp"
		env["OTEL_METRIC_EXPORT_INTERVAL"] = "1000"
		env["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"] = metricsURL
		// VictoriaMetrics rejects JSON encoding ("json encoding isn't supported
		// for opentelemetry format"). The Node.js OTEL SDK defaults to JSON;
		// force protobuf so the push succeeds.
		env["OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"] = "http/protobuf"
		// Mirror into bd's own var names so any `bd` call inside the Claude
		// session emits metrics/logs to the same VictoriaMetrics instance.
		env["BD_OTEL_METRICS_URL"] = metricsURL
		if logsURL, ok := lookupConfigEnv(daemonEnv, "GT_OTEL_LOGS_URL"); ok {
			env["BD_OTEL_LOGS_URL"] = logsURL
			// Claude Code supports OTLP log export; route to the same VictoriaLogs
			// instance. Uses protobuf (VictoriaLogs rejects JSON).
			env["OTEL_LOGS_EXPORTER"] = "otlp"
			env["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"] = logsURL
			env["OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"] = "http/protobuf"
			// Log tool usage details (which tools ran, their status).
			env["OTEL_LOG_TOOL_DETAILS"] = "true"
			// Log tool output content (e.g. gt prime stdout as it reaches Claude).
			env["OTEL_LOG_TOOL_CONTENT"] = "true"
			// Log user-turn messages (initial beacon + any human prompts to Claude).
			env["OTEL_LOG_USER_PROMPTS"] = "true"
		}

		// Attach GT context as OTEL resource attributes so Claude's metrics
		// can be correlated with gastown's own telemetry in VictoriaMetrics.
		// Claude Code's Node.js SDK picks up OTEL_RESOURCE_ATTRIBUTES automatically.
		var attrs []string
		if v := env["GT_ROLE"]; v != "" {
			attrs = append(attrs, "gt.role="+v)
		}
		if cfg.Rig != "" {
			attrs = append(attrs, "gt.rig="+cfg.Rig)
		}
		if v := env["BD_ACTOR"]; v != "" {
			attrs = append(attrs, "gt.actor="+v)
		}
		if cfg.AgentName != "" {
			attrs = append(attrs, "gt.agent="+cfg.AgentName)
		}
		if cfg.TownRoot != "" {
			attrs = append(attrs, "gt.town="+filepath.Base(cfg.TownRoot))
		}
		if cfg.Prompt != "" {
			attrs = append(attrs, "gt.prompt="+sanitizeOTELAttrValue(cfg.Prompt, 120))
		}
		if cfg.Issue != "" {
			attrs = append(attrs, "gt.issue="+sanitizeOTELAttrValue(cfg.Issue, 40))
		}
		if cfg.Topic != "" {
			attrs = append(attrs, "gt.topic="+sanitizeOTELAttrValue(cfg.Topic, 40))
		}
		if cfg.SessionName != "" {
			attrs = append(attrs, "gt.session="+sanitizeOTELAttrValue(cfg.SessionName, 80))
		}
		if len(attrs) > 0 {
			env["OTEL_RESOURCE_ATTRIBUTES"] = strings.Join(attrs, ",")
		}
	}

	// Inject Dolt server port so agents' direct bd invocations connect to
	// gt's central server instead of auto-starting rogue per-rig servers.
	// Without this, bd falls back to its own discovery (.beads/dolt-server.port
	// or auto-start), causing split-brain after reinstall/restart.
	//
	// Resolution: config file first, then daemon.json, then process env fallback.
	// daemon.json is checked before process env so newly spawned sessions pick up
	// fresh daemon.json values without requiring a daemon restart (gu-kj7c).
	if cfg.TownRoot != "" {
		if port := resolveDoltPort(cfg.TownRoot); port > 0 {
			portStr := strconv.Itoa(port)
			env["GT_DOLT_PORT"] = portStr
			env["BEADS_DOLT_PORT"] = portStr
		}
	}
	// Propagate GT_DOLT_PORT / BEADS_DOLT_PORT from daemon.json / process env
	// when not already resolved from config. This covers sessions where
	// TownRoot is empty or has no config.yaml (GH#2412).
	if _, ok := env["GT_DOLT_PORT"]; !ok {
		if v, ok := lookupConfigEnv(daemonEnv, "GT_DOLT_PORT"); ok {
			env["GT_DOLT_PORT"] = v
			// Also set BEADS_DOLT_PORT if not explicitly overridden.
			if _, hasBeads := lookupConfigEnv(daemonEnv, "BEADS_DOLT_PORT"); !hasBeads {
				env["BEADS_DOLT_PORT"] = v
			}
		}
	}
	if _, ok := env["BEADS_DOLT_PORT"]; !ok {
		if v, ok := lookupConfigEnv(daemonEnv, "BEADS_DOLT_PORT"); ok {
			env["BEADS_DOLT_PORT"] = v
		}
	}

	// Suppress bd's Dolt auto-start for all Gas Town agents (GH#2930).
	// Gas Town manages its own Dolt server (gt dolt start/stop). When the
	// server is momentarily unreachable (restart, journal hiccup), bd's
	// auto-start tries to launch a shadow server in the agent's .beads/dolt/
	// directory — which conflicts with the real server on the same port and
	// triggers an escalation flood loop. Dogs are especially affected because
	// their kennel's .beads/ has no explicit dolt_server_port in metadata.json.
	if cfg.TownRoot != "" {
		env["BEADS_DOLT_AUTO_START"] = "0"
	}

	// Propagate Dolt server host so bd doesn't fall back to 127.0.0.1 when
	// the server runs on a remote machine (e.g., mini2 over Tailscale).
	// Prefer daemon.json (fresh) over process env (may be stale).
	if _, ok := env["BEADS_DOLT_SERVER_HOST"]; !ok {
		if v, ok := lookupConfigEnv(daemonEnv, "BEADS_DOLT_SERVER_HOST"); ok {
			env["BEADS_DOLT_SERVER_HOST"] = v
		} else if v, ok := lookupConfigEnv(daemonEnv, "GT_DOLT_HOST"); ok {
			env["BEADS_DOLT_SERVER_HOST"] = v
		}
	}

	// Pass through cloud API credentials and provider configuration.
	// Only variables explicitly listed here are forwarded; all others are blocked for isolation.
	// Prefer daemon.json (fresh at call time) over the daemon's os.Environ()
	// snapshot so edits to daemon.json take effect without a daemon restart (gu-kj7c).
	for _, key := range []string{
		// Anthropic API (direct)
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		// ANTHROPIC_BASE_URL intentionally excluded — agents that need a custom
		// base URL (MiniMax, Groq, etc.) get it from their agent config's Env
		// block, not from the parent process. Passthrough caused cross-provider
		// contamination: a MiniMax deacon's base URL leaked into Claude polecats.
		"ANTHROPIC_CUSTOM_HEADERS",

		// Model selection
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",

		// AWS Bedrock
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_SKIP_BEDROCK_AUTH",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_REGION",
		"AWS_PROFILE",
		"AWS_BEARER_TOKEN_BEDROCK",
		"ANTHROPIC_SMALL_FAST_MODEL_AWS_REGION",

		// Microsoft Foundry
		"CLAUDE_CODE_USE_FOUNDRY",
		"CLAUDE_CODE_SKIP_FOUNDRY_AUTH",
		"ANTHROPIC_FOUNDRY_API_KEY",
		"ANTHROPIC_FOUNDRY_BASE_URL",
		"ANTHROPIC_FOUNDRY_RESOURCE",

		// Google Vertex AI
		"CLAUDE_CODE_USE_VERTEX",
		"CLAUDE_CODE_SKIP_VERTEX_AUTH",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
		"VERTEX_PROJECT",
		"VERTEX_LOCATION",
		"VERTEX_REGION_CLAUDE_3_5_HAIKU",
		"VERTEX_REGION_CLAUDE_3_7_SONNET",
		"VERTEX_REGION_CLAUDE_4_0_OPUS",
		"VERTEX_REGION_CLAUDE_4_0_SONNET",
		"VERTEX_REGION_CLAUDE_4_1_OPUS",

		// Proxy / network
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"NO_PROXY",

		// mTLS
		"CLAUDE_CODE_CLIENT_CERT",
		"CLAUDE_CODE_CLIENT_KEY",
		"CLAUDE_CODE_CLIENT_KEY_PASSPHRASE",
	} {
		if val, ok := lookupConfigEnv(daemonEnv, key); ok {
			env[key] = val
		}
	}

	return env
}

// sanitizeOTELAttrValue prepares a string for use as a value in OTEL_RESOURCE_ATTRIBUTES.
// It takes the first line only, replaces commas (which break key=value,key=value parsing),
// and truncates to maxLen bytes.
func sanitizeOTELAttrValue(s string, maxLen int) string {
	// First line only — beacons are multi-line; the first line is the structured header.
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	// Commas separate key=value pairs in OTEL_RESOURCE_ATTRIBUTES — replace with |.
	s = strings.ReplaceAll(s, ",", "|")
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}

// readDaemonJSONEnv reads the env block from mayor/daemon.json at call time.
// Returns an empty map (never nil) when the file is missing, unreadable, or
// has no env block. Callers should prefer values from this map over os.Getenv
// for config-sourced vars so spawned sessions inherit fresh config, not the
// stale daemon process env snapshot from daemon startup (gu-kj7c).
//
// Reading daemon.json fresh on every AgentEnv call is cheap (typically <1ms)
// and avoids requiring a daemon restart when daemon.json is edited. If
// townRoot is empty, returns an empty map — AgentEnv handles this gracefully
// by falling back to os.Getenv.
func readDaemonJSONEnv(townRoot string) map[string]string {
	if townRoot == "" {
		return map[string]string{}
	}
	daemonJSONPath := filepath.Join(townRoot, "mayor", "daemon.json")
	data, err := os.ReadFile(daemonJSONPath)
	if err != nil {
		return map[string]string{}
	}
	var daemonEnv struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &daemonEnv); err != nil {
		return map[string]string{}
	}
	if daemonEnv.Env == nil {
		return map[string]string{}
	}
	return daemonEnv.Env
}

// lookupConfigEnv returns the value for key, preferring daemon.json's env
// block (fresh at call time) over the process env (which may be stale if the
// daemon was started before daemon.json was edited). Returns ("", false) when
// the key is not set in either source.
//
// Use this for config-sourced vars (GT_DOLT_PORT, GT_OTEL_METRICS_URL, etc.)
// in AgentEnv so newly spawned sessions pick up fresh daemon.json values
// without requiring a daemon restart (gu-kj7c).
func lookupConfigEnv(daemonEnv map[string]string, key string) (string, bool) {
	if v, ok := daemonEnv[key]; ok && v != "" {
		return v, true
	}
	if v := os.Getenv(key); v != "" {
		return v, true
	}
	return "", false
}

// resolveDoltPort determines the Dolt server port for the given town root.
//
// Resolution order (mirrors doltserver.DefaultConfig without importing it):
//  1. .dolt-data/config.yaml listener.port (authoritative, machine-generated)
//  2. mayor/daemon.json env.GT_DOLT_PORT (fresh at call time)
//  3. GT_DOLT_PORT environment variable (daemon process env; may be stale)
//  4. 0 (caller should skip injection — DefaultPort 3307 is bd's own default)
//
// daemon.json is checked before the env variable because the daemon's
// os.Environ() snapshot can go stale after daemon.json is edited. Reading
// the file fresh ensures new spawns pick up config updates without a
// daemon restart (gu-kj7c).
//
// This avoids importing doltserver (which pulls in yaml, sql, mysql driver)
// by scanning the config.yaml line-by-line. The file is machine-generated by
// gt dolt start with a known format, so a simple line scan is safe.
func resolveDoltPort(townRoot string) int {
	// 1. Read from .dolt-data/config.yaml (authoritative)
	configPath := filepath.Join(townRoot, ".dolt-data", "config.yaml")
	if data, err := os.ReadFile(configPath); err == nil {
		if port := parsePortFromConfigYAML(data); port > 0 {
			return port
		}
	}

	// 2. daemon.json (fresh at call time — preferred over process env)
	daemonEnv := readDaemonJSONEnv(townRoot)
	if v, ok := daemonEnv["GT_DOLT_PORT"]; ok && v != "" {
		if port, err := strconv.Atoi(v); err == nil && port > 0 {
			return port
		}
	}

	// 3. Environment variable (daemon process env; may be stale)
	if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}

	return 0
}

// parsePortFromConfigYAML extracts the listener port from a Dolt config.yaml
// without a yaml dependency. The file is machine-generated by gt dolt start
// with the format:
//
//	listener:
//	  port: 3307
func parsePortFromConfigYAML(data []byte) int {
	lines := strings.Split(string(data), "\n")
	inListener := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "listener:" {
			inListener = true
			continue
		}
		if inListener {
			if strings.HasPrefix(trimmed, "port:") {
				portStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "port:"))
				if port, err := strconv.Atoi(portStr); err == nil {
					return port
				}
			}
			// Any non-indented line ends the listener block
			if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
				inListener = false
			}
		}
	}
	return 0
}

// AgentEnvSimple is a convenience function for simple role-based env var lookup.
// Use this when you only need role, rig, and agentName without advanced options.
func AgentEnvSimple(role, rig, agentName string) map[string]string {
	return AgentEnv(AgentEnvConfig{
		Role:      role,
		Rig:       rig,
		AgentName: agentName,
	})
}

// ShellQuote returns a shell-safe quoted string.
// Values containing special characters are wrapped in single quotes.
// Single quotes within the value are escaped using the '\” idiom.
func ShellQuote(s string) string {
	// Check if quoting is needed (contains shell special chars)
	needsQuoting := false
	for _, c := range s {
		switch c {
		case ' ', '\t', '\n', '"', '\'', '`', '$', '\\', '!', '*', '?',
			'[', ']', '{', '}', '(', ')', '<', '>', '|', '&', ';', '#':
			needsQuoting = true
		}
		if needsQuoting {
			break
		}
	}

	if !needsQuoting {
		return s
	}

	// Use single quotes, escaping any embedded single quotes
	// 'foo'\''bar' means: 'foo' + escaped-single-quote + 'bar'
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// psQuote quotes a value for use in PowerShell $env: assignments.
// Uses single quotes with embedded single quotes doubled ('').
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ExportPrefix builds an export statement prefix for shell commands.
// Returns a string like "export GT_ROLE=mayor BD_ACTOR=mayor && "
// The keys are sorted for deterministic output.
// Values containing special characters are properly shell-quoted.
func ExportPrefix(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}

	// Sort keys for deterministic output
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if runtime.GOOS == "windows" {
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("$env:%s=%s", k, psQuote(env[k])))
		}
		return strings.Join(parts, "; ") + "; "
	}

	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, ShellQuote(env[k])))
	}
	return "export " + strings.Join(parts, " ") + " && "
}

// BuildStartupCommandWithEnv builds a startup command with the given environment variables.
// This combines the export prefix with the agent command and optional prompt.
func BuildStartupCommandWithEnv(env map[string]string, agentCmd, prompt string) string {
	prefix := ExportPrefix(env)

	if prompt != "" {
		// Include prompt as argument to agent command
		return fmt.Sprintf("%s%s %q", prefix, agentCmd, prompt)
	}
	return prefix + agentCmd
}

// MergeEnv merges multiple environment maps, with later maps taking precedence.
func MergeEnv(maps ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			result[k] = v
		}
	}
	return result
}

// FilterEnv returns a new map with only the specified keys.
func FilterEnv(env map[string]string, keys ...string) map[string]string {
	result := make(map[string]string)
	for _, k := range keys {
		if v, ok := env[k]; ok {
			result[k] = v
		}
	}
	return result
}

// WithoutEnv returns a new map without the specified keys.
func WithoutEnv(env map[string]string, keys ...string) map[string]string {
	result := make(map[string]string)
	exclude := make(map[string]bool)
	for _, k := range keys {
		exclude[k] = true
	}
	for k, v := range env {
		if !exclude[k] {
			result[k] = v
		}
	}
	return result
}

// EnvForExecCommand returns os.Environ() with the given env vars appended.
// This is useful for setting cmd.Env on exec.Command.
func EnvForExecCommand(env map[string]string) []string {
	result := os.Environ()
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// EnvToSlice converts an env map to a slice of "K=V" strings.
// Useful for appending to os.Environ() manually.
func EnvToSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// ClaudeConfigDir resolves the Claude Code configuration directory.
// Resolution order:
//  1. CLAUDE_CONFIG_DIR env var (if set and non-empty)
//  2. $HOME/.claude (fallback)
func ClaudeConfigDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

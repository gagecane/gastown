package web

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
)

// FetchMayor returns the Mayor's current status.
func (f *LiveConvoyFetcher) FetchMayor() (*MayorStatus, error) {
	status := &MayorStatus{
		IsAttached: false,
	}

	// Get the actual mayor session name (e.g., "hq-mayor")
	mayorSessionName := session.MayorSessionName()

	// Check if mayor tmux session exists
	stdout, err := fetcherRunCmd(f.tmuxCmdTimeout, "tmux", f.tmuxArgs("list-sessions", "-F", "#{session_name}:#{session_activity}")...)
	if err != nil {
		// tmux not running or no sessions
		return status, nil
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, mayorSessionName+":") {
			status.IsAttached = true
			status.SessionName = mayorSessionName

			// Parse activity timestamp
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if activityTs, ok := parseActivityTimestamp(parts[1]); ok {
					age := time.Since(time.Unix(activityTs, 0))
					status.LastActivity = formatTimestamp(time.Unix(activityTs, 0))
					status.IsActive = age < f.mayorActiveThreshold
				}
			}
			break
		}
	}

	if status.IsAttached {
		status.Runtime = f.resolveMayorRuntime(mayorSessionName)
	}

	return status, nil
}

func (f *LiveConvoyFetcher) resolveMayorRuntime(sessionName string) string {
	if agentName, err := fetcherGetSessionEnv(sessionName, "GT_AGENT"); err == nil && strings.TrimSpace(agentName) != "" {
		agentName = strings.TrimSpace(agentName)
		rc, _, resolveErr := config.ResolveAgentConfigWithOverride(f.townRoot, "", agentName)
		if resolveErr == nil {
			return runtimeLabelForRuntimeConfig(rc, agentName)
		}
		if roleRC := config.ResolveRoleAgentConfig(constants.RoleMayor, f.townRoot, ""); roleRC != nil && strings.TrimSpace(roleRC.ResolvedAgent) == agentName {
			return runtimeLabelForRuntimeConfig(roleRC, agentName)
		}
		return agentName
	}

	return runtimeLabelForRuntimeConfig(config.ResolveRoleAgentConfig(constants.RoleMayor, f.townRoot, ""), "")
}

func runtimeLabelForRuntimeConfig(rc *config.RuntimeConfig, fallback string) string {
	if rc == nil {
		if fallback != "" {
			return fallback
		}
		return "claude"
	}
	if fallback == "" {
		fallback = rc.ResolvedAgent
	}
	return runtimeLabelFromConfig(rc.Command, rc.Args, fallback)
}

func runtimeLabelFromConfig(command string, args []string, fallback string) string {
	command = strings.TrimSpace(command)
	cmd := ""
	if command != "" {
		cmd = strings.TrimSpace(filepath.Base(command))
	}
	if cmd == "" {
		cmd = fallback
	}
	if cmd == "" {
		cmd = "claude"
	}
	if cmd == "cgroup-wrap" && len(args) > 0 {
		cmd = filepath.Base(args[0])
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--model" || arg == "-m") && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "" {
			return cmd + "/" + stripModelSuffix(strings.TrimSpace(args[i+1]))
		}
		if strings.HasPrefix(arg, "--model=") {
			if v := strings.TrimSpace(strings.TrimPrefix(arg, "--model=")); v != "" {
				return cmd + "/" + stripModelSuffix(v)
			}
		}
		if strings.HasPrefix(arg, "-m=") {
			if v := strings.TrimSpace(strings.TrimPrefix(arg, "-m=")); v != "" {
				return cmd + "/" + stripModelSuffix(v)
			}
		}
	}

	return cmd
}

// stripModelSuffix removes bracketed context-window hints (e.g. "[1m]")
// from model names so the dashboard label stays human-readable.
// "sonnet[1m]" → "sonnet", "opus" → "opus".
func stripModelSuffix(model string) string {
	if idx := strings.Index(model, "["); idx > 0 {
		return model[:idx]
	}
	return model
}

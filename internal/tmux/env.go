package tmux

import (
	"errors"
	"fmt"
	"strings"
)

// SetExitEmpty controls the tmux exit-empty server option.
// When on (default), the server exits when there are no sessions.
// When off, the server stays running even with no sessions.
// This is useful during shutdown to prevent the server from exiting
// when all Gas Town sessions are killed but the user has no other sessions.
func (t *Tmux) SetExitEmpty(on bool) error {
	value := "on"
	if !on {
		value = "off"
	}
	_, err := t.run("set-option", "-g", "exit-empty", value)
	if errors.Is(err, ErrNoServer) {
		return nil // No server to configure
	}
	return err
}

// SetEnvironment sets an environment variable in the session.
func (t *Tmux) SetEnvironment(session, key, value string) error {
	_, err := t.run("set-environment", "-t", session, key, value)
	return err
}

// GetEnvironment gets an environment variable from the session.
func (t *Tmux) GetEnvironment(session, key string) (string, error) {
	out, err := t.run("show-environment", "-t", session, key)
	if err != nil {
		return "", err
	}
	// psmux may return all environment variables instead of just the requested key.
	// Parse line-by-line and find the matching KEY=value line.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && parts[0] == key {
			return parts[1], nil
		}
	}
	// Fallback: if only one line, use it directly (standard tmux behavior)
	parts := strings.SplitN(strings.TrimSpace(out), "=", 2)
	if len(parts) == 2 && parts[0] == key {
		return parts[1], nil
	}
	return "", fmt.Errorf("environment variable %s not found in session %s", key, session)
}

// SetGlobalEnvironment sets an environment variable in the tmux global environment.
// Unlike SetEnvironment, this is not scoped to a session — it applies server-wide.
func (t *Tmux) SetGlobalEnvironment(key, value string) error {
	_, err := t.run("set-environment", "-g", key, value)
	return err
}

// UnsetGlobalEnvironment removes an environment variable from the tmux global environment.
func (t *Tmux) UnsetGlobalEnvironment(key string) error {
	_, err := t.run("set-environment", "-g", "-u", key)
	return err
}

// GetGlobalEnvironment gets an environment variable from the tmux global environment.
func (t *Tmux) GetGlobalEnvironment(key string) (string, error) {
	out, err := t.run("show-environment", "-g", key)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && parts[0] == key {
			return parts[1], nil
		}
	}
	return "", fmt.Errorf("global environment variable %s not found", key)
}

// GetAllEnvironment returns all environment variables for a session.
func (t *Tmux) GetAllEnvironment(session string) (map[string]string, error) {
	out, err := t.run("show-environment", "-t", session)
	if err != nil {
		return nil, err
	}

	env := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") {
			// Skip empty lines and unset markers (lines starting with -)
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	return env, nil
}

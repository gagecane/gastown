package doltserver

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// EnsureDoltIdentity configures dolt global identity (user.name, user.email)
// if not already set. Copies values from git config as a sensible default.
// This must run before InitRig and Start, since dolt init requires identity.
func EnsureDoltIdentity() error {
	// Check each field independently to avoid creating duplicates with --add.
	// Distinguish "key not found" (exit code 1, empty output) from dolt crashes.
	needName, err := doltConfigMissing("user.name")
	if err != nil {
		return fmt.Errorf("probing dolt user.name: %w", err)
	}
	needEmail, err := doltConfigMissing("user.email")
	if err != nil {
		return fmt.Errorf("probing dolt user.email: %w", err)
	}

	if !needName && !needEmail {
		return nil // already configured
	}

	// Copy missing fields from git global config.
	// We read --global only (not repo-local) to avoid silently persisting
	// a repo-scoped override into dolt's permanent global config.
	if needName {
		nameCmd := exec.Command("git", "config", "--global", "user.name")
		setProcessGroup(nameCmd)
		gitName, err := nameCmd.Output()
		if err != nil || len(bytes.TrimSpace(gitName)) == 0 {
			return fmt.Errorf("dolt identity not configured and git user.name not available; run: dolt config --global --add user.name \"Your Name\"")
		}
		if err := setDoltGlobalConfig("user.name", strings.TrimSpace(string(gitName))); err != nil {
			return fmt.Errorf("failed to set dolt user.name: %w", err)
		}
	}

	if needEmail {
		emailCmd := exec.Command("git", "config", "--global", "user.email")
		setProcessGroup(emailCmd)
		gitEmail, err := emailCmd.Output()
		if err != nil || len(bytes.TrimSpace(gitEmail)) == 0 {
			return fmt.Errorf("dolt identity not configured and git user.email not available; run: dolt config --global --add user.email \"you@example.com\"")
		}
		if err := setDoltGlobalConfig("user.email", strings.TrimSpace(string(gitEmail))); err != nil {
			return fmt.Errorf("failed to set dolt user.email: %w", err)
		}
	}

	return nil
}

// doltConfigMissing checks whether a dolt global config key is unset.
// Returns (true, nil) for missing keys, (false, nil) for present keys,
// and (false, error) when dolt itself fails unexpectedly.
func doltConfigMissing(key string) (bool, error) {
	cmd := exec.Command("dolt", "config", "--global", "--get", key)
	setProcessGroup(cmd)
	out, err := cmd.Output()
	if err == nil {
		// Command succeeded — key exists if output is non-empty
		return len(bytes.TrimSpace(out)) == 0, nil
	}
	// dolt config --get exits 1 for missing keys with no stderr.
	// Any other failure (crash, permission error) is unexpected.
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return true, nil // key not found — expected
	}
	return false, fmt.Errorf("dolt config --global --get %s: %w", key, err)
}

// setDoltGlobalConfig idempotently sets a dolt global config key.
// Uses --unset then --add to avoid duplicate entries from repeated calls.
func setDoltGlobalConfig(key, value string) error {
	// Remove existing value (ignore error — key may not exist yet)
	unsetCmd := exec.Command("dolt", "config", "--global", "--unset", key)
	setProcessGroup(unsetCmd)
	_ = unsetCmd.Run()
	addCmd := exec.Command("dolt", "config", "--global", "--add", key, value)
	setProcessGroup(addCmd)
	return addCmd.Run()
}

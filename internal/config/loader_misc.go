package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// findTownRootFromCwd locates the town root by walking up from cwd.
// It looks for the mayor/town.json marker file.
// Returns empty string and no error if not found (caller should use defaults).
func findTownRootFromCwd() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting cwd: %w", err)
	}

	absDir, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}

	const marker = "mayor/town.json"

	current := absDir
	for {
		if _, err := os.Stat(filepath.Join(current, marker)); err == nil {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("town root not found (no %s marker)", marker)
		}
		current = parent
	}
}

// ExtractSimpleRole extracts the simple role name from a GT_ROLE value.
// GT_ROLE can be:
//   - Simple: "mayor", "deacon"
//   - Compound: "rig/witness", "rig/refinery", "rig/crew/name", "rig/polecats/name"
//
// For compound format, returns the role segment (second part).
// For simple format, returns the role as-is.
func ExtractSimpleRole(gtRole string) string {
	if gtRole == "" {
		return ""
	}
	parts := strings.Split(gtRole, "/")
	switch len(parts) {
	case 1:
		// Simple format: "mayor", "deacon"
		return parts[0]
	case 2:
		// "rig/witness", "rig/refinery"
		return parts[1]
	case 3:
		// "rig/crew/name" → "crew", "rig/polecats/name" → "polecat"
		role := parts[1]
		if role == "polecats" {
			return constants.RolePolecat
		}
		return role
	default:
		return gtRole
	}
}

// GetDefaultFormula returns the default formula for a rig from settings/config.json.
// Returns empty string if no default is configured.
// rigPath is the path to the rig directory (e.g., ~/gt/gastown).
func GetDefaultFormula(rigPath string) string {
	settingsPath := RigSettingsPath(rigPath)
	settings, err := LoadRigSettings(settingsPath)
	if err != nil {
		return ""
	}
	if settings.Workflow == nil {
		return ""
	}
	return settings.Workflow.DefaultFormula
}

// GetRigPrefix returns the beads prefix for a rig from rigs.json.
// Falls back to "gt" if the rig isn't found or has no prefix configured.
// townRoot is the path to the town directory (e.g., ~/gt).
func GetRigPrefix(townRoot, rigName string) string {
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return "gt" // fallback
	}

	entry, ok := rigsConfig.Rigs[rigName]
	if !ok {
		return "gt" // fallback
	}

	if entry.BeadsConfig == nil || entry.BeadsConfig.Prefix == "" {
		return "gt" // fallback
	}

	// Strip trailing hyphen if present (prefix stored as "gt-" but used as "gt")
	prefix := entry.BeadsConfig.Prefix
	return strings.TrimSuffix(prefix, "-")
}

// AllRigPrefixes returns a sorted list of all rig beads prefixes from rigs.json.
// Trailing hyphens are stripped (e.g. "gt-" becomes "gt").
// Returns nil on error (caller should handle the fallback).
func AllRigPrefixes(townRoot string) []string {
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil
	}
	var prefixes []string
	for _, entry := range rigsConfig.Rigs {
		if entry.BeadsConfig != nil && entry.BeadsConfig.Prefix != "" {
			prefixes = append(prefixes, strings.TrimSuffix(entry.BeadsConfig.Prefix, "-"))
		}
	}
	sort.Strings(prefixes)
	return prefixes
}

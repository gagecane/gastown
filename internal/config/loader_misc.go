package config

import (
	"path/filepath"
	"sort"
	"strings"
)

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

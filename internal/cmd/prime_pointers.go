// Package: internal/cmd (gastown)
// File: prime_pointers.go
//
// This file implements config-driven pointer injection for gt prime.
// Apply to: github.com/gagecane/gastown :: internal/cmd/prime_pointers.go
//
// Integration point: Add `outputPointers(ctx)` call in outputRoleContext()
// between outputContextFile(ctx) and outputHandoffContent(ctx).
//
// Depends on: gopkg.in/yaml.v3 (already in go.mod)

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PointerEntry represents a single pointer line emitted during gt prime.
// Pointers are brief one-line references (commands, links, bead IDs) that
// agents should be aware of but don't need to act on during startup.
type PointerEntry struct {
	// Label is the human-readable prefix (e.g., "Pipeline guardian").
	Label string `yaml:"label"`
	// Command is the actionable part (e.g., "bd show hq-casc-guardian-digest").
	Command string `yaml:"command"`
	// Roles limits which roles see this pointer (empty = all roles).
	Roles []string `yaml:"roles,omitempty"`
}

// PointersConfig represents the gt-prime-pointers.yaml file.
type PointersConfig struct {
	// Pointers is the list of pointer entries to emit during gt prime.
	Pointers []PointerEntry `yaml:"pointers"`
}

// pointerConfigFileName is the filename gt prime searches for.
const pointerConfigFileName = "gt-prime-pointers.yaml"

// outputPointers loads and emits pointer entries from gt-prime-pointers.yaml.
// Resolution order (first found wins):
//  1. Project directory: <workDir>/configs/gt-prime-pointers.yaml
//  2. Project .beads configs: <workDir>/.beads/configs/gt-prime-pointers.yaml
//  3. Rig root: <townRoot>/<rig>/configs/gt-prime-pointers.yaml
//  4. Town root: <townRoot>/configs/gt-prime-pointers.yaml
//
// Multiple files are NOT merged — only the first found is used.
// This keeps behavior predictable and avoids ordering surprises.
func outputPointers(ctx RoleContext) {
	cfg := loadPointers(ctx)
	if cfg == nil || len(cfg.Pointers) == 0 {
		explain(true, "Pointers: no gt-prime-pointers.yaml found or empty")
		return
	}

	role := string(ctx.Role)
	var filtered []PointerEntry
	for _, p := range cfg.Pointers {
		if len(p.Roles) == 0 || containsRole(p.Roles, role) {
			filtered = append(filtered, p)
		}
	}

	if len(filtered) == 0 {
		explain(true, "Pointers: config found but no entries match current role")
		return
	}

	explain(true, fmt.Sprintf("Pointers: emitting %d pointer(s)", len(filtered)))
	fmt.Println()
	fmt.Println("## Quick Pointers")
	fmt.Println()
	for _, p := range filtered {
		if p.Command != "" {
			fmt.Printf("> **%s**: `%s`\n", p.Label, p.Command)
		} else {
			fmt.Printf("> **%s**\n", p.Label)
		}
	}
}

// loadPointers searches for gt-prime-pointers.yaml in the resolution order.
func loadPointers(ctx RoleContext) *PointersConfig {
	candidates := pointerConfigPaths(ctx)
	for _, path := range candidates {
		data, err := os.ReadFile(path) //nolint:gosec // G304: paths from trusted config dirs
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == "" {
			continue
		}
		var cfg PointersConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			fmt.Fprintf(os.Stderr, "prime: invalid %s at %s: %v\n", pointerConfigFileName, path, err)
			continue
		}
		explain(true, "Pointers: loaded from "+path)
		return &cfg
	}
	return nil
}

// pointerConfigPaths returns the ordered list of candidate paths for the pointer config.
func pointerConfigPaths(ctx RoleContext) []string {
	var paths []string
	if ctx.WorkDir != "" {
		paths = append(paths, filepath.Join(ctx.WorkDir, "configs", pointerConfigFileName))
		paths = append(paths, filepath.Join(ctx.WorkDir, ".beads", "configs", pointerConfigFileName))
	}
	if ctx.TownRoot != "" && ctx.Rig != "" {
		paths = append(paths, filepath.Join(ctx.TownRoot, ctx.Rig, "configs", pointerConfigFileName))
	}
	if ctx.TownRoot != "" {
		paths = append(paths, filepath.Join(ctx.TownRoot, "configs", pointerConfigFileName))
	}
	return paths
}

// containsRole checks if the given role is in the list (case-insensitive).
func containsRole(roles []string, role string) bool {
	for _, r := range roles {
		if strings.EqualFold(r, role) {
			return true
		}
	}
	return false
}

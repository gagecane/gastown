package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/workspace"
)

// checkRigNotParkedOrDocked checks if a rig is parked or docked and returns
// an error if so. This prevents starting agents on rigs that have been
// intentionally taken offline.
func checkRigNotParkedOrDocked(rigName string) error {
	townRoot, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	if IsRigParked(townRoot, rigName) {
		return fmt.Errorf("rig '%s' is parked - use 'gt rig unpark %s' first", rigName, rigName)
	}

	prefix := "gt"
	if r.Config != nil && r.Config.Prefix != "" {
		prefix = r.Config.Prefix
	}

	if IsRigDocked(townRoot, rigName, prefix) {
		return fmt.Errorf("rig '%s' is docked - use 'gt rig undock %s' first", rigName, rigName)
	}

	return nil
}

// getRig finds the town root and retrieves the specified rig.
// This is the common boilerplate extracted from get*Manager functions.
// Returns the town root path and rig instance.
func getRig(rigName string) (string, *rig.Rig, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigsConfigPath := constants.MayorRigsPath(townRoot)
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return "", nil, fmt.Errorf("rig '%s' not found", rigName)
	}

	return townRoot, r, nil
}

// hasRigBeadLabel checks if a rig's identity bead has a specific label.
// Returns false if the rig config or bead can't be loaded (safe default).
//
// Deprecated: kept as a thin wrapper for any direct callers inside the cmd
// package. New code should use internal/rig helpers directly.
func hasRigBeadLabel(townRoot, rigName, label string) bool {
	// Delegate to the shared implementation. The rig package doesn't expose
	// this as a public function (it's an internal helper), so we reproduce
	// the minimal bead lookup here by asking for the two labels we actually
	// care about.
	switch label {
	case rig.LabelStatusParked:
		return rig.IsRigParked(townRoot, rigName)
	case rig.LabelStatusDocked:
		prefix := rig.RigBeadsPrefix(townRoot, filepath.Join(townRoot, rigName), rigName)
		if prefix == "" {
			return false
		}
		return rig.IsRigDocked(townRoot, rigName, prefix)
	default:
		// Any other label check would require a full bead fetch. No
		// current caller asks for anything other than parked/docked;
		// add a new rig-package helper if that changes.
		return false
	}
}

// IsRigParkedOrDocked checks if a rig is parked or docked by any mechanism
// (wisp ephemeral state or persistent bead labels). Returns (blocked, reason).
// This is the single entry point for all dispatch paths (sling, convoy launch,
// convoy stage) to check rig availability.
//
// Parked vs docked asymmetry: parked state is checked in both the wisp layer
// (ephemeral, set by "gt rig park") and bead labels (persistent fallback for
// when wisp state is lost during cleanup). Docked state is bead-label only
// because "gt rig dock" never writes to wisp — it persists exclusively via
// the rig identity bead's status:docked label.
//
// Delegates to internal/rig.IsRigParkedOrDocked. Kept in cmd for back-compat
// with existing call sites.
func IsRigParkedOrDocked(townRoot, rigName string) (bool, string) {
	return rig.IsRigParkedOrDocked(townRoot, rigName)
}

// rigBeadsPrefix resolves a rig's beads prefix. Thin wrapper around the
// shared implementation.
func rigBeadsPrefix(townRoot, rigPath, rigName string) string {
	return rig.RigBeadsPrefix(townRoot, rigPath, rigName)
}

// getAllRigs discovers all rigs in the current Gas Town workspace.
// Returns the list of rigs and any error.
func getAllRigs() ([]*rig.Rig, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	rigs, err := rigMgr.DiscoverRigs()
	if err != nil {
		return nil, err
	}

	return rigs, nil
}

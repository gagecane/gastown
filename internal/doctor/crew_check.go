package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CrewStateCheck detects leftover crew worker state.json files from pre-migration
// gt versions. Since gu-kplt, crew metadata lives in the crew agent bead
// (gt-<rig>-crew-<name>) and <rig>/crew/<name>/state.json is obsolete. Leftover
// files are harmless but confusing (stale Name/Branch values), so Fix removes them.
type CrewStateCheck struct {
	FixableCheck
	staleFiles []staleCrewStateFile
}

type staleCrewStateFile struct {
	path     string
	rigName  string
	crewName string
}

// NewCrewStateCheck creates a new crew state check.
func NewCrewStateCheck() *CrewStateCheck {
	return &CrewStateCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "crew-state",
				CheckDescription: "Detect leftover crew state.json files (migrated to beads in gu-kplt)",
				CheckCategory:    CategoryCleanup,
			},
		},
	}
}

// Run looks for obsolete state.json files in crew directories.
func (c *CrewStateCheck) Run(ctx *CheckContext) *CheckResult {
	c.staleFiles = nil

	crewDirs := c.findAllCrewDirs(ctx.TownRoot)
	if len(crewDirs) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No crew workspaces found",
		}
	}

	var details []string
	for _, cd := range crewDirs {
		stateFile := filepath.Join(cd.path, "state.json")
		if _, err := os.Stat(stateFile); err != nil {
			continue // No leftover file, all good
		}
		c.staleFiles = append(c.staleFiles, staleCrewStateFile{
			path:     stateFile,
			rigName:  cd.rigName,
			crewName: cd.crewName,
		})
		details = append(details, fmt.Sprintf("%s/%s: leftover state.json (safe to delete)", cd.rigName, cd.crewName))
	}

	if len(c.staleFiles) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("All %d crew workspace(s) clean (no legacy state.json)", len(crewDirs)),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d crew workspace(s) with leftover state.json from pre-migration gt", len(c.staleFiles)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to remove obsolete state.json files",
	}
}

// Fix removes leftover state.json files identified during Run.
func (c *CrewStateCheck) Fix(ctx *CheckContext) error {
	if len(c.staleFiles) == 0 {
		return nil
	}

	var lastErr error
	for _, sf := range c.staleFiles {
		if err := os.Remove(sf.path); err != nil && !os.IsNotExist(err) {
			lastErr = fmt.Errorf("%s/%s: %w", sf.rigName, sf.crewName, err)
		}
	}

	return lastErr
}

type crewDir struct {
	path     string
	rigName  string
	crewName string
}

// findAllCrewDirs finds all crew directories in the workspace.
func (c *CrewStateCheck) findAllCrewDirs(townRoot string) []crewDir {
	var dirs []crewDir

	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return dirs
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "mayor" {
			continue
		}

		rigName := entry.Name()
		crewPath := filepath.Join(townRoot, rigName, "crew")

		crewEntries, err := os.ReadDir(crewPath)
		if err != nil {
			continue
		}

		for _, crew := range crewEntries {
			if !crew.IsDir() || strings.HasPrefix(crew.Name(), ".") {
				continue
			}
			dirs = append(dirs, crewDir{
				path:     filepath.Join(crewPath, crew.Name()),
				rigName:  rigName,
				crewName: crew.Name(),
			})
		}
	}

	return dirs
}

// CrewWorktreeCheck detects stale cross-rig worktrees in crew directories.
// Cross-rig worktrees are created by `gt worktree <rig>` and live in crew/
// with names like `<source-rig>-<crewname>`. They should be cleaned up when
// no longer needed to avoid confusion with regular crew workspaces.
type CrewWorktreeCheck struct {
	FixableCheck
	staleWorktrees []staleWorktree
}

type staleWorktree struct {
	path      string
	rigName   string
	name      string
	sourceRig string
	crewName  string
}

// NewCrewWorktreeCheck creates a new crew worktree check.
func NewCrewWorktreeCheck() *CrewWorktreeCheck {
	return &CrewWorktreeCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "crew-worktrees",
				CheckDescription: "Detect stale cross-rig worktrees in crew directories",
				CheckCategory:    CategoryCleanup,
			},
		},
	}
}

// Run checks for cross-rig worktrees that may need cleanup.
func (c *CrewWorktreeCheck) Run(ctx *CheckContext) *CheckResult {
	c.staleWorktrees = nil

	worktrees := c.findCrewWorktrees(ctx.TownRoot)
	if len(worktrees) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No cross-rig worktrees in crew directories",
		}
	}

	c.staleWorktrees = worktrees
	var details []string
	for _, wt := range worktrees {
		details = append(details, fmt.Sprintf("%s/crew/%s (from %s/crew/%s)",
			wt.rigName, wt.name, wt.sourceRig, wt.crewName))
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d cross-rig worktree(s) in crew directories", len(worktrees)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to remove, or use 'gt crew remove <name> --purge'",
	}
}

// Fix removes stale cross-rig worktrees.
func (c *CrewWorktreeCheck) Fix(ctx *CheckContext) error {
	if len(c.staleWorktrees) == 0 {
		return nil
	}

	var lastErr error
	for _, wt := range c.staleWorktrees {
		// Use git worktree remove to properly clean up
		mayorRigPath := filepath.Join(ctx.TownRoot, wt.rigName, "mayor", "rig")
		removeCmd := exec.Command("git", "worktree", "remove", "--force", wt.path)
		removeCmd.Dir = mayorRigPath
		if output, err := removeCmd.CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("%s/crew/%s: %v (%s)", wt.rigName, wt.name, err, strings.TrimSpace(string(output)))
		}
	}

	return lastErr
}

// findCrewWorktrees finds cross-rig worktrees in crew directories.
// These are worktrees with hyphenated names (e.g., "beads-dave") that
// indicate they were created via `gt worktree` for cross-rig work.
func (c *CrewWorktreeCheck) findCrewWorktrees(townRoot string) []staleWorktree {
	var worktrees []staleWorktree

	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return worktrees
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "mayor" {
			continue
		}

		rigName := entry.Name()
		crewPath := filepath.Join(townRoot, rigName, "crew")

		crewEntries, err := os.ReadDir(crewPath)
		if err != nil {
			continue
		}

		for _, crew := range crewEntries {
			if !crew.IsDir() || strings.HasPrefix(crew.Name(), ".") {
				continue
			}

			name := crew.Name()
			path := filepath.Join(crewPath, name)

			// Check if it's a worktree (has .git file, not directory)
			gitPath := filepath.Join(path, ".git")
			info, err := os.Stat(gitPath)
			if err != nil || info.IsDir() {
				// Not a worktree (regular clone or error)
				continue
			}

			// Check for hyphenated name pattern: <source-rig>-<crewname>
			// This indicates a cross-rig worktree created by `gt worktree`
			parts := strings.SplitN(name, "-", 2)
			if len(parts) != 2 {
				// Not a cross-rig worktree pattern
				continue
			}

			sourceRig := parts[0]
			crewName := parts[1]

			// Verify the source rig exists (sanity check)
			sourceRigPath := filepath.Join(townRoot, sourceRig)
			if _, err := os.Stat(sourceRigPath); os.IsNotExist(err) {
				// Source rig doesn't exist - definitely stale
			}

			worktrees = append(worktrees, staleWorktree{
				path:      path,
				rigName:   rigName,
				name:      name,
				sourceRig: sourceRig,
				crewName:  crewName,
			})
		}
	}

	return worktrees
}

package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Note: CrewStateCheck (state.json validation) was removed when crew workers
// migrated from on-disk state.json to beads-tracked agent beads (see gu-ykn).
// Crew presence is determined by directory existence and verified by
// AgentBeadsCheck (agent_beads_check.go). Worktree cleanup remains the
// responsibility of CrewWorktreeCheck below.

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

package cmd

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// rollbackSlingArtifactsFn is a seam for tests. Production uses rollbackSlingArtifacts.
var rollbackSlingArtifactsFn = rollbackSlingArtifacts

// Rollback seams allow tests to assert molecule-cleanup behavior without
// depending on full beads storage side effects.
var getBeadInfoForRollback = getBeadInfo
var collectExistingMoleculesForRollback = collectExistingMolecules
var burnExistingMoleculesForRollback = burnExistingMolecules

// restorePinnedBead re-pins a bead to its prior assignee after a rollback path
// unhooked it. Used when --force sling triggers rollback of a spawned polecat
// and the original bead was pinned — without this, the unhook step clears the
// pinned state.
func restorePinnedBead(townRoot, beadID, assignee string) {
	if townRoot == "" || beadID == "" {
		return
	}
	dir := beads.ResolveHookDir(townRoot, beadID, "")
	cmd := exec.Command("bd", "update", beadID, "--status=pinned", "--assignee="+assignee)
	if dir != "" {
		cmd.Dir = dir
	}
	if err := cmd.Run(); err != nil {
		fmt.Printf("  %s Could not restore pinned state for bead %s: %v\n", style.Dim.Render("Warning:"), beadID, err)
	} else {
		fmt.Printf("  %s Restored pinned state for bead %s\n", style.Dim.Render("○"), beadID)
	}
}

// rollbackSlingArtifacts cleans up artifacts left by a partial sling when session start fails.
// This prevents zombie polecats that block subsequent sling attempts with "bead already hooked".
// Cleanup is best-effort: each step logs warnings but continues to clean as much as possible.
func rollbackSlingArtifacts(spawnInfo *SpawnedPolecatInfo, beadID, hookWorkDir, convoyID string) {
	townRoot, err := workspace.FindFromCwdOrError()

	// 1. Burn any attached molecules from partial formula instantiation.
	// This clears attached_molecule metadata and closes stale wisps that
	// otherwise block subsequent sling attempts.
	// Some failure modes happen before any bead is hooked (e.g., wisp creation fails).
	if beadID != "" {
		if err != nil {
			fmt.Printf("  %s Could not find workspace to rollback bead %s: %v\n", style.Dim.Render("Warning:"), beadID, err)
		} else {
			info, infoErr := getBeadInfoForRollback(beadID)
			if infoErr != nil {
				fmt.Printf("  %s Could not inspect bead %s for stale molecules: %v\n", style.Dim.Render("Warning:"), beadID, infoErr)
			} else {
				existingMolecules := collectExistingMoleculesForRollback(info)
				if len(existingMolecules) > 0 {
					if burnErr := burnExistingMoleculesForRollback(existingMolecules, beadID, townRoot); burnErr != nil {
						fmt.Printf("  %s Could not burn stale molecule(s) from %s: %v\n", style.Dim.Render("Warning:"), beadID, burnErr)
					} else {
						fmt.Printf("  %s Burned %d stale molecule(s): %s\n",
							style.Dim.Render("○"), len(existingMolecules), strings.Join(existingMolecules, ", "))
					}
				}
			}

			// 2. Unhook the bead (set status back to open so it can be re-slung).
			unhookDir := beads.ResolveHookDir(townRoot, beadID, hookWorkDir)
			unhookCmd := exec.Command("bd", "update", beadID, "--status=open", "--assignee=")
			unhookCmd.Dir = unhookDir
			if err := unhookCmd.Run(); err != nil {
				fmt.Printf("  %s Could not unhook bead %s: %v\n", style.Dim.Render("Warning:"), beadID, err)
			} else {
				fmt.Printf("  %s Unhooked bead %s\n", style.Dim.Render("○"), beadID)
			}
		}
	}

	// 3. Clean up the spawned polecat (worktree, agent bead, convoy, etc.)
	cleanupSpawnedPolecat(spawnInfo, spawnInfo.RigName, convoyID)
}

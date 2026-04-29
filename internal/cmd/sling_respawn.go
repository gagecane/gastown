package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

// slingRespawnResetCmd resets the per-bead respawn counter so a bead blocked
// by the respawn limit can be slung again.
//
// When a bead hits the respawn limit (3 attempts), gt sling blocks further
// dispatches to prevent spawn storms. After investigating the root cause,
// this command allows re-dispatch.
var slingRespawnResetCmd = &cobra.Command{
	Use:   "respawn-reset <bead-id>",
	Short: "Reset the respawn counter for a bead",
	Long: `Reset the per-bead respawn counter so it can be slung again.

When a bead hits the respawn limit (3 attempts), gt sling blocks further
dispatches to prevent spawn storms. After investigating the root cause,
use this command to allow re-dispatch.`,
	Args: cobra.ExactArgs(1),
	RunE: runSlingRespawnReset,
}

func runSlingRespawnReset(_ *cobra.Command, args []string) error {
	beadID := args[0]
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	if err := witness.ResetBeadRespawnCount(townRoot, beadID); err != nil {
		return fmt.Errorf("resetting respawn count for %s: %w", beadID, err)
	}
	fmt.Printf("Reset respawn counter for %s. It can be slung again.\n", beadID)
	return nil
}

// `gt auto-test-pr cycle-tick` — run one tick of the auto-test-pr cycle.
//
// Phase 0 task 4 (gu-2n7xi). This is the patrol-shell entry point the
// mol-auto-test-pr-cycle formula invokes each tick. It assembles a
// CycleConfig from the town root + rigs config and calls
// autotestpr.RunCycle, then prints the structured CycleResult.
//
// In Phase 0 the cycle is INERT: with no rig opted in, RunCycle
// reconciles enabled_rigs[] and returns early with
// exit_reason="no-rigs-enabled" — no per-rig bead is touched and no
// work is dispatched. The command does NOT wire the per-rig Phase-1
// hooks (RigStore/Targets/Dispatch/…); Phase 1 (gu-gmj0r) supplies them.
package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/autotestpr"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/workspace"
)

var autoTestPRCycleTickCmd = &cobra.Command{
	Use:   "cycle-tick",
	Short: "Run one auto-test-pr cycle tick (Mayor patrol entry point)",
	Long: `Run a single tick of the auto-test-pr cycle.

Each tick reconciles the town-state bead's enabled_rigs[] against the
per-rig settings-JSON ground truth, runs the D18 cooldown-release for
any cooled-down rig whose cadence has elapsed, reads the town state for
a global pause / circuit-breaker, and — when a rig is opted in —
processes each idle rig (pick target → dispatch).

In Phase 0 (no rig opted in) the tick reconciles and exits early with
exit_reason="no-rigs-enabled". A missing town-state bead is reported as
a structured warning (exit_reason="town-bead-missing"), not an error.

This command is invoked by the mol-auto-test-pr-cycle standing patrol.`,
	RunE: runAutoTestPRCycleTick,
}

func runAutoTestPRCycleTick(cmd *cobra.Command, _ []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	townBeads := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))

	rigsConfigPath := filepath.Join(townRoot, constants.DirMayor, constants.FileRigsJSON)
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return fmt.Errorf("loading rigs config from %s: %w", rigsConfigPath, err)
	}

	cfg := &autotestpr.CycleConfig{
		TownRoot:   townRoot,
		TownBeads:  townBeads,
		RigsConfig: rigsConfig,
		Now:        time.Now(),
		// Phase 0: per-rig hooks intentionally unset → inert cycle.
	}

	result, err := autotestpr.RunCycle(cfg)
	if err != nil {
		return fmt.Errorf("auto-test-pr cycle tick: %w", err)
	}

	out, jerr := result.JSON()
	if jerr != nil {
		// Fall back to a human summary if JSON marshaling fails.
		fmt.Fprintf(cmd.OutOrStdout(),
			"auto-test-pr cycle: reconciled=%v enabled_rigs=%v exit_reason=%q rigs_processed=%d\n",
			result.Reconciled, result.EnabledRigs, result.ExitReason, result.RigsProcessed)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(out))
	return nil
}

func init() {
	autoTestPRCmd.AddCommand(autoTestPRCycleTickCmd)
}

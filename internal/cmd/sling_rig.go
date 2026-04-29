package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// runSlingToRig routes a single-sling rig-target dispatch through the unified
// executeSling() path (gu-4d1, scheduler-unify). Rig targets always spawn a
// fresh polecat, which is exactly what executeSling handles. Non-rig targets
// (dogs, self, mayor, crew, existing polecats, dead-polecat fallback) remain
// on the inline runSling path because executeSling does not cover them.
//
// Responsibilities this function owns (executeSling does not):
//   - Cross-rig guard (executeSling requires the caller to pre-check)
//   - Dry-run preview (executeSling has no dry-run mode)
//   - Auto-apply mol-polecat-work (runSling has this only for the inline path)
//   - wakeRigAgents() after dispatch when !NoBoot (executeSling requires the caller)
func runSlingToRig(ctx context.Context, beadID, rigName, formulaName string, info *beadInfo, townRoot, townBeadsDir string, force bool) error {
	_ = ctx  // reserved for future use (e.g., ctx-aware executeSling)
	_ = info // reserved — info already validated in runSling before this call

	// Cross-rig guard: prevent slinging beads to a rig whose prefix is different
	// (gt-myecw). executeSling does NOT run this check — its contract puts it on
	// the caller. Skipped under --force.
	if !force {
		if err := checkCrossRigGuard(beadID, rigName+"/polecats/_", townRoot); err != nil {
			return err
		}
	}

	// Auto-apply mol-polecat-work when slinging a bare bead to a rig (issue #288).
	// Rig targets always dispatch to a polecat, so the polecat path always applies.
	// Use --hook-raw-bead to bypass for expert/debugging scenarios.
	if formulaName == "" && !slingHookRawBead {
		formulaName = resolveFormula(slingFormula, false, townRoot, rigName)
		if slingFormula != "" {
			fmt.Printf("  Applying %s for polecat work...\n", formulaName)
		} else {
			fmt.Printf("  Auto-applying %s for polecat work...\n", formulaName)
		}
	}

	// Dry-run preview: describe what would happen without invoking executeSling.
	// Mirrors the inline runSling output so existing CLI UX is preserved.
	if slingDryRun {
		fmt.Printf("Would spawn fresh polecat in rig '%s'\n", rigName)
		if formulaName != "" {
			fmt.Printf("Would instantiate formula %s on bead %s\n", formulaName, beadID)
		}
		fmt.Printf("Would hook %s to new polecat\n", beadID)
		if slingArgs != "" {
			fmt.Printf("  args: %s\n", slingArgs)
		}
		if slingSubject != "" {
			fmt.Printf("  subject: %s\n", slingSubject)
		}
		if slingMessage != "" {
			fmt.Printf("  context: %s\n", slingMessage)
		}
		return nil
	}

	// Announce dispatch (mirrors inline runSling UX).
	if formulaName != "" {
		fmt.Printf("%s Slinging formula %s on %s to %s...\n", style.Bold.Render("🎯"), formulaName, beadID, rigName)
	} else {
		fmt.Printf("%s Slinging %s to %s...\n", style.Bold.Render("🎯"), beadID, rigName)
	}

	var mode string
	if slingRalph {
		mode = "ralph"
	}

	params := SlingParams{
		BeadID:           beadID,
		FormulaName:      formulaName,
		RigName:          rigName,
		Args:             slingArgs,
		Vars:             slingVars,
		Merge:            slingMerge,
		BaseBranch:       slingBaseBranch,
		Account:          slingAccount,
		Agent:            slingAgent,
		NoConvoy:         slingNoConvoy,
		Owned:            slingOwned,
		NoMerge:          slingNoMerge,
		ReviewOnly:       slingReviewOnly,
		Force:            force,
		HookRawBead:      slingHookRawBead,
		NoBoot:           slingNoBoot,
		Mode:             mode,
		FormulaFailFatal: true, // Single-sling: fatal on formula failure (batch-sling uses false)
		CallerContext:    "sling",
		TownRoot:         townRoot,
		BeadsDir:         townBeadsDir,
	}

	if _, err := executeSling(params); err != nil {
		return err
	}

	// wakeRigAgents is the caller's responsibility (see executeSling header).
	if !slingNoBoot {
		wakeRigAgents(rigName)
	}

	return nil
}

// checkCrossRigGuard validates that a bead's prefix matches the target rig.
// Polecats work in their rig's worktree and cannot fix code owned by another rig.
// Returns an error if the bead belongs to a different rig than the target polecat.
// Town-root beads (hq-*) are rejected — tasks must be created in the target rig.
func checkCrossRigGuard(beadID, targetAgent, townRoot string) error {
	beadPrefix := beads.ExtractPrefix(beadID)
	if beadPrefix == "" {
		return nil // Can't determine prefix, skip check
	}

	// Extract target rig from agent path (e.g., "gastown/polecats/Toast" → "gastown")
	targetRig := strings.SplitN(targetAgent, "/", 2)[0]
	if targetRig == "" {
		return nil
	}

	beadRig := beads.GetRigNameForPrefix(townRoot, beadPrefix)

	if beadRig != targetRig {
		if beadRig == "" {
			return fmt.Errorf("bead %s (prefix %q) is not in rig %q — it belongs to town root\n"+
				"Create the task from the rig directory: cd %s && bd create --title=...\n"+
				"Use --force to override", beadID, strings.TrimSuffix(beadPrefix, "-"), targetRig, targetRig)
		}
		return fmt.Errorf("cross-rig mismatch: bead %s (prefix %q) belongs to rig %q, but target is rig %q\n"+
			"Create the task from the target rig: cd %s && bd create --title=...\n"+
			"Use --force to override", beadID, strings.TrimSuffix(beadPrefix, "-"), beadRig, targetRig, targetRig)
	}

	return nil
}

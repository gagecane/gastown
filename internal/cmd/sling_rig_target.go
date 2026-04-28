package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/style"
)

// runSlingRigTargetParams bundles inputs for the rig-target dispatch helper.
// Inputs come from runSling's already-validated state (bead guards ran, force
// auto-upgrade applied, etc.) plus the package-level sling* flag variables.
type runSlingRigTargetParams struct {
	BeadID      string
	RigName     string
	FormulaName string     // pre-resolved formula name ("" means auto-apply for polecat targets)
	Info        *beadInfo  // bead info snapshot from getBeadInfo (for cross-rig guard)
	Force       bool       // local force (may include dead-agent auto-force)
	TownRoot    string
}

// runSlingRigTarget dispatches a single-bead sling to a rig target via the
// unified executeSling() path used by batch sling and scheduler dispatch.
//
// This replaces the inline 12-step flow that previously lived in runSling.
// Non-rig targets (dogs, mayor, crew, self-sling, existing polecats) remain
// on the inline path because executeSling only covers rig dispatch.
//
// Preserved single-sling semantics (differences from raw executeSling):
//   - Dry-run output (executeSling has no dry-run support)
//   - Cross-rig guard (unless --force)
//   - Auto-convoy gating that matches the old inline behavior:
//     auto-convoy only when user did NOT use --on <formula> mode
//   - Formula auto-apply for polecat targets (mol-polecat-work) unless
//     --hook-raw-bead was passed
func runSlingRigTarget(p runSlingRigTargetParams) error {
	beadID := p.BeadID
	rigName := p.RigName
	townRoot := p.TownRoot
	formulaName := p.FormulaName

	// Cross-rig guard: prevent slinging beads to polecats in the wrong rig
	// (gt-myecw). Mirrors the guard that resolveTarget applied for rig targets
	// on the old inline path.
	if !p.Force {
		if err := checkCrossRigGuard(beadID, rigName+"/polecats/_", townRoot); err != nil {
			return err
		}
	}

	// Issue #288: Auto-apply mol-polecat-work when slinging bare bead to a rig
	// (spawned polecat). Bypass with --hook-raw-bead. This matches the inline
	// single-sling behavior and the batch/scheduler paths.
	autoApplied := false
	if formulaName == "" && !slingHookRawBead {
		formulaName = resolveFormula(slingFormula, false, townRoot, rigName)
		autoApplied = true
	}

	if slingDryRun {
		return dryRunRigTarget(beadID, rigName, formulaName, autoApplied)
	}

	// Preserve old single-sling auto-convoy gating: create a convoy only when
	// the user did NOT invoke formula-on-bead mode (--on <formula>). The old
	// inline path checked formulaName BEFORE auto-apply-mol-polecat-work, so
	// bare-bead dispatch always got a convoy and explicit --on did not.
	noConvoy := slingNoConvoy || slingOnTarget != ""

	// Display banner (matches old inline format)
	if formulaName != "" {
		fmt.Printf("%s Slinging formula %s on %s to rig %s...\n",
			style.Bold.Render("🎯"), formulaName, beadID, rigName)
	} else {
		fmt.Printf("%s Slinging %s to rig %s...\n",
			style.Bold.Render("🎯"), beadID, rigName)
	}

	if autoApplied {
		if slingFormula != "" {
			fmt.Printf("  Applying %s for polecat work...\n", formulaName)
		} else {
			fmt.Printf("  Auto-applying %s for polecat work...\n", formulaName)
		}
	}

	var slingMode string
	if slingRalph {
		slingMode = "ralph"
	}

	beadsDir := ""
	if townRoot != "" {
		beadsDir = filepath.Join(townRoot, ".beads")
	}

	params := SlingParams{
		BeadID:           beadID,
		FormulaName:      formulaName,
		RigName:          rigName,
		Args:             slingArgs,
		Vars:             append([]string(nil), slingVars...),
		Merge:            slingMerge,
		BaseBranch:       slingBaseBranch,
		Account:          slingAccount,
		Agent:            slingAgent,
		NoConvoy:         noConvoy,
		Owned:            slingOwned,
		NoMerge:          slingNoMerge,
		ReviewOnly:       slingReviewOnly,
		Force:            p.Force,
		HookRawBead:      slingHookRawBead,
		NoBoot:           slingNoBoot,
		Mode:             slingMode,
		SkipCook:         false,
		FormulaFailFatal: true, // single sling: fail fast, rollback spawned polecat
		CallerContext:    "sling",
		TownRoot:         townRoot,
		BeadsDir:         beadsDir,
	}

	result, err := executeSling(params)
	if err != nil {
		// executeSling already emitted detailed warnings; surface the top-level
		// error with bead context so CLI users see what failed.
		if result != nil && result.ErrMsg != "" {
			return fmt.Errorf("slinging %s to rig %s: %s: %w", beadID, rigName, result.ErrMsg, err)
		}
		return fmt.Errorf("slinging %s to rig %s: %w", beadID, rigName, err)
	}

	// wakeRigAgents mirrors the post-resolveTarget wake in the inline path and
	// the post-loop wake in batch sling. Skip when --no-boot is set (callers
	// like the daemon manage wakes separately).
	if !slingNoBoot {
		wakeRigAgents(rigName)
	}

	return nil
}

// dryRunRigTarget prints the equivalent of the old inline dry-run output for
// rig targets. executeSling itself has no dry-run mode — we render the plan
// ourselves and return nil without touching state.
func dryRunRigTarget(beadID, rigName, formulaName string, autoApplied bool) error {
	fmt.Printf("Target is rig '%s', would spawn fresh polecat\n", rigName)
	if formulaName != "" {
		if autoApplied {
			fmt.Printf("  Would auto-apply formula %s for polecat work\n", formulaName)
		}
		fmt.Printf("Would instantiate formula %s:\n", formulaName)
		fmt.Printf("  1. bd cook %s\n", formulaName)
		fmt.Printf("  2. bd mol wisp %s --var issue=\"%s\"\n", formulaName, beadID)
		fmt.Printf("  3. bd mol bond <wisp-root> %s\n", beadID)
		fmt.Printf("  4. bd update <compound-root> --status=hooked --assignee=%s/polecats/<new>\n", rigName)
	} else {
		fmt.Printf("Would run: bd update %s --status=hooked --assignee=%s/polecats/<new>\n", beadID, rigName)
	}
	if slingSubject != "" {
		fmt.Printf("  subject (in nudge): %s\n", slingSubject)
	}
	if slingMessage != "" {
		fmt.Printf("  context: %s\n", slingMessage)
	}
	if slingArgs != "" {
		fmt.Printf("  args (in nudge): %s\n", slingArgs)
	}
	fmt.Printf("Would start fresh polecat session in rig '%s'\n", rigName)
	return nil
}

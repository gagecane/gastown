package cmd

// Autonomous work mode output for `gt prime`.
//
// When an agent primes with a hooked bead, these functions render the
// AUTONOMOUS WORK MODE directive, the bead details, and the attached molecule
// or formula checklist. The goal is to make the agent start working immediately
// without waiting for user confirmation — see the "propulsion principle" in the
// role prompts.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/style"
)

// checkSlungWork checks for hooked work on the agent's hook.
// If found, displays AUTONOMOUS WORK MODE and tells the agent to execute immediately.
// Returns true if hooked work was found (caller should skip normal startup directive).
//
// hookedBead is pre-fetched by the caller (runPrime) via findAgentWork to avoid a
// redundant lookup and ensure work context is already injected before output runs.
func checkSlungWork(ctx RoleContext, hookedBead *beads.Issue) bool {
	if hookedBead == nil {
		return false
	}

	attachment := beads.ParseAttachmentFields(hookedBead)
	hasWorkflow := hasWorkflowAttachment(attachment)

	outputAutonomousDirective(ctx, hookedBead, hasWorkflow)
	outputHookedBeadDetails(hookedBead)

	if hasWorkflow {
		outputMoleculeWorkflow(ctx, attachment)
	} else {
		outputBeadPreview(hookedBead)
	}

	return true
}

func hasWorkflowAttachment(attachment *beads.AttachmentFields) bool {
	return attachment != nil && (attachment.AttachedMolecule != "" || attachment.AttachedFormula != "")
}

// outputAutonomousDirective displays the AUTONOMOUS WORK MODE header and instructions.
func outputAutonomousDirective(ctx RoleContext, hookedBead *beads.Issue, hasMolecule bool) {
	roleAnnounce := buildRoleAnnouncement(ctx)

	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render("## 🚨 AUTONOMOUS WORK MODE 🚨"))
	fmt.Println("Work is on your hook. After announcing your role, begin IMMEDIATELY.")
	fmt.Println()
	fmt.Println("This is physics, not politeness. Gas Town is a steam engine - you are a piston.")
	fmt.Println("Every moment you wait is a moment the engine stalls. Other agents may be")
	fmt.Println("blocked waiting on YOUR output. The hook IS your assignment. RUN IT.")
	fmt.Println()
	fmt.Println("Remember: Every completion is recorded in the capability ledger. Your work")
	fmt.Println("history is visible, and quality matters. Execute with care - you're building")
	fmt.Println("a track record that proves autonomous execution works at scale.")
	fmt.Println()
	fmt.Println("1. Announce: \"" + roleAnnounce + "\" (ONE line, no elaboration)")

	if hasMolecule {
		fmt.Println("2. This bead has an ATTACHED MOLECULE (formula workflow)")
		fmt.Println("3. Work through molecule steps in order - see CURRENT STEP below")
		fmt.Println("4. Close each step with `bd close <step-id>`, then check `bd mol current` for next step")
	} else {
		fmt.Printf("2. Then IMMEDIATELY run: `bd show %s`\n", hookedBead.ID)
		fmt.Println("3. Begin execution - no waiting for user input")
	}

	// Polecats MUST call gt done — this is the single most important instruction.
	// Without it, work lands but sessions accumulate and the merge queue stalls.
	if ctx.Role == RolePolecat {
		fmt.Println()
		fmt.Printf("**⚠️ MANDATORY: When all work is committed, run `%s done` to submit and exit.**\n", cli.Name())
		fmt.Printf("Do NOT stop at the prompt. Do NOT push to main directly. `%s done` is your final action.\n", cli.Name())
	}

	fmt.Println()
	fmt.Println("**DO NOT:**")
	fmt.Println("- Wait for user response after announcing")
	fmt.Println("- Ask clarifying questions")
	fmt.Println("- Describe what you're going to do")
	fmt.Println("- Check mail first (hook takes priority)")
	if hasMolecule {
		fmt.Println("- Skip molecule steps or work on the base bead directly")
	}
	if ctx.Role == RolePolecat {
		fmt.Printf("- Sit idle after committing (run `%s done`)\n", cli.Name())
		fmt.Println("- Push directly to main (use the merge queue)")
	}
	fmt.Println()
}

// outputHookedBeadDetails displays the hooked bead's ID, title, and description summary.
func outputHookedBeadDetails(hookedBead *beads.Issue) {
	fmt.Printf("%s\n\n", style.Bold.Render("## Hooked Work"))
	fmt.Printf("  Bead ID: %s\n", style.Bold.Render(hookedBead.ID))
	fmt.Printf("  Title: %s\n", hookedBead.Title)
	if hookedBead.Description != "" {
		lines := strings.Split(hookedBead.Description, "\n")
		maxLines := 5
		if len(lines) > maxLines {
			lines = lines[:maxLines]
			lines = append(lines, "...")
		}
		fmt.Println("  Description:")
		for _, line := range lines {
			fmt.Printf("    %s\n", line)
		}
	}
	fmt.Println()
}

// outputMoleculeWorkflow displays attached molecule context with current step.
func outputMoleculeWorkflow(ctx RoleContext, attachment *beads.AttachmentFields) {
	fmt.Printf("%s\n\n", style.Bold.Render("## 🧬 ATTACHED FORMULA (WORKFLOW CHECKLIST)"))
	if attachment.AttachedFormula != "" {
		fmt.Printf("Formula: %s\n", attachment.AttachedFormula)
	}
	if attachment.AttachedMolecule != "" {
		fmt.Printf("Molecule ID: %s\n", attachment.AttachedMolecule)
	}
	if len(attachment.AttachedVars) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("🧩 VARS (instantiated formula inputs):"))
		for _, variable := range attachment.AttachedVars {
			fmt.Printf("  --var %s\n", variable)
		}
	}
	if attachment.AttachedArgs != "" {
		fmt.Printf("\n%s\n", style.Bold.Render("📋 ARGS (use these to guide execution):"))
		fmt.Printf("  %s\n", attachment.AttachedArgs)
	}
	fmt.Println()

	// Ralph loop mode: output Ralph Wiggum loop command instead of step-by-step execution
	if attachment.Mode == "ralph" {
		outputRalphLoopDirective(ctx, attachment)
		return
	}

	// Show inline formula steps from the embedded binary (root-only: no child wisps to query).
	if attachment.AttachedFormula != "" {
		showFormulaStepsFull(attachment.AttachedFormula, ctx.TownRoot, ctx.Rig, strings.Split(attachment.FormulaVars, "\n"))
		fmt.Println()
		fmt.Printf("%s\n", style.Bold.Render("Work through ALL steps above, including submit and cleanup."))
		fmt.Println("The base bead is your assignment. The formula steps define your workflow.")
		fmt.Printf("\n%s\n", style.Bold.Render("REQUIRED: When all steps complete, run `"+cli.Name()+" done` to submit to the merge queue. Do NOT stop after implementation — the formula has submit steps you must follow."))
		return
	}

	// Legacy path: no formula name stored, fall back to bd mol current
	showMoleculeExecutionPrompt(ctx.WorkDir, attachment.AttachedMolecule)
	fmt.Println()
	fmt.Printf("%s\n", style.Bold.Render("Follow the molecule steps above, NOT the base bead."))
	fmt.Println("The base bead is just a container. The molecule steps define your workflow.")
}

// outputRalphLoopDirective emits inline iterative work instructions for ralph mode.
// Ralph mode is designed for long, iterative workflows (e.g., quality improvement
// loops) that benefit from committing progress incrementally. The agent works
// through formula steps iteratively, committing after each meaningful change,
// and calls gt done when all acceptance criteria are met or no further progress
// can be made.
func outputRalphLoopDirective(ctx RoleContext, attachment *beads.AttachmentFields) {
	fmt.Printf("%s\n\n", style.Bold.Render("## RALPH LOOP MODE (ITERATIVE WORKFLOW)"))
	fmt.Println("This work uses iterative loop mode. Work through the steps below,")
	fmt.Println("committing after each meaningful change. Loop until acceptance criteria")
	fmt.Println("are met or no further progress can be made.")
	fmt.Println()

	// Show the formula steps inline (same as normal mode) so the agent has
	// the full checklist. Previously this emitted a /ralph-loop slash command
	// that didn't exist, causing the polecat to die immediately.
	if attachment.AttachedFormula != "" {
		showFormulaStepsFull(attachment.AttachedFormula, ctx.TownRoot, ctx.Rig, strings.Split(attachment.FormulaVars, "\n"))
		fmt.Println()
	}

	if attachment.AttachedArgs != "" {
		fmt.Printf("%s\n", style.Bold.Render("Context:"))
		fmt.Printf("  %s\n\n", attachment.AttachedArgs)
	}

	fmt.Printf("%s\n", style.Bold.Render("Iterative workflow:"))
	fmt.Println("1. Work through the formula steps above")
	fmt.Println("2. Commit after each meaningful change (preserve progress via git)")
	fmt.Println("3. After completing a pass, evaluate results against acceptance criteria")
	fmt.Println("4. If criteria not met, loop: identify the worst gap, fix it, commit, re-evaluate")
	fmt.Println("5. When all criteria are met (or no further progress possible), run `" + cli.Name() + " done`")
	fmt.Println()
	fmt.Printf("%s\n", style.Bold.Render("Commit frequently. Each commit preserves your progress."))
}

// outputBeadPreview runs `bd show` and displays a truncated preview of the bead.
func outputBeadPreview(hookedBead *beads.Issue) {
	fmt.Println("**Bead details:**")
	cmd := exec.Command("bd", "show", hookedBead.ID)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errMsg := strings.TrimSpace(stderr.String()); errMsg != "" {
			fmt.Fprintf(os.Stderr, "  bd show %s: %s\n", hookedBead.ID, errMsg)
		} else {
			fmt.Fprintf(os.Stderr, "  bd show %s: %v\n", hookedBead.ID, err)
		}
	} else {
		lines := strings.Split(stdout.String(), "\n")
		maxLines := 15
		if len(lines) > maxLines {
			lines = lines[:maxLines]
			lines = append(lines, "...")
		}
		for _, line := range lines {
			fmt.Printf("  %s\n", line)
		}
	}
	fmt.Println()
}

// buildRoleAnnouncement creates the role announcement string for autonomous mode.
func buildRoleAnnouncement(ctx RoleContext) string {
	switch ctx.Role {
	case RoleMayor:
		return "Mayor, checking in."
	case RoleDeacon:
		return "Deacon, checking in."
	case RoleBoot:
		return "Boot, checking in."
	case RoleWitness:
		return fmt.Sprintf("%s Witness, checking in.", ctx.Rig)
	case RoleRefinery:
		return fmt.Sprintf("%s Refinery, checking in.", ctx.Rig)
	case RolePolecat:
		return fmt.Sprintf("%s Polecat %s, checking in.", ctx.Rig, ctx.Polecat)
	case RoleCrew:
		return fmt.Sprintf("%s Crew %s, checking in.", ctx.Rig, ctx.Polecat)
	default:
		return "Agent, checking in."
	}
}

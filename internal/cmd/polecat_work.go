package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// polecatWorkCmd implements "gt polecat work" — a single entry point that
// encapsulates the canonical "pick up my next piece of work" flow:
//
//  1. Show hook; if work is hooked, print the standard status and exit 0.
//  2. Otherwise, inspect the agent's mail inbox for hookable messages
//     (those carrying an `attached_molecule: <id>` reference, or similar).
//  3. If exactly one hookable message is found, auto-attach it to the hook.
//  4. If multiple hookable messages exist, list them without choosing.
//  5. If none, tell the user the hook is empty.
//
// This exists because agent-facing nudges sometimes tell polecats to "run
// gt polecat work" (or similar guesses), and having a real command at that
// shape makes those nudges robust. See bead gu-nuz5 for the original ask.
var polecatWorkCmd = &cobra.Command{
	Use:         "work",
	Short:       "Pick up next work: show hook, or auto-hook from mail",
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Long: `Pick up the next piece of work for this polecat.

This is a convenience wrapper around the canonical pickup sequence:

  gt hook                           # Do I have work?
  # If empty:
  gt mail inbox                     # Any assignments waiting?
  gt mol attach-from-mail <mail-id> # Hook unambiguous work

Behavior:
  1. If the hook already has work, prints the current hook status and exits.
  2. If the hook is empty, scans the mail inbox for hookable messages.
  3. If exactly one hookable message is found, auto-attaches it to the hook.
  4. If several are found, lists them without choosing.
  5. If none are found, reports an empty hook.

A message is considered "hookable" when its body references an
attached_molecule, molecule_id, or molecule identifier — the same
patterns recognized by 'gt mol attach-from-mail'.

Examples:
  gt polecat work       # Pick up next work (auto-hook if unambiguous)
  gt polecat work -n    # Show what would happen, don't attach

Related commands:
  gt hook               # Show/attach hook manually
  gt mail inbox         # List mail
  gt mail hook <id>     # Attach a specific mail to your hook
  gt mol attach-from-mail <id>  # Same thing (older shape)`,
	Args: cobra.NoArgs,
	RunE: runPolecatWork,
}

var (
	polecatWorkDryRun bool
	polecatWorkForce  bool
)

func init() {
	polecatWorkCmd.Flags().BoolVarP(&polecatWorkDryRun, "dry-run", "n", false,
		"Show what would be done without attaching mail")
	polecatWorkCmd.Flags().BoolVarP(&polecatWorkForce, "force", "f", false,
		"Replace existing incomplete hooked bead when auto-attaching")

	polecatCmd.AddCommand(polecatWorkCmd)
}

// runPolecatWork implements the "gt polecat work" flow.
func runPolecatWork(cmd *cobra.Command, args []string) error {
	// Step 1: if the hook already has work, show it and return.
	hasWork, err := polecatWorkHasHookedWork()
	if err != nil {
		return fmt.Errorf("checking hook: %w", err)
	}
	if hasWork {
		// Delegate to the existing status renderer so output format stays
		// consistent with `gt hook` / `gt mol status`.
		return runMoleculeStatus(cmd, nil)
	}

	// Step 2: hook is empty — check mail for hookable messages.
	fmt.Printf("%s Hook is empty; checking mail for hookable work...\n",
		style.Dim.Render("ℹ"))

	address := detectSender()
	if address == "" {
		return fmt.Errorf("cannot determine agent identity; run from a polecat/crew/witness directory")
	}

	mailbox, err := getMailbox(address)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	messages, err := mailbox.List()
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}

	hookable := filterHookableMessages(messages)

	switch len(hookable) {
	case 0:
		fmt.Printf("%s No hookable messages found in %s.\n",
			style.Dim.Render("○"), address)
		fmt.Printf("  %s Run 'gt mail inbox' to inspect all messages.\n",
			style.Dim.Render("Hint:"))
		return nil

	case 1:
		msg := hookable[0]
		fmt.Printf("%s Found 1 hookable message: %s (%s)\n",
			style.Success.Render("✓"), msg.ID, msg.Subject)
		if polecatWorkDryRun {
			fmt.Printf("  %s Would attach %s to %s's hook (dry-run).\n",
				style.Dim.Render("Dry-run:"), msg.ID, address)
			return nil
		}
		// Delegate to the mail-hook code path so behavior (retries, agent
		// bead updates, event logging) stays identical with the existing
		// entry points. Reset shared hook flag state first — runHook reads
		// the globals, and we do not want to leak this command's flags.
		hookSubject = ""
		hookMessage = ""
		hookDryRun = false
		hookForce = polecatWorkForce
		if err := runHook(cmd, []string{msg.ID}); err != nil {
			return fmt.Errorf("auto-attaching mail: %w", err)
		}
		return nil

	default:
		fmt.Printf("%s Found %d hookable messages; not auto-attaching.\n",
			style.Warning.Render("⚠"), len(hookable))
		for i, msg := range hookable {
			fmt.Printf("  %d. %s  %s\n", i+1,
				style.Dim.Render(msg.ID), msg.Subject)
		}
		fmt.Printf("\n%s Pick one with:\n", style.Dim.Render("Hint:"))
		fmt.Printf("    gt mail hook <mail-id>\n")
		return nil
	}
}

// polecatWorkHasHookedWork reports whether the current agent already has
// work on its hook. It performs the same identity lookups as runMoleculeStatus
// but returns a boolean so callers can branch on hook emptiness without
// printing anything.
//
// An agent is considered to "have work" if any bead assigned to it is either:
//   - in the hooked status (canonical slung work), or
//   - in_progress (interrupted work — the hook relation is preserved across
//     session restarts and we don't want to double-hook on top of it).
//
// For rig-level agents, town-level beads (mayor-slung hq-* beads) are also
// consulted so cross-rig assignments are not missed.
func polecatWorkHasHookedWork() (bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return false, fmt.Errorf("not in a Gas Town workspace")
	}

	// Determine the current agent identity using the same cwd-first strategy
	// as `gt mol status` / `gt hook` so the answers are consistent.
	roleCtx := detectRole(cwd, townRoot)
	if roleCtx.Role == RoleUnknown {
		roleCtx, _ = GetRoleWithContext(cwd, townRoot)
	}
	target := buildAgentIdentity(roleCtx)
	if target == "" {
		return false, fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
	}

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return false, fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	checkOne := func(bd *beads.Beads) (bool, error) {
		hooked, err := bd.List(beads.ListOptions{
			Status:   beads.StatusHooked,
			Assignee: target,
			Priority: -1,
		})
		if err != nil {
			return false, err
		}
		if len(hooked) > 0 {
			return true, nil
		}
		inProgress, err := bd.List(beads.ListOptions{
			Status:   "in_progress",
			Assignee: target,
			Priority: -1,
		})
		if err != nil {
			return false, err
		}
		return len(inProgress) > 0, nil
	}

	if found, err := checkOne(b); err != nil {
		return false, err
	} else if found {
		return true, nil
	}

	// For rig-level agents, also consult town beads (mayor-slung hq-* beads).
	if !isTownLevelRole(target) && townRoot != "" {
		townBeadsDir := townRoot + "/.beads"
		if _, statErr := os.Stat(townBeadsDir); statErr == nil {
			townB := beads.New(townBeadsDir)
			if found, err := checkOne(townB); err == nil && found {
				return true, nil
			}
		}
	}

	return false, nil
}

// filterHookableMessages returns messages whose body references a molecule
// ID in one of the patterns understood by `gt mol attach-from-mail`
// (delegated to extractMoleculeIDFromMail).
func filterHookableMessages(messages []*mail.Message) []*mail.Message {
	out := make([]*mail.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if extractMoleculeIDFromMail(msg.Body) != "" {
			out = append(out, msg)
		}
	}
	return out
}

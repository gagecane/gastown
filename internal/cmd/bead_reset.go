package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	beadResetReason  string
	beadResetDryRun  bool
	beadResetKeepOpen bool // keep open sling-contexts (default: close them)
)

var beadResetCmd = &cobra.Command{
	Use:   "reset <bead-id>",
	Short: "Fully reset a bead's execution state (molecules, deps, sling-contexts)",
	Long: `Reset a bead back to a clean dispatchable state.

Atomically performs the cleanup that 'bd update --status=open --assignee none'
SHOULD do but doesn't:

  1. Burns any attached molecule wisp (closes descendants, detaches from bead,
     removes the dep bond, force-closes the wisp root).
  2. Closes any open sling-context bead tracking this work bead, so the
     scheduler doesn't keep retrying a circuit-broken dispatch.
  3. Sets the bead's status back to "open" with assignee cleared so a fresh
     sling can pick it up cleanly.

This is the operator-facing equivalent of the four-command cleanup recipe in
gu-8f7u (gt mol detach + bd close <wisp> + bd update --status=open + gt sling
--force). Without this command, beads recovered from dead-polecat incidents
stay ⏸ blocked on stale dep edges to closed wisps even after the operator
"resets" them.

Example:
  gt bead reset gu-8f7u                  # Full reset, defaults to "manual reset"
  gt bead reset gu-8f7u -r "nux died"    # Reset with reason logged on the bead
  gt bead reset gu-8f7u --dry-run        # Show planned actions, change nothing
`,
	Args: cobra.ExactArgs(1),
	RunE: runBeadReset,
}

func init() {
	beadResetCmd.Flags().StringVarP(&beadResetReason, "reason", "r", "manual reset", "Reason logged on the bead and audit trail")
	beadResetCmd.Flags().BoolVarP(&beadResetDryRun, "dry-run", "n", false, "Show planned actions without changing state")
	beadResetCmd.Flags().BoolVar(&beadResetKeepOpen, "keep-sling-contexts", false, "Do not close open sling-context beads tracking this bead")
	beadCmd.AddCommand(beadResetCmd)
}

func runBeadReset(cmd *cobra.Command, args []string) error {
	beadID := args[0]

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	info, err := getBeadInfo(beadID)
	if err != nil {
		return fmt.Errorf("loading bead: %w", err)
	}

	// Refuse to reset closed beads — that's a destructive change that can mask
	// completed work. Operators can reopen via `bd update --status=open`
	// explicitly, then reset.
	if info.Status == "closed" {
		return fmt.Errorf("bead %s is closed; refusing to reset (reopen first if intentional)", beadID)
	}

	molecules := collectExistingMolecules(info)

	// Discover open sling-contexts pointing at this work bead. Sling contexts
	// live in the town's beads DB (they're rig-agnostic scheduler bookkeeping),
	// so we resolve the town beads dir explicitly.
	var staleContexts []*beads.Issue
	if !beadResetKeepOpen {
		townBd := beads.New(townRoot)
		contexts, listErr := townBd.ListOpenSlingContexts()
		if listErr != nil {
			// Non-fatal: print and continue. Worst case is a leftover context
			// the scheduler will retry; that's the bug we're trying to fix,
			// but failing the whole reset on a list error would be worse —
			// the molecule cleanup is the more important half.
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not list sling-contexts: %v\n", listErr)
		}
		for _, ctx := range contexts {
			fields := beads.ParseSlingContextFields(ctx.Description)
			if fields != nil && fields.WorkBeadID == beadID {
				staleContexts = append(staleContexts, ctx)
			}
		}
	}

	// --- Plan summary --------------------------------------------------------
	fmt.Printf("%s Reset plan for %s (status=%s, assignee=%q):\n",
		style.Bold.Render("→"), beadID, info.Status, info.Assignee)
	if len(molecules) == 0 {
		fmt.Printf("  • No attached molecules to burn\n")
	} else {
		fmt.Printf("  • Burn %d molecule(s): %v\n", len(molecules), molecules)
	}
	if len(staleContexts) == 0 {
		fmt.Printf("  • No open sling-contexts tracking this bead\n")
	} else {
		ids := make([]string, 0, len(staleContexts))
		for _, c := range staleContexts {
			ids = append(ids, c.ID)
		}
		fmt.Printf("  • Close %d sling-context(s): %v\n", len(ids), ids)
	}
	fmt.Printf("  • Set status=open, clear assignee\n")

	if beadResetDryRun {
		fmt.Printf("%s Dry-run: no changes applied\n", style.Dim.Render("ℹ"))
		return nil
	}

	// --- Apply ---------------------------------------------------------------

	// Step 1: Burn attached molecules. burnExistingMolecules already handles
	// descendants → detach → dep-bond removal → wisp root close in the safe
	// order. Running this BEFORE the status flip means the bead's description
	// is the source of truth for the molecule pointer at the moment we burn.
	if len(molecules) > 0 {
		if err := burnExistingMolecules(molecules, beadID, townRoot); err != nil {
			return fmt.Errorf("burning molecules on %s: %w", beadID, err)
		}
		fmt.Printf("%s Burned %d molecule(s)\n", style.Bold.Render("✓"), len(molecules))
	}

	// Step 2: Close orphan sling-contexts so the scheduler stops retrying a
	// circuit-broken dispatch. Idempotent: CloseSlingContext suppresses
	// "already closed" errors.
	if len(staleContexts) > 0 {
		townBd := beads.New(townRoot)
		closed := 0
		for _, ctx := range staleContexts {
			closeReason := fmt.Sprintf("bead reset: %s", beadResetReason)
			if err := townBd.CloseSlingContext(ctx.ID, closeReason); err != nil {
				// Non-fatal: log and continue. A leftover context is better
				// than aborting the reset half-applied.
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not close sling-context %s: %v\n", ctx.ID, err)
				continue
			}
			closed++
		}
		fmt.Printf("%s Closed %d sling-context(s)\n", style.Bold.Render("✓"), closed)
	}

	// Step 3: Reset the bead to open with cleared assignee. Use ReleaseWithReason
	// so the change is logged as a note. Run on the bead's home DB.
	beadDir := beads.ResolveHookDir(townRoot, beadID, "")
	bd := beads.New(beadDir)
	if err := bd.ReleaseWithReason(beadID, beadResetReason); err != nil {
		return fmt.Errorf("resetting %s to open: %w", beadID, err)
	}
	fmt.Printf("%s %s → status=open, assignee=cleared\n", style.Bold.Render("✓"), beadID)

	return nil
}

package cmd

import (
	"github.com/spf13/cobra"
)

// Convoy command flags
var (
	convoyMolecule     string
	convoyNotify       string
	convoyOwner        string
	convoyOwned        bool
	convoyMerge        string
	convoyBaseBranch   string
	convoyStatusJSON   bool
	convoyListJSON     bool
	convoyListStatus   string
	convoyListAll      bool
	convoyListTree     bool
	convoyInteractive  bool
	convoyStrandedJSON bool
	convoyCloseReason  string
	convoyCloseNotify  string
	convoyCloseForce   bool
	convoyCheckDryRun  bool
	convoyLandForce    bool
	convoyLandKeep     bool
	convoyLandDryRun   bool
	convoyFromEpic     string
)

var convoyCmd = &cobra.Command{
	Use:         "convoy",
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Track batches of work across rigs",
	RunE: func(cmd *cobra.Command, args []string) error {
		if convoyInteractive {
			return runConvoyTUI()
		}
		return requireSubcommand(cmd, args)
	},
	Long: `Manage convoys - the primary unit for tracking batched work.

A convoy is a persistent tracking unit that monitors related issues across
rigs. When you kick off work (even a single issue), a convoy tracks it so
you can see when it lands and what was included.

WHAT IS A CONVOY:
  - Persistent tracking unit with an ID (hq-*)
  - Tracks issues across rigs (frontend+backend, beads+gastown, etc.)
  - Auto-closes when all tracked issues complete → notifies subscribers
  - Can be reopened by adding more issues

WHAT IS A SWARM:
  - Ephemeral: "the workers currently assigned to a convoy's issues"
  - No separate ID - uses the convoy ID
  - Dissolves when work completes

TRACKING SEMANTICS:
  - 'tracks' relation is non-blocking (tracked issues don't block convoy)
  - Cross-prefix capable (convoy in hq-* tracks issues in gt-*, bd-*)
  - Landed: all tracked issues closed → notification sent to subscribers

COMMANDS:
  create    Create a convoy tracking specified issues
  add       Add issues to an existing convoy (reopens if closed)
  close     Close a convoy (verifies all items done, or use --force)
  land      Land an owned convoy (cleanup worktrees, close convoy)
  status    Show convoy progress, tracked issues, and active workers
  list      List convoys (the dashboard view)
  watch     Subscribe to convoy completion notifications
  unwatch   Unsubscribe from convoy completion notifications`,
}

var convoyCreateCmd = &cobra.Command{
	Use:   "create <name> [issues...]",
	Short: "Create a new convoy",
	Long: `Create a new convoy that tracks the specified issues.

The convoy is created in town-level beads (hq-* prefix) and can track
issues across any rig.

The --owner flag specifies who requested the convoy (receives completion
notification by default). If not specified, defaults to created_by.
The --notify flag adds additional subscribers beyond the owner.

The --merge flag sets the merge strategy for all work in the convoy:
  direct  Push branch directly to main (no MR, no refinery)
  mr      Create merge-request bead, refinery processes (default)
  local   Keep on feature branch (for upstream PRs, human review)

Examples:
  gt convoy create "Deploy v2.0" gt-abc bd-xyz
  gt convoy create "Release prep" gt-abc --notify           # defaults to mayor/
  gt convoy create "Release prep" gt-abc --notify ops/      # notify ops/
  gt convoy create "Feature rollout" gt-a gt-b --owner mayor/ --notify ops/
  gt convoy create "Feature rollout" gt-a gt-b gt-c --molecule mol-release
  gt convoy create --owned "Manual deploy" gt-abc           # caller-managed lifecycle
  gt convoy create "Quick fix" gt-abc --merge=direct        # bypass refinery

  # Auto-discover issues from an epic's children:
  gt convoy create --from-epic gt-epic-abc
  gt convoy create --from-epic gt-epic-abc --owned --merge=direct`,
	Args:         cobra.ArbitraryArgs,
	SilenceUsage: true,
	RunE:         runConvoyCreate,
}

var convoyStatusCmd = &cobra.Command{
	Use:   "status [convoy-id]",
	Short: "Show convoy status",
	Long: `Show detailed status for a convoy.

Displays convoy metadata, tracked issues, and completion progress.
Without an ID, shows status of all active convoys.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyStatus,
}

var convoyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List convoys",
	Long: `List convoys, showing open convoys by default.

Examples:
  gt convoy list              # Open convoys only (default)
  gt convoy list --all        # All convoys (open + closed)
  gt convoy list --status=closed  # Recently landed
  gt convoy list --tree       # Show convoy + child status tree
  gt convoy list --json`,
	SilenceUsage: true,
	RunE:         runConvoyList,
}

var convoyAddCmd = &cobra.Command{
	Use:   "add <convoy-id> <issue-id> [issue-id...]",
	Short: "Add issues to an existing convoy",
	Long: `Add issues to an existing convoy.

If the convoy is closed, it will be automatically reopened.

Examples:
  gt convoy add hq-cv-abc gt-new-issue
  gt convoy add hq-cv-abc gt-issue1 gt-issue2 gt-issue3`,
	Args:         cobra.MinimumNArgs(2),
	SilenceUsage: true,
	RunE:         runConvoyAdd,
}

var convoyCheckCmd = &cobra.Command{
	Use:   "check [convoy-id]",
	Short: "Check and auto-close completed convoys",
	Long: `Check convoys and auto-close any where all tracked issues are complete.

Without arguments, checks all open convoys. With a convoy ID, checks only that convoy.

This handles cross-rig convoy completion: convoys in town beads tracking issues
in rig beads won't auto-close via bd close alone. This command bridges that gap.

Can be run manually or by deacon patrol to ensure convoys close promptly.

Examples:
  gt convoy check              # Check all open convoys
  gt convoy check hq-cv-abc    # Check specific convoy
  gt convoy check --dry-run    # Preview what would close without acting`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyCheck,
}

var convoyStrandedCmd = &cobra.Command{
	Use:   "stranded",
	Short: "Find stranded convoys (ready work, stuck, or empty) needing attention",
	Long: `Find convoys that have ready issues but no workers processing them,
stuck convoys (tracked issues but none ready), or empty convoys that need cleanup.

A convoy is "stranded" when:
- Convoy is open AND either:
  - Has tracked issues that are ready but unassigned, OR
  - Has tracked issues but none are ready (stuck — waiting on dependencies/workers), OR
  - Has 0 tracked issues (empty — needs auto-close via convoy check)

Use this to detect convoys that need feeding or cleanup. The Deacon patrol
runs this periodically and dispatches dogs to feed stranded convoys.

Examples:
  gt convoy stranded              # Show stranded convoys
  gt convoy stranded --json       # Machine-readable output for automation`,
	SilenceUsage: true,
	RunE:         runConvoyStranded,
}

var convoyCloseCmd = &cobra.Command{
	Use:   "close <convoy-id>",
	Short: "Close a convoy",
	Long: `Close a convoy, optionally with a reason.

By default, verifies that all tracked issues are closed before allowing the
close. Use --force to close regardless of tracked issue status.

The close is idempotent - closing an already-closed convoy is a no-op.

Examples:
  gt convoy close hq-cv-abc                           # Close (all items must be done)
  gt convoy close hq-cv-abc --force                   # Force close abandoned convoy
  gt convoy close hq-cv-abc --reason="no longer needed" --force
  gt convoy close hq-cv-xyz --notify mayor/`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyClose,
}

var convoyLandCmd = &cobra.Command{
	Use:   "land <convoy-id>",
	Short: "Land an owned convoy (cleanup worktrees, close convoy)",
	Long: `Land an owned convoy, performing caller-side cleanup.

This is the caller-managed equivalent of the witness/refinery merge pipeline.
Use this to explicitly land a convoy when you're satisfied with the results.

The command:
  1. Verifies the convoy has the gt:owned label (refuses non-owned convoys)
  2. Checks all tracked issues are done/closed (use --force to override)
  3. Cleans up polecat worktrees associated with the convoy's tracked issues
  4. Closes the convoy bead with reason "Landed by owner"
  5. Sends completion notifications to owner/notify addresses

Use 'gt convoy close' instead for non-owned convoys.

Examples:
  gt convoy land hq-cv-abc                  # Land owned convoy
  gt convoy land hq-cv-abc --force          # Land even with open issues
  gt convoy land hq-cv-abc --keep-worktrees # Skip worktree cleanup
  gt convoy land hq-cv-abc --dry-run        # Preview what would happen`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyLand,
}

func init() {
	// Create flags
	convoyCreateCmd.Flags().StringVar(&convoyMolecule, "molecule", "", "Associated molecule ID")
	convoyCreateCmd.Flags().StringVar(&convoyOwner, "owner", "", "Owner who requested convoy (gets completion notification)")
	convoyCreateCmd.Flags().StringVar(&convoyNotify, "notify", "", "Additional address to notify on completion (default: mayor/ if flag used without value)")
	convoyCreateCmd.Flags().Lookup("notify").NoOptDefVal = "mayor/"
	convoyCreateCmd.Flags().BoolVar(&convoyOwned, "owned", false, "Mark convoy as caller-managed lifecycle (no automatic witness/refinery registration)")
	convoyCreateCmd.Flags().StringVar(&convoyMerge, "merge", "", "Merge strategy: direct (push to main), mr (merge queue, default), local (keep on branch)")
	convoyCreateCmd.Flags().StringVar(&convoyBaseBranch, "base-branch", "", "Target branch for polecats (e.g., 'feat/extraction-review')")
	convoyCreateCmd.Flags().StringVar(&convoyFromEpic, "from-epic", "", "Auto-discover tracked issues from an epic's slingable children")

	// Status flags
	convoyStatusCmd.Flags().BoolVar(&convoyStatusJSON, "json", false, "Output as JSON")

	// List flags
	convoyListCmd.Flags().BoolVar(&convoyListJSON, "json", false, "Output as JSON")
	convoyListCmd.Flags().StringVar(&convoyListStatus, "status", "", "Filter by status (open, closed)")
	convoyListCmd.Flags().BoolVar(&convoyListAll, "all", false, "Show all convoys (open and closed)")
	convoyListCmd.Flags().BoolVar(&convoyListTree, "tree", false, "Show convoy + child status tree")

	// Interactive TUI flag (on parent command)
	convoyCmd.Flags().BoolVarP(&convoyInteractive, "interactive", "i", false, "Interactive tree view")

	// Check flags
	convoyCheckCmd.Flags().BoolVar(&convoyCheckDryRun, "dry-run", false, "Preview what would close without acting")

	// Stranded flags
	convoyStrandedCmd.Flags().BoolVar(&convoyStrandedJSON, "json", false, "Output as JSON")

	// Close flags
	convoyCloseCmd.Flags().StringVar(&convoyCloseReason, "reason", "", "Reason for closing the convoy")
	convoyCloseCmd.Flags().StringVar(&convoyCloseNotify, "notify", "", "Agent to notify on close (e.g., mayor/)")
	convoyCloseCmd.Flags().BoolVarP(&convoyCloseForce, "force", "f", false, "Close even if tracked issues are still open")

	// Land flags
	convoyLandCmd.Flags().BoolVarP(&convoyLandForce, "force", "f", false, "Land even if tracked issues are not all closed")
	convoyLandCmd.Flags().BoolVar(&convoyLandKeep, "keep-worktrees", false, "Skip worktree cleanup")
	convoyLandCmd.Flags().BoolVar(&convoyLandDryRun, "dry-run", false, "Show what would happen without acting")

	// Add subcommands
	convoyCmd.AddCommand(convoyCreateCmd)
	convoyCmd.AddCommand(convoyStatusCmd)
	convoyCmd.AddCommand(convoyListCmd)
	convoyCmd.AddCommand(convoyAddCmd)
	convoyCmd.AddCommand(convoyCheckCmd)
	convoyCmd.AddCommand(convoyStrandedCmd)
	convoyCmd.AddCommand(convoyCloseCmd)
	convoyCmd.AddCommand(convoyLandCmd)
	convoyCmd.AddCommand(convoyStageCmd)
	convoyCmd.AddCommand(convoyLaunchCmd)

	rootCmd.AddCommand(convoyCmd)
}


package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Refinery command flags
var (
	refineryForeground    bool
	refineryStatusJSON    bool
	refineryQueueJSON     bool
	refineryAgentOverride string
	refineryForce         bool
)

var refineryCmd = &cobra.Command{
	Use:     "refinery",
	Aliases: []string{"ref"},
	GroupID: GroupAgents,
	Short:   "Manage the Refinery (merge queue processor)",
	RunE:    requireSubcommand,
	Long: `Manage the Refinery - the per-rig merge queue processor.

The Refinery serializes all merges to main for a rig:
  - Receives MRs submitted by polecats (via gt done)
  - Rebases work branches onto latest main
  - Runs validation (tests, builds, checks)
  - Merges to main when clear
  - If conflict: spawns FRESH polecat to re-implement (original is gone)

Work flows: Polecat completes → gt done → MR in queue → Refinery merges.
The polecat is already nuked by the time the Refinery processes.

One Refinery per rig. Persistent agent that processes work as it arrives.

Role shortcuts: "refinery" in mail/nudge addresses resolves to this rig's Refinery.`,
}

var refineryStartCmd = &cobra.Command{
	Use:     "start [rig]",
	Aliases: []string{"spawn"},
	Short:   "Start the refinery",
	Long: `Start the Refinery for a rig.

Launches the merge queue processor which monitors for polecat work branches
and merges them to the appropriate target branches.

If rig is not specified, infers it from the current directory.

Examples:
  gt refinery start greenplace
  gt refinery start              # infer rig from cwd`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryStart,
}

var refineryStopCmd = &cobra.Command{
	Use:   "stop [rig]",
	Short: "Stop the refinery",
	Long: `Stop a running Refinery.

Gracefully stops the refinery, completing any in-progress merge first.
If rig is not specified, infers it from the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryStop,
}

var refineryStatusCmd = &cobra.Command{
	Use:   "status [rig]",
	Short: "Show refinery status",
	Long: `Show the status of a rig's Refinery.

Displays running state, current work, queue length, and statistics.
If rig is not specified, infers it from the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryStatus,
}

var refineryQueueCmd = &cobra.Command{
	Use:   "queue [rig]",
	Short: "Show merge queue",
	Long: `Show the merge queue for a rig.

Lists all pending merge requests waiting to be processed.
If rig is not specified, infers it from the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryQueue,
}

var refineryAttachCmd = &cobra.Command{
	Use:   "attach [rig]",
	Short: "Attach to refinery session",
	Long: `Attach to a running Refinery's Claude session.

Allows interactive access to the Refinery agent for debugging
or manual intervention.

If rig is not specified, infers it from the current directory.

Examples:
  gt refinery attach greenplace
  gt refinery attach          # infer rig from cwd`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryAttach,
}

var refineryRestartCmd = &cobra.Command{
	Use:   "restart [rig]",
	Short: "Restart the refinery",
	Long: `Restart the Refinery for a rig.

Stops the current session (if running) and starts a fresh one.
If rig is not specified, infers it from the current directory.

Examples:
  gt refinery restart greenplace
  gt refinery restart          # infer rig from cwd`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryRestart,
}

var refineryClaimCmd = &cobra.Command{
	Use:   "claim <mr-id>",
	Short: "Claim an MR for processing",
	Long: `Claim a merge request for processing by this refinery worker.

When running multiple refinery workers in parallel, each worker must claim
an MR before processing to prevent double-processing. Claims expire after
10 minutes if not processed (for crash recovery).

The worker ID is automatically determined from the GT_REFINERY_WORKER
environment variable, or defaults to "refinery-1".

Examples:
  gt refinery claim gt-abc123
  GT_REFINERY_WORKER=refinery-2 gt refinery claim gt-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runRefineryClaim,
}

var refineryReleaseCmd = &cobra.Command{
	Use:   "release <mr-id>",
	Short: "Release a claimed MR back to the queue",
	Long: `Release a claimed merge request back to the queue.

Called when processing fails and the MR should be retried by another worker.
This clears the claim so other workers can pick up the MR.

Examples:
  gt refinery release gt-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runRefineryRelease,
}

var refineryUnclaimedCmd = &cobra.Command{
	Use:   "unclaimed [rig]",
	Short: "List unclaimed MRs available for processing",
	Long: `List merge requests that are available for claiming.

Shows MRs that are not currently claimed by any worker, or have stale
claims (worker may have crashed). Useful for parallel refinery workers
to find work.

Examples:
  gt refinery unclaimed
  gt refinery unclaimed --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryUnclaimed,
}

var refineryUnclaimedJSON bool

var refineryReadyCmd = &cobra.Command{
	Use:   "ready [rig]",
	Short: "List MRs ready for processing (unclaimed and unblocked)",
	Long: `List merge requests ready for processing.

Shows MRs that are:
- Not currently claimed by any worker (or claim is stale)
- Not blocked by an open task (e.g., conflict resolution in progress)

This is the preferred command for finding work to process.

Use --all to see ALL open MRs (claimed, blocked, etc.) with raw data
including timestamps, assignees, and branch existence. Designed for
agent-side queue health analysis.

Examples:
  gt refinery ready
  gt refinery ready --json
  gt refinery ready --all --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryReady,
}

var refineryReadyJSON bool
var refineryReadyAll bool

var refineryBlockedCmd = &cobra.Command{
	Use:   "blocked [rig]",
	Short: "List MRs blocked by open tasks",
	Long: `List merge requests blocked by open tasks.

Shows MRs waiting for conflict resolution or other blocking tasks to complete.
When the blocking task closes, the MR will appear in 'ready'.

Examples:
  gt refinery blocked
  gt refinery blocked --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryBlocked,
}

var refineryBlockedJSON bool

var refineryDiagnoseCmd = &cobra.Command{
	Use:   "diagnose [rig]",
	Short: "Diagnose refinery worktree health (wedge detection, etc.)",
	Long: `Diagnose health of a rig's Refinery worktree.

Detects known wedge conditions that can stall the merge queue without
killing the refinery agent — most notably the "reaped temp upstream"
state where the worktree's local 'temp' branch tracks an origin/polecat
branch that has since been reaped (gu-hlie / parent gu-xn2z).

By default the command is read-only. Pass --fix to clear any wedge
condition in place (unsets the upstream and resets to origin/<default>).

If rig is not specified, infers it from the current directory.

Examples:
  gt refinery diagnose
  gt refinery diagnose greenplace
  gt refinery diagnose --fix
  gt refinery diagnose --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryDiagnose,
}

var (
	refineryDiagnoseJSON bool
	refineryDiagnoseFix  bool
)

var refineryDisableCmd = &cobra.Command{
	Use:   "disable [rig]",
	Short: "Persistently disable the refinery for a rig",
	Long: `Persistently disable the refinery for a rig (witness stays up).

Sets refinery_disabled=true in the rig's config.json. The refinery will
not start even after daemon restarts or 'gt up'. Use 'gt refinery enable'
to re-enable.

This is the correct persistent state for fork rigs where the refinery
should never run (the witness continues to manage polecat lifecycle).

If rig is not specified, infers it from the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryDisable,
}

var refineryEnableCmd = &cobra.Command{
	Use:   "enable [rig]",
	Short: "Re-enable the refinery for a rig",
	Long: `Re-enable the refinery for a rig after 'gt refinery disable'.

Clears the refinery_disabled flag in the rig's config.json.
Does not automatically start the refinery — use 'gt refinery start' after.

If rig is not specified, infers it from the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryEnable,
}

func runRefineryDisable(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}
	_, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}
	if err := rig.SetRefineryDisabled(r.Path, true); err != nil {
		return fmt.Errorf("disabling refinery: %w", err)
	}
	fmt.Printf("%s Refinery disabled for %s\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  %s\n", style.Dim.Render("Witness continues running. Use 'gt refinery enable "+rigName+"' to re-enable."))
	return nil
}

func runRefineryEnable(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}
	_, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}
	if err := rig.SetRefineryDisabled(r.Path, false); err != nil {
		return fmt.Errorf("enabling refinery: %w", err)
	}
	fmt.Printf("%s Refinery enabled for %s\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  %s\n", style.Dim.Render("Use 'gt refinery start "+rigName+"' to start."))
	return nil
}

func init() {
	// Start flags
	refineryStartCmd.Flags().BoolVar(&refineryForeground, "foreground", false, "Run in foreground (default: background)")
	_ = refineryStartCmd.Flags().MarkHidden("foreground")
	refineryStartCmd.Flags().StringVar(&refineryAgentOverride, "agent", "", "Agent alias to run the Refinery with (overrides town default)")
	refineryStartCmd.Flags().BoolVar(&refineryForce, "force", false, "Start refinery even on a fork rig (overrides upstream_url guard)")

	// Attach flags
	refineryAttachCmd.Flags().StringVar(&refineryAgentOverride, "agent", "", "Agent alias to run the Refinery with (overrides town default)")

	// Restart flags
	refineryRestartCmd.Flags().StringVar(&refineryAgentOverride, "agent", "", "Agent alias to run the Refinery with (overrides town default)")
	refineryRestartCmd.Flags().BoolVar(&refineryForce, "force", false, "Restart refinery even on a fork rig (overrides upstream_url guard)")

	// Status flags
	refineryStatusCmd.Flags().BoolVar(&refineryStatusJSON, "json", false, "Output as JSON")

	// Queue flags
	refineryQueueCmd.Flags().BoolVar(&refineryQueueJSON, "json", false, "Output as JSON")

	// Unclaimed flags
	refineryUnclaimedCmd.Flags().BoolVar(&refineryUnclaimedJSON, "json", false, "Output as JSON")

	// Ready flags
	refineryReadyCmd.Flags().BoolVar(&refineryReadyJSON, "json", false, "Output as JSON")
	refineryReadyCmd.Flags().BoolVar(&refineryReadyAll, "all", false, "Show all open MRs (claimed, blocked, etc.) with raw data for queue health analysis")

	// Blocked flags
	refineryBlockedCmd.Flags().BoolVar(&refineryBlockedJSON, "json", false, "Output as JSON")

	// Diagnose flags
	refineryDiagnoseCmd.Flags().BoolVar(&refineryDiagnoseJSON, "json", false, "Output as JSON")
	refineryDiagnoseCmd.Flags().BoolVar(&refineryDiagnoseFix, "fix", false, "Clear any detected wedge condition in place")

	// Add subcommands
	refineryCmd.AddCommand(refineryStartCmd)
	refineryCmd.AddCommand(refineryStopCmd)
	refineryCmd.AddCommand(refineryRestartCmd)
	refineryCmd.AddCommand(refineryStatusCmd)
	refineryCmd.AddCommand(refineryQueueCmd)
	refineryCmd.AddCommand(refineryAttachCmd)
	refineryCmd.AddCommand(refineryClaimCmd)
	refineryCmd.AddCommand(refineryReleaseCmd)
	refineryCmd.AddCommand(refineryUnclaimedCmd)
	refineryCmd.AddCommand(refineryReadyCmd)
	refineryCmd.AddCommand(refineryBlockedCmd)
	refineryCmd.AddCommand(refineryDiagnoseCmd)
	refineryCmd.AddCommand(refineryDisableCmd)
	refineryCmd.AddCommand(refineryEnableCmd)

	rootCmd.AddCommand(refineryCmd)
}

// getRefineryManager creates a refinery manager for a rig.
// If rigName is empty, infers the rig from cwd.
func getRefineryManager(rigName string) (*refinery.Manager, *rig.Rig, string, error) {
	// Infer rig from cwd if not provided
	if rigName == "" {
		townRoot, err := workspace.FindFromCwdOrError()
		if err != nil {
			return nil, nil, "", fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
		rigName, err = inferRigFromCwd(townRoot)
		if err != nil {
			return nil, nil, "", fmt.Errorf("could not determine rig: %w\nUsage: gt refinery <command> <rig>", err)
		}
	}

	_, r, err := getRig(rigName)
	if err != nil {
		return nil, nil, "", err
	}

	mgr := refinery.NewManager(r)
	return mgr, r, rigName, nil
}

func runRefineryStart(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	mgr, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	if err := checkRigNotParkedOrDocked(rigName); err != nil {
		return err
	}
	if refineryForeground {
		return fmt.Errorf("foreground mode is deprecated; use background mode (remove --foreground flag)")
	}

	// Refuse to start the refinery on a fork rig unless --force is given.
	// The refinery merges branches to the rig's main, which corrupts fork/upstream
	// fast-forward sync for rigs where upstream_url != git_url.
	if !refineryForce {
		if rigCfg, err := rig.LoadRigConfig(r.Path); err == nil && rigCfg.UpstreamURL != "" {
			fmt.Println()
			fmt.Printf("  %s Refinery blocked for fork rig %q\n", style.Bold.Render("⛔"), rigName)
			fmt.Println()
			fmt.Printf("  This rig is a fork (upstream_url = %s).\n", rigCfg.UpstreamURL)
			fmt.Println("  Running the refinery merges branches to fork main, which breaks")
			fmt.Println("  fork↔upstream fast-forward sync and pollutes the fork's history.")
			fmt.Println()
			fmt.Println("  Correct workflow for a fork rig:")
			fmt.Println("    1. Work on a feature branch")
			fmt.Println("    2. Push to your fork")
			fmt.Println("    3. Open a PR to the upstream repo")
			fmt.Println()
			fmt.Printf("  To permanently disable the refinery for this rig: gt refinery disable %s\n", rigName)
			fmt.Printf("  To start anyway (advanced / non-standard):        gt refinery start %s --force\n", rigName)
			fmt.Println()
			return fmt.Errorf("refinery blocked for fork rig %q (use --force to override)", rigName)
		}
	}

	fmt.Printf("Starting refinery for %s...\n", rigName)

	if err := mgr.Start(refineryForeground, refineryAgentOverride); err != nil {
		if err == refinery.ErrAlreadyRunning {
			fmt.Printf("%s Refinery is already running\n", style.Dim.Render("⚠"))
			// Still clear the operator-stop flag — the refinery is running
			// and the operator's intent here is "I want it running", so
			// leaving a stale stop flag would cause the daemon to kill it
			// on the next heartbeat.
			clearRefineryOperatorStop(r, rigName)
			return nil
		}
		if err == refinery.ErrDisabled {
			fmt.Printf("%s Refinery is disabled for %s (use 'gt refinery enable %s' to re-enable)\n",
				style.Dim.Render("⚠"), rigName, rigName)
			return nil
		}
		return fmt.Errorf("starting refinery: %w", err)
	}

	// Clear the operator-stop flag now that the refinery is running again.
	// Without this, the daemon's next heartbeat would see the lingering flag
	// and immediately kill the newly-started session. See gu-8ug1.
	clearRefineryOperatorStop(r, rigName)

	if refineryForeground {
		// This will block until stopped
		return nil
	}

	fmt.Printf("%s Refinery started for %s\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  %s\n", style.Dim.Render("Use 'gt refinery status' to check progress"))
	return nil
}

func runRefineryStop(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	mgr, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	stopErr := mgr.Stop()
	if stopErr != nil && stopErr != refinery.ErrNotRunning {
		return fmt.Errorf("stopping refinery: %w", stopErr)
	}

	// Record operator intent BEFORE reporting success so the daemon's next
	// heartbeat (which can fire immediately) reads the freshly-set flag and
	// does not resurrect the refinery. This is the fix for the SSH cert
	// expiry escalation loop: without it, `gt refinery stop` and the
	// daemon's `ensureRefineryRunning` fight each other every 3 minutes,
	// producing duplicate git-auth-failed escalations. See gu-8ug1.
	setRefineryOperatorStop(r, rigName)

	if stopErr == refinery.ErrNotRunning {
		fmt.Printf("%s Refinery is not running (flagged as operator-stopped so daemon will not auto-start)\n", style.Dim.Render("⚠"))
		return nil
	}

	fmt.Printf("%s Refinery stopped for %s\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  %s\n", style.Dim.Render("Daemon will not auto-restart. Run 'gt refinery start "+rigName+"' to resume."))
	return nil
}

// setRefineryOperatorStop records explicit operator-stop intent in the rig's
// wisp so the daemon's auto-restart path will skip this rig until the
// operator runs `gt refinery start`. Failures are logged but not returned —
// the Stop() itself already succeeded, and a missing flag only degrades back
// to the pre-fix behavior (daemon may resurrect the refinery on next tick).
func setRefineryOperatorStop(r *rig.Rig, rigName string) {
	if r == nil {
		return
	}
	townRoot := filepath.Dir(r.Path)
	if err := rig.SetRefineryStoppedByOperator(townRoot, rigName); err != nil {
		style.PrintWarning("could not persist operator-stop intent for %s: %v", rigName, err)
	}
}

// clearRefineryOperatorStop removes the operator-stop flag. Best-effort:
// a failure here means a daemon heartbeat between now and the next
// successful clear may kill the refinery, which the operator can fix by
// re-running `gt refinery start`. Still logged so the surprising behavior
// is traceable.
func clearRefineryOperatorStop(r *rig.Rig, rigName string) {
	if r == nil {
		return
	}
	townRoot := filepath.Dir(r.Path)
	if err := rig.ClearRefineryStoppedByOperator(townRoot, rigName); err != nil {
		style.PrintWarning("could not clear operator-stop flag for %s: %v", rigName, err)
	}
}

// RefineryStatusOutput is the JSON output format for refinery status.
//
// OperatorStopped surfaces the `refinery_stopped=true` flag that
// `gt refinery stop` persists in the rig's wisp. It is a distinct third
// state from Running (true/false) — a stopped-by-operator refinery is NOT
// "unresponsive"; it is intentionally offline (typically because an
// upstream prerequisite like SSH/Midway cert is broken). Automation such
// as the deacon patrol MUST check this field before deciding to restart,
// otherwise it re-creates the very SSH-cert escalation loop that gu-8ug1
// fixed (deacon → `gt refinery restart` → clears stop flag → refinery
// immediately fails git fetch → escalation spam → mayor stops again →
// deacon restarts again). See gu-i1z2 for the recurrence.
type RefineryStatusOutput struct {
	Running         bool   `json:"running"`
	RigName         string `json:"rig_name"`
	Session         string `json:"session,omitempty"`
	QueueLength     int    `json:"queue_length"`
	OperatorStopped bool   `json:"operator_stopped"`
}

func runRefineryStatus(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	mgr, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// ZFC: tmux is source of truth for running state
	running, _ := mgr.IsRunning()
	sessionInfo, _ := mgr.Status() // may be nil if not running

	// Get queue from beads
	queue, _ := mgr.Queue()
	queueLen := len(queue)

	// Report operator-stop intent alongside running state so automation (the
	// deacon patrol in particular) can distinguish "intentionally stopped"
	// from "unresponsive / crashed". Read from the rig's wisp via the same
	// helper the daemon's auto-restart path uses, so there is a single
	// source of truth for operator intent. Best-effort: if r is nil (rig
	// couldn't be resolved for some reason), we still report running state.
	operatorStopped := false
	if r != nil {
		townRoot := filepath.Dir(r.Path)
		operatorStopped = rig.IsRefineryStoppedByOperator(townRoot, rigName)
	}

	// JSON output
	if refineryStatusJSON {
		output := RefineryStatusOutput{
			Running:         running,
			RigName:         rigName,
			QueueLength:     queueLen,
			OperatorStopped: operatorStopped,
		}
		if sessionInfo != nil {
			output.Session = sessionInfo.Name
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Human-readable output
	fmt.Printf("%s Refinery: %s\n\n", style.Bold.Render("⚙"), rigName)

	if running {
		fmt.Printf("  State: %s\n", style.Bold.Render("● running"))
		if sessionInfo != nil {
			fmt.Printf("  Session: %s\n", sessionInfo.Name)
		}
	} else if operatorStopped {
		// Distinguish intentionally-stopped from dead/missing: surfaces
		// clearly in `gt refinery status` so the deacon patrol (and any
		// operator troubleshooting) reads the correct signal and does not
		// "heal" an intentional stop by restarting. See gu-i1z2.
		fmt.Printf("  State: %s\n", style.Dim.Render("○ stopped (operator-stopped)"))
		fmt.Printf("  %s\n", style.Dim.Render("Run 'gt refinery start "+rigName+"' to resume."))
	} else {
		fmt.Printf("  State: %s\n", style.Dim.Render("○ stopped"))
	}

	fmt.Printf("\n  Queue: %d pending\n", queueLen)

	return nil
}

func runRefineryQueue(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	mgr, _, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	queue, err := mgr.Queue()
	if err != nil {
		return fmt.Errorf("getting queue: %w", err)
	}

	// JSON output
	if refineryQueueJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(queue)
	}

	// Human-readable output
	fmt.Printf("%s Merge queue for '%s':\n\n", style.Bold.Render("📋"), rigName)

	if len(queue) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(empty)"))
		return nil
	}

	for _, item := range queue {
		status := ""
		prefix := fmt.Sprintf("  %d.", item.Position)

		if item.Position == 0 {
			prefix = "  ▶"
			status = style.Bold.Render("[processing]")
		} else {
			switch item.MR.Status {
			case refinery.MROpen:
				if item.MR.Error != "" {
					status = style.Dim.Render("[needs-rework]")
				} else {
					status = style.Dim.Render("[pending]")
				}
			case refinery.MRInProgress:
				status = style.Bold.Render("[processing]")
			case refinery.MRClosed:
				switch item.MR.CloseReason {
				case refinery.CloseReasonMerged:
					status = style.Bold.Render("[merged]")
				case refinery.CloseReasonRejected:
					status = style.Dim.Render("[rejected]")
				case refinery.CloseReasonConflict:
					status = style.Dim.Render("[conflict]")
				case refinery.CloseReasonSuperseded:
					status = style.Dim.Render("[superseded]")
				default:
					status = style.Dim.Render("[closed]")
				}
			}
		}

		issueInfo := ""
		if item.MR.IssueID != "" {
			issueInfo = fmt.Sprintf(" (%s)", item.MR.IssueID)
		}

		fmt.Printf("%s %s %s/%s%s %s\n",
			prefix,
			status,
			item.MR.Worker,
			item.MR.Branch,
			issueInfo,
			style.Dim.Render(item.Age))
	}

	return nil
}

func runRefineryAttach(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	// Use getRefineryManager to validate rig (and infer from cwd if needed)
	mgr, _, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Session name follows the same pattern as refinery manager
	sessionID := session.RefinerySessionName(session.PrefixFor(rigName))

	// Check if session exists
	t := tmux.NewTmux()
	running, err := t.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		// Auto-start if not running
		fmt.Printf("Refinery not running for %s, starting...\n", rigName)
		if err := mgr.Start(false, refineryAgentOverride); err != nil {
			return fmt.Errorf("starting refinery: %w", err)
		}
		fmt.Printf("%s Refinery started\n", style.Bold.Render("✓"))
	}

	// Attach to session using exec to properly forward TTY
	return attachToTmuxSession(sessionID)
}

func runRefineryRestart(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	mgr, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	if err := checkRigNotParkedOrDocked(rigName); err != nil {
		return err
	}

	// Refuse to restart the refinery on a fork rig unless --force is given.
	// Same guard as runRefineryStart: restarting would still merge to fork main.
	if !refineryForce {
		if rigCfg, err := rig.LoadRigConfig(r.Path); err == nil && rigCfg.UpstreamURL != "" {
			fmt.Println()
			fmt.Printf("  %s Refinery blocked for fork rig %q\n", style.Bold.Render("⛔"), rigName)
			fmt.Println()
			fmt.Printf("  This rig is a fork (upstream_url = %s).\n", rigCfg.UpstreamURL)
			fmt.Println("  Running the refinery merges branches to fork main, which breaks")
			fmt.Println("  fork↔upstream fast-forward sync and pollutes the fork's history.")
			fmt.Println()
			fmt.Printf("  To permanently disable the refinery for this rig: gt refinery disable %s\n", rigName)
			fmt.Printf("  To restart anyway (advanced / non-standard):      gt refinery restart %s --force\n", rigName)
			fmt.Println()
			return fmt.Errorf("refinery blocked for fork rig %q (use --force to override)", rigName)
		}
	}

	fmt.Printf("Restarting refinery for %s...\n", rigName)

	// Stop if running (ignore ErrNotRunning)
	if err := mgr.Stop(); err != nil && err != refinery.ErrNotRunning {
		return fmt.Errorf("stopping refinery: %w", err)
	}

	// Start fresh
	if err := mgr.Start(false, refineryAgentOverride); err != nil {
		if err == refinery.ErrDisabled {
			fmt.Printf("%s Refinery is disabled for %s (use 'gt refinery enable %s' to re-enable)\n",
				style.Dim.Render("⚠"), rigName, rigName)
			return nil
		}
		return fmt.Errorf("starting refinery: %w", err)
	}

	// `gt refinery restart` is an explicit "I want this running" intent, so
	// it must clear any operator-stop flag that a previous `gt refinery stop`
	// may have persisted. Otherwise the daemon's next heartbeat would kill
	// the freshly-restarted session. (Stop in this function is called on
	// the Manager directly, bypassing runRefineryStop's flag-setter, so no
	// flag is newly introduced here — we only clear pre-existing state.)
	clearRefineryOperatorStop(r, rigName)

	fmt.Printf("%s Refinery restarted for %s\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  %s\n", style.Dim.Render("Use 'gt refinery attach' to connect"))
	return nil
}

// getWorkerID returns the refinery worker ID from environment or default.
func getWorkerID() string {
	if id := os.Getenv("GT_REFINERY_WORKER"); id != "" {
		return id
	}
	return "refinery-1"
}

func runRefineryClaim(cmd *cobra.Command, args []string) error {
	mrID := args[0]
	workerID := getWorkerID()

	// Find beads from current working directory
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rigName, err := inferRigFromCwd(townRoot)
	if err != nil {
		return fmt.Errorf("could not determine rig: %w", err)
	}

	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	eng := refinery.NewEngineer(r)
	if err := eng.ClaimMR(mrID, workerID); err != nil {
		return fmt.Errorf("claiming MR: %w", err)
	}

	fmt.Printf("%s Claimed %s for %s\n", style.Bold.Render("✓"), mrID, workerID)
	return nil
}

func runRefineryRelease(cmd *cobra.Command, args []string) error {
	mrID := args[0]

	// Find beads from current working directory
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rigName, err := inferRigFromCwd(townRoot)
	if err != nil {
		return fmt.Errorf("could not determine rig: %w", err)
	}

	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	eng := refinery.NewEngineer(r)
	if err := eng.ReleaseMR(mrID); err != nil {
		return fmt.Errorf("releasing MR: %w", err)
	}

	fmt.Printf("%s Released %s back to queue\n", style.Bold.Render("✓"), mrID)
	return nil
}

func runRefineryUnclaimed(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	_, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Query beads for merge-request issues without assignee
	b := beads.New(r.Path)
	issues, err := b.ListMergeRequests(beads.ListOptions{
		Status:   "open",
		Label:    "gt:merge-request",
		Priority: -1,
	})
	if err != nil {
		return fmt.Errorf("listing merge requests: %w", err)
	}

	// Filter for unclaimed (no assignee)
	var unclaimed []*refinery.MRInfo
	for _, issue := range issues {
		if issue.Assignee != "" {
			continue
		}
		fields := beads.ParseMRFields(issue)
		if fields == nil {
			continue
		}
		mr := &refinery.MRInfo{
			ID:       issue.ID,
			Branch:   fields.Branch,
			Target:   fields.Target,
			Worker:   fields.Worker,
			Priority: issue.Priority,
		}
		unclaimed = append(unclaimed, mr)
	}

	// JSON output
	if refineryUnclaimedJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(unclaimed)
	}

	// Human-readable output
	fmt.Printf("%s Unclaimed MRs for '%s':\n\n", style.Bold.Render("📋"), rigName)

	if len(unclaimed) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(none available)"))
		return nil
	}

	for i, mr := range unclaimed {
		priority := fmt.Sprintf("P%d", mr.Priority)
		fmt.Printf("  %d. [%s] %s → %s\n", i+1, priority, mr.Branch, mr.Target)
		fmt.Printf("     ID: %s  Worker: %s\n", mr.ID, mr.Worker)
	}

	return nil
}

func runRefineryReady(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	_, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Create engineer for the rig (it has beads access for status checking)
	eng := refinery.NewEngineer(r)

	if refineryReadyAll {
		return runRefineryReadyAll(eng, rigName)
	}

	// Get ready MRs (unclaimed AND unblocked)
	ready, err := eng.ListReadyMRs()
	if err != nil {
		return fmt.Errorf("listing ready MRs: %w", err)
	}
	anomalies, err := eng.ListQueueAnomalies(time.Now())
	if err != nil {
		return fmt.Errorf("listing queue anomalies: %w", err)
	}

	// JSON output
	if refineryReadyJSON {
		type readyOutput struct {
			Ready     []*refinery.MRInfo    `json:"ready"`
			Anomalies []*refinery.MRAnomaly `json:"anomalies,omitempty"`
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(readyOutput{
			Ready:     ready,
			Anomalies: anomalies,
		})
	}

	// Human-readable output
	fmt.Printf("%s Ready MRs for '%s':\n\n", style.Bold.Render("🚀"), rigName)

	if len(ready) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(none ready)"))
		return nil
	}

	for i, mr := range ready {
		priority := fmt.Sprintf("P%d", mr.Priority)
		fmt.Printf("  %d. [%s] %s → %s\n", i+1, priority, mr.Branch, mr.Target)
		fmt.Printf("     ID: %s  Worker: %s\n", mr.ID, mr.Worker)
	}

	if len(anomalies) > 0 {
		fmt.Printf("\n%s Queue anomalies:\n\n", style.Bold.Render("⚠"))
		for i, anomaly := range anomalies {
			line := fmt.Sprintf("  %d. [%s] %s", i+1, anomaly.Type, anomaly.ID)
			fmt.Println(line)
			fmt.Printf("     Branch: %s\n", anomaly.Branch)
			if anomaly.Assignee != "" {
				fmt.Printf("     Assignee: %s\n", anomaly.Assignee)
			}
			if anomaly.Age > 0 {
				fmt.Printf("     Age: %s\n", anomaly.Age.Truncate(time.Second))
			}
			fmt.Printf("     Detail: %s\n", anomaly.Detail)
		}
	}

	return nil
}

func runRefineryReadyAll(eng *refinery.Engineer, rigName string) error {
	mrs, err := eng.ListAllOpenMRs()
	if err != nil {
		return fmt.Errorf("listing all open MRs: %w", err)
	}

	if refineryReadyJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(mrs)
	}

	// Human-readable output with assignee and updated_at
	fmt.Printf("%s All Open MRs for '%s':\n\n", style.Bold.Render("📋"), rigName)

	if len(mrs) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(none)"))
		return nil
	}

	for i, mr := range mrs {
		priority := fmt.Sprintf("P%d", mr.Priority)
		fmt.Printf("  %d. [%s] %s → %s\n", i+1, priority, mr.Branch, mr.Target)

		assignee := mr.Assignee
		if assignee == "" {
			assignee = "(unclaimed)"
		}
		age := ""
		if !mr.UpdatedAt.IsZero() {
			age = fmt.Sprintf(" (updated %s ago)", time.Since(mr.UpdatedAt).Truncate(time.Second))
		}
		fmt.Printf("     ID: %s  Worker: %s  Assignee: %s%s\n", mr.ID, mr.Worker, assignee, age)

		// Show branch status and blocked-by for --all mode
		var flags []string
		if mr.BlockedBy != "" {
			flags = append(flags, fmt.Sprintf("blocked-by:%s", mr.BlockedBy))
		}
		if !mr.BranchExistsLocal && !mr.BranchExistsRemote {
			flags = append(flags, "no-branch")
		}
		if len(flags) > 0 {
			fmt.Printf("     Flags: %s\n", style.Dim.Render(fmt.Sprintf("[%s]", strings.Join(flags, ", "))))
		}
	}

	return nil
}

func runRefineryBlocked(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	_, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Create engineer for the rig (it has beads access for status checking)
	eng := refinery.NewEngineer(r)

	// Get blocked MRs
	blocked, err := eng.ListBlockedMRs()
	if err != nil {
		return fmt.Errorf("listing blocked MRs: %w", err)
	}

	// JSON output
	if refineryBlockedJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(blocked)
	}

	// Human-readable output
	fmt.Printf("%s Blocked MRs for '%s':\n\n", style.Bold.Render("🚧"), rigName)

	if len(blocked) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(none blocked)"))
		return nil
	}

	for i, mr := range blocked {
		priority := fmt.Sprintf("P%d", mr.Priority)
		fmt.Printf("  %d. [%s] %s → %s\n", i+1, priority, mr.Branch, mr.Target)
		fmt.Printf("     ID: %s  Worker: %s\n", mr.ID, mr.Worker)
		if mr.BlockedBy != "" {
			fmt.Printf("     Blocked by: %s\n", mr.BlockedBy)
		}
	}

	return nil
}

// RefineryDiagnoseOutput is the JSON shape for `gt refinery diagnose --json`.
type RefineryDiagnoseOutput struct {
	Rig          string `json:"rig"`
	Worktree     string `json:"worktree"`
	Exists       bool   `json:"worktree_exists"`
	Branch       string `json:"current_branch,omitempty"`
	HasTemp      bool   `json:"has_temp_branch"`
	TempUpstream string `json:"temp_upstream,omitempty"`
	Wedged       bool   `json:"wedged"`
	Reason       string `json:"reason,omitempty"`
	Fixed        bool   `json:"fixed,omitempty"`
}

func runRefineryDiagnose(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	_, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	worktree := refinery.RefineryWorktreePath(r)
	status, detectErr := refinery.DetectWedge(worktree)
	if detectErr != nil {
		return fmt.Errorf("detect wedge: %w", detectErr)
	}

	out := RefineryDiagnoseOutput{
		Rig:          rigName,
		Worktree:     worktree,
		Exists:       status.Exists,
		Branch:       status.CurrentBranch,
		HasTemp:      status.HasTempBranch,
		TempUpstream: status.TempUpstream,
		Wedged:       status.Wedged(),
		Reason:       status.Reason,
	}

	if refineryDiagnoseFix && status.Wedged() {
		// Apply repair, then re-detect to verify and surface the new state.
		if fixErr := refinery.UnwedgeWorktree(worktree, r.DefaultBranch(), os.Stdout); fixErr != nil {
			return fmt.Errorf("unwedge: %w", fixErr)
		}
		out.Fixed = true
		// Re-detect for accurate post-fix output.
		if post, err := refinery.DetectWedge(worktree); err == nil {
			out.Branch = post.CurrentBranch
			out.HasTemp = post.HasTempBranch
			out.TempUpstream = post.TempUpstream
			out.Wedged = post.Wedged()
			out.Reason = post.Reason
		}
	}

	if refineryDiagnoseJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("%s Refinery diagnose: %s\n\n", style.Bold.Render("🩺"), rigName)
	fmt.Printf("  Worktree: %s\n", out.Worktree)
	if !out.Exists {
		fmt.Printf("  %s worktree directory does not exist\n", style.Dim.Render("○"))
		return nil
	}
	if out.Branch != "" {
		fmt.Printf("  Branch:   %s\n", out.Branch)
	}
	fmt.Printf("  temp:     ")
	if out.HasTemp {
		if out.TempUpstream != "" {
			fmt.Printf("present (tracks %s)\n", out.TempUpstream)
		} else {
			fmt.Printf("present (no upstream)\n")
		}
	} else {
		fmt.Printf("absent\n")
	}

	if out.Wedged {
		fmt.Printf("\n  %s %s\n", style.Bold.Render("⚠ WEDGED:"), out.Reason)
		if !refineryDiagnoseFix {
			fmt.Printf("  %s\n", style.Dim.Render("Run with --fix to clear in place."))
		}
	} else {
		if out.Fixed {
			fmt.Printf("\n  %s wedge cleared (%s)\n", style.Bold.Render("✓"), out.Reason)
		} else {
			fmt.Printf("\n  %s healthy: %s\n", style.Bold.Render("✓"), out.Reason)
		}
	}
	return nil
}

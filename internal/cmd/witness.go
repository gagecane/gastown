package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Witness command flags
var (
	witnessForeground    bool
	witnessStatusJSON    bool
	witnessAgentOverride string
	witnessEnvOverrides  []string
)

var witnessCmd = &cobra.Command{
	Use:     "witness",
	GroupID: GroupAgents,
	Short:   "Manage the Witness (per-rig polecat health monitor)",
	RunE:    requireSubcommand,
	Long: `Manage the Witness - the per-rig polecat health monitor.

The Witness patrols a single rig, watching over its polecats:
  - Detects stalled polecats (crashed or stuck mid-work)
  - Nudges unresponsive sessions back to life
  - Cleans up zombie polecats (finished but failed to exit)
  - Nukes sandboxes when polecats complete via 'gt done'

The Witness does NOT force session cycles or interrupt working polecats.
Polecats manage their own sessions (via gt handoff). The Witness handles
failures and edge cases only.

One Witness per rig. The Deacon monitors all Witnesses.

Role shortcuts: "witness" in mail/nudge addresses resolves to this rig's Witness.`,
}

var witnessStartCmd = &cobra.Command{
	Use:     "start <rig>",
	Aliases: []string{"spawn"},
	Short:   "Start the witness",
	Long: `Start the Witness for a rig.

Launches the monitoring agent which watches for stuck polecats and orphaned
sandboxes, taking action to keep work flowing.

Self-Cleaning Model: Polecats nuke themselves after work. The Witness handles
crash recovery (restart with hooked work) and orphan cleanup (nuke abandoned
sandboxes). There is no "idle" state - polecats either have work or don't exist.

Examples:
  gt witness start greenplace
  gt witness start greenplace --agent codex
  gt witness start greenplace --env ANTHROPIC_MODEL=claude-3-haiku`,
	Args: cobra.ExactArgs(1),
	RunE: runWitnessStart,
}

var witnessStopCmd = &cobra.Command{
	Use:   "stop <rig>",
	Short: "Stop the witness",
	Long: `Stop a running Witness.

Gracefully stops the witness monitoring agent.`,
	Args: cobra.ExactArgs(1),
	RunE: runWitnessStop,
}

var witnessStatusCmd = &cobra.Command{
	Use:   "status <rig>",
	Short: "Show witness status",
	Long: `Show the status of a rig's Witness.

Displays running state, monitored polecats, and statistics.`,
	Args: cobra.ExactArgs(1),
	RunE: runWitnessStatus,
}

var witnessAttachCmd = &cobra.Command{
	Use:     "attach [rig]",
	Aliases: []string{"at"},
	Short:   "Attach to witness session",
	Long: `Attach to the Witness tmux session for a rig.

Attaches the current terminal to the witness's tmux session.
Detach with Ctrl-B D.

If the witness is not running, this will start it first.
If rig is not specified, infers it from the current directory.

Examples:
  gt witness attach greenplace
  gt witness attach          # infer rig from cwd`,
	Args: cobra.MaximumNArgs(1),
	RunE: runWitnessAttach,
}

var witnessRestartCmd = &cobra.Command{
	Use:   "restart <rig>",
	Short: "Restart the witness",
	Long: `Restart the Witness for a rig.

Stops the current session (if running) and starts a fresh one.

Examples:
  gt witness restart greenplace
  gt witness restart greenplace --agent codex
  gt witness restart greenplace --env ANTHROPIC_MODEL=claude-3-haiku`,
	Args: cobra.ExactArgs(1),
	RunE: runWitnessRestart,
}

func init() {
	// Start flags
	witnessStartCmd.Flags().BoolVar(&witnessForeground, "foreground", false, "Run in foreground (default: background)")
	_ = witnessStartCmd.Flags().MarkHidden("foreground")
	witnessStartCmd.Flags().StringVar(&witnessAgentOverride, "agent", "", "Agent alias to run the Witness with (overrides town default)")
	witnessStartCmd.Flags().StringArrayVar(&witnessEnvOverrides, "env", nil, "Environment variable override (KEY=VALUE, can be repeated)")

	// Status flags
	witnessStatusCmd.Flags().BoolVar(&witnessStatusJSON, "json", false, "Output as JSON")

	// Restart flags
	witnessRestartCmd.Flags().StringVar(&witnessAgentOverride, "agent", "", "Agent alias to run the Witness with (overrides town default)")
	witnessRestartCmd.Flags().StringArrayVar(&witnessEnvOverrides, "env", nil, "Environment variable override (KEY=VALUE, can be repeated)")

	// Add subcommands
	witnessCmd.AddCommand(witnessStartCmd)
	witnessCmd.AddCommand(witnessStopCmd)
	witnessCmd.AddCommand(witnessRestartCmd)
	witnessCmd.AddCommand(witnessStatusCmd)
	witnessCmd.AddCommand(witnessAttachCmd)

	rootCmd.AddCommand(witnessCmd)
}

// getWitnessManager creates a witness manager for a rig.
func getWitnessManager(rigName string) (*witness.Manager, error) {
	_, r, err := getRig(rigName)
	if err != nil {
		return nil, err
	}

	mgr := witness.NewManager(r)
	return mgr, nil
}

func runWitnessStart(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	if err := checkRigNotParkedOrDocked(rigName); err != nil {
		return err
	}

	mgr, err := getWitnessManager(rigName)
	if err != nil {
		return err
	}
	if witnessForeground {
		return fmt.Errorf("foreground mode is deprecated; use background mode (remove --foreground flag)")
	}

	fmt.Printf("Starting witness for %s...\n", rigName)

	if err := mgr.Start(witnessForeground, witnessAgentOverride, witnessEnvOverrides); err != nil {
		if err == witness.ErrAlreadyRunning {
			fmt.Printf("%s Witness is already running\n", style.Dim.Render("⚠"))
			fmt.Printf("  %s\n", style.Dim.Render("Use 'gt witness attach' to connect"))
			return nil
		}
		return fmt.Errorf("starting witness: %w", err)
	}

	fmt.Printf("%s Witness started for %s\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  %s\n", style.Dim.Render("Use 'gt witness attach' to connect"))
	fmt.Printf("  %s\n", style.Dim.Render("Use 'gt witness status' to check progress"))
	return nil
}

func runWitnessStop(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	mgr, err := getWitnessManager(rigName)
	if err != nil {
		return err
	}

	// Kill tmux session if it exists.
	// Use KillSessionWithProcesses to ensure all descendant processes are killed.
	t := tmux.NewTmux()
	sessionName := witnessSessionName(rigName)
	running, _ := t.HasSession(sessionName)
	if running {
		if err := t.KillSessionWithProcesses(sessionName); err != nil {
			style.PrintWarning("failed to kill session: %v", err)
		}
	}

	// Update state file
	if err := mgr.Stop(); err != nil {
		if err == witness.ErrNotRunning && !running {
			fmt.Printf("%s Witness is not running\n", style.Dim.Render("⚠"))
			return nil
		}
		// Even if manager.Stop fails, if we killed the session it's stopped
		if !running {
			return fmt.Errorf("stopping witness: %w", err)
		}
	}

	fmt.Printf("%s Witness stopped for %s\n", style.Bold.Render("✓"), rigName)
	return nil
}

// WitnessStatusOutput is the JSON output format for witness status.
type WitnessStatusOutput struct {
	Running           bool                  `json:"running"`
	RigName           string                `json:"rig_name"`
	Session           string                `json:"session,omitempty"`
	MonitoredPolecats []string              `json:"monitored_polecats,omitempty"`
	// cv-p3fem Phase 3: per-session liveness verdicts. Includes the witness's
	// own verdict plus one entry per monitored polecat so operators can scan
	// the supervisor question ("which agents are live?") in one pass.
	Liveness []PolecatLivenessRow `json:"liveness,omitempty"`
}

// PolecatLivenessRow is a single liveness row for the witness status JSON
// output. cv-p3fem Phase 3 plugin contract — additive only.
type PolecatLivenessRow struct {
	Session       string `json:"session"`
	Role          string `json:"role"`
	Verdict       string `json:"verdict"`
	VerdictReason string `json:"verdict_reason"`
	State         string `json:"state,omitempty"`
	Bead          string `json:"bead,omitempty"`
	AgeSeconds    int64  `json:"age_seconds"`
}

func runWitnessStatus(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	// Get rig for polecat info
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	mgr := witness.NewManager(r)

	// ZFC: tmux is source of truth for running state
	running, _ := mgr.IsRunning()
	sessionInfo, _ := mgr.Status() // may be nil if not running

	// Polecats come from rig config, not state file
	polecats := r.Polecats

	// cv-p3fem Phase 3: gather per-session liveness verdicts for the witness
	// itself plus each monitored polecat. Best-effort: errors silently
	// skipped (a missing town root or unreadable heartbeat shouldn't break
	// the status command).
	livenessRows := collectLivenessRows(rigName, polecats)

	// JSON output
	if witnessStatusJSON {
		output := WitnessStatusOutput{
			Running:           running,
			RigName:           rigName,
			MonitoredPolecats: polecats,
			Liveness:          livenessRows,
		}
		if sessionInfo != nil {
			output.Session = sessionInfo.Name
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Human-readable output
	fmt.Printf("%s Witness: %s\n\n", style.Bold.Render(AgentTypeIcons[AgentWitness]), rigName)

	if running {
		fmt.Printf("  State: %s\n", style.Bold.Render("● running"))
		if sessionInfo != nil {
			fmt.Printf("  Session: %s\n", sessionInfo.Name)
		}
	} else {
		fmt.Printf("  State: %s\n", style.Dim.Render("○ stopped"))
	}

	// Liveness column (cv-p3fem Phase 3): supervisor question first.
	if len(livenessRows) > 0 {
		fmt.Printf("\n  %s\n", style.Bold.Render("Liveness:"))
		for _, row := range livenessRows {
			ageStr := (time.Duration(row.AgeSeconds) * time.Second).Truncate(time.Second).String()
			extras := ""
			if row.State != "" {
				extras += fmt.Sprintf(" state=%s", row.State)
			}
			if row.Bead != "" {
				extras += fmt.Sprintf(" bead=%s", row.Bead)
			}
			fmt.Printf("    %-30s %-12s age=%s%s\n", row.Session, row.Verdict, ageStr, extras)
		}
	}

	// Show monitored polecats
	fmt.Printf("\n  %s\n", style.Bold.Render("Monitored Polecats:"))
	if len(polecats) == 0 {
		fmt.Printf("    %s\n", style.Dim.Render("(none)"))
	} else {
		for _, p := range polecats {
			fmt.Printf("    • %s\n", p)
		}
	}

	return nil
}

// collectLivenessRows gathers liveness verdicts for the witness session and
// each monitored polecat in the rig. Best-effort: an unfindable town root
// or unreadable heartbeat is silently skipped so the status command never
// fails just because liveness cannot be computed.
func collectLivenessRows(rigName string, polecats []string) []PolecatLivenessRow {
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return nil
	}
	prefix := session.PrefixFor(rigName)

	var rows []PolecatLivenessRow
	// Witness self
	witnessSession := session.WitnessSessionName(prefix)
	rows = append(rows, livenessRowFor(townRoot, witnessSession, "witness", polecat.DefaultWitnessLivenessThresholds))
	// Refinery (if any)
	refinerySession := session.RefinerySessionName(prefix)
	if polecat.ReadSessionHeartbeat(townRoot, refinerySession) != nil {
		rows = append(rows, livenessRowFor(townRoot, refinerySession, "refinery", polecat.DefaultRefineryLivenessThresholds))
	}
	// Monitored polecats
	for _, p := range polecats {
		sess := session.PolecatSessionName(prefix, p)
		rows = append(rows, livenessRowFor(townRoot, sess, "polecat", polecat.DefaultLivenessThresholds))
	}
	return rows
}

// livenessRowFor materializes a single liveness row. Used by witness status.
func livenessRowFor(townRoot, sessionName, role string, thresholds polecat.LivenessThresholds) PolecatLivenessRow {
	rep := polecat.Liveness(townRoot, sessionName, thresholds)
	return PolecatLivenessRow{
		Session:       sessionName,
		Role:          role,
		Verdict:       string(rep.Verdict),
		VerdictReason: rep.VerdictReason,
		State:         string(rep.State),
		Bead:          rep.Bead,
		AgeSeconds:    rep.AgeSeconds,
	}
}

// witnessSessionName returns the tmux session name for a rig's witness.
func witnessSessionName(rigName string) string {
	return session.WitnessSessionName(session.PrefixFor(rigName))
}

func runWitnessAttach(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}

	// Infer rig from cwd if not provided
	if rigName == "" {
		townRoot, err := workspace.FindFromCwdOrError()
		if err != nil {
			return fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
		rigName, err = inferRigFromCwd(townRoot)
		if err != nil {
			return fmt.Errorf("could not determine rig: %w\nUsage: gt witness attach <rig>", err)
		}
	}

	// Verify rig exists and get manager
	mgr, err := getWitnessManager(rigName)
	if err != nil {
		return err
	}

	sessionName := witnessSessionName(rigName)

	// Ensure session exists (creates if needed)
	if err := mgr.Start(false, "", nil); err != nil && err != witness.ErrAlreadyRunning {
		return err
	} else if err == nil {
		fmt.Printf("Started witness session for %s\n", rigName)
	}

	// Attach to the session (socket-aware: uses the town's tmux socket).
	return attachToTmuxSession(sessionName)
}

func runWitnessRestart(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	if err := checkRigNotParkedOrDocked(rigName); err != nil {
		return err
	}

	mgr, err := getWitnessManager(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Restarting witness for %s...\n", rigName)

	// Stop existing session (non-fatal: may not be running)
	_ = mgr.Stop()

	// Start fresh
	if err := mgr.Start(false, witnessAgentOverride, witnessEnvOverrides); err != nil {
		return fmt.Errorf("starting witness: %w", err)
	}

	fmt.Printf("%s Witness restarted for %s\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  %s\n", style.Dim.Render("Use 'gt witness attach' to connect"))
	return nil
}

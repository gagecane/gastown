package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

// refineryMQProber adapts a rig's beads handle to witness.MergeQueueProber.
// It reuses pendingMRsForRig so the merge-queue rig-scoping logic stays
// single-sourced with the queue-scan verification gate (gu-6hzv). Used by the
// stale-rig-agent scan to suppress false STALE_RIG_AGENT escalations for an
// idle refinery whose queue is empty (gs-ecdg).
type refineryMQProber struct {
	lister mrLister
}

// PendingMergeRequestCount returns the number of actionable (open, unblocked)
// MRs in the rig's merge queue.
func (p refineryMQProber) PendingMergeRequestCount(rigName string) (int, error) {
	issues, err := p.lister.ListMergeRequests(beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "open",
		Priority: -1,
	})
	if err != nil {
		return 0, err
	}
	return len(pendingMRsForRig(issues, rigName)), nil
}

var (
	patrolScanJSON    bool
	patrolScanNotify  bool
	patrolScanRig     string
	patrolScanVerbose bool
)

var patrolScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan polecats for zombies, stalls, and completions",
	Long: `Run proactive detection across all polecats in a rig.

This command bridges the witness library detection functions to the CLI,
providing a single command for the survey-workers patrol step.

Detections:
  - Zombies: Dead sessions with active agent state, dead agent processes,
    stuck done-intent, closed beads with live sessions
  - Stalls: Agents stuck at startup prompts
  - Completions: Agent bead metadata indicating gt done was called

Actions taken automatically:
  - Zombie restart: Sessions are restarted (not nuked) to preserve worktrees
  - Cleanup wisps: Created for dirty state tracking
  - Completion routing: MR cleanup wisps created, refinery nudged

Use --notify to send mail when zombies with active work are detected.

Examples:
  gt patrol scan                    # Scan current rig
  gt patrol scan --rig gastown      # Scan specific rig
  gt patrol scan --json             # Machine-readable output
  gt patrol scan --notify           # Send mail on zombie detection`,
	RunE: runPatrolScan,
}

func init() {
	patrolScanCmd.Flags().BoolVar(&patrolScanJSON, "json", false, "Output as JSON")
	patrolScanCmd.Flags().BoolVar(&patrolScanNotify, "notify", false, "Send mail to witness/mayor when active-work zombies are detected")
	patrolScanCmd.Flags().StringVar(&patrolScanRig, "rig", "", "Rig to scan (default: infer from cwd or GT_RIG)")
	patrolScanCmd.Flags().BoolVarP(&patrolScanVerbose, "verbose", "v", false, "Verbose output")

	patrolCmd.AddCommand(patrolScanCmd)
}

// PatrolScanOutput is the JSON output format for patrol scan results.
type PatrolScanOutput struct {
	Rig            string                      `json:"rig"`
	Timestamp      string                      `json:"timestamp"`
	Zombies        *PatrolScanZombieOutput     `json:"zombies"`
	Stalls         *PatrolScanStallOutput      `json:"stalls,omitempty"`
	Completions    *PatrolScanCompleteOutput   `json:"completions,omitempty"`
	PostHoc        *PatrolScanPostHocOutput    `json:"post_hoc_completions,omitempty"`
	Stranded       *PatrolScanStrandedOutput   `json:"stranded_assignees,omitempty"`
	StaleRigAgents *PatrolScanStaleRigAgentOut `json:"stale_rig_agents,omitempty"`
	FalseDeferred  *PatrolScanFalseDeferredOut `json:"false_deferred,omitempty"`
	StaleParks     *PatrolScanStaleParkOut     `json:"stale_parks,omitempty"`
	Receipts       []witness.PatrolReceipt     `json:"receipts,omitempty"`
}

// PatrolScanStaleParkOut holds stale-park recovery results (gs-du4h).
type PatrolScanStaleParkOut struct {
	Checked   int                       `json:"checked"`
	Found     int                       `json:"found"`
	Recovered []PatrolScanStaleParkItem `json:"recovered,omitempty"`
	Errors    []string                  `json:"errors,omitempty"`
}

// PatrolScanStaleParkItem is a single stale-park recovery in scan output.
type PatrolScanStaleParkItem struct {
	BeadID           string   `json:"bead_id"`
	ResolvedBlockers []string `json:"resolved_blockers,omitempty"`
	DetachedMolecule string   `json:"detached_molecule,omitempty"`
	Unblocked        bool     `json:"unblocked"`
	Error            string   `json:"error,omitempty"`
}

// PatrolScanFalseDeferredOut holds false-deferred recovery results (gu-wykt).
type PatrolScanFalseDeferredOut struct {
	Checked   int                           `json:"checked"`
	Found     int                           `json:"found"`
	Recovered []PatrolScanFalseDeferredItem `json:"recovered,omitempty"`
	Errors    []string                      `json:"errors,omitempty"`
}

// PatrolScanFalseDeferredItem is a single false-deferred recovery in scan output.
type PatrolScanFalseDeferredItem struct {
	BeadID         string `json:"bead_id"`
	CitedCommitSHA string `json:"cited_commit_sha,omitempty"`
	Action         string `json:"action"`
	Error          string `json:"error,omitempty"`
}

// PatrolScanStaleRigAgentOut holds rig-agent staleness detection results
// (gu-0nmw).
type PatrolScanStaleRigAgentOut struct {
	Checked int                           `json:"checked"`
	Found   int                           `json:"found"`
	Items   []PatrolScanStaleRigAgentItem `json:"items,omitempty"`
	Errors  []string                      `json:"errors,omitempty"`
}

// PatrolScanStaleRigAgentItem is a single stale rig-agent in scan output.
type PatrolScanStaleRigAgentItem struct {
	Role             string `json:"role"`
	Session          string `json:"session"`
	HeartbeatAgeSecs int64  `json:"heartbeat_age_seconds"`
	HeartbeatMissing bool   `json:"heartbeat_missing"`
	SessionAlive     bool   `json:"session_alive"`
	Action           string `json:"action"`
	CorrelatedInto   string `json:"correlated_into,omitempty"`
	MailSent         bool   `json:"mail_sent"`
	Error            string `json:"error,omitempty"`
}

// PatrolScanZombieOutput holds zombie detection results.
type PatrolScanZombieOutput struct {
	Checked int                    `json:"checked"`
	Found   int                    `json:"found"`
	Zombies []PatrolScanZombieItem `json:"zombies,omitempty"`
	Errors  []string               `json:"errors,omitempty"`
}

// PatrolScanZombieItem is a single zombie detection in scan output.
type PatrolScanZombieItem struct {
	Polecat        string `json:"polecat"`
	Classification string `json:"classification"`
	AgentState     string `json:"agent_state"`
	HookBead       string `json:"hook_bead,omitempty"`
	CleanupStatus  string `json:"cleanup_status,omitempty"`
	Action         string `json:"action"`
	WasActive      bool   `json:"was_active"`
	Error          string `json:"error,omitempty"`
}

// PatrolScanStallOutput holds stall detection results.
type PatrolScanStallOutput struct {
	Checked int                   `json:"checked"`
	Found   int                   `json:"found"`
	Stalls  []PatrolScanStallItem `json:"stalls,omitempty"`
}

// PatrolScanStallItem is a single stall detection in scan output.
type PatrolScanStallItem struct {
	Polecat   string `json:"polecat"`
	StallType string `json:"stall_type"`
	Action    string `json:"action"`
	Error     string `json:"error,omitempty"`
}

// PatrolScanCompleteOutput holds completion discovery results.
type PatrolScanCompleteOutput struct {
	Checked   int                      `json:"checked"`
	Found     int                      `json:"found"`
	Completed []PatrolScanCompleteItem `json:"completed,omitempty"`
}

// PatrolScanCompleteItem is a single completion discovery in scan output.
type PatrolScanCompleteItem struct {
	Polecat        string `json:"polecat"`
	ExitType       string `json:"exit_type"`
	IssueID        string `json:"issue_id,omitempty"`
	MRID           string `json:"mr_id,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Action         string `json:"action"`
	WispCreated    string `json:"wisp_created,omitempty"`
	CompletionTime string `json:"completion_time,omitempty"`
}

// PatrolScanPostHocOutput holds post-hoc completion discovery results (gu-jr8).
type PatrolScanPostHocOutput struct {
	Checked int                     `json:"checked"`
	Found   int                     `json:"found"`
	Items   []PatrolScanPostHocItem `json:"items,omitempty"`
	Errors  []string                `json:"errors,omitempty"`
}

// PatrolScanPostHocItem is a single post-hoc completion in scan output.
type PatrolScanPostHocItem struct {
	Polecat   string `json:"polecat"`
	AgentBead string `json:"agent_bead"`
	HookBead  string `json:"hook_bead"`
	Action    string `json:"action"`
	Error     string `json:"error,omitempty"`
}

// PatrolScanStrandedOutput holds stranded-assignee detection results (gu-wwyq).
type PatrolScanStrandedOutput struct {
	Checked  int                      `json:"checked"`
	Found    int                      `json:"found"`
	Stranded []PatrolScanStrandedItem `json:"stranded,omitempty"`
	Errors   []string                 `json:"errors,omitempty"`
}

// PatrolScanStrandedItem is a single stranded-assignee detection in scan output.
type PatrolScanStrandedItem struct {
	BeadID   string `json:"bead_id"`
	Polecat  string `json:"polecat"`
	Assignee string `json:"assignee"`
	AgeSecs  int64  `json:"age_seconds"`
	Action   string `json:"action"`
	MailSent bool   `json:"mail_sent"`
	Error    string `json:"error,omitempty"`
}

func runPatrolScan(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine rig name
	rigName := patrolScanRig
	if rigName == "" {
		// Try GT_RIG env, then infer from cwd
		rigName = os.Getenv("GT_RIG")
		if rigName == "" {
			rigName, err = inferRigFromCwd(townRoot)
			if err != nil {
				return fmt.Errorf("could not determine rig: %w\nUse --rig to specify", err)
			}
		}
	}

	bd := witness.DefaultBdCli()
	router := mail.NewRouter(townRoot)
	workDir := townRoot

	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Run all four detection passes.
	// Note: DetectZombiePolecats takes a router param but does NOT send mail
	// internally — it only uses the router for workspace context. Notifications
	// are sent exclusively below via --notify, avoiding double-send.
	//
	// Ordering matters (gu-1ord): DiscoverPostHocCompletions runs BEFORE
	// DetectZombiePolecats so that polecats whose work has already merged to
	// mainline have their hook bead closed first. Otherwise the zombie scan
	// sees an open hook bead and a dead session, classifies the polecat as
	// ZombieSessionDeadActive, and emits POLECAT_DIED escalations for work
	// that has already landed — the spawn-storm loop documented on this bead.
	completionResult := witness.DiscoverCompletions(bd, workDir, rigName, router)
	postHocResult := witness.DiscoverPostHocCompletions(bd, workDir, rigName)
	zombieResult := witness.DetectZombiePolecats(bd, workDir, rigName, router)
	stallResult := witness.DetectStalledPolecats(workDir, rigName)

	// Steady-state stranded-assignee detection (gu-wwyq). Runs after the
	// transition-based zombie scan so that polecats observed alive→dead this
	// cycle have a chance to take the existing path first; this scan catches
	// the rest (already-dead-at-boot, dir-already-nuked, dead-between-cycles).
	witnessCfg := config.LoadOperationalConfig(townRoot).GetWitnessConfig()
	staleThreshold := witnessCfg.StaleInProgressThresholdD()
	strandedResult := witness.DetectStaleInProgressBeads(bd, workDir, rigName, router, staleThreshold)

	// Stale rig-agent heartbeat detection (gu-0nmw). Catches the case where
	// the refinery (or witness) process is up but the agent loop is wedged,
	// or where the daemon supervisor missed a restart and the heartbeat sat
	// untouched for hours.
	staleAgentThreshold := witnessCfg.StaleRigAgentHeartbeatD()
	// Cooldown between repeated escalations for the same unchanged stale agent,
	// so the witness stops re-mailing mayor every patrol cycle (gu-z8qzq).
	staleAgentCooldown := witnessCfg.StaleRigAgentNotifyCooldownD()
	// Town-wide window over which STALE_RIG_AGENT escalations from DIFFERENT
	// rigs fold into a single thread, so a town-wide incident produces one
	// escalation instead of M independent HIGH mails to mayor (gu-nejgh).
	staleAgentCorrelationWindow := witnessCfg.StaleRigAgentCorrelationWindowD()
	// Pass $GT_SESSION so the detector never escalates the scanning agent's
	// own heartbeat — the self-amplifying STALE_RIG_AGENT flood guard (gu-vqmmp).
	//
	// Build a merge-queue prober so the detector can suppress false
	// STALE_RIG_AGENT escalations for an idle refinery whose queue is empty
	// (gs-ecdg). The MRs live in the rig's beads DB (typically a redirect to
	// mayor/rig/.beads), so query that, not the town root. If the rig can't be
	// resolved, leave the prober nil — the detector then escalates as before.
	var mqProber witness.MergeQueueProber
	if _, r, rigErr := getRig(rigName); rigErr == nil && r != nil {
		mqProber = refineryMQProber{lister: beads.New(r.BeadsPath())}
	}
	staleAgentResult := witness.DetectStaleRigAgentHeartbeats(workDir, rigName, router, staleAgentThreshold, os.Getenv("GT_SESSION"), staleAgentCooldown, staleAgentCorrelationWindow, mqProber)

	// False-deferred bead recovery (gu-wykt). Beads that are status=deferred
	// but whose work has shipped on origin/<default> with a commit citing the
	// bead ID — auto-close them with the cited SHA. Sibling to gu-551r
	// (Pattern A close-validation) but for the deferred-state escape hatch.
	falseDeferredResult := witness.DiscoverDeferredButShipped(bd, workDir, rigName)

	// Stale-park recovery (gs-du4h). Beads parked at status=blocked whose
	// blocking dependencies have all closed — bd does not auto-flip the
	// dependent back to open or drop the satisfied dep edge, so a ready bead
	// stays BLOCKED forever. Unblock it (drop closed blocker edges + stale
	// molecule bond, flip to open, nudge deacon).
	staleParkResult := witness.DetectStaleParkedBeads(bd, workDir, rigName)

	// Build patrol receipts for zombies
	receipts := witness.BuildPatrolReceipts(rigName, zombieResult)

	// Notify when zombies with active work are detected.
	// Always notify the mayor for active-work zombies (dead polecats with hooked
	// beads) — this is the primary mechanism for detecting failed work. (GH #3584)
	// Use --notify=false to suppress (e.g., in dry-run/testing contexts).
	if zombieResult != nil {
		activeZombies := countActiveWorkZombies(zombieResult)
		if activeZombies > 0 {
			sendZombieNotification(router, rigName, zombieResult, activeZombies)
		}
	}

	if patrolScanJSON {
		return outputPatrolScanJSON(rigName, timestamp, zombieResult, stallResult, completionResult, postHocResult, strandedResult, staleAgentResult, falseDeferredResult, staleParkResult, receipts)
	}

	return outputPatrolScanHuman(rigName, zombieResult, stallResult, completionResult, postHocResult, strandedResult, staleAgentResult, falseDeferredResult, staleParkResult, receipts)
}

func countActiveWorkZombies(result *witness.DetectZombiePolecatsResult) int {
	count := 0
	for _, z := range result.Zombies {
		if z.WasActive {
			count++
		}
	}
	return count
}

func sendZombieNotification(router *mail.Router, rigName string, result *witness.DetectZombiePolecatsResult, activeCount int) {
	var lines []string
	lines = append(lines, fmt.Sprintf("Patrol scan detected %d zombie(s) with active work in rig %s:", activeCount, rigName))
	lines = append(lines, "")
	for _, z := range result.Zombies {
		if !z.WasActive {
			continue
		}
		line := fmt.Sprintf("- %s: %s (hook=%s, action=%s)",
			z.PolecatName, string(z.Classification), z.HookBead, z.Action)
		if z.Error != nil {
			line += fmt.Sprintf(" [error: %v]", z.Error)
		}
		lines = append(lines, line)
	}

	body := strings.Join(lines, "\n")
	subject := fmt.Sprintf("ZOMBIE_DETECTED: %d active-work zombie(s) in %s", activeCount, rigName)

	// Send to witness (best-effort)
	witMsg := &mail.Message{
		From:    fmt.Sprintf("%s/witness", rigName),
		To:      fmt.Sprintf("%s/witness", rigName),
		Subject: subject,
		Body:    body,
	}
	_ = router.Send(witMsg)

	// Also notify the mayor so dead polecats don't go unnoticed. (GH #3584)
	// The mayor needs to know so work can be reslung.
	mayorBody := strings.Join(lines, "\n") +
		"\n\nResling instructions:\n" +
		"  gt sling <bead-id> <rig> --create --force"
	mayorMsg := &mail.Message{
		From:    fmt.Sprintf("%s/witness", rigName),
		To:      "mayor/",
		Subject: fmt.Sprintf("POLECAT_DIED: %d polecat(s) died with active work in %s", activeCount, rigName),
		Body:    mayorBody,
	}
	_ = router.Send(mayorMsg)
}

func outputPatrolScanJSON(rigName, timestamp string, zombieResult *witness.DetectZombiePolecatsResult, stallResult *witness.DetectStalledPolecatsResult, completionResult *witness.DiscoverCompletionsResult, postHocResult *witness.DiscoverPostHocCompletionsResult, strandedResult *witness.DetectStaleInProgressBeadsResult, staleAgentResult *witness.DetectStaleRigAgentHeartbeatsResult, falseDeferredResult *witness.DiscoverDeferredButShippedResult, staleParkResult *witness.DetectStaleParkedBeadsResult, receipts []witness.PatrolReceipt) error {
	output := PatrolScanOutput{
		Rig:       rigName,
		Timestamp: timestamp,
		Receipts:  receipts,
	}

	// Zombies
	if zombieResult != nil {
		zo := &PatrolScanZombieOutput{
			Checked: zombieResult.Checked,
			Found:   len(zombieResult.Zombies),
		}
		for _, z := range zombieResult.Zombies {
			item := PatrolScanZombieItem{
				Polecat:        z.PolecatName,
				Classification: string(z.Classification),
				AgentState:     z.AgentState,
				HookBead:       z.HookBead,
				CleanupStatus:  z.CleanupStatus,
				Action:         z.Action,
				WasActive:      z.WasActive,
			}
			if z.Error != nil {
				item.Error = z.Error.Error()
			}
			zo.Zombies = append(zo.Zombies, item)
		}
		for _, e := range zombieResult.Errors {
			zo.Errors = append(zo.Errors, e.Error())
		}
		output.Zombies = zo
	}

	// Stalls
	if stallResult != nil {
		so := &PatrolScanStallOutput{
			Checked: stallResult.Checked,
			Found:   len(stallResult.Stalled),
		}
		for _, s := range stallResult.Stalled {
			item := PatrolScanStallItem{
				Polecat:   s.PolecatName,
				StallType: s.StallType,
				Action:    s.Action,
			}
			if s.Error != nil {
				item.Error = s.Error.Error()
			}
			so.Stalls = append(so.Stalls, item)
		}
		output.Stalls = so
	}

	// Completions
	if completionResult != nil {
		co := &PatrolScanCompleteOutput{
			Checked: completionResult.Checked,
			Found:   len(completionResult.Discovered),
		}
		for _, d := range completionResult.Discovered {
			item := PatrolScanCompleteItem{
				Polecat:        d.PolecatName,
				ExitType:       d.ExitType,
				IssueID:        d.IssueID,
				MRID:           d.MRID,
				Branch:         d.Branch,
				Action:         d.Action,
				WispCreated:    d.WispCreated,
				CompletionTime: d.CompletionTime,
			}
			co.Completed = append(co.Completed, item)
		}
		output.Completions = co
	}

	// Post-hoc completions (gu-jr8)
	if postHocResult != nil {
		po := &PatrolScanPostHocOutput{
			Checked: postHocResult.Checked,
			Found:   len(postHocResult.Discovered),
		}
		for _, d := range postHocResult.Discovered {
			item := PatrolScanPostHocItem{
				Polecat:   d.PolecatName,
				AgentBead: d.AgentBeadID,
				HookBead:  d.HookBead,
				Action:    d.Action,
			}
			if d.Error != nil {
				item.Error = d.Error.Error()
			}
			po.Items = append(po.Items, item)
		}
		for _, e := range postHocResult.Errors {
			po.Errors = append(po.Errors, e.Error())
		}
		output.PostHoc = po
	}

	// Stranded assignees (gu-wwyq)
	if strandedResult != nil {
		so := &PatrolScanStrandedOutput{
			Checked: strandedResult.Checked,
			Found:   len(strandedResult.Stranded),
		}
		for _, s := range strandedResult.Stranded {
			item := PatrolScanStrandedItem{
				BeadID:   s.BeadID,
				Polecat:  s.PolecatName,
				Assignee: s.Assignee,
				AgeSecs:  int64(s.Age.Seconds()),
				Action:   s.Action,
				MailSent: s.MailSent,
			}
			if s.Error != nil {
				item.Error = s.Error.Error()
			}
			so.Stranded = append(so.Stranded, item)
		}
		for _, e := range strandedResult.Errors {
			so.Errors = append(so.Errors, e.Error())
		}
		output.Stranded = so
	}

	// Stale rig-agent heartbeats (gu-0nmw)
	if staleAgentResult != nil {
		so := &PatrolScanStaleRigAgentOut{
			Checked: staleAgentResult.Checked,
		}
		for _, s := range staleAgentResult.Stale {
			if s.Action == "escalated" {
				so.Found++
			}
			item := PatrolScanStaleRigAgentItem{
				Role:             s.AgentRole,
				Session:          s.SessionName,
				HeartbeatAgeSecs: int64(s.HeartbeatAge.Seconds()),
				HeartbeatMissing: s.HeartbeatMissing,
				SessionAlive:     s.SessionAlive,
				Action:           s.Action,
				CorrelatedInto:   s.CorrelatedInto,
				MailSent:         s.MailSent,
			}
			if s.Error != nil {
				item.Error = s.Error.Error()
			}
			so.Items = append(so.Items, item)
		}
		for _, e := range staleAgentResult.Errors {
			so.Errors = append(so.Errors, e.Error())
		}
		output.StaleRigAgents = so
	}

	// False-deferred bead recovery (gu-wykt)
	if falseDeferredResult != nil {
		fd := &PatrolScanFalseDeferredOut{
			Checked: falseDeferredResult.Checked,
		}
		for _, r := range falseDeferredResult.Recovered {
			if r.Action == "closed" {
				fd.Found++
			}
			item := PatrolScanFalseDeferredItem{
				BeadID:         r.BeadID,
				CitedCommitSHA: r.CitedCommitSHA,
				Action:         r.Action,
			}
			if r.Error != nil {
				item.Error = r.Error.Error()
			}
			fd.Recovered = append(fd.Recovered, item)
		}
		for _, e := range falseDeferredResult.Errors {
			fd.Errors = append(fd.Errors, e.Error())
		}
		output.FalseDeferred = fd
	}

	// Stale-park recovery (gs-du4h)
	if staleParkResult != nil {
		sp := &PatrolScanStaleParkOut{
			Checked: staleParkResult.Checked,
		}
		for _, r := range staleParkResult.Recovered {
			if r.Unblocked {
				sp.Found++
			}
			item := PatrolScanStaleParkItem{
				BeadID:           r.BeadID,
				ResolvedBlockers: r.ResolvedBlockers,
				DetachedMolecule: r.DetachedMolecule,
				Unblocked:        r.Unblocked,
			}
			if r.Error != nil {
				item.Error = r.Error.Error()
			}
			sp.Recovered = append(sp.Recovered, item)
		}
		for _, e := range staleParkResult.Errors {
			sp.Errors = append(sp.Errors, e.Error())
		}
		output.StaleParks = sp
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

func outputPatrolScanHuman(rigName string, zombieResult *witness.DetectZombiePolecatsResult, stallResult *witness.DetectStalledPolecatsResult, completionResult *witness.DiscoverCompletionsResult, postHocResult *witness.DiscoverPostHocCompletionsResult, strandedResult *witness.DetectStaleInProgressBeadsResult, staleAgentResult *witness.DetectStaleRigAgentHeartbeatsResult, falseDeferredResult *witness.DiscoverDeferredButShippedResult, staleParkResult *witness.DetectStaleParkedBeadsResult, _ []witness.PatrolReceipt) error {
	fmt.Printf("%s Patrol scan: %s\n\n", style.Bold.Render("🔍"), rigName)

	// Zombies
	if zombieResult != nil {
		fmt.Printf("%s Zombie Detection: checked %d polecat(s)\n",
			style.Bold.Render("👻"), zombieResult.Checked)

		if len(zombieResult.Zombies) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No zombies detected"))
		} else {
			for _, z := range zombieResult.Zombies {
				icon := "⚠"
				if z.WasActive {
					icon = "🚨"
				}
				fmt.Printf("  %s %s: %s\n", icon, z.PolecatName, z.Classification)
				fmt.Printf("    State: %s", z.AgentState)
				if z.HookBead != "" {
					fmt.Printf("  Hook: %s", z.HookBead)
				}
				if z.CleanupStatus != "" {
					fmt.Printf("  Cleanup: %s", z.CleanupStatus)
				}
				fmt.Println()
				fmt.Printf("    Action: %s\n", z.Action)
				if z.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", z.Error)))
				}
			}
		}

		if len(zombieResult.Errors) > 0 && patrolScanVerbose {
			fmt.Printf("  Errors: %d\n", len(zombieResult.Errors))
			for _, e := range zombieResult.Errors {
				fmt.Printf("    - %v\n", e)
			}
		}

		if len(zombieResult.ConvoyFailures) > 0 {
			fmt.Printf("  Convoy failures: %d\n", len(zombieResult.ConvoyFailures))
		}
		fmt.Println()
	}

	// Stalls
	if stallResult != nil && (len(stallResult.Stalled) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Stall Detection: checked %d polecat(s)\n",
			style.Bold.Render("⏳"), stallResult.Checked)

		if len(stallResult.Stalled) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No stalls detected"))
		} else {
			for _, s := range stallResult.Stalled {
				fmt.Printf("  ⚠ %s: %s → %s\n", s.PolecatName, s.StallType, s.Action)
				if s.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", s.Error)))
				}
			}
		}
		fmt.Println()
	}

	// Completions
	if completionResult != nil && (len(completionResult.Discovered) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Completion Discovery: checked %d polecat(s)\n",
			style.Bold.Render("✅"), completionResult.Checked)

		if len(completionResult.Discovered) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No completions discovered"))
		} else {
			for _, d := range completionResult.Discovered {
				fmt.Printf("  ● %s: exit=%s", d.PolecatName, d.ExitType)
				if d.IssueID != "" {
					fmt.Printf("  issue=%s", d.IssueID)
				}
				if d.MRID != "" {
					fmt.Printf("  mr=%s", d.MRID)
				}
				fmt.Println()
				fmt.Printf("    Action: %s\n", d.Action)
			}
		}
		fmt.Println()
	}

	// Post-hoc completions (gu-jr8): polecats whose branch was merged to mainline
	// but whose hook bead was never closed because gt done didn't run.
	if postHocResult != nil && (len(postHocResult.Discovered) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Post-Hoc Completion Recovery: checked %d polecat(s)\n",
			style.Bold.Render("🪦"), postHocResult.Checked)

		if len(postHocResult.Discovered) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No post-hoc completions found"))
		} else {
			for _, d := range postHocResult.Discovered {
				fmt.Printf("  ● %s: hook=%s → %s\n", d.PolecatName, d.HookBead, d.Action)
				if d.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", d.Error)))
				}
			}
		}
		if len(postHocResult.Errors) > 0 && patrolScanVerbose {
			for _, e := range postHocResult.Errors {
				fmt.Printf("    - %v\n", e)
			}
		}
		fmt.Println()
	}

	// Stranded assignees (gu-wwyq)
	if strandedResult != nil && (len(strandedResult.Stranded) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Stranded-Assignee Detection: checked %d in_progress bead(s)\n",
			style.Bold.Render("🪨"), strandedResult.Checked)

		escalated := 0
		for _, s := range strandedResult.Stranded {
			if s.Action == "escalated" {
				escalated++
			}
		}
		if escalated == 0 && len(strandedResult.Stranded) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No stranded in_progress beads"))
		} else {
			for _, s := range strandedResult.Stranded {
				if s.Action != "escalated" && !patrolScanVerbose {
					continue
				}
				fmt.Printf("  ● %s: polecat=%s age=%s action=%s\n",
					s.BeadID, s.PolecatName, s.Age.Round(time.Second), s.Action)
				if s.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", s.Error)))
				}
			}
		}
		if len(strandedResult.Errors) > 0 && patrolScanVerbose {
			for _, e := range strandedResult.Errors {
				fmt.Printf("    - %v\n", e)
			}
		}
		fmt.Println()
	}

	// Stale rig-agent heartbeats (gu-0nmw)
	if staleAgentResult != nil && (len(staleAgentResult.Stale) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Stale Rig-Agent Heartbeats: checked %d agent(s)\n",
			style.Bold.Render("💔"), staleAgentResult.Checked)

		escalated := 0
		for _, s := range staleAgentResult.Stale {
			if s.Action == "escalated" {
				escalated++
			}
		}
		if escalated == 0 && !patrolScanVerbose {
			fmt.Printf("  %s\n", style.Dim.Render("No stale rig-agent heartbeats"))
		} else {
			for _, s := range staleAgentResult.Stale {
				if s.Action != "escalated" && !patrolScanVerbose {
					continue
				}
				ageStr := s.HeartbeatAge.Round(time.Second).String()
				if s.HeartbeatMissing {
					ageStr = "missing"
				}
				fmt.Printf("  ● %s/%s: age=%s session_alive=%v action=%s\n",
					rigName, s.AgentRole, ageStr, s.SessionAlive, s.Action)
				if s.CorrelatedInto != "" {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("correlated into %s (gu-nejgh)", s.CorrelatedInto)))
				}
				if s.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", s.Error)))
				}
			}
		}
		if len(staleAgentResult.Errors) > 0 && patrolScanVerbose {
			for _, e := range staleAgentResult.Errors {
				fmt.Printf("    - %v\n", e)
			}
		}
		fmt.Println()
	}

	// False-deferred bead recovery (gu-wykt)
	if falseDeferredResult != nil {
		closed := 0
		for _, r := range falseDeferredResult.Recovered {
			if r.Action == "closed" {
				closed++
			}
		}
		if closed > 0 || patrolScanVerbose {
			fmt.Printf("%s False-Deferred Recovery: checked %d deferred bead(s)\n",
				style.Bold.Render("⏳"), falseDeferredResult.Checked)

			if closed == 0 && !patrolScanVerbose {
				fmt.Printf("  %s\n", style.Dim.Render("No false-deferred beads recovered"))
			} else {
				for _, r := range falseDeferredResult.Recovered {
					if r.Action != "closed" && !patrolScanVerbose {
						continue
					}
					fmt.Printf("  ● %s: action=%s", r.BeadID, r.Action)
					if r.CitedCommitSHA != "" {
						fmt.Printf("  cited=%s", r.CitedCommitSHA)
					}
					fmt.Println()
					if r.Error != nil {
						fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", r.Error)))
					}
				}
			}
			if len(falseDeferredResult.Errors) > 0 && patrolScanVerbose {
				for _, e := range falseDeferredResult.Errors {
					fmt.Printf("    - %v\n", e)
				}
			}
			fmt.Println()
		}
	}

	// Stale-park recovery (gs-du4h)
	if staleParkResult != nil {
		unblocked := 0
		for _, r := range staleParkResult.Recovered {
			if r.Unblocked {
				unblocked++
			}
		}
		if unblocked > 0 || patrolScanVerbose {
			fmt.Printf("%s Stale-Park Recovery: checked %d blocked bead(s)\n",
				style.Bold.Render("🅿"), staleParkResult.Checked)

			if unblocked == 0 && !patrolScanVerbose {
				fmt.Printf("  %s\n", style.Dim.Render("No stale parks recovered"))
			} else {
				for _, r := range staleParkResult.Recovered {
					if !r.Unblocked && !patrolScanVerbose {
						continue
					}
					fmt.Printf("  ● %s: unblocked=%t", r.BeadID, r.Unblocked)
					if len(r.ResolvedBlockers) > 0 {
						fmt.Printf("  blockers=%s", strings.Join(r.ResolvedBlockers, ","))
					}
					if r.DetachedMolecule != "" {
						fmt.Printf("  molecule=%s", r.DetachedMolecule)
					}
					fmt.Println()
					if r.Error != nil {
						fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", r.Error)))
					}
				}
			}
			if len(staleParkResult.Errors) > 0 && patrolScanVerbose {
				for _, e := range staleParkResult.Errors {
					fmt.Printf("    - %v\n", e)
				}
			}
			fmt.Println()
		}
	}

	// Summary
	zombieCount := 0
	activeCount := 0
	if zombieResult != nil {
		zombieCount = len(zombieResult.Zombies)
		activeCount = countActiveWorkZombies(zombieResult)
	}
	stallCount := 0
	if stallResult != nil {
		stallCount = len(stallResult.Stalled)
	}
	completionCount := 0
	if completionResult != nil {
		completionCount = len(completionResult.Discovered)
	}
	postHocCount := 0
	if postHocResult != nil {
		postHocCount = len(postHocResult.Discovered)
	}
	strandedCount := 0
	if strandedResult != nil {
		for _, s := range strandedResult.Stranded {
			if s.Action == "escalated" {
				strandedCount++
			}
		}
	}
	staleAgentCount := 0
	if staleAgentResult != nil {
		for _, s := range staleAgentResult.Stale {
			if s.Action == "escalated" {
				staleAgentCount++
			}
		}
	}
	falseDeferredCount := 0
	if falseDeferredResult != nil {
		for _, r := range falseDeferredResult.Recovered {
			if r.Action == "closed" {
				falseDeferredCount++
			}
		}
	}

	if zombieCount == 0 && stallCount == 0 && completionCount == 0 && postHocCount == 0 && strandedCount == 0 && staleAgentCount == 0 && falseDeferredCount == 0 {
		fmt.Printf("%s All clear — no issues detected\n", style.Success.Render("✓"))
	} else {
		fmt.Printf("Summary: %d zombie(s) (%d active-work), %d stall(s), %d completion(s), %d post-hoc, %d stranded, %d stale-agent(s), %d false-deferred\n",
			zombieCount, activeCount, stallCount, completionCount, postHocCount, strandedCount, staleAgentCount, falseDeferredCount)
	}

	return nil
}

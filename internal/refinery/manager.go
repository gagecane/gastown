package refinery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

// Common errors
var (
	ErrNotRunning     = errors.New("refinery not running")
	ErrAlreadyRunning = errors.New("refinery already running")
	ErrNoQueue        = errors.New("no items in queue")
	ErrDisabled       = errors.New("refinery disabled for this rig")
)

// Manager handles refinery lifecycle and queue operations.
type Manager struct {
	rig     *rig.Rig
	workDir string
	output  io.Writer // Output destination for user-facing messages

	// verifyMergeLanded confirms an MR's merge commit is actually present on
	// origin/<target> before PostMerge closes the MR + source beads. It guards
	// against silent merge-loss (gu-ilf86): if the merge step no-ops or errors
	// but the polecat branch push already succeeded, the commit lives only on
	// the polecat branch — closing the beads anyway strands it off mainline.
	// Injectable for testing; defaults to defaultVerifyMergeLanded.
	verifyMergeLanded func(mr *MergeRequest) error
}

type scoredIssue struct {
	issue *beads.Issue
	score float64
}

// NewManager creates a new refinery manager for a rig.
func NewManager(r *rig.Rig) *Manager {
	m := &Manager{
		rig:     r,
		workDir: r.Path,
		output:  os.Stdout,
	}
	m.verifyMergeLanded = m.defaultVerifyMergeLanded
	return m
}

// SetOutput sets the output writer for user-facing messages.
// This is useful for testing or redirecting output.
func (m *Manager) SetOutput(w io.Writer) {
	m.output = w
}

// SessionName returns the tmux session name for this refinery.
func (m *Manager) SessionName() string {
	return session.RefinerySessionName(session.PrefixFor(m.rig.Name))
}

// IsRunning checks if the refinery session is active and healthy.
// Checks both tmux session existence AND agent process liveness to avoid
// reporting zombie sessions (tmux alive but Claude dead) as "running".
// ZFC: tmux session existence is the source of truth for session state,
// but agent liveness determines if the session is actually functional.
func (m *Manager) IsRunning() (bool, error) {
	t := tmux.NewTmux()
	sessionName := m.SessionName()
	status := t.CheckSessionHealth(sessionName, 0)
	return status == tmux.SessionHealthy, nil
}

// IsHealthy checks if the refinery is running and has been active recently.
// Unlike IsRunning which only checks process liveness, this also detects hung
// sessions where Claude is alive but hasn't produced output in maxInactivity.
// Returns the detailed ZombieStatus for callers that need to distinguish
// between different failure modes.
func (m *Manager) IsHealthy(maxInactivity time.Duration) tmux.ZombieStatus {
	t := tmux.NewTmux()
	return t.CheckSessionHealth(m.SessionName(), maxInactivity)
}

// Status returns information about the refinery session.
// ZFC-compliant: tmux session is the source of truth.
func (m *Manager) Status() (*tmux.SessionInfo, error) {
	t := tmux.NewTmux()
	sessionID := m.SessionName()

	running, err := t.HasSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return nil, ErrNotRunning
	}

	return t.GetSessionInfo(sessionID)
}

// Start starts the refinery.
// If foreground is true, returns an error (foreground mode deprecated).
// Otherwise, spawns a Claude agent in a tmux session to process the merge queue.
// The agentOverride parameter allows specifying an agent alias to use instead of the town default.
// ZFC-compliant: no state file, tmux session is source of truth.
func (m *Manager) Start(foreground bool, agentOverride string) error {
	t := tmux.NewTmux()
	sessionID := m.SessionName()

	if foreground {
		// Foreground mode is deprecated - the Refinery agent handles merge processing
		return fmt.Errorf("foreground mode is deprecated; use background mode (remove --foreground flag)")
	}

	// Check persistent refinery-disabled flag before doing anything.
	if rigCfg, err := rig.LoadRigConfig(m.rig.Path); err == nil && rigCfg.RefineryDisabled {
		return ErrDisabled
	}

	// Check if session already exists
	running, _ := t.HasSession(sessionID)
	if running {
		// Session exists - check if agent is actually running (healthy vs zombie)
		if t.IsAgentAlive(sessionID) {
			return ErrAlreadyRunning
		}
		// Zombie - tmux alive but agent dead. Kill and recreate.
		_, _ = fmt.Fprintln(m.output, "⚠ Detected zombie session (tmux alive, agent dead). Recreating...")
		if err := t.KillSession(sessionID); err != nil {
			return fmt.Errorf("killing zombie session: %w", err)
		}
	}

	// Note: No PID check per ZFC - tmux session is the source of truth

	// Background mode: spawn a Claude agent in a tmux session
	// The Claude agent handles MR processing using git commands and beads

	// Working directory is the refinery worktree (shares .git with mayor/polecats).
	// If the worktree is missing (pruned, deleted, or corrupted), auto-repair it
	// from the shared bare repo (.repo.git) instead of falling back to mayor/rig.
	// Falling back to mayor/rig causes the refinery to operate in the mayor's
	// clone, which can interfere with mayor operations and confuse agents.
	//
	// Rigs using a standard .git clone (e.g. beads) never have a .repo.git bare
	// repo, so the repair path is not applicable for them. Fall back to mayor/rig
	// silently in that case — the fallback is correct and the warning would be noise.
	refineryRigDir := filepath.Join(m.rig.Path, "refinery", "rig")
	if _, err := os.Stat(refineryRigDir); os.IsNotExist(err) {
		bareRepoPath := filepath.Join(m.rig.Path, ".repo.git")
		_, bareErr := os.Stat(bareRepoPath)
		standardGitPath := filepath.Join(m.rig.Path, ".git")
		_, standardGitErr := os.Stat(standardGitPath)
		if os.IsNotExist(bareErr) && standardGitErr == nil {
			// Rig uses standard .git layout — worktree repair is not applicable.
			// Fall back to mayor/rig silently; the fallback works correctly here.
			refineryRigDir = filepath.Join(m.rig.Path, "mayor", "rig")
		} else if repairErr := m.repairRefineryWorktree(refineryRigDir); repairErr != nil {
			// Repair failed — fall back to mayor/rig as last resort.
			_, _ = fmt.Fprintf(m.output, "⚠ Could not repair refinery worktree: %v (falling back to mayor/rig)\n", repairErr)
			refineryRigDir = filepath.Join(m.rig.Path, "mayor", "rig")
		}
	}

	// Clear any "reaped temp upstream" wedge before the agent starts.
	// After a successful merge, refinery's post-merge cleanup deletes the
	// polecat branch from origin — but the worktree's local `temp` branch
	// (created during the rebase step of the patrol molecule) still has
	// branch.temp.merge pointing at the now-reaped ref. The next cycle's
	// rebase/pull/push fails or behaves unexpectedly, and the wedge survives
	// session restart because it's persisted in .git/config. (gu-hlie /
	// parent gu-xn2z, 2026-05-29)
	if unwedgeErr := UnwedgeWorktree(refineryRigDir, m.rig.DefaultBranch(), m.output); unwedgeErr != nil {
		_, _ = fmt.Fprintf(m.output, "⚠ Could not clear refinery worktree wedge: %v (continuing)\n", unwedgeErr)
	}

	// Ensure runtime settings exist in the shared refinery parent directory.
	// Settings are passed to Claude Code via --settings flag.
	townRoot := filepath.Dir(m.rig.Path)

	// Resolve CLAUDE_CONFIG_DIR from accounts.json so refinery sessions
	// use the correct account. Mirrors the daemon restart path (lifecycle.go).
	accountsPath := constants.MayorAccountsPath(townRoot)
	runtimeConfigDir, _, _ := config.ResolveAccountConfigDir(accountsPath, "")
	if runtimeConfigDir == "" {
		runtimeConfigDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}

	runtimeConfig := config.ResolveRoleAgentConfig("refinery", townRoot, m.rig.Path)
	refinerySettingsDir := config.RoleSettingsDir("refinery", m.rig.Path)
	if err := runtime.EnsureSettingsForRole(refinerySettingsDir, refineryRigDir, "refinery", runtimeConfig); err != nil {
		return fmt.Errorf("ensuring runtime settings: %w", err)
	}

	// Ensure worktree-local git exclude has required Gas Town patterns.
	// Writing to .git/info/exclude keeps Gas Town ignore rules out of the
	// tracked .gitignore (which belongs to the user's repo), so the refinery
	// worktree doesn't accidentally commit Gas Town infrastructure patterns
	// upstream. (gu-o406)
	if err := rig.EnsureLocalExcludePatterns(refineryRigDir); err != nil {
		style.PrintWarning("could not update refinery git exclude: %v", err)
	}

	initialPrompt := session.BuildStartupPrompt(session.BeaconConfig{
		Recipient: session.BeaconRecipient("refinery", "", m.rig.Name),
		Sender:    "deacon",
		Topic:     "patrol",
	}, "SessionStart already injected `gt prime --hook`. Continue from the hooked patrol context and begin patrol.")

	command, err := config.BuildStartupCommandFromConfig(config.AgentEnvConfig{
		Role:             "refinery",
		Rig:              m.rig.Name,
		TownRoot:         townRoot,
		RuntimeConfigDir: runtimeConfigDir,
		Prompt:           initialPrompt,
		Topic:            "patrol",
		SessionName:      sessionID,
	}, m.rig.Path, initialPrompt, agentOverride)
	if err != nil {
		return fmt.Errorf("building startup command: %w", err)
	}

	// Compute environment BEFORE creating the session so it can be passed to
	// tmux via -e flags. Setting env via SetEnvironment after session creation
	// only affects newly spawned panes — the running pane (and Claude's
	// subprocesses like bd) keeps its original environment (gt-neycp).
	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:             "refinery",
		Rig:              m.rig.Name,
		TownRoot:         townRoot,
		RuntimeConfigDir: runtimeConfigDir,
		Agent:            agentOverride,
		SessionName:      sessionID,
	})
	envVars = session.MergeRuntimeLivenessEnv(envVars, runtimeConfig)
	envVars["GT_REFINERY"] = "1"

	// Generate the GASTA run ID for this refinery session.
	runID := uuid.New().String()
	envVars["GT_RUN"] = runID

	// Create session with command and env vars via -e flags so the initial
	// shell — and Claude's subprocesses — inherit them from the start.
	// See: https://github.com/anthropics/gastown/issues/280 (race condition fix)
	if err := t.NewSessionWithCommandAndEnv(sessionID, refineryRigDir, command, envVars); err != nil {
		return fmt.Errorf("creating tmux session: %w", err)
	}

	// Set remain-on-exit IMMEDIATELY after session creation so the pane
	// survives even if the agent exits before the auto-respawn hook is
	// installed below. Without this, a fast agent crash would destroy the
	// pane and leave the daemon to spawn a fresh session 3-5 minutes later.
	// Mirrors the deacon/manager.go pattern (PATCH-010).
	_ = t.SetRemainOnExit(sessionID, true)

	// Apply theme (non-fatal: theming failure doesn't affect operation)
	theme := tmux.ResolveSessionTheme(townRoot, m.rig.Name, "refinery", "")
	_ = t.ConfigureGasTownSession(sessionID, theme, m.rig.Name, "refinery", "refinery")

	// Accept startup dialogs (workspace trust + bypass permissions) if they appear.
	// Must be before WaitForRuntimeReady to avoid race where dialog blocks prompt detection.
	_ = t.AcceptStartupDialogs(sessionID)

	// Wait for Claude to start and show its prompt - fatal if Claude fails to launch
	// WaitForRuntimeReady waits for the runtime to be ready
	if err := t.WaitForRuntimeReady(sessionID, runtimeConfig, constants.ClaudeStartTimeout); err != nil {
		// Kill the zombie session before returning error
		_ = t.KillSessionWithProcesses(sessionID)
		return fmt.Errorf("waiting for refinery to start: %w", err)
	}

	// Install the auto-respawn hook so the agent (kiro-cli / claude / etc.)
	// is restarted immediately at the tmux layer when it exits — no need to
	// wait for the daemon's 5-minute refinery patrol tick to notice.
	// Mirrors the deacon/manager.go PATCH-010 pattern. Non-fatal: if the
	// hook fails to install, the daemon heartbeat still restarts the
	// session, just with a delay.
	// (gu-6az)
	if err := t.SetAutoRespawnHook(sessionID); err != nil {
		log.Printf("warning: failed to set auto-respawn hook for refinery %s: %v", sessionID, err)
	}

	// Start nudge-queue poller (gt-dgf). Claude's UserPromptSubmit hook only
	// drains when the agent submits a prompt. Idle agents never submit, so
	// queued nudges deadlock. The poller breaks the cycle by polling every 10s.
	if _, pollerErr := nudge.StartPoller(townRoot, sessionID); pollerErr != nil {
		log.Printf("warning: could not start nudge poller for %s: %v", sessionID, pollerErr)
	}

	_ = runtime.RunStartupFallback(t, sessionID, "refinery", runtimeConfig)
	_ = runtime.DeliverStartupPromptFallback(t, sessionID, initialPrompt, runtimeConfig, constants.ClaudeStartTimeout)

	// Track PID for defense-in-depth orphan cleanup (non-fatal)
	if err := session.TrackSessionPID(townRoot, sessionID, t); err != nil {
		log.Printf("warning: tracking session PID for %s: %v", sessionID, err)
	}

	// Touch initial heartbeat so liveness detection works from the start.
	// Subsequent touches happen on every gt command via persistentPreRun
	// in cmd/root.go (gu-0nmw). Without this, the witness's stale-refinery
	// scan can't tell a freshly-spawned refinery from a wedged one.
	polecat.TouchSessionHeartbeat(townRoot, sessionID)

	// Stream refinery's Claude Code JSONL conversation log to VictoriaLogs (opt-in).
	if os.Getenv("GT_LOG_AGENT_OUTPUT") == "true" && os.Getenv("GT_OTEL_LOGS_URL") != "" {
		if err := session.ActivateAgentLogging(sessionID, refineryRigDir, runID); err != nil {
			log.Printf("warning: agent log watcher setup failed for %s: %v", sessionID, err)
		}
	}

	// Record the agent instantiation event (GASTA root span).
	session.RecordAgentInstantiateFromDir(context.Background(), runID, runtimeConfig.ResolvedAgent,
		"refinery", "refinery", sessionID, m.rig.Name, townRoot, "", refineryRigDir)

	return nil
}

// repairRefineryWorktree recreates a missing refinery/rig worktree from the
// shared bare repo (.repo.git). The refinery worktree is created during
// `gt rig add` but can be lost if `git worktree prune` runs, the directory
// is deleted, or the .git file becomes corrupted. This self-heals on startup
// instead of requiring manual intervention.
func (m *Manager) repairRefineryWorktree(refineryRigDir string) error {
	bareRepoPath := filepath.Join(m.rig.Path, ".repo.git")
	if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
		return fmt.Errorf("bare repo not found at %s", bareRepoPath)
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(refineryRigDir), 0755); err != nil {
		return fmt.Errorf("creating refinery dir: %w", err)
	}

	// Prune stale worktree entries so git doesn't reject the add
	bareGit := git.NewGitWithDir(bareRepoPath, "")
	_ = bareGit.WorktreePrune()

	// Create worktree on the rig's default branch
	defaultBranch := m.rig.DefaultBranch()
	if err := bareGit.WorktreeAddExisting(refineryRigDir, defaultBranch); err != nil {
		return fmt.Errorf("git worktree add: %w", err)
	}

	// Configure hooks path (matches rig add behavior)
	refineryGit := git.NewGit(refineryRigDir)
	if err := refineryGit.ConfigureHooksPath(); err != nil {
		// Non-fatal: worktree is usable without hooks
		_, _ = fmt.Fprintf(m.output, "⚠ Could not configure hooks for repaired worktree: %v\n", err)
	}

	_, _ = fmt.Fprintf(m.output, "✓ Auto-repaired missing refinery worktree at %s\n", refineryRigDir)
	return nil
}

// Stop stops the refinery.
// ZFC-compliant: tmux session is the source of truth.
func (m *Manager) Stop() error {
	t := tmux.NewTmux()
	sessionID := m.SessionName()

	// Check if tmux session exists
	running, _ := t.HasSession(sessionID)
	if !running {
		return ErrNotRunning
	}

	// Clear the tracked PID file BEFORE killing the tmux session (gu-ytwg).
	// Without this, <townRoot>/.runtime/pids/<session>.pid outlives the
	// process and any consumer that trusts the value (doctor, heartbeat,
	// KillTrackedPIDs) may signal the wrong PID on respawn.
	townRoot := filepath.Dir(m.rig.Path)
	session.UntrackPID(townRoot, sessionID)

	// Kill the tmux session
	return t.KillSession(sessionID)
}

// Queue returns the current merge queue.
// Uses beads merge-request issues as the source of truth (not git branches).
// ZFC-compliant: beads is the source of truth, no state file.
func (m *Manager) Queue() ([]QueueItem, error) {
	// Query beads for open merge-request issues
	// BeadsPath() returns the git-synced beads location
	b := beads.New(m.rig.BeadsPath())
	issues, err := b.ListMergeRequests(beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "open",
		Priority: -1, // No priority filter
	})
	if err != nil {
		return nil, fmt.Errorf("querying merge queue from beads: %w", err)
	}

	// Score and sort issues by priority score (highest first)
	now := time.Now()
	scored := make([]scoredIssue, 0, len(issues))
	for _, issue := range issues {
		// Defensive filter: bd status filters can drift; queue must only include open MRs.
		if issue == nil || issue.Status != "open" {
			continue
		}

		// Filter by rig — wisps are shared across all rigs (GH#2718).
		fields := beads.ParseMRFields(issue)
		if fields != nil && fields.Rig != "" && !strings.EqualFold(fields.Rig, m.rig.Name) {
			continue
		}

		score := m.calculateIssueScore(issue, now)
		scored = append(scored, scoredIssue{issue: issue, score: score})
	}

	sort.Slice(scored, func(i, j int) bool {
		return compareScoredIssues(scored[i], scored[j])
	})

	// Convert scored issues to queue items
	var items []QueueItem
	pos := 1
	for _, s := range scored {
		mr := m.issueToMR(s.issue)
		if mr != nil {
			items = append(items, QueueItem{
				Position: pos,
				MR:       mr,
				Age:      formatAge(mr.CreatedAt),
			})
			pos++
		}
	}

	return items, nil
}

func compareScoredIssues(a, b scoredIssue) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.issue == nil || b.issue == nil {
		return a.issue != nil
	}
	return a.issue.ID < b.issue.ID
}

// calculateIssueScore computes the priority score for an MR issue.
// Higher scores mean higher priority (process first).
func (m *Manager) calculateIssueScore(issue *beads.Issue, now time.Time) float64 {
	fields := beads.ParseMRFields(issue)

	// Parse MR creation time
	mrCreatedAt := parseTime(issue.CreatedAt)
	if mrCreatedAt.IsZero() {
		mrCreatedAt = now // Fallback
	}

	// Build score input
	input := ScoreInput{
		Priority:    issue.Priority,
		MRCreatedAt: mrCreatedAt,
		Now:         now,
	}

	// Add fields from MR metadata if available
	if fields != nil {
		input.RetryCount = fields.RetryCount

		// Parse convoy created at if available
		if fields.ConvoyCreatedAt != "" {
			if convoyTime := parseTime(fields.ConvoyCreatedAt); !convoyTime.IsZero() {
				input.ConvoyCreatedAt = &convoyTime
			}
		}
	}

	return ScoreMRWithDefaults(input)
}

// issueToMR converts a beads issue to a MergeRequest.
func (m *Manager) issueToMR(issue *beads.Issue) *MergeRequest {
	if issue == nil {
		return nil
	}

	// Get configured default branch for this rig
	defaultBranch := m.rig.DefaultBranch()

	fields := beads.ParseMRFields(issue)
	if fields == nil {
		// No MR fields in description, construct from title/ID
		return &MergeRequest{
			ID:           issue.ID,
			IssueID:      issue.ID,
			Status:       MROpen,
			CreatedAt:    parseTime(issue.CreatedAt),
			TargetBranch: defaultBranch,
		}
	}

	// Default target to rig's default branch if not specified
	target := fields.Target
	if target == "" {
		target = defaultBranch
	}

	return &MergeRequest{
		ID:           issue.ID,
		Branch:       fields.Branch,
		Worker:       fields.Worker,
		IssueID:      fields.SourceIssue,
		TargetBranch: target,
		MergeCommit:  fields.MergeCommit,
		Status:       MROpen,
		CreatedAt:    parseTime(issue.CreatedAt),
	}
}

// parseTime parses a time string, returning zero time on error.
func parseTime(s string) time.Time {
	// Try RFC3339 first (most common)
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Try date-only format as fallback
		t, _ = time.Parse("2006-01-02", s)
	}
	return t
}

// formatAge formats a duration since the given time.
func formatAge(t time.Time) string {
	d := time.Since(t)

	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// Common errors for MR operations
var (
	ErrMRNotFound  = errors.New("merge request not found")
	ErrMRNotFailed = errors.New("merge request has not failed")
)

// GetMR returns a merge request by ID.
// ZFC-compliant: delegates to FindMR which uses beads as source of truth.
// Deprecated: Use FindMR directly for more flexible matching.
func (m *Manager) GetMR(id string) (*MergeRequest, error) {
	return m.FindMR(id)
}

// FindMR finds a merge request by ID or branch name in the queue.
func (m *Manager) FindMR(idOrBranch string) (*MergeRequest, error) {
	queue, err := m.Queue()
	if err != nil {
		return nil, err
	}

	for _, item := range queue {
		// Match by ID
		if item.MR.ID == idOrBranch {
			return item.MR, nil
		}
		// Match by branch name (with or without polecat/ prefix)
		if item.MR.Branch == idOrBranch {
			return item.MR, nil
		}
		if constants.BranchPolecatPrefix+idOrBranch == item.MR.Branch {
			return item.MR, nil
		}
		// Match by ID prefix (partial match for convenience)
		if strings.HasPrefix(item.MR.ID, idOrBranch) {
			return item.MR, nil
		}
	}

	return nil, ErrMRNotFound
}

// findClosedMRByID loads a merge-request bead directly by its exact ID,
// bypassing the open-only Queue() filter. It is used by PostMerge to recover
// the MR metadata (branch, source_issue) when a prior, interrupted post-merge
// run already closed the MR bead but left branch/issue cleanup incomplete
// (gu-3f02d). The bead must carry the gt:merge-request label so unrelated
// IDs cannot be mistaken for an MR.
func (m *Manager) findClosedMRByID(id string) (*MergeRequest, error) {
	b := beads.New(m.rig.BeadsPath())
	issue, err := b.Show(id)
	if err != nil {
		return nil, err
	}
	if issue == nil || !beads.HasLabel(issue, "gt:merge-request") {
		return nil, ErrMRNotFound
	}
	mr := m.issueToMR(issue)
	if mr == nil {
		return nil, ErrMRNotFound
	}
	if beads.IssueStatus(issue.Status).IsTerminal() {
		mr.Status = MRClosed
	}
	return mr, nil
}

// Retry is deprecated - the Refinery agent handles retry logic autonomously.
// ZFC-compliant: no state file, agent uses beads issue status.
// The agent will automatically retry failed MRs in its patrol cycle.
func (m *Manager) Retry(_ string, _ bool) error {
	_, _ = fmt.Fprintln(m.output, "Note: Retry is deprecated. The Refinery agent handles retries autonomously via beads.")
	return nil
}

// RegisterMR is deprecated - MRs are registered via beads merge-request issues.
// ZFC-compliant: beads is the source of truth, not state file.
// Use 'gt mr create' or create a merge-request type bead directly.
func (m *Manager) RegisterMR(_ *MergeRequest) error {
	return fmt.Errorf("RegisterMR is deprecated: use beads to create merge-request issues")
}

// RejectMR manually rejects a merge request.
// It closes the MR with rejected status and optionally notifies the worker.
// Returns the rejected MR for display purposes.
func (m *Manager) RejectMR(idOrBranch string, reason string, notify bool) (*MergeRequest, error) {
	mr, err := m.FindMR(idOrBranch)
	if err != nil {
		return nil, err
	}

	// Verify MR is open or in_progress (can't reject already closed)
	if mr.IsClosed() {
		return nil, fmt.Errorf("%w: MR is already closed with reason: %s", ErrClosedImmutable, mr.CloseReason)
	}

	// Close the bead in storage with the rejection reason
	b := beads.New(m.rig.BeadsPath())
	if err := b.CloseWithReason("rejected: "+reason, mr.ID); err != nil {
		return nil, fmt.Errorf("failed to close MR bead: %w", err)
	}

	// Update in-memory state for return value
	if err := mr.Close(CloseReasonRejected); err != nil {
		// Non-fatal: bead is already closed, just log
		_, _ = fmt.Fprintf(m.output, "Warning: failed to update MR state: %v\n", err)
	}
	mr.Error = reason

	// Optionally notify worker
	if notify {
		m.notifyWorkerRejected(mr, reason)
	}

	return mr, nil
}

// PostMergeResult holds the result of a post-merge cleanup operation.
type PostMergeResult struct {
	MR                  *MergeRequest
	MRClosed            bool
	SourceIssueClosed   bool
	SourceIssueID       string
	SourceIssueNotFound bool // true if source issue doesn't exist (already closed or invalid)
}

// PostMerge performs post-merge cleanup for a successfully merged MR.
// It closes the MR bead and its source issue. Branch deletion is handled
// by the caller since the Manager doesn't have git access.
func (m *Manager) PostMerge(idOrBranch string) (*PostMergeResult, error) {
	mr, err := m.FindMR(idOrBranch)
	if err != nil {
		// FindMR only searches the OPEN queue. A previous post-merge run may
		// have closed the MR bead but been interrupted before completing the
		// branch-delete and source-issue-close steps (gu-3f02d). Retrying must
		// not error with "merge request not found" and abandon the leftover
		// cleanup — fall back to loading the closed MR bead directly by ID so
		// the (idempotent) remaining steps can finish.
		if errors.Is(err, ErrMRNotFound) {
			if closedMR, findErr := m.findClosedMRByID(idOrBranch); findErr == nil {
				mr = closedMR
			} else {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	result := &PostMergeResult{
		MR:            mr,
		SourceIssueID: stripMRIssueTimestampSuffix(mr.IssueID),
	}

	b := beads.New(m.rig.BeadsPath())

	// Close the MR bead
	if mr.IsClosed() {
		_, _ = fmt.Fprintf(m.output, "  %s MR already closed\n", style.Dim.Render("—"))
		result.MRClosed = true
	} else {
		// Guard against silent merge-loss (gu-ilf86): refuse to close the MR
		// and source beads unless mr.MergeCommit is actually an ancestor of
		// origin/<target>. The merge step can no-op or error while the polecat
		// branch push already succeeded, leaving the commit only on the polecat
		// branch — closing the beads then strands it off mainline forever.
		// Fail-closed: an MR left open is recoverable by a retry; a lost merge
		// is not. Only verify on the active-close path; an already-closed MR
		// (idempotent gu-3f02d recovery) is past this point.
		if m.verifyMergeLanded != nil {
			if err := m.verifyMergeLanded(mr); err != nil {
				return result, fmt.Errorf("refusing to close MR %s: %w", mr.ID, err)
			}
		}
		if err := b.CloseWithReason("merged", mr.ID); err != nil {
			return result, fmt.Errorf("closing MR bead: %w", err)
		}
		if closeErr := mr.Close(CloseReasonMerged); closeErr != nil {
			_, _ = fmt.Fprintf(m.output, "Warning: failed to update MR state: %v\n", closeErr)
		}
		result.MRClosed = true
	}

	// Close the source issue with reason and --force to bypass dependency checks.
	// The source issue may have an attached molecule (wisp) whose open steps
	// would block a normal bd close. ForceCloseWithReason bypasses this,
	// matching how gt done handles closures for the no-MR path.
	//
	// Defense-in-depth: strip any timestamp suffix from the MR's IssueID
	// before closing. Historically (pre-gu-y2w fix) the submit flow wrote
	// convoy-suffixed IDs like "gu-aei--moiitf15" into source_issue, which
	// caused this close to fail with "not found". result.SourceIssueID was
	// already stripped above — reuse it so both the close and the UI agree.
	if result.SourceIssueID != "" {
		sourceID := result.SourceIssueID
		closeReason := fmt.Sprintf("Merged in %s", mr.ID)
		if mr.MergeCommit != "" {
			closeReason = fmt.Sprintf("%s\ntarget_branch: %s\ncommit_sha: %s", closeReason, mr.TargetBranch, mr.MergeCommit)
		}
		if err := b.ForceCloseWithReason(closeReason, sourceID); err != nil {
			// Check if already closed (by polecat's gt done) — that's fine
			if issue, showErr := b.Show(sourceID); showErr == nil && beads.IssueStatus(issue.Status).IsTerminal() {
				_, _ = fmt.Fprintf(m.output, "  %s source issue already closed: %s\n", style.Dim.Render("○"), sourceID)
				result.SourceIssueClosed = true
			} else {
				_, _ = fmt.Fprintf(m.output, "  %s source issue close: %v\n", style.Dim.Render("○"), err)
				result.SourceIssueNotFound = true
			}
		} else {
			result.SourceIssueClosed = true
		}

		// Clear the awaiting_refinery_merge label as part of the force-close
		// transaction (gu-mhwn). The polecat's gt done adds this label when
		// it submits an MR; without explicit removal here, the label leaks
		// onto a closed-and-cited bead, confusing downstream consumers
		// (convoy ship-verify, etc.). Best-effort: failures are logged but
		// non-fatal because the merge has already succeeded.
		if result.SourceIssueClosed {
			if err := b.Update(sourceID, beads.UpdateOptions{
				RemoveLabels: []string{"awaiting_refinery_merge"},
			}); err != nil {
				_, _ = fmt.Fprintf(m.output, "  %s clear awaiting_refinery_merge label on %s: %v\n", style.Dim.Render("○"), sourceID, err)
			}
		}
	}

	return result, nil
}

// gitAncestryOps is the subset of *git.Git used to verify a merge commit
// actually landed on the target branch. Kept narrow so tests can inject a
// fake without a real repository (mirrors gitForkSyncOps in fork_sync.go).
type gitAncestryOps interface {
	FetchBranch(remote, branch string) error
	IsAncestor(ancestor, descendant string) (bool, error)
}

// defaultVerifyMergeLanded is the production implementation of
// Manager.verifyMergeLanded. It opens the refinery's git worktree and delegates
// to verifyMergeCommitLanded.
func (m *Manager) defaultVerifyMergeLanded(mr *MergeRequest) error {
	gitDir := filepath.Join(m.rig.Path, "refinery", "rig")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		gitDir = filepath.Join(m.rig.Path, "mayor", "rig")
	}
	g := git.NewGit(gitDir)

	// Target Resolution Rule (gu-eakas): resolve the MR's target branch using
	// the same rule the merge step uses. Polecat MRs submitted before gu-wcb37
	// carry target="main" even when the rig's actual default branch is
	// "mainline" (no origin/main exists). Without resolution the fetch fails
	// and verification rejects every target=main MR as unlanded. Resolve:
	// if target is a common default-branch alias ("main", "master") and the
	// rig's configured default branch differs, use the rig's default instead.
	target := resolveVerifyTarget(mr.TargetBranch, m.rig.DefaultBranch())

	return verifyMergeCommitLanded(g, mr.MergeCommit, target)
}

// resolveVerifyTarget applies the Target Resolution Rule for post-merge
// verification (gu-eakas). When the MR carries a generic default-branch alias
// ("main", "master") but the rig is configured with a different default
// (e.g. "mainline"), return the rig's configured default — the merge landed
// there, not on the literal MR target string. Non-generic targets (integration
// branches, feature branches) pass through unchanged.
func resolveVerifyTarget(mrTarget, rigDefault string) string {
	if mrTarget == rigDefault {
		return mrTarget
	}
	// Only resolve well-known default-branch aliases. An MR targeting an
	// integration branch (e.g. "epic/batch-42") must NOT be remapped.
	switch mrTarget {
	case "main", "master":
		return rigDefault
	}
	return mrTarget
}

// verifyMergeCommitLanded returns nil only when mergeCommit is non-empty and is
// an ancestor of origin/<target> (i.e. the merge genuinely landed on mainline).
// It fetches origin/<target> first so the local view isn't stale. Any failure —
// empty commit, fetch error, ancestry lookup error, or "not an ancestor" — is
// returned as an error so the caller fails closed and leaves the beads open
// rather than silently losing the merge (gu-ilf86).
func verifyMergeCommitLanded(g gitAncestryOps, mergeCommit, target string) error {
	if g == nil {
		return fmt.Errorf("nil git ops")
	}
	commit := strings.TrimSpace(mergeCommit)
	if commit == "" {
		return fmt.Errorf("merge_commit not recorded — cannot verify merge landed on origin/%s", target)
	}
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("empty target branch — cannot verify merge landed")
	}

	// Refresh the remote-tracking ref so the ancestry check sees the current
	// origin tip, not a stale snapshot. A fetch failure is fatal: we cannot
	// confirm the commit landed, so we must not close the beads.
	if err := g.FetchBranch("origin", target); err != nil {
		return fmt.Errorf("fetching origin/%s to verify merge: %w", target, err)
	}

	remoteRef := "origin/" + target
	landed, err := g.IsAncestor(commit, remoteRef)
	if err != nil {
		return fmt.Errorf("checking whether %s landed on %s: %w", commit, remoteRef, err)
	}
	if !landed {
		return fmt.Errorf("merge commit %s is not on %s — merge did not land (possible silent merge-loss)", commit, remoteRef)
	}
	return nil
}

// stripMRIssueTimestampSuffix removes a trailing "--<timestamp>" or
// "@<timestamp>" suffix from an MR's source_issue field. Polecat branches
// are named polecat/<name>/<issue>--<ts> (current) or ...@<ts> (legacy),
// and the submit flow historically wrote the un-stripped branch tail into
// the MR bead. This helper lets the refinery close the actual bug bead
// (gu-aei) rather than a non-existent gu-aei--moiitf15 — see gu-y2w.
//
// The primary fix lives in cmd/mq_submit.go's parseBranchName; this
// function provides belt-and-suspenders protection for MR beads that were
// written by older binaries before that fix shipped.
func stripMRIssueTimestampSuffix(id string) string {
	if idx := strings.Index(id, "--"); idx > 0 {
		return id[:idx]
	}
	if idx := strings.Index(id, "@"); idx > 0 {
		return id[:idx]
	}
	return id
}

// notifyWorkerRejected sends a rejection notification to a polecat.
func (m *Manager) notifyWorkerRejected(mr *MergeRequest, reason string) {
	// Nudge polecat about rejection instead of sending permanent mail.
	polecatName := strings.TrimPrefix(mr.Worker, "polecats/")
	target := fmt.Sprintf("%s/%s", m.rig.Name, polecatName)
	nudgeMsg := fmt.Sprintf("MR rejected: branch=%s issue=%s reason=%s — review feedback and resubmit with 'gt done'",
		mr.Branch, mr.IssueID, reason)
	nudgeCmd := exec.Command("gt", "nudge", target, nudgeMsg)
	util.SetDetachedProcessGroup(nudgeCmd)
	nudgeCmd.Dir = m.workDir
	if err := nudgeCmd.Run(); err != nil {
		log.Printf("warning: nudging worker about rejection for %s: %v", mr.IssueID, err)
	}
}

// Town root is computed in Start() as filepath.Dir(m.rig.Path) and passed
// through to callers — no filesystem-inference function needed (ZFC gt-qago).

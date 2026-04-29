package cmd

// handoff_state.go — mail, state collection, cleanup, and cooldown helpers
// used by the handoff command. Split out of handoff.go to keep each file
// focused (gu-a1q).
//
// This file groups the side-effect-heavy helpers that deal with:
//   - Sending and auto-hooking the handoff mail
//   - Warning about dirty git state
//   - Pinning a bead to the current agent's hook
//   - Collecting git + inbox + beads state for handoff context
//   - Cleaning up in-progress molecule steps before cycling
//   - Enforcing the minimum handoff cooldown

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/ui"
	"github.com/steveyegge/gastown/internal/workspace"
)

// sendHandoffMail sends a handoff mail to self and auto-hooks it.
// Returns the created bead ID and any error.
func sendHandoffMail(subject, message string) (string, error) {
	// Build subject with handoff prefix if not already present
	if subject == "" {
		subject = "🤝 HANDOFF: Session cycling"
	} else if !strings.Contains(subject, "HANDOFF") {
		subject = "🤝 HANDOFF: " + subject
	}

	// Default message if not provided
	if message == "" {
		message = "Context cycling. Check bd ready for pending work."
	}

	// Detect agent identity for self-mail
	agentID, _, _, err := resolveSelfTarget()
	if err != nil {
		return "", fmt.Errorf("detecting agent identity: %w", err)
	}

	// Normalize identity to match mailbox query format
	agentID = mail.AddressToIdentity(agentID)

	// Detect town root for beads location
	townRoot := detectTownRootFromCwd()
	if townRoot == "" {
		return "", fmt.Errorf("cannot detect town root")
	}

	// Build labels for mail metadata (matches mail router format)
	labels := fmt.Sprintf("from:%s", agentID)

	// Create mail bead directly using bd create with --silent to get the ID
	// Mail goes to town-level beads (hq- prefix)
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags.
	args := []string{
		"create",
		"--assignee", agentID,
		"-d", message,
		"--priority", "1", // high — handoffs should float above normal mail
		"--labels", labels + ",gt:message",
		"--actor", agentID,
		// NOT ephemeral: handoff mail must be in issues table so gt hook can find it.
		// Ephemeral wisps are invisible to hook queries and may be reaped before successor reads.
		"--silent", // Output only the bead ID
		"--", subject,
	}

	cmd := BdCmd(args...).
		WithAutoCommit().
		Dir(townRoot).
		Build()
	cmd.Env = append(cmd.Env, "BEADS_DIR="+filepath.Join(townRoot, ".beads"))

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("creating handoff mail: %s", errMsg)
		}
		return "", fmt.Errorf("creating handoff mail: %w", err)
	}

	beadID := strings.TrimSpace(stdout.String())
	if beadID == "" {
		return "", fmt.Errorf("bd create did not return bead ID")
	}

	// Auto-hook the created mail bead
	hookCmd := BdCmd("update", beadID, "--status=hooked", "--assignee="+agentID).
		WithAutoCommit().
		Dir(townRoot).
		Build()
	hookCmd.Env = append(hookCmd.Env, "BEADS_DIR="+filepath.Join(townRoot, ".beads"))
	hookCmd.Stderr = os.Stderr

	if err := hookCmd.Run(); err != nil {
		// Non-fatal: mail was created, just couldn't hook
		style.PrintWarning("created mail %s but failed to auto-hook: %v", beadID, err)
		return beadID, nil
	}

	return beadID, nil
}

// warnHandoffGitStatus checks the current workspace for uncommitted or unpushed
// work and prints a warning if found. Non-blocking — handoff continues regardless.
// Skips .beads/ changes since those are managed by Dolt and not a concern.
func warnHandoffGitStatus() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	g := git.NewGit(cwd)
	if !g.IsRepo() {
		return
	}
	status, err := g.CheckUncommittedWork()
	if err != nil || status.CleanExcludingBeads() {
		return
	}
	fmt.Fprintf(os.Stderr, "%s workspace has uncommitted work: %s\n", ui.IconWarn, status.String())
	if len(status.ModifiedFiles) > 0 {
		fmt.Fprintf(os.Stderr, "%s   modified: %s\n", ui.IconWarn, strings.Join(status.ModifiedFiles, ", "))
	}
	if len(status.UntrackedFiles) > 0 {
		fmt.Fprintf(os.Stderr, "%s   untracked: %s\n", ui.IconWarn, strings.Join(status.UntrackedFiles, ", "))
	}
	if status.UnpushedCommits > 0 {
		fmt.Fprintf(os.Stderr, "%s   %d unpushed commit(s) — run 'git push' before handoff\n", ui.IconWarn, status.UnpushedCommits)
	}
	fmt.Fprintln(os.Stderr, "  (use --no-git-check to suppress this warning)")
}

// looksLikeBeadID checks if a string looks like a bead ID.
// Bead IDs have format: prefix-xxxx where prefix is 1-5 lowercase letters and xxxx is alphanumeric.
// Examples: "gt-abc123", "bd-ka761", "hq-cv-abc", "beads-xyz", "ap-qtsup.16"
func looksLikeBeadID(s string) bool {
	// Find the first hyphen
	idx := strings.Index(s, "-")
	if idx < 1 || idx > 5 {
		// No hyphen, or prefix is empty/too long
		return false
	}

	// Check prefix is all lowercase letters
	prefix := s[:idx]
	for _, c := range prefix {
		if c < 'a' || c > 'z' {
			return false
		}
	}

	// Check there's something after the hyphen
	rest := s[idx+1:]
	if len(rest) == 0 {
		return false
	}

	// Check rest starts with alphanumeric and contains only alphanumeric, dots, hyphens
	for i, c := range rest {
		if i == 0 {
			// First char must be alphanumeric
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				return false
			}
		} else {
			// Subsequent chars: alphanumeric, dots, hyphens
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '-') {
				return false
			}
		}
	}

	return true
}

// hookBeadForHandoff attaches a bead to the current agent's hook.
func hookBeadForHandoff(beadID string) error {
	// Verify the bead exists first
	verifyCmd := exec.Command("bd", "show", beadID, "--json")
	if err := verifyCmd.Run(); err != nil {
		return fmt.Errorf("bead '%s' not found", beadID)
	}

	// Determine agent identity
	agentID, _, _, err := resolveSelfTarget()
	if err != nil {
		return fmt.Errorf("detecting agent identity: %w", err)
	}

	fmt.Printf("%s Hooking %s...\n", style.Bold.Render("🪝"), beadID)

	if handoffDryRun {
		fmt.Printf("Would run: bd update %s --status=pinned --assignee=%s\n", beadID, agentID)
		return nil
	}

	// Pin the bead using bd update (discovery-based approach)
	pinCmd := exec.Command("bd", "update", beadID, "--status=pinned", "--assignee="+agentID)
	pinCmd.Stderr = os.Stderr
	if err := pinCmd.Run(); err != nil {
		return fmt.Errorf("pinning bead: %w", err)
	}

	fmt.Printf("%s Work attached to hook (pinned bead)\n", style.Bold.Render("✓"))
	return nil
}

// collectHandoffState gathers current state for handoff context.
// Collects: git workspace state (deterministic), inbox summary, ready beads, hooked work.
// Git state is always collected first via Go library calls (no shelling out) to ensure
// the handoff always contains useful context even when external commands fail. (GH#1996)
func collectHandoffState() string {
	var parts []string

	// Deterministic git state — always collected via Go library, never empty. (GH#1996)
	if gitState := collectGitState(); gitState != "" {
		parts = append(parts, gitState)
	}

	// Get hooked work
	hookOutput, err := exec.Command("gt", "hook").Output()
	if err == nil {
		hookStr := strings.TrimSpace(string(hookOutput))
		if hookStr != "" && !strings.Contains(hookStr, "Nothing on hook") {
			parts = append(parts, "## Hooked Work\n"+hookStr)
		}
	}

	// Get inbox summary (first few messages)
	inboxOutput, err := exec.Command("gt", "mail", "inbox").Output()
	if err == nil {
		inboxStr := strings.TrimSpace(string(inboxOutput))
		if inboxStr != "" && !strings.Contains(inboxStr, "Inbox empty") {
			// Limit to first 10 lines for brevity
			lines := strings.Split(inboxStr, "\n")
			if len(lines) > 10 {
				lines = append(lines[:10], "... (more messages)")
			}
			parts = append(parts, "## Inbox\n"+strings.Join(lines, "\n"))
		}
	}

	// Get ready beads
	readyOutput, err := exec.Command("bd", "ready").Output()
	if err == nil {
		readyStr := strings.TrimSpace(string(readyOutput))
		if readyStr != "" && !strings.Contains(readyStr, "No issues ready") {
			// Limit to first 10 lines
			lines := strings.Split(readyStr, "\n")
			if len(lines) > 10 {
				lines = append(lines[:10], "... (more issues)")
			}
			parts = append(parts, "## Ready Work\n"+strings.Join(lines, "\n"))
		}
	}

	// Get in-progress beads
	inProgressOutput, err := exec.Command("bd", "list", "--status=in_progress").Output()
	if err == nil {
		ipStr := strings.TrimSpace(string(inProgressOutput))
		if ipStr != "" && !strings.Contains(ipStr, "No issues") {
			lines := strings.Split(ipStr, "\n")
			if len(lines) > 5 {
				lines = append(lines[:5], "... (more)")
			}
			parts = append(parts, "## In Progress\n"+strings.Join(lines, "\n"))
		}
	}

	if len(parts) == 0 {
		return "No active state to report."
	}

	return strings.Join(parts, "\n\n")
}

// collectGitState captures deterministic workspace state using the Go git library.
// This uses only the git.Git wrapper (no shelling out to gt/bd), so it works
// reliably even when PATH is broken or external commands are unavailable.
// Returns empty string if git state cannot be read (e.g., not in a git repo). (GH#1996)
func collectGitState() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	g := git.NewGit(cwd)
	if !g.IsRepo() {
		return ""
	}

	var lines []string

	// Branch
	if branch, err := g.CurrentBranch(); err == nil && branch != "" {
		lines = append(lines, "Branch: "+branch)
	}

	// Uncommitted work summary (skip section on error, don't bail entirely)
	if work, err := g.CheckUncommittedWork(); err == nil {
		if work.HasUncommittedChanges {
			if len(work.ModifiedFiles) > 0 {
				files := work.ModifiedFiles
				if len(files) > 10 {
					files = append(files[:10], fmt.Sprintf("... (+%d more)", len(work.ModifiedFiles)-10))
				}
				lines = append(lines, "Modified: "+strings.Join(files, ", "))
			}
			if len(work.UntrackedFiles) > 0 {
				files := work.UntrackedFiles
				if len(files) > 5 {
					files = append(files[:5], fmt.Sprintf("... (+%d more)", len(work.UntrackedFiles)-5))
				}
				lines = append(lines, "Untracked: "+strings.Join(files, ", "))
			}
		}
		if work.StashCount > 0 {
			lines = append(lines, fmt.Sprintf("Stashes: %d", work.StashCount))
		}
		if work.UnpushedCommits > 0 {
			lines = append(lines, fmt.Sprintf("Unpushed commits: %d", work.UnpushedCommits))
		}
	}

	// Recent commits (last 5) for context on what was being worked on.
	if logStr, err := g.RecentCommits(5); err == nil && logStr != "" {
		lines = append(lines, "Recent commits:\n"+logStr)
	}

	if len(lines) == 0 {
		return ""
	}

	return "## Workspace State\n" + strings.Join(lines, "\n")
}

// cleanupMoleculeOnHandoff closes any in-progress molecule steps before session
// handoff, preventing orphaned wisps from accumulating. (gt-e26g)
//
// Without this, patrol agents (witness, refinery, deacon) that handoff mid-cycle
// leave unfinished molecule steps open forever. The next session pours a new
// molecule, so the old steps are never completed.
//
// All errors are non-fatal — handoff must succeed even if cleanup fails.
func cleanupMoleculeOnHandoff() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return
	}

	// Detect agent identity
	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return
	}
	roleCtx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}
	agentID := buildAgentIdentity(roleCtx)
	if agentID == "" {
		return
	}

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return
	}

	b := beads.New(workDir)

	// Extract the role name for FindHandoffBead
	parts := strings.Split(agentID, "/")
	role := parts[len(parts)-1]

	handoffBead, err := b.FindHandoffBead(role)
	if err != nil || handoffBead == nil {
		return
	}

	// Check for attached molecule on the handoff bead
	attachment := beads.ParseAttachmentFields(handoffBead)
	if attachment == nil || attachment.AttachedMolecule == "" {
		return
	}

	molID := attachment.AttachedMolecule

	// Close descendant steps (the leaked wisps)
	if n := closeDescendants(b, molID); n > 0 {
		fmt.Fprintf(os.Stderr, "handoff: closed %d molecule step(s) for %s\n", n, molID)
	}

	// Detach molecule with audit trail
	if _, err := b.DetachMoleculeWithAudit(handoffBead.ID, beads.DetachOptions{
		Operation: "squash",
		Reason:    "handoff: session cycling",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "handoff: warning: detach molecule audit failed: %v\n", err)
	}

	// Close all descendant wisps first, then the molecule root.
	// Without this, handoff leaks orphan wisps into the DB.
	// Best-effort in handoff path — log but proceed.
	if _, err := forceCloseDescendants(b, molID); err != nil {
		style.PrintWarning("handoff: could not close descendants of %s: %v", molID, err)
	}

	// Force-close the molecule root wisp
	if err := b.ForceCloseWithReason("handoff", molID); err != nil {
		fmt.Fprintf(os.Stderr, "handoff: warning: couldn't close molecule %s: %v\n", molID, err)
	}
}

// enforceHandoffCooldown sleeps if the last handoff was too recent.
// This prevents tight restart loops when patrol agents (e.g., witness)
// complete quickly on idle rigs and immediately hand off. (gt-058d)
//
// The cooldown is based on the modification time of the last_handoff_ts
// file in the .runtime directory. If the file exists and was written
// less than MinHandoffCooldown ago, the function sleeps for the remaining
// time. This ensures at least MinHandoffCooldown passes between handoffs.
//
// Crew and mayor roles are exempt — they hand off on human request,
// not on patrol loops, so the cooldown just gets in the way.
func enforceHandoffCooldown() {
	if role := os.Getenv("GT_ROLE"); role != "" {
		parsed, _, _ := parseRoleString(role)
		switch parsed {
		case RoleMayor, RoleCrew:
			return
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	tsPath := filepath.Join(cwd, constants.DirRuntime, constants.FileLastHandoffTS)
	info, err := os.Stat(tsPath)
	if err != nil {
		return // No previous handoff recorded — first handoff, no cooldown
	}

	age := time.Since(info.ModTime())
	if age >= constants.MinHandoffCooldown {
		return // Enough time has passed
	}

	remaining := constants.MinHandoffCooldown - age
	fmt.Printf("%s Handoff cooldown: waiting %v (last handoff %v ago, min %v)\n",
		style.Dim.Render("⏳"), remaining.Round(time.Second),
		age.Round(time.Second), constants.MinHandoffCooldown)
	time.Sleep(remaining)
}

// recordHandoffTime writes the current timestamp to the handoff cooldown file.
// Called before respawning to establish the baseline for the next cooldown check.
func recordHandoffTime() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	runtimeDir := filepath.Join(cwd, constants.DirRuntime)
	_ = os.MkdirAll(runtimeDir, 0755)
	tsPath := filepath.Join(runtimeDir, constants.FileLastHandoffTS)
	_ = os.WriteFile(tsPath, []byte(fmt.Sprintf("%d", time.Now().Unix())), 0644)
}

// isPatrolRole returns true if the role runs a patrol loop (refinery, witness, deacon).
// Patrol roles must re-enter their patrol molecule on handoff rather than
// "waiting for instructions," which leads to idle CPU burn.
func isPatrolRole(role string) bool {
	switch role {
	case "refinery", "witness", "deacon":
		return true
	}
	return false
}

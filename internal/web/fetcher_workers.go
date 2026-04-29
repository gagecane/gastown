package web

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/activity"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
)

// FetchWorkers fetches all running worker sessions (polecats and refinery) with activity data.
func (f *LiveConvoyFetcher) FetchWorkers() ([]WorkerRow, error) {
	// Load registered rigs to filter sessions
	rigsConfigPath := filepath.Join(f.townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs config: %w", err)
	}

	// Build set of registered rig names
	registeredRigs := make(map[string]bool)
	for rigName := range rigsConfig.Rigs {
		registeredRigs[rigName] = true
	}

	// Pre-fetch assigned issues map: assignee -> (issueID, title)
	assignedIssues := f.getAssignedIssuesMap()

	// Query all tmux sessions with window_activity for more accurate timing
	stdout, err := runCmd(f.tmuxCmdTimeout, "tmux", f.tmuxArgs("list-sessions", "-F", "#{session_name}|#{window_activity}")...)
	if err != nil {
		// tmux not running or no sessions
		return nil, nil
	}

	// Pre-fetch merge queue count to determine refinery idle status
	mergeQueueCount := f.getMergeQueueCount()

	var workers []WorkerRow
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}

		sessionName := parts[0]

		// Parse session name using the fetcher's own registry to avoid
		// dependency on global DefaultRegistry initialization (gt-y24).
		identity, err := session.ParseSessionNameWithRegistry(sessionName, f.registry)
		if err != nil {
			log.Printf("dashboard: FetchWorkers: skipping session %q: %v", sessionName, err)
			continue
		}

		rig := identity.Rig

		// Skip rigs not registered in this workspace
		if !registeredRigs[rig] {
			continue
		}

		// Skip non-worker sessions (witness, mayor, deacon, boot)
		switch identity.Role {
		case session.RoleMayor, session.RoleDeacon, session.RoleWitness:
			continue
		}

		// Determine agent type and worker name
		workerName := identity.Name
		agentType := constants.RolePolecat // Default for ephemeral sessions (polecats, crew)
		if identity.Role == session.RoleRefinery {
			agentType = constants.RoleRefinery
		}

		// Parse activity timestamp
		var activityUnix int64
		if _, err := fmt.Sscanf(parts[1], "%d", &activityUnix); err != nil || activityUnix == 0 {
			continue
		}
		activityTime := time.Unix(activityUnix, 0)
		activityAge := time.Since(activityTime)

		// Get status hint - special handling for refinery
		var statusHint string
		if workerName == "refinery" {
			statusHint = f.getRefineryStatusHint(mergeQueueCount)
		} else {
			statusHint = f.getWorkerStatusHint(sessionName)
		}

		// Look up assigned issue for this worker
		// Assignee format: "rigname/polecats/workername"
		assignee := fmt.Sprintf("%s/polecats/%s", rig, workerName)
		var issueID, issueTitle string
		if issue, ok := assignedIssues[assignee]; ok {
			issueID = issue.ID
			issueTitle = issue.Title
			// Keep full title - CSS handles overflow
		}

		// Calculate work status based on activity age and issue assignment
		workStatus := calculateWorkerWorkStatus(activityAge, issueID, workerName, f.staleThreshold, f.stuckThreshold)

		workers = append(workers, WorkerRow{
			Name:         workerName,
			Rig:          rig,
			SessionID:    sessionName,
			LastActivity: activity.Calculate(activityTime),
			StatusHint:   statusHint,
			IssueID:      issueID,
			IssueTitle:   issueTitle,
			WorkStatus:   workStatus,
			AgentType:    agentType,
		})
	}

	return workers, nil
}

// assignedIssue holds issue info for the assigned issues map.
type assignedIssue struct {
	ID    string
	Title string
}

// getAssignedIssuesMap returns a map of assignee -> assigned issue.
// Queries beads for all in_progress issues with assignees.
func (f *LiveConvoyFetcher) getAssignedIssuesMap() map[string]assignedIssue {
	result := make(map[string]assignedIssue)

	// Query all in_progress issues (these are the ones being worked on)
	stdout, err := f.runBdCmd(f.townRoot, "list", "--status=in_progress", "--json")
	if err != nil {
		log.Printf("warning: bd list in_progress failed: %v", err)
		return result
	}

	var issues []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		log.Printf("warning: parsing bd list output: %v", err)
		return result
	}

	for _, issue := range issues {
		if issue.Assignee != "" {
			result[issue.Assignee] = assignedIssue{
				ID:    issue.ID,
				Title: issue.Title,
			}
		}
	}

	return result
}

// calculateWorkerWorkStatus determines the worker's work status based on activity and assignment.
// Returns: "working", "stale", "stuck", or "idle"
func calculateWorkerWorkStatus(activityAge time.Duration, issueID, workerName string, staleThreshold, stuckThreshold time.Duration) string {
	// Refinery has special handling - it's always "working" if it has PRs
	if workerName == "refinery" {
		return "working"
	}

	// No issue assigned = idle
	if issueID == "" {
		return "idle"
	}

	// Has issue - determine status based on activity
	switch {
	case activityAge < staleThreshold:
		return "working" // Active recently
	case activityAge < stuckThreshold:
		return "stale" // Might be thinking or stuck
	default:
		return "stuck" // Likely stuck - no activity for threshold+ minutes
	}
}

// getWorkerStatusHint captures the last non-empty line from a worker's pane.
func (f *LiveConvoyFetcher) getWorkerStatusHint(sessionName string) string {
	stdout, err := runCmd(f.tmuxCmdTimeout, "tmux", f.tmuxArgs("capture-pane", "-t", sessionName, "-p", "-J")...)
	if err != nil {
		return ""
	}

	// Get last non-empty line
	lines := strings.Split(stdout.String(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			// Truncate long lines
			if len(line) > 60 {
				line = line[:57] + "..."
			}
			return line
		}
	}
	return ""
}

// getMergeQueueCount returns the total number of open PRs across all repos.
func (f *LiveConvoyFetcher) getMergeQueueCount() int {
	mergeQueue, err := f.FetchMergeQueue()
	if err != nil {
		return 0
	}
	return len(mergeQueue)
}

// getRefineryStatusHint returns appropriate status for refinery based on merge queue.
func (f *LiveConvoyFetcher) getRefineryStatusHint(mergeQueueCount int) string {
	if mergeQueueCount == 0 {
		return "Idle - Waiting for PRs"
	}
	if mergeQueueCount == 1 {
		return "Processing 1 PR"
	}
	return fmt.Sprintf("Processing %d PRs", mergeQueueCount)
}

// parseActivityTimestamp parses a Unix timestamp string from tmux.
// Returns (0, false) for invalid or zero timestamps.
func parseActivityTimestamp(s string) (int64, bool) {
	var unix int64
	if _, err := fmt.Sscanf(s, "%d", &unix); err != nil || unix <= 0 {
		return 0, false
	}
	return unix, true
}

package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
)

// trackedIssueInfo holds info about an issue being tracked by a convoy.
type trackedIssueInfo struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Type      string   `json:"dependency_type"`
	IssueType string   `json:"issue_type"`
	Blocked   bool     `json:"blocked,omitempty"`    // True if issue currently has blockers
	Assignee  string   `json:"assignee,omitempty"`   // Assigned agent (e.g., gastown/polecats/goose)
	Labels    []string `json:"labels,omitempty"`     // Bead labels (propagated from trackedDependency)
	Worker    string   `json:"worker,omitempty"`     // Worker currently assigned (e.g., gastown/nux)
	WorkerAge string   `json:"worker_age,omitempty"` // How long worker has been on this issue
}

// trackedDependency is dep-list data enriched with fresh issue details.
type trackedDependency struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Status         string   `json:"status"`
	IssueType      string   `json:"issue_type"`
	Assignee       string   `json:"assignee"`
	DependencyType string   `json:"dependency_type"`
	Labels         []string `json:"labels"`
	Blocked        bool     `json:"-"`
}

func applyFreshIssueDetails(dep *trackedDependency, details *issueDetails) {
	dep.Status = details.Status
	dep.Blocked = details.IsBlocked()
	if dep.Title == "" {
		dep.Title = details.Title
	}
	if dep.Assignee == "" {
		dep.Assignee = details.Assignee
	}
	if dep.IssueType == "" {
		dep.IssueType = details.IssueType
	}
	// Always refresh labels unconditionally — bd dep list may return stale
	// labels from dependency records, but bd show returns current bead labels.
	// This ensures isReadyIssue sees accurate queue labels (gt:queued,
	// gt:queue-dispatched) for cross-rig beads. Assigning even when fresh
	// labels are empty clears stale queue labels that would otherwise
	// suppress stranded issue detection.
	dep.Labels = details.Labels
}

// getTrackedIssues gets issues tracked by a convoy with fresh cross-rig details.
// Returns issue details including status, type, and worker info.
//
// Prefers raw SQL query against the dependencies table (bdDepListRawIDs) which
// avoids the JOIN with the issues table that silently drops cross-database
// dependencies (see GH #2624, #2832). Falls back to bd dep list and bd show
// for older bd versions that don't support bd sql.
// Then fetches fresh issue details via bd show with prefix routing.
func getTrackedIssues(townBeads, convoyID string) ([]trackedIssueInfo, error) {
	// Prefer raw SQL — works for cross-database deps where tracked beads
	// live in different Dolt databases. Falls back to bd dep list if bd sql
	// is not available (older bd versions).
	trackedIDs, err := bdDepListRawIDs(townBeads, convoyID, "down", "tracks")
	if err != nil {
		// bd sql not supported (older bd) — fall back to bd dep list.
		trackedIDs, err = bdDepListTracked(townBeads, convoyID)
		if err != nil {
			return nil, fmt.Errorf("querying tracked issues for %s: %w", convoyID, err)
		}
	}

	// Fallback: when dep queries return empty (common for cross-database deps
	// on older bd where the JOIN fails), try parsing from bd show output.
	if len(trackedIDs) == 0 {
		trackedIDs, err = bdShowTrackedDeps(townBeads, convoyID)
		if err != nil {
			return nil, fmt.Errorf("fallback show for tracked deps of %s: %w", convoyID, err)
		}
	}

	if len(trackedIDs) == 0 {
		return nil, nil
	}

	// Fetch fresh issue details via bd show (uses prefix routing for cross-rig).
	freshDetails := getIssueDetailsBatch(trackedIDs)

	// Build tracked dependency structs from fresh details
	var deps []trackedDependency
	for _, id := range trackedIDs {
		dep := trackedDependency{
			ID:             id,
			DependencyType: "tracks",
		}
		if details, ok := freshDetails[id]; ok {
			applyFreshIssueDetails(&dep, details)
		}
		deps = append(deps, dep)
	}

	// Collect non-closed issue IDs for worker lookup
	openIssueIDs := make([]string, 0, len(deps))
	for _, dep := range deps {
		if dep.Status != "closed" {
			openIssueIDs = append(openIssueIDs, dep.ID)
		}
	}
	workersMap := getWorkersForIssues(openIssueIDs)

	// Build result
	var tracked []trackedIssueInfo
	for _, dep := range deps {
		info := trackedIssueInfo{
			ID:        dep.ID,
			Title:     dep.Title,
			Status:    dep.Status,
			Type:      dep.DependencyType,
			IssueType: dep.IssueType,
			Blocked:   dep.Blocked,
			Assignee:  dep.Assignee,
			Labels:    dep.Labels,
		}

		// Add worker info if available
		if worker, ok := workersMap[dep.ID]; ok {
			info.Worker = worker.Worker
			info.WorkerAge = worker.Age
		}

		tracked = append(tracked, info)
	}

	return tracked, nil
}

// bdDepListTracked runs `bd dep list <convoyID> --direction=down --type=tracks --json`
// and returns the tracked issue IDs (unwrapped from external: prefixes).
// Uses --allow-stale for consistency with sling's other bd calls (verifyBeadExists,
// bdShowBead) — without it, a jsonl write that straddles a second boundary causes
// "database out of sync" errors in CI and fast-turnaround production workflows.
func bdDepListTracked(dir, convoyID string) ([]string, error) {
	out, err := runBdJSON(dir, "dep", "list", convoyID, "--direction=down", "--type=tracks", "--allow-stale", "--json")
	if err != nil {
		return nil, err
	}

	var results []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parsing dep list for %s: %w", convoyID, err)
	}

	seen := make(map[string]bool, len(results))
	var ids []string
	for _, r := range results {
		id := beads.ExtractIssueID(r.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// bdShowTrackedDeps falls back to `bd show <convoyID> --json` and extracts
// tracked dependency IDs from the convoy's dependencies array.
// This handles cross-database dependencies where bd dep list returns empty.
func bdShowTrackedDeps(dir, convoyID string) ([]string, error) {
	out, err := runBdJSON(dir, "show", convoyID, "--json")
	if err != nil {
		return nil, err
	}

	var results []struct {
		Dependencies []issueDependency `json:"dependencies"`
	}
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parsing show for %s: %w", convoyID, err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool)
	var ids []string
	for _, dep := range results[0].Dependencies {
		if dep.DependencyType != "tracks" {
			continue
		}
		id := beads.ExtractIssueID(dep.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}


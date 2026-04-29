package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/workspace"
)

type issueDependency struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	DependencyType string `json:"dependency_type"`
}

type issueDetailsJSON struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	Status         string            `json:"status"`
	IssueType      string            `json:"issue_type"`
	Assignee       string            `json:"assignee"`
	Labels         []string          `json:"labels"`
	BlockedBy      []string          `json:"blocked_by"`
	BlockedByCount int               `json:"blocked_by_count"`
	Dependencies   []issueDependency `json:"dependencies"`
}

func (issue issueDetailsJSON) toIssueDetails() *issueDetails {
	return &issueDetails{
		ID:             issue.ID,
		Title:          issue.Title,
		Status:         issue.Status,
		IssueType:      issue.IssueType,
		Assignee:       issue.Assignee,
		Labels:         issue.Labels,
		BlockedBy:      issue.BlockedBy,
		BlockedByCount: issue.BlockedByCount,
		Dependencies:   issue.Dependencies,
	}
}

// getExternalIssueDetails fetches issue details from an external rig database.
// townBeads: path to town .beads directory
// rigName: name of the rig (e.g., "claycantrell")
// issueID: the issue ID to look up
func getExternalIssueDetails(townBeads, rigName, issueID string) *issueDetails {
	// Resolve rig directory path: townBeads is the town root
	rigDir := filepath.Join(townBeads, rigName)

	// Check if rig directory exists
	if _, err := os.Stat(rigDir); os.IsNotExist(err) {
		return nil
	}

	// Query the rig database by running bd show from the rig directory
	showArgs := beads.MaybePrependAllowStale([]string{"show", issueID, "--json"})
	showCmd := exec.Command("bd", showArgs...)
	showCmd.Dir = rigDir // Set working directory to rig directory
	var stdout bytes.Buffer
	showCmd.Stdout = &stdout

	if err := showCmd.Run(); err != nil {
		return nil
	}
	if stdout.Len() == 0 {
		return nil
	}

	var issues []issueDetailsJSON
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil
	}
	if len(issues) == 0 {
		return nil
	}

	return issues[0].toIssueDetails()
}

// issueDetails holds basic issue info.
type issueDetails struct {
	ID             string
	Title          string
	Status         string
	IssueType      string
	Assignee       string
	Labels         []string
	BlockedBy      []string
	BlockedByCount int
	Dependencies   []issueDependency
}

func (d issueDetails) IsBlocked() bool {
	if d.BlockedByCount > 0 || len(d.BlockedBy) > 0 {
		return true
	}

	// bd show can omit blocked_by_count; fall back to live dependency edges.
	for _, dep := range d.Dependencies {
		if dep.DependencyType == "blocks" && dep.Status != "closed" && dep.Status != "tombstone" {
			return true
		}
	}

	return false
}

// getIssueDetailsBatch fetches details for multiple issues in a single bd show call.
// Returns a map from issue ID to details. Missing/invalid issues are omitted from the map.
func getIssueDetailsBatch(issueIDs []string) map[string]*issueDetails {
	result := make(map[string]*issueDetails)
	if len(issueIDs) == 0 {
		return result
	}

	// Build args: bd show id1 id2 id3 ... --json
	args := append([]string{"show"}, issueIDs...)
	args = append(args, "--json")

	// Run from town root so bd's prefix routing (routes.jsonl) can dispatch
	// to the correct rig database for cross-rig bead lookups. (GH#2960)
	townRoot, _ := workspace.FindFromCwdOrError()
	showCmd := exec.Command("bd", args...)
	if townRoot != "" {
		showCmd.Dir = townRoot
		showCmd.Env = stripEnvKey(os.Environ(), "BEADS_DIR")
	}
	var stdout bytes.Buffer
	showCmd.Stdout = &stdout

	if err := showCmd.Run(); err != nil {
		// Batch failed - fall back to individual lookups for robustness
		// This handles cases where some IDs are invalid/missing
		for _, id := range issueIDs {
			if details := getIssueDetails(id); details != nil {
				result[id] = details
			}
		}
		return result
	}

	var issues []issueDetailsJSON
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return result
	}

	for _, issue := range issues {
		result[issue.ID] = issue.toIssueDetails()
	}

	return result
}

// getIssueDetails fetches issue details by trying to show it via bd.
// Prefer getIssueDetailsBatch for multiple issues to avoid N+1 subprocess calls.
func getIssueDetails(issueID string) *issueDetails {
	// Use bd show with routing - resolve from town root so bd's prefix
	// routing (routes.jsonl) can dispatch to the correct rig database.
	// Without Dir + StripBeadsDir, bd inherits CWD/BEADS_DIR which may
	// point to a rig that doesn't contain the target bead. (GH#2960)
	townRoot, _ := workspace.FindFromCwdOrError()
	showCmd := exec.Command("bd", "show", issueID, "--json")
	if townRoot != "" {
		showCmd.Dir = townRoot
		showCmd.Env = stripEnvKey(os.Environ(), "BEADS_DIR")
	}
	var stdout bytes.Buffer
	showCmd.Stdout = &stdout

	if err := showCmd.Run(); err != nil {
		return nil
	}
	// Handle bd exit 0 bug: empty stdout means not found
	if stdout.Len() == 0 {
		return nil
	}

	var issues []issueDetailsJSON
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil || len(issues) == 0 {
		return nil
	}

	return issues[0].toIssueDetails()
}


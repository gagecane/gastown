// Package beads provides read/list operations against the bd CLI.
package beads

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	beadsdk "github.com/steveyegge/beads"
)

// List returns issues matching the given options.
// When Ephemeral is true, uses "bd query" with ephemeral=true to search the
// wisps table (where ephemeral issues live in beads v0.59+). Without this,
// "bd list" only searches the issues table and misses wisps entirely.
func (b *Beads) List(opts ListOptions) ([]*Issue, error) {
	if b.store != nil && !opts.Ephemeral {
		return b.storeList(opts)
	}
	if opts.Ephemeral {
		return b.listEphemeral(opts)
	}

	args := []string{"list", "--json"}

	if opts.Status != "" {
		args = append(args, "--status="+opts.Status)
	}
	// Prefer Label over Type (Type is deprecated)
	if opts.Label != "" {
		args = append(args, "--label="+opts.Label)
	} else if opts.Type != "" {
		// Deprecated: convert type to label for backward compatibility
		args = append(args, "--label=gt:"+opts.Type)
	}
	if opts.Priority >= 0 {
		args = append(args, fmt.Sprintf("--priority=%d", opts.Priority))
	}
	if opts.Parent != "" {
		args = append(args, "--parent="+opts.Parent)
	}
	if opts.Assignee != "" {
		args = append(args, "--assignee="+opts.Assignee)
	}
	if opts.NoAssignee {
		args = append(args, "--no-assignee")
	}
	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("--limit=%d", opts.Limit))
	} else {
		// Override bd's default limit of 50 to avoid silent truncation
		args = append(args, "--limit=0")
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	// bd list --json may return plain text (e.g., "No issues found.") instead
	// of an empty JSON array when there are no results. Handle gracefully.
	if len(out) == 0 || !isJSONBytes(out) {
		return nil, nil
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	return issues, nil
}

// listEphemeral searches the wisps table using "bd query" with ephemeral=true.
// This is necessary because "bd list" only searches the issues table and does
// not support an --ephemeral flag. Wisps (ephemeral issues like merge-request
// beads) live in a separate table since beads v0.59.
func (b *Beads) listEphemeral(opts ListOptions) ([]*Issue, error) {
	// Build query expression: ephemeral=true AND <filters>
	clauses := []string{"ephemeral=true"}

	if opts.Label != "" {
		clauses = append(clauses, "label="+opts.Label)
	} else if opts.Type != "" {
		clauses = append(clauses, "label=gt:"+opts.Type)
	}
	if opts.Status != "" && opts.Status != "all" {
		clauses = append(clauses, "status="+opts.Status)
	}
	if opts.Priority >= 0 {
		clauses = append(clauses, fmt.Sprintf("priority=%d", opts.Priority))
	}
	if opts.Parent != "" {
		clauses = append(clauses, "parent="+opts.Parent)
	}
	if opts.Assignee != "" {
		clauses = append(clauses, "assignee="+opts.Assignee)
	}

	queryExpr := strings.Join(clauses, " AND ")
	args := []string{"query", "--json", queryExpr}

	if opts.Status == "all" {
		args = append(args, "--all")
	}
	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("--limit=%d", opts.Limit))
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	if len(out) == 0 || !isJSONBytes(out) {
		return nil, nil
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd query output: %w", err)
	}

	return issues, nil
}

// ListMergeRequests returns merge-request beads from both the issues table
// and the wisps table. MRs are created as ephemeral (wisps) by gt mq submit,
// but bd list only queries the issues table. This method queries the wisps
// table via bd sql --json to get full data including labels and assignee.
func (b *Beads) ListMergeRequests(opts ListOptions) ([]*Issue, error) {
	// 1. Query issues table (bd list) — don't use Ephemeral since bd query
	// can't parse colons in label values like "gt:merge-request".
	opts.Ephemeral = false
	issueResults, err := b.List(opts)
	if err != nil {
		return nil, err
	}

	// Build dedup map from issues
	seen := make(map[string]bool, len(issueResults))
	for _, issue := range issueResults {
		seen[issue.ID] = true
	}

	// 2. Query wisps table via SQL for merge-request wisps with full data
	statusFilter := "w.status = 'open'"
	if opts.Status != "" && strings.EqualFold(opts.Status, "all") {
		statusFilter = "1=1"
	} else if opts.Status != "" {
		statusFilter = fmt.Sprintf("w.status = '%s'", strings.ReplaceAll(strings.ToLower(opts.Status), "'", "''"))
	}

	labelFilter := "l.label = 'gt:merge-request'"
	if opts.Label != "" {
		labelFilter = fmt.Sprintf("l.label = '%s'", strings.ReplaceAll(opts.Label, "'", "''"))
	}

	query := fmt.Sprintf(
		"SELECT w.id, w.title, w.description, w.status, w.priority, w.assignee, "+
			"w.created_at, w.updated_at, w.created_by, "+
			"GROUP_CONCAT(al.label) as labels_csv "+
			"FROM wisps w "+
			"JOIN wisp_labels l ON w.id = l.issue_id "+
			"LEFT JOIN wisp_labels al ON w.id = al.issue_id "+
			"WHERE %s AND %s "+
			"GROUP BY w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at, w.created_by",
		labelFilter, statusFilter)

	sqlOut, sqlErr := b.run("sql", "--json", query)
	if sqlErr == nil && len(sqlOut) > 0 && isJSONBytes(sqlOut) {
		var rows []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Status      string `json:"status"`
			Priority    int    `json:"priority"`
			Assignee    string `json:"assignee"`
			CreatedAt   string `json:"created_at"`
			UpdatedAt   string `json:"updated_at"`
			CreatedBy   string `json:"created_by"`
			LabelsCSV   string `json:"labels_csv"`
		}
		if jsonErr := json.Unmarshal(sqlOut, &rows); jsonErr == nil {
			for _, row := range rows {
				if seen[row.ID] {
					continue
				}
				issue := &Issue{
					ID:          row.ID,
					Title:       row.Title,
					Description: row.Description,
					Status:      row.Status,
					Priority:    row.Priority,
					Assignee:    row.Assignee,
					CreatedAt:   row.CreatedAt,
					UpdatedAt:   row.UpdatedAt,
					CreatedBy:   row.CreatedBy,
					Ephemeral:   true,
				}
				if row.LabelsCSV != "" {
					issue.Labels = strings.Split(row.LabelsCSV, ",")
				}
				issueResults = append(issueResults, issue)
			}
		}
	}

	return issueResults, nil
}

// ListByAssignee returns all issues assigned to a specific assignee.
// The assignee is typically in the format "rig/polecats/polecatName" (e.g., "gastown/polecats/Toast").
func (b *Beads) ListByAssignee(assignee string) ([]*Issue, error) {
	return b.List(ListOptions{
		Status:   "all", // Include both open and closed for state derivation
		Assignee: assignee,
		Priority: -1, // No priority filter
	})
}

// GetAssignedIssue returns the first issue assigned to the given assignee.
// Checks open, in_progress, and hooked statuses (hooked = work on agent's hook).
// Returns nil if no matching issue is assigned.
func (b *Beads) GetAssignedIssue(assignee string) (*Issue, error) {
	// Check all active work statuses: open, in_progress, and hooked
	// "hooked" status is set by gt sling when work is attached to an agent's hook
	for _, status := range []string{"open", "in_progress", StatusHooked} {
		issues, err := b.List(ListOptions{
			Status:   status,
			Assignee: assignee,
			Priority: -1,
		})
		if err != nil {
			return nil, err
		}
		if len(issues) > 0 {
			return issues[0], nil
		}
	}

	return nil, nil
}

// Ready returns issues that are ready to work (not blocked).
func (b *Beads) Ready() ([]*Issue, error) {
	if b.store != nil {
		return b.storeReady()
	}

	out, err := b.run("ready", "--json")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd ready output: %w", err)
	}

	return issues, nil
}

// ReadyForMol returns ready steps within a specific molecule.
// Delegates to bd ready --mol which uses beads' canonical blocking semantics
// (blocked_issues_cache), handling all blocking types, transitive propagation,
// and conditional-blocks resolution.
func (b *Beads) ReadyForMol(moleculeID string) ([]*Issue, error) {
	if b.store != nil {
		return b.storeReadyWithFilter(beadsdk.WorkFilter{
			ParentID: &moleculeID,
			Limit:    100,
		})
	}

	out, err := b.run("ready", "--mol", moleculeID, "--json", "-n", "100")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd ready --mol output: %w", err)
	}

	return issues, nil
}

// ReadyWithType returns ready issues filtered by label.
// Uses bd ready --label flag for server-side filtering.
// The issueType is converted to a gt:<type> label (e.g., "molecule" -> "gt:molecule").
func (b *Beads) ReadyWithType(issueType string) ([]*Issue, error) {
	if b.store != nil {
		return b.storeReadyWithFilter(beadsdk.WorkFilter{
			Labels: []string{"gt:" + issueType},
			Limit:  100,
		})
	}

	out, err := b.run("ready", "--json", "--label", "gt:"+issueType, "-n", "100")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd ready output: %w", err)
	}

	return issues, nil
}

// Show returns detailed information about an issue.
func (b *Beads) Show(id string) (*Issue, error) {
	// Route cross-rig queries via routes.jsonl so that rig-level bead IDs
	// (e.g., "gt-abc123") resolve to the correct rig database.
	targetDir := ResolveRoutingTarget(b.getTownRoot(), id, b.getResolvedBeadsDir())
	if targetDir != b.getResolvedBeadsDir() {
		target := NewWithBeadsDir(filepath.Dir(targetDir), targetDir)
		return target.Show(id)
	}

	if b.store != nil {
		return b.storeShow(id)
	}

	out, err := b.run("show", id, "--json")
	if err != nil {
		return nil, err
	}

	// bd show --json returns an array with one element
	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd show output: %w", err)
	}

	if len(issues) == 0 {
		return nil, ErrNotFound
	}

	return issues[0], nil
}

// FindLatestIssueByTitleAndAssignee finds the newest issue matching the given title and assignee.
func (b *Beads) FindLatestIssueByTitleAndAssignee(title, assignee string) (*Issue, error) {
	out, err := b.run("list", "--json", "--limit", "0", "--title", title, "--assignee", assignee)
	if err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}
	if len(issues) == 0 {
		return nil, ErrNotFound
	}

	var newest *Issue
	for _, issue := range issues {
		if issue.Title != title || issue.Assignee != assignee {
			continue
		}
		if newest == nil || issue.CreatedAt > newest.CreatedAt {
			newest = issue
		}
	}
	if newest == nil {
		return nil, ErrNotFound
	}
	return newest, nil
}

// ShowMultiple fetches multiple issues by ID in a single bd call.
// Returns a map of ID to Issue. Missing IDs are not included in the map.
func (b *Beads) ShowMultiple(ids []string) (map[string]*Issue, error) {
	if len(ids) == 0 {
		return make(map[string]*Issue), nil
	}

	if b.store != nil {
		return b.storeShowMultiple(ids)
	}

	// bd show supports multiple IDs
	args := append([]string{"show", "--json"}, ids...)
	out, err := b.run(args...)
	if err != nil {
		return nil, fmt.Errorf("bd show: %w", err)
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd show output: %w", err)
	}

	result := make(map[string]*Issue, len(issues))
	for _, issue := range issues {
		result[issue.ID] = issue
	}

	return result, nil
}

// Blocked returns issues that are blocked by dependencies.
func (b *Beads) Blocked() ([]*Issue, error) {
	if b.store != nil {
		return b.storeBlocked()
	}

	out, err := b.run("blocked", "--json")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd blocked output: %w", err)
	}

	return issues, nil
}

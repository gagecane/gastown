package web

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FetchMergeQueue fetches open PRs from registered rigs.
func (f *LiveConvoyFetcher) FetchMergeQueue() ([]MergeQueueRow, error) {
	// Query beads for open merge requests (the actual Refinery queue).
	stdout, err := f.runBdCmd(f.townBeads, "list", "--label=gt:merge-request", "--json")
	if err != nil {
		return nil, fmt.Errorf("querying merge requests: %w", err)
	}

	var issues []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Assignee    string   `json:"assignee"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil, fmt.Errorf("parsing merge requests: %w", err)
	}

	result := make([]MergeQueueRow, 0, len(issues))
	for _, iss := range issues {
		// Parse branch and target from description (format: "branch: X\ntarget: Y\n...")
		branch, target, rig, crID := parseMRDescription(iss.Description)
		color := "mq-yellow"
		if iss.Status == "hooked" {
			color = "mq-green" // Being processed by refinery
		}
		result = append(result, MergeQueueRow{
			ID:         iss.ID,
			CRID:       crID,
			Repo:       rig,
			Title:      iss.Title,
			Branch:     branch,
			Target:     target,
			Status:     iss.Status,
			Assignee:   iss.Assignee,
			ColorClass: color,
		})
	}
	return result, nil
}

// parseMRDescription extracts branch, target, rig, and CR ID from MR bead description.
func parseMRDescription(desc string) (branch, target, rig, crID string) {
	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "branch:") {
			branch = strings.TrimSpace(strings.TrimPrefix(line, "branch:"))
		} else if strings.HasPrefix(line, "target:") {
			target = strings.TrimSpace(strings.TrimPrefix(line, "target:"))
		} else if strings.HasPrefix(line, "rig:") {
			rig = strings.TrimSpace(strings.TrimPrefix(line, "rig:"))
		} else if strings.HasPrefix(line, "cr:") {
			crID = strings.TrimSpace(strings.TrimPrefix(line, "cr:"))
		}
	}
	return
}

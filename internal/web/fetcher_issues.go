package web

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// FetchIssues returns open issues (the backlog).
func (f *LiveConvoyFetcher) FetchIssues() ([]IssueRow, error) {
	// Query both open AND hooked issues for the Work panel
	// Open = ready to assign, Hooked = in progress
	var allBeads []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Type      string   `json:"type"`
		Priority  int      `json:"priority"`
		Labels    []string `json:"labels"`
		CreatedAt string   `json:"created_at"`
	}

	// Fetch open issues
	if stdout, err := f.runBdCmd(f.townRoot, "list", "--status=open", "--json", "--limit=50"); err == nil {
		var openBeads []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Type      string   `json:"type"`
			Priority  int      `json:"priority"`
			Labels    []string `json:"labels"`
			CreatedAt string   `json:"created_at"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &openBeads); err == nil {
			allBeads = append(allBeads, openBeads...)
		}
	}

	// Fetch hooked issues (in progress)
	if stdout, err := f.runBdCmd(f.townRoot, "list", "--status=hooked", "--json", "--limit=50"); err == nil {
		var hookedBeads []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Type      string   `json:"type"`
			Priority  int      `json:"priority"`
			Labels    []string `json:"labels"`
			CreatedAt string   `json:"created_at"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &hookedBeads); err == nil {
			allBeads = append(allBeads, hookedBeads...)
		}
	}

	beads := allBeads

	var rows []IssueRow
	for _, bead := range beads {
		// Skip internal types (messages, convoys, queues, merge-requests, wisps)
		// Check both legacy type field and gt: labels
		isInternal := false
		switch bead.Type {
		case "message", "convoy", "queue", "merge-request", "wisp", "agent":
			isInternal = true
		}
		for _, l := range bead.Labels {
			switch l {
			case "gt:message", "gt:convoy", "gt:queue", "gt:merge-request", "gt:wisp", "gt:agent":
				isInternal = true
			}
		}
		if isInternal {
			continue
		}

		row := IssueRow{
			ID:       bead.ID,
			Title:    bead.Title,
			Type:     bead.Type,
			Priority: bead.Priority,
		}

		// Keep full title - CSS handles overflow

		// Format labels (skip internal labels)
		var displayLabels []string
		for _, label := range bead.Labels {
			if !strings.HasPrefix(label, "gt:") && !strings.HasPrefix(label, "internal:") {
				displayLabels = append(displayLabels, label)
			}
		}
		if len(displayLabels) > 0 {
			row.Labels = strings.Join(displayLabels, ", ")
			if len(row.Labels) > 25 {
				row.Labels = row.Labels[:22] + "..."
			}
		}

		// Calculate age
		if bead.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, bead.CreatedAt); err == nil {
				row.Age = formatTimestamp(t)
			}
		}

		rows = append(rows, row)
	}

	// Sort by priority (1=critical first), then by age
	sort.Slice(rows, func(i, j int) bool {
		pi, pj := rows[i].Priority, rows[j].Priority
		if pi == 0 {
			pi = 5 // Treat unset priority as low
		}
		if pj == 0 {
			pj = 5
		}
		if pi != pj {
			return pi < pj
		}
		return rows[i].Age > rows[j].Age // Older first for same priority
	})

	return rows, nil
}

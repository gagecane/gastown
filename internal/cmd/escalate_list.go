package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/workspace"
)

// runEscalateList lists escalation beads (open or all) with live-Dolt filtering
// to skip phantom entries whose bead IDs are no longer resolvable.
func runEscalateList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))

	var issues []*beads.Issue
	if escalateListAll {
		// List all (open and closed)
		out, err := bd.Run("list", "--label=gt:escalation", "--status=all", "--json")
		if err != nil {
			return fmt.Errorf("listing escalations: %w", err)
		}
		if err := json.Unmarshal(out, &issues); err != nil {
			return fmt.Errorf("parsing escalations: %w", err)
		}
	} else {
		issues, err = bd.ListEscalations()
		if err != nil {
			return fmt.Errorf("listing escalations: %w", err)
		}
	}

	// Cross-check each entry against live Dolt to filter out phantom escalations.
	// When a rig's Dolt server dies and is restarted fresh, the label-based list
	// query may still return stale IDs (e.g. from a cached or cross-rig query)
	// that no longer exist in the live database. We skip any entries that cannot
	// be fetched individually, since they cannot be acked or closed anyway.
	var live []*beads.Issue
	var phantomCount int
	for _, issue := range issues {
		if _, err := bd.Show(issue.ID); err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				phantomCount++
				fmt.Fprintf(os.Stderr, "warning: skipping unresolvable escalation %s (not found in live Dolt)\n", issue.ID)
				continue
			}
			// For other errors (e.g. Dolt temporarily unreachable), include
			// the entry so the user can see it — just warn.
			fmt.Fprintf(os.Stderr, "warning: could not verify escalation %s: %v\n", issue.ID, err)
		}
		live = append(live, issue)
	}
	issues = live

	if escalateListJSON {
		out, _ := json.MarshalIndent(issues, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	if len(issues) == 0 {
		if phantomCount > 0 {
			fmt.Printf("No escalations found (%d phantom entr%s skipped — bead IDs no longer exist in live Dolt)\n",
				phantomCount, map[bool]string{true: "y", false: "ies"}[phantomCount == 1])
		} else {
			fmt.Println("No escalations found")
		}
		return nil
	}

	fmt.Printf("Escalations (%d):\n\n", len(issues))
	for _, issue := range issues {
		fields := beads.ParseEscalationFields(issue.Description)
		emoji := severityEmoji(fields.Severity)

		status := issue.Status
		if beads.HasLabel(issue, "acked") {
			status = "acked"
		}

		fmt.Printf("  %s %s [%s] %s\n", emoji, issue.ID, status, issue.Title)
		fmt.Printf("     Severity: %s | From: %s | %s\n",
			fields.Severity, fields.EscalatedBy, formatRelativeTime(issue.CreatedAt))
		if fields.AckedBy != "" {
			fmt.Printf("     Acked by: %s\n", fields.AckedBy)
		}
		fmt.Println()
	}

	return nil
}

// runEscalateShow renders details for a single escalation bead.
func runEscalateShow(cmd *cobra.Command, args []string) error {
	escalationID := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	issue, fields, err := bd.GetEscalationBead(escalationID)
	if err != nil {
		return fmt.Errorf("getting escalation: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("escalation not found: %s", escalationID)
	}

	if escalateJSON {
		data := map[string]interface{}{
			"id":           issue.ID,
			"title":        issue.Title,
			"status":       issue.Status,
			"created_at":   issue.CreatedAt,
			"severity":     fields.Severity,
			"reason":       fields.Reason,
			"escalatedBy":  fields.EscalatedBy,
			"escalatedAt":  fields.EscalatedAt,
			"ackedBy":      fields.AckedBy,
			"ackedAt":      fields.AckedAt,
			"closedBy":     fields.ClosedBy,
			"closedReason": fields.ClosedReason,
			"relatedBead":  fields.RelatedBead,
		}
		out, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	emoji := severityEmoji(fields.Severity)
	fmt.Printf("%s Escalation: %s\n", emoji, issue.ID)
	fmt.Printf("  Title: %s\n", issue.Title)
	fmt.Printf("  Status: %s\n", issue.Status)
	fmt.Printf("  Severity: %s\n", fields.Severity)
	fmt.Printf("  Created: %s\n", formatRelativeTime(issue.CreatedAt))
	fmt.Printf("  Escalated by: %s\n", fields.EscalatedBy)
	if fields.Reason != "" {
		fmt.Printf("  Reason: %s\n", fields.Reason)
	}
	if fields.AckedBy != "" {
		fmt.Printf("  Acknowledged by: %s at %s\n", fields.AckedBy, fields.AckedAt)
	}
	if fields.ClosedBy != "" {
		fmt.Printf("  Closed by: %s\n", fields.ClosedBy)
		fmt.Printf("  Resolution: %s\n", fields.ClosedReason)
	}
	if fields.RelatedBead != "" {
		fmt.Printf("  Related: %s\n", fields.RelatedBead)
	}

	return nil
}

// severityEmoji returns a visual emoji marker for a severity level.
func severityEmoji(severity string) string {
	switch severity {
	case config.SeverityCritical:
		return "🚨"
	case config.SeverityHigh:
		return "⚠️"
	case config.SeverityMedium:
		return "📢"
	case config.SeverityLow:
		return "ℹ️"
	default:
		return "📋"
	}
}

// formatRelativeTime renders a timestamp as a coarse "N units ago" string.
func formatRelativeTime(timestamp string) string {
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return timestamp
	}

	duration := time.Since(t)
	if duration < time.Minute {
		return "just now"
	}
	if duration < time.Hour {
		mins := int(duration.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	}
	if duration < 24*time.Hour {
		hours := int(duration.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	}
	days := int(duration.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
)

func runConvoyStatus(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// If no ID provided, show all active convoys
	if len(args) == 0 {
		return showAllConvoyStatus(townBeads)
	}

	convoyID := args[0]

	// Check if it's a numeric shortcut (e.g., "1" instead of "hq-cv-xyz")
	if n, err := strconv.Atoi(convoyID); err == nil && n > 0 {
		resolved, err := resolveConvoyNumber(townBeads, n)
		if err != nil {
			return err
		}
		convoyID = resolved
	}

	// Get convoy details
	showOut, err := runBdJSON(townBeads, "show", convoyID, "--json")
	if err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	// Parse convoy data
	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Description string   `json:"description"`
		CreatedAt   string   `json:"created_at"`
		ClosedAt    string   `json:"closed_at,omitempty"`
		DependsOn   []string `json:"depends_on,omitempty"`
		Labels      []string `json:"labels,omitempty"`
	}
	if err := json.Unmarshal(showOut, &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	convoy := convoys[0]

	// Check if convoy is owned (caller-managed lifecycle)
	isOwned := hasLabel(convoy.Labels, "gt:owned")

	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return fmt.Errorf("getting tracked issues for %s: %w", convoyID, err)
	}

	// Count completed
	completed := 0
	for _, t := range tracked {
		if t.Status == "closed" {
			completed++
		}
	}

	if convoyStatusJSON {
		lifecycle := "system-managed"
		if isOwned {
			lifecycle = "caller-managed"
		}
		type jsonStatus struct {
			ID            string             `json:"id"`
			Title         string             `json:"title"`
			Status        string             `json:"status"`
			Owned         bool               `json:"owned"`
			Lifecycle     string             `json:"lifecycle"`
			MergeStrategy string             `json:"merge_strategy,omitempty"`
			Tracked       []trackedIssueInfo `json:"tracked"`
			Completed     int                `json:"completed"`
			Total         int                `json:"total"`
		}
		out := jsonStatus{
			ID:            convoy.ID,
			Title:         convoy.Title,
			Status:        convoy.Status,
			Owned:         isOwned,
			Lifecycle:     lifecycle,
			MergeStrategy: convoyMergeFromFields(convoy.Description),
			Tracked:       tracked,
			Completed:     completed,
			Total:         len(tracked),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human-readable output
	fmt.Printf("🚚 %s %s\n\n", style.Bold.Render(convoy.ID+":"), convoy.Title)
	fmt.Printf("  Status:    %s\n", formatConvoyStatus(convoy.Status))
	fmt.Printf("  Owned:     %s\n", formatYesNo(isOwned))
	if isOwned {
		fmt.Printf("  Lifecycle: %s\n", style.Warning.Render("caller-managed"))
	} else {
		fmt.Printf("  Lifecycle: %s\n", "system-managed")
	}
	merge := convoyMergeFromFields(convoy.Description)
	if merge != "" {
		fmt.Printf("  Merge:     %s\n", merge)
	}
	fmt.Printf("  Progress:  %d/%d completed\n", completed, len(tracked))
	fmt.Printf("  Created:   %s\n", convoy.CreatedAt)
	if convoy.ClosedAt != "" {
		fmt.Printf("  Closed:    %s\n", convoy.ClosedAt)
	}

	if len(tracked) > 0 {
		fmt.Printf("\n  %s\n", style.Bold.Render("Tracked Issues:"))
		for _, t := range tracked {
			// Status symbol: ✓ closed, ▶ in_progress/hooked, ○ other
			status := "○"
			switch t.Status {
			case "closed":
				status = "✓"
			case "in_progress", "hooked":
				status = "▶"
			}

			// Show assignee in brackets (extract short name from path like gastown/polecats/goose -> goose)
			bracketContent := t.IssueType
			if t.Assignee != "" {
				parts := strings.Split(t.Assignee, "/")
				bracketContent = parts[len(parts)-1] // Last part of path
			} else if bracketContent == "" {
				bracketContent = "unassigned"
			}

			line := fmt.Sprintf("    %s %s: %s [%s]", status, t.ID, t.Title, bracketContent)
			if t.Worker != "" {
				workerDisplay := "@" + t.Worker
				if t.WorkerAge != "" {
					workerDisplay += fmt.Sprintf(" (%s)", t.WorkerAge)
				}
				line += fmt.Sprintf("  %s", style.Dim.Render(workerDisplay))
			}
			fmt.Println(line)
		}
	}

	// Hint for owned convoys when all issues are complete
	if isOwned && completed == len(tracked) && len(tracked) > 0 && normalizeConvoyStatus(convoy.Status) == convoyStatusOpen {
		fmt.Printf("\n  %s\n", style.Dim.Render("All issues complete. Land with: gt convoy land "+convoyID))
	}

	return nil
}

func showAllConvoyStatus(townBeads string) error {
	// List all convoy-type issues
	out, err := runBdJSON(townBeads, "list", "--type=convoy", "--status=open", "--json")
	if err != nil {
		return fmt.Errorf("listing convoys: %w", err)
	}

	var convoys []struct {
		ID     string   `json:"id"`
		Title  string   `json:"title"`
		Status string   `json:"status"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &convoys); err != nil {
		return fmt.Errorf("parsing convoy list: %w", err)
	}

	if len(convoys) == 0 {
		fmt.Println("No active convoys.")
		fmt.Println("Create a convoy with: gt convoy create <name> [issues...]")
		return nil
	}

	if convoyStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(convoys)
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Active Convoys"))
	for _, c := range convoys {
		ownedTag := ""
		if hasLabel(c.Labels, "gt:owned") {
			ownedTag = " " + style.Warning.Render("[owned]")
		}
		fmt.Printf("  🚚 %s: %s%s\n", c.ID, c.Title, ownedTag)
	}
	fmt.Printf("\nUse 'gt convoy status <id>' for detailed status.\n")

	return nil
}

func runConvoyList(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// List convoy-type issues.
	listArgs := []string{"list", "--type=convoy", "--json"}
	if convoyListStatus != "" {
		listArgs = append(listArgs, "--status="+convoyListStatus)
	} else if convoyListAll {
		listArgs = append(listArgs, "--all")
	}
	// bd no longer requires --flat for JSON output.

	out, err := runBdJSON(townBeads, listArgs...)
	if err != nil {
		return fmt.Errorf("listing convoys: %w", err)
	}

	var convoys []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Status    string   `json:"status"`
		CreatedAt string   `json:"created_at"`
		Labels    []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &convoys); err != nil {
		return fmt.Errorf("parsing convoy list: %w", err)
	}

	if convoyListJSON {
		// Enrich each convoy with tracked issues and completion counts
		type convoyListEntry struct {
			ID        string             `json:"id"`
			Title     string             `json:"title"`
			Status    string             `json:"status"`
			CreatedAt string             `json:"created_at"`
			Tracked   []trackedIssueInfo `json:"tracked"`
			Completed int                `json:"completed"`
			Total     int                `json:"total"`
		}
		enriched := make([]convoyListEntry, 0, len(convoys))
		for _, c := range convoys {
			tracked, err := getTrackedIssues(townBeads, c.ID)
			if err != nil {
				style.PrintWarning("skipping convoy %s: %v", c.ID, err)
				continue
			}
			if tracked == nil {
				tracked = []trackedIssueInfo{} // Ensure JSON [] not null
			}
			completed := 0
			for _, t := range tracked {
				if t.Status == "closed" {
					completed++
				}
			}
			enriched = append(enriched, convoyListEntry{
				ID:        c.ID,
				Title:     c.Title,
				Status:    c.Status,
				CreatedAt: c.CreatedAt,
				Tracked:   tracked,
				Completed: completed,
				Total:     len(tracked),
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(enriched)
	}

	if len(convoys) == 0 {
		fmt.Println("No convoys found.")
		fmt.Println("Create a convoy with: gt convoy create <name> [issues...]")
		return nil
	}

	// Tree view: show convoys with their child issues
	if convoyListTree {
		return printConvoyTree(townBeads, convoys)
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Convoys"))
	for i, c := range convoys {
		status := formatConvoyStatus(c.Status)
		ownedTag := ""
		if hasLabel(c.Labels, "gt:owned") {
			ownedTag = " " + style.Warning.Render("[owned]")
		}
		fmt.Printf("  %d. 🚚 %s: %s %s%s\n", i+1, c.ID, c.Title, status, ownedTag)
	}
	fmt.Printf("\nUse 'gt convoy status <id>' or 'gt convoy status <n>' for detailed view.\n")

	return nil
}

// printConvoyTree displays convoys with their child issues in a tree format.
func printConvoyTree(townBeads string, convoys []struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	CreatedAt string   `json:"created_at"`
	Labels    []string `json:"labels"`
}) error {
	for _, c := range convoys {
		// Get tracked issues for this convoy
		tracked, err := getTrackedIssues(townBeads, c.ID)
		if err != nil {
			style.PrintWarning("skipping convoy %s: %v", c.ID, err)
			continue
		}

		// Count completed
		completed := 0
		for _, t := range tracked {
			if t.Status == "closed" {
				completed++
			}
		}

		// Print convoy header with progress
		total := len(tracked)
		progress := ""
		if total > 0 {
			progress = fmt.Sprintf(" (%d/%d)", completed, total)
		}
		ownedTag := ""
		if hasLabel(c.Labels, "gt:owned") {
			ownedTag = " " + style.Warning.Render("[owned]")
		}
		fmt.Printf("🚚 %s: %s%s%s\n", c.ID, c.Title, progress, ownedTag)

		// Print tracked issues as tree children
		for i, t := range tracked {
			// Determine tree connector
			isLast := i == len(tracked)-1
			connector := "├──"
			if isLast {
				connector = "└──"
			}

			// Status symbol: ✓ closed, ▶ in_progress/hooked, ○ other
			status := "○"
			switch t.Status {
			case "closed":
				status = "✓"
			case "in_progress", "hooked":
				status = "▶"
			}

			fmt.Printf("%s %s %s: %s\n", connector, status, t.ID, t.Title)
		}

		// Add blank line between convoys
		fmt.Println()
	}

	return nil
}


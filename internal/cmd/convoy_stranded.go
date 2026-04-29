package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	convoyops "github.com/steveyegge/gastown/internal/convoy"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
)

// strandedConvoyInfo holds info about a stranded convoy.
type strandedConvoyInfo struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	TrackedCount int      `json:"tracked_count"`
	ReadyCount   int      `json:"ready_count"`
	ReadyIssues  []string `json:"ready_issues"`
	CreatedAt    string   `json:"created_at,omitempty"`
	BaseBranch   string   `json:"base_branch,omitempty"`
}

// readyIssueInfo holds info about a ready (stranded) issue.
type readyIssueInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority string `json:"priority"`
}

func runConvoyStranded(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	stranded, err := findStrandedConvoys(townBeads)
	if err != nil {
		return err
	}

	if convoyStrandedJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stranded)
	}

	if len(stranded) == 0 {
		fmt.Println("No stranded convoys found.")
		return nil
	}

	fmt.Printf("%s Found %d stranded convoy(s):\n\n", style.Warning.Render("⚠"), len(stranded))
	for _, s := range stranded {
		fmt.Printf("  🚚 %s: %s\n", s.ID, s.Title)
		if s.ReadyCount == 0 && s.TrackedCount == 0 {
			fmt.Printf("     Empty convoy (0 tracked issues) — needs cleanup\n")
		} else if s.ReadyCount == 0 && s.TrackedCount > 0 {
			fmt.Printf("     %d tracked issues, 0 ready — needs agent review\n", s.TrackedCount)
		} else {
			fmt.Printf("     Ready issues: %d (of %d tracked)\n", s.ReadyCount, s.TrackedCount)
			for _, issueID := range s.ReadyIssues {
				fmt.Printf("       • %s\n", issueID)
			}
		}
		fmt.Println()
	}

	// Separate feed advice, needs-attention convoys, and cleanup advice.
	var feedable, needsAttention, empty []strandedConvoyInfo
	for _, s := range stranded {
		if s.ReadyCount > 0 {
			feedable = append(feedable, s)
		} else if s.TrackedCount > 0 {
			needsAttention = append(needsAttention, s)
		} else {
			empty = append(empty, s)
		}
	}

	if len(feedable) > 0 {
		fmt.Println("To feed stranded convoys, run:")
		for _, s := range feedable {
			fmt.Printf("  gt sling mol-convoy-feed deacon/dogs --var convoy=%s\n", s.ID)
		}
	}
	if len(needsAttention) > 0 {
		if len(feedable) > 0 {
			fmt.Println()
		}
		fmt.Println("Needs agent review (tracked issues exist but none are ready):")
		for _, s := range needsAttention {
			fmt.Printf("  🚚 %s (%d tracked, 0 ready)\n", s.ID, s.TrackedCount)
		}
	}
	if len(empty) > 0 {
		if len(feedable) > 0 || len(needsAttention) > 0 {
			fmt.Println()
		}
		fmt.Println("To close empty convoys, run:")
		for _, s := range empty {
			fmt.Printf("  gt convoy check %s\n", s.ID)
		}
	}
	fmt.Println()
	fmt.Println(style.Dim.Render("  Note: Pool dispatch auto-creates dogs if pool is under capacity."))

	return nil
}

// findStrandedConvoys finds convoys with ready work but no workers,
// or empty convoys (0 tracked issues) that need cleanup.
func findStrandedConvoys(townBeads string) ([]strandedConvoyInfo, error) {
	stranded := []strandedConvoyInfo{} // Initialize as empty slice for proper JSON encoding

	// List all open convoys
	out, err := runBdJSON(townBeads, "list", "--type=convoy", "--status=open", "--json")
	if err != nil {
		return nil, fmt.Errorf("listing convoys: %w", err)
	}

	var convoys []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		CreatedAt   string `json:"created_at"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &convoys); err != nil {
		return nil, fmt.Errorf("parsing convoy list: %w", err)
	}

	// Check each convoy for stranded state
	for _, convoy := range convoys {
		// Extract base_branch from convoy description fields
		var baseBranch string
		if cf := beads.ParseConvoyFields(&beads.Issue{Description: convoy.Description}); cf != nil {
			baseBranch = cf.BaseBranch
		}

		tracked, err := getTrackedIssues(townBeads, convoy.ID)
		if err != nil {
			// Write to stderr explicitly — stdout may be consumed as JSON
			// by the daemon's JSON parser (fixes #2142).
			fmt.Fprintf(os.Stderr, "⚠ Warning: skipping convoy %s: %v\n", convoy.ID, err)
			continue
		}
		// Empty convoys (0 tracked issues) are stranded — they need
		// attention (auto-close via convoy check or manual cleanup).
		if len(tracked) == 0 {
			stranded = append(stranded, strandedConvoyInfo{
				ID:           convoy.ID,
				Title:        convoy.Title,
				TrackedCount: 0,
				ReadyCount:   0,
				ReadyIssues:  []string{},
				CreatedAt:    convoy.CreatedAt,
				BaseBranch:   baseBranch,
			})
			continue
		}

		// Find ready issues (open, not blocked, no live assignee, slingable).
		// Town-level beads (hq- prefix with path=".") are excluded because
		// they can't be dispatched via gt sling -- they're handled by the deacon.
		// Non-slingable types (epics, convoys, etc.) are also excluded.

		// Batch-check scheduling status for all tracked issues (single DB query).
		var trackedIDs []string
		for _, t := range tracked {
			trackedIDs = append(trackedIDs, t.ID)
		}
		scheduledSet := areScheduled(trackedIDs)

		var readyIssues []string
		for _, t := range tracked {
			if isReadyIssue(t, scheduledSet) {
				if !isSlingableBead(townBeads, t.ID) {
					continue
				}
				if !convoyops.IsSlingableType(t.IssueType) {
					continue
				}
				// Ghost-dispatch guard (gu-ypjm): skip identity beads even if
				// they slip through as slingable. Matches by gt:agent label,
				// status=closed, or identity/system title regex.
				if convoyops.IsIdentityBead(t.Title, t.Status, t.Labels) {
					continue
				}
				readyIssues = append(readyIssues, t.ID)
			}
		}

		if len(readyIssues) > 0 {
			stranded = append(stranded, strandedConvoyInfo{
				ID:           convoy.ID,
				Title:        convoy.Title,
				TrackedCount: len(tracked),
				ReadyCount:   len(readyIssues),
				ReadyIssues:  readyIssues,
				CreatedAt:    convoy.CreatedAt,
				BaseBranch:   baseBranch,
			})
		} else {
			// Has tracked issues but none are ready — include in stranded
			// list so callers can distinguish from truly empty convoys.
			stranded = append(stranded, strandedConvoyInfo{
				ID:           convoy.ID,
				Title:        convoy.Title,
				TrackedCount: len(tracked),
				ReadyCount:   0,
				ReadyIssues:  []string{},
				CreatedAt:    convoy.CreatedAt,
				BaseBranch:   baseBranch,
			})
		}
	}

	return stranded, nil
}

// isReadyIssue checks if an issue is ready for dispatch (stranded).
// An issue is ready if:
// - status = "open" AND (no assignee OR assignee session is dead)
// - OR status = "in_progress"/"hooked" AND assignee session is dead (orphaned molecule)
// - AND not blocked (cross-rig-aware from issue details)
// scheduledSet is a pre-computed set of bead IDs with open sling contexts (from areScheduled).
func isReadyIssue(t trackedIssueInfo, scheduledSet map[string]bool) bool {
	// Closed issues are never ready
	if t.Status == "closed" || t.Status == "tombstone" {
		return false
	}

	// Must not be blocked
	if t.Blocked {
		return false
	}

	// Scheduled beads are not stranded — they're waiting for dispatch capacity.
	if scheduledSet[t.ID] {
		return false
	}

	// Open issues with no assignee are trivially ready
	if t.Status == "open" && t.Assignee == "" {
		return true
	}

	// For issues with an assignee (or non-open status with molecule attached),
	// check if the worker session is still alive
	if t.Assignee == "" {
		// Non-open status but no assignee is an edge case (shouldn't happen
		// normally, but could occur if molecule detached improperly)
		return true
	}

	// Has assignee - check if session is alive
	// Use the shared assigneeToSessionName from rig.go
	sessionName, _ := assigneeToSessionName(t.Assignee)
	if sessionName == "" {
		return true // Can't determine session = treat as ready
	}

	// Check if tmux session exists
	checkCmd := tmux.BuildCommand("has-session", "-t", sessionName)
	if err := checkCmd.Run(); err != nil {
		// Session doesn't exist = orphaned molecule or dead worker
		// This is the key fix: issues with in_progress/hooked status but
		// dead workers are now correctly detected as stranded
		return true
	}

	return false // Session exists = worker is active
}

// isSlingableBead reports whether a bead can be dispatched via gt sling.
// Town-level beads (hq- prefix with path=".") and beads with unknown
// prefixes are not slingable — they're handled by the deacon/mayor.
func isSlingableBead(townRoot, beadID string) bool {
	prefix := beads.ExtractPrefix(beadID)
	if prefix == "" {
		return true // No prefix info, assume slingable
	}
	return beads.GetRigNameForPrefix(townRoot, prefix) != ""
}

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

func runConvoyClose(cmd *cobra.Command, args []string) error {
	convoyID := args[0]

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// Get convoy details
	showArgs := []string{"show", convoyID, "--json"}
	showCmd := exec.Command("bd", showArgs...)
	showCmd.Dir = townBeads
	var stdout bytes.Buffer
	showCmd.Stdout = &stdout

	if err := showCmd.Run(); err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	var convoys []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Status      string `json:"status"`
		Type        string `json:"issue_type"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	convoy := convoys[0]

	// Verify it's actually a convoy type
	if convoy.Type != "convoy" {
		return fmt.Errorf("'%s' is not a convoy (type: %s)", convoyID, convoy.Type)
	}
	if err := ensureKnownConvoyStatus(convoy.Status); err != nil {
		return fmt.Errorf("convoy '%s' has invalid lifecycle state: %w", convoyID, err)
	}

	// Idempotent: if already closed, just report it
	if normalizeConvoyStatus(convoy.Status) == convoyStatusClosed {
		fmt.Printf("%s Convoy %s is already closed\n", style.Dim.Render("○"), convoyID)
		return nil
	}
	if err := validateConvoyStatusTransition(convoy.Status, convoyStatusClosed); err != nil {
		return fmt.Errorf("can't close convoy '%s': %w", convoyID, err)
	}

	// Verify all tracked issues are done (unless --force)
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		// If we can't check tracked issues, require --force
		if !convoyCloseForce {
			return fmt.Errorf("couldn't verify tracked issues: %w\n  Use --force to close anyway", err)
		}
		style.PrintWarning("couldn't verify tracked issues: %v", err)
	}

	if len(tracked) > 0 && !convoyCloseForce {
		var openIssues []trackedIssueInfo
		for _, t := range tracked {
			if t.Status != "closed" && t.Status != "tombstone" {
				openIssues = append(openIssues, t)
			}
		}

		if len(openIssues) > 0 {
			fmt.Printf("%s Convoy %s has %d open issue(s):\n\n", style.Warning.Render("⚠"), convoyID, len(openIssues))
			for _, t := range openIssues {
				status := "○"
				if t.Status == "in_progress" || t.Status == "hooked" {
					status = "▶"
				}
				fmt.Printf("    %s %s: %s [%s]\n", status, t.ID, t.Title, t.Status)
			}
			fmt.Printf("\n  Use %s to close anyway.\n", style.Bold.Render("--force"))
			return fmt.Errorf("convoy has %d open issue(s)", len(openIssues))
		}
	}

	// Build close reason
	reason := convoyCloseReason
	if reason == "" {
		if convoyCloseForce {
			reason = "Force closed"
		} else {
			reason = "All tracked issues completed"
		}
	}

	// Close the convoy
	closeArgs := []string{"close", convoyID, "-r", reason}
	closeCmd := exec.Command("bd", closeArgs...)
	closeCmd.Dir = townBeads

	if err := closeCmd.Run(); err != nil {
		return fmt.Errorf("closing convoy: %w", err)
	}

	fmt.Printf("%s Closed convoy 🚚 %s: %s\n", style.Bold.Render("✓"), convoyID, convoy.Title)
	if convoyCloseReason != "" {
		fmt.Printf("  Reason: %s\n", convoyCloseReason)
	}

	// Report cleanup summary
	if len(tracked) > 0 {
		closedCount := 0
		openCount := 0
		for _, t := range tracked {
			if t.Status == "closed" || t.Status == "tombstone" {
				closedCount++
			} else {
				openCount++
			}
		}
		fmt.Printf("  Tracked: %d issue(s) (%d closed", len(tracked), closedCount)
		if openCount > 0 {
			fmt.Printf(", %d still open", openCount)
		}
		fmt.Println(")")
	}

	// Report molecule if present
	convoyFields := beads.ParseConvoyFields(&beads.Issue{Description: convoy.Description})
	if convoyFields != nil && convoyFields.Molecule != "" {
		fmt.Printf("  Molecule: %s (not auto-detached)\n", convoyFields.Molecule)
	}

	// Send notification if --notify flag provided
	if convoyCloseNotify != "" {
		sendCloseNotification(convoyCloseNotify, convoyID, convoy.Title, reason)
	} else {
		// Check if convoy has a notify address in description
		notifyConvoyCompletion(townBeads, convoyID, convoy.Title)
	}

	return nil
}

// sendCloseNotification sends a notification about convoy closure.
func sendCloseNotification(addr, convoyID, title, reason string) {
	subject := fmt.Sprintf("🚚 Convoy closed: %s", title)
	body := fmt.Sprintf("Convoy %s has been closed.\n\nReason: %s", convoyID, reason)

	mailArgs := []string{"mail", "send", addr, "-s", subject, "-m", body}
	mailCmd := exec.Command("gt", mailArgs...)
	if err := mailCmd.Run(); err != nil {
		style.PrintWarning("couldn't send notification: %v", err)
	} else {
		fmt.Printf("  Notified: %s\n", addr)
	}
}

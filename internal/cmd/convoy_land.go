package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

func runConvoyLand(cmd *cobra.Command, args []string) error {
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
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Type        string   `json:"issue_type"`
		Description string   `json:"description"`
		Labels      []string `json:"labels,omitempty"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	convoy := convoys[0]

	// Verify it's a convoy type
	if convoy.Type != "convoy" {
		return fmt.Errorf("'%s' is not a convoy (type: %s)", convoyID, convoy.Type)
	}

	// Verify the convoy is owned
	if !hasLabel(convoy.Labels, "gt:owned") {
		return fmt.Errorf("convoy '%s' is not an owned convoy\n  Only convoys created with --owned can be landed.\n  Use %s instead for non-owned convoys.",
			convoyID, style.Bold.Render("gt convoy close"))
	}

	// Check if already closed
	if err := ensureKnownConvoyStatus(convoy.Status); err != nil {
		return fmt.Errorf("convoy '%s' has invalid lifecycle state: %w", convoyID, err)
	}
	if normalizeConvoyStatus(convoy.Status) == convoyStatusClosed {
		fmt.Printf("%s Convoy %s is already closed\n", style.Dim.Render("○"), convoyID)
		return nil
	}

	// Get tracked issues
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		if !convoyLandForce {
			return fmt.Errorf("couldn't verify tracked issues: %w\n  Use --force to land anyway", err)
		}
		style.PrintWarning("couldn't verify tracked issues: %v", err)
	}

	// Check if all tracked issues are done
	var openIssues []trackedIssueInfo
	for _, t := range tracked {
		if t.Status != "closed" && t.Status != "tombstone" {
			openIssues = append(openIssues, t)
		}
	}

	if len(openIssues) > 0 && !convoyLandForce {
		fmt.Printf("%s Convoy %s has %d open issue(s):\n\n", style.Warning.Render("⚠"), convoyID, len(openIssues))
		for _, t := range openIssues {
			status := "○"
			if t.Status == "in_progress" || t.Status == "hooked" {
				status = "▶"
			}
			fmt.Printf("    %s %s: %s [%s]\n", status, t.ID, t.Title, t.Status)
		}
		fmt.Printf("\n  Use %s to land anyway.\n", style.Bold.Render("--force"))
		return fmt.Errorf("convoy has %d open issue(s)", len(openIssues))
	}

	if convoyLandDryRun {
		fmt.Printf("%s Dry run — would land convoy 🚚 %s: %s\n\n", style.Warning.Render("⚠"), convoyID, convoy.Title)
		fmt.Printf("  Tracked: %d issue(s) (%d closed, %d open)\n", len(tracked), len(tracked)-len(openIssues), len(openIssues))
		if !convoyLandKeep {
			worktrees := findConvoyWorktrees(tracked)
			fmt.Printf("  Worktrees to clean: %d\n", len(worktrees))
			for _, wt := range worktrees {
				fmt.Printf("    • %s (%s)\n", wt.polecatName, wt.rigName)
			}
		} else {
			fmt.Printf("  Worktrees: skipped (--keep-worktrees)\n")
		}
		fmt.Printf("  Close reason: Landed by owner\n")
		return nil
	}

	// Phase 1: Clean up polecat worktrees
	if !convoyLandKeep {
		worktrees := findConvoyWorktrees(tracked)
		if len(worktrees) > 0 {
			fmt.Printf("  Cleaning up %d worktree(s)...\n", len(worktrees))
			for _, wt := range worktrees {
				if err := removePolecatWorktree(wt); err != nil {
					style.PrintWarning("couldn't remove worktree %s/%s: %v", wt.rigName, wt.polecatName, err)
				} else {
					fmt.Printf("    %s %s/%s\n", style.Dim.Render("✓"), wt.rigName, wt.polecatName)
				}
			}
		}
	}

	// Phase 2: Close the convoy
	reason := "Landed by owner"
	closeArgs := []string{"close", convoyID, "-r", reason}
	closeCmd := exec.Command("bd", closeArgs...)
	closeCmd.Dir = townBeads

	if err := closeCmd.Run(); err != nil {
		return fmt.Errorf("closing convoy: %w", err)
	}

	fmt.Printf("\n%s Landed convoy 🚚 %s: %s\n", style.Bold.Render("✓"), convoyID, convoy.Title)
	fmt.Printf("  Reason: %s\n", reason)
	if len(tracked) > 0 {
		closedCount := len(tracked) - len(openIssues)
		fmt.Printf("  Tracked: %d issue(s) (%d closed", len(tracked), closedCount)
		if len(openIssues) > 0 {
			fmt.Printf(", %d still open", len(openIssues))
		}
		fmt.Println(")")
	}

	// Phase 3: Send completion notifications
	notifyConvoyCompletion(townBeads, convoyID, convoy.Title)

	return nil
}

// convoyWorktreeInfo holds info about a polecat worktree to clean up.
type convoyWorktreeInfo struct {
	rigName     string // e.g., "gastown"
	polecatName string // e.g., "rictus"
	townRoot    string // workspace root
}

// findConvoyWorktrees discovers polecat worktrees associated with a convoy's tracked issues.
// It matches tracked issue assignees to polecat worktrees across all rigs.
func findConvoyWorktrees(tracked []trackedIssueInfo) []convoyWorktreeInfo {
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return nil
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil
	}

	// Collect all assignees from tracked issues
	assignees := make(map[string]bool)
	for _, t := range tracked {
		if t.Assignee != "" {
			assignees[t.Assignee] = true
		}
	}

	if len(assignees) == 0 {
		return nil
	}

	var worktrees []convoyWorktreeInfo

	for rigName := range rigsConfig.Rigs {
		rigPath := filepath.Join(townRoot, rigName)
		polecatsDir := filepath.Join(rigPath, "polecats")

		entries, err := os.ReadDir(polecatsDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}

			// Check if this polecat's assignee matches any tracked issue assignee
			// Assignees have format: rig/polecats/name
			polecatAssignee := fmt.Sprintf("%s/polecats/%s", rigName, entry.Name())
			if assignees[polecatAssignee] {
				worktrees = append(worktrees, convoyWorktreeInfo{
					rigName:     rigName,
					polecatName: entry.Name(),
					townRoot:    townRoot,
				})
			}
		}
	}

	return worktrees
}

// removePolecatWorktree removes a polecat worktree via gt polecat remove.
func removePolecatWorktree(wt convoyWorktreeInfo) error {
	// gt polecat remove accepts rig/polecat format
	target := fmt.Sprintf("%s/%s", wt.rigName, wt.polecatName)
	cmd := exec.Command("gt", "polecat", "remove", target, "--force")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return err
	}
	return nil
}


package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
)

func runConvoyCheck(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// If a specific convoy ID is provided, check only that convoy
	if len(args) == 1 {
		convoyID := args[0]
		return checkSingleConvoy(townBeads, convoyID, convoyCheckDryRun)
	}

	// Check all open convoys
	closed, err := checkAndCloseCompletedConvoys(townBeads, convoyCheckDryRun)
	if err != nil {
		return err
	}

	if len(closed) == 0 {
		fmt.Println("No convoys ready to close.")
	} else {
		if convoyCheckDryRun {
			fmt.Printf("%s Would auto-close %d convoy(s):\n", style.Warning.Render("⚠"), len(closed))
		} else {
			fmt.Printf("%s Auto-closed %d convoy(s):\n", style.Bold.Render("✓"), len(closed))
		}
		for _, c := range closed {
			fmt.Printf("  🚚 %s: %s\n", c.ID, c.Title)
		}
	}

	return nil
}

// closeConvoyIfComplete checks whether all tracked issues in a convoy are resolved
// and closes the convoy if so. Returns (true, nil) if the convoy was closed or
// would be closed (dry-run), (false, nil) if not ready, or (false, err) on failure.
func closeConvoyIfComplete(townBeads, convoyID, title string, tracked []trackedIssueInfo, dryRun bool) (bool, error) {
	// If no tracked issues were resolved, skip auto-close. A 0/0 result means
	// cross-rig tracking resolution failed — not that all issues are done.
	// Treating 0/0 as "complete" caused false 🚚 Convoy landed notifications. (GH#3xxx)
	if len(tracked) == 0 {
		return false, nil
	}

	allClosed := true
	openCount := 0
	for _, t := range tracked {
		if t.Status != "closed" && t.Status != "tombstone" {
			allClosed = false
			openCount++
		}
	}

	if !allClosed {
		fmt.Printf("%s Convoy %s has %d open issue(s) remaining\n", style.Dim.Render("○"), convoyID, openCount)
		return false, nil
	}

	if dryRun {
		fmt.Printf("%s Would auto-close convoy 🚚 %s: %s\n", style.Warning.Render("⚠"), convoyID, title)
		return true, nil
	}

	reason := "All tracked issues completed"
	closeArgs := []string{"close", convoyID, "-r", reason}
	closeCmd := exec.Command("bd", closeArgs...)
	closeCmd.Dir = townBeads

	if err := closeCmd.Run(); err != nil {
		return false, fmt.Errorf("closing convoy: %w", err)
	}

	fmt.Printf("%s Auto-closed convoy 🚚 %s: %s\n", style.Bold.Render("✓"), convoyID, title)
	notifyConvoyCompletion(townBeads, convoyID, title)
	return true, nil
}

// checkSingleConvoy checks a specific convoy and closes it if all tracked issues are complete.
func checkSingleConvoy(townBeads, convoyID string, dryRun bool) error {
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

	// Check if convoy is already closed
	if normalizeConvoyStatus(convoy.Status) == convoyStatusClosed {
		fmt.Printf("%s Convoy %s is already closed\n", style.Dim.Render("○"), convoyID)
		return nil
	}

	// Get tracked issues
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return fmt.Errorf("checking convoy %s: %w", convoyID, err)
	}

	_, err = closeConvoyIfComplete(townBeads, convoyID, convoy.Title, tracked, dryRun)
	return err
}

// checkAndCloseCompletedConvoys finds open convoys where all tracked issues are closed
// and auto-closes them. Returns the list of convoys that were closed (or would be closed in dry-run mode).
// If dryRun is true, no changes are made and the function returns what would have been closed.
func checkAndCloseCompletedConvoys(townBeads string, dryRun bool) ([]struct{ ID, Title string }, error) {
	var closed []struct{ ID, Title string }

	// List all open convoys
	out, err := runBdJSON(townBeads, "list", "--type=convoy", "--status=open", "--json")
	if err != nil {
		return nil, fmt.Errorf("listing convoys: %w", err)
	}

	var convoys []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &convoys); err != nil {
		return nil, fmt.Errorf("parsing convoy list: %w", err)
	}

	// Check each convoy
	for _, convoy := range convoys {
		if err := ensureKnownConvoyStatus(convoy.Status); err != nil {
			style.PrintWarning("skipping convoy %s: invalid lifecycle state: %v", convoy.ID, err)
			continue
		}
		tracked, err := getTrackedIssues(townBeads, convoy.ID)
		if err != nil {
			style.PrintWarning("skipping convoy %s: %v", convoy.ID, err)
			continue
		}
		ready, err := closeConvoyIfComplete(townBeads, convoy.ID, convoy.Title, tracked, dryRun)
		if err != nil {
			style.PrintWarning("couldn't close convoy %s: %v", convoy.ID, err)
			continue
		}
		if ready {
			closed = append(closed, struct{ ID, Title string }{convoy.ID, convoy.Title})
		}
	}

	return closed, nil
}

// notifyConvoyCompletion sends notifications to owner and any notify addresses.
func notifyConvoyCompletion(townBeads, convoyID, title string) {
	// Get convoy description to find owner and notify addresses
	showArgs := []string{"show", convoyID, "--json"}
	showCmd := exec.Command("bd", showArgs...)
	showCmd.Dir = townBeads
	var stdout bytes.Buffer
	showCmd.Stdout = &stdout

	if err := showCmd.Run(); err != nil {
		return
	}

	var convoys []struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil || len(convoys) == 0 {
		return
	}

	// ZFC: Use typed accessor instead of parsing description text
	fields := beads.ParseConvoyFields(&beads.Issue{Description: convoys[0].Description})
	for _, addr := range fields.NotificationAddresses() {
		mailArgs := []string{"mail", "send", addr,
			"-s", fmt.Sprintf("🚚 Convoy landed: %s", title),
			"-m", fmt.Sprintf("Convoy %s has completed.\n\nAll tracked issues are now closed.", convoyID)}
		mailCmd := exec.Command("gt", mailArgs...)
		if err := mailCmd.Run(); err != nil {
			style.PrintWarning("could not notify %s: %v", addr, err)
		}
	}

	// Send nudge notifications to nudge watchers
	for _, addr := range fields.NudgeNotificationAddresses() {
		nudgeMsg := fmt.Sprintf("🚚 Convoy landed: %s — Convoy %s has completed. All tracked issues are now closed.", title, convoyID)
		nudgeCmd := exec.Command("gt", "nudge", addr, "-m", nudgeMsg)
		if err := nudgeCmd.Run(); err != nil {
			style.PrintWarning("could not nudge %s: %v", addr, err)
		}
	}

	// Push notification to active Mayor session if configured
	notifyMayorSession(townBeads, convoyID, title)
}

// notifyMayorSession pushes a convoy completion notification into the active
// Mayor session via nudge, if convoy.notify_on_complete is enabled.
func notifyMayorSession(townBeads, convoyID, title string) {
	settingsPath := config.TownSettingsPath(townBeads)
	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return
	}
	if settings.Convoy == nil || !settings.Convoy.NotifyOnComplete {
		return
	}

	nudgeMsg := fmt.Sprintf("🚚 Convoy landed: %s — Convoy %s has completed. All tracked issues are now closed.", title, convoyID)
	nudgeCmd := exec.Command("gt", "nudge", "mayor", "-m", nudgeMsg)
	if err := nudgeCmd.Run(); err != nil {
		style.PrintWarning("could not nudge Mayor session: %v", err)
	}
}


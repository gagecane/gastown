package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

func runConvoyCreate(cmd *cobra.Command, args []string) error {
	// Validate --merge flag if provided
	if convoyMerge != "" {
		switch convoyMerge {
		case "direct", "mr", "local":
			// Valid
		default:
			return fmt.Errorf("invalid --merge value %q: must be direct, mr, or local", convoyMerge)
		}
	}

	var name string
	var trackedIssues []string

	if convoyFromEpic != "" {
		// --from-epic mode: auto-discover children
		epicIssues, err := collectEpicChildren(convoyFromEpic)
		if err != nil {
			return err
		}
		trackedIssues = epicIssues

		// Use epic title as convoy name unless a name arg was provided
		if len(args) > 0 {
			name = args[0]
		} else {
			if epic, err := bdShow(convoyFromEpic); err == nil {
				name = epic.Title
			} else {
				name = fmt.Sprintf("From epic %s", convoyFromEpic)
			}
		}
	} else {
		// Standard mode: explicit issue list
		if len(args) == 0 {
			return fmt.Errorf("at least one argument is required\nUsage: gt convoy create <name> <issue-id> [issue-id...]\n       gt convoy create --from-epic <epic-id>")
		}
		name = args[0]
		trackedIssues = args[1:]

		// If first arg looks like an issue ID (has beads prefix), treat all args as issues
		// and auto-generate a name from the first issue's title
		if looksLikeIssueID(name) {
			trackedIssues = args
			if details := getIssueDetails(args[0]); details != nil && details.Title != "" {
				name = details.Title
			} else {
				name = fmt.Sprintf("Tracking %s", args[0])
			}
		}

		if len(trackedIssues) == 0 {
			return fmt.Errorf("at least one issue ID is required\nUsage: gt convoy create <name> <issue-id> [issue-id...]")
		}
	}

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// Resolve the actual .beads directory (follows redirects) before calling
	// EnsureCustomTypes/Statuses, which expect a .beads path, not a workspace root.
	resolvedBeads := beads.ResolveBeadsDir(townBeads)

	// Ensure custom types (including 'convoy') are registered in town beads.
	// This handles cases where install didn't complete or beads was initialized manually.
	if err := beads.EnsureCustomTypes(resolvedBeads); err != nil {
		return fmt.Errorf("ensuring custom types: %w", err)
	}

	// Ensure custom statuses (staged_ready, staged_warnings) are registered.
	if err := beads.EnsureCustomStatuses(resolvedBeads); err != nil {
		return fmt.Errorf("ensuring custom statuses: %w", err)
	}

	// Create convoy issue in town beads
	description := fmt.Sprintf("Convoy tracking %d issues", len(trackedIssues))

	// Default owner to creator identity if not specified
	owner := convoyOwner
	if owner == "" {
		owner = detectSender()
	}
	convoyFieldValues := &beads.ConvoyFields{
		Owner:      owner,
		Notify:     convoyNotify,
		Merge:      convoyMerge,
		Molecule:   convoyMolecule,
		BaseBranch: convoyBaseBranch,
	}
	description = beads.SetConvoyFields(&beads.Issue{Description: description}, convoyFieldValues)

	// Guard against flag-like convoy names (gt-e0kx5)
	if beads.IsFlagLikeTitle(name) {
		return fmt.Errorf("refusing to create convoy: name %q looks like a CLI flag", name)
	}

	// Generate convoy ID with cv- prefix
	convoyID := fmt.Sprintf("hq-cv-%s", generateShortID())

	createArgs := []string{
		"create",
		"--type=convoy",
		"--id=" + convoyID,
		"--title=" + name,
		"--description=" + description,
		"--json",
	}
	if convoyOwned {
		createArgs = append(createArgs, "--labels=gt:owned")
	}
	if beads.NeedsForceForID(convoyID) {
		createArgs = append(createArgs, "--force")
	}

	var stderr bytes.Buffer
	if err := BdCmd(createArgs...).
		WithAutoCommit().
		Dir(townBeads).
		Stderr(&stderr).
		Run(); err != nil {
		return fmt.Errorf("creating convoy: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	// Notify address is stored in description (line 166-168) and read from there

	// Add 'tracks' relations for each tracked issue
	trackedCount := 0
	for _, issueID := range trackedIssues {
		if err := addTrackingRelationFn(townBeads, convoyID, issueID); err != nil {
			style.PrintWarning("couldn't track %s: %s", issueID, err)
		} else {
			trackedCount++
		}
	}

	// Output
	fmt.Printf("%s Created convoy 🚚 %s\n\n", style.Bold.Render("✓"), convoyID)
	fmt.Printf("  Name:     %s\n", name)
	if convoyFromEpic != "" {
		fmt.Printf("  Epic:     %s\n", convoyFromEpic)
	}
	fmt.Printf("  Tracking: %d issues\n", trackedCount)
	if convoyFromEpic == "" && len(trackedIssues) > 0 {
		fmt.Printf("  Issues:   %s\n", strings.Join(trackedIssues, ", "))
	}
	if owner != "" {
		fmt.Printf("  Owner:    %s\n", owner)
	}
	if convoyNotify != "" {
		fmt.Printf("  Notify:   %s\n", convoyNotify)
	}
	if convoyMerge != "" {
		fmt.Printf("  Merge:    %s\n", convoyMerge)
	}
	if convoyMolecule != "" {
		fmt.Printf("  Molecule: %s\n", convoyMolecule)
	}
	if convoyBaseBranch != "" {
		fmt.Printf("  Base:     %s\n", convoyBaseBranch)
	}
	if convoyOwned {
		fmt.Printf("  Owned:    %s\n", style.Warning.Render("caller-managed lifecycle"))
	}

	if convoyOwned {
		fmt.Printf("\n  %s\n", style.Dim.Render("Owned convoy: caller manages lifecycle via gt convoy land"))
	} else {
		fmt.Printf("\n  %s\n", style.Dim.Render("Convoy auto-closes when all tracked issues complete"))
	}

	return nil
}

func runConvoyAdd(cmd *cobra.Command, args []string) error {
	convoyID := args[0]
	issuesToAdd := args[1:]

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// Validate convoy exists and get its status
	showOut, err := BdCmd("show", convoyID, "--json").
		Dir(townBeads).
		Stderr(io.Discard).
		Output()
	if err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	var convoys []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
		Type   string `json:"issue_type"`
	}
	if err := json.Unmarshal(showOut, &convoys); err != nil {
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

	// If convoy is closed, reopen it
	reopened := false
	if normalizeConvoyStatus(convoy.Status) == convoyStatusClosed {
		// closed→open is always valid; ensureKnownConvoyStatus above guarantees
		// the current status is known, so no additional transition check needed.
		if err := BdCmd("update", convoyID, "--status=open").
			Dir(townBeads).
			WithAutoCommit().
			Run(); err != nil {
			return fmt.Errorf("couldn't reopen convoy: %w", err)
		}
		reopened = true
		fmt.Printf("%s Reopened convoy %s\n", style.Bold.Render("↺"), convoyID)
	}

	// Add 'tracks' relations for each issue
	addedCount := 0
	for _, issueID := range issuesToAdd {
		if err := addTrackingRelationFn(townBeads, convoyID, issueID); err != nil {
			style.PrintWarning("couldn't add %s: %s", issueID, err)
		} else {
			addedCount++
		}
	}

	// Output
	if reopened {
		fmt.Println()
	}
	fmt.Printf("%s Added %d issue(s) to convoy 🚚 %s\n", style.Bold.Render("✓"), addedCount, convoyID)
	if addedCount > 0 {
		fmt.Printf("  Issues: %s\n", strings.Join(issuesToAdd[:addedCount], ", "))
	}

	return nil
}

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

func runMoleculeAttach(cmd *cobra.Command, args []string) error {
	var pinnedBeadID, moleculeID string

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	if len(args) == 2 {
		// Explicit: gt mol attach <pinned-bead-id> <molecule-id>
		pinnedBeadID = args[0]
		moleculeID = args[1]
	} else {
		// Auto-detect: gt mol attach <molecule-id>
		// Find the agent's pinned handoff bead (same pattern as mol burn/squash)
		moleculeID = args[0]

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting current directory: %w", err)
		}

		townRoot, err := workspace.FindFromCwd()
		if err != nil {
			return fmt.Errorf("finding workspace: %w", err)
		}
		if townRoot == "" {
			return fmt.Errorf("not in a Gas Town workspace")
		}

		roleInfo, err := GetRoleWithContext(cwd, townRoot)
		if err != nil {
			return fmt.Errorf("detecting role: %w", err)
		}
		roleCtx := RoleContext{
			Role:     roleInfo.Role,
			Rig:      roleInfo.Rig,
			Polecat:  roleInfo.Polecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
		}
		target := buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
		}

		role := extractRoleFromIdentity(target)

		handoff, err := b.FindHandoffBead(role)
		if err != nil {
			return fmt.Errorf("finding handoff bead: %w", err)
		}
		if handoff == nil {
			return fmt.Errorf("no handoff bead found for %s (looked for %q with pinned status)", target, beads.HandoffBeadTitle(role))
		}
		pinnedBeadID = handoff.ID
	}

	// Attach the molecule
	issue, err := b.AttachMolecule(pinnedBeadID, moleculeID)
	if err != nil {
		return fmt.Errorf("attaching molecule: %w", err)
	}

	attachment := beads.ParseAttachmentFields(issue)
	fmt.Printf("%s Attached %s to %s\n", style.Bold.Render("✓"), moleculeID, pinnedBeadID)
	if attachment != nil && attachment.AttachedAt != "" {
		fmt.Printf("  attached_at: %s\n", attachment.AttachedAt)
	}

	return nil
}

func runMoleculeDetach(cmd *cobra.Command, args []string) error {
	pinnedBeadID := args[0]

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	// Locate the pinned bead in its actual home DB.
	//
	// We can't rely solely on prefix routing (routes.jsonl): legacy pinned
	// beads can have a prefix that routes to a different rig than where the
	// bead actually lives. For example, a "gt-*" pinned bead in
	// casc_constructs/mayor/rig/.beads predates the town-level "gt-" prefix
	// mapping and still lives in the rig's DB even though routing would send
	// lookups to the town DB. Without this fallback, `gt mol detach` returns
	// "issue not found" for all such stale pins, forcing users to `bd close
	// --force` and leaving the attached_molecule metadata behind. (gu-vkg3)
	b, found, err := resolveDetachTarget(workDir, pinnedBeadID)
	if err != nil {
		return fmt.Errorf("checking attachment: %w", err)
	}
	if !found {
		return fmt.Errorf("checking attachment: %w", beads.ErrNotFound)
	}

	// Check current attachment.
	// DetachMoleculeWithAuditLocal only reads/writes the pinned bead's
	// description, so it naturally tolerates dead molecule references — the
	// attached_molecule ID is never dereferenced, just cleared. (gu-vkg3)
	attachment, err := b.GetAttachmentLocal(pinnedBeadID)
	if err != nil {
		return fmt.Errorf("checking attachment: %w", err)
	}

	if attachment == nil {
		fmt.Printf("%s No molecule attached to %s\n", style.Dim.Render("ℹ"), pinnedBeadID)
		return nil
	}

	previousMolecule := attachment.AttachedMolecule

	// Detach the molecule with audit logging, using the bead's actual home
	// DB rather than the prefix-routed DB (gu-vkg3).
	_, err = b.DetachMoleculeWithAuditLocal(pinnedBeadID, beads.DetachOptions{
		Operation: "detach",
		Agent:     detectCurrentAgent(),
	})
	if err != nil {
		return fmt.Errorf("detaching molecule: %w", err)
	}

	fmt.Printf("%s Detached %s from %s\n", style.Bold.Render("✓"), previousMolecule, pinnedBeadID)

	return nil
}

// resolveDetachTarget returns a Beads wrapper bound to the DB that actually
// holds the given pinned bead. It tries, in order:
//
//  1. Prefix-based routing via routes.jsonl (the common case).
//  2. A fallback scan of all .beads directories under the town root
//     (catches legacy/misclassified pins whose prefix routes elsewhere).
//
// The returned Beads wrapper is configured to use ShowLocal-family methods
// so that callers can operate on the bead without triggering another round
// of routing. The found bool is false when the bead cannot be located in
// any rig's DB; err is reserved for IO / unexpected errors, not "not found".
func resolveDetachTarget(workDir, pinnedBeadID string) (*beads.Beads, bool, error) {
	// Step 1: try prefix routing. beads.ForIssueID returns a wrapper bound
	// to the routed rig's beads dir when the prefix matches a route.
	routed := beads.New(workDir).ForIssueID(pinnedBeadID)
	if _, err := routed.ShowLocal(pinnedBeadID); err == nil {
		return routed, true, nil
	} else if !errors.Is(err, beads.ErrNotFound) && !strings.Contains(err.Error(), "not found") {
		// Non-404 errors (IO, subprocess failures) surface to the caller.
		return nil, false, err
	}

	// Step 2: scan every .beads directory under the town root. This is
	// bounded by the number of rigs in the town (typically < 50) and only
	// runs when routing missed, so the cost is acceptable.
	townRoot := beads.FindTownRoot(workDir)
	if townRoot == "" {
		return nil, false, nil
	}
	for _, rigBeadsDir := range discoverRigBeadsDirs(townRoot) {
		rigDir := filepath.Dir(rigBeadsDir)
		b := beads.NewWithBeadsDir(rigDir, rigBeadsDir)
		if _, err := b.ShowLocal(pinnedBeadID); err == nil {
			return b, true, nil
		} else if !errors.Is(err, beads.ErrNotFound) && !strings.Contains(err.Error(), "not found") {
			// Skip rigs with IO errors; don't fail the whole scan on one flaky DB.
			fmt.Fprintf(os.Stderr, "Warning: error probing %s for %s: %v\n", rigBeadsDir, pinnedBeadID, err)
			continue
		}
	}

	return nil, false, nil
}

// discoverRigBeadsDirs returns every .beads directory under townRoot at
// depth ≤ 2, including the town's own .beads. Mirrors the discovery logic
// used by the doctor's hook-attachment-valid check so both code paths see
// the same set of candidate DBs.
func discoverRigBeadsDirs(townRoot string) []string {
	var dirs []string

	// Town-level first.
	townBeads := filepath.Join(townRoot, ".beads")
	if info, err := os.Stat(townBeads); err == nil && info.IsDir() {
		dirs = append(dirs, townBeads)
	}

	// One level deep: <townRoot>/<rig>/.beads
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return dirs
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(townRoot, entry.Name(), ".beads")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			dirs = append(dirs, candidate)
		}
	}
	return dirs
}

func runMoleculeAttachment(cmd *cobra.Command, args []string) error {
	pinnedBeadID := args[0]

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Get the issue
	issue, err := b.Show(pinnedBeadID)
	if err != nil {
		return fmt.Errorf("getting issue: %w", err)
	}

	attachment := beads.ParseAttachmentFields(issue)

	if moleculeJSON {
		type attachmentOutput struct {
			IssueID          string `json:"issue_id"`
			IssueTitle       string `json:"issue_title"`
			Status           string `json:"status"`
			AttachedMolecule string `json:"attached_molecule,omitempty"`
			AttachedAt       string `json:"attached_at,omitempty"`
		}
		out := attachmentOutput{
			IssueID:    issue.ID,
			IssueTitle: issue.Title,
			Status:     issue.Status,
		}
		if attachment != nil {
			out.AttachedMolecule = attachment.AttachedMolecule
			out.AttachedAt = attachment.AttachedAt
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human-readable output
	fmt.Printf("\n%s: %s\n", style.Bold.Render(issue.ID), issue.Title)
	fmt.Printf("Status: %s\n", issue.Status)

	if attachment == nil || attachment.AttachedMolecule == "" {
		fmt.Printf("\n%s\n", style.Dim.Render("No molecule attached"))
	} else {
		fmt.Printf("\n%s\n", style.Bold.Render("Attached Molecule:"))
		fmt.Printf("  ID: %s\n", attachment.AttachedMolecule)
		if attachment.AttachedAt != "" {
			fmt.Printf("  Attached at: %s\n", attachment.AttachedAt)
		}
	}

	return nil
}

// detectCurrentAgent returns the current agent identity based on GT_ROLE or working directory.
// Returns empty string if identity cannot be determined.
func detectCurrentAgent() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return ""
	}

	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return ""
	}
	ctx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}
	return buildAgentIdentity(ctx)
}

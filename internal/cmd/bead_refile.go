package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	beadRefileToRig  string
	beadRefileDryRun bool
)

var beadRefileCmd = &cobra.Command{
	Use:   "refile <bead-id> --to-rig <rig>",
	Short: "Refile a bead into another rig's database, preserving history",
	Long: `Refile a bead from its current rig database into another rig's database
while preserving comments, labels, description, priority, type, metadata,
defer date, and timestamps.

Unlike 'gt bead move' (which recreates the bead via 'bd create' and loses
comments, dependencies, and other history), 'refile' round-trips the bead
through 'bd export'/'bd import' so the full record survives the move. The
refiled bead is minted with the target rig's prefix, and the source bead is
closed as a redirect tombstone pointing at the new ID.

Cross-database dependencies cannot be represented in the target database, so
any dependency links on the source bead are reported and must be re-linked by
hand if their targets are also refiled.

The target rig is resolved by NAME (from routes.jsonl / rigs.json), so this
works from anywhere in town. The special names "hq" and "town" target the
town-level database.

Examples:
  gt bead refile gc-u321tu --to-rig gastown_upstream   # hq bead -> gastown rig
  gt bead refile gu-abc123 --to-rig talon              # gastown bead -> talon rig
  gt bead refile gu-abc123 --to-rig talon --dry-run    # preview only`,
	Args: cobra.ExactArgs(1),
	RunE: runBeadRefile,
}

func init() {
	beadRefileCmd.Flags().StringVar(&beadRefileToRig, "to-rig", "", "Target rig name (required)")
	beadRefileCmd.Flags().BoolVarP(&beadRefileDryRun, "dry-run", "n", false, "Show what would be done without making changes")
	_ = beadRefileCmd.MarkFlagRequired("to-rig")
	beadCmd.AddCommand(beadRefileCmd)
}

// refileRecord is the subset of an exported bead record that the refile
// transform inspects directly. All other fields pass through verbatim via the
// raw JSON object, so export-only fields (metadata, defer_until, design,
// notes, timestamps, ...) are preserved without being enumerated here.
type refileRecord struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// transformRefileJSONL takes one exported bead JSONL record and rewrites it so
// 'bd import' will mint a fresh bead in the target database while preserving the
// full record. It returns the transformed JSONL line, the parsed source record,
// and the IDs of any dependency links that were dropped (cross-database
// dependencies cannot be represented in the target DB).
//
// Rewrites applied:
//   - "id" is removed so the importer mints a fresh target-prefix ID.
//   - "dependencies" is removed (cross-DB deps cannot resolve) and reported.
//   - each comment's "id" and "issue_id" are removed so comments relink to the
//     newly minted bead without colliding on their original UUID primary keys.
func transformRefileJSONL(line []byte) (out []byte, rec refileRecord, droppedDeps []string, err error) {
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, rec, nil, fmt.Errorf("parsing exported bead record: %w", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return nil, rec, nil, fmt.Errorf("parsing exported bead record: %w", err)
	}

	delete(obj, "id")

	if rawDeps, ok := obj["dependencies"]; ok {
		droppedDeps = extractDepTargets(rawDeps)
		delete(obj, "dependencies")
	}

	if rawComments, ok := obj["comments"].([]any); ok {
		for _, c := range rawComments {
			if comment, ok := c.(map[string]any); ok {
				delete(comment, "id")
				delete(comment, "issue_id")
			}
		}
	}

	out, err = json.Marshal(obj)
	if err != nil {
		return nil, rec, nil, fmt.Errorf("re-encoding bead record: %w", err)
	}
	return out, rec, droppedDeps, nil
}

// extractDepTargets pulls the depends_on_id values from an exported
// "dependencies" array for reporting which links were dropped.
func extractDepTargets(rawDeps any) []string {
	deps, ok := rawDeps.([]any)
	if !ok {
		return nil
	}
	var targets []string
	for _, d := range deps {
		dep, ok := d.(map[string]any)
		if !ok {
			continue
		}
		if target, ok := dep["depends_on_id"].(string); ok && target != "" {
			targets = append(targets, target)
		}
	}
	return targets
}

// exportBeadLine exports the source bead from its database and returns the
// single JSONL record matching sourceID. 'bd export' ignores positional ID
// filters and dumps every record, so we filter by ID here.
func exportBeadLine(sourceDir, sourceID string) ([]byte, error) {
	out, err := BdCmd("export", "--all").
		Dir(sourceDir).
		StripBeadsDir().
		Output()
	if err != nil {
		return nil, fmt.Errorf("exporting from source database: %w", err)
	}

	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec refileRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.ID == sourceID {
			return line, nil
		}
	}
	return nil, fmt.Errorf("bead %s not found in source database", sourceID)
}

func runBeadRefile(cmd *cobra.Command, args []string) error {
	sourceID := args[0]
	targetRig := strings.TrimSpace(beadRefileToRig)
	if targetRig == "" {
		return fmt.Errorf("--to-rig is required")
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	targetBeadsDir, ok := beads.ResolveRepoAliasBeadsDir(townRoot, targetRig)
	if !ok {
		return fmt.Errorf("cannot resolve beads database for rig %q "+
			"(unknown rig or missing .beads workspace); known rigs come from routes.jsonl", targetRig)
	}

	sourceDir := resolveBeadDir(sourceID)

	line, err := exportBeadLine(sourceDir, sourceID)
	if err != nil {
		return err
	}

	transformed, rec, droppedDeps, err := transformRefileJSONL(line)
	if err != nil {
		return err
	}

	if rec.Status == "closed" {
		return fmt.Errorf("cannot refile closed bead %s", sourceID)
	}
	if beads.IsFlagLikeTitle(rec.Title) {
		return fmt.Errorf("refusing to refile bead: title %q looks like a CLI flag", rec.Title)
	}

	fmt.Printf("%s Refiling %s to rig %s...\n", style.Bold.Render("→"), sourceID, targetRig)
	fmt.Printf("  Title: %s\n", rec.Title)
	if len(droppedDeps) > 0 {
		fmt.Printf("  %s %d cross-database dependency link(s) will be dropped: %s\n",
			style.Bold.Render("!"), len(droppedDeps), strings.Join(droppedDeps, ", "))
	}

	if beadRefileDryRun {
		fmt.Printf("\nDry run - would:\n")
		fmt.Printf("  1. Import the full bead record into rig %s (new ID minted with that rig's prefix)\n", targetRig)
		fmt.Printf("  2. Close %s as a redirect tombstone pointing at the new ID\n", sourceID)
		return nil
	}

	newID, err := importBeadRecord(targetBeadsDir, transformed)
	if err != nil {
		return err
	}
	fmt.Printf("%s Refiled to %s in rig %s\n", style.Bold.Render("✓"), newID, targetRig)

	// Add a provenance comment on the new bead pointing back at the source.
	provenance := fmt.Sprintf("Refiled from %s (rig database move).", sourceID)
	if commentErr := BdCmd("comment", newID, provenance).
		WithBeadsDir(targetBeadsDir).
		WithAutoCommit().
		Run(); commentErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not add provenance comment to %s: %v\n", newID, commentErr)
	}

	// Close the source bead as a redirect tombstone.
	closeReason := fmt.Sprintf("Refiled to %s in rig %s", newID, targetRig)
	if closeErr := BdCmd("close", sourceID, "--reason", closeReason).
		Dir(sourceDir).
		StripBeadsDir().
		WithAutoCommit().
		Run(); closeErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: refiled to %s but failed to close source %s: %v\n", newID, sourceID, closeErr)
		fmt.Fprintf(os.Stderr, "Source bead %s remains open - close it manually to complete the refile.\n", sourceID)
		return closeErr
	}

	fmt.Printf("%s Closed %s (refiled to %s)\n", style.Bold.Render("✓"), sourceID, newID)
	if len(droppedDeps) > 0 {
		fmt.Printf("\nNote: re-link these dependencies by hand if their targets are also refiled: %s\n",
			strings.Join(droppedDeps, ", "))
	}
	fmt.Printf("\nBead refiled: %s → %s\n", sourceID, newID)
	return nil
}

// importBeadRecord imports a single transformed JSONL record into the target
// database via stdin and returns the newly minted bead ID.
func importBeadRecord(targetBeadsDir string, record []byte) (string, error) {
	importCmd := BdCmd("import", "-", "--json").
		WithBeadsDir(targetBeadsDir).
		WithAutoCommit().
		Build()
	importCmd.Stdin = bytes.NewReader(record)

	var stdout bytes.Buffer
	importCmd.Stdout = &stdout
	if err := importCmd.Run(); err != nil {
		return "", fmt.Errorf("importing into target database: %w", err)
	}

	var result struct {
		Created int      `json:"created"`
		IDs     []string `json:"ids"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return "", fmt.Errorf("parsing import result: %w\noutput: %s", err, stdout.String())
	}
	if result.Created == 0 || len(result.IDs) == 0 {
		return "", fmt.Errorf("import did not create a bead: %s", stdout.String())
	}
	return result.IDs[0], nil
}

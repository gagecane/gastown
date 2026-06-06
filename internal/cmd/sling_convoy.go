package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	rigpkg "github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/sling"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/workspace"
)

// slingGenerateShortID generates a short random ID (5 lowercase chars).
func slingGenerateShortID() string {
	return sling.GenerateShortID()
}

// isTrackedByConvoy checks if an issue is already being tracked by a convoy.
// Returns the convoy ID if tracked, empty string otherwise.
//
// Uses bdDepListRawIDs for cross-database dep resolution (GH #2624).
// For direction=up queries, the raw SQL approach queries the same table but
// looks for rows where depends_on_id matches the beadID, returning the
// issue_id (which is the convoy). Since this only returns IDs (no issue_type
// or status), we verify each candidate via bd show.
func isTrackedByConvoy(beadID string) string {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return ""
	}
	townBeads := filepath.Join(townRoot, ".beads")

	// Primary: Use raw dep query to find what tracks this issue (direction=up).
	// This returns convoy IDs that have a "tracks" dep on beadID.
	trackerIDs, err := bdDepListRawIDs(townBeads, beadID, "up", "tracks")
	if err == nil && len(trackerIDs) > 0 {
		// Check each tracker to find an open convoy
		for _, trackerID := range trackerIDs {
			result, err := bdShow(trackerID)
			if err != nil {
				continue
			}
			if isConvoyIssue(result.IssueType, result.Labels) && result.Status == "open" {
				return trackerID
			}
		}
	}

	// Fallback: Query convoys directly by description pattern
	// This is more robust when cross-rig routing has issues (G19, G21)
	// Auto-convoys have description "Auto-created convoy tracking <beadID>"
	return findConvoyByDescription(townRoot, beadID)
}

// findConvoyByDescription searches open convoys for one tracking the given beadID.
// Checks both convoy descriptions (for auto-created convoys) and tracked deps
// (for manually-created convoys where the description won't match).
// Returns convoy ID if found, empty string otherwise.
func findConvoyByDescription(townRoot, beadID string) string {
	townBeads := filepath.Join(townRoot, ".beads")

	convoys, err := listConvoyIssues(townBeads, "open", false)
	if err != nil {
		return ""
	}

	// Check if any convoy's description mentions tracking this beadID
	// (matches auto-created convoys with "Auto-created convoy tracking <beadID>")
	trackingPattern := fmt.Sprintf("tracking %s", beadID)
	for _, convoy := range convoys {
		if strings.Contains(convoy.Description, trackingPattern) {
			return convoy.ID
		}
	}

	// Check tracked deps of each convoy (for manually-created convoys).
	// This handles the case where cross-rig dep resolution (direction=up) fails
	// but the convoy does have a tracks dependency on the bead.
	for _, convoy := range convoys {
		if convoyTracksBead(townBeads, convoy.ID, beadID) {
			return convoy.ID
		}
	}

	return ""
}

// convoyTracksBead checks if a convoy has a tracks dependency on the given beadID.
// Uses bdDepListRawIDs for cross-database dep resolution (GH #2624).
func convoyTracksBead(beadsDir, convoyID, beadID string) bool {
	trackedIDs, err := bdDepListRawIDs(beadsDir, convoyID, "down", "tracks")
	if err != nil {
		return false
	}

	for _, id := range trackedIDs {
		if id == beadID {
			return true
		}
	}
	return false
}

// ConvoyInfo holds convoy details for an issue's tracking convoy. The domain
// type and its IsOwnedDirect predicate live in internal/sling; this alias keeps
// the cmd-package call surface stable (gu-yju86 increment 2).
type ConvoyInfo = sling.ConvoyInfo

// getConvoyInfoForIssue checks if an issue is tracked by a convoy and returns its info.
// Returns nil if not tracked by any convoy.
func getConvoyInfoForIssue(issueID string) *ConvoyInfo {
	convoyID := isTrackedByConvoy(issueID)
	if convoyID == "" {
		return nil
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return nil
	}
	townBeads := filepath.Join(townRoot, ".beads")

	var stderr bytes.Buffer
	stdout, err := BdCmd("show", convoyID, "--json").
		AllowStale().
		Dir(townRoot).
		WithBeadsDir(townBeads).
		Stderr(&stderr).
		Output()

	if err != nil {
		// Check if this is a "not found" error (phantom convoy) vs transient error.
		// Phantom convoys occur when a convoy bead is deleted from HQ but tracking
		// deps still exist in local beads DB (gt-9xum2). Return nil to treat as
		// untracked, allowing normal MR flow to proceed.
		stderrStr := stderr.String()
		if strings.Contains(stderrStr, "not found") ||
			strings.Contains(stderrStr, "Issue not found") ||
			strings.Contains(stderrStr, "no issue found") {
			return nil // Phantom convoy - proceed without convoy context
		}
		// Other error (transient) - return basic info as fallback
		return &ConvoyInfo{ID: convoyID}
	}

	var convoys []struct {
		Labels      []string `json:"labels"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(stdout, &convoys); err != nil || len(convoys) == 0 {
		return &ConvoyInfo{ID: convoyID}
	}

	info := &ConvoyInfo{ID: convoyID}

	// Check for gt:owned label
	for _, label := range convoys[0].Labels {
		if label == "gt:owned" {
			info.Owned = true
			break
		}
	}

	// Parse merge strategy + relay base branch from description (typed accessor).
	info.MergeStrategy = convoyMergeFromFields(convoys[0].Description)
	info.BaseBranch = convoyBaseFromFields(convoys[0].Description)

	return info
}

// convoyBaseFromFields extracts the relay/base branch from a convoy description
// using the typed ConvoyFields accessor. Returns "" if unset. Mirrors
// convoyMergeFromFields (gs-9ct #1). Delegates to the sling domain API.
func convoyBaseFromFields(description string) string {
	return sling.BaseFromFields(description)
}

// effectiveBaseBranch resolves the base branch a polecat should cut from when
// dispatching beadID. An explicit --base-branch always wins; otherwise the
// bead's tracking convoy's base branch is inherited so a relay leg's worktree
// is cut from the named branch (proto/v3-build) rather than the rig default
// (gs-9ct #1: "worktree had nothing to edit" when the named-branch work was
// absent from the rig default). Returns explicit unchanged when there is no
// convoy base to inherit, so non-relay slings are unaffected.
func effectiveBaseBranch(beadID, explicit string) string {
	if explicit != "" || beadID == "" {
		return explicit
	}
	townRoot, rootErr := workspace.FindFromCwd()
	// Primary: the base branch the bead records in its OWN attachment fields,
	// stamped at the first dispatch (sling_dispatch.go). This is the reliable
	// source on RE-dispatch — the DEFERRED / convoy-feed re-sling path (gs-o5f
	// family) — where the cross-rig dep resolution getConvoyInfoForIssue depends
	// on can silently return "" and drop the bead onto the rig default base
	// instead of its relay base (gs-n6h). Mirrors the primary/fallback convoy
	// lookup order gt done already uses (done.go, gt-7b6wf).
	if rootErr == nil {
		if bb := beadStampedBaseBranch(beadID, townRoot); bb != "" {
			return bb
		}
	}
	// Fallback: the tracking convoy's named base (first dispatch, before the bead
	// carries its own base_branch).
	if info := getConvoyInfoForIssue(beadID); info != nil && info.BaseBranch != "" {
		return info.BaseBranch
	}
	// Inherited: a relay-epic SLICE is created with base_branch=<rig default>
	// (epic-slicing / mol-idea-to-plan stamps the rig default, not the epic's
	// relay base — gs-w7k), and on its FIRST auto-dispatch it is not yet tracked
	// by the relay convoy, so neither its own stamped base nor its own convoy
	// yields the relay base. gs-n6h only recovered the relay base on RE-dispatch
	// (once the bead carries it). Walk up to the parent epic and inherit ITS
	// relay base so the very FIRST dispatch cuts from the named branch instead of
	// misrouting onto the rig default. No-op for non-relay beads (no ancestor
	// carries a relay base).
	if rootErr == nil {
		if bb := resolveRelayBaseFromAncestors(beadID, maxRelayInheritHops, func(id string) (string, string) {
			return beadParentAndRelayBase(id, townRoot)
		}); bb != "" {
			return bb
		}
	}
	return explicit
}

// maxRelayInheritHops bounds the parent-epic walk in resolveRelayBaseFromAncestors
// so a malformed parent cycle can never spin the dispatcher. Relay slices sit one
// level under their epic; a handful of hops covers nested epics with margin.
const maxRelayInheritHops = 8

// resolveRelayBaseFromAncestors walks beadID's parent chain and returns the first
// ancestor's relay base, or "" when no ancestor carries one. lookup(id) returns
// (parent, relayBase) for a bead; relayBase is "" when the bead records only the
// rig default and has no relay convoy. The walk is bounded by maxHops and skips
// already-seen IDs so a cycle terminates. Pure (lookup is injected) for testing.
func resolveRelayBaseFromAncestors(beadID string, maxHops int, lookup func(id string) (parent, relayBase string)) string {
	type rec struct{ parent, base string }
	cache := map[string]rec{}
	get := func(id string) rec {
		if r, ok := cache[id]; ok {
			return r
		}
		p, b := lookup(id)
		r := rec{parent: p, base: b}
		cache[id] = r
		return r
	}
	seen := map[string]bool{beadID: true}
	cur := beadID
	for hops := 0; hops < maxHops; hops++ {
		parent := get(cur).parent
		if parent == "" || seen[parent] {
			return ""
		}
		seen[parent] = true
		if base := get(parent).base; base != "" {
			return base
		}
		cur = parent
	}
	return ""
}

// beadParentAndRelayBase reads beadID and returns its parent ID and the relay base
// it contributes to inheritance: its own stamped base_branch when that differs
// from the rig default, else its tracking convoy's named base, else "". Mirrors
// the stamped/convoy resolution effectiveBaseBranch applies to the dispatched bead
// itself, so an ancestor's relay base is recovered however it was recorded.
func beadParentAndRelayBase(beadID, townRoot string) (parent string, relayBase string) {
	beadsDir := beads.ResolveBeadsDirForID(filepath.Join(townRoot, ".beads"), beadID)
	bd := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
	issue, err := bd.Show(beadID)
	if err != nil || issue == nil {
		return "", ""
	}
	parent = issue.Parent
	if af := beads.ParseAttachmentFields(issue); af != nil {
		if bb := extractFormulaVar(af.FormulaVars, "base_branch"); bb != "" && bb != rigDefaultBranchForBead(townRoot, beadID) {
			return parent, bb
		}
	}
	if info := getConvoyInfoForIssue(beadID); info != nil && info.BaseBranch != "" {
		relayBase = info.BaseBranch
	}
	return parent, relayBase
}

// beadStampedBaseBranch returns the relay base branch a bead records in its own
// attachment formula_vars (base_branch=...), or "" when absent or equal to the
// rig default. Every bead carries base_branch=<rig default> from the formula's
// default var, so only a value that DIFFERS is a genuine relay base worth
// honoring — the same predicate gt done / gt mq submit use for target detection
// (bb != defaultBranch). The base is stamped at dispatch time, so this recovers
// the relay base directly from the bead on re-dispatch without a convoy lookup.
func beadStampedBaseBranch(beadID, townRoot string) string {
	// Resolve the bead's OWN rig database from its ID prefix so the read routes
	// to where the bead lives (a rig DB), not the town-level DB. Mirrors the
	// routed-read pattern used elsewhere for cross-rig bead lookups.
	beadsDir := beads.ResolveBeadsDirForID(filepath.Join(townRoot, ".beads"), beadID)
	bd := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
	issue, err := bd.Show(beadID)
	if err != nil || issue == nil {
		return ""
	}
	af := beads.ParseAttachmentFields(issue)
	if af == nil {
		return ""
	}
	bb := extractFormulaVar(af.FormulaVars, "base_branch")
	if bb == "" || bb == rigDefaultBranchForBead(townRoot, beadID) {
		return ""
	}
	return bb
}

// rigDefaultBranchForBead resolves the default branch of the rig that owns
// beadID (by ID prefix), falling back to "main" when the rig or its config
// cannot be resolved. Mirrors the defaultBranch resolution in gt mq submit.
func rigDefaultBranchForBead(townRoot, beadID string) string {
	prefix := beads.ExtractPrefix(beadID)
	if prefix == "" {
		return "main"
	}
	rigName := beads.GetRigNameForPrefix(townRoot, prefix)
	if rigName == "" {
		return "main"
	}
	if cfg, err := rigpkg.LoadRigConfig(filepath.Join(townRoot, rigName)); err == nil && cfg != nil && cfg.DefaultBranch != "" {
		return cfg.DefaultBranch
	}
	return "main"
}

// getConvoyInfoFromIssue reads convoy info directly from the issue's attachment fields.
// This is the primary lookup method (gt-7b6wf fix): gt sling stores convoy_id and
// merge_strategy on the issue when dispatching, avoiding unreliable cross-rig dep
// resolution. Returns nil if the issue has no convoy fields in its description.
func getConvoyInfoFromIssue(issueID, cwd string) *ConvoyInfo {
	if issueID == "" {
		return nil
	}

	bd := beads.New(beads.ResolveBeadsDir(cwd))
	issue, err := bd.Show(issueID)
	if err != nil {
		return nil
	}

	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil || attachment.ConvoyID == "" {
		return nil
	}

	return &ConvoyInfo{
		ID:            attachment.ConvoyID,
		MergeStrategy: attachment.MergeStrategy,
		Owned:         attachment.ConvoyOwned,
		// gs-d26: surface the relay base branch from the bead's formula_vars so
		// `gt done` can FF-push a merge=local relay leg to its named base branch.
		// AttachmentFields has no dedicated BaseBranch field; sling stamps it as a
		// base_branch=<value> entry inside FormulaVars.
		BaseBranch: extractFormulaVar(attachment.FormulaVars, "base_branch"),
	}
}

// printConvoyConflict prints detailed information about a bead that is already
// tracked by another convoy, including all beads in that convoy with their
// statuses, and recommended actions the user can take.
func printConvoyConflict(beadID, convoyID string) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		fmt.Printf("\n  %s is already tracked by convoy %s\n", beadID, convoyID)
		return
	}
	townBeads := filepath.Join(townRoot, ".beads")

	var convoyTitle string
	showOut, err := BdCmd("show", convoyID, "--json").
		AllowStale().
		Dir(townRoot).
		WithBeadsDir(townBeads).
		Stderr(io.Discard).
		Output()
	if err == nil {
		var items []struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(showOut, &items) == nil && len(items) > 0 {
			convoyTitle = items[0].Title
		}
	}

	fmt.Printf("\n  Conflict: %s is already tracked by convoy %s", beadID, convoyID)
	if convoyTitle != "" {
		fmt.Printf(" (%s)", convoyTitle)
	}
	fmt.Println()

	// Get all beads in the conflicting convoy
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err == nil && len(tracked) > 0 {
		fmt.Printf("\n  Beads in convoy %s:\n", convoyID)
		for _, t := range tracked {
			marker := " "
			if t.ID == beadID {
				marker = "→"
			}
			statusIcon := "○"
			switch t.Status {
			case "open":
				statusIcon = "●"
			case "closed":
				statusIcon = "✓"
			case "hooked", "pinned":
				statusIcon = "◆"
			}
			title := t.Title
			if title == "" {
				title = "(no title)"
			}
			suffix := ""
			if t.ID == beadID {
				suffix = "  ← conflict"
			}
			fmt.Printf("    %s %s %s  %s [%s]%s\n", marker, statusIcon, t.ID, title, t.Status, suffix)
		}
	}

	fmt.Printf("\n  Options:\n")
	fmt.Printf("    1. Remove the bead from this batch:\n")
	fmt.Printf("         gt sling <other-beads...> <rig>   (without %s)\n", beadID)
	fmt.Printf("    2. Move the bead to the new batch (remove from existing convoy first):\n")
	fmt.Printf("         bd dep remove %s %s --type=tracks\n", convoyID, beadID)
	fmt.Printf("         gt sling <all-beads...> <rig>\n")
	fmt.Printf("    3. Close the existing convoy and re-sling all beads together:\n")
	fmt.Printf("         gt convoy close %s --reason \"re-batching\"\n", convoyID)
	fmt.Printf("         gt sling <all-beads...> <rig>\n")
	fmt.Printf("    4. Add the other beads to the existing convoy instead:\n")
	fmt.Printf("         gt convoy add %s <other-beads...>\n", convoyID)
	fmt.Println()
}

// createBatchConvoy creates a single auto-convoy that tracks all beads in a batch sling.
// Returns the convoy ID and the list of bead IDs that were successfully tracked.
// Callers should only stamp ConvoyID on beads in the tracked set — a bead whose
// dep add failed should not reference a convoy that has no knowledge of it.
// If owned is true, the convoy is marked with gt:owned label.
// beadIDs must be non-empty. The convoy title uses the rig name and bead count.
func createBatchConvoy(beadIDs []string, rigName string, owned bool, mergeStrategy, baseBranch string) (string, []string, error) {
	if len(beadIDs) == 0 {
		return "", nil, fmt.Errorf("no beads to track")
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return "", nil, fmt.Errorf("finding town root: %w", err)
	}

	townBeads := filepath.Join(townRoot, ".beads")

	convoyID := fmt.Sprintf("hq-cv-%s", slingGenerateShortID())

	convoyTitle := fmt.Sprintf("Batch: %d beads to %s", len(beadIDs), rigName)
	prose := fmt.Sprintf("Auto-created convoy tracking %d beads", len(beadIDs))
	description := beads.SetConvoyFields(&beads.Issue{Description: prose}, &beads.ConvoyFields{
		Merge:      mergeStrategy,
		BaseBranch: baseBranch,
	})

	createArgs := []string{
		"create",
		"--type=task",
		"--id=" + convoyID,
		"--title=" + convoyTitle,
		"--description=" + description,
		"--labels=" + convoyLabels(owned),
	}
	if beads.NeedsForceForID(convoyID) {
		createArgs = append(createArgs, "--force")
	}

	// Use BdCmd with WithAutoCommit to ensure convoy is persisted even when
	// gt sling has set BD_DOLT_AUTO_COMMIT=off globally (gt-9xum2 root cause fix).
	if out, err := BdCmd(createArgs...).Dir(townBeads).WithAutoCommit().CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("creating batch convoy: %w\noutput: %s", err, out)
	}

	// Add tracking relations for all beads, recording which succeed.
	// Use WithAutoCommit for the same reason as above.
	var tracked []string
	for _, beadID := range beadIDs {
		if err := addTrackingRelationFn(townRoot, convoyID, beadID); err != nil {
			// Log but continue — partial tracking is better than no tracking
			fmt.Printf("  Warning: could not track %s in convoy: %v\n", beadID, err)
		} else {
			tracked = append(tracked, beadID)
		}
	}

	return convoyID, tracked, nil
}

// createAutoConvoy creates an auto-convoy for a single issue and tracks it.
// If owned is true, the convoy is marked with the gt:owned label for caller-managed lifecycle.
// mergeStrategy is optional: "direct", "mr", or "local" (empty = default mr).
// Returns the created convoy ID.
func createAutoConvoy(beadID, beadTitle string, owned bool, mergeStrategy, baseBranch string) (_ string, retErr error) {
	defer func() { telemetry.RecordConvoyCreate(context.Background(), beadID, retErr) }()
	// Guard against flag-like titles propagating into convoy names (gt-e0kx5)
	if beads.IsFlagLikeTitle(beadTitle) {
		return "", fmt.Errorf("refusing to create convoy: bead title %q looks like a CLI flag", beadTitle)
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return "", fmt.Errorf("finding town root: %w", err)
	}

	townBeads := filepath.Join(townRoot, ".beads")

	// Generate convoy ID with hq-cv- prefix for visual distinction
	// The hq-cv- prefix is registered in routes during gt install
	convoyID := fmt.Sprintf("hq-cv-%s", slingGenerateShortID())

	// Create convoy with title "Work: <issue-title>"
	convoyTitle := fmt.Sprintf("Work: %s", beadTitle)
	prose := fmt.Sprintf("Auto-created convoy tracking %s", beadID)
	description := beads.SetConvoyFields(&beads.Issue{Description: prose}, &beads.ConvoyFields{
		Merge:      mergeStrategy,
		BaseBranch: baseBranch,
	})

	createArgs := []string{
		"create",
		"--type=task",
		"--id=" + convoyID,
		"--title=" + convoyTitle,
		"--description=" + description,
		"--labels=" + convoyLabels(owned),
	}
	if beads.NeedsForceForID(convoyID) {
		createArgs = append(createArgs, "--force")
	}

	// Use BdCmd with WithAutoCommit to ensure convoy is persisted even when
	// gt sling has set BD_DOLT_AUTO_COMMIT=off globally (gt-9xum2 root cause fix).
	if out, err := BdCmd(createArgs...).Dir(townBeads).WithAutoCommit().CombinedOutput(); err != nil {
		return "", fmt.Errorf("creating convoy: %w\noutput: %s", err, out)
	}

	// Add tracking relation: convoy tracks the issue.
	if err := addTrackingRelationFn(townRoot, convoyID, beadID); err != nil {
		fmt.Printf("Warning: Could not create auto-convoy tracking: %v\n", err)
	}

	return convoyID, nil
}

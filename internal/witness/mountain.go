// Mountain failure tracking for the Mountain-Eater (Layer 1).
//
// When a polecat exits without completing its hooked bead, this module checks
// if the issue belongs to a convoy with the "mountain" label. For mountain
// convoys, it increments a failure count (stored as mountain:failures:N label).
// After 3 failures, the issue is auto-skipped (marked blocked + mountain:skipped).
// For regular convoys, a warning is logged.
//
// See docs/design/convoy/mountain-eater.md section 5.
package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// MountainMaxFailures is the number of polecat failures before an issue is
// auto-skipped in a mountain convoy. Exported for testing.
const MountainMaxFailures = 3

// ConvoyFailureResult tracks the result of convoy failure tracking for a single issue.
type ConvoyFailureResult struct {
	IssueID      string
	ConvoyID     string // Tracking convoy (if any)
	IsMountain   bool   // Convoy has "mountain" label
	FailureCount int    // New failure count after increment
	Skipped      bool   // Issue was auto-skipped (count >= MountainMaxFailures)
	Warning      string // Warning message for regular convoys
	Error        error
}

// trackConvoyFailures processes zombie detection results for convoy failure tracking.
// For each zombie that had active work on a hook_bead (polecat failed without
// completing), checks if the issue belongs to a convoy and tracks the failure.
// Called from DetectZombiePolecats after all zombies are collected.
//
// gu-h7hc0: The convoy lookups are batched to avoid an N+1 explosion of bd
// subprocess+Dolt round-trips during a mass-death incident (documented: 14
// deaths in one rig). Instead of dep-list + show + show per zombie, we issue:
//   - one batched up-query (`bd dep list <all-hooks> --direction=up
//     --type=tracks`) to discover the tracking convoys and their labels, then
//   - one down-query per unique convoy (`bd dep list <convoy> --direction=down
//     --type=tracks`) to recover which hook bead belongs to which convoy and to
//     read each leg's current failure labels in the same response.
//
// Unique convoys are few — a mass death is typically concentrated in a single
// mountain convoy — so the read cost collapses from O(3N) to roughly O(unique
// convoys). The increment/skip writes remain per-affected-leg (they are rare
// and must hit each leg individually).
func trackConvoyFailures(bd *BdCli, workDir string, result *DetectZombiePolecatsResult) {
	// Collect the hook beads of zombies that represent an actual incomplete
	// failure. Submitted/orphan cleanup states can still carry a hook_bead for
	// traceability, but they must not increment mountain failure counts.
	hookBeads := make([]string, 0, len(result.Zombies))
	seen := make(map[string]bool, len(result.Zombies))
	for i := range result.Zombies {
		zombie := &result.Zombies[i]
		if zombie.HookBead == "" || !zombieImpliesActiveFailure(*zombie) {
			continue
		}
		if seen[zombie.HookBead] {
			continue
		}
		seen[zombie.HookBead] = true
		hookBeads = append(hookBeads, zombie.HookBead)
	}
	if len(hookBeads) == 0 {
		return
	}

	// Resolve hook bead -> tracking convoy in batch.
	assignments := resolveConvoyAssignments(bd, workDir, hookBeads)
	if len(assignments) == 0 {
		return
	}

	// Process the affected legs in stable hook-bead order.
	for _, issueID := range hookBeads {
		a, ok := assignments[issueID]
		if !ok {
			continue // Not convoy-tracked
		}

		cfr := &ConvoyFailureResult{
			IssueID:    issueID,
			ConvoyID:   a.convoyID,
			IsMountain: a.isMountain,
		}
		if a.isMountain {
			// Reuse the leg labels already fetched by the down-query instead of
			// re-issuing a bd show per leg.
			cfr.Error = applyMountainFailure(bd, workDir, issueID, a.issueLabels, cfr)
		} else {
			cfr.Warning = fmt.Sprintf("polecat failure on convoy-tracked issue %s (convoy %s)", issueID, a.convoyID)
		}

		if cfr.IsMountain {
			if cfr.Skipped {
				fmt.Fprintf(os.Stderr, "witness: Mountain: skipped %s after %d failures (convoy %s)\n",
					cfr.IssueID, cfr.FailureCount, cfr.ConvoyID)
			} else {
				fmt.Fprintf(os.Stderr, "witness: Mountain: %s failure %d/%d (convoy %s)\n",
					cfr.IssueID, cfr.FailureCount, MountainMaxFailures, cfr.ConvoyID)
			}
		} else if cfr.Warning != "" {
			fmt.Fprintf(os.Stderr, "witness: %s\n", cfr.Warning)
		}

		if cfr.Error != nil {
			fmt.Fprintf(os.Stderr, "witness: convoy failure tracking error for %s: %v\n",
				cfr.IssueID, cfr.Error)
		}

		result.ConvoyFailures = append(result.ConvoyFailures, *cfr)
	}
}

// convoyAssignment records the tracking convoy a hook bead belongs to, along
// with the convoy's mountain status and the leg's labels (carrying the
// mountain:failures:N count) as observed in the convoy down-query.
type convoyAssignment struct {
	convoyID    string
	isMountain  bool
	issueLabels []string
}

// resolveConvoyAssignments maps each hook bead to its tracking convoy using two
// batched query stages (see trackConvoyFailures). Hook beads with no tracking
// convoy are absent from the returned map.
//
// An issue is assumed to belong to at most one active convoy (the same
// invariant the per-issue path relies on); when a hook bead is tracked by more
// than one convoy, the first convoy encountered in the up-query order wins.
func resolveConvoyAssignments(bd *BdCli, workDir string, hookBeads []string) map[string]*convoyAssignment {
	// Stage 1: one up-query discovers every convoy tracking any hook bead,
	// deduped in first-seen order, with mountain status read from inline labels.
	convoys := getTrackingConvoysBatch(bd, workDir, hookBeads)
	if len(convoys) == 0 {
		return nil
	}

	eligible := make(map[string]bool, len(hookBeads))
	for _, id := range hookBeads {
		eligible[id] = true
	}

	// Stage 2: one down-query per unique convoy recovers attribution (which leg
	// belongs to which convoy) and each leg's failure labels in the same
	// response. First convoy to claim a leg wins.
	assignments := make(map[string]*convoyAssignment, len(hookBeads))
	for _, c := range convoys {
		for _, leg := range getConvoyLegsWithLabels(bd, workDir, c.id) {
			if !eligible[leg.id] {
				continue
			}
			if _, claimed := assignments[leg.id]; claimed {
				continue
			}
			assignments[leg.id] = &convoyAssignment{
				convoyID:    c.id,
				isMountain:  c.isMountain,
				issueLabels: leg.labels,
			}
		}
	}
	return assignments
}

// convoyRef is a tracking convoy discovered by the batched up-query.
type convoyRef struct {
	id         string
	isMountain bool
}

// beadRef is a bead id paired with its labels.
type beadRef struct {
	id     string
	labels []string
}

// getTrackingConvoysBatch issues a single `bd dep list <ids...> --direction=up
// --type=tracks --json` and returns the tracking convoys, deduped in first-seen
// order. Mountain status is read from each convoy's inline labels, so no
// follow-up bd show is needed. The batched up-query returns a flat array of
// dependent (convoy) records with no per-source attribution; attribution is
// recovered separately via the per-convoy down-query.
func getTrackingConvoysBatch(bd *BdCli, workDir string, issueIDs []string) []convoyRef {
	args := append([]string{"dep", "list"}, issueIDs...)
	args = append(args, "--direction=up", "--type=tracks", "--json")
	output, err := bd.Exec(workDir, args...)
	if err != nil || output == "" || output == "[]" || output == "null" {
		return nil
	}

	var deps []struct {
		ID     string   `json:"id"`
		Type   string   `json:"dependency_type"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(output), &deps); err != nil {
		return nil
	}

	convoys := make([]convoyRef, 0, len(deps))
	seen := make(map[string]bool, len(deps))
	for _, d := range deps {
		if d.ID == "" || seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		convoys = append(convoys, convoyRef{id: d.ID, isMountain: hasLabel(d.Labels, "mountain")})
	}
	return convoys
}

// getConvoyLegsWithLabels issues a single `bd dep list <convoy> --direction=down
// --type=tracks --json` and returns the convoy's tracked legs with their labels.
// The single-id down-query returns full issue records (id + labels), so each
// leg's mountain:failures:N count is available without a per-leg bd show.
func getConvoyLegsWithLabels(bd *BdCli, workDir, convoyID string) []beadRef {
	output, err := bd.Exec(workDir, "dep", "list", convoyID, "--direction=down", "--type=tracks", "--json")
	if err != nil || output == "" || output == "[]" || output == "null" {
		return nil
	}

	var legs []struct {
		ID     string   `json:"id"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(output), &legs); err != nil {
		return nil
	}

	refs := make([]beadRef, 0, len(legs))
	for _, l := range legs {
		if l.ID == "" {
			continue
		}
		refs = append(refs, beadRef{id: l.ID, labels: l.Labels})
	}
	return refs
}

func zombieImpliesActiveFailure(zombie ZombieResult) bool {
	switch zombie.Classification {
	case ZombieBeadClosedStillRunning, ZombieSubmittedStillRunning:
		return false
	}
	if zombie.Classification != "" {
		return zombie.Classification.ImpliesActiveWork()
	}
	return zombie.WasActive
}

// TrackConvoyFailure checks if an issue belongs to a convoy and tracks the
// polecat failure. For mountain convoys, increments the failure count and
// auto-skips after MountainMaxFailures. For regular convoys, returns a warning.
//
// Returns nil if the issue has no tracking convoy.
func TrackConvoyFailure(bd *BdCli, workDir, issueID string) *ConvoyFailureResult {
	if issueID == "" {
		return nil
	}

	// Find convoys tracking this issue (dependents with type "tracks")
	convoyIDs := getTrackingConvoysCLI(bd, workDir, issueID)
	if len(convoyIDs) == 0 {
		return nil
	}

	// Process first matching convoy (an issue typically belongs to at most
	// one active convoy).
	convoyID := convoyIDs[0]
	labels := getBeadLabels(bd, workDir, convoyID)
	isMountain := hasLabel(labels, "mountain")

	result := &ConvoyFailureResult{
		IssueID:    issueID,
		ConvoyID:   convoyID,
		IsMountain: isMountain,
	}

	if isMountain {
		result.Error = trackMountainFailure(bd, workDir, issueID, result)
	} else {
		result.Warning = fmt.Sprintf("polecat failure on convoy-tracked issue %s (convoy %s)", issueID, convoyID)
	}

	return result
}

// trackMountainFailure increments the failure count for a mountain-tracked
// issue and auto-skips if the count reaches MountainMaxFailures. Used by the
// per-issue path (TrackConvoyFailure), which fetches the issue labels itself.
func trackMountainFailure(bd *BdCli, workDir, issueID string, result *ConvoyFailureResult) error {
	issueLabels := getBeadLabels(bd, workDir, issueID)
	return applyMountainFailure(bd, workDir, issueID, issueLabels, result)
}

// applyMountainFailure increments the failure count for a mountain-tracked issue
// (using the supplied current labels) and auto-skips if the count reaches
// MountainMaxFailures. The labels are passed in so callers that already fetched
// them (the batched path) do not re-issue a bd show per leg.
func applyMountainFailure(bd *BdCli, workDir, issueID string, issueLabels []string, result *ConvoyFailureResult) error {
	currentCount := getMountainFailureCount(issueLabels)
	newCount := currentCount + 1
	result.FailureCount = newCount

	// Update failure count label
	if err := updateMountainFailureCount(bd, workDir, issueID, currentCount, newCount); err != nil {
		return fmt.Errorf("updating failure count: %w", err)
	}

	// Auto-skip after MountainMaxFailures
	if newCount >= MountainMaxFailures {
		if err := skipMountainIssue(bd, workDir, issueID, newCount); err != nil {
			return fmt.Errorf("skipping issue: %w", err)
		}
		result.Skipped = true
	}

	return nil
}

// getTrackingConvoysCLI finds convoy IDs that track a given issue using the bd CLI.
// Returns convoy IDs (dependents with type "tracks").
func getTrackingConvoysCLI(bd *BdCli, workDir, issueID string) []string {
	output, err := bd.Exec(workDir, "dep", "list", issueID, "--direction=up", "--type=tracks", "--json")
	if err != nil || output == "" || output == "[]" || output == "null" {
		return nil
	}

	var deps []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(output), &deps); err != nil {
		return nil
	}

	ids := make([]string, 0, len(deps))
	for _, d := range deps {
		ids = append(ids, d.ID)
	}
	return ids
}

// getBeadLabels returns the labels for a bead.
func getBeadLabels(bd *BdCli, workDir, beadID string) []string {
	output, err := bd.Exec(workDir, "show", beadID, "--json")
	if err != nil || output == "" {
		return nil
	}

	var issues []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil || len(issues) == 0 {
		return nil
	}
	return issues[0].Labels
}

// hasLabel checks if a label list contains a specific label.
func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// getMountainFailureCount extracts the failure count from labels.
// Looks for labels matching "mountain:failures:N" and returns N.
// Returns 0 if no failure label is found.
func getMountainFailureCount(labels []string) int {
	for _, l := range labels {
		if after, ok := strings.CutPrefix(l, "mountain:failures:"); ok {
			if n, err := strconv.Atoi(after); err == nil {
				return n
			}
		}
	}
	return 0
}

// updateMountainFailureCount updates the mountain:failures:N label on an issue.
// Removes the old count label (if any) and adds the new one.
func updateMountainFailureCount(bd *BdCli, workDir, issueID string, oldCount, newCount int) error {
	args := []string{"update", issueID}
	if oldCount > 0 {
		args = append(args, "--remove-label", fmt.Sprintf("mountain:failures:%d", oldCount))
	}
	args = append(args, "--add-label", fmt.Sprintf("mountain:failures:%d", newCount))
	return bd.Run(workDir, args...)
}

// skipMountainIssue retires a leg the Mountain-Eater has given up on: it adds the
// mountain:skipped label (for queryability/visibility) and then CLOSES the leg
// with a mountain:skipped close reason.
//
// Closing — rather than the old status=blocked — is essential to keep the convoy
// alive (gu-lcddy). A mountain convoy's synthesis bead is gated by blocks-edges
// from every leg, and bd's blocker computation (and convoy.go's completion gate)
// treat ONLY closed/tombstone blockers as satisfied. A blocked leg is therefore a
// permanent non-closed blocker: the synthesis rollup never dispatches and the
// convoy never auto-completes — the exact opposite of the design intent that
// skipping lets the convoy grind on around the failed leg. Closing satisfies the
// edge so the synthesis can run and the convoy can close.
//
// The matching convoy ship-verification gate recognizes this close reason
// (see shippingNotExpected) so the closed-but-unshipped leg does not trip the
// Pattern B/C false-close warning and re-block the convoy.
func skipMountainIssue(bd *BdCli, workDir, issueID string, failureCount int) error {
	if err := bd.Run(workDir, "update", issueID, "--add-label", "mountain:skipped"); err != nil {
		return err
	}
	reason := fmt.Sprintf("mountain:skipped: Skipped by Mountain-Eater after %d polecat failures", failureCount)
	return bd.Run(workDir, "close", issueID, "--reason", reason)
}

package cmd

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// scheduleItem is the minimal bead data the candidate-selection ladder needs.
// Both convoy tracked issues (trackedIssueInfo) and epic children (epicChild)
// adapt to it so they can share a single filter ladder.
type scheduleItem struct {
	ID          string
	Title       string
	Status      string
	Assignee    string
	Labels      []string
	DeferUntil  string
	Description string
}

// scheduleCandidate is an item that passed the candidate-selection ladder and is
// ready to be scheduled/dispatched to a rig.
type scheduleCandidate struct {
	ID      string
	Title   string
	RigName string
}

// scheduleSkipCounts tallies why items were excluded from the candidate set.
type scheduleSkipCounts struct {
	Closed    int
	Deferred  int
	Assigned  int
	Scheduled int
	NoRig     int
}

// any reports whether anything was skipped (used to gate the skip summary line).
func (s scheduleSkipCounts) any() bool {
	return s.Closed > 0 || s.Deferred > 0 || s.Assigned > 0 || s.Scheduled > 0 || s.NoRig > 0
}

// selectScheduleCandidates runs the shared candidate-selection filter ladder for
// the convoy and epic schedulers. It walks each item through: closed/tombstone
// skip -> deferred guard -> assigned skip -> already-scheduled skip -> rig
// resolution -> append, returning the surviving candidates plus a tally of why
// items were skipped.
//
// Deferred guard (gu-pi35l regression). The df7da9bf fix added the deferred guard
// to runSling / executeSling / scheduleBead / the stranded scan, but originally
// NOT to the convoy/epic candidate selection. That let `gt sling <convoy|epic>`
// (the re-attach path) select a DEFERRED tracked bead/child and re-attach a fresh
// molecule every cycle — the convoy-driven re-attach bypass observed on gu-n5dvk.
// Filtering deferred items here (status=deferred OR future defer_until) keeps them
// out of the candidate set entirely so they never reach scheduleBead/executeSling.
// Not bypassed by force — un-defer the bead first. Centralizing the ladder here
// means new guards are added once instead of being duplicated across both
// schedulers (the canonical fix-one-copy-miss-the-other failure this consolidates).
//
// scheduledSet may be nil for direct-dispatch (sling) paths that do not pre-check
// scheduling status; a nil map yields false on lookup so no items are skipped as
// already-scheduled.
func selectScheduleCandidates(townRoot string, items []scheduleItem, scheduledSet map[string]bool, force bool) ([]scheduleCandidate, scheduleSkipCounts) {
	var candidates []scheduleCandidate
	var skips scheduleSkipCounts

	for _, it := range items {
		if it.Status == "closed" || it.Status == "tombstone" {
			skips.Closed++
			continue
		}

		if isDeferredBead(&beadInfo{Status: it.Status, DeferUntil: it.DeferUntil, Description: it.Description}) {
			skips.Deferred++
			continue
		}

		if it.Assignee != "" && !force {
			skips.Assigned++
			continue
		}

		if scheduledSet[it.ID] {
			skips.Scheduled++
			continue
		}

		rigName := resolveRigForBeadWithLabels(townRoot, it.ID, it.Labels)
		if rigName == "" {
			skips.NoRig++
			prefix := beads.ExtractPrefix(it.ID)
			fmt.Printf("  %s %s: cannot resolve rig from prefix %q (town-root or unknown)\n",
				style.Dim.Render("○"), it.ID, prefix)
			continue
		}

		candidates = append(candidates, scheduleCandidate{ID: it.ID, Title: it.Title, RigName: rigName})
	}

	return candidates, skips
}

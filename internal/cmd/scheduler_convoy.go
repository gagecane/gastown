package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// convoyScheduleOpts holds options for convoy schedule operations.
type convoyScheduleOpts struct {
	Formula     string
	HookRawBead bool
	Force       bool
	DryRun      bool
	NoBoot      bool
}

// runConvoyScheduleByID schedules all open tracked issues of a convoy.
func runConvoyScheduleByID(convoyID string, opts convoyScheduleOpts) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	if err := verifyBeadExists(convoyID); err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	townBeads := filepath.Join(townRoot, ".beads")
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return fmt.Errorf("getting tracked issues: %w", err)
	}

	if len(tracked) == 0 {
		fmt.Printf("Convoy %s has no tracked issues.\n", convoyID)
		return nil
	}

	// Batch-check scheduling status for all tracked issues (single DB query).
	var beadIDs []string
	for _, t := range tracked {
		beadIDs = append(beadIDs, t.ID)
	}
	scheduledSet := areScheduled(beadIDs)

	items := make([]scheduleItem, 0, len(tracked))
	for _, t := range tracked {
		items = append(items, scheduleItem{
			ID:          t.ID,
			Title:       t.Title,
			Status:      t.Status,
			Assignee:    t.Assignee,
			Labels:      t.Labels,
			DeferUntil:  t.DeferUntil,
			Description: t.Description,
		})
	}
	candidates, skips := selectScheduleCandidates(townRoot, items, scheduledSet, opts.Force)

	if len(candidates) == 0 {
		fmt.Printf("No issues to schedule from convoy %s", convoyID)
		if skips.any() {
			fmt.Printf(" (%d closed, %d deferred, %d assigned, %d already scheduled, %d no rig)",
				skips.Closed, skips.Deferred, skips.Assigned, skips.Scheduled, skips.NoRig)
		}
		fmt.Println()
		return nil
	}

	formula := opts.Formula

	if opts.DryRun {
		fmt.Printf("%s Would schedule %d issue(s) from convoy %s:\n",
			style.Bold.Render("DRY-RUN"), len(candidates), convoyID)
		if formula != "" {
			fmt.Printf("  Formula: %s\n", formula)
		} else {
			fmt.Printf("  Hook raw beads (no formula)\n")
		}
		for _, c := range candidates {
			fmt.Printf("  Would schedule: %s -> %s (%s)\n", c.ID, c.RigName, c.Title)
		}
		if skips.any() {
			fmt.Printf("\nSkipped: %d closed, %d deferred, %d assigned, %d already scheduled, %d no rig\n",
				skips.Closed, skips.Deferred, skips.Assigned, skips.Scheduled, skips.NoRig)
		}
		return nil
	}

	fmt.Printf("%s Scheduling %d issue(s) from convoy %s...\n",
		style.Bold.Render("📋"), len(candidates), convoyID)

	successCount := 0
	for _, c := range candidates {
		err := scheduleBead(c.ID, c.RigName, ScheduleOptions{
			Formula:     formula,
			NoConvoy:    true, // Already tracked by this convoy
			Force:       opts.Force,
			HookRawBead: opts.HookRawBead,
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), c.ID, err)
			continue
		}
		successCount++
	}

	fmt.Printf("\n%s Scheduled %d/%d issue(s) from convoy %s\n",
		style.Bold.Render("📊"), successCount, len(candidates), convoyID)
	if skips.any() {
		fmt.Printf("  Skipped: %d closed, %d deferred, %d assigned, %d already scheduled, %d no rig\n",
			skips.Closed, skips.Deferred, skips.Assigned, skips.Scheduled, skips.NoRig)
	}

	if successCount == 0 {
		return fmt.Errorf("all %d schedule attempts failed for convoy %s", len(candidates), convoyID)
	}
	return nil
}

// runConvoySlingByID immediately dispatches all open tracked issues of a convoy.
// Used when max_polecats=-1 (direct dispatch mode). Each tracked issue gets its
// own polecat via executeSling(). Sets NoConvoy=true since issues are already tracked.
func runConvoySlingByID(convoyID string, opts convoyScheduleOpts) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	if err := verifyBeadExists(convoyID); err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	townBeads := filepath.Join(townRoot, ".beads")
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return fmt.Errorf("getting tracked issues: %w", err)
	}

	if len(tracked) == 0 {
		fmt.Printf("Convoy %s has no tracked issues.\n", convoyID)
		return nil
	}

	items := make([]scheduleItem, 0, len(tracked))
	for _, t := range tracked {
		items = append(items, scheduleItem{
			ID:          t.ID,
			Title:       t.Title,
			Status:      t.Status,
			Assignee:    t.Assignee,
			Labels:      t.Labels,
			DeferUntil:  t.DeferUntil,
			Description: t.Description,
		})
	}
	// Direct-dispatch convoy path: no pre-check of scheduling status (nil set),
	// so the shared ladder skips no items as already-scheduled. The deferred guard
	// still applies — opts.Force passes into executeSling whose own deferred guard
	// is bypassed by an explicit --force, so filtering here keeps the force seam
	// from re-dispatching deferred beads (the gu-n5dvk re-attach bypass).
	candidates, skips := selectScheduleCandidates(townRoot, items, nil, opts.Force)

	if len(candidates) == 0 {
		fmt.Printf("No issues to dispatch from convoy %s", convoyID)
		if skips.any() {
			fmt.Printf(" (%d closed, %d deferred, %d assigned, %d no rig)",
				skips.Closed, skips.Deferred, skips.Assigned, skips.NoRig)
		}
		fmt.Println()
		return nil
	}

	formula := opts.Formula

	if opts.DryRun {
		fmt.Printf("%s Would dispatch %d issue(s) from convoy %s:\n",
			style.Bold.Render("DRY-RUN"), len(candidates), convoyID)
		for _, c := range candidates {
			fmt.Printf("  Would dispatch: %s -> %s (%s)\n", c.ID, c.RigName, c.Title)
		}
		if skips.any() {
			fmt.Printf("\nSkipped: %d closed, %d deferred, %d assigned, %d no rig\n",
				skips.Closed, skips.Deferred, skips.Assigned, skips.NoRig)
		}
		return nil
	}

	fmt.Printf("%s Dispatching %d issue(s) from convoy %s...\n",
		style.Bold.Render("▶"), len(candidates), convoyID)

	successCount := 0
	successfulRigs := make(map[string]bool)
	for i, c := range candidates {
		if slingMaxConcurrent > 0 && i >= slingMaxConcurrent {
			fmt.Printf("  %s Reached --max-concurrent spawn batch size (%d), remaining will be scheduled next cycle\n", style.Dim.Render("○"), slingMaxConcurrent)
			break
		}

		fmt.Printf("\n[%d/%d] Dispatching %s → %s...\n", i+1, len(candidates), c.ID, c.RigName)
		_, err := executeSling(SlingParams{
			BeadID:        c.ID,
			RigName:       c.RigName,
			FormulaName:   formula,
			Force:         opts.Force,
			HookRawBead:   opts.HookRawBead,
			NoConvoy:      true, // Already tracked by this convoy
			NoBoot:        opts.NoBoot,
			CallerContext: "convoy-sling",
			TownRoot:      townRoot,
			BeadsDir:      filepath.Join(townRoot, ".beads"),
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), c.ID, err)
			continue
		}
		successCount++
		successfulRigs[c.RigName] = true

		// Brief delay between spawns to avoid Dolt contention
		if i < len(candidates)-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Wake rig agents for each unique rig that had successful dispatches
	if !opts.NoBoot {
		for rig := range successfulRigs {
			wakeRigAgents(rig)
		}
	}

	fmt.Printf("\n%s Dispatched %d/%d issue(s) from convoy %s\n",
		style.Bold.Render("📊"), successCount, len(candidates), convoyID)
	if skips.any() {
		fmt.Printf("  Skipped: %d closed, %d deferred, %d assigned, %d no rig\n",
			skips.Closed, skips.Deferred, skips.Assigned, skips.NoRig)
	}

	if successCount == 0 {
		return fmt.Errorf("all %d dispatch attempts failed for convoy %s", len(candidates), convoyID)
	}
	return nil
}

package cmd

import (
	"fmt"
	"sort"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

// runSlingAll implements `gt sling --all <rig>`: enumerate every ready,
// dispatchable bead in a rig and sling each to its own polecat in one command.
//
// It reuses the exact ready-set source and filters that `gt ready --rig <rig>`
// uses (readyDispatchableBeadIDsForRig), then delegates to the existing batch
// dispatch paths (runBatchSchedule when the scheduler is active, runBatchSling
// otherwise). This means:
//   - Enumeration matches `gt ready --rig` (open + unblocked, minus formula
//     scaffolds, wisps, identity/epic/mayor-only beads, and off-route IDs).
//   - The per-bead server-side scheduleBead/executeSling guards still apply, so
//     any bead that slips through the ready filters but is non-dispatchable is
//     skipped with a reason rather than slung.
//   - Idempotency is inherited: already-hooked/scheduled beads are not in the
//     ready set, so re-running --all is a no-op for in-flight work.
//   - --dry-run is inherited from the batch paths (prints the set without acting).
//
// Unlike the unattended auto-dispatch plugin, --all INCLUDES agent/crew-owned
// beads by default: an operator running `gt sling --all` is explicitly asking
// to sling everything ready in the rig (gu-rlyor; remedies the gu-3y6ro
// crew-owned-step stranding case).
func runSlingAll(townRoot, townBeadsDir string) error {
	// Resolve the rig target: --rig flag wins, else the single positional.
	rigName := slingAllRig
	if rigName == "" {
		return fmt.Errorf("--all requires a rig: gt sling --all <rig> (or --all --rig <rig>)")
	}
	if _, isRig := IsRigName(rigName); !isRig {
		return fmt.Errorf("'%s' is not a known rig", rigName)
	}

	beadIDs, err := readyDispatchableBeadIDsForRig(townRoot, rigName)
	if err != nil {
		return err
	}
	if len(beadIDs) == 0 {
		fmt.Printf("%s No ready, dispatchable beads in rig '%s'\n", style.Dim.Render("○"), rigName)
		return nil
	}

	fmt.Printf("%s --all: %d ready bead(s) in rig '%s'\n", style.Bold.Render("🎯"), len(beadIDs), rigName)

	// Route through the existing batch paths so all guards, idempotency, and
	// dry-run behavior are shared with `gt sling <a> <b> <c> <rig>`.
	deferred, deferErr := shouldDeferDispatch()
	if deferErr != nil {
		return deferErr
	}
	if deferred {
		return runBatchSchedule(beadIDs, rigName, townRoot)
	}
	return runBatchSling(beadIDs, rigName, townBeadsDir)
}

// readyDispatchableBeadIDsForRig returns the IDs of ready, dispatchable beads
// for a single rig, applying the same filters as `gt ready --rig <rig>`. The
// result is sorted by priority (highest first, i.e. lowest priority number)
// so the most important work is slung first.
func readyDispatchableBeadIDsForRig(townRoot, rigName string) ([]string, error) {
	rigsConfigPath := constants.MayorRigsPath(townRoot)
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := mgr.GetRig(rigName)
	if err != nil {
		return nil, fmt.Errorf("rig not found: %s", rigName)
	}

	rigBeads := beads.New(r.BeadsPath())
	issues, err := rigBeads.Ready()
	if err != nil {
		return nil, fmt.Errorf("reading ready beads for rig %s: %w", rigName, err)
	}

	// Mirror gt ready --rig filtering (ready.go).
	formulaNames := getFormulaNames(r.BeadsPath())
	filtered := filterFormulaScaffolds(issues, formulaNames)
	wispIDs := getWispIDs(r.BeadsPath())
	filtered = filterWisps(filtered, wispIDs)
	filtered = filterReadyIssuesByRoute(townRoot, rigName, filtered)
	filtered = filterIdentityBeads(filtered)

	return sortReadyIssueIDs(filtered), nil
}

// sortReadyIssueIDs returns issue IDs sorted by priority (lower number = higher
// priority, slung first), breaking ties by ID for deterministic ordering.
func sortReadyIssueIDs(issues []*beads.Issue) []string {
	sorted := make([]*beads.Issue, len(issues))
	copy(sorted, issues)
	sort.SliceStable(sorted, func(a, b int) bool {
		if sorted[a].Priority != sorted[b].Priority {
			return sorted[a].Priority < sorted[b].Priority
		}
		return sorted[a].ID < sorted[b].ID
	})
	ids := make([]string, 0, len(sorted))
	for _, issue := range sorted {
		ids = append(ids, issue.ID)
	}
	return ids
}

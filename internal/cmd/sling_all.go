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

	// Thundering-herd guard (gu-iw3pf). A large ready-set slung all at once is a
	// per-bead polecat/tmux spawn burst that, stacked on a busy daemon heartbeat,
	// wedged the daemon and killed the tmux server (2026-06-15 incident). Bound
	// the fan-out to the daemon's spawn-gate cap two ways:
	//   1. When the set exceeds the cap, require an explicit confirmation
	//      (interactive y/n, or --force / --dry-run) so an operator never fires a
	//      surprise stampede. This protects both dispatch modes — including the
	//      deferred path, which still synchronously creates N sling-contexts +
	//      convoys against Dolt.
	//   2. For the direct-spawn path, default --max-concurrent to the cap so
	//      runBatchSling paces spawns in waves instead of all at once.
	spawnCap := slingAllSpawnCap(townRoot)
	if !slingDryRun && len(beadIDs) > spawnCap {
		if !confirmLargeSlingAll(len(beadIDs), spawnCap, rigName, slingForce) {
			return fmt.Errorf("aborted: %d ready beads exceeds the spawn-gate cap of %d", len(beadIDs), spawnCap)
		}
	}

	// Route through the existing batch paths so all guards, idempotency, and
	// dry-run behavior are shared with `gt sling <a> <b> <c> <rig>`.
	deferred, deferErr := shouldDeferDispatch()
	if deferErr != nil {
		return deferErr
	}
	if deferred {
		return runBatchSchedule(beadIDs, rigName, townRoot)
	}
	// Direct-spawn path: throttle spawn rate to the cap when the operator did
	// not set --max-concurrent, so the fan-out ramps in waves instead of firing
	// every polecat at once.
	if slingMaxConcurrent <= 0 && len(beadIDs) > spawnCap {
		slingMaxConcurrent = spawnCap
	}
	return runBatchSling(beadIDs, rigName, townBeadsDir)
}

// slingAllSpawnCap returns the spawn-gate cap used to bound a `--all` fan-out.
// It mirrors the daemon's per-heartbeat spawn cap
// (scheduler-independent: this is the rate at which NEW sessions may start),
// so an operator's manual `--all` burst is held to the same ceiling the
// unattended dispatcher already respects. Falls back to the default when town
// config is unavailable.
func slingAllSpawnCap(townRoot string) int {
	if townRoot == "" {
		return config.DefaultSpawnMaxPerHeartbeat
	}
	c := config.LoadOperationalConfig(townRoot).GetDaemonConfig().SpawnMaxPerHeartbeatV()
	if c <= 0 {
		return config.DefaultSpawnMaxPerHeartbeat
	}
	return c
}

// confirmLargeSlingAll gates a fan-out larger than the spawn-gate cap. With
// --force on an interactive terminal it prompts y/n; with --force on a
// non-interactive stream it proceeds (operator opted in explicitly). Without
// --force on a terminal it prompts; without --force on a non-interactive stream
// it refuses and points at the safe alternatives. Mirrors confirmUnsafeProceed
// (rig.go) and reuses its test seams (isStdinTerminal, promptYesNoUnsafeProceed).
func confirmLargeSlingAll(count, spawnCap int, rigName string, force bool) bool {
	if isStdinTerminal() {
		fmt.Printf("\n%s slinging %d ready beads in '%s' exceeds the spawn-gate cap of %d.\n",
			style.Warning.Render("⚠"), count, rigName, spawnCap)
		fmt.Printf("  Each bead spawns its own polecat (tmux session + Claude process). A burst this large\n")
		fmt.Printf("  can wedge the daemon. Spawns will be throttled to %d at a time.\n", spawnCap)
		return promptYesNoUnsafeProceed("Proceed?")
	}
	if force {
		return true
	}
	fmt.Printf("\n%s %d ready beads exceeds the spawn-gate cap of %d (rig '%s').\n",
		style.Warning.Render("⚠"), count, spawnCap, rigName)
	fmt.Printf("  Re-run with %s to confirm, or %s to preview the set without slinging.\n",
		style.Bold.Render("--force"), style.Bold.Render("--dry-run"))
	return false
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

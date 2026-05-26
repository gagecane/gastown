// Auto-test-pr cycle implementation.
//
// Phase 0 task 4 (gu-zbat) — the mol-auto-test-pr-cycle formula. Runs
// as a standing Mayor patrol. Complete cycle logic is implemented here
// so Phase 1 activation is a single-flag flip (set auto_test_pr.enabled
// on a rig).
//
// Per-tick steps (from the synthesis §"Proposed Design component 1"):
//
//  0. Reconcile enabled_rigs[]: walk rig settings JSON, CAS-update town
//     bead's enabled_rigs[] to match. Self-healing for partial-failure
//     cases from task 2a's two-step write. Idempotent, <100ms.
//  1. D18 cooldown-release: for each enabled rig in state=cooled-down,
//     if now - last_transition.at >= cadence_days*24h, CAS
//     cooled-down→idle.
//  2. Read town-auto-test-pr-state for global pause / circuit-breaker.
//  3. For each enabled rig: read <rig>-auto-test-state; if state!=idle
//     or paused_until>now, skip.
//  4. CAS idle→picking: on commit failure, skip (another tick running
//     this rig).
//  5. Compute target candidates: git log --since=30d × coverage profile,
//     ranked by (churn × uncovered_branches). Per-file rejection
//     cooldown filtering. Within-file churn-proximity ranking (NG5).
//  6. CAS picking→dispatched: file dispatch bead (JSON envelope);
//     sling-attach to polecat pool with priority floor.
//
// Exit-early path (Phase 0): Step 2 reads town state; if enabled_rigs
// is empty → log + exit 0. No further processing.
//
// Missing-town-bead path (Round 2 fix #10): If town-auto-test-pr-state
// cannot be read (ErrTownStateNotProvisioned), emit a structured warning
// and return nil — do NOT panic.
//
// Design context:
//   - .designs/auto-test-pr/synthesis.md §"Proposed Design"
//   - .designs/auto-test-pr/integration.md §"Phase 0 — task 4"
package autotestpr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

// CycleResult holds the outcome of a single cycle tick for structured
// logging / telemetry. Callers can inspect this to determine what
// happened without parsing log output.
type CycleResult struct {
	// Reconciled is true if the reconcile step ran (always on success).
	Reconciled bool `json:"reconciled"`

	// EnabledRigs is the list of enabled rigs after reconcile.
	EnabledRigs []string `json:"enabled_rigs"`

	// ExitReason is a short machine-readable tag explaining why the
	// cycle exited early. Empty string means the cycle ran to completion
	// (or would have, in Phase 0's inert path). Common values:
	//   "no-rigs-enabled"     — enabled_rigs[] is empty after reconcile
	//   "town-bead-missing"   — town-state bead not provisioned
	//   "global-pause"        — town-wide pause in effect
	//   "circuit-breaker"     — town circuit breaker is tripped
	ExitReason string `json:"exit_reason,omitempty"`

	// Warning is a human-readable warning message for operational
	// visibility. Set on degraded-but-not-fatal paths (e.g.,
	// town-bead-missing).
	Warning string `json:"warning,omitempty"`

	// RigsProcessed is the count of rigs that made it past the
	// state-check in step 3. Phase 0: always 0 (no rigs enabled).
	RigsProcessed int `json:"rigs_processed"`
}

// CycleConfig holds the configuration for a single cycle tick.
// Passed in by the caller (Mayor daemon) so the cycle doesn't depend
// on global state or environment variables.
type CycleConfig struct {
	// TownRoot is the absolute path to the town directory (~/ gt).
	TownRoot string

	// TownBeads is the beads client for the town-level beads store
	// (where town-auto-test-pr-state lives).
	TownBeads *beads.Beads

	// RigsConfig is the parsed rigs.json configuration, used to
	// enumerate all registered rigs for the reconcile step.
	RigsConfig *config.RigsConfig

	// Now is the wall-clock at the start of the tick. Passed in so
	// tests can pin time deterministically.
	Now time.Time
}

// validate returns nil if the config is well-formed. Exposed fields
// only; the cycle fails fast on bad config rather than proceeding with
// garbage state.
func (c *CycleConfig) validate() error {
	if c.TownRoot == "" {
		return fmt.Errorf("CycleConfig.TownRoot is empty")
	}
	if c.TownBeads == nil {
		return fmt.Errorf("CycleConfig.TownBeads is nil")
	}
	if c.RigsConfig == nil {
		return fmt.Errorf("CycleConfig.RigsConfig is nil")
	}
	if c.Now.IsZero() {
		return fmt.Errorf("CycleConfig.Now is zero — caller must set time.Now()")
	}
	return nil
}

// RunCycle executes a single tick of the auto-test-pr cycle. This is
// the top-level entry point called by the Mayor patrol daemon on its
// configured cadence.
//
// The function is intentionally synchronous — the caller (daemon)
// handles the ticker and concurrency. One tick = one RunCycle call.
//
// Returns a CycleResult describing what happened. Errors are reserved
// for unrecoverable infrastructure failures (Dolt down, config broken).
// Operational-but-degraded paths (missing town bead, no rigs enabled)
// return nil error with the reason encoded in CycleResult.ExitReason.
func RunCycle(cfg *CycleConfig) (*CycleResult, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("cycle config validation: %w", err)
	}

	result := &CycleResult{}

	// ─── Step 0: Reconcile enabled_rigs[] ────────────────────────────
	enabledRigs, err := reconcileEnabledRigs(cfg)
	if err != nil {
		return nil, fmt.Errorf("reconcile enabled_rigs: %w", err)
	}
	result.Reconciled = true
	result.EnabledRigs = enabledRigs

	// ─── Step 2: Read town-auto-test-pr-state ────────────────────────
	// (Step 1 — cooldown-release — is skipped if no rigs enabled, and
	// reading the town state first determines whether we can proceed.)
	townState, err := LoadTownState(cfg.TownBeads)
	if err != nil {
		if err == ErrTownStateNotProvisioned {
			// Round 2 fix #10: structured warning, not panic.
			result.ExitReason = "town-bead-missing"
			result.Warning = fmt.Sprintf(
				"town-auto-test-pr-state bead not provisioned; "+
					"run 'gt auto-test-pr status' to auto-provision. "+
					"Cycle exits gracefully. Timestamp: %s",
				cfg.Now.UTC().Format(time.RFC3339))
			return result, nil
		}
		return nil, fmt.Errorf("loading town state: %w", err)
	}

	// ─── Check exit-early: no rigs enabled ───────────────────────────
	if len(enabledRigs) == 0 {
		result.ExitReason = "no-rigs-enabled"
		return result, nil
	}

	// ─── Check global pause ──────────────────────────────────────────
	if townState.GlobalPauseUntil != "" {
		result.ExitReason = "global-pause"
		return result, nil
	}

	// ─── Check circuit breaker ───────────────────────────────────────
	if townState.CircuitBreaker.IsTripped() {
		result.ExitReason = "circuit-breaker"
		return result, nil
	}

	// ─── Step 1: D18 cooldown-release ────────────────────────────────
	// For each enabled rig in state=cooled-down, check if cadence has
	// elapsed. Phase 0: no per-rig state beads exist yet, so this is a
	// no-op. The code structure is here so Phase 1 wiring is minimal.
	//
	// TODO(phase-1): implement cooldown-release once per-rig state
	// beads are provisioned (task 15).

	// ─── Steps 3-6: Per-rig processing ──────────────────────────────
	// Phase 0: no rigs are enabled (auto_test_pr.enabled is false for
	// all rigs until Phase 1 flip). The loop below is the Phase 1
	// path — it compiles and is structurally complete, but in Phase 0
	// we never reach it because enabledRigs is empty above.
	for _, rigName := range enabledRigs {
		processed, err := processRig(cfg, rigName)
		if err != nil {
			// Per-rig errors are non-fatal to the cycle — log and
			// continue. The next tick retries.
			fmt.Fprintf(os.Stderr, "auto-test-pr-cycle: rig %s: %v\n", rigName, err)
			continue
		}
		if processed {
			result.RigsProcessed++
		}
	}

	return result, nil
}

// processRig handles steps 3-6 for a single rig. Returns true if the
// rig was processed (dispatched), false if skipped (not idle, paused,
// CAS conflict). Errors are non-fatal operational issues.
//
// Phase 0: this code compiles but is never reached (no rigs enabled).
// Phase 1 wires the per-rig state bead reads and CAS transitions.
func processRig(_ *CycleConfig, _ string) (bool, error) {
	// Phase 0 stub: no per-rig state beads exist yet. Steps 3-6 are
	// structurally present so Phase 1 is a targeted fill-in, not a
	// rewrite. Return false (not processed) — the cycle moves on.
	//
	// Phase 1 implementation outline:
	//   3. Read <rig>-auto-test-state; if state!=idle || paused → skip
	//   4. CAS idle→picking; on conflict → skip
	//   5. Compute target candidates (target_ranker.go)
	//   6. CAS picking→dispatched; file dispatch bead; sling-attach
	return false, nil
}

// reconcileEnabledRigs walks all rigs' settings JSON and computes the
// set of rigs with auto_test_pr.enabled=true. Then CAS-updates the
// town bead's enabled_rigs[] to match. This self-heals partial-failure
// cases from task 2a's two-step write (settings JSON → town bead).
//
// Returns the reconciled list of enabled rigs (sorted). Idempotent:
// if the town bead already matches, no write occurs.
//
// This is the "Round 3 fix #4" reconcile step.
func reconcileEnabledRigs(cfg *CycleConfig) ([]string, error) {
	// Walk all registered rigs and read their settings.
	groundTruth := computeEnabledRigsFromSettings(cfg.TownRoot, cfg.RigsConfig)

	// Load current town state to compare.
	townState, err := LoadTownState(cfg.TownBeads)
	if err != nil {
		if err == ErrTownStateNotProvisioned {
			// Town bead doesn't exist — reconcile cannot write to it.
			// The main cycle path will handle this via the town-bead-missing
			// exit. Return the ground truth so the result is accurate.
			return groundTruth, nil
		}
		return nil, fmt.Errorf("loading town state for reconcile: %w", err)
	}

	// Compare: if already in sync, skip the write.
	if stringSlicesEqual(townState.EnabledRigs, groundTruth) {
		return groundTruth, nil
	}

	// CAS-update: write the ground truth to the town bead.
	err = casUpdateEnabledRigs(cfg.TownBeads, groundTruth)
	if err != nil {
		// Non-fatal: the main cycle can still proceed with the ground
		// truth from settings JSON. The next tick retries the CAS.
		fmt.Fprintf(os.Stderr,
			"auto-test-pr-cycle: reconcile CAS failed (non-fatal): %v\n", err)
		return groundTruth, nil
	}

	return groundTruth, nil
}

// computeEnabledRigsFromSettings walks all rig settings and returns
// the sorted list of rig names with auto_test_pr.enabled=true.
func computeEnabledRigsFromSettings(townRoot string, rigsConfig *config.RigsConfig) []string {
	var enabled []string
	for rigName := range rigsConfig.Rigs {
		rigPath := filepath.Join(townRoot, rigName)
		settingsPath := config.RigSettingsPath(rigPath)
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			// Rig has no settings or broken config — not enabled.
			continue
		}
		if settings.GetAutoTestPR().IsEnabled() {
			enabled = append(enabled, rigName)
		}
	}
	sort.Strings(enabled)
	return enabled
}

// casUpdateEnabledRigs performs a CAS update of the town bead's
// enabled_rigs[] slice to match the provided ground truth. Uses the
// same retry pattern as AppendEnabledRig/RemoveEnabledRig.
func casUpdateEnabledRigs(b *beads.Beads, enabledRigs []string) error {
	return mutateTownState(b, func(s *TownState) error {
		// If already in sync after the re-read (another writer beat us),
		// skip the update.
		if stringSlicesEqual(s.EnabledRigs, enabledRigs) {
			return errSkipUpdate
		}
		s.EnabledRigs = enabledRigs
		return nil
	})
}

// stringSlicesEqual returns true if two sorted string slices have
// identical contents.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// CycleResultJSON returns a compact JSON representation of the result
// suitable for structured logging / daemon telemetry.
func (r *CycleResult) JSON() ([]byte, error) {
	return json.Marshal(r)
}

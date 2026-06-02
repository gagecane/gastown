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
	"errors"
	"fmt"
	"log"
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

	// Logger sinks operational messages (per-rig errors, non-fatal CAS
	// failures). Optional: if nil, the cycle defaults to a logger that
	// writes to os.Stderr with stdlib timestamp prefix. Tests can pass
	// a buffer-backed logger to assert on output.
	Logger *log.Logger

	// ─── Per-rig processing hooks (steps 1, 3-6) ─────────────────────
	// These are nil in Phase 0, which keeps the cycle INERT: with no
	// store, processRig and the cooldown-release step return without
	// touching any per-rig bead. Phase 1 (gu-gmj0r) supplies these to
	// activate the full pick→dispatch path — a single wiring change,
	// not a rewrite, per this file's "structurally complete" promise.

	// RigStore reads/writes per-rig state beads and appends transition
	// attachments. Nil → per-rig processing is skipped (Phase 0).
	RigStore RigStateStore

	// Targets computes the ranked-input candidates and active rejection
	// records for a rig (git churn × coverage profile). Nil → no
	// candidates, so a rig that reaches picking dispatches nothing and
	// is left for the next tick.
	Targets func(rig string) (candidates []TargetCandidate, rejections []RejectionRecord, err error)

	// Dispatch files the dispatch bead and sling-attaches it to the
	// polecat pool at the lowest priority floor. It returns the work
	// bead ID stamped into the rig's current-cycle pointer. Nil → the
	// rig is rolled back from picking (no dispatch this tick).
	Dispatch func(rig string, env DispatchEnvelope) (workBeadID string, err error)

	// LastTransitionAt returns the timestamp of the rig's most-recent
	// state transition, used by the D18 cooldown-release check. Nil →
	// cooldown-release is skipped for that rig.
	LastTransitionAt func(rig string) (time.Time, error)

	// RigCadenceDays returns the rig's configured auto-test-pr cadence
	// in days. Nil → DefaultCadenceDaysFallback is used.
	RigCadenceDays func(rig string) int
}

// logger returns cfg.Logger when set, otherwise a default stderr logger.
// Centralized so the two call sites stay symmetric and the fallback is
// clear from one place.
func (c *CycleConfig) logger() *log.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return log.New(os.Stderr, "", log.LstdFlags)
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
	// For each enabled rig in state=cooled-down, release back to idle if
	// the per-rig cadence has elapsed (cadence-elapsed trigger). A rig
	// in paused-by-circuit-breaker never auto-releases. Skipped entirely
	// in Phase 0 (cfg.RigStore nil → no per-rig beads to evaluate).
	for _, rigName := range enabledRigs {
		releaseCooldownForRig(cfg, rigName)
	}

	// ─── Steps 3-6: Per-rig processing ──────────────────────────────
	// Phase 0: no rigs are enabled (auto_test_pr.enabled is false for
	// all rigs until Phase 1 flip), so enabledRigs is empty above and
	// this loop never runs. The loop is structurally complete so Phase 1
	// activation is a single wiring change (supply cfg.RigStore etc.).
	for _, rigName := range enabledRigs {
		processed, err := processRig(cfg, rigName)
		if err != nil {
			// Per-rig errors are non-fatal to the cycle — log and
			// continue. The next tick retries.
			cfg.logger().Printf("auto-test-pr-cycle: rig %s: %v", rigName, err)
			continue
		}
		if processed {
			result.RigsProcessed++
		}
	}

	return result, nil
}

// releaseCooldownForRig runs the D18 cooldown-release check for a single
// rig. No-op when the Phase-1 hooks are absent (Phase 0 inert path).
// Errors are non-fatal: logged and swallowed so one rig's trouble does
// not stall the tick.
func releaseCooldownForRig(cfg *CycleConfig, rig string) {
	if cfg.RigStore == nil || cfg.LastTransitionAt == nil {
		return // Phase 0: no per-rig state to evaluate.
	}
	lastAt, err := cfg.LastTransitionAt(rig)
	if err != nil {
		cfg.logger().Printf("auto-test-pr-cycle: rig %s: cooldown-release last-transition lookup: %v", rig, err)
		return
	}
	cadence := DefaultCadenceDaysFallback
	if cfg.RigCadenceDays != nil {
		cadence = cfg.RigCadenceDays(rig)
	}
	released, err := ReleaseCooldownIfElapsed(cfg.RigStore, rig, lastAt, cadence, cfg.Now)
	if err != nil {
		cfg.logger().Printf("auto-test-pr-cycle: rig %s: cooldown-release: %v", rig, err)
		return
	}
	if released {
		cfg.logger().Printf("auto-test-pr-cycle: rig %s: cooldown-release → idle (cadence %dd elapsed)", rig, cadence)
	}
}

// processRig handles steps 3-6 for a single rig. Returns true if the
// rig was processed (dispatched), false if skipped (not idle, paused,
// CAS conflict, no candidates). Errors are non-fatal operational issues.
//
// Phase 0: never reached (no rigs enabled). When the Phase-1 hooks are
// absent it returns (false, nil) immediately, so the structure compiles
// and is exercised by tests via injected hooks.
//
// Steps:
//  3. Read <rig>-auto-test-state; skip unless state == idle.
//  4. CAS idle→picking; on conflict (another tick) → skip.
//  5. Compute & rank target candidates; if none, roll back picking→idle.
//  6. CAS picking→dispatched (stamps current-cycle); file dispatch bead.
func processRig(cfg *CycleConfig, rig string) (bool, error) {
	// Phase 0 inert path: no per-rig wiring supplied.
	if cfg.RigStore == nil || cfg.Targets == nil || cfg.Dispatch == nil {
		return false, nil
	}

	// Step 3: read per-rig state; only an idle rig is eligible.
	state, err := cfg.RigStore.LoadRigState(rig)
	if err != nil {
		return false, fmt.Errorf("loading rig state: %w", err)
	}
	if state.State != PerRigCycleStateIdle {
		return false, nil // not idle (in-flight, cooled-down, paused) → skip
	}
	if state.PausedUntil != "" {
		if until, perr := time.Parse(time.RFC3339, state.PausedUntil); perr == nil && until.After(cfg.Now) {
			return false, nil // per-rig pause still active
		}
	}

	// Step 4: CAS idle→picking. A conflict means another tick owns this
	// rig — skip without error.
	if err := CASTransition(cfg.RigStore, rig,
		PerRigCycleStateIdle, PerRigCycleStatePicking,
		"mayor", cfg.Now, nil); err != nil {
		if errors.Is(err, ErrTransitionConflict) {
			return false, nil
		}
		return false, fmt.Errorf("CAS idle→picking: %w", err)
	}

	// Step 5: compute and rank candidates.
	candidates, rejections, err := cfg.Targets(rig)
	if err != nil {
		// Roll back picking→idle so the rig is retried next tick.
		_ = CASTransition(cfg.RigStore, rig,
			PerRigCycleStatePicking, PerRigCycleStateIdle, "mayor", cfg.Now, nil)
		return false, fmt.Errorf("computing targets: %w", err)
	}
	ranked := RankCandidates(candidates, rejections, cfg.Now)
	if len(ranked) == 0 {
		// Nothing to test right now — return to idle.
		_ = CASTransition(cfg.RigStore, rig,
			PerRigCycleStatePicking, PerRigCycleStateIdle, "mayor", cfg.Now, nil)
		return false, nil
	}
	chosen := ranked[0]

	// Build the dispatch envelope for the top candidate. Conventions /
	// template / language come from the rig settings the Targets hook
	// already consulted; we pass the defaults the synthesis documents
	// and let the Dispatch hook override per-rig paths if needed.
	settings := cfg.rigAutoTestPR(rig)
	env := BuildDispatchEnvelope(
		"", rig, settings.language, settings.conventionsPath, settings.templatePath,
		chosen, DefaultSizeBudget(), cfg.Now,
	)

	// Step 6: file the dispatch bead, then CAS picking→dispatched
	// stamping the work bead onto the current-cycle pointer.
	workBeadID, err := cfg.Dispatch(rig, env)
	if err != nil {
		_ = CASTransition(cfg.RigStore, rig,
			PerRigCycleStatePicking, PerRigCycleStateIdle, "mayor", cfg.Now, nil)
		return false, fmt.Errorf("dispatch: %w", err)
	}

	if err := CASTransition(cfg.RigStore, rig,
		PerRigCycleStatePicking, PerRigCycleStateDispatched,
		"mayor", cfg.Now, func(s *RigState) {
			s.CurrentCycle = &CurrentCycle{
				CycleID:     workBeadID,
				StartedAt:   cfg.Now.UTC().Format(time.RFC3339),
				PolecatBead: workBeadID,
			}
		}); err != nil {
		return false, fmt.Errorf("CAS picking→dispatched: %w", err)
	}

	return true, nil
}

// rigAutoTestPRSettings is the small subset of a rig's auto_test_pr
// config the dispatch envelope needs. Resolved from rigs config when
// available; otherwise synthesis defaults.
type rigAutoTestPRSettings struct {
	language        string
	conventionsPath string
	templatePath    string
}

// rigAutoTestPR resolves the dispatch-relevant settings for a rig,
// applying synthesis defaults when the per-rig settings JSON cannot be
// read. Kept here (not on the hooks) so the envelope always has sane
// paths even if a rig opts in with a minimal config.
func (cfg *CycleConfig) rigAutoTestPR(rig string) rigAutoTestPRSettings {
	out := rigAutoTestPRSettings{
		language:        "go",
		conventionsPath: ".gt/auto-test-pr/conventions.md",
		templatePath:    ".gt/auto-test-pr/mr-template.md",
	}
	rigPath := filepath.Join(cfg.TownRoot, rig)
	settings, err := config.LoadRigSettings(config.RigSettingsPath(rigPath))
	if err != nil {
		return out
	}
	atpr := settings.GetAutoTestPR()
	if atpr != nil && atpr.Language != "" {
		out.language = atpr.Language
	}
	if cp := atpr.GetConventionsPath(); cp != "" {
		out.conventionsPath = cp
	}
	return out
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
		cfg.logger().Printf(
			"auto-test-pr-cycle: reconcile CAS failed (non-fatal): %v", err)
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

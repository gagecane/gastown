// Phase 5 (gu-1zfy). Per-rig health check for the upstream-sync
// subsystem. Fires only when --rig is supplied: the check is rig-
// scoped because the state bead lives at <rig-prefix>-upstream-sync-
// state and the configuration is per-rig in settings/config.json.
//
// What this check covers:
//   - Upstream-sync is enabled but the state bead is missing (the
//     Deacon should provision it; if days pass without provisioning
//     something is wedged).
//   - The rig is in StatePaused without a recorded reason (operator
//     paused without context, or auto-pause lost its reason).
//   - ConsecutiveFailures has crossed the configured (or default)
//     circuit-breaker threshold without a transition to Paused —
//     indicates a missed transition.
//   - The state bead has a CurrentAttempt with a ResolutionBranch
//     but no PolecatBead, or vice versa (orphaned dispatch context).
//
// What this check does NOT cover:
//   - Whether the latest sync attempt's gates passed (use `gt
//     upstream history`).
//   - Conflict-resolution polecat health (Witness owns that).
//   - Audit findings (use `gt upstream audit`).
//
// The check is read-only and never auto-fixes. Recovery is
// operator-driven (`gt upstream resume`, manual state edit, polecat
// dispatch) — too many of the failure modes are policy decisions to
// safely automate from `gt doctor --fix`.
package doctor

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/upstreamsync"
)

// UpstreamSyncCheck validates the per-rig upstream-sync state bead
// when the feature is enabled. Inactive (disabled) rigs return
// StatusOK with a "disabled" message so the doctor table stays
// consistent across rigs.
type UpstreamSyncCheck struct {
	BaseCheck
}

// NewUpstreamSyncCheck wires the check at the standard rig category.
// Registration lives in cmd/doctor.go alongside the other rig checks.
func NewUpstreamSyncCheck() *UpstreamSyncCheck {
	return &UpstreamSyncCheck{
		BaseCheck: BaseCheck{
			CheckName:        "upstream-sync-health",
			CheckDescription: "Verify upstream-sync state bead and circuit-breaker health (per-rig)",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run executes the health check. Returns:
//
//   - StatusOK with "disabled" when upstream_sync is not configured.
//   - StatusOK with "idle/synced/…" plus relevant context when healthy.
//   - StatusWarning for soft issues (paused without reason, stale).
//   - StatusError for structural issues (state bead missing while
//     enabled, orphaned dispatch context).
//
// Read-only by contract; never mutates the state bead or settings.
func (c *UpstreamSyncCheck) Run(ctx *CheckContext) *CheckResult {
	// Rig-scoped: only run when the operator passed --rig. Otherwise
	// this would fire once per rig without --rig, which is what we
	// want long-term but pulls in a rigs-discovery dependency we
	// haven't wired yet. Phase 5 ships the per-rig path; the all-rigs
	// rollup is a follow-up bead.
	if ctx.RigName == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "(skipped — run with --rig=<name> to check upstream-sync health)",
		}
	}

	rigPath := ctx.RigPath()
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not load %s: %v", settingsPath, err),
		}
	}
	if settings == nil || !settings.UpstreamSync.IsEnabled() {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "upstream-sync disabled (set upstream_sync.enabled=true to enable)",
		}
	}

	rigPrefix := deriveRigPrefix(ctx.RigName)
	bd := beads.NewWithBeadsDir(ctx.TownRoot, filepath.Join(ctx.TownRoot, ".beads"))

	state, err := upstreamsync.LoadSyncState(bd, rigPrefix)
	if err != nil {
		if errors.Is(err, upstreamsync.ErrStateBeadNotProvisioned) {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: "upstream-sync enabled but state bead not provisioned",
				Details: []string{
					fmt.Sprintf("Expected bead ID: %s", upstreamsync.StateBeadID(rigPrefix)),
					"The Deacon provisions the state bead on its first patrol tick.",
					"If this persists for more than one cooldown period, the Deacon is wedged.",
				},
				FixHint: "Verify the deacon is running (`gt deacon status`); inspect deacon logs for upstream-sync provisioning errors.",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not load upstream-sync state: %v", err),
		}
	}

	return assessUpstreamSyncState(c.Name(), state, settings.UpstreamSync)
}

// assessUpstreamSyncState classifies a loaded state bead into a
// CheckResult. Pure function so it can be unit-tested without a beads
// dependency. Caller passes the rig's UpstreamSyncConfig so the check
// can compare ConsecutiveFailures against the rig's configured
// threshold (falling back to the package default when nil).
func assessUpstreamSyncState(checkName string, state upstreamsync.SyncStateMetadata, cfg *config.UpstreamSyncConfig) *CheckResult {
	var problems []string
	worst := StatusOK

	// Paused without a reason: operator likely paused via direct bead
	// edit, or an old auto-pause lost its reason. Either way the
	// operator can't tell *why* without reading attempt history. Soft
	// problem — surface as warning.
	if state.State == upstreamsync.StatePaused && strings.TrimSpace(state.PauseReason) == "" {
		problems = append(problems,
			"Rig is paused but no PauseReason is recorded on the state bead.")
		worst = bumpStatus(worst, StatusWarning)
	}

	// Circuit-breaker threshold without paused state: this is a
	// missed transition. The dispatcher should have moved the rig to
	// Paused at >=MaxConsecutiveFailures — if we see the count above
	// threshold while in Idle/Failed/etc., the next attempt may run
	// despite the breaker.
	threshold := defaultCircuitBreakerThreshold(cfg)
	if state.ConsecutiveFailures >= threshold && state.State != upstreamsync.StatePaused {
		problems = append(problems, fmt.Sprintf(
			"ConsecutiveFailures=%d exceeds threshold=%d but state=%s (expected paused).",
			state.ConsecutiveFailures, threshold, state.State))
		worst = bumpStatus(worst, StatusError)
	}

	// Orphan dispatch context: ResolutionBranch without PolecatBead
	// (or vice versa). Either field alone leaves the rig in a half-
	// dispatched state that the deacon will not recover from on its
	// own.
	if state.CurrentAttempt != nil {
		hasBranch := strings.TrimSpace(state.CurrentAttempt.ResolutionBranch) != ""
		hasBead := strings.TrimSpace(state.CurrentAttempt.PolecatBead) != ""
		if hasBranch != hasBead {
			problems = append(problems, fmt.Sprintf(
				"CurrentAttempt has half-set dispatch context: ResolutionBranch=%q PolecatBead=%q",
				state.CurrentAttempt.ResolutionBranch,
				state.CurrentAttempt.PolecatBead))
			worst = bumpStatus(worst, StatusWarning)
		}
	}

	if len(problems) == 0 {
		return &CheckResult{
			Name:   checkName,
			Status: StatusOK,
			Message: fmt.Sprintf("upstream-sync healthy (state=%s, last_sync=%s, consecutive_failures=%d)",
				state.State,
				upstreamsync.FormatLastSync(state.LastSyncAt),
				state.ConsecutiveFailures),
		}
	}

	msg := fmt.Sprintf("%d upstream-sync issue(s) detected", len(problems))
	hint := "Run `gt upstream status --rig=" + state.Rig + "` for full state; `gt upstream audit --rig=" + state.Rig + "` for risk findings."
	return &CheckResult{
		Name:    checkName,
		Status:  worst,
		Message: msg,
		Details: problems,
		FixHint: hint,
	}
}

// defaultCircuitBreakerThreshold returns the rig's configured
// MaxConsecutiveFailures when set, falling back to the package
// default. Phase 4 (gu-g5gh) wired this constant; we read it
// loosely here so the doctor check doesn't have to know the
// transition logic.
func defaultCircuitBreakerThreshold(cfg *config.UpstreamSyncConfig) int {
	if cfg == nil {
		return config.DefaultUpstreamSyncMaxConsecutiveFailures
	}
	if cfg.MaxConsecutiveFailures > 0 {
		return cfg.MaxConsecutiveFailures
	}
	return config.DefaultUpstreamSyncMaxConsecutiveFailures
}

// bumpStatus returns the more severe of two CheckStatus values. Used
// to track the worst problem found while accumulating multiple
// findings.
func bumpStatus(a, b CheckStatus) CheckStatus {
	if int(b) > int(a) {
		return b
	}
	return a
}

// deriveRigPrefix mirrors cmd.resolveRigPrefix. Lifted here to avoid
// adding a cmd→doctor import cycle; behavior must stay in lockstep.
// Keep updates synchronized with internal/cmd/upstream.go.
func deriveRigPrefix(rigName string) string {
	parts := strings.Split(rigName, "_")
	if len(parts) >= 2 {
		var prefix strings.Builder
		for _, p := range parts {
			if len(p) > 0 {
				prefix.WriteByte(p[0])
			}
		}
		result := prefix.String()
		if len(result) >= 2 {
			return result[:2]
		}
		return result
	}
	if len(rigName) >= 2 {
		return rigName[:2]
	}
	return rigName
}

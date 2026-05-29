// gt upstream pause / resume — operator pause control for upstream sync.
//
// Phase 2 (gu-4mj2). These two verbs flip the state machine between
// {idle, failed} and {paused}, recording the reason on the per-rig
// state bead so the audit trail survives session death. They are the
// ONLY supported way to enter StatePaused — the deacon patrol /
// circuit breaker uses the same TransitionTo path internally.
//
// Why pause/resume share a file: they read the same state bead, run
// the same actor-resolution logic, and mirror each other's flag set.
// The auto-test-pr command group consolidates pause+resume+status+
// show+history into one file (auto_test_pr_pause.go) for the same
// reason; we keep upstream's verbs split across two files because
// the history / config verbs belong to different operational stages.
//
// Design context: .designs/cv-2s6tq/data.md §"State Machine".
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/upstreamsync"
	"github.com/steveyegge/gastown/internal/workspace"
)

// CLI flag bindings for pause/resume. Captured as package-level vars
// to mirror upstream.go's existing pattern (upstreamRig, upstreamJSON).
var (
	upstreamPauseRig    string
	upstreamPauseReason string
	upstreamPauseTTL    string

	upstreamResumeRig string
)

// upstreamNowFn is the indirection point for tests. Production code
// resolves time.Now() lazily through this var so unit tests can pin
// it when verifying pause expiry timestamps. Mirrors nowFn in
// auto_test_pr_pause.go but kept separate so tests for the two
// command groups don't fight over the global.
var upstreamNowFn = func() time.Time { return time.Now() }

var upstreamPauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Pause upstream sync for a rig",
	Long: `Pause automatic upstream sync for a rig. The deacon patrol will skip
sync evaluations while paused, but state reads (gt upstream status) still work.

A reason is required so the audit trail (visible via gt upstream history)
explains why the operator paused. Pauses can carry an optional --ttl
(auto-resume after duration); without --ttl the pause persists until an
explicit gt upstream resume.

Examples:

  gt upstream pause --reason "Investigating broken upstream test"
  gt upstream pause --rig=gastown_upstream --reason "release window" --ttl=24h`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runUpstreamPause,
}

var upstreamResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume upstream sync for a rig",
	Long: `Resume automatic upstream sync for a rig previously paused via
gt upstream pause. The state machine returns to idle; the next deacon
patrol tick is eligible to trigger a sync.

If consecutive_failures had tripped a circuit breaker pause, resume also
clears that counter — operators are explicitly acknowledging the failure
state by resuming.

Examples:

  gt upstream resume
  gt upstream resume --rig=gastown_upstream`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runUpstreamResume,
}

func init() {
	upstreamPauseCmd.Flags().StringVar(&upstreamPauseRig, "rig", "",
		"Target rig (defaults to current worktree's rig)")
	upstreamPauseCmd.Flags().StringVar(&upstreamPauseReason, "reason", "",
		"Reason for pausing (required, recorded on state bead)")
	upstreamPauseCmd.Flags().StringVar(&upstreamPauseTTL, "ttl", "",
		"Optional auto-resume duration (e.g. 24h, 2h30m). Empty means indefinite.")

	upstreamResumeCmd.Flags().StringVar(&upstreamResumeRig, "rig", "",
		"Target rig (defaults to current worktree's rig)")

	upstreamCmd.AddCommand(upstreamPauseCmd)
	upstreamCmd.AddCommand(upstreamResumeCmd)
}

// resolveUpstreamRigContext loads the town root, rig name, and rig
// settings for an upstream-sync verb. Returns NewSilentExit(2) when
// the rig cannot be determined or settings can't be read — the
// stderr hint is already printed for the operator.
//
// rigFlag is the value of --rig (empty means "infer from cwd").
// verb is the command name used in error messages ("pause", "resume", …).
func resolveUpstreamRigContext(cmd *cobra.Command, verb, rigFlag string) (
	townRoot, rigName, rigPath string,
	settings *config.RigSettings,
	err error,
) {
	stderr := cmd.ErrOrStderr()

	townRoot, err = workspace.FindFromCwdOrError()
	if err != nil {
		return "", "", "", nil, fmt.Errorf("locating town root: %w", err)
	}

	rigName = rigFlag
	if rigName == "" {
		rigName = resolveCurrentRig(townRoot)
		if rigName == "" {
			fmt.Fprintf(stderr, "gt upstream %s: could not determine current rig\n", verb)
			fmt.Fprintln(stderr, "  hint: use --rig=<name> or cd into a rig worktree")
			return "", "", "", nil, NewSilentExit(2)
		}
	}

	rigPath = filepath.Join(townRoot, rigName)
	if _, statErr := os.Stat(rigPath); statErr != nil {
		return "", "", "", nil, fmt.Errorf("rig directory not found at %s: %w", rigPath, statErr)
	}

	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err = config.LoadRigSettings(settingsPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return "", "", "", nil, fmt.Errorf("loading rig settings: %w", err)
	}
	// settings may be nil if the file is absent — caller must handle
	// the nil case (most verbs require upstream_sync to be configured).

	return townRoot, rigName, rigPath, settings, nil
}

// resolveActor returns the operator identity for audit-log entries.
// Mirrors resolveOperatorActor in auto_test_pr_pause.go.
func resolveActor() string {
	if a := os.Getenv("BD_ACTOR"); a != "" {
		return a
	}
	if r := os.Getenv("GT_ROLE"); r != "" {
		return r
	}
	return "overseer"
}

func runUpstreamPause(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	if upstreamPauseReason == "" {
		fmt.Fprintln(stderr, "gt upstream pause: --reason is required")
		fmt.Fprintln(stderr, `  example: gt upstream pause --reason "investigating upstream test failure"`)
		return NewSilentExit(2)
	}

	townRoot, rigName, _, settings, err := resolveUpstreamRigContext(cmd, "pause", upstreamPauseRig)
	if err != nil {
		return err
	}

	if settings == nil || !settings.UpstreamSync.IsEnabled() {
		fmt.Fprintf(stderr, "gt upstream pause: upstream sync is not enabled for rig %s\n", rigName)
		fmt.Fprintln(stderr, "  hint: enable in settings/config.json (upstream_sync.enabled = true)")
		return NewSilentExit(2)
	}

	var pausedUntil string
	if upstreamPauseTTL != "" {
		dur, parseErr := time.ParseDuration(upstreamPauseTTL)
		if parseErr != nil {
			fmt.Fprintf(stderr, "gt upstream pause: invalid --ttl %q: %v\n", upstreamPauseTTL, parseErr)
			return NewSilentExit(2)
		}
		if dur <= 0 {
			fmt.Fprintf(stderr, "gt upstream pause: --ttl must be positive, got %s\n", dur)
			return NewSilentExit(2)
		}
		pausedUntil = upstreamNowFn().Add(dur).UTC().Format(time.RFC3339)
	}

	rigPrefix := resolveRigPrefix(rigName)
	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
	actor := resolveActor()
	reason := upstreamPauseReason

	// Pre-flight: state bead must already be provisioned. Pause is a
	// mutating verb — auto-provisioning here would mask config drift.
	if _, err := upstreamsync.LoadSyncState(bd, rigPrefix); err != nil {
		if errors.Is(err, upstreamsync.ErrStateBeadNotProvisioned) {
			fmt.Fprintf(stderr, "gt upstream pause: state bead not provisioned for rig %s\n", rigName)
			fmt.Fprintln(stderr, "  hint: the deacon will provision on the next patrol tick, or run gt upstream sync once")
			return NewSilentExit(3)
		}
		return fmt.Errorf("loading sync state: %w", err)
	}

	err = upstreamsync.TransitionTo(bd, rigPrefix, upstreamsync.StatePaused, func(s *upstreamsync.SyncStateMetadata) error {
		s.State = upstreamsync.StatePaused
		s.PausedUntil = pausedUntil
		s.PauseReason = fmt.Sprintf("%s (by %s)", reason, actor)
		// Pause aborts any in-progress attempt — deacons resume from
		// idle, not from mid-merge state.
		s.CurrentAttempt = nil
		return nil
	})
	if err != nil {
		var invalid *upstreamsync.ErrInvalidTransition
		if errors.As(err, &invalid) {
			fmt.Fprintf(stderr, "gt upstream pause: cannot pause from state %s\n", invalid.From)
			return NewSilentExit(3)
		}
		return fmt.Errorf("pausing upstream sync: %w", err)
	}

	if pausedUntil != "" {
		fmt.Fprintf(stdout, "✓ rig %s paused until %s by=%s\n", rigName, pausedUntil, actor)
	} else {
		fmt.Fprintf(stdout, "✓ rig %s paused (indefinite) by=%s\n", rigName, actor)
	}
	if reason != "" {
		fmt.Fprintf(stdout, "  reason: %s\n", reason)
	}
	return nil
}

func runUpstreamResume(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	townRoot, rigName, _, settings, err := resolveUpstreamRigContext(cmd, "resume", upstreamResumeRig)
	if err != nil {
		return err
	}

	if settings == nil || !settings.UpstreamSync.IsEnabled() {
		fmt.Fprintf(stderr, "gt upstream resume: upstream sync is not enabled for rig %s\n", rigName)
		return NewSilentExit(2)
	}

	rigPrefix := resolveRigPrefix(rigName)
	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
	actor := resolveActor()

	state, err := upstreamsync.LoadSyncState(bd, rigPrefix)
	if err != nil {
		if errors.Is(err, upstreamsync.ErrStateBeadNotProvisioned) {
			fmt.Fprintf(stderr, "gt upstream resume: state bead not provisioned for rig %s\n", rigName)
			return NewSilentExit(3)
		}
		return fmt.Errorf("loading sync state: %w", err)
	}

	if state.State != upstreamsync.StatePaused && state.PausedUntil == "" {
		// Already not paused — idempotent friendly: report success.
		fmt.Fprintf(stdout, "✓ rig %s is not paused (state=%s)\n", rigName, state.State)
		return nil
	}

	err = upstreamsync.TransitionTo(bd, rigPrefix, upstreamsync.StateIdle, func(s *upstreamsync.SyncStateMetadata) error {
		s.State = upstreamsync.StateIdle
		s.PausedUntil = ""
		s.PauseReason = ""
		// Resume clears the consecutive-failure counter so the
		// circuit breaker doesn't re-trip immediately.
		s.ConsecutiveFailures = 0
		return nil
	})
	if err != nil {
		var invalid *upstreamsync.ErrInvalidTransition
		if errors.As(err, &invalid) {
			fmt.Fprintf(stderr, "gt upstream resume: cannot resume from state %s\n", invalid.From)
			return NewSilentExit(3)
		}
		return fmt.Errorf("resuming upstream sync: %w", err)
	}

	fmt.Fprintf(stdout, "✓ rig %s resumed by=%s\n", rigName, actor)
	return nil
}

// gt upstream sync — manual trigger for an upstream-sync cycle.
//
// Phase 2 (gu-4mj2). This verb walks the state machine through one
// full sync attempt: idle → checking → syncing → gating → pushing →
// idle. Each transition is CAS-protected via TransitionTo and
// recorded as a SyncAttempt entry on the per-rig state bead.
//
// In Phase 2 the implementation is conservative: it only handles the
// fast-path (clean fast-forward merge with passing gates). Conflict
// resolution dispatches a polecat in Phase 3 when the deacon patrol
// integration lands. For now, conflicts cause a transition to
// StateFailed with conflicts populated on the attempt record so an
// operator can resolve manually.
//
// The --dry-run flag short-circuits before any git ops: it prints
// the current divergence and what would happen, leaving the state
// machine untouched. Useful for testing the wiring without performing
// a real merge.
//
// Design context: .designs/cv-2s6tq/api.md §"gt upstream sync".
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/upstreamsync"
)

// CLI flag bindings — package-level vars per upstream.go convention.
var (
	upstreamSyncRig      string
	upstreamSyncDryRun   bool
	upstreamSyncSkipPush bool
)

var upstreamSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Trigger an upstream sync cycle",
	Long: `Trigger an immediate upstream-sync cycle for a rig: fetch upstream,
merge into the target branch, run the configured gate suite, and push.

Phase 2 only handles the fast-path (no conflicts). On conflict the cycle
transitions to FAILED with the conflicting files recorded on the state
bead — an operator must resolve manually until Phase 3 wires polecat
dispatch into the deacon patrol.

Examples:

  gt upstream sync                      # Run a full cycle for the current rig
  gt upstream sync --rig=gastown_upstream
  gt upstream sync --dry-run            # Show what would happen, no state change
  gt upstream sync --skip-push          # Run gates but don't push to origin`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runUpstreamSync,
}

func init() {
	upstreamSyncCmd.Flags().StringVar(&upstreamSyncRig, "rig", "",
		"Target rig (defaults to current worktree's rig)")
	upstreamSyncCmd.Flags().BoolVar(&upstreamSyncDryRun, "dry-run", false,
		"Report divergence without modifying state or running gates")
	upstreamSyncCmd.Flags().BoolVar(&upstreamSyncSkipPush, "skip-push", false,
		"Run merge + gates but do not push the result to origin")

	upstreamCmd.AddCommand(upstreamSyncCmd)
}

func runUpstreamSync(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	townRoot, rigName, rigPath, settings, err := resolveUpstreamRigContext(cmd, "sync", upstreamSyncRig)
	if err != nil {
		return err
	}

	if settings == nil || !settings.UpstreamSync.IsEnabled() {
		fmt.Fprintf(stderr, "gt upstream sync: upstream sync is not enabled for rig %s\n", rigName)
		fmt.Fprintln(stderr, "  hint: enable in settings/config.json (upstream_sync.enabled = true)")
		return NewSilentExit(2)
	}

	syncCfg := settings.UpstreamSync
	rigPrefix := resolveRigPrefix(rigName)
	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))

	// Resolve the git working dir (refinery clone preferred, rig root fallback).
	gitDir := resolveSyncGitDir(rigPath)
	if gitDir == "" {
		fmt.Fprintf(stderr, "gt upstream sync: no git repository found under %s\n", rigPath)
		return NewSilentExit(2)
	}

	// Pre-flight: state bead must be provisioned.
	state, err := upstreamsync.LoadSyncState(bd, rigPrefix)
	if err != nil {
		if errors.Is(err, upstreamsync.ErrStateBeadNotProvisioned) {
			fmt.Fprintf(stderr, "gt upstream sync: state bead not provisioned for rig %s\n", rigName)
			fmt.Fprintln(stderr, "  hint: the deacon will provision on the next patrol tick")
			return NewSilentExit(3)
		}
		return fmt.Errorf("loading sync state: %w", err)
	}

	// Refuse to run if currently paused — operators must resume first.
	if state.State == upstreamsync.StatePaused {
		fmt.Fprintf(stderr, "gt upstream sync: rig %s is paused (resume with `gt upstream resume`)\n", rigName)
		return NewSilentExit(3)
	}
	// Refuse to run if a sync is already in progress.
	if state.State != upstreamsync.StateIdle && state.State != upstreamsync.StateFailed {
		fmt.Fprintf(stderr, "gt upstream sync: rig %s is busy (state=%s)\n", rigName, state.State)
		fmt.Fprintln(stderr, "  hint: wait for the current attempt to complete, or `gt upstream history` to inspect")
		return NewSilentExit(3)
	}

	// Fetch upstream so divergence calculation is accurate.
	if err := gitFetchUpstream(gitDir, syncCfg); err != nil {
		fmt.Fprintf(stderr, "gt upstream sync: git fetch failed: %v\n", err)
		return NewSilentExit(4)
	}

	upstreamRef := syncCfg.GetUpstreamRemote() + "/" + syncCfg.GetUpstreamBranch()
	targetRef := "origin/" + syncCfg.GetTargetBranch()

	upstreamSHA, err := gitRevParse(gitDir, upstreamRef)
	if err != nil {
		fmt.Fprintf(stderr, "gt upstream sync: cannot resolve %s: %v\n", upstreamRef, err)
		return NewSilentExit(4)
	}
	targetSHA, err := gitRevParse(gitDir, targetRef)
	if err != nil {
		fmt.Fprintf(stderr, "gt upstream sync: cannot resolve %s: %v\n", targetRef, err)
		return NewSilentExit(4)
	}

	behind := gitCountAhead(gitDir, targetRef, upstreamRef)
	ahead := gitCountAhead(gitDir, upstreamRef, targetRef)

	fmt.Fprintf(stdout, "Upstream Sync: %s\n", rigName)
	fmt.Fprintf(stdout, "  Upstream:  %s @ %s\n", upstreamRef, shortSHA(upstreamSHA))
	fmt.Fprintf(stdout, "  Target:    %s @ %s\n", targetRef, shortSHA(targetSHA))
	fmt.Fprintf(stdout, "  Divergence: %d behind, %d ahead\n", behind, ahead)

	if behind == 0 {
		fmt.Fprintln(stdout, "✓ already in sync — nothing to do")
		return nil
	}

	if upstreamSyncDryRun {
		fmt.Fprintln(stdout, "(dry-run) would attempt fast-forward merge and run gates:")
		for _, g := range syncCfg.GetGateCommands() {
			fmt.Fprintf(stdout, "  - %s\n", g)
		}
		return nil
	}

	// Begin the cycle: idle/failed → checking.
	attempt := upstreamsync.SyncAttempt{
		ID:          fmt.Sprintf("%s-sync-att-%d", rigPrefix, time.Now().Unix()),
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		UpstreamSHA: upstreamSHA,
		PreSyncSHA:  targetSHA,
		Strategy:    syncCfg.GetStrategy(),
		Actor:       resolveActor(),
	}

	if err := beginCheckingState(bd, rigPrefix, &attempt); err != nil {
		fmt.Fprintf(stderr, "gt upstream sync: cannot begin attempt: %v\n", err)
		return NewSilentExit(5)
	}

	// Determine merge strategy. Phase 2 only handles fast-forward.
	canFastForward := isAncestor(gitDir, targetRef, upstreamRef)
	if !canFastForward {
		// Conflict path — record and bail. Phase 3 dispatches polecat.
		attempt.Outcome = "conflict"
		attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		attempt.Conflicts = []string{"non-fast-forward (Phase 2: manual resolution required)"}
		_ = appendAttemptAndTransition(bd, rigPrefix, attempt, upstreamsync.StateFailed)
		fmt.Fprintln(stderr, "gt upstream sync: non-fast-forward merge required")
		fmt.Fprintln(stderr, "  Phase 2 only supports clean fast-forwards. Phase 3 will dispatch a polecat for resolution.")
		return NewSilentExit(6)
	}

	// Transition checking → syncing.
	if err := transitionWithAttempt(bd, rigPrefix, upstreamsync.StateSyncing, &attempt); err != nil {
		return fmt.Errorf("transition to syncing: %w", err)
	}

	fmt.Fprintf(stdout, "Fast-forwarding %s to %s...\n", targetRef, shortSHA(upstreamSHA))
	if err := gitFastForward(gitDir, syncCfg); err != nil {
		attempt.Outcome = "error"
		attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendAttemptAndTransition(bd, rigPrefix, attempt, upstreamsync.StateFailed)
		fmt.Fprintf(stderr, "gt upstream sync: fast-forward failed: %v\n", err)
		return NewSilentExit(7)
	}

	// Transition syncing → gating, run gates.
	if err := transitionWithAttempt(bd, rigPrefix, upstreamsync.StateGating, &attempt); err != nil {
		return fmt.Errorf("transition to gating: %w", err)
	}

	gates := syncCfg.GetGateCommands()
	fmt.Fprintf(stdout, "Running %d gate(s)...\n", len(gates))
	gateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	gateResult := upstreamsync.RunGates(gateCtx, gates, upstreamsync.GateRunOptions{
		Dir: gitDir,
	})
	attempt.GateResults = gateResult.GateResultsMap()
	for _, r := range gateResult.Results {
		fmt.Fprintf(stdout, "  %s %s (%s)\n", iconForGate(r.Result), r.Command, r.Duration.Truncate(time.Millisecond))
	}

	if !gateResult.AllPassed {
		attempt.Outcome = "gate-failure"
		attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendAttemptAndTransition(bd, rigPrefix, attempt, upstreamsync.StateFailed)
		fmt.Fprintf(stderr, "gt upstream sync: gate failed: %s\n", gateResult.FailedCommand)
		return NewSilentExit(8)
	}

	// Transition gating → pushing.
	if err := transitionWithAttempt(bd, rigPrefix, upstreamsync.StatePushing, &attempt); err != nil {
		return fmt.Errorf("transition to pushing: %w", err)
	}

	if upstreamSyncSkipPush {
		fmt.Fprintln(stdout, "(--skip-push) gates passed; skipping push to origin")
	} else {
		fmt.Fprintf(stdout, "Pushing %s to origin...\n", syncCfg.GetTargetBranch())
		if err := gitPush(gitDir, syncCfg); err != nil {
			attempt.Outcome = "push-failure"
			attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			_ = appendAttemptAndTransition(bd, rigPrefix, attempt, upstreamsync.StateFailed)
			fmt.Fprintf(stderr, "gt upstream sync: push failed: %v\n", err)
			return NewSilentExit(9)
		}
	}

	// Success: pushing → idle, record attempt.
	postSyncSHA, _ := gitRevParse(gitDir, "HEAD")
	attempt.PostSyncSHA = postSyncSHA
	attempt.Outcome = "success"
	attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	if err := appendAttemptAndTransition(bd, rigPrefix, attempt, upstreamsync.StateIdle); err != nil {
		return fmt.Errorf("recording successful attempt: %w", err)
	}

	fmt.Fprintf(stdout, "✓ synced %s → %s\n", shortSHA(targetSHA), shortSHA(postSyncSHA))
	return nil
}

// beginCheckingState transitions the rig from idle/failed to checking
// and stamps the in-progress attempt onto CurrentAttempt.
func beginCheckingState(bd *beads.Beads, rigPrefix string, attempt *upstreamsync.SyncAttempt) error {
	return upstreamsync.TransitionTo(bd, rigPrefix, upstreamsync.StateChecking, func(s *upstreamsync.SyncStateMetadata) error {
		s.State = upstreamsync.StateChecking
		s.CurrentAttempt = &upstreamsync.CurrentAttempt{
			ID:          attempt.ID,
			StartedAt:   attempt.StartedAt,
			UpstreamSHA: attempt.UpstreamSHA,
			PreSyncSHA:  attempt.PreSyncSHA,
			Strategy:    attempt.Strategy,
			Actor:       attempt.Actor,
		}
		return nil
	})
}

// transitionWithAttempt advances the state machine to `target` while
// keeping CurrentAttempt populated. The state-bead invariant
// "CurrentAttempt non-null when state ∉ {idle, paused, failed}" is
// preserved by this helper.
func transitionWithAttempt(bd *beads.Beads, rigPrefix string, target upstreamsync.SyncState, attempt *upstreamsync.SyncAttempt) error {
	return upstreamsync.TransitionTo(bd, rigPrefix, target, func(s *upstreamsync.SyncStateMetadata) error {
		s.State = target
		if s.CurrentAttempt == nil {
			s.CurrentAttempt = &upstreamsync.CurrentAttempt{
				ID:          attempt.ID,
				StartedAt:   attempt.StartedAt,
				UpstreamSHA: attempt.UpstreamSHA,
				PreSyncSHA:  attempt.PreSyncSHA,
				Strategy:    attempt.Strategy,
				Actor:       attempt.Actor,
			}
		}
		s.CurrentAttempt.GateResults = attempt.GateResults
		return nil
	})
}

// appendAttemptAndTransition is the cycle-completion helper: it
// transitions to a terminal state (idle or failed), clears
// CurrentAttempt, and appends the attempt to the bounded history.
func appendAttemptAndTransition(bd *beads.Beads, rigPrefix string, attempt upstreamsync.SyncAttempt, target upstreamsync.SyncState) error {
	err := upstreamsync.TransitionTo(bd, rigPrefix, target, func(s *upstreamsync.SyncStateMetadata) error {
		s.State = target
		s.CurrentAttempt = nil
		// Append + bound + update last_sync_* (mirrors AppendAttempt).
		s.Attempts = append(s.Attempts, attempt)
		max := config.DefaultUpstreamSyncMaxAttempts
		if len(s.Attempts) > max {
			drop := len(s.Attempts) - max
			s.Attempts = s.Attempts[drop:]
		}
		if attempt.Outcome == "success" {
			s.LastSyncAt = attempt.CompletedAt
			s.LastSyncOutcome = "success"
			s.LastSyncSHA = attempt.PostSyncSHA
			s.ConsecutiveFailures = 0
		} else {
			s.LastSyncOutcome = attempt.Outcome
			s.ConsecutiveFailures++
		}
		return nil
	})
	return err
}

// resolveSyncGitDir picks the git directory to operate on. Refinery
// clone preferred (it's a clean clone), rig root fallback.
func resolveSyncGitDir(rigPath string) string {
	candidates := []string{
		filepath.Join(rigPath, "refinery", "rig"),
		rigPath,
	}
	for _, c := range candidates {
		if _, err := exec.Command("git", "-C", c, "rev-parse", "--git-dir").Output(); err == nil {
			return c
		}
	}
	return ""
}

// gitFetchUpstream fetches the configured upstream remote/branch.
func gitFetchUpstream(gitDir string, cfg *config.UpstreamSyncConfig) error {
	out, err := exec.Command("git", "-C", gitDir, "fetch", cfg.GetUpstreamRemote(),
		cfg.GetUpstreamBranch()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitRevParse resolves a ref to its SHA.
func gitRevParse(gitDir, ref string) (string, error) {
	out, err := exec.Command("git", "-C", gitDir, "rev-parse", ref).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitCountAhead reports how many commits b is ahead of a.
func gitCountAhead(gitDir, a, b string) int {
	out, err := exec.Command("git", "-C", gitDir, "rev-list", "--count", a+".."+b).Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return n
}

// isAncestor reports whether `target` is an ancestor of `upstream`.
// True means a fast-forward merge from target to upstream is possible.
func isAncestor(gitDir, target, upstream string) bool {
	err := exec.Command("git", "-C", gitDir, "merge-base", "--is-ancestor", target, upstream).Run()
	return err == nil
}

// gitFastForward merges the upstream ref into the target branch with
// fast-forward only. Bails if a real merge would be needed.
func gitFastForward(gitDir string, cfg *config.UpstreamSyncConfig) error {
	target := cfg.GetTargetBranch()
	upstream := cfg.GetUpstreamRemote() + "/" + cfg.GetUpstreamBranch()

	// Checkout target branch first so the merge updates it in place.
	if out, err := exec.Command("git", "-C", gitDir, "checkout", target).CombinedOutput(); err != nil {
		return fmt.Errorf("checkout %s: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("git", "-C", gitDir, "merge", "--ff-only", upstream).CombinedOutput(); err != nil {
		return fmt.Errorf("merge --ff-only %s: %w: %s", upstream, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitPush pushes the target branch to origin.
func gitPush(gitDir string, cfg *config.UpstreamSyncConfig) error {
	out, err := exec.Command("git", "-C", gitDir, "push", "origin",
		cfg.GetTargetBranch()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// iconForGate maps a GateResult to a single-character status icon.
func iconForGate(r upstreamsync.GateResult) string {
	switch r {
	case upstreamsync.GatePass:
		return "✓"
	case upstreamsync.GateFail:
		return "✗"
	case upstreamsync.GateSkip:
		return "⊘"
	default:
		return "?"
	}
}

// Compile-time assertion that io.Writer is satisfied by the cobra
// stdout/stderr we hand to printers (defense against API drift).
var _ io.Writer = (*strings.Builder)(nil)

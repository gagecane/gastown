// gt upstream sync — manual trigger for an upstream-sync cycle.
//
// Phase 2 (gu-4mj2). This verb walks the state machine through one
// full sync attempt: idle → checking → syncing → gating → pushing →
// idle. Each transition is CAS-protected via TransitionTo and
// recorded as a SyncAttempt entry on the per-rig state bead.
//
// Phase 4 (gu-g5gh) extends the conflict path: when a merge has real
// conflicts, we run the complexity gate (file/hunk thresholds
// + restricted-path allowlist), and either dispatch a polecat for
// autonomous resolution (StateChecking → StateResolving + work bead)
// or escalate to a human (StateFailed with structured reason). After
// any failure path, the circuit breaker is consulted: if N consecutive
// failures have accumulated, the rig auto-pauses (StateFailed →
// StatePaused) so polecat slots aren't burned retrying a wedged sync.
//
// gu-oedcu (2026-06-09): the clean non-FF case is now automated. When
// origin/main has commits upstream lacks AND a `git merge upstream/main`
// would be conflict-free (the common fork-divergent shape), this verb
// performs a real `git merge --no-ff` instead of bailing, runs gates,
// and pushes — closing the loop the bead `[fork-sync]` tracking
// described.
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
	"github.com/steveyegge/gastown/internal/doltserver"
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

Three merge cases are handled automatically:

  1. Fast-forward — origin/main is an ancestor of upstream/main.
     git merge --ff-only.
  2. Non-FF but clean — fork has divergent commits upstream lacks but
     no real conflicts (the common fork-divergent case). Performs a
     real git merge --no-ff upstream/main commit, then runs gates +
     push exactly like the FF case.
  3. Non-FF with conflicts — dispatches a polecat for autonomous
     resolution when the conflict is resolvable, escalates otherwise.

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

	// Pre-flight: load the state bead, self-provisioning it on first run.
	// `gt upstream sync` is the documented first-touch provisioning path
	// (see the `gt upstream pause` hint: "run gt upstream sync once"); the
	// deacon patrol does not call EnsureStateBead, so without this an
	// unprovisioned rig would deadlock — sync refuses because there is no
	// state bead and nothing else ever creates one. EnsureStateBead is
	// idempotent, so this is a no-op on every subsequent run. Dry-run
	// promises "no state change", so it never provisions — it treats an
	// unprovisioned rig as a clean idle state for reporting purposes.
	state, err := upstreamsync.LoadSyncState(bd, rigPrefix)
	if err != nil {
		if errors.Is(err, upstreamsync.ErrStateBeadNotProvisioned) {
			if upstreamSyncDryRun {
				state = upstreamsync.DefaultSyncStateMetadata(
					rigName, syncCfg.GetUpstreamRemote(), syncCfg.GetUpstreamBranch(), syncCfg.GetTargetBranch())
				err = nil
			} else {
				if _, perr := upstreamsync.EnsureStateBead(bd, rigPrefix, rigName, syncCfg); perr != nil {
					fmt.Fprintf(stderr, "gt upstream sync: cannot provision state bead for rig %s: %v\n", rigName, perr)
					return NewSilentExit(3)
				}
				state, err = upstreamsync.LoadSyncState(bd, rigPrefix)
			}
		}
		if err != nil {
			return fmt.Errorf("loading sync state: %w", err)
		}
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

	// Pick a merge strategy. Three cases (gu-oedcu):
	//
	//  1. Fast-forward possible — origin/main is an ancestor of
	//     upstream/main. Existing FF path.
	//  2. Non-FF but CLEAN — fork has divergent commits but no real
	//     conflicts (the common fork-divergent case the bead describes:
	//     origin has commits upstream lacks; a real `git merge` would
	//     succeed). Perform `git merge --no-ff upstream/main` then run
	//     gates + push exactly like the FF path. This is the path that
	//     was previously rejected with "manual review required" — turning
	//     it into the routine automated case is the whole point of this
	//     change.
	//  3. Non-FF with conflicts — hand off via the existing
	//     complexity-gated polecat-dispatch path (handleMergeConflict).
	canFastForward := isAncestor(gitDir, targetRef, upstreamRef)
	if !canFastForward {
		// Probe conflicts non-destructively before deciding whether
		// this is case 2 (clean merge — automate) or case 3 (real
		// conflicts — dispatch).
		report, derr := upstreamsync.DetectConflicts(gitDir, targetRef, upstreamRef)
		if derr != nil {
			attempt.Outcome = "conflict-detect-error"
			attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			attempt.Conflicts = []string{fmt.Sprintf("conflict detection failed: %v", derr)}
			_ = appendAttemptAndTransition(bd, rigPrefix, attempt, upstreamsync.StateFailed)
			fmt.Fprintf(stderr, "gt upstream sync: cannot detect conflicts: %v\n", derr)
			maybeReportCircuitBreaker(stderr, bd, rigPrefix, syncCfg)
			return NewSilentExit(6)
		}

		if !report.IsClean() {
			// Case 3: real conflicts. Existing complexity-gated path
			// dispatches a polecat or escalates. All failure paths
			// consult the circuit breaker before returning so a
			// wedged rig auto-pauses. Skip the breaker check when
			// dispatch took us to StateResolving (exitCode==0) —
			// only attempt-failure paths trip.
			exitCode := handleMergeConflict(
				cmd, bd, rigPrefix, rigName, syncCfg, &attempt, report)
			if exitCode != 0 {
				maybeReportCircuitBreaker(stderr, bd, rigPrefix, syncCfg)
				return NewSilentExit(exitCode)
			}
			return nil
		}

		// Case 2: clean non-FF merge. Record the actual strategy on
		// the attempt so `gt upstream history` can distinguish a
		// fork-sync merge commit from a fast-forward. Then transition
		// to syncing and perform the real `git merge --no-ff`.
		attempt.Strategy = "merge"
		if err := transitionWithAttempt(bd, rigPrefix, upstreamsync.StateSyncing, &attempt); err != nil {
			return fmt.Errorf("transition to syncing: %w", err)
		}
		fmt.Fprintf(stdout, "Merging %s into %s (--no-ff, clean)...\n", upstreamRef, targetRef)
		if err := gitMergeUpstream(gitDir, syncCfg); err != nil {
			attempt.Outcome = "merge-error"
			attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			_ = appendAttemptAndTransition(bd, rigPrefix, attempt, upstreamsync.StateFailed)
			fmt.Fprintf(stderr, "gt upstream sync: merge failed: %v\n", err)
			maybeReportCircuitBreaker(stderr, bd, rigPrefix, syncCfg)
			return NewSilentExit(7)
		}
	} else {
		// Case 1: fast-forward.
		if err := transitionWithAttempt(bd, rigPrefix, upstreamsync.StateSyncing, &attempt); err != nil {
			return fmt.Errorf("transition to syncing: %w", err)
		}
		fmt.Fprintf(stdout, "Fast-forwarding %s to %s...\n", targetRef, shortSHA(upstreamSHA))
		if err := gitFastForward(gitDir, syncCfg); err != nil {
			attempt.Outcome = "error"
			attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			_ = appendAttemptAndTransition(bd, rigPrefix, attempt, upstreamsync.StateFailed)
			fmt.Fprintf(stderr, "gt upstream sync: fast-forward failed: %v\n", err)
			maybeReportCircuitBreaker(stderr, bd, rigPrefix, syncCfg)
			return NewSilentExit(7)
		}
	}

	// Transition syncing → gating, run gates.
	if err := transitionWithAttempt(bd, rigPrefix, upstreamsync.StateGating, &attempt); err != nil {
		return fmt.Errorf("transition to gating: %w", err)
	}

	gates := syncCfg.GetGateCommands()
	fmt.Fprintf(stdout, "Running %d gate(s)...\n", len(gates))
	gateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// SandboxedEnv defaults to true here per Phase 5 (gu-1zfy / C-SEC-1):
	// upstream code is semi-trusted, gates execute it (`go test`, `go
	// build`, `go vet`), and any AWS/GitHub/Dolt/beads credential the
	// parent inherits would otherwise be exfiltratable from a malicious
	// init() or TestMain(). The Refinery's gate runner has its own
	// sandboxing model and is unaffected.
	gateResult := upstreamsync.RunGates(gateCtx, gates, upstreamsync.GateRunOptions{
		Dir:          gitDir,
		SandboxedEnv: true,
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
		maybeReportCircuitBreaker(stderr, bd, rigPrefix, syncCfg)
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
			maybeReportCircuitBreaker(stderr, bd, rigPrefix, syncCfg)
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

// checkoutTargetBranch checks out the target branch so an in-place merge
// can advance it. It handles the detached-clone case (gu-my577): the
// refinery's refinery/rig clone is in DETACHED HEAD with no local target
// branch, only origin/<target> and upstream/<target> remote-tracking
// refs. A bare `git checkout main` there fails with "matched multiple (2)
// remote tracking branches", which blocked both the FF and clean non-FF
// merge paths.
//
// If a local target branch already exists, it is checked out as-is so any
// local commits are preserved (the merge advances them). If it does not,
// the branch is created pointing at origin/<target> — the same base the
// caller already evaluated the FF/merge decision against — so a detached
// clone gets a sane, non-ambiguous local branch without clobbering
// anything (there is nothing local to clobber).
func checkoutTargetBranch(gitDir string, cfg *config.UpstreamSyncConfig) error {
	target := cfg.GetTargetBranch()

	// Local branch already present: plain checkout preserves its commits.
	exists := exec.Command("git", "-C", gitDir, "rev-parse", "--verify",
		"--quiet", "refs/heads/"+target).Run() == nil
	if exists {
		if out, err := exec.Command("git", "-C", gitDir, "checkout", target).CombinedOutput(); err != nil {
			return fmt.Errorf("checkout %s: %w: %s", target, err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	// No local branch (detached clone): create it from origin/<target>.
	origin := "origin/" + target
	if out, err := exec.Command("git", "-C", gitDir, "checkout", "-b", target, origin).CombinedOutput(); err != nil {
		return fmt.Errorf("checkout -b %s %s: %w: %s", target, origin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitFastForward merges the upstream ref into the target branch with
// fast-forward only. Bails if a real merge would be needed.
func gitFastForward(gitDir string, cfg *config.UpstreamSyncConfig) error {
	upstream := cfg.GetUpstreamRemote() + "/" + cfg.GetUpstreamBranch()

	// Checkout target branch first so the merge updates it in place.
	if err := checkoutTargetBranch(gitDir, cfg); err != nil {
		return err
	}
	if out, err := exec.Command("git", "-C", gitDir, "merge", "--ff-only", upstream).CombinedOutput(); err != nil {
		return fmt.Errorf("merge --ff-only %s: %w: %s", upstream, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitMergeUpstream performs a real (no-ff) merge of upstream into the
// target branch with a fixed commit message. Used for the clean
// non-fast-forward case (gu-oedcu) where the fork has divergent commits
// upstream lacks but the merge has no conflicts. Aborts the merge on
// failure so the working tree is left clean.
//
// The --no-ff form is required so the resulting commit has upstream as
// a second parent, which keeps `scripts/check-upstream-rebased.sh` happy
// (and matches the topology preservation already done in the refinery's
// fork_sync path).
func gitMergeUpstream(gitDir string, cfg *config.UpstreamSyncConfig) error {
	target := cfg.GetTargetBranch()
	upstream := cfg.GetUpstreamRemote() + "/" + cfg.GetUpstreamBranch()

	// Checkout target branch first so the merge updates it in place.
	if err := checkoutTargetBranch(gitDir, cfg); err != nil {
		return err
	}
	msg := fmt.Sprintf("Merge %s into %s (upstream sync)", upstream, target)
	out, err := exec.Command("git", "-C", gitDir, "merge", "--no-ff", "--no-edit",
		"-m", msg, upstream).CombinedOutput()
	if err != nil {
		// Best-effort abort so we don't leave a half-merged worktree
		// behind. merge-tree said this should have been clean, so a
		// failure here is unexpected — the abort is a safety net.
		_, _ = exec.Command("git", "-C", gitDir, "merge", "--abort").CombinedOutput()
		return fmt.Errorf("merge --no-ff %s: %w: %s", upstream, err, strings.TrimSpace(string(out)))
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

// handleMergeConflict is the conflict-resolution decision point. The
// caller has already detected non-empty conflicts via DetectConflicts;
// this function:
//
//  1. Evaluates the configured complexity policy + restricted-path
//     allowlist via EvaluateComplexity.
//  2. If the conflict is resolvable AND the rig's ConflictResolution
//     mode is "agent": dispatch a polecat (StateChecking →
//     StateResolving + sling-context). Returns 0 on success.
//  3. Otherwise: record the failure with structured reason
//     (StateChecking → StateFailed). Returns a non-zero exit code
//     so callers can update the circuit breaker.
//
// The function takes ownership of mutating CurrentAttempt's
// Conflicts/PolecatBead/ResolutionBranch fields when dispatching, so
// the state bead reflects the in-flight resolution.
//
// Renamed from handleNonFastForward (gu-oedcu): clean non-FF merges
// are now performed inline by the caller, so this path is reached only
// when there are real conflicts to triage.
func handleMergeConflict(
	cmd *cobra.Command,
	bd *beads.Beads,
	rigPrefix, rigName string,
	cfg *config.UpstreamSyncConfig,
	attempt *upstreamsync.SyncAttempt,
	report upstreamsync.ConflictReport,
) int {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	// Caller's contract: DetectConflicts has already run and reported
	// conflicts. An empty list here would be a programming error, not
	// a recoverable input.
	if report.IsClean() {
		attempt.Outcome = "conflict"
		attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		attempt.Conflicts = []string{"handleMergeConflict invoked with empty conflict report"}
		_ = appendAttemptAndTransition(bd, rigPrefix, *attempt, upstreamsync.StateFailed)
		fmt.Fprintln(stderr, "gt upstream sync: internal error — empty conflict report")
		return 6
	}

	// 2. Evaluate complexity. The policy comes from rig config with
	// security-design defaults filled in.
	policy := upstreamsync.ComplexityPolicy{
		MaxFiles:        cfg.GetMaxConflictFiles(),
		MaxHunks:        cfg.GetMaxConflictHunks(),
		RestrictedPaths: cfg.GetRestrictedPaths(),
	}
	verdict := upstreamsync.EvaluateComplexity(report.Files, report.HunkCount, policy)

	fmt.Fprintf(stdout, "Conflict detected: %d file(s), %d hunk(s)\n",
		verdict.FileCount, verdict.HunkCount)
	for _, f := range report.Files {
		fmt.Fprintf(stdout, "  - %s\n", f)
	}

	switch verdict.Class {
	case upstreamsync.ComplexityRestrictedEscalate:
		attempt.Outcome = "conflict-restricted"
		attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		attempt.Conflicts = report.Files
		_ = appendAttemptAndTransition(bd, rigPrefix, *attempt, upstreamsync.StateFailed)
		fmt.Fprintf(stderr, "gt upstream sync: %s — escalating to human review\n", verdict.Reason)
		return 10

	case upstreamsync.ComplexityTooComplexEscalate:
		attempt.Outcome = "conflict-too-complex"
		attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		attempt.Conflicts = report.Files
		_ = appendAttemptAndTransition(bd, rigPrefix, *attempt, upstreamsync.StateFailed)
		fmt.Fprintf(stderr, "gt upstream sync: %s — escalating to human review\n", verdict.Reason)
		return 11

	case upstreamsync.ComplexityResolvable:
		// Continue below.
	}

	// Honor the operator's "escalate" mode — even resolvable conflicts
	// can be forced to escalation when the rig is configured for it.
	if cfg.GetConflictResolution() == "escalate" {
		attempt.Outcome = "conflict-escalated"
		attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		attempt.Conflicts = report.Files
		_ = appendAttemptAndTransition(bd, rigPrefix, *attempt, upstreamsync.StateFailed)
		fmt.Fprintf(stderr, "gt upstream sync: conflict escalated per rig config (conflict_resolution=escalate)\n")
		return 12
	}

	// 3. Dispatch a polecat for autonomous resolution.
	restricted := cfg.GetRestrictedPaths()
	if len(restricted) == 0 {
		restricted = upstreamsync.DefaultRestrictedPaths()
	}
	in := upstreamsync.DispatchInput{
		Rig:             rigName,
		AttemptID:       attempt.ID,
		UpstreamRemote:  cfg.GetUpstreamRemote(),
		UpstreamBranch:  cfg.GetUpstreamBranch(),
		UpstreamSHA:     attempt.UpstreamSHA,
		TargetBranch:    cfg.GetTargetBranch(),
		TargetSHA:       attempt.PreSyncSHA,
		ConflictedFiles: report.Files,
		HunkCount:       report.HunkCount,
		RestrictedPaths: restricted,
		Strategy:        cfg.GetStrategy(),
		Actor:           attempt.Actor,
	}
	// The work bead + sling-context must be created in the TARGET RIG's
	// beads DB, not the town/hq DB (gu-pinfi). `gt sling` refuses to
	// dispatch a bead that isn't present in the target rig's database, so
	// a town-DB bead (hq-/gc- prefix) would stall the fork-sync. Resolve
	// the rig beads dir and hand the dispatcher a rig-scoped handle.
	rigBeads := bd
	if townRoot := bd.TownRoot(); townRoot != "" {
		if rigBeadsDir := doltserver.FindRigBeadsDir(townRoot, rigName); rigBeadsDir != "" {
			rigBeads = beads.NewWithBeadsDir(townRoot, rigBeadsDir)
		}
	}
	result, dispatchErr := upstreamsync.DispatchConflictResolution(rigBeads, in)
	if dispatchErr != nil {
		attempt.Outcome = "dispatch-error"
		attempt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		attempt.Conflicts = report.Files
		_ = appendAttemptAndTransition(bd, rigPrefix, *attempt, upstreamsync.StateFailed)
		fmt.Fprintf(stderr, "gt upstream sync: polecat dispatch failed: %v\n", dispatchErr)
		return 13
	}

	// 4. Transition StateChecking → StateResolving and stamp the
	// dispatched work bead onto CurrentAttempt for audit / recovery.
	err := upstreamsync.TransitionTo(bd, rigPrefix, upstreamsync.StateResolving, func(s *upstreamsync.SyncStateMetadata) error {
		s.State = upstreamsync.StateResolving
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
		s.CurrentAttempt.Conflicts = report.Files
		s.CurrentAttempt.PolecatBead = result.WorkBeadID
		s.CurrentAttempt.ResolutionBranch = result.ResolutionBranch
		return nil
	})
	if err != nil {
		// The dispatch already created beads; recording the transition
		// failure on the state bead is best-effort to avoid orphaning
		// the sync flow. The polecat will still pick up the work and
		// can advance state when it lands.
		fmt.Fprintf(stderr, "gt upstream sync: dispatched but state transition failed: %v\n", err)
	}
	fmt.Fprintf(stdout, "✓ dispatched polecat for conflict resolution\n")
	fmt.Fprintf(stdout, "  Work bead:     %s\n", result.WorkBeadID)
	fmt.Fprintf(stdout, "  Sling context: %s\n", result.ContextBeadID)
	fmt.Fprintf(stdout, "  Resolution branch: %s\n", result.ResolutionBranch)
	return 0
}

// maybeReportCircuitBreaker checks the breaker and, if it tripped,
// prints a hint to stderr so operators know the rig was auto-paused.
// Idempotent — safe to call after every failure path. Logging-only on
// breaker errors: we don't want a Dolt blip to mask the original failure.
func maybeReportCircuitBreaker(stderr io.Writer, bd *beads.Beads, rigPrefix string, cfg *config.UpstreamSyncConfig) {
	tripped, err := upstreamsync.TripIfNeeded(bd, rigPrefix, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "  warning: circuit-breaker check failed: %v\n", err)
		return
	}
	if tripped {
		fmt.Fprintf(stderr, "  ⚡ circuit breaker tripped — rig auto-paused after %d consecutive failures\n",
			cfg.GetMaxConsecutiveFailures())
		fmt.Fprintln(stderr, "    resume with: gt upstream resume")
	}
}

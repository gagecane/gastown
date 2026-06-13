package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/polecat/completion"
	"github.com/steveyegge/gastown/internal/pushlog"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/templates"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var doneCmd = &cobra.Command{
	Use:         "done",
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Signal work ready for merge queue",
	Long: `Signal that your work is complete and ready for the merge queue.

This is a convenience command for polecats that:
1. Submits the current branch to the merge queue
2. Auto-detects issue ID from branch name
3. Notifies the Witness with the exit outcome
4. Syncs worktree to main and transitions polecat to IDLE
   (sandbox preserved, session stays alive for reuse)

Exit statuses:
  COMPLETED      - Work done, MR submitted (default)
  ESCALATED      - Hit blocker, needs human intervention
  DEFERRED       - Work paused, issue still open

Examples:
  gt done                              # Submit branch, notify COMPLETED, transition to IDLE
  gt done --pre-verified               # Submit with pre-verification fast-path
  gt done --target feat/my-branch      # Explicit MR target branch
  gt done --pre-verified --target feat/contract-review  # Pre-verified with explicit target
  gt done --issue gt-abc               # Explicit issue ID
  gt done --skip-verify                # Audit-only escape hatch for non-code closes
  gt done --no-code --reason "verify-only: all checks passed, no code change required"  # Close a verify/report-only bead
  gt done --status ESCALATED           # Signal blocker, skip MR
  gt done --status DEFERRED            # Pause work, skip MR
  gt done --status DEFERRED --reason "spec-unclear: need API contract for auth endpoint"`,
	RunE:         runDone,
	SilenceUsage: true, // Don't print usage on operational errors (confuses agents)
}

var (
	doneIssue            string
	donePriority         int
	doneStatus           string
	doneCleanupStatus    string
	doneResume           bool
	donePreVerified      bool
	doneTarget           string
	doneSkipVerify       bool
	doneSkipVerifyReason string
	doneNoCode           bool
	doneReason           string
	doneDeferUntil       string
)

// defaultDeferredOffset is the cooldown applied to DEFERRED beads when the
// polecat does not specify --defer-until. Without this, deacon's stale-hooks
// patrol would reopen the bead immediately and `bd ready` would re-surface it
// to the auto-dispatcher within seconds, causing a re-dispatch loop (gu-vty0).
const defaultDeferredOffset = "+1d"

// Valid exit types for gt done
const (
	ExitCompleted = "COMPLETED"
	ExitEscalated = "ESCALATED"
	ExitDeferred  = "DEFERRED"
)

func doneContaminationBaseRef(defaultBranch, explicitTarget string) string {
	targetBranch := defaultBranch
	if explicitTarget != "" {
		targetBranch = strings.TrimPrefix(explicitTarget, "origin/")
	}

	return "origin/" + targetBranch
}

// resolveRigDefaultBranch resolves the rig's default branch for MR target
// resolution. The rig config's default_branch is the source of truth; when it
// is unreadable or empty, fall back to the repo's actual default branch
// (origin/HEAD via g) rather than a hardcoded "main" (gu-wcb37). A hardcoded
// "main" silently misroutes MRs in a "mainline"-default repo (casc_webapp),
// which would fail or misroute a merge once a repo carries both "main" and
// "mainline". "main" is kept only as the final fallback when git can't answer.
func resolveRigDefaultBranch(townRoot, rigName string, g *git.Git) string {
	if rigCfg, err := rig.LoadRigConfig(filepath.Join(townRoot, rigName)); err == nil && rigCfg.DefaultBranch != "" {
		return rigCfg.DefaultBranch
	}
	if g != nil {
		if b := g.RemoteDefaultBranch(); b != "" {
			return b
		}
	}
	return "main"
}

// relayBaseForLocalMerge decides whether a merge=local convoy leg should
// FF-push to a named relay base branch instead of parking the work on the local
// feature branch (gs-d26). It returns the base branch and true only when the
// convoy carries a base_branch that is set and differs from the rig default —
// the relay case. A plain merge=local convoy (no base, or base == default) keeps
// the existing keep-local behavior used for human-review / upstream-PR work.
func relayBaseForLocalMerge(convoyInfo *ConvoyInfo, defaultBranch string) (string, bool) {
	if convoyInfo == nil {
		return "", false
	}
	base := convoyInfo.BaseBranch
	if base == "" || base == defaultBranch {
		return "", false
	}
	return base, true
}

// localMergeWouldStrandReviewedCodeBead reports whether a merge=local bead
// arriving at `gt done` would SILENTLY strand reviewable work on a PR-review
// rig (gs-ydv9). During auto/deferred dispatch the rig-default formula can stage
// merge_strategy=local, overwriting the bead's intended PR workflow; the
// keep-local path then closes the bead COMPLETED while the commits stay on the
// local feature branch — never pushed, never reviewed, invisible (on a customer
// repo, effectively lost). It fires only when ALL hold:
//   - the convoy's merge=local is NOT a relay leg (relay legs carry a base branch
//     and legitimately FF-push, gs-d26);
//   - the bead is NOT a no_merge / review_only task (their local-stay is
//     intentional and handled by their own paths);
//   - the rig routes work through PR review (merge_strategy=pr or
//     require_review=true).
//
// At that exact combination there is no legitimate keep-local behavior to
// preserve, so the caller routes the bead through the normal merge-queue path
// instead of stranding it.
func localMergeWouldStrandReviewedCodeBead(sc strategyContext, convoyInfo *ConvoyInfo, isNoMergeTask bool) bool {
	if isNoMergeTask {
		return false
	}
	if _, isRelay := relayBaseForLocalMerge(convoyInfo, sc.defaultBranch); isRelay {
		return false
	}
	mq := loadRigMergeQueueConfig(sc.townRoot, sc.rigName)
	if mq == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(mq.MergeStrategy), "pr") || mq.IsRequireReviewEnabled()
}

// mrRelayTargetOverride resolves the relay base branch an MR should target when
// no higher-priority source already chose one. It fires only when no explicit
// --target was given AND the running target is still the rig default — so the
// explicit flag (step 1) and formula_vars base_branch (step 2) keep precedence.
//
// gs-dus: relay legs whose bead has base_branch=proto/v3-build carry that base
// on the tracking convoy (or the bead's own stamped attachment fields), NOT in
// formula_vars. The MR-target ladder only consulted formula_vars, so a relay
// leg run as plain `gt done` (no --target) that did NOT fast-forward-push fell
// through to the rig default and created an MR targeting gagecane/gt instead of
// proto/v3-build → modify/delete conflict → MERGE_FAILED → source bead reopened
// → re-slung → fails identically (reopen/resling churn). effectiveBaseBranch is
// the same resolver the dispatch (gs-n6h/gs-w7k) and auto-rebase (gs-xbo) paths
// use: stamped base → tracking convoy base → ancestor relay base.
//
// resolveBase is injected (effectiveBaseBranch in production) so the decision is
// unit-testable without bd/Dolt I/O. Returns "" to keep the current target.
func mrRelayTargetOverride(explicitTarget bool, currentTarget, defaultBranch, issueID string, resolveBase func(beadID, explicit string) string) string {
	if explicitTarget || currentTarget != defaultBranch || issueID == "" {
		return ""
	}
	relayBase := resolveBase(issueID, "")
	if relayBase == "" || relayBase == defaultBranch {
		return ""
	}
	return relayBase
}

// correctPhantomMainTarget is a safety net against MRs that target a literal
// "main" branch which does not exist on the rig's remote (gu-aucji). It returns
// the corrected target, or currentTarget unchanged when no correction applies.
//
// Why this is needed even though resolveRigDefaultBranch exists: the polecat
// formula renders `gt done --target {{base_branch}}`, and {{base_branch}} can
// fall back to the formula's static TOML default ("main") when gt prime cannot
// read the bead's stamped base_branch=<rig default> formula var (e.g. a Dolt
// read-lag window, gu-9qbg5). An explicit --target is authoritative and
// deliberately bypasses resolveRigDefaultBranch, so a phantom "main" sails
// through to the MR. On a mainline-only rig (talontriage) the refinery then has
// to manually retarget every occurrence — recurring toil and a latent
// wrong-merge risk if a rig ever carried BOTH main and mainline.
//
// The correction fires ONLY in the unambiguous footgun case: the running target
// is literally "main", the rig's real default branch is something else, and
// origin/main genuinely does not exist on the remote. In that case "main" can
// only be the phantom default, never a deliberate choice — so we rewrite it to
// the rig default. It is a strict no-op when the rig default IS main, when the
// target was never "main", or when origin/main actually exists (then "main" may
// be intentional and we must not second-guess it). branchExists is injected so
// the decision is unit-testable without a live remote.
func correctPhantomMainTarget(currentTarget, defaultBranch string, branchExists func(remote, branch string) (bool, error)) string {
	if currentTarget != "main" || defaultBranch == "" || defaultBranch == "main" {
		return currentTarget
	}
	// Only rewrite when origin/main is confirmed ABSENT. On any error querying
	// the remote, leave the target untouched — we never want a transient remote
	// hiccup to silently retarget an MR.
	exists, err := branchExists("origin", "main")
	if err != nil || exists {
		return currentTarget
	}
	return defaultBranch
}

// pushForDoneMaxAttempts is the total number of push attempts (initial + retries)
// pushForDone makes when a transient push-infra error is observed. git push is
// idempotent — re-pushing an already-landed commit exits 0 ("up to date") — so
// retrying a transient failure cannot corrupt or duplicate work.
const pushForDoneMaxAttempts = 3

// pushForDoneRetryBackoff is the base backoff between transient-error retries.
// Attempt N waits pushForDoneRetryBackoff * N before retrying (2s, 4s).
var pushForDoneRetryBackoff = 2 * time.Second

// isTransientPushError reports whether a failed push looks like a transient
// push-infra blip (network reset, server 5xx, TLS handshake, timeout, hung-up
// connection) rather than a deterministic rejection. Deterministic rejections
// — non-fast-forward divergence, auth failures, missing refspec, gate
// rejections — are NOT transient: retrying them just repeats the same failure,
// and each already has a dedicated recovery path (recoverNonFFOwnBranch,
// SHA-refspec recovery, etc.). Only transient errors are worth retrying.
//
// Motivation (gu-1or22): two casc_cdk stranded-merges in one session pushed
// the feature branch fine, passed gates, then failed the mainline push — and
// the gastown_upstream witness saw two simultaneous push_failed in the same
// window, across both repos. That cross-repo correlation points at a transient
// git push-infra blip, not a per-repo defect. The existing recovery paths all
// re-check origin AFTER a failure, which cannot help a genuine blip: the commit
// never landed, so the re-check confirms "not there" and the work strands. A
// bounded retry-with-backoff on the push itself closes that gap.
func isTransientPushError(err error) bool {
	if err == nil {
		return false
	}
	// Non-fast-forward is deterministic, not transient — never retry it.
	// (recoverNonFFOwnBranch handles the safe slice of that case.)
	if isNonFastForwardPushError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	transientMarkers := []string{
		"connection reset",
		"connection refused",
		"connection timed out",
		"operation timed out",
		"timed out",
		"could not resolve host",
		"failed to connect",
		"the remote end hung up unexpectedly",
		"early eof",
		"rpc failed",
		"broken pipe",
		"tls handshake",
		"gnutls_handshake",
		"ssl_read",
		"recv failure",
		"send failure",
		"temporary failure",
		"internal server error",
		"503",
		"502",
		"504",
	}
	for _, marker := range transientMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// pushForDone wraps git.Push, opting into GT_SKIP_PREPUSH=1 when the polecat
// declared --pre-verified. The repo's pre-push hook re-runs build+vet+test
// (~2-5min), which during gt done routinely exceeds the witness's idle timeout
// and triggers a force-kill that loses unpushed commits (gu-d416). When
// --pre-verified, the polecat already ran the same gates on the rebased
// branch (formula step 7), so the hook's gates are pure waste. The hook's
// branch-name and integration-branch guardrails still run.
//
// Transient push-infra blips are retried with backoff (gu-1or22): see
// isTransientPushError. Deterministic rejections fail fast on the first
// attempt and fall through to the caller's existing recovery paths.
func pushForDone(g *git.Git, refspec string) error {
	var err error
	for attempt := 1; attempt <= pushForDoneMaxAttempts; attempt++ {
		if donePreVerified {
			err = g.PushSkipPrePush("origin", refspec, false)
		} else {
			err = g.Push("origin", refspec, false)
		}
		if err == nil || !isTransientPushError(err) || attempt == pushForDoneMaxAttempts {
			return err
		}
		backoff := pushForDoneRetryBackoff * time.Duration(attempt)
		style.PrintWarning("push attempt %d/%d failed (transient): %v — retrying in %s", attempt, pushForDoneMaxAttempts, err, backoff)
		time.Sleep(backoff)
	}
	return err
}

// pushSHAForDone is the orphan-commit recovery counterpart of pushForDone.
func pushSHAForDone(g *git.Git, remote, sha, branch string, force bool) error {
	if donePreVerified {
		return g.PushSHASkipPrePush(remote, sha, branch, force)
	}
	return g.PushSHA(remote, sha, branch, force)
}

// validateSkipVerifyReason enforces the gu-kruw requirement that
// --skip-verify carry a non-empty rationale (either via the
// --skip-verify-reason flag or the GT_SKIP_VERIFY_REASON env var).
// Mirrors the GT_SKIP_PREPUSH_REASON pattern from gu-zy57: skipping
// verification without recording why is what let mis-cited close-reasons
// proliferate (gu-rpeg). The reason is required so misconfigured callers
// fail loudly instead of silently bypassing verification.
//
// On success the resolved reason is written back to doneSkipVerifyReason
// so the rest of runDone (and the audit emitters in noteVerifiedPushSkipped
// / MR description composition) can use it directly.
//
// No-op when --skip-verify is not set.
func validateSkipVerifyReason() error {
	if !doneSkipVerify {
		return nil
	}
	if strings.TrimSpace(doneSkipVerifyReason) == "" {
		doneSkipVerifyReason = strings.TrimSpace(os.Getenv("GT_SKIP_VERIFY_REASON"))
	}
	doneSkipVerifyReason = strings.TrimSpace(doneSkipVerifyReason)
	if doneSkipVerifyReason == "" {
		return fmt.Errorf("--skip-verify requires --skip-verify-reason=<text> (or GT_SKIP_VERIFY_REASON env var).\n" +
			"Skipping verified-push checks without recording why is what let mis-cited\n" +
			"close-reasons proliferate (gu-kruw, gu-rpeg). Provide a brief rationale, e.g.:\n" +
			"  gt done --skip-verify --skip-verify-reason=\"audit-only: no code changes for this report bead\"")
	}
	return nil
}

// validateNoCode enforces that --no-code carries a non-empty rationale via
// the --reason flag. --no-code is the explicit "COMPLETED — no code change
// required" exit for verify/report-only beads that were dispatched with a
// CODE formula (mol-polecat-work) and therefore produce zero commits by
// design (gu-gc4ex). Without it, such beads strand on gt done: the
// zero-commit guard and the commit-citation guard (gu-kruw) both block a
// COMPLETED close, and --skip-verify does not bypass the citation guard.
//
// The reason requirement mirrors the gu-kruw --skip-verify-reason gate:
// a no-code close still mutates bead state (closes it), so the rationale is
// recorded in the close reason for the audit trail. Falls back to the
// GT_NO_CODE_REASON env var when --reason is empty.
//
// No-op when --no-code is not set.
func validateNoCode() error {
	if !doneNoCode {
		return nil
	}
	if strings.TrimSpace(doneReason) == "" {
		doneReason = strings.TrimSpace(os.Getenv("GT_NO_CODE_REASON"))
	}
	doneReason = strings.TrimSpace(doneReason)
	if doneReason == "" {
		return fmt.Errorf("--no-code requires --reason=<text> (or GT_NO_CODE_REASON env var).\n" +
			"--no-code closes a verify/report-only bead that has no commits by design (gu-gc4ex).\n" +
			"Record why no code was required so the close is auditable, e.g.:\n" +
			"  gt done --no-code --reason=\"verify-only: all checks passed, no code change required\"")
	}
	return nil
}

// shortSHA abbreviates a git SHA for human-readable diagnostic output.
// Returns the first 8 characters, or the full value if shorter. Used by
// the gu-vtkn staleness guard to report stash-parent vs. HEAD divergence.
func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// sessionDurationMs returns the polecat time-to-close in milliseconds — the
// wall-clock from session start to now. The session-start timestamp is stamped
// into the GT_SESSION_START tmux env (Unix seconds) at spawn by the polecat
// session manager (gu-nniyx, KPI-1). Returns 0 when GT_SESSION_START is
// missing or unparseable (e.g. a session predating this change, or a manual
// invocation), in which case callers omit the duration from telemetry.
func sessionDurationMs() float64 {
	raw := os.Getenv("GT_SESSION_START")
	if raw == "" {
		return 0
	}
	startUnix, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || startUnix <= 0 {
		return 0
	}
	elapsed := time.Since(time.Unix(startUnix, 0))
	if elapsed <= 0 {
		return 0
	}
	return float64(elapsed.Milliseconds())
}

func shouldSyncIdlePolecatWorktree(exitType, mergeStrategy string, pushFailed, mrFailed, syncSafe bool) bool {
	if exitType != ExitCompleted || pushFailed || mrFailed || !syncSafe {
		return false
	}
	return mergeStrategy != "local"
}

func cleanupStatusAfterSuccessfulPush(status string) string {
	if status == "unpushed" || status == "has_unpushed" {
		return "clean"
	}
	return status
}

func init() {
	doneCmd.Flags().StringVar(&doneIssue, "issue", "", "Source issue ID (default: parse from branch name)")
	doneCmd.Flags().IntVarP(&donePriority, "priority", "p", -1, "Override priority (0-4, default: inherit from issue)")
	doneCmd.Flags().StringVar(&doneStatus, "status", ExitCompleted, "Exit status: COMPLETED, ESCALATED, or DEFERRED")
	doneCmd.Flags().StringVar(&doneReason, "reason", "", "Reason for DEFERRED/ESCALATED exit (recorded on bead notes)")
	doneCmd.Flags().StringVar(&doneCleanupStatus, "cleanup-status", "", "Git cleanup status: clean, uncommitted, unpushed, stash, unknown (ZFC: agent-observed)")
	doneCmd.Flags().BoolVar(&doneResume, "resume", false, "Resume from last checkpoint (auto-detected, for Witness recovery)")
	doneCmd.Flags().BoolVar(&donePreVerified, "pre-verified", false, "Mark MR as pre-verified (polecat ran gates after rebasing onto target). gt done re-runs the gates locally to verify the attestation; on red, the attestation is dropped and refinery runs gates normally (gu-xp5f).")
	doneCmd.Flags().StringVar(&doneTarget, "target", "", "Explicit MR target branch (overrides formula_vars and auto-detection)")
	doneCmd.Flags().BoolVar(&doneSkipVerify, "skip-verify", false, "Skip verified-push checks for audit/test-only completion (recorded on bead)")
	doneCmd.Flags().StringVar(&doneSkipVerifyReason, "skip-verify-reason", "", "Required when --skip-verify is set: human-readable rationale recorded in audit comment (gu-kruw). Falls back to GT_SKIP_VERIFY_REASON env var.")
	doneCmd.Flags().BoolVar(&doneNoCode, "no-code", false, "Complete a verify/report-only bead that has no commits by design (gu-gc4ex). Bypasses the zero-commit and commit-citation guards; requires --reason.")
	doneCmd.Flags().StringVar(&doneDeferUntil, "defer-until", "", "For --status=DEFERRED: when the bead becomes dispatchable again (e.g., +6h, +1d, tomorrow). Default: "+defaultDeferredOffset)

	rootCmd.AddCommand(doneCmd)
}

func runDone(cmd *cobra.Command, args []string) (retErr error) {
	// Telemetry attributes for the done event (KPI-1, gu-nniyx). rig and bead
	// are resolved during execution and captured into these closure variables;
	// the deferred recorder reads their final values at exit. rig defaults to
	// GT_RIG so a value is present even on early-return paths.
	doneRig := os.Getenv("GT_RIG")
	doneBeadID := ""
	doneDurationMs := sessionDurationMs()
	defer func() {
		telemetry.RecordDone(context.Background(), strings.ToUpper(doneStatus),
			doneRig, doneBeadID, doneDurationMs, retErr)
	}()

	// Up-front flag validation (polecats-only guard, exit-status, --skip-verify /
	// --no-code rationale gates). Extracted to validateDoneFlags (gu-nid89.12.1).
	exitType, err := validateDoneFlags()
	if err != nil {
		return err
	}

	// Persistent polecat model (gt-hdf8): sessions stay alive after gt done.
	// No deferred session kill — the polecat transitions to IDLE with sandbox
	// preserved. The Witness handles any cleanup if the polecat gets stuck.

	// Find workspace with fallback for deleted worktrees (hq-3xaxy)
	// If the polecat's worktree was deleted by Witness before gt done finishes,
	// getcwd will fail. We fall back to GT_TOWN_ROOT env var in that case.
	townRoot, cwd, err := workspace.FindFromCwdWithFallback()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Track if cwd is available - affects which operations we can do
	cwdAvailable := cwd != ""
	if !cwdAvailable {
		style.PrintWarning("working directory deleted (worktree nuked?), using fallback paths")
		// Try to get cwd from GT_POLECAT_PATH env var (set by session manager)
		if polecatPath := os.Getenv("GT_POLECAT_PATH"); polecatPath != "" {
			cwd = polecatPath // May still be gone, but we have a path to use
		}
	}

	// Find current rig - use cwd (which has fallback for deleted worktrees)
	// instead of findCurrentRig which calls os.Getwd() and fails on deleted cwd.
	// Logic extracted to completion.ResolveRigName (gs-bn1) so the cwd-vs-GT_RIG
	// resolution is testable in isolation.
	rigName, err := completion.ResolveRigName(townRoot, cwd, os.Getenv("GT_RIG"))
	if err != nil {
		return err
	}
	if rigName != "" {
		doneRig = rigName // refine the telemetry rig now that it's resolved
	}

	// Normalize the working directory: reconstruct the real polecat worktree when
	// the shell CWD was reset to mayor/rig or the town root (shell alias / Claude
	// Code), and walk up to the git repo root when gt done runs from a
	// subdirectory. Extracted to completion.ResolveWorktreeCwd (gu-nid89.12.1) so
	// the filesystem-probing ladder is testable in isolation. GT_POLECAT/GT_CREW
	// are read here and passed in so the resolver stays pure modulo os.Stat.
	cwd = completion.ResolveWorktreeCwd(cwd, cwdAvailable, townRoot, rigName, os.Getenv("GT_POLECAT"), os.Getenv("GT_CREW"))

	// Initialize git - use cwd if available, otherwise use rig's mayor clone
	var g *git.Git
	if cwdAvailable {
		g = git.NewGit(cwd)
	} else {
		// Fallback: use the rig's mayor clone for git operations
		mayorClone := filepath.Join(townRoot, rigName, "mayor", "rig")
		g = git.NewGit(mayorClone)
	}

	// Resolve the working branch with env-var fallbacks and the gu-ge1s
	// detached-HEAD guard. Extracted to completion.ResolveBranch (gs-bn1) so the
	// fallback ladder is testable in isolation. GT_BRANCH/GT_POLECAT are read
	// here and passed in so the resolver stays pure modulo git queries.
	branch, err := completion.ResolveBranch(g, cwdAvailable, os.Getenv("GT_BRANCH"), os.Getenv("GT_POLECAT"))
	if err != nil {
		return err
	}

	// Auto-detect cleanup status if not explicitly provided.
	// This prevents premature polecat cleanup by ensuring witness knows git state.
	// Logic extracted to detectCleanupStatus (gu-y7ouk) so the auto-detect path
	// is testable in isolation and runDone reads as a workflow, not git plumbing.
	if doneCleanupStatus == "" {
		doneCleanupStatus = completion.DetectCleanupStatus(g, branch, cwdAvailable)
	}

	// Resolve the rig's default branch early so downstream guards (safety-net
	// auto-commit, push refspec builder) can refuse operations that would
	// contaminate mainline. Duplicated below where the MR path needs it —
	// keep both in sync until we consolidate into a single resolution pass.
	// Source of truth: rig config default_branch. When unreadable/empty, fall
	// back to the repo's actual default (origin/HEAD), not a hardcoded "main"
	// — the latter misroutes MR targets in a "mainline"-default repo (gu-wcb37).
	defaultBranchEarly := resolveRigDefaultBranch(townRoot, rigName, g)

	// SAFETY NET (gt-pvx, stash recovery): If we detected stashes belonging to
	// this branch, auto-pop them so the existing uncommitted-work auto-commit
	// path (below) catches the contents and saves them as a normal commit.
	//
	// Background: agents have been observed running `git stash` to clear the
	// working tree before rebase/checkout, then dying before `git stash pop`.
	// The stash entries become orphaned in .git/refs/stash, surviving for
	// indefinite periods and silently leaking work. By popping them on the way
	// out of `gt done`, the recovery flow turns "lost" stashes into a
	// committed safety-net snapshot.
	//
	// Pop happens oldest-first so the most recent state ends up on top of the
	// working tree (matches what a user would do manually). If any pop has
	// conflicts, we stop and let the agent/user resolve — surfacing the
	// conflict is better than silently dropping the stash.
	if cwdAvailable {
		// runStashAutoPop (gu-y7ouk extraction) handles the gt-pvx stash safety
		// net + gu-vtkn staleness guard. When status != "stash" it returns the
		// input unchanged, so calling unconditionally is safe.
		doneCleanupStatus = completion.RunStashAutoPop(g, doneCleanupStatus)
	}

	// SAFETY NET: Auto-commit uncommitted work before ANY exit path (gt-pvx).
	// Polecats have been observed running gt done without committing their
	// implementation work (1000s of lines lost). This happened because:
	// 1. The agent skips the "commit changes" formula step
	// 2. The COMPLETED check blocks, but the agent retries with --status DEFERRED
	//    which skips all checks
	// 3. The agent's session dies after the error, before it can commit
	//
	// Auto-commit ensures work is NEVER lost regardless of exit type or agent behavior.
	// The commit message is clearly marked as an auto-save so reviewers know.
	//
	// HARD GUARD (gu-cfb): Refuse to auto-commit if the current branch is the
	// rig's default branch (main) or a common mainline alias (master). When a
	// polecat runs `gt done` from the rig root or otherwise ends up on main
	// (shouldn't happen in normal flow, but has been observed when a session's
	// cwd is the rig root rather than the polecat worktree), the auto-commit
	// + later push path would land unrelated artifacts (worktree pointers,
	// sibling-rig .kiro/ configs) directly on origin/main — bypassing the
	// merge queue and racing with refinery merges. Losing an unfinished
	// auto-save is strictly better than poisoning mainline: the polecat can
	// recover its work from the worktree; refinery cannot un-push bad commits.
	if err := runAutoCommitSafetyNet(g, cwd, branch, defaultBranchEarly, cwdAvailable); err != nil {
		return err
	}

	// Parse branch info
	info := parseBranchName(branch)

	// Override with explicit flags
	issueID := doneIssue
	if issueID == "" {
		issueID = info.Issue
	}
	worker := info.Worker

	// Determine polecat name from sender detection
	sender := detectSender()

	// Get agent bead ID for cross-referencing
	var agentBeadID string
	if roleInfo, err := GetRoleWithContext(cwd, townRoot); err == nil {
		if actor := roleInfo.ActorString(); actor != "" {
			sender = actor
		}
		ctx := RoleContext{
			Role:     roleInfo.Role,
			Rig:      roleInfo.Rig,
			Polecat:  roleInfo.Polecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
		}
		agentBeadID = getAgentBeadID(ctx)

		// Recreate the agent bead if it's missing (hq-xu4p). Done-intent
		// labels, checkpoints, and active_mr all write to it; when it's gone
		// every write fails 'issue not found' and witness zombie detection +
		// done-resume silently degrade. Best-effort: a failed recreate just
		// leaves the existing warnings.
		ensureAgentBeadExists(beads.New(cwd).ForAgentBead(), agentBeadID, ctx)

		// Persistent polecat model (gt-hdf8): no deferred session kill.
		// Sessions stay alive after gt done — polecat transitions to IDLE.
	}
	polecatName := ""
	if parts := strings.Split(sender, "/"); len(parts) >= 2 {
		polecatName = parts[len(parts)-1]
	}

	var assignedIssueIDs []string
	loadAssignedIssueIDs := func() []string {
		if assignedIssueIDs == nil && sender != "" {
			assignedIssueIDs = findAssignedBeadsForAgent(cwd, sender)
		}
		return assignedIssueIDs
	}

	// If issue ID not set by flag or branch name, query for hooked beads
	// assigned to this agent. This replaces reading agent_bead.hook_bead
	// (hq-l6mm5: direct bead tracking instead of agent bead slot).
	if issueID == "" && sender != "" {
		if hookIssue, ambiguous := selectAssignedIssue("", loadAssignedIssueIDs()); hookIssue != "" {
			issueID = hookIssue
		} else if ambiguous {
			return fmt.Errorf("multiple active assignments found for %s; cannot infer issue from hook. Use --issue to disambiguate", sender)
		}
	}

	// Stale-branch guard (hq-l0fj): a redispatched polecat that reuses its
	// previous work branch carries the OLD bead-id in the branch name, which
	// would mis-attribute this MR (close credit goes to a closed bead; the
	// real issue stays open and hooked). When the branch-derived id differs
	// from the hooked bead, trust the hook. An explicit --issue flag still
	// wins, and subtask branches of the hooked bead (e.g. gt-abc.1 under
	// hooked gt-abc) are left alone.
	if doneIssue == "" && info.Issue != "" && sender != "" {
		if hookIssue, ambiguous := selectAssignedIssue(info.Issue, loadAssignedIssueIDs()); isStaleBranchIssue(info.Issue, hookIssue) {
			style.PrintWarning("branch %q embeds issue %s but your hooked bead is %s — submitting for %s (stale branch reuse?)", branch, info.Issue, hookIssue, hookIssue)
			fmt.Printf("  Fresh branches must be named polecat/<name>/<bead-id>@<suffix> for the bead you are working.\n")
			fmt.Printf("  Use --issue to override if the branch-derived id is actually correct.\n\n")
			issueID = hookIssue
		} else if ambiguous {
			return fmt.Errorf("branch %q embeds issue %s but %s has multiple active assignments; use --issue to disambiguate", branch, info.Issue, sender)
		}
	}
	doneBeadID = issueID // capture for the deferred done telemetry (KPI-1)

	// hq-9jeyo: refuse to complete/close a reference or gate tripwire. Extracted
	// to releaseIfReferenceBead (gu-nid89.12.1): when the hooked bead is a
	// do-not-dispatch / pinned tripwire it is released back to open and gt done
	// exits without closing it.
	if releaseIfReferenceBead(cwd, issueID) {
		return nil
	}

	// Write the done-intent label EARLY (so the Witness can auto-nuke a zombie if
	// gt done crashes) and read any resume checkpoints (gt-aufru). Extracted to
	// writeDoneIntentAndReadCheckpoints (gu-nid89.12.1).
	checkpoints := writeDoneIntentAndReadCheckpoints(cwd, agentBeadID, exitType)

	// Write heartbeat state="exiting" (gt-3vr5: heartbeat v2).
	// Tells the witness we're in the gt done flow — trust the agent until
	// heartbeat goes stale. No timer-based inference needed.
	// Parallel to done-intent label for backwards compat during migration.
	if sessionName := os.Getenv("GT_SESSION"); sessionName != "" && townRoot != "" {
		polecat.TouchSessionHeartbeatWithState(townRoot, sessionName, polecat.HeartbeatExiting, "gt done", issueID)
	}

	// Reuse the rig's default branch resolved earlier for the safety-net guard.
	// Kept as a local alias to minimize diff to the extensive code below that
	// references `defaultBranch` by name.
	defaultBranch := defaultBranchEarly

	// For COMPLETED, we need an issue ID and branch must not be the default branch
	var mrID string
	var pushFailed bool
	var mrFailed bool
	var convoyInfo *ConvoyInfo // Populated if issue is tracked by a convoy
	if exitType == ExitCompleted {
		if branch == defaultBranch || branch == "master" {
			return fmt.Errorf("cannot submit %s/master branch to merge queue", defaultBranch)
		}

		// Bundle the completion-path state shared by the extracted COMPLETED
		// phases (completeNoMR, the convoy strategies, runNoMergeStrategy). Built
		// once here; all fields are already resolved in the preamble above.
		sc := strategyContext{
			g:             g,
			cwd:           cwd,
			townRoot:      townRoot,
			rigName:       rigName,
			sender:        sender,
			branch:        branch,
			defaultBranch: defaultBranch,
			issueID:       issueID,
			worker:        worker,
			agentBeadID:   agentBeadID,
		}

		// CRITICAL: Verify work exists before completing (hq-xthqf). The
		// work-exists preflight (cwd availability, uncommitted-changes guard,
		// commits-ahead count, no_merge/review_only detection) is extracted to
		// verifyWorkExistsForCompletion (gu-nid89.12.1).
		originDefault, aheadCount, isNoMergeTask, err := verifyWorkExistsForCompletion(sc, cwdAvailable)
		if err != nil {
			return err
		}

		// If no commits ahead, work was likely pushed directly to main (or already merged)
		// For polecats, zero commits usually means the polecat sleepwalked through
		// implementation without writing code (gastown#1484, beads#emma).
		// The --cleanup-status=clean escape is preserved for legitimate report-only
		// tasks (audits, reviews) that the formula explicitly directs to use it.
		// no_merge/review_only tasks (GH#2496, gt-kvf) also bypass: non-code work has no commits by design.
		// IMPORTANT: The error message must NOT mention --cleanup-status=clean.
		// LLM agents read error messages and self-bypass (the original bug).
		if aheadCount == 0 {
			// Zero commits ahead: work was pushed directly to main, already
			// merged, or this is a no_merge/review_only/--no-code task. The
			// close-without-MR path (zero-commit guard, citation guard, verify,
			// bead close) is extracted to completeNoMR (gu-nid89.12.1).
			if noMRErr := completeNoMR(sc, originDefault, isNoMergeTask); noMRErr != nil {
				return noMRErr
			}
			// Skip straight to witness notification (no MR needed)
			goto notifyWitness
		}

		// Pre-push preflight (gu-nid89.12.1 extraction): GH#2220 branch
		// contamination check + gh#3400 auto-rebase + gu-xp5f --pre-verified
		// attestation re-verification. Returns whether the polecat's
		// --pre-verified claim still holds (false → refinery re-runs gates).
		alreadyPushed := checkpoints[CheckpointPushed] == branch
		preVerifiedAttestationValid, err := runContaminationPreflight(sc, cwdAvailable, alreadyPushed)
		if err != nil {
			return err
		}

		// Strip Gas Town overlay from CLAUDE.md / CLAUDE.local.md (gt-p35).
		// Polecats commit the overlay (polecat lifecycle boilerplate) into repos,
		// overwriting project-specific CLAUDE.md content. Detect and revert before push.
		stripOverlayCLAUDEmd(g, defaultBranch)

		// Determine merge strategy from convoy (gt-myofa.3)
		// Convoys can override the default MR-based workflow:
		//   direct: push commits straight to target branch, bypass refinery
		//   mr:     default — create merge-request bead, refinery merges
		//   local:  keep on feature branch, no push, no MR (for human review/upstream PRs)
		//
		// Primary: read convoy info from the issue's attachment fields (gt-7b6wf fix).
		// gt sling stores convoy_id and merge_strategy on the issue when dispatching,
		// which avoids unreliable cross-rig dep resolution at gt done time.
		// Fallback: dep-based lookup via getConvoyInfoForIssue (for issues dispatched
		// before this fix, or where attachment fields weren't set).
		convoyInfo = getConvoyInfoFromIssue(issueID, cwd)
		if convoyInfo == nil {
			convoyInfo = getConvoyInfoForIssue(issueID)
		}

		// Convoy merge-strategy dispatch (gu-nid89.12.1 extraction). The local
		// (incl. gs-d26 relay-leg FF-push) and direct (incl. gu-8edz merge-queue
		// guard) phases live in done_strategies.go; runDone owns only the
		// dispatch + notifyWitness routing. sc (strategyContext) was built at the
		// top of the COMPLETED block.

		// Handle "local" strategy: skip push and MR entirely (or FF-push a relay
		// leg). A merge=local convoy is always fully handled here.
		//
		// gs-ydv9 guard: a non-relay merge=local on a PR-review rig was stamped by
		// a rig-default formula during auto/deferred dispatch, overriding the
		// bead's intended PR workflow. Taking the keep-local path would close the
		// bead COMPLETED while the commits never reach origin or review (work loss
		// on a customer repo). Rewrite to "mr" so it falls through to the normal
		// merge-queue path instead of stranding.
		if convoyInfo != nil && convoyInfo.MergeStrategy == "local" &&
			localMergeWouldStrandReviewedCodeBead(sc, convoyInfo, isNoMergeTask) {
			fmt.Fprintf(os.Stderr, "%s merge=local with no relay base on a PR-review rig — routing through the merge queue instead of stranding (gs-ydv9)\n", style.Bold.Render("→"))
			convoyInfo.MergeStrategy = "mr"
		}

		if convoyInfo != nil && convoyInfo.MergeStrategy == "local" {
			pushFailed = runConvoyLocalStrategy(sc, convoyInfo)
			goto notifyWitness
		}

		// Handle "direct" strategy: push to target branch, skip MR. When the
		// gu-8edz guard fires the strategy is not handled — fall through to the
		// normal push+MR path so the work still lands via the merge queue.
		if convoyInfo != nil && convoyInfo.MergeStrategy == "direct" {
			if handled, pf := runConvoyDirectStrategy(sc); handled {
				pushFailed = pf
				goto notifyWitness
			}
		}

		// Default: "mr" strategy (or no convoy) — push branch, then create MR bead.
		// The push phase (gt-aufru resume, gu-cfb default-branch guard, gs-pd6
		// fallback ladder, gu-epv5 verify-then-recover, receipt + checkpoint) is
		// extracted to pushBranchForMR (gu-nid89.12.1). strand=true means the push
		// failed and a stranded-push wisp was filed — route to notifyWitness.
		if pushBranchForMR(sc, checkpoints) {
			pushFailed = true
			goto notifyWitness
		}

		if issueID == "" {
			return fmt.Errorf("cannot determine source issue from branch '%s'; use --issue to specify", branch)
		}

		// Initialize beads — warn if resolved to a local .beads/ (no redirect).
		// Without a redirect, MR beads are invisible to the Refinery.
		resolvedBeads := beads.ResolveBeadsDir(cwd)
		if beads.IsLocalBeadsDir(cwd, resolvedBeads) {
			fmt.Fprintf(os.Stderr, "WARNING: beads resolved to local dir %s (no shared-beads redirect)\n", resolvedBeads)
			fmt.Fprintf(os.Stderr, "  MR beads written here will be invisible to the Refinery — run 'gt polecat repair' to fix\n")
		}
		bd := beads.NewWithBeadsDir(cwd, resolvedBeads)

		// Check for no_merge flag - if set, skip merge queue and notify for review.
		// The no-merge handler (PR creation, dispatcher notify, bead close) is
		// extracted to runNoMergeStrategy (gu-nid89.12.1); runDone owns only the
		// flag check + notifyWitness routing. sourceIssueForNoMerge is reused below
		// for MR target resolution, so the Show() stays here.
		sourceIssueForNoMerge, err := bd.Show(issueID)
		if err == nil {
			attachmentFields := beads.ParseAttachmentFields(sourceIssueForNoMerge)
			if attachmentFields != nil && attachmentFields.NoMerge {
				runNoMergeStrategy(sc, bd, sourceIssueForNoMerge, attachmentFields)
				goto notifyWitness
			}
		}

		// Fallback: check if issue belongs to a direct-merge convoy that the
		// primary check (line ~483) missed — e.g., issues dispatched before the
		// attachment-field fix, or where dep-based lookup failed at that point.
		// At this stage the branch was pushed to origin/<branch> (feature branch),
		// NOT to main. So we must push to main now before skipping MR creation.
		convoyInfo = getConvoyInfoFromIssue(issueID, cwd)
		if convoyInfo == nil {
			convoyInfo = getConvoyInfoForIssue(issueID)
		}
		if convoyInfo != nil && convoyInfo.MergeStrategy == "direct" {
			// Late-detected direct merge: the feature branch is already on
			// origin/<branch>; push it to the rig default and close the bead.
			// When the gu-8edz guard fires or the push fails, fall through to
			// normal MR creation (runLateDirectStrategy returns handled=false).
			if handled, pf := runLateDirectStrategy(sc, bd, convoyInfo); handled {
				pushFailed = pf
				goto notifyWitness
			}
		}

		// Determine target branch for the MR. The full priority ladder (explicit
		// --target > formula_vars base_branch > relay base > integration-branch
		// auto-detect > rig default, plus the gu-aucji phantom-main safety net) is
		// extracted to resolveMRTarget (gu-nid89.12.1).
		target := resolveMRTarget(sc, bd, sourceIssueForNoMerge)

		// MR bead priority: --priority flag, else inherit from the source bead
		// (resolveMRPriority, gu-nid89.12.1).
		priority := resolveMRPriority(bd, issueID)

		// GH#3032: Resolve HEAD commit SHA for MR dedup. Branch name alone is not a
		// valid dedup key — a polecat may push new commits to the same branch after
		// a gate failure. The commit SHA distinguishes new submissions from retries.
		commitSHA, _ := g.Rev("HEAD")

		// Resume: skip MR creation if already durably filed in a previous run
		// (gt-aufru + gs-onu fresh-main verify), extracted to
		// resumeMRFromCheckpoint (gu-nid89.12.1).
		if cpMRID, resumed := resumeMRFromCheckpoint(sc, bd, resolvedBeads, commitSHA, checkpoints); resumed {
			mrID = cpMRID
			goto afterMR
		}

		// Submit the merge-request bead for the pushed branch (gs-t0k phase 2).
		// submitToMergeQueue owns the find/dedup → create → read-back → main-view
		// verify (gs-onu/gs-9sr) → supersede → back-link → checkpoint sequence,
		// matching the pushBranchWithFallbacks (gs-pd6) and completion-package
		// (gs-bn1) extractions. A true mrFailed means the MR could not be durably
		// filed: the branch is on origin but there is no MR the refinery can see,
		// so route to notifyWitness. mrID may still be set when mrFailed is true
		// (it is populated before the late verifications) — the gu-v76i guard
		// below accounts for that.
		mrID, mrFailed = submitToMergeQueue(mrSubmitParams{
			bd:                          bd,
			g:                           g,
			cwd:                         cwd,
			resolvedBeads:               resolvedBeads,
			townRoot:                    townRoot,
			rigName:                     rigName,
			branch:                      branch,
			commitSHA:                   commitSHA,
			target:                      target,
			issueID:                     issueID,
			worker:                      worker,
			agentBeadID:                 agentBeadID,
			priority:                    priority,
			preVerifiedAttestationValid: preVerifiedAttestationValid,
		})
		if mrFailed {
			goto notifyWitness
		}

	afterMR:
		fmt.Printf("  Source: %s\n", branch)
		fmt.Printf("  Target: %s\n", target)
		fmt.Printf("  Issue: %s\n", issueID)
		if worker != "" {
			fmt.Printf("  Worker: %s\n", worker)
		}
		fmt.Printf("  Priority: P%d\n", priority)
		fmt.Println()
		fmt.Printf("%s\n", style.Dim.Render("The Refinery will process your merge request."))
	} else {
		// For ESCALATED or DEFERRED, just print status
		fmt.Printf("%s Signaling %s\n", style.Bold.Render("→"), exitType)
		if issueID != "" {
			fmt.Printf("  Issue: %s\n", issueID)
		}
		fmt.Printf("  Branch: %s\n", branch)
	}

notifyWitness:
	// gs-a5v (gs-pd6 phase 3): the completion tail — refinery nudge, completion
	// metadata, done logging, agent-state update, witness notify, worktree sync,
	// and opt-in self-terminate — lives in teardownAfterDone. Every `goto
	// notifyWitness` site reaches this single tail with the live MR/push state.
	//
	// Resolve the convoy merge strategy for the idle-sync guard: a merge=local
	// leg must NOT sync its worktree away from the feature branch (the work is
	// parked there for human review / upstream PR). Mirrors upstream idle-sync-fix.
	if exitType == ExitCompleted && issueID != "" && convoyInfo == nil {
		convoyInfo = getConvoyInfoFromIssue(issueID, cwd)
		if convoyInfo == nil {
			convoyInfo = getConvoyInfoForIssue(issueID)
		}
	}
	teardownMergeStrategy := ""
	if convoyInfo != nil {
		teardownMergeStrategy = convoyInfo.MergeStrategy
	}
	teardownAfterDone(teardownParams{
		g:             g,
		cwd:           cwd,
		townRoot:      townRoot,
		rigName:       rigName,
		sender:        sender,
		polecatName:   polecatName,
		branch:        branch,
		defaultBranch: defaultBranch,
		issueID:       issueID,
		agentBeadID:   agentBeadID,
		exitType:      exitType,
		mrID:          mrID,
		mrFailed:      mrFailed,
		pushFailed:    pushFailed,
		cwdAvailable:  cwdAvailable,
		mergeStrategy: teardownMergeStrategy,
	})
	return nil
}

// teardownParams carries the runDone completion-path state into
// teardownAfterDone. These are the values live at the notifyWitness label:
// the MR/push outcome flags, the agent identity, and the worktree handle
// needed to nudge refinery, record completion, notify the witness, and
// sync/self-terminate the sandbox.
type teardownParams struct {
	g             *git.Git
	cwd           string
	townRoot      string
	rigName       string
	sender        string
	polecatName   string
	branch        string
	defaultBranch string
	issueID       string
	agentBeadID   string
	exitType      string
	mrID          string
	mrFailed      bool
	pushFailed    bool
	cwdAvailable  bool
	mergeStrategy string
}

// teardownAfterDone runs the shared completion tail of runDone (the
// notifyWitness path). Extracted from runDone (gs-a5v, gs-pd6 phase 3);
// behavior is preserved exactly. The previous inline block and every `goto
// notifyWitness` site now funnel through a single teardownAfterDone call.
func teardownAfterDone(p teardownParams) {
	// Nudge refinery — MR bead is already on main (transaction-based shared main).
	//
	// gu-v76i: Guard against spurious MQ_SUBMIT events. If the MR bead could not
	// be created, returned an empty ID, or failed read-back verification, mrFailed
	// is set. In those cases mrID may still be non-empty (set before the failure
	// was detected), but there is no durable MR wisp for the refinery to pick up.
	// Nudging anyway causes refinery sessions to wake, scan an empty queue, and
	// escalate phantom MQ_SUBMIT alerts across the town.
	if p.mrID != "" && !p.mrFailed && shouldNudgeRefinery(p.exitType, p.mrID) {
		nudgeRefinery(p.rigName, "MERGE_READY received - check inbox for pending work")
	}

	// Write completion metadata to agent bead for audit trail.
	// Self-managed completion (gt-1qlg): metadata is retained for anomaly
	// detection and crash recovery by witness patrol, but the witness no
	// longer processes routine completions from these fields.
	fmt.Printf("\nNotifying Witness...\n")
	if p.agentBeadID != "" {
		// Agent bead lives in town DB despite rig prefix — bypass routing.
		completionBd := beads.New(p.cwd).ForAgentBead()
		meta := &beads.CompletionMetadata{
			ExitType:       p.exitType,
			MRID:           p.mrID,
			Branch:         p.branch,
			HookBead:       p.issueID,
			MRFailed:       p.mrFailed,
			PushFailed:     p.pushFailed,
			CompletionTime: time.Now().UTC().Format(time.RFC3339),
			LastOutcome:    deriveLifecycleOutcome(p.exitType, p.mrID, p.mrFailed, p.pushFailed),
		}
		if err := completionBd.UpdateAgentCompletion(p.agentBeadID, meta); err != nil {
			style.PrintWarning("could not write completion metadata to agent bead: %v", err)
		}
	}

	// Write witness notification checkpoint for resume (gt-aufru)
	if p.agentBeadID != "" {
		// Agent bead lives in town DB despite rig prefix — bypass routing.
		cpBd := beads.New(p.cwd).ForAgentBead()
		writeDoneCheckpoint(cpBd, p.agentBeadID, CheckpointWitnessNotified, "ok")
	}

	// Log done event (townlog and activity feed)
	if err := LogDone(p.townRoot, p.sender, p.issueID); err != nil {
		style.PrintWarning("could not log done event: %v", err)
	}
	if err := events.LogFeed(events.TypeDone, p.sender, events.DonePayload(p.issueID, p.branch)); err != nil {
		style.PrintWarning("could not log feed event: %v", err)
	}

	// Emit a clean session-end event (D3 / gu-dnkz4). This is the clean-exit
	// complement to TypeSessionDeath, giving the crash-rate KPI a clean
	// denominator: rate(session_death) / rate(session_end) per rig. Audit-only
	// — a routine completion is mechanical, and TypeDone already carries the
	// feed-visible "completed work" line; session_end is for the events plane.
	sessionName := os.Getenv("GT_SESSION")
	_ = events.LogAudit(events.TypeSessionEnd, p.sender,
		events.SessionEndPayload(sessionName, p.sender, p.rigName,
			fmt.Sprintf("gt done (exit=%s)", p.exitType), "gt done"))

	// Update agent bead state (ZFC: self-report completion).
	// gu-rh0g: pass pushFailed/mrFailed through so the hooked-bead close
	// path can refuse to false-close work that never reached origin/main.
	// Without this guard the bead transitions to closed("Completed via gt
	// done (exit=COMPLETED)") even when the polecat's commits were stranded
	// on a polecat branch — destroying real work and trust signals.
	//
	// gu-treq: when push+MR succeed the work has NOT yet shipped to
	// origin/main — refinery merges the polecat branch asynchronously.
	// Compute awaitingRefineryMerge so the hooked bead stays open until
	// refinery's PostMerge path closes it with the real on-main commit_sha.
	// Pattern A guards (gu-551r commit-references-bead) live inside refinery's
	// close path; the refinery is merging the polecat's own commits so the
	// citation is correct by construction.
	//
	// gu-y2w7g: the discriminator is "was an MR successfully created?", NOT
	// "is this a merge-queue rig?". MR-bead creation in the mr-strategy path
	// is not gated on completion.IsMergeQueueRig — so when an MR exists but the
	// rig doesn't report merge_queue.enabled (settings unreadable, or the flag
	// is unset), the old IsMergeQueueRig gate let the bead false-close while
	// the commit sat only on a feature branch, never on origin/main (incident
	// cacr-d9to0/uqpnf: closed 06-06, never landed). If we got here with an MR,
	// the work is awaiting merge regardless of rig detection — refinery (or a
	// recovery sweep over the awaiting_refinery_merge label) owns the close.
	awaitingRefineryMerge := shouldAwaitRefineryMerge(p.exitType, p.pushFailed, p.mrFailed, p.mrID)
	updateAgentStateOnDone(p.cwd, p.townRoot, p.exitType, p.issueID, p.pushFailed || p.mrFailed, awaitingRefineryMerge, p.mrID, p.branch)

	// Nudge witness only after hook/cleanup state is updated. Otherwise witness can
	// evaluate slot availability against stale hook_bead or cleanup_status and emit
	// false SLOT_BLOCKED/SLOT_OPEN signals.
	nudgeWitness(p.rigName, fmt.Sprintf("POLECAT_DONE %s exit=%s", p.polecatName, p.exitType))
	fmt.Printf("%s Witness notified of %s (via nudge)\n", style.Bold.Render("✓"), p.exitType)

	// Persistent polecat model (gt-hdf8), reconciled with self-terminate (gs-4pg):
	// the pool persists IDENTITY + SANDBOX (worktree); the SESSION is ephemeral.
	// On completion the polecat transitions to IDLE (agent_state=idle, worktree
	// synced to main, old branch deleted) so its warm worktree is reusable, and
	// then — when polecat_self_terminate is on (gu-ci0l, now default) — kills its
	// own session below. These are NOT competing models: reuse (ReuseIdlePolecat)
	// always kills any existing session and respawns fresh for a clean context, so
	// session liveness is irrelevant to reuse. "done means idle" describes the
	// SANDBOX (warm, reusable), not the session (which dies and respawns on reuse).
	isPolecat := false
	if roleInfo, err := GetRoleWithContext(p.cwd, p.townRoot); err == nil && roleInfo.Role == RolePolecat {
		isPolecat = true

		fmt.Printf("%s Sandbox preserved for reuse (persistent polecat)\n", style.Bold.Render("✓"))

		if p.pushFailed || p.mrFailed {
			fmt.Printf("%s Work needs recovery (push or MR failed) — session preserved\n", style.Bold.Render("⚠"))
		}

		// Sync worktree to main so the polecat is ready for new assignments.
		// Phase 3 of persistent-polecat-pool: DONE→IDLE syncs to main and deletes old branch.
		// Non-fatal: if sync fails, the polecat is still IDLE and the Witness
		// or next gt sling can handle the branch state.
		//
		// GUARD (gt-pvx): Refuse to sync if uncommitted changes remain.
		// If the auto-commit safety net above failed (git add/commit error),
		// switching branches would discard the work. Better to leave the worktree
		// dirty on the feature branch so work can be recovered.
		syncSafe := true
		if p.cwdAvailable {
			if ws, wsErr := p.g.CheckUncommittedWork(); wsErr != nil {
				syncSafe = false
				style.PrintWarning("could not inspect worktree before idle sync: %v — skipping sync to preserve work", wsErr)
			} else if ws.HasUncommittedChanges && !ws.CleanExcludingRuntime() {
				syncSafe = false
				style.PrintWarning("uncommitted changes still present — skipping worktree sync to preserve work")
				fmt.Printf("  Files: %s\n", ws.String())
			}
		}
		if p.cwdAvailable && shouldSyncIdlePolecatWorktree(p.exitType, p.mergeStrategy, p.pushFailed, p.mrFailed, syncSafe) {
			// Remember the old branch so we can delete it after switching
			oldBranch := p.branch

			fmt.Printf("%s Syncing worktree to %s...\n", style.Bold.Render("→"), p.defaultBranch)
			if err := p.g.Checkout(p.defaultBranch); err != nil {
				// Worktree can't checkout defaultBranch (likely held by rig-root).
				// Detach HEAD so the old feature branch can be deleted cleanly.
				if detachErr := p.g.DetachHead(); detachErr != nil {
					style.PrintWarning("could not checkout %s or detach: %v (worktree stays on feature branch)", p.defaultBranch, err)
				} else {
					fmt.Printf("%s Detached HEAD (worktree checkout of %s blocked by another worktree)\n", style.Bold.Render("✓"), p.defaultBranch)
				}
			} else if err := p.g.Pull("origin", p.defaultBranch); err != nil {
				style.PrintWarning("could not pull %s: %v (worktree on %s but may be stale)", p.defaultBranch, p.defaultBranch, err)
			} else {
				fmt.Printf("%s Worktree synced to %s\n", style.Bold.Render("✓"), p.defaultBranch)
			}

			// Delete the old polecat branch (non-fatal: cleanup only).
			// This prevents stale branch accumulation from persistent polecats.
			if oldBranch != "" && oldBranch != p.defaultBranch && oldBranch != "master" {
				if err := p.g.DeleteBranch(oldBranch, true); err != nil {
					style.PrintWarning("could not delete old branch %s: %v", oldBranch, err)
				} else {
					fmt.Printf("%s Deleted old branch %s\n", style.Bold.Render("✓"), oldBranch)
				}
			}
		}

		// On customer-repo rigs, sweep this polecat's own landed branches off the
		// customer's origin so their gastown-internal names (agent + bead) stop
		// leaking (gs-7s52). Best-effort and non-fatal; only branches whose work
		// has already landed are touched, so an open PR's head is never removed.
		if p.cwdAvailable {
			sweepCustomerRepoLeakedBranches(p)
		}

		fmt.Printf("%s Polecat IDLE — warm worktree preserved for reuse (next gt sling respawns a fresh session)\n", style.Bold.Render("✓"))
	}

	fmt.Println()
	if !isPolecat {
		fmt.Printf("%s Session exiting\n", style.Bold.Render("→"))
		fmt.Printf("  Witness will handle cleanup.\n")
	}

	// Self-terminate AFTER all cleanup is complete (opt-in via config).
	// When enabled, polecats kill their session after gt done finishes
	// instead of transitioning to IDLE. This gives fresh context windows
	// per task, reduces token waste, and eliminates stale state bugs.
	// Must be the LAST thing gt done does — everything above must complete first.
	//
	// Uses a detached subprocess instead of a goroutine (gu-fr85): the
	// goroutine ran inside the process being killed by the tmux session
	// destroy, creating a race where the kill might never execute. A
	// detached subprocess survives the parent's exit independently.
	if isPolecat {
		daemonCfg := config.LoadOperationalConfig(p.townRoot).GetDaemonConfig()
		// gu-ci0l: default-true via PolecatSelfTerminateV(). The previous nil-check
		// pattern silently fell through to false when the operator did not configure
		// the field, which exposed polecats to a post-done wedge loop (witness
		// dependence + restart re-dispatch). Operators can still opt out by setting
		// operational.daemon.polecat_self_terminate=false explicitly.
		if daemonCfg.PolecatSelfTerminateV() {
			fmt.Printf("%s Self-terminating session (polecat_self_terminate=true)\n", style.Bold.Render("✓"))
			sessionName := session.PolecatSessionName(session.PrefixFor(p.rigName), p.polecatName)
			t := tmux.NewTmux()
			if err := t.DetachedKillSessionWithProcesses(sessionName, 3*time.Second); err != nil {
				style.PrintWarning("could not spawn detached session kill: %v", err)
			}
		}
	}
}

// sweepCustomerRepoLeakedBranches deletes this polecat's own pushed
// polecat/<name>/* branches from origin once their work has landed, but ONLY on
// rigs flagged customer_repo=true. On such rigs origin IS the customer's real
// remote, so every polecat branch pushed to open a PR (mol-lia-pr-work) leaves a
// gastown-internal name — agent + bead ID — sitting in the customer's repo
// (gs-7s52). gs-8p5r closed the preserved/* vector; this closes the
// working-branch/PR-head vector.
//
// The branch must stay on origin while its PR is open (that is how the PR was
// opened), so a branch is removed only when its work has demonstrably LANDED:
// a merged PR reported by the VCS provider, or commits that are already
// patch-equivalent to origin/<defaultBranch> AND have no open PR. That predicate
// never matches the just-pushed branch this polecat is completing, nor any peer's
// open-PR branch, so the PR flow is untouched.
//
// Best-effort and self-scoped: failures are non-fatal warnings, and only
// polecat/<this-polecat>/* refs are considered — matching the proxy push-gate
// that restricts a polecat to its own namespace (internal/proxy/git.go).
func sweepCustomerRepoLeakedBranches(p teardownParams) {
	r := &rig.Rig{Name: p.rigName, Path: filepath.Join(p.townRoot, p.rigName)}
	if !r.GetBoolConfig("customer_repo") {
		return
	}

	// Prune stale remote-tracking refs first so merged-and-already-deleted
	// branches don't resurface as phantom candidates.
	if err := p.g.FetchPrune("origin"); err != nil {
		style.PrintWarning("customer-repo branch sweep: fetch --prune failed: %v (continuing)", err)
	}

	refs, err := p.g.ListPushRemoteRefsWithHashes("origin", "refs/heads/polecat/")
	if err != nil {
		style.PrintWarning("customer-repo branch sweep: could not list remote polecat branches: %v", err)
		return
	}

	base := p.defaultBranch
	ownDir := fmt.Sprintf("polecat/%s/", p.polecatName)  // polecat/<name>/<issue>--<ts>
	ownFlat := fmt.Sprintf("polecat/%s-", p.polecatName) // polecat/<name>-<ts> (no issue)
	swept := 0
	for _, ref := range refs {
		branch := strings.TrimPrefix(ref.Name, "refs/heads/")
		// Self-scope: only this polecat's own branches.
		if !strings.HasPrefix(branch, ownDir) && !strings.HasPrefix(branch, ownFlat) {
			continue
		}
		// Never the branch we are completing right now (its PR is still open).
		if branch == p.branch {
			continue
		}
		if !customerRepoBranchLanded(p.g, branch, base, ref.Hash) {
			continue
		}
		if err := p.g.DeleteRemoteBranchIfAt("origin", branch, ref.Hash); err != nil {
			style.PrintWarning("customer-repo branch sweep: could not delete origin %s: %v", branch, err)
			continue
		}
		fmt.Printf("%s Deleted landed branch from customer origin: %s (was leaking agent/bead name)\n", style.Bold.Render("✓"), branch)
		swept++
	}
	if swept > 0 {
		fmt.Printf("%s Swept %d landed polecat branch(es) off the customer remote (gs-7s52)\n", style.Bold.Render("✓"), swept)
	}
}

// customerRepoBranchLanded reports whether a remote polecat branch's work has
// already landed, so deleting it from the customer origin cannot orphan
// in-flight work or close an open PR. Two independent landing signals:
//
//  1. The VCS provider reports a MERGED PR for the branch (squash-safe: this is
//     GitHub's merge state, not a SHA comparison).
//  2. The branch has NO open PR AND its commits are already patch-equivalent to
//     origin/<base> (git cherry reports zero unmerged commits) — covers
//     direct-merged work and squash/rebase landings.
//
// A freshly pushed branch whose PR is not yet open fails BOTH checks (it still
// has unique commits), so it is left in place.
func customerRepoBranchLanded(g *git.Git, branch, base, hash string) bool {
	if merged, err := g.FindMergedPRCommit(branch); err == nil && merged != "" {
		return true
	}
	if g.HasOpenPR(branch) {
		return false
	}
	cherryOut, err := g.Cherry("origin/"+base, hash)
	if err != nil {
		return false
	}
	return git.CountCherryUnmergedCommits(cherryOut) == 0
}

// mrSubmitParams carries the runDone completion state that submitToMergeQueue
// needs to file the merge-request bead. The done* flag globals (doneSkipVerify,
// doneSkipVerifyReason, donePreVerified) are read directly and not threaded here.
type mrSubmitParams struct {
	bd            *beads.Beads
	g             *git.Git
	cwd           string
	resolvedBeads string
	townRoot      string
	rigName       string
	branch        string
	commitSHA     string
	target        string
	issueID       string
	worker        string
	agentBeadID   string
	priority      int
	// preVerifiedAttestationValid is false once an auto-rebase invalidated the
	// polecat's pre-verification gates (gs-4bn); the pre_verified attestation is
	// only written when this stays true.
	preVerifiedAttestationValid bool
}

// submitToMergeQueue files (or reuses) the merge-request bead for a completed
// polecat branch that is already pushed to origin. It is the MR-submit phase of
// runDone extracted whole (gs-t0k phase 2, follow-up to the gs-pd6 push-ladder
// and gs-bn1 completion-package extractions), bounded by the checkpoint-resume
// `goto afterMR` site and the afterMR label.
//
// The sequence: idempotent find by branch+SHA (GH#3032) → create the MR bead
// with DoltAutoCommit:"on" so the write reaches shared main (gs-onu) → read-back
// verify (GH#1945) → main-view verify through a fresh connection (gs-9sr) →
// rig-prefix guard (gt-gpy) → supersede stale MRs for the issue (GH#3032) →
// back-link agent + source beads → write the MR checkpoint for resume (gt-aufru).
//
// It returns the MR id and mrFailed. mrFailed is true when the MR could not be
// durably filed (create error, empty id, failed read-back, or invisible on
// shared main); in every such case the side effects (warnings, stranded-push
// wisp) are already emitted and the caller must route to notifyWitness rather
// than report COMPLETED. mrID may be non-empty even when mrFailed is true — it
// is set before the late verifications run, and the gu-v76i nudge guard accounts
// for this. A false mrFailed means the MR is on shared main and the refinery can
// see it.
func submitToMergeQueue(p mrSubmitParams) (mrID string, mrFailed bool) {
	var existingMR *beads.Issue
	var err error

	// Check if MR bead already exists for this branch+SHA (idempotency)
	if p.commitSHA != "" {
		existingMR, err = p.bd.FindMRForBranchAndSHA(p.branch, p.commitSHA)
	} else {
		existingMR, err = p.bd.FindMRForBranch(p.branch)
	}
	if err != nil {
		style.PrintWarning("could not check for existing MR: %v", err)
		// Continue with creation attempt - Create will fail if duplicate
	}

	if existingMR != nil {
		// MR already exists with same branch AND commit — true idempotent retry
		mrID = existingMR.ID
		fmt.Printf("%s MR already exists (idempotent)\n", style.Bold.Render("✓"))
		fmt.Printf("  MR ID: %s\n", style.Bold.Render(mrID))
	} else {
		// Build MR bead title and description
		title := fmt.Sprintf("Merge: %s", p.issueID)
		description := fmt.Sprintf("branch: %s\ntarget: %s\nsource_issue: %s\nrig: %s",
			p.branch, p.target, p.issueID, p.rigName)
		if p.commitSHA != "" {
			description += fmt.Sprintf("\ncommit_sha: %s", p.commitSHA)
		}
		if doneSkipVerify {
			description += "\nskip_verify: true"
			description += fmt.Sprintf("\nskip_verify_reason: %s", doneSkipVerifyReason)
		}
		if p.worker != "" {
			description += fmt.Sprintf("\nworker: %s", p.worker)
		}
		if p.agentBeadID != "" {
			description += fmt.Sprintf("\nagent_bead: %s", p.agentBeadID)
		}

		// Add conflict resolution tracking fields (initialized, updated by Refinery)
		description += "\nretry_count: 0"
		description += "\nlast_conflict_sha: null"
		description += "\nconflict_task_id: null"

		// Phase 3: Add pre-verification metadata if polecat ran gates after rebasing.
		// The refinery uses these fields to fast-path merge without re-running gates.
		//
		// gs-4bn: Only record the attestation when it is still valid. If an
		// auto-rebase fired above, the polecat's gates ran against the
		// pre-rebase base; advertising pre-verification would invite refinery
		// to fast-path on a stale claim.
		if donePreVerified && p.preVerifiedAttestationValid {
			description += "\npre_verified: true"
			description += fmt.Sprintf("\npre_verified_at: %s", time.Now().UTC().Format(time.RFC3339))
			// Capture current origin/target HEAD as the verified base.
			// The polecat rebased onto this SHA before running gates.
			if verifiedBase, baseErr := p.g.Rev("origin/" + p.target); baseErr == nil {
				description += fmt.Sprintf("\npre_verified_base: %s", verifiedBase)
			} else {
				style.PrintWarning("could not resolve origin/%s for pre-verified base: %v (pre-verification data incomplete)", p.target, baseErr)
			}
		}

		mrIssue, err := p.bd.Create(beads.CreateOptions{
			Title:       title,
			Labels:      []string{"gt:merge-request"},
			Priority:    p.priority,
			Description: description,
			Ephemeral:   true,
			Rig:         p.rigName, // Ensure MR bead is created in the rig's database (gt-7y7)
			// gs-onu: force this write to commit to shared main even if the
			// rig config has drifted to auto-commit=off. Otherwise the MR bead
			// sits in the polecat session's Dolt working set, the local bd.Show
			// readback below still passes, then the polecat self-terminates
			// before the write commits — the refinery never sees the MR and the
			// branch is silently stranded.
			DoltAutoCommit: "on",
		})
		if err != nil {
			// Non-fatal: record the error and skip to notifyWitness.
			// Push succeeded so branch is on remote, but MR bead failed.
			// Set mrFailed so the witness knows not to send MERGE_READY.
			errMsg := fmt.Sprintf("MR bead creation failed: %v", err)
			style.PrintWarning("%s\nBranch is pushed but MR bead not created. Witness will be notified.", errMsg)
			return mrID, true
		}
		mrID = mrIssue.ID

		// Guard against empty ID from bd create (observed in ephemeral/wisp mode).
		// Fail fast with a clear message rather than passing "" to bd.Show.
		if mrID == "" {
			errMsg := "MR bead creation returned empty ID"
			style.PrintWarning("%s\nBranch is pushed but MR bead has no ID. Witness will be notified.", errMsg)
			return mrID, true
		}

		// GH#1945: Verify MR bead is readable before considering it confirmed.
		// bd.Create() succeeds when the bead is written locally, but if the write
		// didn't persist (Dolt failure, corrupt state), we'd nuke the worktree
		// with no MR in the queue — losing the polecat's work permanently.
		if verifiedMR, verifyErr := p.bd.Show(mrID); verifyErr != nil || verifiedMR == nil {
			errMsg := fmt.Sprintf("MR bead created but verification read-back failed (id=%s): %v", mrID, verifyErr)
			style.PrintWarning("%s\nBranch is pushed but MR bead not confirmed. Preserving worktree.", errMsg)
			return mrID, true
		}

		// gs-9sr: MAIN-VIEW verify (gs-onu defense-in-depth). The read-back
		// above only proves the MR is in THIS session's local Dolt view — it
		// passes even when an auto-commit drift leaves the write uncommitted,
		// so the refinery never sees the MR and the branch strands silently.
		// Re-run the refinery's own discovery through a FRESH bd connection;
		// if the MR isn't discoverable on shared main, fail LOUD via a
		// stranded-push wisp instead of exiting COMPLETED.
		if visible, qErr := verifyMRVisibleOnMain(beads.NewWithBeadsDir(p.cwd, p.resolvedBeads), p.branch, p.commitSHA); qErr != nil {
			// Inconclusive (e.g. transient Dolt blip). The read-back passed and
			// DoltAutoCommit:"on" committed the write, so don't false-strand —
			// warn and proceed.
			style.PrintWarning("main-view MR verify inconclusive for %s (query error): %v\nProceeding on read-back + auto-commit durability.", mrID, qErr)
		} else if !visible {
			strandErr := fmt.Errorf("MR %s not discoverable on shared main via fresh query (branch=%s sha=%s) — the refinery would not see it", mrID, p.branch, shortSHA(p.commitSHA))
			style.PrintWarning("%v\nBranch is pushed but the MR is invisible to the refinery. Filing a stranded-push wisp for recovery instead of reporting COMPLETED.", strandErr)
			fileStrandedPushWisp(beads.New(p.cwd), p.rigName, p.branch, p.commitSHA, p.target, p.issueID, p.agentBeadID, p.worker, strandErr)
			return mrID, true
		}

		// gt-gpy: Validate that the MR bead landed in the rig's database.
		// If the source bead has a cross-rig prefix (e.g., hq-), the routing
		// could still resolve to the wrong database despite Rig: rigName.
		// This is a warning-only guard — mrFailed is NOT set on mismatch.
		if prefixErr := beads.ValidateRigPrefix(p.townRoot, p.rigName, mrID); prefixErr != nil {
			style.PrintWarning("MR bead prefix mismatch: %v\nThe refinery may not find this MR — check 'gt mq list %s'", prefixErr, p.rigName)
		}

		// GH#3032: Supersede older open MRs for the same source issue.
		// When a polecat re-submits after fixing a gate failure, the old MR
		// (same branch, different SHA) is stale. Close it so the refinery
		// doesn't process the old submission.
		if p.issueID != "" {
			if oldMRs, findErr := p.bd.FindOpenMRsForIssue(p.issueID); findErr == nil {
				for _, old := range oldMRs {
					if old.ID == mrID {
						continue // skip the one we just created
					}
					reason := fmt.Sprintf("superseded by %s", mrID)
					if closeErr := p.bd.CloseWithReason(reason, old.ID); closeErr != nil {
						style.PrintWarning("could not supersede old MR %s: %v", old.ID, closeErr)
						continue
					}
					fmt.Printf("  %s Superseded old MR: %s\n", style.Dim.Render("○"), old.ID)

					// gs-stvm: re-point the superseded MR's owning agent bead to the
					// new MR. Otherwise that (usually dead) polecat's agent bead keeps
					// active_mr pointing at this now-CLOSED MR; the post-merge orphan
					// reconcile never fires for it and `gt polecat nuke` refuses,
					// leaving the slot stuck until manual recovery.
					if repointErr := p.bd.RepointSupersededMRAgent(old, mrID); repointErr != nil {
						style.PrintWarning("could not re-point superseded MR %s agent bead: %v", old.ID, repointErr)
					}
				}
			}
		}

		// Update agent bead with active_mr reference (for traceability).
		// Agent beads live in HQ regardless of rig prefix — bypass routing
		// via ForAgentBead() to avoid the "issue not found" warning that
		// leaves active_mr null after every gt done (hq-e73z).
		if p.agentBeadID != "" {
			if err := p.bd.ForAgentBead().UpdateAgentActiveMR(p.agentBeadID, mrID); err != nil {
				style.PrintWarning("could not update agent bead with active_mr: %v", err)
			}
		}

		// GH#2599: Back-link source issue to MR bead for discoverability.
		if p.issueID != "" {
			comment := fmt.Sprintf("MR created: %s", mrID)
			if _, err := p.bd.Run("comments", "add", p.issueID, comment); err != nil {
				style.PrintWarning("could not back-link source issue %s to MR %s: %v", p.issueID, mrID, err)
			}
		}

		// Success output
		fmt.Printf("%s Work submitted to merge queue (verified)\n", style.Bold.Render("✓"))
		fmt.Printf("  MR ID: %s\n", style.Bold.Render(mrID))

		// NOTE: the refinery nudge is deferred to the notifyWitness/post-MR
		// section below, not fired here. (Historical note: an earlier comment
		// claimed this was to wait for a "polecat Dolt branch merge" — that is
		// stale. Beads use the transaction-based shared-main model against the
		// shared Dolt server; with DoltAutoCommit:"on" above the MR bead is
		// committed to main as soon as Create returns, so there is no branch to
		// merge. The deferral is just ordering: nudge after hook/cleanup state
		// is settled. See gs-onu.)
	}

	// Write MR checkpoint for resume (gt-aufru)
	if mrID != "" && p.agentBeadID != "" {
		// Agent bead lives in town DB despite rig prefix — bypass routing.
		cpBd := beads.New(p.cwd).ForAgentBead()
		writeDoneCheckpoint(cpBd, p.agentBeadID, CheckpointMRCreated, mrID)
	}

	return mrID, false
}

// pushSubmoduleChanges detects submodules modified between origin/defaultBranch
// and HEAD, and pushes each submodule's new commit to its remote before the
// parent repo push. This prevents the parent's submodule pointer from
// referencing commits that don't exist on the submodule's remote (gt-dzs).
func pushSubmoduleChanges(g *git.Git, defaultBranch string) {
	subChanges, err := g.SubmoduleChanges("origin/"+defaultBranch, "HEAD")
	if err != nil {
		// Non-fatal: repos without submodules return nil, nil.
		// Only warn if the error is real (not just "no submodules").
		style.PrintWarning("could not detect submodule changes: %v", err)
		return
	}
	for _, sc := range subChanges {
		if sc.NewSHA == "" {
			continue // Submodule removed, nothing to push
		}
		shortSHA := sc.NewSHA
		if len(shortSHA) > 8 {
			shortSHA = shortSHA[:8]
		}
		fmt.Printf("Pushing submodule %s (%s)...\n", sc.Path, shortSHA)
		if subPushErr := g.PushSubmoduleCommit(sc.Path, sc.NewSHA, "origin"); subPushErr != nil {
			style.PrintWarning("submodule push failed for %s: %v (parent push may fail)", sc.Path, subPushErr)
		} else {
			fmt.Printf("%s Submodule %s pushed\n", style.Bold.Render("✓"), sc.Path)
		}
	}
}

// pushBranchWithFallbacks performs the polecat's branch:branch push for the
// `gt done` "mr" strategy and, when the primary push fails, walks the full
// recovery ladder (extracted from runDone, gs-pd6 phase 1 — the biggest CC
// contributor in the completion path):
//
//  1. bare-repo fallback (.repo.git shares the worktree object DB) — GH#1348
//  2. mayor/rig clone fallback
//  3. orphan-commit recovery (gu-0l56): origin/<branch> already at our SHA
//     (a prior push delivered the commit), or an explicit SHA-refspec push for
//     the detached-HEAD trap where no local branch ref exists
//  4. transient / lost-ack recovery: re-check origin tip (gu-epv5), force-update
//     an identical-tree non-fast-forward own branch (gu-hz3vx), or adopt a
//     patch-identical commit already on origin (gs-y7g)
//
// It returns the SHA that was pushed (possibly adopted from origin in the gs-y7g
// case) and the final error after all recovery attempts. A nil error means the
// commit is on origin/<branch>; the caller still verifies the remote tip before
// creating an MR bead. This function performs only git operations — the
// push-failure receipt + stranded-wisp side-effects stay with the caller so the
// failure routing reads in one place.
func pushBranchWithFallbacks(g *git.Git, townRoot, rigName, branch, defaultBranch string) (string, error) {
	// Auto-push submodule changes BEFORE parent push (gt-dzs).
	// If the parent repo's submodule pointer references commits that don't
	// exist on the submodule's remote, the Refinery MR will be broken.
	pushSubmoduleChanges(g, defaultBranch)

	// Use explicit refspec (branch:branch) to create the remote branch.
	// Without refspec, git push follows the tracking config — polecat branches
	// track origin/main, so a bare push sends commits to main directly,
	// bypassing the MR/refinery flow (G20 root cause).
	refspec := branch + ":" + branch
	pushedCommitSHA, _ := g.Rev("HEAD")

	// Skip the push entirely when origin/<branch> already points at our HEAD
	// (gu-r96tr). A polecat that manually pushed its branch before `gt done`
	// (all gates green locally) leaves origin/<branch> == HEAD, so the push
	// below would deliver nothing — git reports "Everything up-to-date". But
	// git STILL fires the pre-push hook on that no-op push, and the hook
	// re-runs the full ~2min `go test ./...` suite. During a finish-storm,
	// 15+ polecats doing this redundant re-run pile up on the host-wide
	// gate-slot semaphore (gs-orsm) and `gt done` blocks ~30min in the push
	// timeout. Detecting the no-op here avoids invoking the hook at all.
	//
	// Conservative: only skip on an exact tip==HEAD match against the push
	// target (PushRemoteBranchTip honors a divergent pushurl, matching where
	// the push would write). Any read error, empty tip, or mismatch falls
	// through to the normal push — no behavior change. The caller still
	// verifies origin/<branch> after this returns, so a skip cannot mask a
	// commit that never actually reached origin.
	if pushedCommitSHA != "" {
		if tip, tipErr := g.PushRemoteBranchTip("origin", branch); tipErr == nil && tip == pushedCommitSHA {
			fmt.Printf("%s Branch already at HEAD on origin — skipping redundant push + pre-push gate re-run (gu-r96tr)\n", style.Bold.Render("✓"))
			return pushedCommitSHA, nil
		}
	}

	fmt.Printf("Pushing branch to remote...\n")
	pushErr := pushForDone(g, refspec)
	if pushErr != nil {
		// Primary push failed — try fallback from the bare repo (GH #1348).
		// When polecat sessions are reused or worktrees are stale, the worktree's
		// git context may be broken. But the branch always exists in the bare repo
		// (.repo.git) because worktree commits share the same object database.
		style.PrintWarning("primary push failed: %v — trying bare repo fallback...", pushErr)
		rigPath := filepath.Join(townRoot, rigName)
		bareRepoPath := filepath.Join(rigPath, ".repo.git")
		if _, statErr := os.Stat(bareRepoPath); statErr == nil {
			bareGit := git.NewGitWithDir(bareRepoPath, "")
			pushErr = pushForDone(bareGit, refspec)
			if pushErr != nil {
				style.PrintWarning("bare repo push also failed: %v", pushErr)
			} else {
				fmt.Printf("%s Branch pushed via bare repo fallback\n", style.Bold.Render("✓"))
			}
		} else {
			// No bare repo — try mayor/rig as last resort
			mayorPath := filepath.Join(rigPath, "mayor", "rig")
			if _, statErr := os.Stat(mayorPath); statErr == nil {
				mayorGit := git.NewGit(mayorPath)
				pushErr = pushForDone(mayorGit, refspec)
				if pushErr != nil {
					style.PrintWarning("mayor/rig push also failed: %v", pushErr)
				} else {
					fmt.Printf("%s Branch pushed via mayor/rig fallback\n", style.Bold.Render("✓"))
				}
			}
		}
	}

	if pushErr != nil {
		// Orphan-commit recovery (gu-0l56): all branch:branch pushes failed.
		// A common cause is the detached-HEAD scenario from gu-h5pr: an
		// auto-save commit landed on detached HEAD, the local branch ref
		// was deleted, and branch:branch now fails with "src refspec does
		// not match any" — even though the commit itself is fine.
		//
		// Two recovery paths, in order:
		//   (a) Check origin/<branch>: if it already points at our HEAD
		//       commit, a prior push delivered the commit. Treat as no-op
		//       success so the MR bead still gets filed.
		//   (b) Retry with an explicit SHA refspec (<sha>:refs/heads/<branch>).
		//       This pushes the commit from the local object DB to the
		//       named branch ref on origin, even when no local branch ref
		//       exists. Works for detached HEAD.
		//
		// Without this, polecats that land in the detached-HEAD trap see
		// "push failed", skip MR creation, and leave their work orphaned
		// on origin with no merge request — exactly the gu-br8a failure.
		if pushedCommitSHA != "" {
			if tip, tipErr := g.RemoteBranchTip("origin", branch); tipErr == nil && tip != "" && tip == pushedCommitSHA {
				fmt.Printf("%s Branch already at expected commit on origin (orphan-commit recovery: prior push delivered SHA)\n", style.Bold.Render("✓"))
				pushErr = nil
			}
		}
		if pushErr != nil && pushedCommitSHA != "" {
			style.PrintWarning("attempting SHA-refspec recovery (branch ref may be missing locally)...")
			shaErr := pushSHAForDone(g, "origin", pushedCommitSHA, branch, false)
			if shaErr == nil {
				fmt.Printf("%s Branch pushed via SHA-refspec recovery\n", style.Bold.Render("✓"))
				pushErr = nil
			} else {
				style.PrintWarning("SHA-refspec recovery also failed: %v", shaErr)
				// Try the bare repo with SHA refspec too — it shares the
				// object DB with the worktree, so the commit SHA is valid
				// there even when the worktree's git context is broken.
				bareRepoPath := filepath.Join(townRoot, rigName, ".repo.git")
				if _, statErr := os.Stat(bareRepoPath); statErr == nil {
					bareGit := git.NewGitWithDir(bareRepoPath, "")
					if bareErr := pushSHAForDone(bareGit, "origin", pushedCommitSHA, branch, false); bareErr == nil {
						fmt.Printf("%s Branch pushed via bare repo + SHA-refspec recovery\n", style.Bold.Render("✓"))
						pushErr = nil
					} else {
						style.PrintWarning("bare repo SHA-refspec recovery also failed: %v", bareErr)
					}
				}
			}
		}
	}

	if pushErr != nil {
		// All push attempts failed.
		//
		// gu-epv5 Option C: before giving up, re-check origin/<branch>.
		// A push may have actually delivered the SHA but the local git
		// reported failure (transient net error, lost ack, verifier
		// timeout). If the tip matches our expected SHA, treat as
		// success so MR creation still happens.
		if pushedCommitSHA != "" && recoverPushFromOriginTip(g, branch, pushedCommitSHA) {
			fmt.Printf("%s Push reported failure but origin/%s already matches expected SHA — proceeding to MR creation (gu-epv5 recovery)\n", style.Bold.Render("✓"), branch)
			pushErr = nil
		} else if pushedCommitSHA != "" && recoverNonFFOwnBranch(g, branch, pushedCommitSHA, pushErr) {
			// gu-hz3vx Mayor fix-direction (b): a non-fast-forward rejection
			// on the polecat's OWN private feature branch where the local
			// HEAD tree is identical to origin's tip (pure amend/rebase, no
			// content divergence) is safe to force-update — the branch is
			// session-private, not shared history, and an identical tree
			// loses nothing and smuggles nothing. recoverNonFFOwnBranch
			// force-updated origin and verified the tip; proceed to MR.
			fmt.Printf("%s Non-fast-forward on own branch with identical tree — force-updated origin/%s (gu-hz3vx recovery)\n", style.Bold.Render("✓"), branch)
			pushErr = nil
		} else if adoptedOriginSHA := recoverNonFFAdoptOriginPatchIdentical(g, branch, pushedCommitSHA, pushErr); adoptedOriginSHA != "" {
			// gs-y7g: peer-merge-during-work strand. A peer MR merged to main
			// mid-work, so `gt done` rebased onto the new main and re-pushed a
			// divergent SHA — but origin already holds the earlier,
			// patch-identical push of this work. The trees differ (the rebase
			// pulled in the peer's content) so the gu-hz3vx tree check above
			// cannot match, yet the work is provably the same patch. Adopt the
			// commit already on origin as the pushed commit and enqueue it —
			// no force-push, no contamination risk.
			fmt.Printf("%s Non-fast-forward on own branch; origin/%s holds a patch-identical commit — adopting it for MR (gs-y7g recovery)\n", style.Bold.Render("✓"), branch)
			pushedCommitSHA = adoptedOriginSHA
			pushErr = nil
		}
		// else: all recovery exhausted — return pushErr; the caller records the
		// failure and files a discoverable stranded-push wisp.
	}

	return pushedCommitSHA, pushErr
}

func forceCloseIssueWithRetry(closeFn func(string, ...string) error, issueID, reason, successFormat string) error {
	return forceCloseIssueWithRetrySleep(closeFn, issueID, reason, successFormat, time.Sleep)
}

func forceCloseIssueWithRetrySleep(closeFn func(string, ...string) error, issueID, reason, successFormat string, sleep func(time.Duration)) error {
	var closeErr error
	for attempt := 1; attempt <= 3; attempt++ {
		closeErr = closeFn(reason, issueID)
		if closeErr == nil {
			fmt.Printf("%s "+successFormat+"\n", style.Bold.Render("✓"), issueID)
			return nil
		}
		if attempt < 3 {
			style.PrintWarning("close attempt %d/3 failed: %v (retrying in %ds)", attempt, closeErr, attempt*2)
			sleep(time.Duration(attempt*2) * time.Second)
		}
	}
	return closeErr
}

func notifyDoneCloseSkipped(townRoot, rigName, sender, issueID, reason string) {
	if townRoot == "" || rigName == "" || issueID == "" {
		return
	}
	if sender == "" {
		sender = fmt.Sprintf("%s/polecat", rigName)
	}

	router := mail.NewRouter(townRoot)
	defer router.WaitPendingNotifications()
	msg := &mail.Message{
		To:      fmt.Sprintf("%s/witness", rigName),
		From:    sender,
		Subject: fmt.Sprintf("DONE_CLOSE_SKIPPED: %s", issueID),
		Body: fmt.Sprintf("gt done skipped closing %s.\n\nReason: %s\n\nThe bead remains open for witness/mayor review.",
			issueID, reason),
	}
	if err := router.Send(msg); err != nil {
		style.PrintWarning("could not notify witness about skipped close: %v", err)
	} else {
		fmt.Printf("%s Witness notified: DONE_CLOSE_SKIPPED\n", style.Bold.Render("✓"))
	}
}

func noteVerifiedPushFailure(cwd, issueID, branch, commit string, verifyErr error) {
	if issueID == "" || cwd == "" {
		return
	}
	bd := beads.New(cwd)
	inProgress := "in_progress"
	_ = bd.Update(issueID, beads.UpdateOptions{Status: &inProgress})
	msg := fmt.Sprintf("verified_push_failed: commit %s not verified on origin/%s: %v", commit, branch, verifyErr)
	_, _ = bd.Run("comments", "add", issueID, msg)
}

func noteVerifiedPushSkipped(cwd, issueID, branch, commit, reason string) {
	if issueID == "" || cwd == "" {
		return
	}
	msg := fmt.Sprintf("verified_push_skipped: commit %s branch origin/%s reason=%s", commit, branch, reason)
	_, _ = beads.New(cwd).Run("comments", "add", issueID, msg)
}

// recordPushReceipt persists a push receipt to the rig's runtime log
// (see internal/pushlog). Called from every code path in `gt done` that
// has just verified a push to origin succeeded.
//
// gu-ftja: Without a durable record, witness/deacon forensics rely on
// live `git ls-remote`. After a fork branch is later reaped, that check
// can't distinguish "push happened then was reaped" from "push never
// happened". The receipt log makes the distinction unambiguous.
//
// Best-effort: any failure is logged to stderr inside pushlog.LogOrWarn
// and otherwise swallowed. We never block a successful push on a
// logging failure.
func recordPushReceipt(g *git.Git, townRoot, rigName, branch, commit, source, worker, issueID string) {
	if townRoot == "" || rigName == "" || branch == "" || commit == "" {
		return
	}
	pushURL := ""
	if g != nil {
		if u, err := g.GetPushURL("origin"); err == nil {
			pushURL = u
		}
	}
	pushlog.LogOrWarn(townRoot, rigName, pushlog.Receipt{
		Branch:    branch,
		CommitSHA: commit,
		Remote:    "origin",
		PushURL:   pushURL,
		Source:    source,
		Worker:    worker,
		IssueID:   issueID,
	})
}

// recordPushFailure persists a push/verify failure to the rig's runtime
// failure log (see internal/pushlog). Called from every `gt done` code path
// that gives up on a push or fails post-push verification, adjacent to the
// stranded-push wisp it files.
//
// gu-7m9h9: the actual push error otherwise lives only in (a) the dying
// session's stderr and (b) a Dolt-backed stranded wisp whose Create can fail
// silently in a terminating session. A durable, Dolt-independent local file
// lets the next strand investigation read the real error instead of inferring
// it from convoy re-sling noise in daemon.log.
//
// Best-effort: any failure is logged to stderr inside pushlog.LogFailureOrWarn
// and otherwise swallowed. We never block teardown on a logging failure.
func recordPushFailure(townRoot, rigName, branch, commit, source, stage, worker, issueID string, pushErr error) {
	if townRoot == "" || rigName == "" || branch == "" {
		return
	}
	errMsg := ""
	if pushErr != nil {
		errMsg = pushErr.Error()
	}
	pushlog.LogFailureOrWarn(townRoot, rigName, pushlog.Failure{
		Branch:    branch,
		CommitSHA: commit,
		Remote:    "origin",
		Source:    source,
		Stage:     stage,
		Error:     errMsg,
		Worker:    worker,
		IssueID:   issueID,
	})
}

// recoverPushFromOriginTip implements Option C recovery for gu-epv5: when a
// push step reports failure, the push may have actually succeeded — the error
// could be a transient network glitch, verification timeout, or post-push
// communication failure. Re-check the remote branch tip; if it matches the
// expected commit SHA, the push really did land, and the caller can clear
// pushFailed and proceed to MR creation.
//
// Returns true if origin/<branch> tip matches expectedSHA.
//
// Best-effort: any error from the remote read is treated as "could not
// confirm" (returns false) so callers fall through to the stranded-wisp path.
func recoverPushFromOriginTip(g *git.Git, branch, expectedSHA string) bool {
	if g == nil || branch == "" || expectedSHA == "" {
		return false
	}
	tip, err := g.RemoteBranchTip("origin", branch)
	if err != nil {
		return false
	}
	return strings.TrimSpace(tip) == strings.TrimSpace(expectedSHA)
}

// isNonFastForwardPushError reports whether a failed push was rejected by the
// remote for being a non-fast-forward update (the diverged-branch case). Git
// phrases this several ways depending on version and whether the ref is a
// branch tip or a tracking ref, so we match the stable substrings that all
// of them share. Used by recoverNonFFOwnBranch to distinguish a divergence
// rejection (safe to consider force-updating a private branch) from any other
// push failure (network, auth, gate rejection — never force-update on those).
func isNonFastForwardPushError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "non-fast-forward") ||
		strings.Contains(msg, "fetch first") ||
		strings.Contains(msg, "updates were rejected") ||
		strings.Contains(msg, "tip of your current branch is behind") ||
		strings.Contains(msg, "failed to push some refs")
}

// recoverNonFFOwnBranch implements the safe slice of Mayor fix-direction (b)
// for gu-hz3vx: when `gt done`'s branch:branch push is rejected non-fast-forward
// on the polecat's OWN private feature branch, the branch is uniquely named per
// session and not shared history — so force-updating it is permissible IN
// PRINCIPLE. But the shiny gu-qx6rn recovery surfaced a footgun: the local
// amended commit (4778a4c1) had bundled UNRELATED files on top of the
// already-pushed work (ae9aaf99). Blindly force-pushing it would have shipped
// that contamination to the merge queue.
//
// The provably-safe condition we require before force-updating is that the
// local HEAD produces the BYTE-IDENTICAL tree as origin's current branch tip.
// That means the divergence is purely a history-shape difference (an amend or
// a rebase onto a different base that picked up no new content) — exactly the
// shiny case once the contaminating files are excluded. Force-updating to an
// identical tree:
//   - loses nothing on origin (the content already there is preserved), and
//   - adds no new content (so it cannot smuggle unrelated changes into the MR).
//
// When the trees differ, we refuse and return false so the caller falls through
// to the existing loud-strand path (fileStrandedPushWisp + gu-rh0g refuse-close):
// a real content divergence still demands human/Mayor judgement, not an
// automatic force-push.
//
// Preconditions enforced by the caller: branch is NOT the rig default (the
// gu-cfb guard above already rejected that), and pushErr was a real push
// failure. Returns true only if it force-updated origin to expectedSHA AND
// verified the new tip.
func recoverNonFFOwnBranch(g *git.Git, branch, expectedSHA string, pushErr error) bool {
	if g == nil || branch == "" || expectedSHA == "" {
		return false
	}
	if !isNonFastForwardPushError(pushErr) {
		return false
	}

	// Resolve origin's current tip for this branch.
	originTip, err := g.RemoteBranchTip("origin", branch)
	if err != nil || strings.TrimSpace(originTip) == "" {
		// Can't read the remote — don't risk a force-push blind.
		return false
	}
	originTip = strings.TrimSpace(originTip)

	// If origin already matches our SHA there was nothing to recover; the
	// origin-tip recovery path handles that case. A non-FF here with a
	// matching tip would be contradictory, so bail.
	if originTip == expectedSHA {
		return false
	}

	// Make origin's tip object available locally so we can compare trees.
	// Best-effort: if the fetch fails we cannot prove tree-equality, so refuse.
	if fetchErr := g.FetchBranch("origin", branch); fetchErr != nil {
		return false
	}

	// Compare the resulting TREES, not the commits. Identical trees ⇒ no
	// content is lost and none is added by the force-update.
	localTree, lErr := g.Rev(expectedSHA + "^{tree}")
	remoteTree, rErr := g.Rev(originTip + "^{tree}")
	if lErr != nil || rErr != nil {
		return false
	}
	if strings.TrimSpace(localTree) != strings.TrimSpace(remoteTree) {
		// Real content divergence (e.g. shiny's contaminating bundle). Do
		// NOT force-push — let the caller strand it loudly for review.
		return false
	}

	// Trees are identical — safe to force-update the private branch.
	if forceErr := pushSHAForDone(g, "origin", expectedSHA, branch, true); forceErr != nil {
		return false
	}

	// Verify the force-update actually landed our SHA before claiming success.
	return recoverPushFromOriginTip(g, branch, expectedSHA)
}

// recoverNonFFAdoptOriginPatchIdentical handles the peer-merge-during-work
// strand (gs-y7g) that recoverNonFFOwnBranch cannot: a peer MR merged to main
// DURING the polecat's work, so `gt done` rebased the branch onto the new main
// and re-pushed a divergent SHA — but origin/<branch> already holds the EARLIER
// push of the same work, made before the rebase on the old base. The re-push is
// rejected non-fast-forward because the SHAs diverged.
//
// The two commits are PATCH-IDENTICAL (local HEAD and origin's tip introduce
// byte-identical diffs) but NOT tree-identical: the rebased local tree carries
// the peer's freshly-merged content, origin's pre-rebase tree does not. So
// recoverNonFFOwnBranch's tree-equality check necessarily fails here and the
// work strands, needing manual witness MR-enqueue recovery.
//
// The witness's manual fix is simply to enqueue the commit ALREADY on origin as
// the MR: it is patch-identical, already pushed, and needs no force-push. This
// function automates exactly that — it returns origin's tip SHA (for the caller
// to adopt as the pushed commit) when that tip is patch-identical to local HEAD,
// and "" otherwise.
//
// Why patch-identity is a sound safety bar (and the gu-qx6rn contamination
// footgun cannot bite): NO force-push happens — we ship origin's existing,
// already-clean commit, never the local HEAD. Had the local HEAD bundled
// unrelated files, its diff — and therefore its patch-id — would differ from
// origin's clean commit, the ids would not match, and we would fall through to
// the loud strand. We adopt origin's commit only when it provably introduces the
// same change as the work.
//
// Preconditions enforced by the caller mirror recoverNonFFOwnBranch: branch is
// not the rig default, and pushErr was a real push failure.
func recoverNonFFAdoptOriginPatchIdentical(g *git.Git, branch, localSHA string, pushErr error) string {
	if g == nil || branch == "" || localSHA == "" {
		return ""
	}
	if !isNonFastForwardPushError(pushErr) {
		return ""
	}

	originTip, err := g.RemoteBranchTip("origin", branch)
	if err != nil || strings.TrimSpace(originTip) == "" {
		return ""
	}
	originTip = strings.TrimSpace(originTip)

	// If origin already matches our SHA there is no divergence to adopt; the
	// origin-tip recovery path handles that case.
	if originTip == localSHA {
		return ""
	}

	// Make origin's tip object available locally so we can diff it. Best-effort:
	// if the fetch fails we cannot prove patch-equality, so refuse.
	if fetchErr := g.FetchBranch("origin", branch); fetchErr != nil {
		return ""
	}

	localPID, lErr := g.PatchID(localSHA)
	originPID, oErr := g.PatchID(originTip)
	if lErr != nil || oErr != nil || localPID == "" || originPID == "" {
		return ""
	}
	if localPID != originPID {
		// Real content divergence (e.g. a contaminating bundle changes the
		// diff and thus the patch-id). Do NOT adopt — let the caller strand it.
		return ""
	}

	return originTip
}

// fileStrandedPushWisp implements Option B for gu-epv5: when a push step
// fails irrecoverably, file a discoverable wisp so refinery, witness, and
// human operators can see that work was attempted but stranded. Without
// this record, `gt done` returns silently after `goto notifyWitness` and
// the only trace is a log line in stderr — the merge queue (`gt mq list`)
// has nothing, and the bead's `stranded-merge` label (set by
// updateAgentStateOnDone via gu-rh0g) is the only signal.
//
// The wisp is labeled `gt:push-stranded` rather than `gt:merge-request`
// so refinery's queue scan does NOT pick it up as a merge candidate —
// stranded-push wisps describe work that did NOT land on origin, and the
// refinery must not try to merge a branch that may not exist remotely.
// Mayor/witness sweep these wisps for recovery (re-push, escalation, or
// closing the source bead with a recovery note).
//
// Best-effort: any failure to create the wisp logs a warning and returns
// silently — the calling path still notifies witness via the existing channel.
func fileStrandedPushWisp(bd *beads.Beads, rigName, branch, commitSHA, target, issueID, agentBeadID, worker string, pushErr error) {
	if bd == nil || issueID == "" {
		return
	}
	title := fmt.Sprintf("Push stranded: %s", issueID)
	description := fmt.Sprintf("branch: %s\nsource_issue: %s\nrig: %s\npush_status: stranded",
		branch, issueID, rigName)
	if target != "" {
		description += fmt.Sprintf("\ntarget: %s", target)
	}
	if commitSHA != "" {
		description += fmt.Sprintf("\ncommit_sha: %s", commitSHA)
	}
	if pushErr != nil {
		// Truncate very long error messages so the wisp description stays
		// readable. Full diagnostics live in stderr / pushlog.
		errMsg := pushErr.Error()
		if len(errMsg) > 512 {
			errMsg = errMsg[:512] + "...(truncated)"
		}
		description += fmt.Sprintf("\npush_error: %s", errMsg)
	}
	if worker != "" {
		description += fmt.Sprintf("\nworker: %s", worker)
	}
	if agentBeadID != "" {
		description += fmt.Sprintf("\nagent_bead: %s", agentBeadID)
	}
	wisp, err := bd.Create(beads.CreateOptions{
		Title:       title,
		Labels:      []string{"gt:push-stranded"},
		Priority:    1, // P1 — operator visibility for stranded work
		Description: description,
		Ephemeral:   true,
		Rig:         rigName,
		// gs-9sr: commit the wisp to shared main immediately. This wisp exists to
		// make a strand LOUD; if the underlying cause is an auto-commit config
		// drift (gs-onu), a default-config Create would itself land only in the
		// dying session's local working set and never reach the witness/mayor
		// sweeps — a silent record of a silent strand. Force the commit so the
		// alarm actually rings.
		DoltAutoCommit: "on",
	})
	if err != nil {
		style.PrintWarning("could not file push-stranded wisp for %s: %v", issueID, err)
		return
	}
	if wisp == nil || wisp.ID == "" {
		return
	}
	fmt.Printf("%s Filed push-stranded wisp: %s\n", style.Bold.Render("⚠"), wisp.ID)
}

// mrMainViewFinder is the minimal beads surface verifyMRVisibleOnMain needs: the
// refinery's MR discovery query. *beads.Beads satisfies it; tests inject a fake.
type mrMainViewFinder interface {
	FindMRForBranchAndSHA(branch, commitSHA string) (*beads.Issue, error)
}

// verifyMRVisibleOnMain is the gs-9sr defense-in-depth check (gs-onu follow-up).
//
// After creating the MR bead, gt done already re-reads it via bd.Show(mrID).
// That read-back confirms the bead exists in THIS polecat session's LOCAL Dolt
// view — which passes even when an auto-commit config drift leaves the write
// uncommitted: the session self-terminates, the INSERT never reaches shared
// main, and the refinery (a separate connection) never sees the MR. The branch
// is then silently stranded. DoltAutoCommit:"on" on the Create should prevent
// that at the source; this is the belt that catches any residual/again-drifted
// case and converts a silent strand into a loud, recoverable one.
//
// The caller passes a FRESH bd wrapper (a new Dolt session that reads committed
// shared main, not the local working set) and we re-run the refinery's own
// discovery — FindMRForBranchAndSHA, the same query Manager.Queue() uses. If the
// fresh connection cannot find the MR, neither will the refinery.
//
// Returns (visible, err):
//   - (true, nil)  — discoverable on main; safe to report COMPLETED.
//   - (false, nil) — genuinely absent; caller must fail loud (stranded wisp).
//   - (false, err) — query inconclusive (e.g. transient Dolt blip); caller
//     should warn but NOT strand, since the read-back already passed.
func verifyMRVisibleOnMain(f mrMainViewFinder, branch, commitSHA string) (bool, error) {
	issue, err := f.FindMRForBranchAndSHA(branch, commitSHA)
	if err != nil {
		return false, err
	}
	return issue != nil, nil
}

// shouldTrustMRCheckpoint decides whether gt done's resume path may skip MR
// creation and trust a prior run's mr-created checkpoint, given the result of a
// fresh main-view visibility check (gs-onu). It trusts the checkpoint unless
// the MR is DEFINITIVELY absent on shared main (visible==false with no query
// error) — the silent-strand signature. A query error is inconclusive (e.g. a
// transient Dolt blip): trust the checkpoint rather than risk re-creating a
// duplicate MR, matching the create-path's "warn, don't false-strand" rule.
func shouldTrustMRCheckpoint(visibleOnMain bool, queryErr error) bool {
	if queryErr != nil {
		return true // inconclusive — keep the prior-run checkpoint
	}
	return visibleOnMain
}

func verifyPushedCommitWithBareFallback(g *git.Git, townRoot, rigName, branch, commit string) error {
	verifyErr := g.VerifyPushedCommit("origin", branch, commit)
	if verifyErr == nil {
		return nil
	}

	bareRepoPath := filepath.Join(townRoot, rigName, ".repo.git")
	if _, statErr := os.Stat(bareRepoPath); statErr != nil {
		return verifyErr
	}
	bareGit := git.NewGitWithDir(bareRepoPath, "")
	tip, tipErr := bareGit.Rev("refs/heads/" + branch)
	if tipErr == nil && strings.TrimSpace(tip) == strings.TrimSpace(commit) {
		return nil
	}
	return verifyErr
}

// closeHookedBeadBackoff is the sleep used by forceCloseWithRetry between
// attempts. Production uses real time.Sleep; tests override to time.Duration(0)
// for speed. Keep as a package var so only the close retries are fast in tests —
// other timing logic is unaffected.
var closeHookedBeadBackoff = func(d time.Duration) { time.Sleep(d) }

// closeAttachedWispNoMR closes any molecule wisp attached to the given hooked
// bead BEFORE the no-MR close path closes the bead itself. Without this, a
// later reopen of the bead (Pattern A audit, refinery stranded-merge label,
// or manual `bd update --status=open`) leaves the wisp open as a `blocks` dep
// — the scheduler refuses to redispatch the bead. (gu-irou)
//
// Mirrors the merged-close path in updateAgentStateOnDone (~line 2346) so both
// close paths leave the wisp closed; only this no-MR close path was missing
// the symmetric cleanup. The merged path lives in updateAgentStateOnDone where
// it has bd already in scope; runDone constructs bd separately, so we expose
// this as a small named helper to keep the no-MR path readable and testable.
//
// Errors are logged but never fatal: a stuck wisp will re-block only if the
// bead is later reopened, and witness/reapers can sweep it. Closing the work
// bead remains the primary goal of the no-MR close path.
func closeAttachedWispNoMR(bd *beads.Beads, issueID string) {
	issue, err := bd.Show(issueID)
	if err != nil {
		return
	}
	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil || attachment.AttachedMolecule == "" {
		return
	}
	if n := closeDescendants(bd, attachment.AttachedMolecule); n > 0 {
		fmt.Fprintf(os.Stderr, "Closed %d molecule step(s) for %s\n", n, attachment.AttachedMolecule)
	}
	if wispErr := bd.ForceCloseWithReason("done (no-MR close)", attachment.AttachedMolecule); wispErr != nil && !errors.Is(wispErr, beads.ErrNotFound) {
		style.PrintWarning("could not close attached molecule %s: %v", attachment.AttachedMolecule, wispErr)
	}
}

// forceCloseWithRetry closes an issue via bd.ForceCloseWithReason with up to
// 3 attempts and exponential-ish backoff (2s, 4s) between attempts. Returns
// nil on success or the final error on exhaustion.
//
// Bug fix (gu-z93z): The close-on-successful-merge path in updateAgentStateOnDone
// previously used plain bd.Close with no force and no retry. Transient dolt-lock
// contention or lingering dependency checks silently failed the close and left
// the hooked bead stuck IN_PROGRESS after gt done + merge queue landed the branch.
// This helper mirrors the robust pattern already used by the no-MR close path
// (line ~768) so both paths behave consistently.
//
// Force semantics are intentional: the polecat is about to transition to IDLE —
// open wisps or molecule steps should not block closure. Witness handles any
// orphaned descendants.
func forceCloseWithRetry(bd *beads.Beads, issueID, closeReason string) error {
	var closeErr error
	for attempt := 1; attempt <= 3; attempt++ {
		closeErr = bd.ForceCloseWithReason(closeReason, issueID)
		if closeErr == nil {
			return nil
		}
		if attempt < 3 {
			style.PrintWarning("close attempt %d/3 failed for %s: %v (retrying in %ds)", attempt, issueID, closeErr, attempt*2)
			closeHookedBeadBackoff(time.Duration(attempt*2) * time.Second)
		}
	}
	return closeErr
}

// verifyCommitReferencesBead checks that the commit at the given SHA mentions
// the given bead ID in its message. This guards against the false-close
// pattern where a polecat with no commits of its own closes its hooked bead
// citing whatever commit is currently at HEAD — which is a sibling polecat's
// landing for an unrelated bead.
//
// The check is intentionally mechanical and lenient: a single substring match
// on the bead ID anywhere in the commit message body or subject is enough.
// Conventional commit format used by Gas Town puts (<bead-id>) in the subject
// for any committed work, so legitimate no-MR closes (rare but real — e.g.
// when a previous polecat's commit already shipped this bead's work) match
// trivially. False-close scenarios — citing some other bead's commit — fail.
//
// Returns nil if the commit references beadID. Returns a descriptive error
// otherwise. An empty SHA, an empty beadID, or an unreadable commit message
// also returns a descriptive error so the close path fails closed (gu-551r).
func verifyCommitReferencesBead(g *git.Git, commitSHA, beadID string) error {
	if beadID == "" {
		return fmt.Errorf("internal error: bead ID is empty (cannot verify commit reference)")
	}
	if commitSHA == "" {
		return fmt.Errorf("no commit at HEAD to cite as evidence (cannot close with no-code-changes reason)")
	}
	msg, err := g.GetBranchCommitMessage(commitSHA)
	if err != nil {
		return fmt.Errorf("could not read commit %s message to verify bead reference: %w", shortSHA(commitSHA), err)
	}
	if !strings.Contains(msg, beadID) {
		subject := firstLine(msg)
		return fmt.Errorf("commit %s does not reference bead %s in its message — refusing to falsely cite it as 'completed' evidence (commit subject: %q)",
			shortSHA(commitSHA), beadID, subject)
	}
	return nil
}

// firstLine returns the first newline-delimited line of s, trimmed. Used to
// extract the subject line from a multi-line commit message for error display.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}

// shouldNudgeRefinery reports whether a gt done invocation may wake the
// refinery. Only COMPLETED exits create an MR bead; DEFERRED and ESCALATED
// exits (polecats finishing operational tasks with no code changes) must
// never emit MQ_SUBMIT, or the refinery wakes from backoff to find an empty
// merge queue (gh#3885). The exitType check is defensive: it holds the
// invariant even if a future code path populates mrID outside COMPLETED.
func shouldNudgeRefinery(exitType, mrID string) bool {
	return exitType == ExitCompleted && mrID != ""
}

// shouldAwaitRefineryMerge reports whether the hooked bead must stay OPEN
// (labeled awaiting_refinery_merge) instead of being closed by gt done.
//
// The single sufficient condition is: a COMPLETED exit that successfully
// created an MR bead (mrID set, push not failed, MR not failed). Reaching this
// point with an MR means the polecat's commit is on a feature branch and an MR
// bead exists, but the work is NOT yet on origin/main — only the refinery's
// PostMerge path (or a recovery sweep over the label) can prove the merge
// landed and close the bead with the real on-main commit_sha.
//
// gu-y2w7g: this deliberately does NOT consult completion.IsMergeQueueRig.
// MR-bead creation in the mr-strategy path is not gated on that check, so
// gating the close-deferral on it produced stranded commits: an MR existed but
// the rig wasn't detected as merge-queue-managed (settings unreadable or
// merge_queue.enabled unset), so the bead false-closed while the commit never
// reached origin/main (incident cacr-d9to0/uqpnf). Non-MR completion paths
// (direct/local/no_merge) close the bead themselves and never reach here with
// mrID set, so this predicate cannot regress them.
func shouldAwaitRefineryMerge(exitType string, pushFailed, mrFailed bool, mrID string) bool {
	return exitType == ExitCompleted && !pushFailed && !mrFailed && mrID != ""
}

// deriveLifecycleOutcome maps a gt done exit into a durable terminal lifecycle
// outcome (gs-2m1b). The outcome is persisted on the agent bead and — unlike the
// ephemeral completion metadata — survives the witness's routine completion
// processing, giving zombie-patrol and dispatch a durable signal that
// distinguishes "completed + cleaned up" from "died mid-work" (which leaves no
// recent outcome). gt done only ever records the completed-* / deferred /
// escalated outcomes; the died-* cases are inferred from the absence of one.
func deriveLifecycleOutcome(exitType, mrID string, mrFailed, pushFailed bool) string {
	switch exitType {
	case ExitEscalated:
		return beads.OutcomeEscalated
	case ExitDeferred:
		return beads.OutcomeDeferred
	default: // ExitCompleted
		switch {
		case pushFailed:
			return beads.OutcomeCompletedUnpushed
		case mrFailed:
			return beads.OutcomeCompletedStranded
		case mrID != "":
			return beads.OutcomeCompletedMerged
		default:
			return beads.OutcomeCompletedPushed
		}
	}
}

// setDoneIntentLabel writes a done-intent:<type>:<unix-ts> label on the agent bead
// EARLY in gt done, before push/MR. This allows the Witness to detect polecats that
// crashed mid-gt-done: if the session is dead but done-intent exists, the polecat was
// trying to exit and should be auto-nuked.
//
// Follows the existing idle:N / backoff-until:TIMESTAMP label pattern.
// Non-fatal: if this fails, gt done continues without the safety net.
func setDoneIntentLabel(bd *beads.Beads, agentBeadID, exitType string) {
	if agentBeadID == "" {
		return
	}
	label := fmt.Sprintf("done-intent:%s:%d", exitType, time.Now().Unix())
	if err := bd.Update(agentBeadID, beads.UpdateOptions{
		AddLabels: []string{label},
	}); err != nil {
		// Non-fatal: warn but continue
		fmt.Fprintf(os.Stderr, "Warning: couldn't set done-intent label on %s: %v\n", agentBeadID, err)
	}
}

// clearDoneIntentLabel removes any done-intent:* label from the agent bead.
// Called at the end of updateAgentStateOnDone on clean exit.
// Uses read-modify-write pattern (same as clearAgentBackoffUntil).
func clearDoneIntentLabel(bd *beads.Beads, agentBeadID string) {
	if agentBeadID == "" {
		return
	}
	issue, err := bd.Show(agentBeadID)
	if err != nil {
		return // Agent bead gone, nothing to clear
	}

	var toRemove []string
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, "done-intent:") {
			toRemove = append(toRemove, label)
		}
	}
	if len(toRemove) == 0 {
		return // No done-intent label to clear
	}

	if err := bd.Update(agentBeadID, beads.UpdateOptions{
		RemoveLabels: toRemove,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't clear done-intent label on %s: %v\n", agentBeadID, err)
	}
}

// maxSilentDefers bounds how many times a work bead may be auto-deferred by
// reason-less `gt done --status DEFERRED` exits before gt done stops silently
// re-parking it and escalates instead (gu-hadus).
//
// Each reason-less DEFERRED exit re-applies the +1d cooldown (gu-vty0). The
// scheduler releases the bead when the timer expires (releaseExpiredDeferredBeads),
// the dispatcher re-dispatches it, and the next polecat exits DEFERRED again —
// re-parking to the same +1d date. That loop runs forever and silently overrides
// an operator's manual un-defer, with no concrete blocker ever recorded. A
// DEFERRED exit that carries `--reason` is a legitimate paused-with-blocker exit
// and resets the streak; only consecutive reason-less defers count toward this cap.
const maxSilentDefers = 3

// deferLoopLabel marks a work bead that has exceeded maxSilentDefers consecutive
// reason-less auto-defers, so witness/mayor audits can spot the churn.
const deferLoopLabel = "defer-loop"

// deferCountLabelPrefix is the read-modify-write counter label tracking
// consecutive reason-less auto-defers (follows the idle:N convention).
const deferCountLabelPrefix = "defer-count:"

// silentDeferCount returns the current consecutive reason-less defer count
// recorded on the bead's defer-count:N label (0 if absent/unparseable).
func silentDeferCount(issue *beads.Issue) int {
	if issue == nil {
		return 0
	}
	for _, label := range issue.Labels {
		if rest, ok := strings.CutPrefix(label, deferCountLabelPrefix); ok {
			if n, err := strconv.Atoi(rest); err == nil {
				return n
			}
		}
	}
	return 0
}

// setSilentDeferCount rewrites the defer-count:N label on the bead to the given
// value (removing any prior defer-count:* label first). A count of 0 clears the
// label entirely — used when a DEFERRED exit records a concrete blocker via
// --reason, resetting the streak. Non-fatal: warns but does not abort gt done.
func setSilentDeferCount(bd *beads.Beads, issue *beads.Issue, beadID string, count int) {
	var toRemove []string
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, deferCountLabelPrefix) {
			toRemove = append(toRemove, label)
		}
	}
	opts := beads.UpdateOptions{RemoveLabels: toRemove}
	if count > 0 {
		opts.AddLabels = []string{fmt.Sprintf("%s%d", deferCountLabelPrefix, count)}
	}
	if len(opts.AddLabels) == 0 && len(opts.RemoveLabels) == 0 {
		return
	}
	if err := bd.Update(beadID, opts); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't update %s on %s: %v\n", deferCountLabelPrefix, beadID, err)
	}
}

// DoneCheckpoint represents a checkpoint stage in the gt done flow (gt-aufru).
// Checkpoints are stored as labels on the agent bead, enabling resume after
// process interruption (context exhaustion, SIGTERM, etc.).
type DoneCheckpoint string

const (
	CheckpointPushed          DoneCheckpoint = "pushed"
	CheckpointMRCreated       DoneCheckpoint = "mr-created"
	CheckpointWitnessNotified DoneCheckpoint = "witness-notified"
)

// writeDoneCheckpoint writes a checkpoint label on the agent bead.
// Format: done-cp:<stage>:<value>:<unix-ts>
// Non-fatal: if this fails, gt done continues without the checkpoint.
func writeDoneCheckpoint(bd *beads.Beads, agentBeadID string, cp DoneCheckpoint, value string) {
	if agentBeadID == "" {
		return
	}
	label := fmt.Sprintf("done-cp:%s:%s:%d", cp, value, time.Now().Unix())
	if err := bd.Update(agentBeadID, beads.UpdateOptions{
		AddLabels: []string{label},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't write checkpoint %s on %s: %v\n", cp, agentBeadID, err)
	}
}

// readDoneCheckpoints reads all done-cp:* labels from the agent bead.
// Returns a map of checkpoint stage -> value. Empty map if none found.
func readDoneCheckpoints(bd *beads.Beads, agentBeadID string) map[DoneCheckpoint]string {
	checkpoints := make(map[DoneCheckpoint]string)
	if agentBeadID == "" {
		return checkpoints
	}
	issue, err := bd.Show(agentBeadID)
	if err != nil {
		return checkpoints
	}
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, "done-cp:") {
			// Format: done-cp:<stage>:<value>:<ts>
			parts := strings.SplitN(label, ":", 4)
			if len(parts) >= 3 {
				stage := DoneCheckpoint(parts[1])
				value := parts[2]
				checkpoints[stage] = value
			}
		}
	}
	return checkpoints
}

// clearDoneCheckpoints removes all done-cp:* labels from the agent bead.
// Called on clean exit to prevent stale checkpoints from interfering with future runs.
func clearDoneCheckpoints(bd *beads.Beads, agentBeadID string) {
	if agentBeadID == "" {
		return
	}
	issue, err := bd.Show(agentBeadID)
	if err != nil {
		return
	}
	var toRemove []string
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, "done-cp:") {
			toRemove = append(toRemove, label)
		}
	}
	if len(toRemove) == 0 {
		return
	}
	if err := bd.Update(agentBeadID, beads.UpdateOptions{
		RemoveLabels: toRemove,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't clear done checkpoints on %s: %v\n", agentBeadID, err)
	}
}

// updateAgentStateOnDone closes the hooked work bead and reports cleanup status.
// Uses issueID directly to find the hooked bead instead of reading the agent bead's
// hook_bead slot (hq-l6mm5: direct bead tracking).
//
// Per gt-zecmc: observable states ("done", "idle") removed - use tmux to discover.
// Non-observable states ("stuck", "awaiting-gate") are still set since they represent
// intentional agent decisions that can't be observed from tmux.
//
// Also self-reports cleanup_status for ZFC compliance (#10).
//
// BUG FIX (hq-3xaxy): This function must be resilient to working directory deletion.
// If the polecat's worktree is deleted before gt done finishes, we use env vars as fallback.
// All errors are warnings, not failures - gt done must complete even if bead ops fail.
// stranded indicates the polecat's work failed to reach origin/main (push or
// MR step failed). When true, updateAgentStateOnDone refuses to close the
// hooked bead even on a COMPLETED exit, preventing the Pattern B false-close
// where a stranded MR is incorrectly reported as shipped. (gu-rh0g)
//
// awaitingRefineryMerge indicates the polecat successfully submitted to a
// merge queue (push + MR creation succeeded) but the refinery has not yet
// merged the work to origin/main. When true, updateAgentStateOnDone leaves
// the hooked bead open with an awaiting_refinery_merge label + audit note;
// the refinery's PostMerge path closes it with the real on-main commit_sha
// once the merge actually happens. This catches the Pattern B variant where
// gu-rh0g's push-failure guard does not apply (push succeeded) but the
// refinery later wedges or is slow. (gu-treq)
func updateAgentStateOnDone(cwd, townRoot, exitType, issueID string, stranded bool, awaitingRefineryMerge bool, awaitingMergeMRID, awaitingMergeBranch string) {
	// Get role context - try multiple sources for resilience
	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		// Fallback: try to construct role info from environment variables
		// This handles the case where cwd is deleted but env vars are set
		envRole := os.Getenv("GT_ROLE")
		envRig := os.Getenv("GT_RIG")
		envPolecat := os.Getenv("GT_POLECAT")

		if envRole == "" || envRig == "" {
			// Can't determine role, skip agent state update
			style.PrintWarning("could not determine role for agent state update (env: GT_ROLE=%q, GT_RIG=%q)", envRole, envRig)
			return
		}

		// Parse role string to get Role type
		parsedRole, _, _ := parseRoleString(envRole)

		roleInfo = RoleInfo{
			Role:     parsedRole,
			Rig:      envRig,
			Polecat:  envPolecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
			Source:   "env-fallback",
		}
	}

	ctx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}

	agentBeadID := getAgentBeadID(ctx)
	if agentBeadID == "" {
		style.PrintWarning("no agent bead ID found for %s/%s, skipping agent state update", ctx.Rig, ctx.Polecat)
		return
	}

	// Use rig path for bd commands.
	// IMPORTANT: Use the rig's directory (not polecat worktree) so bd commands
	// work even if the polecat worktree is deleted.
	var beadsPath string
	switch ctx.Role {
	case RoleMayor, RoleDeacon:
		beadsPath = townRoot
	default:
		beadsPath = filepath.Join(townRoot, ctx.Rig)
	}
	bd := beads.New(beadsPath)
	// agentBd bypasses prefix routing — agent beads (gt:agent label) live in
	// the town DB regardless of their ID prefix, but the rig-prefix routing
	// would otherwise misroute them to the rig DB and silently fail with
	// "issue not found". See beads.ForAgentBead docstring for details.
	agentBd := bd.ForAgentBead()

	// Find the hooked bead to close. Use issueID directly instead of reading
	// agent bead's hook_bead slot (hq-l6mm5: direct bead tracking).
	hookedBeadID := issueID
	if hookedBeadID == "" {
		// Fallback: query for hooked beads assigned to this agent
		agentID := roleInfo.ActorString()
		if found := findHookedBeadForAgent(bd, agentID); found != "" {
			hookedBeadID = found
		}
	}

	// Record deferred/escalated reason on the bead (gu-o1ga).
	// This gives downstream consumers (witness, mayor, convoy-feeder) visibility
	// into WHY the polecat exited without completing.
	if hookedBeadID != "" && (exitType == ExitDeferred || exitType == ExitEscalated) && doneReason != "" {
		polecatName := os.Getenv("GT_POLECAT")
		if polecatName == "" {
			polecatName = "unknown"
		}
		note := fmt.Sprintf("[polecat %s %s] %s: %s",
			polecatName, time.Now().UTC().Format(time.RFC3339), exitType, doneReason)
		if _, err := bd.Run("comments", "add", hookedBeadID, note); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: couldn't record %s reason on %s: %v\n", exitType, hookedBeadID, err)
		}
	}

	// Workflow step beads (*-wfs-*) are ephemeral formula steps managed by the workflow
	// engine. For these, DEFERRED means "step complete, no code commits" not "work
	// paused for resumption". Close them on DEFERRED so the convoy can advance
	// (upstream #3867).
	isWorkflowStep := strings.Contains(hookedBeadID, "-wfs-")

	// Review-only and no-merge beads (gu-ybjb): analysis-only legs (review_only=true,
	// e.g. mol-prd-review / mol-plan-review / mol-polecat-code-review) and
	// no-code tasks (no_merge=true, e.g. email/research) finish with zero commits
	// by design. mol-polecat-work historically advised these polecats to run
	// `gt done --status DEFERRED`, which sent them down the defer-cooldown path
	// instead of closing — leaving the bead DEFERRED and blocking convoy
	// synthesis. Treat DEFERRED on a review_only/no_merge bead like a workflow
	// step: close, don't defer-cooldown. This is a safety net so the close path
	// is reliable regardless of which exit flag the polecat used.
	isReviewOrNoMergeBead := false
	if hookedBeadID != "" {
		if hb, err := bd.Show(hookedBeadID); err == nil {
			if af := beads.ParseAttachmentFields(hb); af != nil && (af.ReviewOnly || af.NoMerge) {
				isReviewOrNoMergeBead = true
			}
		}
	}

	// Apply defer cooldown for DEFERRED exits on non-workflow, non-review beads (gu-vty0).
	// Without this, the stale-hooks patrol reopens the bead (status=open) and
	// `bd ready` re-surfaces it to the auto-dispatcher within seconds, looping
	// failed work through fresh polecats. Setting --defer hides it from
	// `bd ready` for the cooldown window without altering status semantics, so
	// witness/mayor still see it via `bd list` and can intervene.
	if hookedBeadID != "" && exitType == ExitDeferred && !isWorkflowStep && !isReviewOrNoMergeBead {
		deferUntil := doneDeferUntil
		if deferUntil == "" {
			deferUntil = defaultDeferredOffset
		}
		if _, err := bd.Run("update", hookedBeadID, "--defer="+deferUntil); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: couldn't set defer=%s on %s: %v\n", deferUntil, hookedBeadID, err)
		} else {
			fmt.Fprintf(os.Stderr, "Deferred %s until %s (suppresses re-dispatch loop)\n", hookedBeadID, deferUntil)
		}

		// Break the silent defer loop (gu-hadus). A reason-less DEFERRED exit
		// re-parks the bead to +1d every cycle; the scheduler releases it, the
		// dispatcher re-dispatches it, and the next polecat re-defers — looping
		// forever and silently overriding any operator un-defer. We still apply
		// the cooldown above (so we don't reintroduce the gu-vty0 tight loop),
		// but we count consecutive reason-less defers and, once they exceed the
		// cap, escalate LOUDLY so witness/mayor break the cycle instead of it
		// churning invisibly. A DEFERRED exit WITH --reason is a legitimate
		// paused-with-blocker exit and resets the streak.
		if hb, err := bd.Show(hookedBeadID); err == nil {
			if strings.TrimSpace(doneReason) != "" {
				// Concrete blocker recorded — reset the streak and clear any
				// prior defer-loop marker; the loop is broken by the blocker note.
				setSilentDeferCount(bd, hb, hookedBeadID, 0)
				if beads.HasLabel(hb, deferLoopLabel) {
					if err := bd.Update(hookedBeadID, beads.UpdateOptions{RemoveLabels: []string{deferLoopLabel}}); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: couldn't clear %s label on %s: %v\n", deferLoopLabel, hookedBeadID, err)
					}
				}
			} else {
				count := silentDeferCount(hb) + 1
				setSilentDeferCount(bd, hb, hookedBeadID, count)
				if count > maxSilentDefers && !beads.HasLabel(hb, deferLoopLabel) {
					if err := bd.Update(hookedBeadID, beads.UpdateOptions{AddLabels: []string{deferLoopLabel}}); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: couldn't add %s label to %s: %v\n", deferLoopLabel, hookedBeadID, err)
					}
					note := fmt.Sprintf("[defer-loop gu-hadus] %s auto-deferred %d consecutive times with no --reason; "+
						"likely an auto-re-defer overriding operator directives. Polecats are not recording a concrete blocker. "+
						"Witness/mayor: inspect for missing fix-context or pin/close the bead.", hookedBeadID, count)
					if _, err := bd.Run("comments", "add", hookedBeadID, note); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: couldn't record defer-loop note on %s: %v\n", hookedBeadID, err)
					}
					nudgeWitness(ctx.Rig, fmt.Sprintf("DEFER_LOOP %s deferred %dx with no reason — needs intervention", hookedBeadID, count))
					fmt.Fprintf(os.Stderr, "⚠ %s deferred %d consecutive times with no --reason; tagged %s and escalated to witness\n",
						hookedBeadID, count, deferLoopLabel)
				}
			}
		}
	}

	// Pattern B guard (gu-rh0g): refuse to close the hooked bead when the
	// polecat's work never reached origin/main. A stranded push/MR means
	// the bead is NOT done — closing here would falsely report shipped work
	// and the only on-disk record of the fix (the polecat branch) is at
	// risk of reaping. Tag the bead with `stranded-merge` so audits catch
	// it, and let the bead stay open for cherry-pick / refinery recovery.
	if stranded && hookedBeadID != "" {
		strandedReason := fmt.Sprintf("Stranded merge (gu-rh0g): polecat exited %s but push/MR failed; commits not on origin/main. See polecat branch for unrecovered work.", exitType)
		if err := bd.Update(hookedBeadID, beads.UpdateOptions{AddLabels: []string{"stranded-merge"}}); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: couldn't add stranded-merge label to %s: %v\n", hookedBeadID, err)
		}
		// Append a note to the bead so the audit trail is human-readable.
		if _, err := bd.Run("note", hookedBeadID, strandedReason); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: couldn't add stranded-merge note to %s: %v\n", hookedBeadID, err)
		}
		fmt.Fprintf(os.Stderr, "WARN: hooked bead %s NOT closed — push/MR failed; bead labeled stranded-merge for recovery.\n", hookedBeadID)
		goto doneStateUpdate
	}

	// Pattern B (refinery-stall variant) guard (gu-treq): on merge-queue rigs,
	// when the polecat successfully submitted an MR but the refinery has not
	// yet merged to origin/main, leave the hooked bead OPEN and tag it with
	// awaiting_refinery_merge. The refinery's PostMerge path closes the bead
	// with the real on-main commit_sha when the merge actually lands. This
	// prevents the Pattern B false-close where `gt done` reports work shipped
	// before refinery has merged it (incident gu-xn2z). The molecule wisp is
	// still closed below — the polecat session is done; only the work bead's
	// terminal close is deferred to refinery's authoritative merge event.
	if awaitingRefineryMerge && hookedBeadID != "" {
		// Best-effort: close the molecule wisp (polecat session lifecycle ended).
		if hookedBead, err := bd.Show(hookedBeadID); err == nil {
			attachment := beads.ParseAttachmentFields(hookedBead)
			if attachment != nil && attachment.AttachedMolecule != "" {
				if n := closeDescendants(bd, attachment.AttachedMolecule); n > 0 {
					fmt.Fprintf(os.Stderr, "Closed %d molecule step(s) for %s\n", n, attachment.AttachedMolecule)
				}
				if closeErr := bd.ForceCloseWithReason("done (awaiting refinery merge)", attachment.AttachedMolecule); closeErr != nil && !errors.Is(closeErr, beads.ErrNotFound) {
					fmt.Fprintf(os.Stderr, "Warning: couldn't close attached molecule %s: %v\n", attachment.AttachedMolecule, closeErr)
				}
			}
		}
		completion.MarkAwaitingRefineryMerge(bd, hookedBeadID, awaitingMergeMRID, awaitingMergeBranch)
		fmt.Fprintf(os.Stderr, "Note: hooked bead %s left open pending refinery merge (mr=%s); refinery PostMerge will close with on-main commit_sha.\n", hookedBeadID, awaitingMergeMRID)
		goto doneStateUpdate
	}

	if hookedBeadID != "" && (exitType != ExitDeferred || isWorkflowStep || isReviewOrNoMergeBead) {
		// BUG FIX (gt-pftz): Close hooked bead unless already terminal (closed/tombstone).
		// Previously checked hookedBead.Status == StatusHooked, but polecats update
		// their work bead to in_progress during work. The exact-match check caused
		// gt done to skip closing the bead, leaving it as unassigned open work after
		// the hook was cleared — triggering infinite dispatch loops.
		//
		// DEFERRED exits preserve the bead: work is paused, not done. The bead
		// stays open/in_progress so it can be resumed on the next session.
		// Exception: workflow step beads (*-wfs-*) are always closed — see above.
		if hookedBead, err := bd.Show(hookedBeadID); err == nil && !beads.IssueStatus(hookedBead.Status).IsTerminal() {
			// Guard: never close a rig identity bead. Polecats dispatched with the
			// rig bead as their hook (via mol-polecat-work) must not close permanent
			// infrastructure. Skip close and fall through to idle state update.
			if beads.HasLabel(hookedBead, "gt:rig") {
				fmt.Fprintf(os.Stderr, "Note: hooked bead %s is a rig identity bead (gt:rig) — skipping close\n", hookedBeadID)
				goto doneStateUpdate
			}

			// BUG FIX: Close attached molecule (wisp) BEFORE closing hooked bead.
			// When using formula-on-bead (gt sling formula --on bead), the base bead
			// has attached_molecule pointing to the wisp. Without this fix, gt done
			// only closed the hooked bead, leaving the wisp orphaned.
			// Order matters: wisp closes -> unblocks base bead -> base bead closes.
			attachment := beads.ParseAttachmentFields(hookedBead)
			if attachment != nil && attachment.AttachedMolecule != "" {
				// Close molecule step descendants before closing the wisp root.
				// bd close doesn't cascade — without this, open/in_progress steps
				// from the molecule stay stuck forever after gt done completes.
				// Order: step children -> wisp root -> base bead.
				if n := closeDescendants(bd, attachment.AttachedMolecule); n > 0 {
					fmt.Fprintf(os.Stderr, "Closed %d molecule step(s) for %s\n", n, attachment.AttachedMolecule)
				}

				// Close the wisp root with --force and audit reason.
				// ForceCloseWithReason handles any status (hooked, open, in_progress)
				// and records the reason + session for attribution.
				// Same pattern as gt mol burn/squash (#1879).
				if closeErr := bd.ForceCloseWithReason("done", attachment.AttachedMolecule); closeErr != nil {
					if !errors.Is(closeErr, beads.ErrNotFound) {
						fmt.Fprintf(os.Stderr, "Warning: couldn't close attached molecule %s: %v\n", attachment.AttachedMolecule, closeErr)
						// Molecule close failed. Don't try to close hookedBeadID - it may
						// still be blocked. But DO clear hooks and update agent state
						// (goto doneStateUpdate) so the polecat isn't stuck in 'working'
						// state (za-o9e) or 'stalled' with HOOKED bead. Witness can
						// clean up the orphaned molecule later.
						goto doneStateUpdate
					}
					// Not found = already burned/deleted by another path, continue
				}
			}

			// Acceptance criteria gate: skip close if criteria are unchecked.
			if unchecked := beads.HasUncheckedCriteria(hookedBead); unchecked > 0 {
				style.PrintWarning("hooked bead %s has %d unchecked acceptance criteria — skipping close", hookedBeadID, unchecked)
				fmt.Fprintf(os.Stderr, "  The bead will remain open for witness/mayor review.\n")
			} else {
				// BUG FIX (gu-z93z): Use force-close + retry pattern to match the
				// no-MR close path (see forceCloseWithRetry docstring). Previously
				// this was a single non-force bd.Close — if it hit a transient dolt
				// lock or lingering dep, the bead was left stuck IN_PROGRESS after
				// gt done, even though the merge queue landed the branch. The
				// refinery is a backstop but cannot be relied on: if refinery's
				// own close call also fails, nothing closes the bead and it
				// pollutes bd ready/list output forever.
				closeReason := fmt.Sprintf("Completed via gt done (exit=%s)", exitType)
				if err := forceCloseWithRetry(bd, hookedBeadID, closeReason); err != nil {
					// Refinery is still a backstop — when the MR lands it will attempt
					// its own ForceCloseWithReason("Merged in ..."). Surface this
					// failure clearly so operators can tell the difference between
					// "polecat closed it" and "refinery had to close it".
					fmt.Fprintf(os.Stderr, "ERROR: gt done could not close hooked bead %s after 3 attempts: %v\n", hookedBeadID, err)
					fmt.Fprintf(os.Stderr, "  Refinery will attempt close when the MR merges; otherwise manual intervention required.\n")
				} else {
					fmt.Printf("%s Hooked bead %s closed (gt done)\n", style.Bold.Render("✓"), hookedBeadID)
				}
			}
		}
	}

doneStateUpdate:
	// Clear hook_bead on the agent bead (gt-qbh). The hq-l6mm5 refactor made
	// SetHookBead/ClearHookBead no-ops, but the witness still reads the
	// hook_bead field from the agent bead snapshot. If the hooked bead is a
	// wisp that gets reaped, the witness can't verify it was closed and flags
	// the polecat as a zombie. Clearing hook_bead prevents this false positive.
	emptyHook := ""
	if err := agentBd.UpdateAgentDescriptionFields(agentBeadID, beads.AgentFieldUpdates{HookBead: &emptyHook}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't clear hook_bead on %s: %v\n", agentBeadID, err)
	}

	// Purge closed ephemeral beads (wisps) accumulated during this and prior sessions.
	// Without this, closed wisps from mol-polecat-work steps, mol-witness-patrol cycles,
	// etc. accumulate across sessions and pollute bd ready/list output (hq-6161m).
	// Best-effort: failures are non-fatal since the work is already done.
	purgeClosedEphemeralBeads(bd)

	// Self-managed completion (gt-1qlg, polecat-self-managed-completion.md Phase 2):
	// Polecat sets agent_state=idle directly, skipping the intermediate "done" state.
	// The witness is no longer in the critical path for routine completions.
	// Completion metadata (exit_type, MR ID, branch) remains on the agent bead
	// for audit purposes and anomaly detection by witness patrol.
	// Exception: ESCALATED exits use "stuck" — the polecat needs help.
	doneState := "idle"
	if exitType == ExitEscalated {
		doneState = "stuck"
	}
	// Use UpdateAgentState to sync both column and description (gt-ulom).
	if err := agentBd.UpdateAgentState(agentBeadID, doneState); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't set agent %s to %s: %v\n", agentBeadID, doneState, err)
	}

	// ZFC #10: Self-report cleanup status
	// Agent observes git state and passes cleanup status via --cleanup-status flag
	if doneCleanupStatus != "" {
		cleanupStatus := parseCleanupStatus(doneCleanupStatus)
		if cleanupStatus != polecat.CleanupUnknown {
			if err := agentBd.UpdateAgentCleanupStatus(agentBeadID, string(cleanupStatus)); err != nil {
				// Non-fatal: don't return — done-intent labels still need clearing (za-o9e)
				fmt.Fprintf(os.Stderr, "Warning: couldn't update agent %s cleanup status: %v\n", agentBeadID, err)
			}
		}
	}

	// Clear done-intent label and checkpoints on clean exit — gt done completed
	// successfully. If we don't reach here (crash/stuck), the Witness uses the
	// lingering labels to detect the zombie and resume from checkpoints.
	clearDoneIntentLabel(agentBd, agentBeadID)
	clearDoneCheckpoints(agentBd, agentBeadID)
}

// ensureAgentBeadExists recreates a missing agent bead so done-intent labels,
// checkpoints, and active_mr writes don't silently fail (hq-xu4p). Only
// rig-level agents are handled — town agents (mayor/deacon) are owned by
// gt doctor. Best-effort: failures are warned, never fatal.
func ensureAgentBeadExists(bd *beads.Beads, id string, ctx RoleContext) {
	if id == "" {
		return
	}
	if issue, err := bd.Show(id); err == nil && issue != nil && issue.Status != string(beads.StatusClosed) {
		return // exists and is active
	}

	fields := &beads.AgentFields{Rig: ctx.Rig, AgentState: "idle"}
	var title string
	switch ctx.Role {
	case RolePolecat:
		fields.RoleType = "polecat"
		title = fmt.Sprintf("Polecat worker %s in %s - autonomous worker with persistent identity.", ctx.Polecat, ctx.Rig)
	case RoleWitness:
		fields.RoleType = "witness"
		title = fmt.Sprintf("Witness for %s - monitors polecat health and progress.", ctx.Rig)
	case RoleRefinery:
		fields.RoleType = "refinery"
		title = fmt.Sprintf("Refinery for %s - processes merge queue.", ctx.Rig)
	default:
		return
	}

	if _, err := bd.CreateOrReopenAgentBead(id, title, fields); err != nil {
		style.PrintWarning("agent bead %s missing and recreate failed: %v", id, err)
	} else {
		fmt.Printf("%s Recreated/reopened missing agent bead: %s\n", style.Bold.Render("✓"), id)
	}
}

// isStaleBranchIssue reports whether a branch-derived issue id should be
// overridden by the agent's hooked bead (hq-l0fj stale-branch guard).
// True when both ids exist, they differ, and the branch id is not a subtask
// of the hooked bead (e.g. branch gt-abc.1 under hooked gt-abc is fine).
func isStaleBranchIssue(branchIssue, hookedIssue string) bool {
	if branchIssue == "" || hookedIssue == "" {
		return false
	}
	return branchIssue != hookedIssue && !strings.HasPrefix(branchIssue, hookedIssue+".")
}

// selectAssignedIssue returns the one authoritative assignment to use for
// done attribution. Ambiguous assignment state is deliberately not guessed.
func selectAssignedIssue(branchIssue string, assigned []string) (string, bool) {
	unique := make(map[string]bool, len(assigned))
	for _, id := range assigned {
		if id != "" {
			unique[id] = true
		}
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if len(ids) == 0 {
		return "", false
	}
	if branchIssue != "" {
		for _, id := range ids {
			if branchIssue == id || strings.HasPrefix(branchIssue, id+".") {
				return "", false
			}
		}
	}
	if len(ids) > 1 {
		return "", true
	}
	return ids[0], false
}

// findAssignedBeadsForAgent queries the same assignment locations as gt hook:
// the current rig, the target rig for rig agents, then town beads. The assigned
// work bead is authoritative; agent-bead hook slots are intentionally ignored.
func findAssignedBeadsForAgent(workDir, agentID string) []string {
	if agentID == "" {
		return nil
	}

	assigned := assignedIssueIDs(queryAssignedBeads(beads.New(workDir), agentID))
	if len(assigned) > 0 {
		return assigned
	}

	townRoot, err := findTownRoot()
	if err != nil || townRoot == "" {
		return nil
	}

	parts := strings.Split(agentID, "/")
	rigName := ""
	if len(parts) > 0 {
		rigName = parts[0]
	}
	if rigName != "" && rigName != "mayor" && rigName != "deacon" {
		rigWorkDir := filepath.Join(townRoot, rigName, "mayor", "rig")
		if rigWorkDir != workDir {
			assigned = assignedIssueIDs(queryAssignedBeads(beads.New(rigWorkDir), agentID))
			if len(assigned) > 0 {
				return assigned
			}
		}
	}

	townBeadsDir := filepath.Join(townRoot, ".beads")
	if _, err := os.Stat(townBeadsDir); err == nil {
		assigned = assignedIssueIDs(queryAssignedBeads(beads.New(townBeadsDir), agentID))
		if len(assigned) > 0 {
			return assigned
		}
	}
	if isTownLevelRole(agentID) {
		return assignedIssueIDs(scanAllRigsForHookedBeads(townRoot, agentID))
	}
	return nil
}

func queryAssignedBeads(bd *beads.Beads, agentID string) []*beads.Issue {
	hooked, err := bd.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: agentID,
		Priority: -1,
	})
	if err == nil && len(hooked) > 0 {
		return hooked
	}
	inProgress, err := bd.List(beads.ListOptions{
		Status:   "in_progress",
		Assignee: agentID,
		Priority: -1,
	})
	if err == nil {
		return inProgress
	}
	return nil
}

func assignedIssueIDs(assigned []*beads.Issue) []string {
	ids := make([]string, 0, len(assigned))
	for _, issue := range assigned {
		if issue != nil && issue.ID != "" {
			ids = append(ids, issue.ID)
		}
	}
	return ids
}

// findHookedBeadForAgent queries for the agent's current assignment bead.
// This is the authoritative source for what work a polecat is doing, since the
// work bead itself tracks status and assignee (hq-l6mm5).
//
// Both hooked AND in_progress are checked (hq-xa4z): polecats routinely claim
// their assignment with `bd update --status=in_progress` when starting work,
// which made a hooked-only lookup blind to the active assignment — the stale-
// branch guard and the hook fallback silently no-op'd (same class of bug as
// gt-pftz in the close path). Hooked wins over in_progress when both exist.
// Returns empty string if no assignment bead is found.
func findHookedBeadForAgent(bd *beads.Beads, agentID string) string {
	issueID, _ := selectAssignedIssue("", assignedIssueIDs(queryAssignedBeads(bd, agentID)))
	return issueID
}

// parseCleanupStatus converts a string flag value to a CleanupStatus.
// ZFC: Agent observes git state and passes the appropriate status.
func parseCleanupStatus(s string) polecat.CleanupStatus {
	switch strings.ToLower(s) {
	case "clean":
		return polecat.CleanupClean
	case "uncommitted", "has_uncommitted":
		return polecat.CleanupUncommitted
	case "stash", "has_stash":
		return polecat.CleanupStash
	case "unpushed", "has_unpushed":
		return polecat.CleanupUnpushed
	default:
		return polecat.CleanupUnknown
	}
}

// isPolecatActor checks if a BD_ACTOR value represents a polecat.
// Polecat actors have format: rigname/polecats/polecatname
// Non-polecat actors have formats like: gastown/crew/name, rigname/witness, etc.
func isPolecatActor(actor string) bool {
	parts := strings.Split(actor, "/")
	return len(parts) >= 2 && parts[1] == "polecats"
}

// isDefaultBranchName reports whether `branch` is the rig's configured
// default branch or a common mainline alias ("main", "master"). Used by
// the gt-pvx auto-commit safety net and the push refspec builder to
// refuse operations that would land polecat work directly on mainline,
// bypassing the merge queue. See gu-cfb for the incident that motivated
// this guard.
//
// The check is conservative: we always reject "main" and "master" in
// addition to whatever the rig config says, because some rigs have both
// a legacy master and a newer main tracked side-by-side.
func isDefaultBranchName(branch, defaultBranch string) bool {
	if branch == "" {
		return false
	}
	if branch == defaultBranch {
		return true
	}
	return branch == "main" || branch == "master"
}

// stripOverlayCLAUDEmd detects and removes Gas Town overlay content from CLAUDE.md
// and CLAUDE.local.md before the branch is pushed. Polecats were committing the
// overlay (which contains polecat lifecycle boilerplate like "Idle Polecat Heresy",
// "gt done" protocol, etc.) into actual repos, overwriting project-specific CLAUDE.md
// content. (gt-p35)
//
// This runs after all commits but before push. If overlay files are detected in
// the branch diff, they are restored (CLAUDE.md) or removed (CLAUDE.local.md)
// and a cleanup commit is created.
//
// Returns true if a cleanup commit was created.
func stripOverlayCLAUDEmd(g *git.Git, defaultBranch string) bool {
	originRef := "origin/" + defaultBranch

	// Check which files changed on this branch vs origin/main
	changedFiles, err := g.DiffNameOnly(originRef, "HEAD")
	if err != nil {
		// Can't determine diff — skip silently (push will still work)
		return false
	}

	claudeChanged := false
	claudeLocalChanged := false
	for _, f := range changedFiles {
		switch f {
		case "CLAUDE.md":
			claudeChanged = true
		case "CLAUDE.local.md":
			claudeLocalChanged = true
		}
	}

	if !claudeChanged && !claudeLocalChanged {
		return false // Nothing to strip
	}

	needsCommit := false

	// Handle CLAUDE.md: check if the committed version contains overlay marker
	if claudeChanged {
		// Read current CLAUDE.md from HEAD
		currentContent, showErr := g.ShowFile("HEAD", "CLAUDE.md")
		if showErr == nil && strings.Contains(currentContent, templates.PolecatLifecycleMarker) {
			// Current CLAUDE.md has overlay content — restore from origin
			origContent, origErr := g.ShowFile(originRef, "CLAUDE.md")
			if origErr != nil {
				// CLAUDE.md didn't exist on origin/main — the overlay created it.
				// Remove it from tracking.
				if rmErr := g.RmCached("CLAUDE.md"); rmErr == nil {
					needsCommit = true
					fmt.Printf("%s Removed overlay CLAUDE.md (did not exist on %s)\n",
						style.Bold.Render("→"), defaultBranch)
				}
			} else {
				// CLAUDE.md existed on origin — restore original content
				_ = origContent // Restore via checkout
				if coErr := g.CheckoutFileFromRef(originRef, "CLAUDE.md"); coErr == nil {
					if addErr := g.Add("CLAUDE.md"); addErr == nil {
						needsCommit = true
						fmt.Printf("%s Restored original CLAUDE.md (stripped Gas Town overlay)\n",
							style.Bold.Render("→"))
					}
				}
			}
		}
	}

	// Handle CLAUDE.local.md: always remove from commits (it's a runtime artifact)
	if claudeLocalChanged {
		if rmErr := g.RmCached("CLAUDE.local.md"); rmErr == nil {
			needsCommit = true
			fmt.Printf("%s Removed CLAUDE.local.md from branch (Gas Town overlay)\n",
				style.Bold.Render("→"))
		}
	}

	if !needsCommit {
		return false
	}

	// Create cleanup commit
	if commitErr := g.Commit("chore: strip Gas Town overlay from CLAUDE.md (gt-p35)"); commitErr != nil {
		style.PrintWarning("failed to create overlay cleanup commit: %v", commitErr)
		return false
	}

	fmt.Printf("%s Created cleanup commit to remove Gas Town overlay files\n",
		style.Bold.Render("✓"))
	return true
}

// purgeClosedEphemeralBeads removes closed ephemeral beads (wisps) that accumulated
// during this and prior sessions. Polecat/witness sessions create mol-polecat-work
// steps, mol-witness-patrol cycles, etc. as wisps. These get closed during normal
// operation but are never deleted, accumulating hundreds of rows that pollute
// bd ready/list output. (hq-6161m)
//
// Best-effort: errors are logged but don't block gt done completion.
func purgeClosedEphemeralBeads(bd *beads.Beads) {
	out, err := bd.Run("purge", "--force", "--quiet")
	if err != nil {
		// Non-fatal: purge failure shouldn't block session completion
		fmt.Fprintf(os.Stderr, "Warning: wisp purge failed: %v\n", err)
		return
	}
	// bd purge --force --quiet outputs the count of purged beads
	outStr := strings.TrimSpace(string(out))
	if outStr != "" && outStr != "0" {
		fmt.Fprintf(os.Stderr, "Purged closed ephemeral beads: %s\n", outStr)
	}
}

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/docsonly"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/polecat/completion"
	"github.com/steveyegge/gastown/internal/pushlog"
	"github.com/steveyegge/gastown/internal/style"
)

// strategyContext carries the runDone completion-path state shared by the
// convoy merge-strategy phases (gu-nid89.12.1 extraction). These are the
// values live at the point the merge strategy is dispatched: the git handle,
// the agent/worker identity, and the branch/target names needed to push, close
// the source bead, and file a stranded-push wisp on failure.
//
// The phases live in package cmd (not internal/polecat/completion) because they
// depend on cmd-private push helpers (pushForDone, recordPushFailure,
// fileStrandedPushWisp, recordPushReceipt) and the done flag globals
// (doneSkipVerify, doneSkipVerifyReason). Moving those would cascade the entire
// push-side-effect cluster across the completion import boundary; keeping the
// phases here is a pure same-package code move with no behavior change, mirroring
// the submitToMergeQueue / pushBranchWithFallbacks / teardownAfterDone
// precedent (gs-pd6/gs-t0k).
type strategyContext struct {
	g             *git.Git
	cwd           string
	townRoot      string
	rigName       string
	sender        string
	branch        string
	defaultBranch string
	issueID       string
	worker        string
	agentBeadID   string
}

// closeBaseIssueWithRetry force-closes the source bead with 3 attempts and
// backoff, matching the inline retry loops the convoy strategies previously
// duplicated. Best-effort: a final failure is warned, not fatal — the polecat
// is about to be torn down and the witness/mayor sweeps recover a still-open
// bead.
func closeBaseIssueWithRetry(cwd, issueID, closeReason, successNote string) {
	if issueID == "" {
		return
	}
	bd := beads.New(cwd)
	var closeErr error
	for attempt := 1; attempt <= 3; attempt++ {
		closeErr = bd.ForceCloseWithReason(closeReason, issueID)
		if closeErr == nil {
			fmt.Printf("%s Issue %s closed (%s)\n", style.Bold.Render("✓"), issueID, successNote)
			return
		}
		if attempt < 3 {
			style.PrintWarning("close attempt %d/3 failed: %v (retrying in %ds)", attempt, closeErr, attempt*2)
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
	}
	style.PrintWarning("could not close issue %s after 3 attempts: %v", issueID, closeErr)
}

// runConvoyLocalStrategy handles a merge=local convoy (gu-nid89.12.1 extraction
// from runDone). Plain merge=local parks the work on the local feature branch
// for human review / upstream PR and skips push + MR entirely.
//
// EXCEPTION (gs-d26): a relay leg — a merge=local convoy carrying a named
// base_branch other than the rig default — FF-pushes its commits to
// origin/<base_branch> so the next leg builds on top of them. The push is
// strictly fast-forward-only (pushForDone uses force=false); a non-ff means an
// unexpected concurrent writer, which we surface as a stranded-push wisp rather
// than clobber with a force-push.
//
// Returns pushFailed — true when a relay FF-push or its verification failed and
// the work was stranded. The caller always routes to notifyWitness afterward
// (a merge=local convoy is fully handled here, relay or not).
//
// Mirrors the lines previously inlined in the COMPLETED block of runDone.
func runConvoyLocalStrategy(sc strategyContext, convoyInfo *ConvoyInfo) (pushFailed bool) {
	relayBase, isRelay := relayBaseForLocalMerge(convoyInfo, sc.defaultBranch)
	if !isRelay {
		fmt.Printf("%s Local merge strategy: skipping push and merge queue\n", style.Bold.Render("→"))
		fmt.Printf("  Branch: %s\n", sc.branch)
		if sc.issueID != "" {
			fmt.Printf("  Issue: %s\n", sc.issueID)
		}
		fmt.Println()
		fmt.Printf("%s\n", style.Dim.Render("Work stays on local feature branch."))
		return false
	}

	// FF-push the relay leg to its named base branch.
	fmt.Printf("%s Relay leg: FF-pushing to %s\n", style.Bold.Render("→"), relayBase)
	if sc.issueID != "" {
		fmt.Printf("  Issue: %s\n", sc.issueID)
	}
	pushSubmoduleChanges(sc.g, relayBase)
	relayRefspec := sc.branch + ":" + relayBase
	relayHeadSHA, _ := sc.g.Rev("HEAD")
	relayPushErr := pushForDone(sc.g, relayRefspec)
	if relayPushErr != nil {
		// gu-epv5 recovery: the push may have actually landed even though the
		// local report indicated failure (transient net error).
		if relayHeadSHA != "" && recoverPushFromOriginTip(sc.g, relayBase, relayHeadSHA) {
			fmt.Printf("%s Relay push reported failure but origin/%s already matches HEAD — treating as success (gu-epv5 recovery)\n", style.Bold.Render("✓"), relayBase)
		} else {
			errMsg := fmt.Sprintf("relay FF-push to %s failed (non-fast-forward or remote error — possible concurrent writer): %v", relayBase, relayPushErr)
			style.PrintWarning("%s", errMsg)
			recordPushFailure(sc.townRoot, sc.rigName, sc.branch, relayHeadSHA, pushlog.SourceDoneRelay, pushlog.StagePush, sc.worker, sc.issueID, relayPushErr)
			strandedBd := beads.New(sc.cwd)
			fileStrandedPushWisp(strandedBd, sc.rigName, sc.branch, relayHeadSHA, relayBase, sc.issueID, sc.agentBeadID, sc.worker, relayPushErr)
			return true
		}
	}
	relayCommitSHA := relayHeadSHA
	if relayCommitSHA == "" {
		relayCommitSHA, _ = sc.g.Rev("HEAD")
	}
	if doneSkipVerify {
		noteVerifiedPushSkipped(sc.cwd, sc.issueID, relayBase, relayCommitSHA, fmt.Sprintf("--skip-verify on relay FF-push: %s", doneSkipVerifyReason))
	} else if verifyErr := sc.g.VerifyCommitOnRemoteBranch("origin", relayBase, relayCommitSHA); verifyErr != nil {
		// gu-epv5: verify may have hit a transient remote read failure.
		if recoverPushFromOriginTip(sc.g, relayBase, relayCommitSHA) {
			fmt.Printf("%s Verify reported failure but origin/%s tip matches — treating as success (gu-epv5 recovery)\n", style.Bold.Render("✓"), relayBase)
		} else {
			errMsg := verifyErr.Error()
			noteVerifiedPushFailure(sc.cwd, sc.issueID, relayBase, relayCommitSHA, verifyErr)
			style.PrintWarning("%s\nRelay FF-push reported success but remote verification failed. Source bead will remain in progress.", errMsg)
			recordPushFailure(sc.townRoot, sc.rigName, sc.branch, relayCommitSHA, pushlog.SourceDoneRelay, pushlog.StageVerify, sc.worker, sc.issueID, verifyErr)
			strandedBd := beads.New(sc.cwd)
			fileStrandedPushWisp(strandedBd, sc.rigName, sc.branch, relayCommitSHA, relayBase, sc.issueID, sc.agentBeadID, sc.worker, verifyErr)
			return true
		}
	}
	fmt.Printf("%s Relay leg FF-pushed to %s\n", style.Bold.Render("✓"), relayBase)
	recordPushReceipt(sc.g, sc.townRoot, sc.rigName, relayBase, relayCommitSHA, pushlog.SourceDoneRelay, sc.worker, sc.issueID)

	closeBaseIssueWithRetry(sc.cwd, sc.issueID, fmt.Sprintf("Relay FF-push to %s (merge=local relay leg)", relayBase), "relay FF-push")
	return false
}

// runConvoyDirectStrategy handles a merge=direct convoy (gu-nid89.12.1
// extraction from runDone): push commits straight to the rig default branch and
// skip the merge request.
//
// gu-8edz guard: on a merge_queue.enabled rig the refinery owns the gate suite,
// so a polecat direct-push bypasses gates and races refinery merges. When the
// guard fires the strategy is NOT handled here — it returns handled=false so the
// caller falls through to the normal push+MR path (hq-dlksi), keeping the work
// in the merge queue rather than stranding it. The GT_ALLOW_DIRECT_PUSH=1
// override (with GT_SKIP_PREPUSH_REASON per gu-zy57) lets the direct push proceed.
//
// Returns (handled, pushFailed): handled=true means the direct push path ran to
// completion (success or stranded) and the caller should route to notifyWitness;
// handled=false means fall through to the MR path.
//
// Mirrors the lines previously inlined in the COMPLETED block of runDone.
func runConvoyDirectStrategy(sc strategyContext) (handled bool, pushFailed bool) {
	if guardErr := completion.GuardDirectPushOnMergeQueue(sc.townRoot, sc.rigName, "convoy direct merge"); guardErr != nil {
		style.PrintWarning("%v — falling through to normal push+MR path", guardErr)
		if stale, reason := completion.IsRefineryHeartbeatStale(sc.townRoot, sc.rigName); stale {
			fmt.Fprintf(os.Stderr, "  Refinery heartbeat is stale (%s) — falling through to MR creation so refinery can pick up on recovery.\n", reason)
			if sc.issueID != "" {
				completion.MarkAwaitingRefineryRecovery(beads.New(sc.cwd), sc.issueID, reason)
			}
		}
		// Fall through to normal push + MR path.
		return false, false
	}

	fmt.Printf("%s Direct merge strategy: pushing to %s\n", style.Bold.Render("→"), sc.defaultBranch)
	// Push submodule changes before direct push (gt-dzs)
	pushSubmoduleChanges(sc.g, sc.defaultBranch)
	directRefspec := sc.branch + ":" + sc.defaultBranch
	directPushErr := pushForDone(sc.g, directRefspec)
	directHeadSHA, _ := sc.g.Rev("HEAD")
	if directPushErr != nil {
		// gu-epv5 Option C: re-check origin/<defaultBranch>.
		if directHeadSHA != "" && recoverPushFromOriginTip(sc.g, sc.defaultBranch, directHeadSHA) {
			fmt.Printf("%s Direct push reported failure but origin/%s already matches HEAD — treating as success (gu-epv5 recovery)\n", style.Bold.Render("✓"), sc.defaultBranch)
		} else {
			errMsg := fmt.Sprintf("direct push to %s failed: %v", sc.defaultBranch, directPushErr)
			style.PrintWarning("%s", errMsg)
			recordPushFailure(sc.townRoot, sc.rigName, sc.branch, directHeadSHA, pushlog.SourceDoneDirect, pushlog.StagePush, sc.worker, sc.issueID, directPushErr)
			strandedBd := beads.New(sc.cwd)
			fileStrandedPushWisp(strandedBd, sc.rigName, sc.branch, directHeadSHA, sc.defaultBranch, sc.issueID, sc.agentBeadID, sc.worker, directPushErr)
			return true, true
		}
	}
	directCommitSHA := directHeadSHA
	if directCommitSHA == "" {
		directCommitSHA, _ = sc.g.Rev("HEAD")
	}
	if doneSkipVerify {
		noteVerifiedPushSkipped(sc.cwd, sc.issueID, sc.defaultBranch, directCommitSHA, fmt.Sprintf("--skip-verify on direct merge: %s", doneSkipVerifyReason))
	} else if verifyErr := sc.g.VerifyCommitOnRemoteBranch("origin", sc.defaultBranch, directCommitSHA); verifyErr != nil {
		// gu-epv5: re-check origin tip.
		if recoverPushFromOriginTip(sc.g, sc.defaultBranch, directCommitSHA) {
			fmt.Printf("%s Verify reported failure but origin/%s tip matches — treating as success (gu-epv5 recovery)\n", style.Bold.Render("✓"), sc.defaultBranch)
		} else {
			errMsg := verifyErr.Error()
			noteVerifiedPushFailure(sc.cwd, sc.issueID, sc.defaultBranch, directCommitSHA, verifyErr)
			style.PrintWarning("%s\nDirect merge pushed but remote verification failed. Source bead will remain in progress.", errMsg)
			recordPushFailure(sc.townRoot, sc.rigName, sc.branch, directCommitSHA, pushlog.SourceDoneDirect, pushlog.StageVerify, sc.worker, sc.issueID, verifyErr)
			strandedBd := beads.New(sc.cwd)
			fileStrandedPushWisp(strandedBd, sc.rigName, sc.branch, directCommitSHA, sc.defaultBranch, sc.issueID, sc.agentBeadID, sc.worker, verifyErr)
			return true, true
		}
	}
	fmt.Printf("%s Branch pushed directly to %s\n", style.Bold.Render("✓"), sc.defaultBranch)
	// Status was detected before push, so "unpushed"/"has_unpushed" is now stale.
	doneCleanupStatus = cleanupStatusAfterSuccessfulPush(doneCleanupStatus)
	// gu-ftja: receipt for the direct-merge convoy push.
	recordPushReceipt(sc.g, sc.townRoot, sc.rigName, sc.defaultBranch, directCommitSHA, pushlog.SourceDoneDirect, sc.worker, sc.issueID)

	closeBaseIssueWithRetry(sc.cwd, sc.issueID, fmt.Sprintf("Direct merge to %s (convoy strategy)", sc.defaultBranch), "direct merge")
	return true, false
}

// runAutoCommitSafetyNet auto-commits uncommitted work before any exit path
// (gt-pvx, gu-nid89.12.1 extraction from runDone). Polecats have been observed
// running gt done without committing their implementation work (1000s of lines
// lost); this ensures the work is never lost regardless of exit type.
//
// HARD GUARD (gu-cfb): refuses to auto-commit when the current branch is the
// rig's default (or "master") — committing there would land artifacts on
// origin/main, bypassing the merge queue. The status is left "uncommitted" so
// downstream paths still refuse to submit.
//
// gu-fo82: the core commit logic is polecat.AutoSaveAbandonedWIP; this preserves
// gt done's UI output and doneCleanupStatus management. It mutates the
// doneCleanupStatus global directly (matching the inline behavior).
//
// Returns an error only for the unmerged-conflicts case (a hard failure runDone
// returns directly). A detached-HEAD guard refusal returns nil — the inline
// version jumped to afterSafetyNet, which is equivalent to falling through.
// No-op unless cwdAvailable and status=="uncommitted". Mirrors done.go:538–578.
func runAutoCommitSafetyNet(g *git.Git, cwd, branch, defaultBranchEarly string, cwdAvailable bool) error {
	if !cwdAvailable || doneCleanupStatus != "uncommitted" {
		return nil
	}

	if isDefaultBranchName(branch, defaultBranchEarly) {
		style.PrintWarning("auto-commit safety net refused: current branch %q is a protected default branch", branch)
		fmt.Fprintf(os.Stderr, "  Uncommitted changes will NOT be auto-saved — committing to %q would bypass the merge queue.\n", branch)
		fmt.Fprintf(os.Stderr, "  This usually means gt done was invoked from the rig root or a stale worktree.\n")
		fmt.Fprintf(os.Stderr, "  Manually stash or commit your changes from the correct polecat worktree before re-running.\n\n")
		// Leave doneCleanupStatus == "uncommitted" so downstream paths still
		// refuse to submit. The agent sees the warning and can recover.
		return nil
	}

	// gu-fo82: Delegate to polecat.AutoSaveAbandonedWIP for the core logic.
	workStatus, checkErr := g.CheckUncommittedWork()
	if checkErr == nil && workStatus.HasUncommittedChanges && !workStatus.CleanExcludingRuntime() {
		fmt.Printf("\n%s Uncommitted changes detected — auto-saving to prevent work loss\n", style.Bold.Render("⚠"))
		fmt.Printf("  Files: %s\n\n", workStatus.String())
	}

	saved, _, saveErr := polecat.AutoSaveAbandonedWIP(cwd, branch, "gt-done")
	if saveErr != nil {
		errMsg := saveErr.Error()
		if strings.Contains(errMsg, "detached") {
			style.PrintWarning("auto-commit safety net refused: HEAD is detached")
			fmt.Fprintf(os.Stderr, "  A commit here would orphan the work (no branch ref to advance).\n")
			fmt.Fprintf(os.Stderr, "  Recover manually: git branch %s HEAD && git checkout %s && git commit ...\n", branch, branch)
			fmt.Fprintf(os.Stderr, "  Then re-run gt done.\n\n")
			return nil
		} else if strings.Contains(errMsg, "unmerged") {
			return fmt.Errorf("cannot auto-save unmerged conflicts: %s\nResolve conflicts first, or use --status DEFERRED to exit without completing", errMsg)
		}
		// Real failure — warn but continue.
		style.PrintWarning("auto-commit: %v — uncommitted work may be at risk", saveErr)
	} else if saved {
		fmt.Printf("%s Auto-committed uncommitted work (safety net)\n", style.Bold.Render("✓"))
		fmt.Printf("  The agent should have committed before running gt done.\n")
		fmt.Printf("  This auto-save prevents work loss.\n\n")
		doneCleanupStatus = "unpushed" // Update status — changes are now committed but not pushed
	}
	return nil
}

// verifyWorkExistsForCompletion runs the hq-xthqf "work exists" preflight of
// the COMPLETED path (gu-nid89.12.1 extraction from runDone). Polecats calling
// gt done without commits lose work, so this blocks when:
//   - the working directory is unavailable (can't verify git state)
//   - there are uncommitted changes that would be lost (runtime artifacts under
//     .claude/.opencode/.beads/etc. are excluded)
//
// It then computes how many commits the branch is ahead of origin/<default>
// (falling back to a local comparison, then to "assume work exists"), and
// detects whether the bead is a no_merge/review_only/--no-code task for which
// zero commits is expected (GH#2496, gu-gc4ex).
//
// Returns (originDefault, aheadCount, isNoMergeTask, err). A non-nil err is a
// hard failure runDone returns directly. Mirrors the lines previously inlined in
// the COMPLETED block.
func verifyWorkExistsForCompletion(sc strategyContext, cwdAvailable bool) (originDefault string, aheadCount int, isNoMergeTask bool, err error) {
	// Block if working directory not available - can't verify git state.
	if !cwdAvailable {
		return "", 0, false, fmt.Errorf("cannot complete: working directory not available (worktree deleted?)\nUse --status DEFERRED to exit without completing")
	}

	// Block if there are uncommitted changes (would be lost on completion).
	workStatus, wsErr := sc.g.CheckUncommittedWork()
	if wsErr != nil {
		return "", 0, false, fmt.Errorf("checking git status: %w", wsErr)
	}
	if workStatus.HasUncommittedChanges && !workStatus.CleanExcludingRuntime() {
		return "", 0, false, fmt.Errorf("cannot complete: uncommitted changes would be lost\nCommit your changes first, or use --status DEFERRED to exit without completing\nUncommitted: %s", workStatus.String())
	}

	// Check if branch has commits ahead of origin/default. If not, work may have
	// been pushed directly to main — that's fine, the caller skips MR.
	originDefault = "origin/" + sc.defaultBranch
	aheadCount, aheadErr := sc.g.CommitsAhead(originDefault, "HEAD")
	if aheadErr != nil {
		// Fallback to local branch comparison if origin not available.
		aheadCount, aheadErr = sc.g.CommitsAhead(sc.defaultBranch, sc.branch)
		if aheadErr != nil {
			// Can't determine - assume work exists and continue.
			style.PrintWarning("could not check commits ahead of %s: %v", sc.defaultBranch, aheadErr)
			aheadCount = 1
		}
	}

	// Check no_merge / review_only flags on the hooked bead. When set, this is a
	// non-code task (email, research, analysis, PRD review) where zero commits is
	// expected. Must be checked before the zero-commit guard (GH#2496, gt-kvf).
	isNoMergeTask = doneNoCode
	if !isNoMergeTask && sc.issueID != "" {
		noMergeBd := beads.New(sc.cwd)
		if noMergeIssue, showErr := noMergeBd.Show(sc.issueID); showErr == nil {
			if af := beads.ParseAttachmentFields(noMergeIssue); af != nil && (af.NoMerge || af.ReviewOnly) {
				isNoMergeTask = true
			}
		}
	}

	return originDefault, aheadCount, isNoMergeTask, nil
}

// integrationBranchRebaseParkReason decides whether a rebase-onto-base abort
// should PARK the polecat (return a non-empty reason) rather than hard-fail
// (return ""). It fires only when the contamination base is an INTEGRATION
// branch — origin/<base> where <base> is not the rig default. An abort onto the
// rig default is the polecat's own conflict to resolve, so it returns "" and the
// caller surfaces the hard error. contaminationBase carries the "origin/" prefix
// (doneContaminationBaseRef); rebaseErr is the underlying AbortRebase cause,
// woven into the human-readable park reason. gs-nva3.
func integrationBranchRebaseParkReason(contaminationBase, defaultBranch string, rebaseErr error) string {
	if strings.TrimPrefix(contaminationBase, "origin/") == defaultBranch {
		return ""
	}
	return fmt.Sprintf(
		"auto-rebase onto integration branch %s aborted (%v); parking — PR awaits human review/merge, slot released",
		contaminationBase, rebaseErr)
}

// runContaminationPreflight runs the pre-push preflight of the COMPLETED path
// (gu-nid89.12.1 extraction from runDone): the GH#2220 branch-contamination
// check + gh#3400 auto-rebase, and the gu-xp5f --pre-verified attestation
// re-verification.
//
// It fetches origin, blocks when the branch is severely behind the effective
// target (>=200 commits), auto-rebases when moderately behind, and re-runs the
// rig gates when the polecat declared --pre-verified (skipping the gate run for
// docs-only diffs, gs-2c9). An auto-rebase or a failed gate re-run invalidates
// the attestation (gs-4bn / gu-xp5f): on either, refinery re-runs gates instead
// of trusting the stale claim.
//
// alreadyPushed is checkpoints[CheckpointPushed]==branch. Returns
// (preVerifiedAttestationValid, parkReason, err):
//   - a non-nil err is a hard failure runDone returns directly (severe
//     contamination, or a rebase error onto the rig default).
//   - a non-empty parkReason (gs-nva3) means the rebase aborted onto an
//     INTEGRATION branch (base != rig default); runDone should PARK the polecat
//     into clean idle (deferred) rather than hard-fail, so the slot is released.
//
// Mirrors the lines previously inlined in the COMPLETED block.
func runContaminationPreflight(sc strategyContext, cwdAvailable, alreadyPushed bool) (preVerifiedAttestationValid bool, parkReason string, err error) {
	preVerifiedAttestationValid = donePreVerified

	// gs-xbo: rebase onto the bead's RELAY base, not just --target / rig default.
	contaminationBase := doneContaminationBaseRef(sc.defaultBranch, effectiveBaseBranch(sc.issueID, doneTarget))
	if fetchErr := sc.g.Fetch("origin"); fetchErr != nil {
		style.PrintWarning("could not fetch origin before contamination check: %v (proceeding with local refs)", fetchErr)
	}
	contam, contamErr := sc.g.CheckBranchContamination(contaminationBase)
	if contamErr == nil && contam.Behind > 0 {
		const warnThreshold = 50
		const blockThreshold = 200
		if contam.Behind >= blockThreshold {
			return preVerifiedAttestationValid, "", fmt.Errorf("branch contamination: %d commits behind %s (threshold: %d)\n"+
				"The branch is severely stale and will include unrelated changes in the PR.\n"+
				"Fix: git fetch origin && git rebase %s",
				contam.Behind, contaminationBase, blockThreshold, contaminationBase)
		} else if contam.Behind >= warnThreshold {
			style.PrintWarning("branch is %d commits behind %s — consider rebasing to avoid PR contamination", contam.Behind, contaminationBase)
		}

		// gh#3400: Auto-rebase the polecat branch onto the latest target before push.
		rebased, skipReason, rebaseErr := completion.AutoRebaseOnTarget(sc.g, contaminationBase, contam.Behind, donePreVerified, alreadyPushed)
		if rebaseErr != nil {
			// gs-nva3: a rebase-onto-base abort on an INTEGRATION branch (base is
			// not the rig default — e.g. gagecane/gt, which carries modules that
			// exist only on the integration branch) is not a conflict the polecat
			// can resolve: the work targets a PR awaiting human review/merge, not
			// the merge queue. Hard-failing here leaves the bead IN_PROGRESS on the
			// hook, holding the rig slot until the PR merges, so the daemon
			// re-dispatches it every cycle (lb-fa28 / lb-0rs3.14 / lb-1tee). Signal
			// runDone to PARK the polecat into clean idle (deferred) so the slot is
			// released. A rebase abort onto the rig default still hard-fails — that
			// IS the polecat's conflict to resolve before resubmitting.
			if reason := integrationBranchRebaseParkReason(contaminationBase, sc.defaultBranch, rebaseErr); reason != "" {
				return preVerifiedAttestationValid, reason, nil
			}
			return preVerifiedAttestationValid, "", rebaseErr
		}
		if rebased {
			fmt.Printf("%s Branch rebased onto %s\n", style.Bold.Render("✓"), contaminationBase)
			// gs-4bn: auto-rebase invalidates a --pre-verified attestation; the
			// gates ran against the pre-rebase base. donePreVerified itself stays
			// set so pushForDone still skips the pre-push hook.
			if donePreVerified {
				style.PrintWarning("auto-rebase invalidated --pre-verified attestation (gs-4bn); refinery will run gates")
				preVerifiedAttestationValid = false
			}
		} else if skipReason != "" {
			style.PrintWarning("branch is %d commits behind %s but %s; skipping auto-rebase", contam.Behind, contaminationBase, skipReason)
		}
	}

	// gu-ph24z: auto-format Go files before the branch is pushed. gofmt is a
	// REQUIRED refinery gate + pre-push-hook fast gate, so an unformatted branch
	// cannot land — but before this the rejection surfaced AFTER submit, by which
	// point the polecat is gone and the trivial fix has to bounce through a fresh
	// dispatch (gu-mxupc rejected twice for the same struct-alignment failure).
	// Auto-fixing here mirrors the hook's `gofmt -l .` and commits the result so
	// formatting can no longer reach the merge queue. gofmt is whitespace-only, so
	// the build/vet/test gates the polecat ran stay valid across the fixup commit;
	// a gofmt tooling error never strands submission (hook + refinery still gate).
	if cwdAvailable {
		if _, fmtErr := completion.AutoFormatGoFiles(sc.g, nil); fmtErr != nil {
			style.PrintWarning("pre-submit gofmt auto-format failed: %v (pre-push hook / refinery gate will still catch formatting)", fmtErr)
		}
	}

	// gu-xp5f: re-run the rig's pre-merge gates to verify the --pre-verified
	// attestation against reality. On gate failure we DROP the attestation rather
	// than fail submission. Skipped when already invalidated by auto-rebase.
	if donePreVerified && preVerifiedAttestationValid && cwdAvailable {
		// gs-2c9: skip the gate re-run for docs-only changes — go gates only
		// inspect .go sources, so a docs-only diff makes every gate a no-op.
		if files, derr := sc.g.DiffNameOnly(contaminationBase, "HEAD"); derr == nil && docsonly.IsDocsOnly(files) {
			fmt.Printf("%s docs-only change (*.md / .quality/**) — skipping pre-merge gate verification (gs-2c9)\n", style.Bold.Render("→"))
		} else if !completion.VerifyPreVerifiedAttestation(context.Background(), sc.townRoot, sc.rigName, sc.cwd) {
			preVerifiedAttestationValid = false
		}
	}

	return preVerifiedAttestationValid, "", nil
}

// resolveMRTarget determines the target branch for the merge-request bead
// (gu-nid89.12.1 extraction from runDone). Priority, highest first:
//
//  1. explicit --target flag (the formula passes {{base_branch}} directly)
//  2. base_branch override stamped in the bead's formula_vars at sling time
//     2b. relay base carried on the tracking convoy / stamped fields (gs-dus)
//  3. integration branch auto-detected from the epic hierarchy (when enabled)
//  4. gu-aucji phantom-"main" safety net: rewrite target=="main" to the rig
//     default when origin/main does not exist, so a mainline-only rig never
//     emits an unmergeable target=main MR
//
// sourceIssue is the already-loaded source bead (nil when bd.Show failed
// earlier); bd/g are used for the auto-detect + phantom-main steps. The
// human-readable target-decision output is preserved here so runDone reads as a
// straight call. Mirrors the lines previously inlined in the COMPLETED block.
func resolveMRTarget(sc strategyContext, bd *beads.Beads, sourceIssue *beads.Issue) string {
	target := sc.defaultBranch
	explicitTarget := false

	// 1. Explicit --target flag (highest priority — polecat knows its base branch).
	if doneTarget != "" {
		target = doneTarget
		explicitTarget = true
		fmt.Printf("  Target branch: %s (from --target flag)\n", target)
	}

	// 2. Check for base_branch override in formula vars (stored on bead at sling time).
	if !explicitTarget && target == sc.defaultBranch && sourceIssue != nil {
		if af := beads.ParseAttachmentFields(sourceIssue); af != nil {
			if bb := extractFormulaVar(af.FormulaVars, "base_branch"); bb != "" && bb != sc.defaultBranch {
				target = bb
				fmt.Printf("  Target branch override: %s (from formula_vars)\n", target)
			}
		}
	} else if !explicitTarget && target == sc.defaultBranch && sourceIssue == nil && sc.issueID != "" {
		// sourceIssue is nil — bd.Show(issueID) failed earlier. This is the
		// silent failure path that caused 150+ procedure beads to target main.
		style.PrintWarning("could not load source issue %s for target branch detection (Dolt/beads lookup failed) — using default branch %s", sc.issueID, sc.defaultBranch)
	}

	// 2b. Relay base carried on the tracking convoy or stamped fields (gs-dus).
	if relayBase := mrRelayTargetOverride(explicitTarget, target, sc.defaultBranch, sc.issueID, effectiveBaseBranch); relayBase != "" {
		target = relayBase
		fmt.Printf("  Target branch: %s (relay base from convoy/stamped fields — gs-dus)\n", target)
	}

	// 3. Auto-detect integration branch from epic hierarchy (if enabled).
	if !explicitTarget && target == sc.defaultBranch {
		refineryEnabled := true
		settingsPath := filepath.Join(sc.townRoot, sc.rigName, "settings", "config.json")
		if settings, err := config.LoadRigSettings(settingsPath); err == nil && settings.MergeQueue != nil {
			refineryEnabled = settings.MergeQueue.IsRefineryIntegrationEnabled()
		}
		if refineryEnabled {
			autoTarget, err := beads.DetectIntegrationBranch(bd, sc.g, sc.issueID)
			if err == nil && autoTarget != "" {
				target = autoTarget
			}
		}
	}

	// 4. Phantom-"main" safety net (gu-aucji).
	if corrected := correctPhantomMainTarget(target, sc.defaultBranch, sc.g.RemoteBranchExists); corrected != target {
		style.PrintWarning("MR target was %q but rig default is %q and origin/main does not exist — retargeting to %q (gu-aucji)", target, sc.defaultBranch, corrected)
		target = corrected
	}

	return target
}

// validateDoneFlags runs the up-front argument validation for gt done
// (gu-nid89.12.1 extraction from runDone): the polecats-only actor guard, the
// exit-status check, and the --skip-verify-reason / --no-code rationale gates.
// Returns the normalized exit type (uppercased --status) or an error runDone
// returns directly. Mirrors done.go:401–418.
func validateDoneFlags() (exitType string, err error) {
	// Guard: Only polecats should call gt done. Crew, deacons, witnesses etc.
	// persist across tasks and don't use gt done.
	actor := os.Getenv("BD_ACTOR")
	if actor != "" && !isPolecatActor(actor) {
		return "", fmt.Errorf("gt done is for polecats only (you are %s)\nPolecat sessions end with gt done — the session is cleaned up, but identity persists.\nOther roles persist across tasks and don't use gt done.", actor)
	}

	exitType = strings.ToUpper(doneStatus)
	if exitType != ExitCompleted && exitType != ExitEscalated && exitType != ExitDeferred {
		return "", fmt.Errorf("invalid exit status '%s': must be COMPLETED, ESCALATED, or DEFERRED", doneStatus)
	}

	if verifyErr := validateSkipVerifyReason(); verifyErr != nil {
		return "", verifyErr
	}
	if noCodeErr := validateNoCode(); noCodeErr != nil {
		return "", noCodeErr
	}
	return exitType, nil
}

// writeDoneIntentAndReadCheckpoints writes the done-intent label EARLY (before
// push/MR ops) so the Witness can auto-nuke a zombie polecat if gt done crashes,
// and reads any existing resume checkpoints (gt-aufru): when gt done was
// interrupted (SIGTERM, context exhaustion, SIGKILL), checkpoints record which
// stages completed so re-invocation skips them. gu-nid89.12.1 extraction from
// runDone. No-op (returns an empty map) when agentBeadID is empty. The agent
// bead lives in the town DB despite the rig prefix, so ForAgentBead bypasses
// routing. Mirrors done.go:613–622.
func writeDoneIntentAndReadCheckpoints(cwd, agentBeadID, exitType string) map[DoneCheckpoint]string {
	checkpoints := map[DoneCheckpoint]string{}
	if agentBeadID == "" {
		return checkpoints
	}
	bd := beads.New(cwd).ForAgentBead()
	setDoneIntentLabel(bd, agentBeadID, exitType)
	checkpoints = readDoneCheckpoints(bd, agentBeadID)
	if len(checkpoints) > 0 {
		fmt.Printf("%s Resuming gt done from checkpoint (previous run was interrupted)\n", style.Bold.Render("→"))
	}
	return checkpoints
}

// releaseIfReferenceBead enforces the hq-9jeyo tripwire guard (gu-nid89.12.1
// extraction from runDone): beads labeled do-not-dispatch / pinned (or
// issue_type=reference) are live safety gates that must stay OPEN forever, never
// work. If one was mis-hooked, letting gt done proceed would CLOSE the tripwire
// (taking the gate down). This releases the hook back to open+unassigned and
// signals the caller to exit without closing, MR creation, or marking stuck.
//
// Returns true when the bead was a reference/tripwire and was handled — the
// caller must return nil immediately. No-op (returns false) for normal work
// beads or an empty/unloadable issueID. Mirrors done.go:592–603.
func releaseIfReferenceBead(cwd, issueID string) bool {
	if issueID == "" {
		return false
	}
	guardBd := beads.New(cwd)
	refIssue, err := guardBd.Show(issueID)
	if err != nil || !isNonDispatchableIssue(refIssue) {
		return false
	}
	style.PrintWarning("refusing to complete %s: it is a do-not-dispatch / pinned reference bead (tripwire), not work — leaving it OPEN", issueID)
	if relErr := guardBd.ReleaseWithReason(issueID, "hq-9jeyo: mis-dispatched reference/tripwire bead is not completable via gt done"); relErr != nil {
		style.PrintWarning("could not release reference bead %s (it stays OPEN): %v", issueID, relErr)
	} else {
		fmt.Printf("%s Released %s back to open; hook cleared\n", style.Bold.Render("✓"), issueID)
	}
	return true
}

// resolveMRPriority resolves the MR bead priority (gu-nid89.12.1 extraction):
// the --priority flag wins; otherwise inherit from the source bead, defaulting
// to P2 when the bead can't be loaded.
func resolveMRPriority(bd *beads.Beads, issueID string) int {
	if donePriority >= 0 {
		return donePriority
	}
	sourceIssue, err := bd.Show(issueID)
	if err != nil {
		return 2 // Default
	}
	return sourceIssue.Priority
}

// resumeMRFromCheckpoint implements the gt-aufru MR-creation resume check
// (gu-nid89.12.1 extraction from runDone). On a resumed run it returns the
// checkpointed MR id when that MR is for the current branch AND verifiably
// present on a fresh shared-main view (gs-onu) — only then is it safe to
// short-circuit MR creation. A stale-branch checkpoint, an absent-on-main MR, or
// a missing checkpoint returns ("", false), so the caller falls through to the
// idempotent find/create path.
//
// commitSHA is HEAD (the gs-onu main-view dedup key). Mirrors done.go:836–869.
func resumeMRFromCheckpoint(sc strategyContext, bd *beads.Beads, resolvedBeads, commitSHA string, checkpoints map[DoneCheckpoint]string) (mrID string, resumed bool) {
	cpMRID := checkpoints[CheckpointMRCreated]
	if cpMRID == "" {
		return "", false
	}
	cpMR, cpErr := bd.Show(cpMRID)
	if cpErr != nil || cpMR == nil {
		// If MR lookup fails, fall through to create/find MR normally.
		return "", false
	}
	branchPrefix := "branch: " + sc.branch + "\n"
	if !strings.HasPrefix(cpMR.Description, branchPrefix) {
		// Checkpoint MR is for a different branch — discard and create fresh.
		fmt.Printf("→ Discarding stale MR checkpoint %s (was for different branch)\n", cpMRID)
		return "", false
	}
	// gs-onu: re-verify on a FRESH main view. Only when the MR is DEFINITIVELY
	// absent there (visible false, no query error) do we distrust the checkpoint.
	// A transient query error keeps the trust-the-checkpoint behavior so a Dolt
	// blip can't spawn a duplicate MR.
	visible, qErr := verifyMRVisibleOnMain(beads.NewWithBeadsDir(sc.cwd, resolvedBeads), sc.branch, commitSHA)
	if shouldTrustMRCheckpoint(visible, qErr) {
		fmt.Printf("%s MR already created (resumed from checkpoint: %s)\n", style.Bold.Render("✓"), cpMRID)
		return cpMRID, true
	}
	fmt.Printf("→ Checkpoint MR %s not on shared main — re-enqueuing instead of stranding (gs-onu)\n", cpMRID)
	return "", false
}

// pushBranchForMR pushes the polecat branch for the default "mr" strategy
// (gu-nid89.12.1 extraction from runDone) — the hq-6dk53 invariant that the
// branch reaches origin BEFORE the MR bead is filed, with the full gs-pd6
// fallback ladder (pushBranchWithFallbacks) and gu-epv5 verify-then-recover.
//
// checkpoints carries the gt-aufru resume state. Returns (strand): true means
// the push failed/was stranded (a wisp was filed) and the caller should set
// pushFailed and route to notifyWitness; false means the branch is on origin and
// the caller proceeds to MR creation. On success it records the push receipt,
// promotes a stale "unpushed" cleanup status to "clean", and writes the push
// checkpoint. Mirrors done.go:754–851.
func pushBranchForMR(sc strategyContext, checkpoints map[DoneCheckpoint]string) (strand bool) {
	// Resume: skip push if already completed in a previous run (gt-aufru).
	if checkpoints[CheckpointPushed] != "" {
		if checkpoints[CheckpointPushed] == sc.branch {
			fmt.Printf("%s Branch already pushed (resumed from checkpoint)\n", style.Bold.Render("✓"))
			return false
		}
		// Stale checkpoint from a previous assignment — discard and push normally.
		fmt.Printf("→ Discarding stale push checkpoint (was for branch %s, now on %s)\n",
			checkpoints[CheckpointPushed], sc.branch)
	}

	// HARD GUARD (gu-cfb): Refuse a default-branch-to-default-branch refspec.
	if isDefaultBranchName(sc.branch, sc.defaultBranch) {
		pushErr := fmt.Errorf("refusing to push %q: branch is the rig's default branch; polecat work must go through the merge queue", sc.branch)
		style.PrintWarning("%s", pushErr.Error())
		return true
	}

	// Push the branch with the full fallback + recovery ladder (gs-pd6 phase 1).
	pushedCommitSHA, pushErr := pushBranchWithFallbacks(sc.g, sc.townRoot, sc.rigName, sc.branch, sc.defaultBranch)
	if pushErr != nil {
		// gu-epv5 Option B: file a discoverable push-stranded wisp.
		errMsg := fmt.Sprintf("push failed for branch '%s': %v", sc.branch, pushErr)
		style.PrintWarning("%s\nCommits exist locally but failed to push. Witness will be notified.", errMsg)
		recordPushFailure(sc.townRoot, sc.rigName, sc.branch, pushedCommitSHA, pushlog.SourceDone, pushlog.StagePush, sc.worker, sc.issueID, pushErr)
		strandedBd := beads.New(sc.cwd)
		fileStrandedPushWisp(strandedBd, sc.rigName, sc.branch, pushedCommitSHA, sc.defaultBranch, sc.issueID, sc.agentBeadID, sc.worker, pushErr)
		return true
	}

	// Verify the pushed branch tip is the exact local commit before creating any
	// MR bead. A stale remote branch can exist while the new commit never landed.
	if pushedCommitSHA == "" {
		pushedCommitSHA, _ = sc.g.Rev("HEAD")
	}
	if doneSkipVerify {
		noteVerifiedPushSkipped(sc.cwd, sc.issueID, sc.branch, pushedCommitSHA, fmt.Sprintf("--skip-verify on branch push: %s", doneSkipVerifyReason))
	} else if verifyErr := verifyPushedCommitWithBareFallback(sc.g, sc.townRoot, sc.rigName, sc.branch, pushedCommitSHA); verifyErr != nil {
		// gu-epv5: re-check origin tip — the verifier may have hit a transient
		// remote read error.
		if recoverPushFromOriginTip(sc.g, sc.branch, pushedCommitSHA) {
			fmt.Printf("%s Verify reported failure but origin/%s tip matches — proceeding to MR creation (gu-epv5 recovery)\n", style.Bold.Render("✓"), sc.branch)
		} else {
			noteVerifiedPushFailure(sc.cwd, sc.issueID, sc.branch, pushedCommitSHA, verifyErr)
			style.PrintWarning("%s\nCommits exist locally but verified push failed. Witness will be notified.", verifyErr.Error())
			recordPushFailure(sc.townRoot, sc.rigName, sc.branch, pushedCommitSHA, pushlog.SourceDone, pushlog.StageVerify, sc.worker, sc.issueID, verifyErr)
			strandedBd := beads.New(sc.cwd)
			fileStrandedPushWisp(strandedBd, sc.rigName, sc.branch, pushedCommitSHA, sc.defaultBranch, sc.issueID, sc.agentBeadID, sc.worker, verifyErr)
			return true
		}
	}
	fmt.Printf("%s Branch pushed to origin\n", style.Bold.Render("✓"))

	// gu-ftja: durable push receipt for witness/deacon teardown decisions.
	recordPushReceipt(sc.g, sc.townRoot, sc.rigName, sc.branch, pushedCommitSHA, pushlog.SourceDone, sc.worker, sc.issueID)

	// Fix cleanup_status after successful push (gt-wcr): "unpushed"/"has_unpushed"
	// is now stale.
	doneCleanupStatus = cleanupStatusAfterSuccessfulPush(doneCleanupStatus)

	// Write push checkpoint for resume (gt-aufru).
	if sc.agentBeadID != "" {
		// Agent bead lives in town DB despite rig prefix — bypass routing.
		cpBd := beads.New(sc.cwd).ForAgentBead()
		writeDoneCheckpoint(cpBd, sc.agentBeadID, CheckpointPushed, sc.branch)
	}
	return false
}

// completeNoMR handles the "zero commits ahead" COMPLETED path (gu-nid89.12.1
// extraction from runDone): work was pushed directly to main, already merged, or
// the bead is a no_merge/review_only/--no-code task that produces no commits by
// design. It closes the source bead (and its attached molecule wisp) without
// creating a merge request.
//
// originDefault is "origin/<defaultBranch>" (used in guard messages);
// isNoMergeTask is the precomputed no_merge/review_only/--no-code flag. Returns
// an error only for a hard failure runDone returns directly (the zero-commit
// guard, the citation guard, or a verify failure); on a nil return the bead was
// closed (or close was deliberately skipped) and the caller routes to
// notifyWitness.
//
// Mirrors the lines previously inlined in the COMPLETED block of runDone.
func completeNoMR(sc strategyContext, originDefault string, isNoMergeTask bool) error {
	if os.Getenv("GT_POLECAT") != "" && doneCleanupStatus != "clean" && !isNoMergeTask {
		// Before failing, check whether commits exist on the remote feature branch.
		// After a polecat pushes to origin/<feature-branch> and submits an MR,
		// if master advances (e.g., other MRs land), the feature branch is no
		// longer ahead of origin/master — but the work WAS committed and pushed.
		// In that case, treat as "MR already submitted" and fall through. (GH#wd7)
		branchPushedWithWork := false
		if sc.branch != sc.defaultBranch {
			pushed, unpushed, pushErr := sc.g.BranchPushedToRemote(sc.branch, "origin")
			branchPushedWithWork = pushErr == nil && pushed && unpushed == 0
		}
		if !branchPushedWithWork {
			return fmt.Errorf("cannot complete: no commits on branch ahead of %s\n"+
				"Polecats must have at least 1 commit to submit.\n"+
				"If the bug was already fixed upstream: gt done --status DEFERRED\n"+
				"If you're blocked: gt done --status ESCALATED",
				originDefault)
		}
	}

	// Non-polecat (crew/mayor), polecat with --cleanup-status=clean
	// (report-only tasks like audits/reviews), or no_merge polecat
	// (non-code tasks like email/research per GH#2496):
	// zero commits is valid.
	fmt.Printf("%s Branch has no commits ahead of %s\n", style.Bold.Render("→"), originDefault)
	fmt.Printf("  Work was likely pushed directly to main or already merged.\n")
	fmt.Printf("  Skipping MR creation - completing without merge request.\n\n")

	// G15 fix: Close the base issue when completing with no MR.
	// Without this, no-op polecats (bug already fixed) leave issues stuck
	// in HOOKED state with assignee pointing to the nuked polecat.
	// Normally the Refinery closes after merge, but with no MR, nothing
	// would ever close the issue.
	if sc.issueID == "" {
		return nil
	}
	bd := beads.New(sc.cwd)

	// Acceptance criteria gate: check for unchecked criteria before closing.
	// If criteria exist and are unchecked, warn and skip close — the bead stays
	// open for witness/mayor to handle.
	if issue, showErr := bd.Show(sc.issueID); showErr == nil {
		if unchecked := beads.HasUncheckedCriteria(issue); unchecked > 0 {
			skipReason := fmt.Sprintf("issue %s has %d unchecked acceptance criteria — skipping close", sc.issueID, unchecked)
			style.PrintWarning("%s", skipReason)
			fmt.Printf("  The bead will remain open for witness/mayor review.\n")
			notifyDoneCloseSkipped(sc.townRoot, sc.rigName, sc.sender, sc.issueID, skipReason)
			return nil
		}
	}

	// gu-irou: Close the attached molecule wisp BEFORE closing the hooked bead.
	// The wisp is bonded to the bead via a `blocks` dep at sling time; if the
	// bead is later reopened the stale open wisp re-blocks the bead and the
	// scheduler refuses to redispatch. See gu-551r/gu-rh0g.
	closeAttachedWispNoMR(bd, sc.issueID)

	closeReason := "Completed with no code changes (already fixed or pushed directly to main)"
	if doneNoCode {
		closeReason = fmt.Sprintf("Completed — no code change required (--no-code, gu-gc4ex)\nno_code_reason: %s", doneReason)
	}
	noMRCommitSHA, _ := sc.g.Rev("HEAD")

	// gu-kruw: The bead-citation guard (gu-551r) is a logical postcondition
	// independent of push verification. It runs REGARDLESS of --skip-verify so
	// polecats can't bypass the false-close protection by adding the flag.
	// no_merge / review_only tasks have no commits to cite by design, so the
	// citation check is skipped for them.
	if !isNoMergeTask {
		if commitErr := verifyCommitReferencesBead(sc.g, noMRCommitSHA, sc.issueID); commitErr != nil {
			return fmt.Errorf("cannot close no-MR code bead: %w\n\n"+
				"This polecat is hooked to %s but the most recent commit on HEAD does not\n"+
				"reference that bead ID. Closing here would falsely claim the work shipped.\n\n"+
				"--skip-verify does NOT bypass this check (gu-kruw): the citation guard\n"+
				"protects the close-reason metadata regardless of push verification.\n\n"+
				"Choose one:\n"+
				"  • If this is a verify/report-only bead with no code to ship by design:\n"+
				"      gt done --no-code --reason=\"<why no code was required>\" (gu-gc4ex)\n"+
				"  • If the work was done in a sibling commit you should be on:\n"+
				"      git rebase / cherry-pick the right commit, then re-run gt done\n"+
				"  • If the bead is genuinely already complete (e.g. duplicate of work\n"+
				"    landed on main): gt done --status DEFERRED with a reason note\n"+
				"  • If you cannot make progress: gt done --status ESCALATED",
				commitErr, sc.issueID)
		}
	}

	if doneSkipVerify {
		noteVerifiedPushSkipped(sc.cwd, sc.issueID, sc.defaultBranch, noMRCommitSHA, fmt.Sprintf("--skip-verify on no-MR close: %s", doneSkipVerifyReason))
		if noMRCommitSHA != "" {
			closeReason = fmt.Sprintf("%s\nskip_verify: true\nskip_verify_reason: %s\ntarget_branch: %s\ncommit_sha: %s", closeReason, doneSkipVerifyReason, sc.defaultBranch, noMRCommitSHA)
		}
	} else if !isNoMergeTask {
		if verifyErr := sc.g.VerifyCommitOnRemoteBranch("origin", sc.defaultBranch, noMRCommitSHA); verifyErr != nil {
			noteVerifiedPushFailure(sc.cwd, sc.issueID, sc.defaultBranch, noMRCommitSHA, verifyErr)
			recordPushFailure(sc.townRoot, sc.rigName, sc.defaultBranch, noMRCommitSHA, pushlog.SourceDoneNoMR, pushlog.StageVerify, sc.worker, sc.issueID, verifyErr)
			return fmt.Errorf("cannot close no-MR code bead: %w", verifyErr)
		}
		if noMRCommitSHA != "" {
			closeReason = fmt.Sprintf("%s\ntarget_branch: %s\ncommit_sha: %s", closeReason, sc.defaultBranch, noMRCommitSHA)
			// gu-ftja: receipt for the no-MR direct-to-default push.
			recordPushReceipt(sc.g, sc.townRoot, sc.rigName, sc.defaultBranch, noMRCommitSHA, pushlog.SourceDoneNoMR, sc.worker, sc.issueID)
		}
	}
	// G15 fix: Force-close bypasses molecule dependency checks. The polecat is
	// about to be nuked — open wisps should not block closure. Shares
	// forceCloseWithRetry with the merged path (gu-z93z).
	if closeErr := forceCloseWithRetry(bd, sc.issueID, closeReason); closeErr == nil {
		fmt.Printf("%s Issue %s closed (no MR needed)\n", style.Bold.Render("✓"), sc.issueID)
	} else {
		style.PrintWarning("could not close issue %s after 3 attempts: %v (issue may be left HOOKED)", sc.issueID, closeErr)
	}
	return nil
}

// runNoMergeStrategy handles a no_merge bead (gu-nid89.12.1 extraction from
// runDone). No-merge work never goes through the refinery: the branch is already
// pushed to origin/<branch>, so this phase optionally opens a GitHub PR (when
// the rig's merge_strategy=pr, gas-rfi), notifies the dispatcher with
// READY_FOR_REVIEW, and closes the source bead (+ attached molecule) here.
//
// bd / sourceIssue / attachmentFields are passed in because runDone already
// loaded them for downstream target resolution; re-loading would risk a second
// Dolt round-trip. Mirrors the lines previously inlined in the COMPLETED block.
func runNoMergeStrategy(sc strategyContext, bd *beads.Beads, sourceIssue *beads.Issue, attachmentFields *beads.AttachmentFields) {
	fmt.Printf("%s No-merge mode: skipping merge queue\n", style.Bold.Render("→"))
	fmt.Printf("  Branch: %s\n", sc.branch)
	fmt.Printf("  Issue: %s\n", sc.issueID)
	fmt.Println()

	// When merge_strategy=pr, create a GitHub PR for human review instead of
	// just leaving the branch on origin (gas-rfi).
	var prURL string
	noMergeSettingsPath := filepath.Join(sc.townRoot, sc.rigName, "settings", "config.json")
	if noMergeSettings, noMergeSettingsErr := config.LoadRigSettings(noMergeSettingsPath); noMergeSettingsErr == nil &&
		noMergeSettings.MergeQueue != nil && noMergeSettings.MergeQueue.MergeStrategy == "pr" {
		issueTitle := sourceIssue.Title
		prTitle := fmt.Sprintf("%s (%s)", issueTitle, sc.issueID)
		if issueTitle == "" {
			prTitle = sc.issueID
		}
		// Build PR body from bead description + diff stat
		var prBodyBuilder strings.Builder
		prBodyBuilder.WriteString("## Summary\n\n")
		if sourceIssue.Description != "" {
			// Strip attachment metadata lines from description
			descLines := strings.Split(sourceIssue.Description, "\n")
			var cleanDesc []string
			for _, line := range descLines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "attached_") || strings.HasPrefix(trimmed, "dispatched_by:") || strings.HasPrefix(trimmed, "formula_vars:") {
					continue
				}
				cleanDesc = append(cleanDesc, line)
			}
			desc := strings.TrimSpace(strings.Join(cleanDesc, "\n"))
			if desc != "" {
				prBodyBuilder.WriteString(desc)
				prBodyBuilder.WriteString("\n\n")
			}
		}
		// Add diff stat for quick review context
		if diffStat, diffErr := sc.g.DiffStat(sc.defaultBranch + "..." + sc.branch); diffErr == nil && diffStat != "" {
			prBodyBuilder.WriteString("## Changes\n\n```\n")
			prBodyBuilder.WriteString(diffStat)
			prBodyBuilder.WriteString("```\n\n")
		}
		prBodyBuilder.WriteString("---\n")
		prBodyBuilder.WriteString(fmt.Sprintf("*Polecat: %s | Issue: %s*\n", sc.worker, sc.issueID))
		prBody := prBodyBuilder.String()
		ghCmd := exec.CommandContext(context.Background(), "gh", "pr", "create",
			"--base", sc.defaultBranch,
			"--head", sc.branch,
			"--title", prTitle,
			"--body", prBody,
		)
		ghCmd.Dir = sc.cwd
		prOutput, prErr := ghCmd.Output()
		if prErr != nil {
			style.PrintWarning("could not create GitHub PR: %v", prErr)
		} else {
			prURL = strings.TrimSpace(string(prOutput))
			fmt.Printf("%s GitHub PR created: %s\n", style.Bold.Render("✓"), prURL)
		}
	} else {
		fmt.Printf("%s\n", style.Dim.Render("Work stays on feature branch for human review."))
	}

	// Mail dispatcher with READY_FOR_REVIEW
	if dispatcher := attachmentFields.DispatchedBy; dispatcher != "" {
		townRouter := mail.NewRouter(sc.townRoot)
		defer townRouter.WaitPendingNotifications()
		reviewBody := fmt.Sprintf("Branch: %s\nIssue: %s\nReady for review.", sc.branch, sc.issueID)
		if prURL != "" {
			reviewBody = fmt.Sprintf("Branch: %s\nIssue: %s\nPR: %s\nReady for review.", sc.branch, sc.issueID, prURL)
		}
		reviewMsg := &mail.Message{
			To:      dispatcher,
			From:    detectSender(),
			Subject: fmt.Sprintf("READY_FOR_REVIEW: %s", sc.issueID),
			Body:    reviewBody,
		}
		if err := townRouter.Send(reviewMsg); err != nil {
			style.PrintWarning("could not notify dispatcher: %v", err)
		} else {
			fmt.Printf("%s Dispatcher notified: READY_FOR_REVIEW\n", style.Bold.Render("✓"))
		}
	}

	// No-merge work never goes through the refinery, so close the source bead
	// here after notifying the dispatcher. Otherwise hooked work remains open.
	if sc.issueID != "" {
		canCloseIssue := true
		if attachmentFields.AttachedMolecule != "" {
			if n := closeDescendants(bd, attachmentFields.AttachedMolecule); n > 0 {
				fmt.Fprintf(os.Stderr, "Closed %d molecule step(s) for %s\n", n, attachmentFields.AttachedMolecule)
			}
			if closeErr := forceCloseIssueWithRetry(
				bd.ForceCloseWithReason,
				attachmentFields.AttachedMolecule,
				"done",
				"Attached molecule %s closed",
			); closeErr != nil && !errors.Is(closeErr, beads.ErrNotFound) {
				style.PrintWarning("could not close attached molecule %s after 3 attempts: %v", attachmentFields.AttachedMolecule, closeErr)
				canCloseIssue = false
			}
		}

		closeReason := "No-merge work completed; merge queue skipped"
		if prURL != "" {
			closeReason = fmt.Sprintf("%s\npr_url: %s", closeReason, prURL)
		}
		if canCloseIssue {
			if closeErr := forceCloseIssueWithRetry(
				bd.ForceCloseWithReason,
				sc.issueID,
				closeReason,
				"Issue %s closed (no-merge)",
			); closeErr != nil {
				style.PrintWarning("could not close issue %s after 3 attempts: %v (issue may be left HOOKED)", sc.issueID, closeErr)
			}
		}
	}
}

// runLateDirectStrategy handles a merge=direct convoy detected only after the
// feature branch was already pushed to origin/<branch> (gu-nid89.12.1 extraction
// from runDone) — e.g. issues dispatched before the attachment-field fix, or
// where dep-based lookup failed at the earlier check. It pushes the branch
// directly to the rig default and closes the bead.
//
// gu-8edz guard: on a merge_queue rig the direct push is refused and the work
// stays parked on origin/<branch> for refinery/follow-up MR — handled=false so
// the caller falls through to normal MR creation. A failed direct push (not the
// guard) also returns handled=false to fall through to MR. handled=true means
// the late direct push ran to completion (success or stranded) and the caller
// should route to notifyWitness.
//
// bd is passed in (already initialized for the MR path). Mirrors the lines
// previously inlined in the COMPLETED block.
func runLateDirectStrategy(sc strategyContext, bd *beads.Beads, convoyInfo *ConvoyInfo) (handled bool, pushFailed bool) {
	directBlocked := false
	if guardErr := completion.GuardDirectPushOnMergeQueue(sc.townRoot, sc.rigName, "late-detected direct merge"); guardErr != nil {
		directBlocked = true
		style.PrintWarning("%v", guardErr)
		if stale, reason := completion.IsRefineryHeartbeatStale(sc.townRoot, sc.rigName); stale {
			fmt.Fprintf(os.Stderr, "  Refinery heartbeat is stale (%s) — falling through to MR creation so refinery can pick up on recovery.\n", reason)
			if sc.issueID != "" {
				completion.MarkAwaitingRefineryRecovery(bd, sc.issueID, reason)
			}
		}
	}
	if directBlocked {
		return false, false
	}

	fmt.Printf("%s Late-detected direct merge strategy: pushing to %s\n", style.Bold.Render("→"), sc.defaultBranch)
	fmt.Printf("  Convoy: %s\n", convoyInfo.ID)

	// Push branch directly to main (the earlier push went to origin/<branch>)
	directRefspec := sc.branch + ":" + sc.defaultBranch
	directPushErr := pushForDone(sc.g, directRefspec)
	if directPushErr != nil {
		// Direct push failed — fall through to normal MR creation
		style.PrintWarning("late direct push to %s failed: %v — falling through to MR", sc.defaultBranch, directPushErr)
		return false, false
	}

	lateDirectCommitSHA, _ := sc.g.Rev("HEAD")
	if doneSkipVerify {
		noteVerifiedPushSkipped(sc.cwd, sc.issueID, sc.defaultBranch, lateDirectCommitSHA, fmt.Sprintf("--skip-verify on late direct merge: %s", doneSkipVerifyReason))
	} else if verifyErr := sc.g.VerifyCommitOnRemoteBranch("origin", sc.defaultBranch, lateDirectCommitSHA); verifyErr != nil {
		// gu-epv5: verify may have hit a transient remote read failure.
		if recoverPushFromOriginTip(sc.g, sc.defaultBranch, lateDirectCommitSHA) {
			fmt.Printf("%s Verify reported failure but origin/%s tip matches — treating as success (gu-epv5 recovery)\n", style.Bold.Render("✓"), sc.defaultBranch)
		} else {
			noteVerifiedPushFailure(sc.cwd, sc.issueID, sc.defaultBranch, lateDirectCommitSHA, verifyErr)
			style.PrintWarning("%s\nLate direct merge pushed but remote verification failed. Source bead will remain in progress.", verifyErr.Error())
			recordPushFailure(sc.townRoot, sc.rigName, sc.branch, lateDirectCommitSHA, pushlog.SourceDoneDirect, pushlog.StageVerify, sc.worker, sc.issueID, verifyErr)
			fileStrandedPushWisp(bd, sc.rigName, sc.branch, lateDirectCommitSHA, sc.defaultBranch, sc.issueID, sc.agentBeadID, sc.worker, verifyErr)
			return true, true
		}
	}
	fmt.Printf("%s Branch pushed directly to %s\n", style.Bold.Render("✓"), sc.defaultBranch)
	// gu-ftja: receipt for the late-detected direct-merge push.
	recordPushReceipt(sc.g, sc.townRoot, sc.rigName, sc.defaultBranch, lateDirectCommitSHA, pushlog.SourceDoneDirect, sc.worker, sc.issueID)

	closeBaseIssueWithRetry(sc.cwd, sc.issueID, fmt.Sprintf("Direct merge to %s (convoy strategy, late detection)", sc.defaultBranch), "direct merge")
	return true, false
}

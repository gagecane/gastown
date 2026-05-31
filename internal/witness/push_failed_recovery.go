package witness

import (
	"fmt"
	"os"
	"sync"

	"github.com/steveyegge/gastown/internal/git"
)

// PushRecoveryOutcome enumerates the possible results of attempting to recover
// a stranded polecat branch when the agent bead has push_failed=true and
// branchOnOrigin reports the branch is genuinely missing upstream.
//
// gu-ebj0: This is the proposal #1 follow-up to gu-01ef. branchOnOrigin
// (gu-ww9u) covers the "branch is actually upstream" fast-path; this enum
// describes what the witness does when the branch is NOT upstream and we
// need to push it ourselves rather than just escalating to mayor.
type PushRecoveryOutcome int

const (
	// PushRecoveryUnknown means recovery was not attempted (worktree missing,
	// branch resolution failed, etc). Caller falls back to existing escalate
	// path.
	PushRecoveryUnknown PushRecoveryOutcome = iota

	// PushRecoveryAlreadyOnOrigin means a re-check showed the branch IS on
	// origin (race with branchOnOrigin or out-of-band recovery). Caller
	// should clear push_failed and route through normal completion.
	PushRecoveryAlreadyOnOrigin

	// PushRecoveryPushed means we successfully pushed the branch — local was
	// ahead of (or identical to) origin/<branch>, push succeeded. Caller
	// should clear push_failed and route through normal completion so the MR
	// path can pick it up.
	PushRecoveryPushed

	// PushRecoveryDiverged means origin and local have incompatible histories
	// (origin/<branch> is not an ancestor of HEAD AND HEAD is not an ancestor
	// of origin/<branch>). Caller MUST escalate to mayor — we won't force-push
	// a polecat branch.
	PushRecoveryDiverged

	// PushRecoveryBackoff means the per-branch retry budget is exhausted.
	// Caller leaves the bead in push_failed state for a future patrol cycle
	// to retry (or for mayor to investigate).
	PushRecoveryBackoff
)

// String returns a stable lowercase token suitable for embedding in
// HandlerResult.Action / CompletionDiscovery.Action strings.
func (o PushRecoveryOutcome) String() string {
	switch o {
	case PushRecoveryAlreadyOnOrigin:
		return "already-on-origin"
	case PushRecoveryPushed:
		return "pushed"
	case PushRecoveryDiverged:
		return "diverged"
	case PushRecoveryBackoff:
		return "backoff"
	case PushRecoveryUnknown:
		fallthrough
	default:
		return "unknown"
	}
}

// pushRecoveryMaxAttempts caps how many times a single (rig, polecat, branch)
// triple may be auto-recovered within the lifetime of one witness process.
// On the next witness restart the counter resets — branchOnOrigin's
// fast-path will catch any out-of-band fixes and the budget renews.
//
// Kept intentionally small so a wedged rig (e.g., remote consistently 5xx
// on push) bounces to mayor escalation quickly rather than spinning forever.
const pushRecoveryMaxAttempts = 3

// pushRecoveryBudget tracks attempts in-process. The cross-process backoff
// pattern from polecat_startup_backoff.go is overkill for this path because
// (a) witness restarts are rare and observable, (b) branchOnOrigin already
// short-circuits when the branch landed via any other path, and (c) the
// per-branch escalation goes to mayor on budget exhaustion anyway.
var (
	pushRecoveryMu      sync.Mutex
	pushRecoveryBudget  = make(map[string]int)
)

func pushRecoveryKey(rigName, polecatName, branch string) string {
	return rigName + "/" + polecatName + "/" + branch
}

// chargePushRecovery increments the budget counter and returns whether the
// caller is still within the cap. It is the single entry point for budget
// accounting so tests can reason about it from one place.
func chargePushRecovery(rigName, polecatName, branch string) (within bool, attempt int) {
	if branch == "" {
		// Nothing to key on — refuse to recover but don't count it as an
		// attempt either; the caller will fall to escalate.
		return false, 0
	}
	pushRecoveryMu.Lock()
	defer pushRecoveryMu.Unlock()
	key := pushRecoveryKey(rigName, polecatName, branch)
	pushRecoveryBudget[key]++
	attempt = pushRecoveryBudget[key]
	return attempt <= pushRecoveryMaxAttempts, attempt
}

// resetPushRecoveryBudget clears budget state. Intended for tests only.
func resetPushRecoveryBudget() {
	pushRecoveryMu.Lock()
	defer pushRecoveryMu.Unlock()
	pushRecoveryBudget = make(map[string]int)
}

// recoverPushFailed attempts to push a stranded polecat branch to origin.
// It is invoked when the agent bead's PushFailed flag is set AND
// branchOnOrigin reports the branch is genuinely missing upstream.
//
// Flow (mirrors gu-ebj0 acceptance criteria):
//
//  1. Open the polecat's worktree. If it is missing or not a git repo,
//     return PushRecoveryUnknown — caller escalates as before.
//  2. Re-read origin/<branch> tip. If origin already has the branch:
//     a. If origin/<branch> == HEAD: PushRecoveryAlreadyOnOrigin (race-safe).
//     b. If origin/<branch> is an ancestor of HEAD: attempt fast-forward push.
//        Success → PushRecoveryPushed. Failure → PushRecoveryDiverged.
//     c. If HEAD is an ancestor of origin/<branch>: PushRecoveryAlreadyOnOrigin
//        (origin moved ahead — local has nothing to add).
//     d. Otherwise: PushRecoveryDiverged (incompatible histories).
//  3. If origin does NOT have the branch: attempt a fresh push.
//     Success → PushRecoveryPushed. Failure → PushRecoveryDiverged
//     (branch absent + push rejected = something we can't auto-resolve).
//
// Force-push is NEVER attempted — by construction polecat branches are
// fork-only and a divergent origin tip means another agent or human has
// taken ownership of the ref. Mayor escalation is the right answer there.
//
// Package-level var so tests can override.
var recoverPushFailed = _recoverPushFailed

func _recoverPushFailed(townRoot, rigName, polecatName, branch string) PushRecoveryOutcome {
	if townRoot == "" || branch == "" {
		return PushRecoveryUnknown
	}

	within, _ := chargePushRecovery(rigName, polecatName, branch)
	if !within {
		return PushRecoveryBackoff
	}

	polecatPath := polecatWorktreePath(townRoot, rigName, polecatName)
	if _, err := os.Stat(polecatPath); err != nil {
		return PushRecoveryUnknown
	}
	g := git.NewGit(polecatPath)
	if !g.IsRepo() {
		return PushRecoveryUnknown
	}

	headSHA, headErr := g.Rev("HEAD")
	if headErr != nil {
		return PushRecoveryUnknown
	}

	// Pick the first remote (mirrors branchOnOrigin); fall back to "origin".
	remotes, _ := g.Remotes()
	remote := "origin"
	if len(remotes) > 0 {
		remote = remotes[0]
	}

	originTip, tipErr := g.PushRemoteBranchTip(remote, branch)
	if tipErr != nil {
		// ls-remote failed — treat as unknown so caller escalates rather
		// than auto-pushing into an indeterminate remote state.
		return PushRecoveryUnknown
	}

	if originTip != "" {
		// Origin already has the branch.
		if originTip == headSHA {
			return PushRecoveryAlreadyOnOrigin
		}
		// Is HEAD ahead of origin? (origin is ancestor of HEAD → fast-forward push.)
		if isAnc, err := g.IsAncestor(originTip, headSHA); err == nil && isAnc {
			if err := g.Push(remote, branch, false); err != nil {
				return PushRecoveryDiverged
			}
			return PushRecoveryPushed
		}
		// Is origin ahead of HEAD? (HEAD is ancestor of origin → nothing to add.)
		if isAnc, err := g.IsAncestor(headSHA, originTip); err == nil && isAnc {
			return PushRecoveryAlreadyOnOrigin
		}
		// Diverged.
		return PushRecoveryDiverged
	}

	// Origin doesn't have the branch — fresh push.
	if err := g.Push(remote, branch, false); err != nil {
		return PushRecoveryDiverged
	}
	return PushRecoveryPushed
}

// pushRecoveryActionString returns the witness Action label for an outcome,
// used by HandlePolecatDoneFromBead and processDiscoveredCompletion to
// describe what the recovery handler did. The prefix "push-failed-recovery-"
// is preserved so existing log scrapers continue to match.
func pushRecoveryActionString(outcome PushRecoveryOutcome, polecatName, branch, issueID string) string {
	return fmt.Sprintf("push-failed-recovery-%s for %s (branch=%s issue=%s)",
		outcome.String(), polecatName, branch, issueID)
}

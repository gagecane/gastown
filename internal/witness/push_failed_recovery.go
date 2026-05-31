package witness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// patchEquivalenceChecker is the subset of *git.Git that classifyCherryUnmerged
// needs. Defined as an interface so tests can drive the patch-equivalence
// decision without a real repo.
type patchEquivalenceChecker interface {
	Cherry(upstream, head string) (string, error)
	IsAncestor(ancestor, descendant string) (bool, error)
}

// isPatchEquivalent reports whether the polecat's work on HEAD has already
// shipped on origin/<branch> — even when SHAs differ from a post-push rebase.
// This is the rebase-after-push detector for gu-l0u0.
//
// Algorithm:
//
//  1. Run `git cherry origin/<branch> HEAD`. Each commit on HEAD becomes a
//     "+ <sha>" (patch missing on origin/<branch>) or "- <sha>" (patch
//     already applied upstream, possibly under a different SHA — the
//     rebase-replay case).
//  2. If there are zero "+" lines, every local patch is patch-equivalent to
//     origin/<branch>. The work shipped.
//  3. If there are "+" lines, they could be EITHER:
//     a. Genuine unique work not on origin/<branch> → diverged.
//     b. Mainline commits that the rebase swept onto HEAD (e.g., the new
//        commits on origin/<defaultBranch> that the rebase incorporated).
//        Origin/<branch> wouldn't have these, but they aren't "unique work"
//        either — they're already on the target. Treat as patch-equivalent.
//
// We discriminate (3a) vs (3b) by checking whether each "+" SHA is an ancestor
// of origin/<defaultBranch>. If every "+" SHA is on mainline, the only
// commits HEAD has that origin/<branch> doesn't are mainline commits the
// rebase pulled in — work is shipped. If any "+" SHA is NOT on mainline AND
// NOT patch-equivalent to origin/<branch>, that's genuine unique work →
// diverged.
//
// Best-effort: any error returns false (caller falls through to the existing
// diverged path). False positives would silently lose work, so we err on the
// side of conservative classification.
//
// `git cherry` against a remote-tracking branch requires the remote ref to be
// locally cached. We fetch the specific branch first; failure is non-fatal.
func isPatchEquivalent(g *git.Git, townRoot, rigName, remote, branch string) bool {
	if g == nil || remote == "" || branch == "" {
		return false
	}
	// Refresh the remote-tracking refs so cherry/IsAncestor compare against
	// the freshest origin state. Non-fatal: stale refs produce a conservative
	// "diverged" answer rather than a wrong one.
	_ = g.FetchBranch(remote, branch)

	defaultBranch := resolveDefaultBranch(townRoot, rigName)
	_ = g.FetchBranch(remote, defaultBranch)

	mainlineRef := remote + "/" + defaultBranch
	return cherryAllShippedOrOnMainline(g, remote+"/"+branch, "HEAD", mainlineRef)
}

// resolveDefaultBranch returns the rig's configured default branch, or "main"
// when the rig config can't be read. Best-effort lookup.
func resolveDefaultBranch(townRoot, rigName string) string {
	defaultBranch := "main"
	if townRoot != "" && rigName != "" {
		if rigCfg, err := rig.LoadRigConfig(filepath.Join(townRoot, rigName)); err == nil && rigCfg.DefaultBranch != "" {
			defaultBranch = rigCfg.DefaultBranch
		}
	}
	return defaultBranch
}

// cherryAllShippedOrOnMainline returns true when every commit on `head` is
// either patch-equivalent to one already on `upstream` (a "-" line in
// `git cherry`) OR an ancestor of `mainlineRef` (a mainline commit the
// rebase incorporated). Factored out from isPatchEquivalent so tests can
// drive the decision against a stub git.
//
// Returns false on any error or unexpected output — conservative classification
// preserves the existing "diverged → escalate to mayor" behavior.
func cherryAllShippedOrOnMainline(g patchEquivalenceChecker, upstream, head, mainlineRef string) bool {
	if g == nil || upstream == "" || head == "" {
		return false
	}
	out, err := g.Cherry(upstream, head)
	if err != nil {
		return false
	}
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// "- <sha>": patch already on upstream by patch-id. Always shipped.
		if strings.HasPrefix(line, "-") {
			continue
		}
		if !strings.HasPrefix(line, "+") {
			// Unexpected format — bail conservatively.
			return false
		}
		// "+ <sha>": patch not on upstream. Acceptable only if the commit is
		// already an ancestor of mainline (rebase swept it in). Otherwise
		// it's unique unshipped work.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return false
		}
		sha := fields[1]
		if mainlineRef == "" {
			// No mainline reference to test against — can't justify the "+".
			return false
		}
		isOnMainline, ancErr := g.IsAncestor(sha, mainlineRef)
		if ancErr != nil || !isOnMainline {
			return false
		}
	}
	return true
}

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

	// PushRecoveryPatchEquivalent means origin and local diverged textually
	// (different SHAs, neither an ancestor of the other) BUT every local patch
	// is already present on origin/<branch> — the rebase-after-push pattern
	// (gu-l0u0). Caller treats this like AlreadyOnOrigin: clear push_failed
	// and route through normal completion. The work shipped; the local SHAs
	// are just the post-rebase replay of patches that already landed.
	//
	// Why a distinct outcome: operators benefit from knowing this happened
	// (it explains why a "diverged" branch was treated as recovered), and
	// log scrapers / metrics may want to track rebase-after-push frequency
	// separately from clean re-checks.
	PushRecoveryPatchEquivalent
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
	case PushRecoveryPatchEquivalent:
		return "patch-equivalent"
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
	pushRecoveryMu     sync.Mutex
	pushRecoveryBudget = make(map[string]int)
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
// Flow (mirrors gu-ebj0 acceptance criteria, extended for gu-l0u0):
//
//  1. Open the polecat's worktree. If it is missing or not a git repo,
//     return PushRecoveryUnknown — caller escalates as before.
//  2. Re-read origin/<branch> tip. If origin already has the branch:
//     a. If origin/<branch> == HEAD: PushRecoveryAlreadyOnOrigin (race-safe).
//     b. If origin/<branch> is an ancestor of HEAD: attempt fast-forward push.
//     Success → PushRecoveryPushed. Failure → PushRecoveryDiverged.
//     c. If HEAD is an ancestor of origin/<branch>: PushRecoveryAlreadyOnOrigin
//     (origin moved ahead — local has nothing to add).
//     d. Otherwise (textual divergence): run `git cherry origin/<branch> HEAD`.
//     If every local patch is already present on origin/<branch> (count of
//     "+ " lines == 0), return PushRecoveryPatchEquivalent — this is the
//     rebase-after-push pattern (gu-l0u0). Otherwise: PushRecoveryDiverged.
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
		// Textual divergence: SHAs differ and neither is an ancestor of the other.
		// Before declaring this a hard divergence, check patch-equivalence (gu-l0u0).
		// The rebase-after-push pattern produces exactly this shape: an earlier
		// push delivered the patches under SHA X, then a subsequent rebase on
		// the local branch picked up new mainline commits and replayed the same
		// patches under SHA Y. The work IS shipped; only the SHAs differ.
		//
		// `git cherry <upstream> <head>` reports each commit on HEAD with "+ "
		// (patch missing on upstream) or "- " (patch already applied upstream
		// by patch-id). If zero "+" lines, every local patch is already on
		// origin → patch-equivalent → treat like AlreadyOnOrigin.
		if isPatchEquivalent(g, townRoot, rigName, remote, branch) {
			return PushRecoveryPatchEquivalent
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

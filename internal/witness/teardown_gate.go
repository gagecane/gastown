// Pre-teardown verification gate (gu-gn1a, follow-up to gu-ftlw / gu-ftja).
//
// Background: removePolecatWorktree (cmd/convoy.go) and NukePolecat (this
// package) used to trust pre-computed cleanup_status strings derived from
// earlier ls-remote queries. Those strings can become stale: a fork branch
// observed on origin at decision time may have been reaped (post-merge or
// fork-sync) by the time teardown runs. When that happens, the local commits
// disappear with the worktree and there is no recovery path — the symptom
// observed in gu-ftlw / gu-r63t (commit 11bfbabe lost).
//
// This gate fires immediately before destructive teardown and requires ONE
// of the following to hold, otherwise it escalates the polecat for manual
// recovery instead of nuking:
//
//	(a) A durable push receipt (gu-ftja) for the polecat's branch matching
//	    its current HEAD SHA — proves the push happened, even if the remote
//	    branch has since been reaped.
//	(b) A live VerifyPushedCommit against the recorded SHA — proves the
//	    branch is still on origin at the expected SHA right now.
//	(c) classifyPolecatMergeState reports MergeCheckMerged or MergeCheckEmpty
//	    — work is already on the rig's default branch (fast-forward, regular
//	    merge, squash-merge via cherry-pick patch-id), or the polecat
//	    produced no commits beyond base.
//	(d) An open gt:merge-request wisp exists for the polecat's branch at its
//	    current HEAD SHA (gs-3ece). The MR wisp is only created by `gt done`
//	    after a verified push, so its existence is durable proof-of-push even
//	    when (b)'s live ls-remote is flaky or transiently fails. This closes
//	    the false-positive where a pushed branch with an enqueued MR was
//	    refused archive ("no proof of push") and re-flagged as a zombie every
//	    witness patrol cycle until the refinery merged the MR.
//
// If none hold, the polecat almost certainly has unmerged local-only work
// that would be irrecoverably lost by teardown. The gate returns an error
// suitable for escalation; callers must surface that error rather than
// proceeding with the destructive operation.
package witness

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/pushlog"
	"github.com/steveyegge/gastown/internal/workspace"
)

// ErrTeardownUnsafe is returned by VerifyTeardownSafe when none of the
// (a)/(b)/(c) push-proof predicates hold. Callers wrap or surface the
// error so the polecat is escalated instead of nuked.
var ErrTeardownUnsafe = errors.New("teardown gate: no proof of push")

// teardownLivePushVerify is the live-push-tip verification predicate.
// Factored out as a package-level var so tests can override it without
// requiring a real origin remote on disk.
var teardownLivePushVerify = func(g *git.Git, branch, commit string) error {
	return g.VerifyPushedCommit("origin", branch, commit)
}

// teardownFindMRForBranchAndSHA is the open-merge-request push-proof predicate
// (d). Factored out as a package-level var so tests can override it without a
// real beads/Dolt backend. Returns the matching open MR wisp (or nil) for the
// branch at the given commit SHA.
var teardownFindMRForBranchAndSHA = func(workDir, branch, commitSHA string) (*beads.Issue, error) {
	b := beads.New(beads.ResolveBeadsDir(workDir))
	return b.FindMRForBranchAndSHA(branch, commitSHA)
}

// VerifyTeardownSafe is the public entrypoint to the pre-teardown gate.
// Package-level var so tests can override the entire gate where needed.
var VerifyTeardownSafe = _verifyTeardownSafe

// _verifyTeardownSafe is the default implementation. Returns nil when any of
// the (a)/(b)/(c) predicates holds, an error wrapping ErrTeardownUnsafe
// otherwise. A missing worktree is treated as safe: there is nothing to
// protect.
func _verifyTeardownSafe(workDir, rigName, polecatName string) error {
	if rigName == "" || polecatName == "" {
		// Defensive: callers should never pass empty names. If they do,
		// fail closed — refusing to nuke is the safe default.
		return fmt.Errorf("%w: empty rig or polecat name", ErrTeardownUnsafe)
	}

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		return fmt.Errorf("%w: finding town root: %v", ErrTeardownUnsafe, err)
	}

	polecatPath := polecatWorktreePath(townRoot, rigName, polecatName)
	if _, err := os.Stat(polecatPath); err != nil {
		// Worktree already gone — nothing to lose.
		return nil
	}

	g := git.NewGit(polecatPath)
	headRaw, err := g.Rev("HEAD")
	if err != nil {
		return fmt.Errorf("%w: reading HEAD for %s/%s: %v", ErrTeardownUnsafe, rigName, polecatName, err)
	}
	headSHA := strings.TrimSpace(headRaw)
	if headSHA == "" {
		return fmt.Errorf("%w: empty HEAD for %s/%s", ErrTeardownUnsafe, rigName, polecatName)
	}

	branchRaw, _ := g.CurrentBranch()
	branch := strings.TrimSpace(branchRaw)

	// (c) Already on default branch — work has landed (or polecat produced
	// no commits beyond base, which is also safe to nuke). Use the same
	// classifier as the unfiled-MR recovery path so squash-merged work is
	// recognized via cherry-pick patch-id, not just ancestor checks.
	if state, err := classifyPolecatMergeState(workDir, rigName, polecatName); err == nil {
		switch state {
		case MergeCheckMerged, MergeCheckEmpty:
			return nil
		}
	}

	// Without a real branch we cannot do (a) or (b). Detached HEAD with
	// unmerged work is the worst-case "escalate, do not nuke" scenario.
	if branch == "" || branch == "HEAD" {
		return fmt.Errorf("%w: detached HEAD at %s for %s/%s; escalate for manual recovery",
			ErrTeardownUnsafe, shortSHAForGate(headSHA), rigName, polecatName)
	}

	// (a) Durable push receipt matching current HEAD.
	if receipt, err := pushlog.FindByBranch(townRoot, rigName, branch); err == nil && receipt != nil {
		if strings.TrimSpace(receipt.CommitSHA) == headSHA {
			return nil
		}
	}

	// (b) Live VerifyPushedCommit.
	if err := teardownLivePushVerify(g, branch, headSHA); err == nil {
		return nil
	}

	// (d) Open merge-request wisp for this branch at the current HEAD SHA.
	// The MR wisp is created by `gt done` only after a verified push, so it is
	// durable proof-of-push even when (b)'s live ls-remote is flaky (gs-3ece).
	if mr, err := teardownFindMRForBranchAndSHA(workDir, branch, headSHA); err == nil && mr != nil {
		return nil
	}

	return fmt.Errorf("%w for %s/%s branch=%s sha=%s; escalate for manual recovery",
		ErrTeardownUnsafe, rigName, polecatName, branch, shortSHAForGate(headSHA))
}

// shortSHAForGate is a local SHA shortener for gate error messages. Mirrors
// internal/git.shortSHA, kept private here to avoid widening that helper's
// public surface.
func shortSHAForGate(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

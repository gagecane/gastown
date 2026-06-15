// Fork-sync merge topology preservation.
//
// When a fork-sync MR (a polecat branch that has merged the fork's upstream
// into itself) is squash-merged by refinery, the merge topology is destroyed:
// the upstream commits' content is preserved, but `git merge-base --is-ancestor
// upstream/<branch> HEAD` fails on the target because there is no second-parent
// edge back to upstream.
//
// This broke the `scripts/check-upstream-rebased.sh` pre-merge gate in
// gu-9yi3 (follow-up to gu-nt9z): gu-nt9z's polecat correctly produced a
// merge commit, but refinery's default squash strategy discarded it. The
// post-merge main_branch_test patrol then fired ancestor-check escalations
// every ~30 minutes.
//
// The detection and fix here preserve that topology automatically, without
// requiring branch-naming conventions, bead labels, or human intervention.
// It is also safe by construction: when the repo has no `upstream` remote
// (the common case for non-fork projects), the helper returns false and the
// existing squash-merge path runs unchanged.

package refinery

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/git"
)

// gitForkSyncOps is the subset of *git.Git used by forkSyncDecision.
// Kept small so unit tests can stub without bringing up a real repo.
type gitForkSyncOps interface {
	RefExists(ref string) (bool, error)
	IsAncestor(ancestor, descendant string) (bool, error)
}

// Compile-time check: the real git.Git satisfies the interface.
var _ gitForkSyncOps = (*git.Git)(nil)

// forkSyncDecision describes whether a merge into `target` should preserve
// merge topology rather than squash. See preserveForkSyncTopology for the
// decision rules.
type forkSyncDecision struct {
	// Preserve is true when refinery should use a no-fast-forward merge
	// (MergeNoFF) instead of a squash merge.
	Preserve bool

	// UpstreamRef is the upstream ref that `branch` has integrated but
	// `target` has not. Populated for logging; empty when Preserve is false.
	UpstreamRef string

	// Reason is a short human-readable explanation used for logs and test
	// assertions. Populated in all cases (Preserve true or false).
	Reason string
}

// upstreamRemoteName is the conventional name for the fork's upstream remote.
// `scripts/check-upstream-rebased.sh` uses the same default (via the
// UPSTREAM_REMOTE env var). Kept as a variable rather than a const so tests
// can override if needed; we intentionally do not read env vars in production
// to avoid refinery behavior drifting with shell environment.
var upstreamRemoteName = "upstream"

// preserveForkSyncTopology decides whether a merge of `branch` into `target`
// should use a no-fast-forward merge commit (preserving upstream ancestry)
// instead of the default squash merge.
//
// Returns Preserve=true when ALL of these hold:
//  1. A ref `<upstreamRemoteName>/<target>` exists locally (the repo is a
//     fork with a tracked upstream remote).
//  2. `<upstreamRemoteName>/<target>` is an ancestor of `branch` (the polecat
//     has integrated upstream on the branch, as in a fork-sync MR).
//  3. `<upstreamRemoteName>/<target>` is NOT already an ancestor of
//     `origin/<target>` (otherwise the fork is already caught up and plain
//     squash is fine — we don't need to preserve anything new).
//
// When any of these conditions is false, Preserve=false and refinery falls
// back to its normal squash-merge path. A non-nil error indicates an
// unexpected git failure (not a missing ref); the caller should treat it as
// "don't preserve" and log, rather than aborting the merge outright.
func preserveForkSyncTopology(g gitForkSyncOps, branch, target string) (forkSyncDecision, error) {
	if g == nil {
		return forkSyncDecision{Reason: "nil git ops"}, fmt.Errorf("preserveForkSyncTopology: nil git ops")
	}
	if branch == "" || target == "" {
		return forkSyncDecision{Reason: "empty branch or target"}, nil
	}

	upstreamRef := upstreamRemoteName + "/" + target

	// (1) upstream remote tracking ref must exist. Absence is the common
	// case (non-fork repos) and is NOT an error — it simply disables the
	// preservation path.
	exists, err := g.RefExists(upstreamRef)
	if err != nil {
		return forkSyncDecision{Reason: fmt.Sprintf("RefExists(%s) failed", upstreamRef)}, err
	}
	if !exists {
		return forkSyncDecision{Reason: fmt.Sprintf("no %s ref — not a fork", upstreamRef)}, nil
	}

	// (2) branch must have integrated upstream (have it as an ancestor).
	// Without this, there's nothing special about this merge.
	branchHasUpstream, err := g.IsAncestor(upstreamRef, branch)
	if err != nil {
		return forkSyncDecision{Reason: fmt.Sprintf("IsAncestor(%s, %s) failed", upstreamRef, branch)}, err
	}
	if !branchHasUpstream {
		return forkSyncDecision{Reason: fmt.Sprintf("%s not in %s history — not a fork-sync branch", upstreamRef, branch)}, nil
	}

	// (3) target must NOT already have upstream integrated. If it does,
	// fork-sync ancestry is already preserved on target and a squash of
	// any extra branch commits is semantically fine.
	targetRef := "origin/" + target
	targetHasUpstream, err := g.IsAncestor(upstreamRef, targetRef)
	if err != nil {
		return forkSyncDecision{Reason: fmt.Sprintf("IsAncestor(%s, %s) failed", upstreamRef, targetRef)}, err
	}
	if targetHasUpstream {
		return forkSyncDecision{Reason: fmt.Sprintf("%s already ancestor of %s — no preservation needed", upstreamRef, targetRef)}, nil
	}

	return forkSyncDecision{
		Preserve:    true,
		UpstreamRef: upstreamRef,
		Reason:      fmt.Sprintf("%s in %s history but not %s — preserving merge topology", upstreamRef, branch, targetRef),
	}, nil
}

// staleForkSyncDecision describes whether a fork-sync MR was generated against
// a stale snapshot of the target branch — its base is behind the current
// origin/<target> tip. Merging such a branch would silently revert every
// commit that landed on origin/<target> after the snapshot (gc-hdi17t,
// gc-ailhrp: the stale gu-yay98 fork-sync that would have reverted 5 landed
// main commits).
type staleForkSyncDecision struct {
	// Stale is true when the branch is a fork-sync branch whose base does
	// NOT contain the current origin/<target> tip — landing it would revert
	// commits that landed after the branch was generated.
	Stale bool

	// Reason is a short human-readable explanation for logs and test
	// assertions. Populated in all cases.
	Reason string
}

// detectStaleForkSync decides whether a fork-sync MR was generated against a
// stale snapshot of `target` and would therefore revert commits that have
// since landed on origin/<target>.
//
// Root cause (gc-hdi17t): a fork-sync MR snapshots origin/main at GENERATION
// time, not SUBMIT time. A burst of landings (or the push-block-outage drain)
// moves origin/main underneath the branch. When the branch's base no longer
// contains the current origin/main tip, squash-/no-ff-merging it back can
// silently revert whatever landed after the snapshot — the -860-line revert
// the refinery caught and rejected by hand in gc-ailhrp.
//
// The guard is intentionally narrow so it never blocks a healthy MR:
// it fires ONLY for fork-sync branches (branches that have integrated
// upstream/<target> via a merge commit — the same predicate
// preserveForkSyncTopology keys on). A normal feature branch is expected to
// be behind origin/main and is rebased forward by the merge itself; rejecting
// those would wedge the whole queue. A fork-sync branch, by contrast, is meant
// to ADD upstream on top of CURRENT main — if it doesn't contain current
// origin/<target>, it is stale by construction and must be regenerated.
//
// Returns Stale=true when ALL of these hold:
//  1. A ref `<upstreamRemoteName>/<target>` exists (the repo is a fork).
//  2. `<upstreamRemoteName>/<target>` is an ancestor of `branch` (this is a
//     fork-sync branch — it integrated upstream).
//  3. `origin/<target>` is NOT an ancestor of `branch` (the branch was built
//     off a stale snapshot and does not contain the current target tip).
//
// When any condition is false, Stale=false and the merge proceeds normally.
// A non-nil error indicates an unexpected git failure; callers should treat
// it as "not stale" (fail open) and log, so a transient git hiccup never
// wedges a legitimate fork-sync — the same fail-safe stance the rest of this
// file takes.
func detectStaleForkSync(g gitForkSyncOps, branch, target string) (staleForkSyncDecision, error) {
	if g == nil {
		return staleForkSyncDecision{Reason: "nil git ops"}, fmt.Errorf("detectStaleForkSync: nil git ops")
	}
	if branch == "" || target == "" {
		return staleForkSyncDecision{Reason: "empty branch or target"}, nil
	}

	upstreamRef := upstreamRemoteName + "/" + target

	// (1) Must be a fork (upstream remote tracking ref present). Absent =>
	// not a fork-sync, nothing to guard.
	exists, err := g.RefExists(upstreamRef)
	if err != nil {
		return staleForkSyncDecision{Reason: fmt.Sprintf("RefExists(%s) failed", upstreamRef)}, err
	}
	if !exists {
		return staleForkSyncDecision{Reason: fmt.Sprintf("no %s ref — not a fork", upstreamRef)}, nil
	}

	// (2) Must be a fork-sync branch (has integrated upstream). A plain
	// feature branch is out of scope — it is allowed to be behind main and
	// is fast-forwarded by the merge.
	branchHasUpstream, err := g.IsAncestor(upstreamRef, branch)
	if err != nil {
		return staleForkSyncDecision{Reason: fmt.Sprintf("IsAncestor(%s, %s) failed", upstreamRef, branch)}, err
	}
	if !branchHasUpstream {
		return staleForkSyncDecision{Reason: fmt.Sprintf("%s not in %s history — not a fork-sync branch", upstreamRef, branch)}, nil
	}

	// (3) Stale iff current origin/<target> is NOT contained in the branch.
	// A fresh fork-sync branches off the current tip, so origin/<target> is
	// an ancestor of it; a stale one branched off an older snapshot and does
	// not contain commits that landed since.
	targetRef := "origin/" + target
	branchHasTarget, err := g.IsAncestor(targetRef, branch)
	if err != nil {
		return staleForkSyncDecision{Reason: fmt.Sprintf("IsAncestor(%s, %s) failed", targetRef, branch)}, err
	}
	if branchHasTarget {
		return staleForkSyncDecision{Reason: fmt.Sprintf("%s contained in %s — fork-sync base is current", targetRef, branch)}, nil
	}

	return staleForkSyncDecision{
		Stale:  true,
		Reason: fmt.Sprintf("%s not contained in fork-sync branch %s — generated off a stale snapshot; merging would revert commits landed since", targetRef, branch),
	}, nil
}

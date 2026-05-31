package cmd

// done_phases.go: extracted sub-workflows from runDone() in done.go.
//
// runDone is a 1800+ LOC procedural function that resists isolated testing and
// forces every new exit-path concern through a single edit point. gu-y7ouk
// recommends starting with runDone and pulling out self-contained phases as
// named functions — this file is that template.
//
// Each helper here was lifted as-is from runDone with no behavior change.
// They intentionally take the same primitives runDone has on hand (a
// *git.Git, a few strings) so the call sites in runDone stay one-liners.
// Refactor in surgical steps; do not redesign signatures while extracting.

import (
	"fmt"
	"os"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
)

// detectCleanupStatus auto-detects the polecat's git cleanup status (gu-y7ouk
// extraction from runDone). Returns one of: "uncommitted", "stash", "unpushed",
// "clean", or "unknown".
//
// The polecat session manager passes this status to the witness so it knows
// whether the worktree can be safely nuked. Detection runs only when the user
// did not supply --cleanup-status on the command line. When the working
// directory was deleted out from under the polecat (e.g. witness force-nuke
// during a SIGKILL race), there is no git state to inspect and we default to
// "unknown" with a warning.
//
// Mirrors the lines previously inlined at done.go:367–400.
func detectCleanupStatus(g *git.Git, branch string, cwdAvailable bool) string {
	if !cwdAvailable {
		// Can't detect git state without working directory, default to unknown.
		style.PrintWarning("cannot detect cleanup status - working directory deleted")
		return "unknown"
	}
	workStatus, err := g.CheckUncommittedWork()
	if err != nil {
		style.PrintWarning("could not auto-detect cleanup status: %v", err)
		return ""
	}
	switch {
	case workStatus.HasUncommittedChanges:
		return "uncommitted"
	case workStatus.StashCount > 0:
		return "stash"
	default:
		// CheckUncommittedWork.UnpushedCommits doesn't work for branches
		// without upstream tracking (common for polecats). Use the more
		// robust BranchPushedToRemote which compares against origin/main.
		pushed, unpushedCount, err := g.BranchPushedToRemote(branch, "origin")
		if err != nil {
			style.PrintWarning("could not check if branch is pushed: %v", err)
			return "unpushed" // err on side of caution
		}
		if !pushed || unpushedCount > 0 {
			return "unpushed"
		}
		return "clean"
	}
}

// runStashAutoPop is the gt-pvx stash safety net (gu-y7ouk extraction).
//
// Background: agents have been observed running `git stash` to clear the
// working tree before rebase/checkout, then dying before `git stash pop`.
// The stash entries become orphaned in .git/refs/stash, surviving for
// indefinite periods and silently leaking work. By popping them on the way
// out of `gt done`, the recovery flow turns "lost" stashes into a committed
// safety-net snapshot via the auto-commit path that follows it.
//
// Pop happens oldest-first so the most recent state ends up on top of the
// working tree (matches what a user would do manually). If any pop has
// conflicts, we stop and let the agent/user resolve — surfacing the
// conflict is better than silently dropping the stash.
//
// Returns the new cleanup status: "uncommitted" if pops produced dirty
// content, "" if pops succeeded with nothing to commit (caller will
// recompute), "stash" if a stale-stash guard fired and the stash remains
// in place for manual handling.
//
// Mirrors the lines previously inlined at done.go:426–506.
func runStashAutoPop(g *git.Git, status string) string {
	if status != "stash" {
		return status
	}
	entries, err := g.StashListForBranch()
	if err != nil {
		style.PrintWarning("auto-pop: could not list stashes: %v — orphaned stashes may remain", err)
		return status
	}
	if len(entries) == 0 {
		return status
	}

	fmt.Printf("\n%s %d stash(es) detected on this branch — auto-popping (gt-pvx safety net)\n",
		style.Bold.Render("⚠"), len(entries))

	popFailed := false
	staleSkipped := 0
	// Pop oldest first: iterate in reverse so newest lands on top.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]

		// STALENESS GUARD (gu-vtkn): Refuse to auto-pop a stash whose
		// parent commit (HEAD at stash-time) differs from current HEAD.
		// When HEAD has advanced past the stash's base, pops can
		// introduce phantom deletions of files that intervening commits
		// added — silently reverting committed work. The rust→nitro
		// near-miss: rust stashed WIP, committed testenv_test.go files,
		// died; the stash's diff (measured against pre-commit HEAD)
		// would have deleted those files when popped against the
		// post-commit HEAD. Refusing the pop and surfacing the stash
		// for manual review is strictly safer than auto-popping and
		// relying on the StagedDeletions unstage + detached-HEAD guard
		// as the last line of defense.
		stale, stashParent, headSHA, staleErr := g.IsStashStale(e.Ref)
		if staleErr != nil {
			style.PrintWarning("auto-pop %s skipped: staleness check failed: %v", e.Ref, staleErr)
			style.PrintWarning("  Manual handling required — inspect with: git stash show -p %s", e.Ref)
			staleSkipped++
			popFailed = true
			break
		}
		if stale {
			style.PrintWarning("auto-pop %s skipped: stale stash (gu-vtkn guard)", e.Ref)
			fmt.Fprintf(os.Stderr, "  Stash parent: %s\n", shortSHA(stashParent))
			fmt.Fprintf(os.Stderr, "  Current HEAD: %s\n", shortSHA(headSHA))
			fmt.Fprintf(os.Stderr, "  HEAD has moved since this stash was created. Popping could\n")
			fmt.Fprintf(os.Stderr, "  introduce phantom deletions of files added by intervening commits.\n")
			fmt.Fprintf(os.Stderr, "  Inspect manually: git stash show -p %s\n", e.Ref)
			fmt.Fprintf(os.Stderr, "  Then either: git stash drop %s  (discard)\n", e.Ref)
			fmt.Fprintf(os.Stderr, "           or: git stash pop %s   (apply, accepting risk)\n\n", e.Ref)
			staleSkipped++
			popFailed = true
			break
		}

		fmt.Printf("  popping %s — %s\n", e.Ref, e.Message)
		if popErr := g.StashPop(e.Ref); popErr != nil {
			style.PrintWarning("auto-pop %s failed (likely conflict): %v", e.Ref, popErr)
			style.PrintWarning("stopping pop chain — resolve conflict manually then re-run gt done")
			popFailed = true
			break
		}
		// After each pop, stash refs shift; re-fetch the list before next pop.
		entries, err = g.StashListForBranch()
		if err != nil || len(entries) == 0 {
			break
		}
	}

	if !popFailed {
		// Re-evaluate cleanup status: pops likely produced uncommitted changes
		// that the next block will auto-commit. Worst case, status was already
		// uncommitted and the next block runs anyway.
		if workStatus, wsErr := g.CheckUncommittedWork(); wsErr == nil && workStatus.HasUncommittedChanges {
			fmt.Printf("%s Stash content moved to working tree — will auto-commit below.\n",
				style.Bold.Render("✓"))
			return "uncommitted"
		}
		// Pops succeeded but produced nothing dirty (e.g. stashes were
		// already merged). Recompute status normally.
		return ""
	}
	if staleSkipped > 0 {
		// Preserve "stash" status so downstream cleanup-wisp accounting
		// reflects that stashes remain on the branch. The stale stash
		// stays in place for manual handling; do not let the auto-commit
		// block below fire against a working tree we don't own.
		return "stash"
	}
	return status
}

package completion

import (
	"fmt"
)

// RebaseGit is the subset of *git.Git that AutoRebaseOnTarget needs. Defined as
// an interface so tests can drive the decision logic without standing up a full
// git repo for every gating case.
type RebaseGit interface {
	Rebase(onto string) error
	AbortRebase() error
}

// AutoRebaseOnTarget rebases the current branch onto base when the branch is
// behind the target. It is a no-op when there is nothing to rebase or when a
// prior push checkpoint exists (rebasing after pushing would require a
// force-push).
//
// Note on --pre-verified (gs-4bn): a previous version skipped the rebase when
// preVerified was set so the polecat's gate-results attestation stayed valid.
// That left polecats pushing branches with stale workflow state (e.g., an old
// ci.yml from before a CI fix landed on origin), guaranteeing CI failures on
// PRs. The trade-off has flipped: we always rebase, and the caller is expected
// to drop the pre-verified attestation from the MR bead when rebased &&
// preVerified, so refinery re-runs gates instead of fast-pathing on a stale
// claim.
//
// Returns:
//   - rebased: true if a rebase actually ran successfully.
//   - skipReason: non-empty when behind > 0 but the rebase was intentionally
//     skipped. Empty when behind == 0 (no rebase needed) or when rebased == true.
//   - err: rebase failure, after AbortRebase has been attempted to clean up.
//
// gh#3400, gs-4bn.
func AutoRebaseOnTarget(g RebaseGit, base string, behind int, preVerified, alreadyPushed bool) (rebased bool, skipReason string, err error) {
	if behind <= 0 {
		return false, "", nil
	}
	if alreadyPushed {
		return false, "prior push checkpoint exists", nil
	}

	fmt.Printf("→ Auto-rebasing onto %s (%d commits behind)\n", base, behind)
	if preVerified {
		fmt.Printf("  Note: --pre-verified was set; rebasing anyway to avoid pushing stale base. Pre-verification metadata will be dropped (gs-4bn).\n")
	}
	if rebaseErr := g.Rebase(base); rebaseErr != nil {
		_ = g.AbortRebase()
		return false, "", fmt.Errorf("auto-rebase onto %s failed: %w\n"+
			"Resolve conflicts manually (git fetch origin && git rebase %s), commit the resolution, then rerun gt done.",
			base, rebaseErr, base)
	}
	return true, "", nil
}

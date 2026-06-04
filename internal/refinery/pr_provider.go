package refinery

// PRProvider abstracts VCS-specific PR operations for the merge queue.
// Implementations exist for GitHub (default) and Bitbucket Cloud.
type PRProvider interface {
	// FindPRNumber returns the PR number/ID for the given branch, or 0 if none exists.
	FindPRNumber(branch string) (int, error)

	// IsPRApproved checks whether a PR has at least one approving review.
	IsPRApproved(prNumber int) (bool, error)

	// MergePR merges a PR using the specified method (e.g., "squash", "merge", "rebase").
	// Returns the merge commit SHA on success (if available).
	MergePR(prNumber int, method string) (string, error)
}

// mergedPRFinder is an optional capability for PRProviders that can detect a
// branch's already-MERGED PR. The refinery uses it to make the post-merge close
// idempotent: when no OPEN PR exists for a branch, an already-merged PR proves
// the work landed, so the source bead + MR wisp can still be closed instead of
// being left as a 'ready + GIT MISSING' orphan that gets re-dispatched (gs-4uz).
// Providers that don't implement it keep the prior behavior (no detection).
type mergedPRFinder interface {
	// FindMergedPRCommit returns the merge commit SHA of a MERGED PR for the
	// branch, or "" if none. Only meaningful when no OPEN PR exists.
	FindMergedPRCommit(branch string) (string, error)
}

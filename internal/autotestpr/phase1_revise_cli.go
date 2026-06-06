// Phase 1 task 18 (gu-q8l6c): MR-banner helper for the manual revision
// pathway. Generates the {{phase1_revise_cli}} placeholder value that
// the polecat substitutes into the MR banner template at gt-done time.
//
// During Phase 1, the automated feedback-patrol (Phase 2) is not live,
// so the MR banner documents the manual fallback CLI for reviewers.
// When Phase 2 is active (revision_routing feature flag is on), this
// helper returns an empty string and the template omits the line.
//
// Design context: .designs/auto-test-pr/synthesis.md §D17 (Phase-1
// manual revision CLI fallback) and §MR-banner template placeholder.
package autotestpr

import "fmt"

// Phase1ReviseCLI generates the manual revision CLI string for
// inclusion in the MR banner. The returned string is the exact command
// a maintainer would paste to trigger a revision polecat for this MR.
//
// When revisionRoutingEnabled is true (Phase 2 is live), returns ""
// so the template conditional omits the fallback line entirely.
//
// Example output (Phase 1):
//
//	gt auto-test-pr revise --mr=gt-mr-abc12
func Phase1ReviseCLI(mrBeadID string, revisionRoutingEnabled bool) string {
	if revisionRoutingEnabled {
		return ""
	}
	if mrBeadID == "" {
		return ""
	}
	return fmt.Sprintf("gt auto-test-pr revise --mr=%s", mrBeadID)
}

// Phase1ReviseCLIWithComment generates the full revision CLI including
// a specific comment thread ID. Used in the MR banner when the
// template wants to show the extended form.
//
// Example output:
//
//	gt auto-test-pr revise --mr=gt-mr-abc12 --comment-id=cmt-42
func Phase1ReviseCLIWithComment(mrBeadID, commentID string, revisionRoutingEnabled bool) string {
	if revisionRoutingEnabled {
		return ""
	}
	if mrBeadID == "" {
		return ""
	}
	if commentID == "" {
		return Phase1ReviseCLI(mrBeadID, revisionRoutingEnabled)
	}
	return fmt.Sprintf("gt auto-test-pr revise --mr=%s --comment-id=%s", mrBeadID, commentID)
}

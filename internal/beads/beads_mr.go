// Package beads provides merge request and gate utilities.
package beads

import (
	"strings"
)

// FindMRForBranch searches for an open merge-request bead for the given branch.
// Returns the MR bead if found, nil if not found.
// This enables idempotent `gt done` - if an MR already exists, we skip creation.
func (b *Beads) FindMRForBranch(branch string) (*Issue, error) {
	return b.findMRForBranch(branch, true)
}

// FindMRForBranchAny searches for a merge-request bead for the given branch
// across all statuses (open and closed). Used by recovery checks to determine
// if work was ever submitted to the merge queue. See #1035.
func (b *Beads) FindMRForBranchAny(branch string) (*Issue, error) {
	return b.findMRForBranch(branch, false)
}

// FindMRForBranchAndSHA searches for an open merge-request bead matching both
// the branch name AND the commit SHA. This is the correct dedup key: two MRs
// from the same branch but with different commit SHAs are distinct submissions
// (e.g., polecat fixed a gate failure and re-pushed). See GH#3032.
//
// Returns nil if no MR matches both branch and SHA. Callers should create a
// new MR in that case and supersede old MRs for the same source issue.
func (b *Beads) FindMRForBranchAndSHA(branch, commitSHA string) (*Issue, error) {
	issues, err := b.ListMergeRequests(ListOptions{
		Status: "all",
		Label:  "gt:merge-request",
	})
	if err != nil {
		return nil, err
	}

	branchPrefix := "branch: " + branch + "\n"
	for _, issue := range issues {
		if issue.Status == "closed" {
			continue
		}
		if !strings.HasPrefix(issue.Description, branchPrefix) {
			continue
		}
		// Branch matches — check commit SHA.
		// If the MR has no commit_sha field (legacy), fall back to branch-only
		// match for backward compatibility.
		fields := ParseMRFields(issue)
		if fields != nil && fields.CommitSHA != "" && commitSHA != "" {
			if fields.CommitSHA != commitSHA {
				// Same branch but different SHA — this is a stale MR.
				// Don't return it; caller will create a new MR and supersede.
				continue
			}
		}
		return issue, nil
	}

	return nil, nil
}

// findMRForBranch searches the wisps table (Dolt) for a merge-request
// bead matching the given branch.
// Uses status=all which includes all issue statuses with full descriptions.
// Ephemeral=true routes to the wisps table where MR beads live (GH#2446).
// When skipClosed is true, closed beads are excluded (for open-MR checks).
func (b *Beads) findMRForBranch(branch string, skipClosed bool) (*Issue, error) {
	branchPrefix := "branch: " + branch + "\n"

	issues, err := b.ListMergeRequests(ListOptions{
		Status: "all",
		Label:  "gt:merge-request",
	})
	if err != nil {
		return nil, err
	}
	for _, issue := range issues {
		if skipClosed && issue.Status == "closed" {
			continue
		}
		if strings.HasPrefix(issue.Description, branchPrefix) {
			return issue, nil
		}
	}

	return nil, nil
}

// FindOpenMRsForIssue returns all open merge-request beads whose source_issue
// matches the given issue ID. Used to find prior attempts when re-dispatching
// an issue and to supersede old MRs when a new one is created.
func (b *Beads) FindOpenMRsForIssue(issueID string) ([]*Issue, error) {
	issues, err := b.ListMergeRequests(ListOptions{
		Status: "open",
		Label:  "gt:merge-request",
	})
	if err != nil {
		return nil, err
	}

	var matches []*Issue
	for _, issue := range issues {
		if MatchesMRSourceIssue(issue.Description, issueID) {
			matches = append(matches, issue)
		}
	}
	return matches, nil
}

// MatchesMRSourceIssue returns true if the MR description contains a
// source_issue field matching the given issue ID exactly. The trailing
// newline in the needle prevents partial ID matches (e.g., "gt-abc"
// must not match "gt-abcdef").
func MatchesMRSourceIssue(description, issueID string) bool {
	needle := "source_issue: " + issueID + "\n"
	return strings.Contains(description, needle)
}

// RepointSupersededMRAgent re-points the agent bead that owns oldMR so its
// active_mr references newMRID — the MR that supersedes oldMR.
//
// When a polecat's MR is superseded (a re-submission for the same source issue
// closes the prior MR), the superseded MR's owning agent bead is left with
// active_mr pointing at the now-CLOSED MR. The polecat that created it has
// usually exited, so that agent bead is dead. The post-merge orphan reconcile
// (gu-7igu8) keys off active_mr being proven-MERGED to complete the close; a
// superseded MR never merges, so the reconcile never fires for that agent bead
// and `gt polecat nuke` refuses (active_mr=closed superseded MR but the source
// bead is still HOOKED), forcing manual recovery on every multi-MR swarm
// (gs-stvm). Re-pointing active_mr at the live superseding MR lets the reconcile
// and nuke follow the MR that actually merges.
//
// No-op when oldMR is nil or carries no agent_bead field (nothing to re-point).
// Agent beads live in the town/HQ database, so the update routes through
// ForAgentBead regardless of which rig the MR bead lives in.
func (b *Beads) RepointSupersededMRAgent(oldMR *Issue, newMRID string) error {
	if oldMR == nil {
		return nil
	}
	fields := ParseMRFields(oldMR)
	if fields == nil || fields.AgentBead == "" {
		return nil
	}
	return b.ForAgentBead().UpdateAgentActiveMR(fields.AgentBead, newMRID)
}

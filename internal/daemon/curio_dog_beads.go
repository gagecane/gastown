package daemon

import (
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/curio"
)

// collectMergedBeadObservations gathers the closed-"merged" bead observations
// that feed Curio's bead_merged_not_landed rule (a). It is the live bead-Dolt
// source; curio.ResolveMergedBeads resolves each observation's git ancestry and
// dedups across sources.
//
// This lives in the daemon (not internal/curio) because it needs bead Dolt
// access, which curio deliberately does not import — keeping the rule's
// failure-mode logic (dual-source dedup, ancestry) unit-testable without a
// live database.
//
// Source shape: merge-request beads carry close_reason + commit_sha + rig as
// "key: value" lines (parsed by beads.ParseMRFields). A merged MR records the
// commit that was supposed to land; the rule flags it if that commit is not in
// the owning rig's main ancestry. A merged MR with no recorded commit is itself
// suspicious (the rule treats an empty commit as not-landed).
func (d *Daemon) collectMergedBeadObservations() []curio.MergedBeadObservation {
	b := beads.New(d.config.TownRoot)
	issues, err := b.ListMergeRequests(beads.ListOptions{
		Status:   "closed",
		Label:    "gt:merge-request",
		Priority: -1,
	})
	if err != nil {
		d.logger.Printf("curio: listing merged beads failed (rule a sees none): %v", err)
		return nil
	}

	var out []curio.MergedBeadObservation
	for _, issue := range issues {
		fields := beads.ParseMRFields(issue)
		if fields == nil || fields.CloseReason != "merged" {
			continue
		}
		// Prefer the merge commit (the SHA that actually landed) when set,
		// falling back to the submission commit_sha.
		commit := fields.MergeCommit
		if commit == "" {
			commit = fields.CommitSHA
		}
		out = append(out, curio.MergedBeadObservation{
			ID:     issue.ID,
			Rig:    fields.Rig,
			Commit: commit,
		})
	}
	return out
}

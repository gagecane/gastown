package daemon

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/curio"
)

// curioBeadLabel marks beads Curio files for tuned-rule candidates (B0a). It
// scopes the air-gap query (collectCurioFiledBeads) and the file-once dedup,
// and pairs with the curio.CurioActor BD_ACTOR stamp so a Curio-filed bead is
// identifiable by both label and provenance.
const curioBeadLabel = "curio-finding"

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

// collectCurioFiledBeads returns the set of bead IDs Curio itself has filed,
// for the Call 1(A) air-gap (curio.Input.CurioBeads). A record whose causal
// chain ROOTS at one of these beads is a reaction to Curio's own activity and
// is suppressed by the loop-breaker.
//
// When the B0a filing gate is OFF, Curio files ZERO beads, so this stays empty
// — the air-gap's causal half is dormant, exactly as before. When filing is ON
// it returns the open Curio-filed beads (label curioBeadLabel), so the moment a
// filing provokes downstream churn the loop-breaker can suppress it. The
// causal-provenance fields the suppressor reads are still plumbing-only (no emit
// site populates CausalRoot yet — gu-5ynaa), so this has no live suppression
// effect today; wiring it now keeps the air-gap honest the instant provenance
// lands.
func (d *Daemon) collectCurioFiledBeads() map[string]bool {
	if !d.curioFileTunedRules() {
		return nil
	}

	b := beads.New(d.config.TownRoot)
	issues, err := b.List(beads.ListOptions{
		Status:   "open",
		Label:    curioBeadLabel,
		Priority: -1,
	})
	if err != nil {
		d.logger.Printf("curio: listing curio-filed beads for air-gap failed (treating as none): %v", err)
		return nil
	}
	if len(issues) == 0 {
		return nil
	}
	out := make(map[string]bool, len(issues))
	for _, issue := range issues {
		out[issue.ID] = true
	}
	return out
}

// fileCurioBead files a town-level (HQ) bead for a tuned-rule candidate and
// returns its ID. The bead is stamped with the curioBeadLabel + the rule and
// fingerprint labels (for air-gap scoping and file-once dedup) and the
// curio.CurioActor provenance so the loop-breaker recognizes it as Curio's own.
//
// alarm_rate_spike candidates carry no rig (the rate series is town-global), so
// the bead lands in HQ rather than a rig tree. The B0a ledger row is written by
// the caller (fileTunedCandidates) AFTER this returns the bead ID.
func (d *Daemon) fileCurioBead(c curio.Candidate) (string, error) {
	title := fmt.Sprintf("curio: %s — %s", c.RuleID, c.Series)
	if c.Summary != "" {
		title = fmt.Sprintf("curio: %s", c.Summary)
	}

	b := beads.New(d.config.TownRoot)
	issue, err := b.Create(beads.CreateOptions{
		Title:       title,
		Description: formatCurioBeadBody(c),
		Priority:    3,
		Labels: []string{
			"gt:task",
			curioBeadLabel,
			"rule:" + c.RuleID,
			"fingerprint:" + c.Fingerprint,
		},
		Actor: curio.CurioActor,
	})
	if err != nil {
		return "", fmt.Errorf("creating curio bead: %w", err)
	}
	if issue == nil || issue.ID == "" {
		return "", fmt.Errorf("curio bead created but no ID returned")
	}
	return issue.ID, nil
}

// formatCurioBeadBody renders the human-readable body for a Curio-filed bead.
func formatCurioBeadBody(c curio.Candidate) string {
	return fmt.Sprintf(`Auto-filed by Curio (rule %s).

%s

rule_id: %s
series: %s
observed: %d
fingerprint: %s
window: %s

Filed by the Curio self-inspection lane (B0a). Precision is tracked in the
curio_ledger; closing this bead with an accurate reason feeds the rule's
precision measurement.`,
		c.RuleID, c.Summary, c.RuleID, c.Series, c.Observed, c.Fingerprint, c.WindowID)
}

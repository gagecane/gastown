// Dangling foreign-key scrubber for the reaper package.
//
// Background (gu-96uxo): the wisp/mol reaper compacts ephemeral beads (wisps)
// when their TTL expires, but does not scrub the foreign-key references that
// non-ephemeral agent beads hold against those wisps. When a referent wisp is
// TTL-reaped (or later purged), fields like `mr_id` and `hook_bead` are left
// pointing at an ID that no longer resolves. Those dangling pointers block
// downstream automation (refinery dispatch reads them as "still working") and
// only get noticed when the consumer escalates after N empty cycles.
//
// gu-dhqm already covers the `active_mr` field via ScrubStaleActiveMR, which
// runs the polecat.AssessActiveMR classifier (MR + source both terminal) over
// every agent bead each reaper cycle. This scrubber is the complementary,
// strictly-narrower pass for the OTHER enumerated FK fields — `mr_id` and
// `hook_bead` — that no existing code path clears.
//
// Why existence-only (not the assessment used for active_mr): a present
// referent — regardless of its status — may still be meaningful to a live
// agent, so we never touch it. We clear a field only when its referent is
// MISSING (the bd lookup returns ErrNotFound), which is the unambiguous
// signature of a reaped/purged wisp. A missing referent is dangling by
// definition; nulling it cannot yank a reference out from under a live agent.
// Lookup errors other than not-found are treated as uncertainty and skipped
// (fail-closed), so a flaky Dolt connection never produces spurious clears.
//
// Preservation invariant (gc-eysed): polecats whose self-reported
// cleanup_status indicates uncommitted work, a stash, or unpushed commits keep
// ALL their FK references untouched — the dangling refs are the audit trail the
// human triage path needs. The scrubber leaves them entirely alone, reusing the
// same isPolecatPreservingHumanWIP predicate as the active_mr scrubber.

package reaper

import (
	"errors"
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// ScrubDanglingFKResult summarizes a single ScrubDanglingFKRefs run.
type ScrubDanglingFKResult struct {
	// Scanned is the number of agent beads inspected.
	Scanned int `json:"scanned"`
	// HadMRID is the number of agent beads with a non-empty mr_id field.
	HadMRID int `json:"had_mr_id"`
	// HadHookBead is the number of agent beads with a non-empty hook_bead field.
	HadHookBead int `json:"had_hook_bead"`
	// ClearedMRID is the number of mr_id fields cleared this run.
	ClearedMRID int `json:"cleared_mr_id"`
	// ClearedHookBead is the number of hook_bead fields cleared this run.
	ClearedHookBead int `json:"cleared_hook_bead"`
	// PreservedWIP is the number of agent beads skipped entirely because their
	// cleanup_status indicates human-WIP that must be preserved (gc-eysed).
	PreservedWIP int `json:"preserved_wip"`
	// DryRun is true when the scrubber reported what it would clear without
	// actually clearing anything.
	DryRun bool `json:"dry_run,omitempty"`
	// ClearedEntries records each FK field that was (or would be) cleared.
	ClearedEntries []ScrubDanglingFKEntry `json:"cleared_entries,omitempty"`
	// Anomalies records any unexpected per-bead failures (best-effort; the
	// scrubber tolerates them and continues).
	Anomalies []Anomaly `json:"anomalies,omitempty"`
}

// ScrubDanglingFKEntry records a single cleared (or would-be-cleared) FK field.
type ScrubDanglingFKEntry struct {
	AgentBeadID string `json:"agent_bead_id"`
	Field       string `json:"field"`    // "mr_id" or "hook_bead"
	Referent    string `json:"referent"` // the dangling ID that was nulled
}

// DanglingFKScrubBeads is the minimal interface ScrubDanglingFKRefs needs from
// a beads client. *beads.Beads satisfies it directly.
type DanglingFKScrubBeads interface {
	polecat.IssueReader
	ListAgentBeads() (map[string]*beads.Issue, error)
	UpdateAgentDescriptionFields(id string, updates beads.AgentFieldUpdates) error
}

// ScrubDanglingFKRefs scans every agent bead and clears `mr_id` and `hook_bead`
// fields whose referent bead no longer exists (the lookup returns ErrNotFound,
// the signature of a TTL-reaped or purged wisp). A present referent — at any
// status — is left untouched. Polecats with cleanup_status indicating human WIP
// (has_uncommitted / has_stash / has_unpushed) are preserved entirely (gc-eysed).
//
// This complements ScrubStaleActiveMR (gu-dhqm), which already covers the
// `active_mr` field via the AssessActiveMR classifier. ScrubDanglingFKRefs
// deliberately does NOT touch active_mr to avoid double-handling.
//
// Caller is responsible for passing a Beads client rooted at the town
// (typically beads.New(townRoot).ForAgentBead()) so lookups and updates target
// the town database where agent beads live.
//
// The function tolerates per-bead failures: a single failed update is recorded
// as an anomaly and the scan continues. It returns a non-nil error only when
// ListAgentBeads itself fails.
func ScrubDanglingFKRefs(bd DanglingFKScrubBeads, dryRun bool) (*ScrubDanglingFKResult, error) {
	if bd == nil {
		return nil, fmt.Errorf("scrub dangling fk: nil beads client")
	}

	agents, err := bd.ListAgentBeads()
	if err != nil {
		return nil, fmt.Errorf("scrub dangling fk: list agent beads: %w", err)
	}

	result := &ScrubDanglingFKResult{DryRun: dryRun}
	for id, issue := range agents {
		if issue == nil {
			continue
		}
		result.Scanned++

		fields := beads.ParseAgentFields(issue.Description)
		if fields == nil {
			continue
		}

		if fields.MRID != "" {
			result.HadMRID++
		}
		if fields.HookBead != "" {
			result.HadHookBead++
		}

		// gc-eysed: never disturb a polecat preserving human WIP. Its dangling
		// refs are the audit trail the triage path relies on.
		if isPolecatPreservingHumanWIP(fields) {
			result.PreservedWIP++
			continue
		}

		clearMRID := fields.MRID != "" && referentMissing(bd, fields.MRID)
		clearHook := fields.HookBead != "" && referentMissing(bd, fields.HookBead)
		if !clearMRID && !clearHook {
			continue
		}

		if dryRun {
			recordFKClears(result, id, fields, clearMRID, clearHook)
			continue
		}

		updates := beads.AgentFieldUpdates{}
		empty := ""
		if clearMRID {
			updates.MRID = &empty
		}
		if clearHook {
			updates.HookBead = &empty
		}
		if err := bd.UpdateAgentDescriptionFields(id, updates); err != nil {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type: "dangling_fk_clear_failed",
				Message: fmt.Sprintf("agent_bead=%s mr_id=%q hook_bead=%q: %v",
					id, fields.MRID, fields.HookBead, err),
			})
			continue
		}
		recordFKClears(result, id, fields, clearMRID, clearHook)
	}

	return result, nil
}

// recordFKClears appends the per-field cleared entries and bumps the counters
// for whichever fields were cleared on this agent bead.
func recordFKClears(result *ScrubDanglingFKResult, id string, fields *beads.AgentFields, clearMRID, clearHook bool) {
	if clearMRID {
		result.ClearedMRID++
		result.ClearedEntries = append(result.ClearedEntries, ScrubDanglingFKEntry{
			AgentBeadID: id,
			Field:       "mr_id",
			Referent:    fields.MRID,
		})
	}
	if clearHook {
		result.ClearedHookBead++
		result.ClearedEntries = append(result.ClearedEntries, ScrubDanglingFKEntry{
			AgentBeadID: id,
			Field:       "hook_bead",
			Referent:    fields.HookBead,
		})
	}
}

// referentMissing reports whether the referenced bead no longer exists — the
// signature of a TTL-reaped or purged wisp. It is fail-closed: any lookup error
// other than ErrNotFound (e.g. a transient Dolt failure) returns false so the
// scrubber never clears a field on uncertainty.
func referentMissing(reader polecat.IssueReader, id string) bool {
	issue, err := reader.Show(id)
	if err != nil {
		return errors.Is(err, beads.ErrNotFound)
	}
	return issue == nil
}

// Package dispatch holds the pure decision logic that gates whether a bead may
// be dispatched (slung) to a polecat. It is carved out of internal/cmd so the
// eligibility rules can be unit-tested without the full CLI harness (no
// cmd.runSling, no workspace/beads/session globals).
//
// Everything here is a pure function over a BeadInfo value (plus, for the one
// side-effecting check, an injected predicate). CLI argument parsing, output
// formatting, bd subprocess I/O, and side-effect orchestration stay in
// internal/cmd.
package dispatch

import (
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// Reference / tripwire markers. Duplicated as package-local constants so this
// package does not depend on internal/cmd. The cmd package keeps its own copies
// (capacity_dispatch.go) for the non-dispatch paths that still live there.
const (
	issueTypeReference = "reference"
	labelDoNotDispatch = "do-not-dispatch"
	labelPinned        = "pinned"
	// labelAwaitingRefineryMerge marks a source bead whose polecat already
	// submitted an MR that the refinery has not yet merged to origin/main.
	// Mirrors completion.awaitingRefineryMergeLabel (kept local so dispatch
	// does not depend on internal/polecat/completion).
	labelAwaitingRefineryMerge = "awaiting_refinery_merge"
)

// BeadInfo holds the status, assignee, and metadata for a bead — the compact
// shape sling's `bd show --json` wrapper parses. It is the input to every
// dispatch-eligibility predicate below.
//
// Field names and json tags match the original cmd.beadInfo exactly; cmd keeps
// a `type beadInfo = dispatch.BeadInfo` alias so existing call sites and the
// bd-show parser are untouched.
type BeadInfo struct {
	ID           string           `json:"id"`
	Title        string           `json:"title"`
	Status       string           `json:"status"`
	Assignee     string           `json:"assignee"`
	Owner        string           `json:"owner,omitempty"`
	Description  string           `json:"description"`
	Labels       []string         `json:"labels,omitempty"`
	Dependencies []beads.IssueDep `json:"dependencies,omitempty"`
	IssueType    string           `json:"issue_type,omitempty"`
}

// hasLabel reports whether target is present in labels.
func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// IsDeferredBead checks whether a bead should be rejected from slinging because
// it has been deferred. Returns true if the bead has status "deferred" or if its
// description contains deferral keywords like "deferred to post-launch".
func IsDeferredBead(info *BeadInfo) bool {
	if info.Status == "deferred" {
		return true
	}
	desc := strings.ToLower(info.Description)
	if strings.Contains(desc, "deferred to post-launch") ||
		strings.Contains(desc, "deferred to post launch") ||
		strings.Contains(desc, "status: deferred") {
		return true
	}
	return false
}

// IsAgentBead reports whether the bead info describes an agent bead
// (polecat/witness/refinery/mayor/dog state bead rather than a work item).
//
// Agent beads track per-agent state — role_type, rig, agent_state, hook_bead,
// cleanup_status — and must never be dispatched as work. Dispatching one causes
// a polecat to accept the agent bead as its hook and submit whatever auto-save
// branch the prior polecat left behind, which can revert merged commits (gu-7gm).
//
// Identification mirrors beads.IsAgentBead: the "gt:agent" label (current
// standard) or the legacy issue_type == "agent" (pre-migration beads).
//
// Note: this is the narrow label/type check. For dispatch gating prefer
// IsIdentityBeadInfo, which also rejects closed beads and identity-named titles
// (see gu-3znx).
func IsAgentBead(info *BeadInfo) bool {
	if info == nil {
		return false
	}
	if info.IssueType == "agent" {
		return true
	}
	for _, l := range info.Labels {
		if l == "gt:agent" {
			return true
		}
	}
	return false
}

// IsIdentityBeadInfo is the BeadInfo-backed dispatch filter. It mirrors
// beads.IsIdentityBead for the compact status/title/labels shape used by
// sling's bd-show wrapper. Returns true when the bead is an agent identity
// bead (label gt:agent OR legacy issue_type=agent OR a role_type field in the
// description — gs-fwu), a rig identity bead (label gt:rig OR issue_type=rig —
// gs-2j6), a role definition bead (label gt:role OR issue_type=role), is
// closed, or has a title matching the identity naming convention
// (^<prefix>-.+-(polecat-.+|refinery)$).
//
// Every dispatch path (runSling, executeSling, scheduleBead) consults this
// helper to guarantee identity beads never hook a polecat. This closes the
// sling-side gap left by fa341247, which only hardened convoy feeding and the
// stranded scan (gu-3znx).
func IsIdentityBeadInfo(info *BeadInfo) bool {
	if info == nil {
		return false
	}
	if IsAgentBead(info) {
		return true
	}
	// role_type in the description is the authoritative agent-bead marker —
	// reliably set even when the title is prose and the gt:agent label is
	// missing. gs-fwu: the gs-gastown-refinery identity bead (role_type:
	// refinery, prose title, no gt:agent label, issue_type=task, OPEN) slipped
	// past every label/title/type/status filter and was dispatched as work
	// after a daemon restart. Gating on role_type hard-excludes it independent
	// of status/owner/hook so a restart can never make it dispatchable.
	if beads.HasAgentRoleType(info.Description) {
		return true
	}
	if info.IssueType == "rig" || info.IssueType == "role" {
		return true
	}
	if beads.IsIdentityBeadFieldsLabels(info.Labels) {
		return true
	}
	if info.Status == "closed" {
		return true
	}
	return beads.IsIdentityBeadTitle(info.Title)
}

// IsEpicLikeBeadInfo reports whether the bead's title marks it as an epic
// ("EPIC: ...") while its issue_type is still slingable (e.g., task).
//
// This is the sling-side dispatch gate for gu-smr1: the auto-dispatch plugin
// only filters by issue_type, so a task bead with an EPIC: title prefix
// escapes the container filter and gets slung to a polecat. The polecat
// spawns, sees an epic, and wastes a slot. Legitimate epics (type=epic) are
// rejected earlier by detectSchedulerIDType; this helper covers the data-
// hygiene gap between title and type.
//
// gu-fs88 extension: also treats the phase:epic label as an epic marker.
// ta-823 carried that label and was hooked to polecats even though the
// title started with "EPIC:" — the label is a belt-and-suspenders signal
// that stays attached across title edits and catches phase-style epics
// that didn't adopt the "EPIC:" title convention.
func IsEpicLikeBeadInfo(info *BeadInfo) bool {
	if info == nil {
		return false
	}
	// Real epics are already routed through the epic path — this gate only
	// fires when the type disagrees with the title.
	if info.IssueType == "epic" {
		return false
	}
	if beads.IsEpicLikeTitle(info.Title) {
		return true
	}
	return beads.HasEpicPhaseLabel(info.Labels)
}

// IsContainerBeadInfo reports whether the bead is a real epic or convoy
// container — a bead whose issue_type or gt:epic/gt:convoy label marks it as a
// parent that tracks work via its children, not a dispatchable work item.
//
// This is the type/label-level container check that IsEpicLikeBeadInfo
// deliberately does NOT cover: that helper only fires on the data-hygiene gap
// where a non-epic TYPE disagrees with an "EPIC:" title (it early-returns false
// for issue_type=="epic"). The actual sling guard rejects real epics/convoys in
// detectSchedulerIDType ("epic cannot be scheduled with an explicit rig"), but
// nothing in the readiness path shared that rule.
//
// gu-9j93s: `bd ready` does NOT filter type=epic beads (despite a long-standing
// assumption in filterIdentityBeads that "real epics are already filtered by bd
// ready"). So real epics surfaced as phantom ready work, got fed to
// `gt sling <id> <rig>`, and were refused by detectSchedulerIDType every cycle —
// noisy, and inflating ready counts. Both `gt ready` and `gt sling` now share
// this predicate so the readiness filter and the dispatch guard cannot drift.
//
// Mirrors detectSchedulerIDType's classification: issue_type epic/convoy, or the
// gt:epic / gt:convoy label. (The "EPIC:"-title data-hygiene fallback is covered
// separately by IsEpicLikeBeadInfo so callers can compose both.)
func IsContainerBeadInfo(info *BeadInfo) bool {
	if info == nil {
		return false
	}
	if info.IssueType == "epic" || info.IssueType == "convoy" {
		return true
	}
	return hasLabel(info.Labels, "gt:epic") || hasLabel(info.Labels, "gt:convoy")
}

// IsAwaitingMergeBeadInfo reports whether the bead is work already in flight to
// the refinery — i.e. it carries the awaiting_refinery_merge label. Such a bead
// has a submitted MR that the refinery has not yet merged to origin/main; the
// work is done and the bead stays open only so the refinery's PostMerge path can
// close it with the real on-main commit_sha (completion.MarkAwaitingRefineryMerge,
// gu-treq). It is NOT dispatchable: re-slinging it spawns a fresh polecat that
// finds the work complete, declines with no commits, and defers — a wasted
// session plus host/Dolt load.
//
// gu-ea25u: `bd ready` does not exclude beads carrying this label, so a source
// bead that is in_progress + awaiting_refinery_merge surfaces as phantom ready
// work and the auto-dispatcher re-slings it every cycle until the refinery
// merges (observed 3+ times in one day: fury×2, pipboy). Both `gt ready` and the
// sling guards now share this predicate so the readiness filter and the dispatch
// guard cannot drift — the single-predicate approach gu-9j93s established for the
// epic/convoy container gate.
func IsAwaitingMergeBeadInfo(info *BeadInfo) bool {
	if info == nil {
		return false
	}
	return hasLabel(info.Labels, labelAwaitingRefineryMerge)
}

// IsMayorOnlyBeadInfo reports whether the bead carries the mayor-only or
// no-polecat label, which marks it as unresolvable by a polecat.
//
// gu-bk6e / gt-pb857: before this guard, the auto-dispatcher would re-sling
// escalations that no polecat can fix (town root edits, origin config,
// cross-rig coordination) because it has no concept of escalation ownership.
// A polecat would spawn, see the bead is outside its directory-discipline
// scope, close no-changes, and the scheduler would immediately re-dispatch
// to the next idle polecat. Observed at 3+ iterations on ta-wisp-1z3 alone.
//
// Operators attach either label to tell the dispatcher "this requires
// mayor or human intervention — do not sling to polecats." Both labels
// are accepted; see beads.MayorOnlyLabel / beads.NoPolecatLabel.
//
// Not bypassed by --force — the label is an explicit assertion about who
// can do the work, not a dispatch preference. If a human really wants to
// force a polecat to attempt the bead, they should remove the label first.
func IsMayorOnlyBeadInfo(info *BeadInfo) bool {
	if info == nil {
		return false
	}
	return beads.HasMayorOnlyLabel(info.Labels)
}

// IsReferenceTripwireBeadInfo reports whether a bead is a permanent reference
// or gate tripwire — do-not-dispatch / pinned labels, or issue_type=reference
// (hq-9jeyo). Such beads stay OPEN forever as live safety gates and must never
// be slung: scheduling one creates a sling-context + auto-convoy and lets a
// polecat hook then CLOSE the tripwire, taking the gate down.
func IsReferenceTripwireBeadInfo(info *BeadInfo) bool {
	if info == nil {
		return false
	}
	if strings.EqualFold(info.IssueType, issueTypeReference) {
		return true
	}
	return hasLabel(info.Labels, labelDoNotDispatch) || hasLabel(info.Labels, labelPinned)
}

// IsSlingContextBeadInfo reports whether the bead is itself a sling context
// wrapper (label gt:sling-context). Sling contexts are scheduler bookkeeping
// beads — never work — and must never be re-scheduled.
//
// Without this guard, a convoy that tracks a sling context (e.g. because
// the real work bead was deleted and the convoy's dep pointer now lands
// mid-chain) would cause runConvoyScheduleByID to call scheduleBead on
// the context itself. The idempotency check in scheduleBead queries by
// WorkBeadID (JSON field), so a sling context's own ID won't match and a
// new wrapper gets created titled "sling-context: sling-context: <title>".
// Repeated retries accumulate an N-deep chain of wrappers (gu-hfr3).
func IsSlingContextBeadInfo(info *BeadInfo) bool {
	if info == nil {
		return false
	}
	for _, l := range info.Labels {
		if l == capacity.LabelSlingContext {
			return true
		}
	}
	return false
}

// IsWrongRigBeadForTarget reports whether the bead carries a
// wrong-rig:<targetRig> label. The label is the cheap-loop-breaker for the
// auto-router's owning-package mis-attribution problem (gu-mhfs).
//
// Background: when an integration test fails, the failure-classifier picks a
// rig from the test's source path. For tests that exercise a service whose
// code lives across multiple rigs (e.g. casc_lambda holds the SQS handler but
// the auth wiring lives in casc_cdk and the HTTP handler in casc_crud), the
// classifier's first guess can be persistently wrong. A polecat in the wrong
// rig closes the bead "no-changes — wrong rig," but nothing fed that signal
// back to the router; the next dispatch cycle re-routed the same bead the
// same way (cala-7e9 → obsidian, then cala-tl5 → obsidian, both wrong-rig).
//
// Operators (or, in time, the close-reason text-miner — see Layer 2 in
// gu-mhfs) attach 'wrong-rig:<rig>' labels to assert "this bead has already
// been routed to <rig> and that rig was wrong." This guard then refuses to
// re-route it to the same rig regardless of how routing chose the target.
//
// Multiple labels stack: a bead that's been wrong in two rigs carries two
// labels. If every plausible rig is labeled wrong, the bead falls through
// for human triage rather than re-cycling through polecats.
//
// Not bypassed by --force — the label is an explicit assertion about
// owning-package, not a dispatch preference. Operators who want to override
// must remove the label first (which also serves as a moment to think about
// whether the labelers were right).
func IsWrongRigBeadForTarget(info *BeadInfo, targetRig string) bool {
	if info == nil {
		return false
	}
	return beads.HasWrongRigLabelFor(info.Labels, targetRig)
}

// IsPolecatOwnedBeadInfo reports whether the bead's owner address identifies
// a polecat ("<rig>/polecats/<name>"). Polecats are not allowed to dispatch
// work — their job is to execute a slung task and return — so a bead they
// filed must never be auto-slung back to a polecat.
//
// gu-gal8: a polecat (casc_lambda/obsidian) self-created a bead (cala-akl)
// for the same work as a user-filed bead (cala-xnv) that was already slung
// to a different polecat. Both polecats raced to land the same change.
// Self-created polecat beads bypass the existing identity / epic / label
// filters because their shape (owner-only signal) was unguarded; this
// helper closes that gap by parsing the owner field for the canonical
// "<rig>/polecats/<name>" address.
//
// The owner is checked exactly — same parsing as isPolecatTarget — so
// addresses with extra path segments (e.g. "rig/polecats/name/sub") or
// non-polecat sublevels ("rig/witness", "rig/refinery") do not match.
// Plain user owners (e.g. email addresses) are unaffected.
//
// Not bypassed by --force — the contract violation is independent of
// dispatch intent. If a polecat genuinely needs to file work, that work
// should be filed by a human or the mayor.
func IsPolecatOwnedBeadInfo(info *BeadInfo) bool {
	if info == nil {
		return false
	}
	owner := strings.TrimSpace(info.Owner)
	if owner == "" {
		return false
	}
	parts := strings.Split(owner, "/")
	// Canonical polecat address has exactly 3 segments: rig, "polecats", name.
	// Each must be non-empty.
	if len(parts) != 3 {
		return false
	}
	if parts[1] != "polecats" {
		return false
	}
	return parts[0] != "" && parts[2] != ""
}

// IsEmptyAssignee reports whether the assignee field is unset. Treats the
// literal sentinel "none" as empty: the operator workaround for clearing an
// assignee was `bd update --assignee none`, which stores the string "none"
// rather than clearing the field. Without this normalization, dispatch paths
// (sling auto-burn, dead-session detection) read "none" as a real address
// and fail to recognize the bead as unassigned.
func IsEmptyAssignee(assignee string) bool {
	switch strings.ToLower(strings.TrimSpace(assignee)) {
	case "", "none":
		return true
	}
	return false
}

// IsOrphanMolecule reports whether a bead's existing attached molecule(s)
// can be safely burned at sling time without operator confirmation. Used
// to gate the auto-burn path that lets sling self-heal from stale state.
//
// A molecule is treated as orphaned when:
//   - the bead has status `open` — semantically "no live worker owns this",
//     regardless of any stale assignee value. Covers gu-koi7, where the
//     operator workaround `bd update --assignee none` left the literal
//     string "none" in the assignee field, defeating the empty-string and
//     dead-session checks below;
//   - the bead has no (or sentinel) assignee and is in_progress, or stuck
//     in `hooked` with no assignee — the latter covers gh-3697, where one
//     orphan wisp would otherwise wedge every subsequent sling to the rig
//     with "bead already has N attached molecule(s)"; or
//   - the bead has an assignee but that assignee's tmux session is dead.
//
// `closed` and `blocked` deliberately fall through to the refuse path:
// burning molecules off a closed bead would mask completed work, and
// burning off a blocked bead can mask a real dependency.
//
// assigneeDead is injected by the caller (cmd passes isHookedAgentDeadFn,
// which checks tmux session liveness) so this function stays pure and
// unit-testable without a real tmux server.
func IsOrphanMolecule(info *BeadInfo, assigneeDead func(assignee string) bool) bool {
	if info == nil {
		return false
	}
	// status=open means the bead is awaiting dispatch — by definition no live
	// worker owns it, so any attached molecule is stale. This path covers
	// operator/witness reset-to-open flows even when the assignee field still
	// carries a stale value (empty, "none", or a dead agent address).
	if info.Status == "open" {
		return true
	}
	if IsEmptyAssignee(info.Assignee) {
		switch info.Status {
		case "in_progress", "hooked":
			return true
		}
		return false
	}
	return assigneeDead(info.Assignee)
}

// CollectExistingMolecules returns all molecule wisp IDs attached to a bead.
// Checks both dependency bonds (ground truth from bd mol bond) and the
// description's attached_molecule field (metadata pointer). Wisp IDs are
// identified by containing "-wisp-" in their ID.
// Uses Dependencies (structured []IssueDep from bd show --json) rather than
// DependsOn (raw ID list, which is unreliable — see molecule_status.go comments).
func CollectExistingMolecules(info *BeadInfo) []string {
	seen := make(map[string]bool)
	var molecules []string

	// Check dependency bonds (ground truth - bd mol bond creates these)
	for _, dep := range info.Dependencies {
		if strings.Contains(dep.ID, "-wisp-") && !seen[dep.ID] {
			// Skip molecules already closed/burned — bond is stale
			if dep.Status == "closed" || dep.Status == "tombstone" {
				continue
			}
			seen[dep.ID] = true
			molecules = append(molecules, dep.ID)
		}
	}

	// Also check description's attached_molecule (may differ from bonds)
	issue := &beads.Issue{Description: info.Description}
	fields := beads.ParseAttachmentFields(issue)
	if fields != nil && fields.AttachedMolecule != "" && !seen[fields.AttachedMolecule] {
		seen[fields.AttachedMolecule] = true
		molecules = append(molecules, fields.AttachedMolecule)
	}

	return molecules
}

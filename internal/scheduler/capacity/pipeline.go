package capacity

import (
	"regexp"
	"sort"
	"strings"
)

// PriorityFloor constants define named priority floor levels.
// Lower numeric value = higher priority = dispatched first.
// Normal (0) is the default; Lowest (4) is never starves but always yields.
const (
	PriorityFloorNormal = 0 // Default: no floor applied
	PriorityFloorLow    = 2 // Low priority, yields to normal
	PriorityFloorLowest = 4 // Lowest priority — never starves user work
)

// ParsePriorityFloor converts a named priority floor string to its numeric value.
// Returns (value, true) on success, (0, false) on unrecognized input.
func ParsePriorityFloor(s string) (int, bool) {
	switch strings.ToLower(s) {
	case "normal", "":
		return PriorityFloorNormal, true
	case "low":
		return PriorityFloorLow, true
	case "lowest":
		return PriorityFloorLowest, true
	}
	return 0, false
}

// PriorityFloorName returns the human-readable name for a priority floor value.
func PriorityFloorName(floor int) string {
	switch {
	case floor <= PriorityFloorNormal:
		return "normal"
	case floor <= PriorityFloorLow:
		return "low"
	default:
		return "lowest"
	}
}

// PendingBead represents a bead that is scheduled and ready for dispatch evaluation.
type PendingBead struct {
	ID              string // Context bead ID (sling context)
	WorkBeadID      string // The actual work bead ID
	Title           string
	TargetRig       string
	Description     string
	Labels          []string
	Context         *SlingContextFields // Parsed sling params from context bead
	ContextWorkDir  string              // Work dir for the DB where the context was discovered.
	ContextBeadsDir string              // Resolved .beads dir where the context was discovered.
}

// SlingContextFields holds scheduling parameters stored on a sling context bead.
// JSON-serialized as the context bead's description.
type SlingContextFields struct {
	Version          int    `json:"version"`
	WorkBeadID       string `json:"work_bead_id"`
	TargetRig        string `json:"target_rig"`
	Formula          string `json:"formula,omitempty"`
	Args             string `json:"args,omitempty"`
	Vars             string `json:"vars,omitempty"`
	EnqueuedAt       string `json:"enqueued_at"`
	Merge            string `json:"merge,omitempty"`
	Convoy           string `json:"convoy,omitempty"`
	BaseBranch       string `json:"base_branch,omitempty"`
	ResumeBranch     string `json:"resume_branch,omitempty"`
	NoMerge          bool   `json:"no_merge,omitempty"`
	ReviewOnly       bool   `json:"review_only,omitempty"`
	Account          string `json:"account,omitempty"`
	Agent            string `json:"agent,omitempty"`
	HookRawBead      bool   `json:"hook_raw_bead,omitempty"`
	Owned            bool   `json:"owned,omitempty"`
	Mode             string `json:"mode,omitempty"`
	PriorityFloor    int    `json:"priority_floor,omitempty"` // 0=normal (default), higher=lower priority
	DispatchFailures int    `json:"dispatch_failures,omitempty"`
	LastFailure      string `json:"last_failure,omitempty"`
}

// LabelSlingContext is the label used to identify sling context beads.
const LabelSlingContext = "gt:sling-context"

// Labels that mark inter-agent messaging beads. These are never polecat work
// and must not be dispatched to rig polecats.
const (
	LabelMessage      = "gt:message"
	LabelHandoff      = "gt:handoff"
	LabelMergeRequest = "gt:merge-request"
)

// LabelNoAutoDispatch marks a bead that must never be picked up by the
// automatic scheduler/dispatch path. A human may still dispatch it manually
// via `gt sling`, but the scheduler must skip it (gs-b2a).
const LabelNoAutoDispatch = "no-auto-dispatch"

// IsNoAutoDispatch reports whether the bead carries the no-auto-dispatch label,
// meaning the automatic dispatch pipeline must not hand it to a polecat.
func IsNoAutoDispatch(labels []string) bool {
	for _, l := range labels {
		if l == LabelNoAutoDispatch {
			return true
		}
	}
	return false
}

// FilterNoAutoDispatch removes no-auto-dispatch-labeled beads from the candidate
// slice. Returns the filtered slice plus the count of removed beads. Callers
// should log the skipped beads so the gap is observable.
func FilterNoAutoDispatch(beads []PendingBead) ([]PendingBead, int) {
	var result []PendingBead
	removed := 0
	for _, b := range beads {
		if IsNoAutoDispatch(b.Labels) {
			removed++
			continue
		}
		result = append(result, b)
	}
	return result, removed
}

// IsMessagingBead reports whether the bead is an inter-agent communication
// artifact rather than dispatchable work. Used as a defensive filter in the
// dispatch pipeline: a bead carrying any of these labels must never be handed
// to a polecat (gt-el4 / gastownhall/gastown#3800).
func IsMessagingBead(labels []string) bool {
	for _, l := range labels {
		switch l {
		case LabelMessage, LabelHandoff, LabelMergeRequest:
			return true
		}
	}
	return false
}

// handoffTitlePattern matches the well-known "🤝 HANDOFF" session-continuity
// subject prefix. Mirrors witness/protocol.go PatternHandoff. Agent-authored
// handoff memos sometimes land as bare type=task beads with NO messaging label
// (gu-a76gk): the operator who routes mail adds gt:message, but a handoff memo
// created directly as a task escapes that path and is treated as dispatchable
// work, getting re-dispatched to a fresh polecat every scheduler cycle forever.
var handoffTitlePattern = regexp.MustCompile(`^🤝\s*HANDOFF`)

// IsHandoffTitle reports whether a bead title is a "🤝 HANDOFF" session-handoff
// memo. This is a belt-and-suspenders guard for handoff beads that are missing
// the gt:handoff / gt:message label that IsMessagingBead relies on (gu-a76gk).
func IsHandoffTitle(title string) bool {
	return handoffTitlePattern.MatchString(strings.TrimSpace(title))
}

// FilterMessagingBeads removes messaging-labeled beads from the candidate slice.
// Returns the filtered slice plus the count of removed beads. Callers should
// log the skipped beads at debug level so the gap is observable.
//
// Beads are filtered out if they carry a messaging label OR have a "🤝 HANDOFF"
// title — the latter catches unlabeled handoff memos that would otherwise be
// dispatched as work and re-dispatched forever (gu-a76gk).
func FilterMessagingBeads(beads []PendingBead) ([]PendingBead, int) {
	var result []PendingBead
	removed := 0
	for _, b := range beads {
		if IsMessagingBead(b.Labels) || IsHandoffTitle(b.Title) {
			removed++
			continue
		}
		result = append(result, b)
	}
	return result, removed
}

// DispatchPlan is the output of PlanDispatch — what to dispatch and why.
type DispatchPlan struct {
	ToDispatch []PendingBead
	Skipped    int
	Reason     string // "capacity" | "batch" | "ready" | "none"
}

// FailureAction indicates what to do after a dispatch failure.
type FailureAction int

const (
	// FailureRetry means the bead should be retried on the next cycle.
	FailureRetry FailureAction = iota
	// FailureQuarantine means the bead should be marked as permanently failed.
	FailureQuarantine
)

// ReadinessFilter is a function that filters pending beads to those ready for dispatch.
type ReadinessFilter func(pending []PendingBead) []PendingBead

// FailurePolicy is a function that determines what to do after N failures.
type FailurePolicy func(failures int) FailureAction

// AllReady is a ReadinessFilter that passes all beads through (no filtering).
func AllReady(pending []PendingBead) []PendingBead {
	return pending
}

// BlockerAware returns a ReadinessFilter that only passes beads whose WorkBeadID
// appears in the readyIDs set (i.e., beads whose work bead has no unresolved blockers).
func BlockerAware(readyIDs map[string]bool) ReadinessFilter {
	return func(pending []PendingBead) []PendingBead {
		var result []PendingBead
		for _, b := range pending {
			if readyIDs[b.WorkBeadID] {
				result = append(result, b)
			}
		}
		return result
	}
}

// SortByPriorityFloor sorts pending beads by priority floor (ascending: lower
// floor = higher priority = dispatched first). Within the same floor, the
// existing order (typically enqueue time) is preserved via stable sort.
// This ensures that beads with --priority-floor=lowest never starve user work.
func SortByPriorityFloor(beads []PendingBead) {
	sort.SliceStable(beads, func(i, j int) bool {
		fi := priorityFloorOf(beads[i])
		fj := priorityFloorOf(beads[j])
		return fi < fj
	})
}

// priorityFloorOf extracts the priority floor from a PendingBead's context.
// Returns 0 (normal) if context is nil or floor is unset.
func priorityFloorOf(b PendingBead) int {
	if b.Context == nil {
		return PriorityFloorNormal
	}
	return b.Context.PriorityFloor
}

// PlanDispatch computes which beads to dispatch given capacity constraints.
// availableCapacity: free slots (positive = that many slots, <= 0 = no capacity).
// batchSize: max beads per cycle.
// ready: beads that passed readiness filtering.
//
// Messaging-labeled beads (gt:message / gt:handoff / gt:merge-request) are
// filtered out defensively before any capacity math runs. They are inter-agent
// communication artifacts and never dispatchable work; if any survived earlier
// filtering they must not reach a polecat (gt-el4).
//
// Beads are sorted by priority floor before capacity limits are applied:
// lower floor values (higher priority) are dispatched first. This guarantees
// that --priority-floor=lowest beads never starve user work when capacity is
// constrained.
func PlanDispatch(availableCapacity, batchSize int, ready []PendingBead) DispatchPlan {
	ready, msgSkipped := FilterMessagingBeads(ready)

	// Defensive filter: beads flagged no-auto-dispatch must never be picked up
	// by the scheduler (gs-b2a). The primary guard is in the dispatch readiness
	// gate (cmd.isScheduledWorkBeadReady); this mirrors the messaging-bead
	// belt-and-suspenders so a label that slipped past earlier filtering still
	// cannot reach a polecat. Folded into msgSkipped for skip accounting.
	ready, noAutoSkipped := FilterNoAutoDispatch(ready)
	msgSkipped += noAutoSkipped

	if len(ready) == 0 {
		if msgSkipped > 0 {
			return DispatchPlan{Skipped: msgSkipped, Reason: "messaging-filtered"}
		}
		return DispatchPlan{Reason: "none"}
	}

	// Sort by priority floor: normal (0) before low (2) before lowest (4).
	// Stable sort preserves FIFO ordering within the same priority level.
	SortByPriorityFloor(ready)

	if availableCapacity <= 0 {
		return DispatchPlan{
			Skipped: len(ready) + msgSkipped,
			Reason:  "capacity",
		}
	}

	// Dispatch up to the smallest of capacity, batchSize, and readyBeads count
	toDispatch := batchSize
	if availableCapacity < toDispatch {
		toDispatch = availableCapacity
	}
	if len(ready) < toDispatch {
		toDispatch = len(ready)
	}

	reason := "batch"
	if availableCapacity < batchSize && availableCapacity < len(ready) {
		reason = "capacity"
	}
	if len(ready) < batchSize && len(ready) < availableCapacity {
		reason = "ready"
	}

	skipped := len(ready) - toDispatch + msgSkipped
	if msgSkipped > 0 {
		reason = reason + "+messaging-filtered"
	}

	return DispatchPlan{
		ToDispatch: ready[:toDispatch],
		Skipped:    skipped,
		Reason:     reason,
	}
}

// NoRetryPolicy returns a FailurePolicy that always quarantines on first failure.
func NoRetryPolicy() FailurePolicy {
	return func(failures int) FailureAction {
		return FailureQuarantine
	}
}

// CircuitBreakerPolicy returns a FailurePolicy that retries up to maxFailures
// times, then quarantines.
func CircuitBreakerPolicy(maxFailures int) FailurePolicy {
	return func(failures int) FailureAction {
		if failures >= maxFailures {
			return FailureQuarantine
		}
		return FailureRetry
	}
}

// FilterCircuitBroken removes beads that have exceeded the maximum dispatch
// failures threshold. Returns the filtered list and the count of removed beads.
func FilterCircuitBroken(beads []PendingBead, maxFailures int) ([]PendingBead, int) {
	var result []PendingBead
	removed := 0
	for _, b := range beads {
		if b.Context != nil && b.Context.DispatchFailures >= maxFailures {
			removed++
			continue
		}
		result = append(result, b)
	}
	return result, removed
}

// DispatchParams captures what the scheduler needs to tell the dispatcher.
// Mirrors the relevant fields from cmd.SlingParams but is scheduler-owned.
type DispatchParams struct {
	BeadID       string
	FormulaName  string
	RigName      string
	Args         string
	Vars         []string
	Merge        string
	BaseBranch   string
	ResumeBranch string
	Account      string
	Agent        string
	Mode         string
	NoMerge      bool
	ReviewOnly   bool
	HookRawBead  bool
}

// ReconstructFromContext builds DispatchParams from sling context fields.
func ReconstructFromContext(ctx *SlingContextFields) DispatchParams {
	p := DispatchParams{
		BeadID:       ctx.WorkBeadID,
		RigName:      ctx.TargetRig,
		FormulaName:  ctx.Formula,
		Args:         ctx.Args,
		Merge:        ctx.Merge,
		BaseBranch:   ctx.BaseBranch,
		ResumeBranch: ctx.ResumeBranch,
		Account:      ctx.Account,
		Agent:        ctx.Agent,
		Mode:         ctx.Mode,
		NoMerge:      ctx.NoMerge,
		ReviewOnly:   ctx.ReviewOnly,
		HookRawBead:  ctx.HookRawBead,
	}
	if ctx.Vars != "" {
		p.Vars = splitVars(ctx.Vars)
	}
	return p
}

// splitVars splits a newline-separated vars string into individual key=value pairs.
func splitVars(vars string) []string {
	if vars == "" {
		return nil
	}
	var result []string
	for _, line := range strings.Split(vars, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

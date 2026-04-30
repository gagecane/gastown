// Package beads provides issue data types and classification helpers.
package beads

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Issue represents a beads issue.
type Issue struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Priority    int      `json:"priority"`
	Type        string   `json:"issue_type"`
	CreatedAt   string   `json:"created_at"`
	CreatedBy   string   `json:"created_by,omitempty"`
	UpdatedAt   string   `json:"updated_at"`
	ClosedAt    string   `json:"closed_at,omitempty"`
	Parent      string   `json:"parent,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
	Children    []string `json:"children,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Blocks      []string `json:"blocks,omitempty"`
	BlockedBy   []string `json:"blocked_by,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Ephemeral   bool     `json:"ephemeral,omitempty"` // Wisp/ephemeral issues, not synced to git

	// Content fields (parsed from bd show --json)
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`

	// Agent bead slots (type=agent only)
	HookBead   string `json:"hook_bead,omitempty"`   // Current work attached to agent's hook
	AgentState string `json:"agent_state,omitempty"` // Agent lifecycle state (spawning, working, done, stuck)
	// Note: role_bead field removed - role definitions are now config-based

	// Counts from list output
	DependencyCount int `json:"dependency_count,omitempty"`
	DependentCount  int `json:"dependent_count,omitempty"`
	BlockedByCount  int `json:"blocked_by_count,omitempty"`

	// Detailed dependency info from show output
	Dependencies []IssueDep `json:"dependencies,omitempty"`
	Dependents   []IssueDep `json:"dependents,omitempty"`

	// Arbitrary metadata blob (JSON object). Used for extension points such as
	// delegation state (delegated_from key) and merge-slot state (holder/waiters).
	// Populated by both bd show --json and the in-process store path.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// IssueDep represents a dependency or dependent issue with its relation.
type IssueDep struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Priority       int    `json:"priority"`
	Type           string `json:"issue_type"`
	DependencyType string `json:"dependency_type,omitempty"`
}

// HasLabel checks if an issue has a specific label.
func HasLabel(issue *Issue, label string) bool {
	for _, l := range issue.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// HasUncheckedCriteria checks if an issue has acceptance criteria with unchecked items.
// Returns the count of unchecked items (0 means all checked or no criteria).
func HasUncheckedCriteria(issue *Issue) int {
	if issue == nil || issue.AcceptanceCriteria == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(issue.AcceptanceCriteria, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [ ] ") {
			count++
		}
	}
	return count
}

// IsAgentBead checks if an issue is an agent bead by checking for the gt:agent
// label (preferred) or the legacy type == "agent" field. This handles the migration
// from type-based to label-based agent identification (see gt-vja7b).
func IsAgentBead(issue *Issue) bool {
	if issue == nil {
		return false
	}
	// Check legacy type field first for backward compatibility
	if issue.Type == "agent" {
		return true
	}
	// Check for gt:agent label (current standard)
	return HasLabel(issue, "gt:agent")
}

// identityBeadTitleRe matches identity/system bead titles by naming convention.
// Examples that match: "af-agentforge-polecat-quartz", "af-agentforge-refinery",
// "gu-gastown-polecat-nux", "gu-gastown-witness", "gu-gastown-crew-joe",
// "hq-dog-alpha", "hq-mayor", "hq-deacon". These are agent/role beads that
// must never be dispatched as work, even if they lack the gt:agent label or
// have non-closed status (see gu-ypjm "ghost dispatch loop", gu-huta "widen
// filter to cover every role").
//
// The regex covers all agent roles used across Gas Town:
//   - Per-rig named agents: <prefix>-<rig>-polecat-<name>, <prefix>-<rig>-crew-<name>
//   - Per-rig singletons:   <prefix>-<rig>-witness, <prefix>-<rig>-refinery
//   - Town-level named:     <prefix>-dog-<name>
//   - Town-level singleton: <prefix>-mayor, <prefix>-deacon
//
// The role token is anchored at the end of the title to avoid matching work
// beads that merely mention a role earlier (e.g. "af-refinery-feature-work").
// The prefix is [a-z]+ (lowercase), matching the canonical ID format from
// AgentBeadIDWithPrefix.
var identityBeadTitleRe = regexp.MustCompile(`^[a-z]+-(.+-(polecat-.+|crew-.+|refinery|witness)|dog-.+|mayor|deacon)$`)

// IsIdentityBeadTitle reports whether a title matches the identity/system
// naming convention (prefix-rig-role[-name]). Exported for callers that only
// have a title string (e.g. dep metadata snapshots).
func IsIdentityBeadTitle(title string) bool {
	if title == "" {
		return false
	}
	return identityBeadTitleRe.MatchString(title)
}

// IsIdentityBead reports whether an issue is an identity/system bead that
// must never be dispatched as work. This is the broader ghost-dispatch filter
// from gu-ypjm, extended in gu-3znx to cover every dispatch path (sling, sling
// dispatch, deferred scheduler). Matches any of:
//
//  1. label "gt:agent" (explicit identity marker)
//  2. legacy issue_type == "agent" (pre-migration beads)
//  3. status == "closed" (closed beads must never dispatch)
//  4. title matches identity/system naming convention (polecat/refinery/
//     witness/deacon/mayor/crew beads)
//
// The title regex is defense-in-depth: labels and status are often stale in
// cross-rig dep metadata, and gt doctor --fix agent-beads-exist re-creates
// identity beads with status=open whenever they go missing.
//
// Callers that only have a "label or type=agent" signal should prefer the
// narrower IsAgentBead; this helper is the correct choice for any code path
// that decides whether a bead may be dispatched as work.
func IsIdentityBead(issue *Issue) bool {
	if issue == nil {
		return false
	}
	if IsAgentBead(issue) {
		return true
	}
	if issue.Status == "closed" {
		return true
	}
	return IsIdentityBeadTitle(issue.Title)
}

// IsIdentityBeadFields is the fields-based variant of IsIdentityBead. Callers
// that have decomposed title/status/labels (e.g. convoy tracked issues or
// cmd/sling beadInfo) use this to avoid constructing a full Issue.
func IsIdentityBeadFields(title, status string, labels []string) bool {
	for _, l := range labels {
		if l == "gt:agent" {
			return true
		}
	}
	if status == "closed" {
		return true
	}
	return IsIdentityBeadTitle(title)
}

// IsProtectedBead checks if a bead has any protection labels that should
// prevent automated status changes (AutoClose, unassign on polecat removal, etc.).
// Protected labels: gt:standing-orders, gt:keep, gt:role, gt:rig.
func IsProtectedBead(issue *Issue) bool {
	if issue == nil {
		return false
	}
	for _, l := range issue.Labels {
		switch l {
		case "gt:standing-orders", "gt:keep", "gt:role", "gt:rig":
			return true
		}
	}
	return false
}

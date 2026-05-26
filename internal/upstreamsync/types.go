package upstreamsync

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SyncState represents the state machine states for upstream sync.
type SyncState string

const (
	// StateIdle — No sync in progress, fork may or may not be current.
	StateIdle SyncState = "idle"

	// StateChecking — Agent is evaluating whether sync is needed.
	StateChecking SyncState = "checking"

	// StateSyncing — Merge/rebase in progress (no conflicts).
	StateSyncing SyncState = "syncing"

	// StateResolving — Conflicts detected, agent is resolving them.
	StateResolving SyncState = "resolving"

	// StateGating — Merge done, running CI gates (build, test, vet).
	StateGating SyncState = "gating"

	// StatePushing — Gates passed, pushing to origin.
	StatePushing SyncState = "pushing"

	// StateFailed — Attempt failed (gates, push, or unresolvable conflict).
	StateFailed SyncState = "failed"

	// StatePaused — Manually or automatically paused (circuit breaker).
	StatePaused SyncState = "paused"
)

// ValidStates is the set of all valid SyncState values.
var ValidStates = []SyncState{
	StateIdle, StateChecking, StateSyncing, StateResolving,
	StateGating, StatePushing, StateFailed, StatePaused,
}

// IsValid reports whether the state is a recognized value.
func (s SyncState) IsValid() bool {
	for _, v := range ValidStates {
		if s == v {
			return true
		}
	}
	return false
}

// GateResult represents the outcome of a single CI gate command.
type GateResult string

const (
	GatePass GateResult = "pass"
	GateFail GateResult = "fail"
	GateSkip GateResult = "skip"
)

// SyncAttempt records one sync attempt's lifecycle.
type SyncAttempt struct {
	// ID is the unique attempt identifier (e.g., "gu-sync-att-001").
	ID string `json:"id"`

	// StartedAt is the RFC3339 timestamp when the attempt began.
	StartedAt string `json:"started_at"`

	// CompletedAt is the RFC3339 timestamp when the attempt ended.
	// Empty if the attempt is still in progress.
	CompletedAt string `json:"completed_at,omitempty"`

	// Outcome is the final result: "success", "conflict", "gate-failure",
	// "push-failure", "skipped", or "error".
	Outcome string `json:"outcome,omitempty"`

	// UpstreamSHA is the upstream commit being synced to.
	UpstreamSHA string `json:"upstream_sha"`

	// PreSyncSHA is the fork's commit before the sync.
	PreSyncSHA string `json:"pre_sync_sha"`

	// PostSyncSHA is the fork's commit after a successful sync.
	PostSyncSHA string `json:"post_sync_sha,omitempty"`

	// Strategy is the merge strategy used: "fast-forward", "merge", or "rebase".
	Strategy string `json:"strategy"`

	// Conflicts lists files with merge conflicts (if any).
	Conflicts []string `json:"conflicts,omitempty"`

	// GateResults maps gate command → pass/fail/skip.
	GateResults map[string]GateResult `json:"gate_results,omitempty"`

	// Actor is the agent that performed this attempt.
	Actor string `json:"actor,omitempty"`
}

// CurrentAttempt holds state for an in-progress sync attempt.
// Non-null when state ∉ {idle, paused, failed}.
type CurrentAttempt struct {
	// ID is the unique attempt identifier.
	ID string `json:"id"`

	// StartedAt is the RFC3339 timestamp when this attempt began.
	StartedAt string `json:"started_at"`

	// UpstreamSHA is the upstream commit being synced to.
	UpstreamSHA string `json:"upstream_sha"`

	// PreSyncSHA is the fork's commit before the sync.
	PreSyncSHA string `json:"pre_sync_sha"`

	// Strategy is the merge strategy: "merge", "rebase", or "fast-forward".
	Strategy string `json:"strategy"`

	// Conflicts lists files with merge conflicts (only in StateResolving).
	Conflicts []string `json:"conflicts,omitempty"`

	// ResolutionBranch is the branch being used for conflict resolution.
	ResolutionBranch string `json:"resolution_branch,omitempty"`

	// PolecatBead is the work bead dispatched for conflict resolution.
	PolecatBead string `json:"polecat_bead,omitempty"`

	// GateResults maps gate command → pass/fail/skip (populated in StateGating).
	GateResults map[string]GateResult `json:"gate_results,omitempty"`

	// Actor is the agent performing this attempt.
	Actor string `json:"actor,omitempty"`
}

// StateBeadSchemaVersion is the current on-disk schema version.
const StateBeadSchemaVersion = 1

// SyncStateMetadata is the JSON payload of the per-rig upstream-sync
// pinned bead's Issue.Metadata field.
type SyncStateMetadata struct {
	// SchemaVersion follows StateBeadSchemaVersion.
	SchemaVersion int `json:"schema_version"`

	// Rig is the rig this state belongs to.
	Rig string `json:"rig"`

	// State is the current state machine state.
	State SyncState `json:"state"`

	// UpstreamRemote is the git remote name for upstream.
	UpstreamRemote string `json:"upstream_remote"`

	// UpstreamBranch is the branch being tracked on upstream.
	UpstreamBranch string `json:"upstream_branch"`

	// TargetBranch is the local/origin branch being synced to.
	TargetBranch string `json:"target_branch"`

	// LastSyncAt is the RFC3339 timestamp of the last successful sync.
	LastSyncAt string `json:"last_sync_at,omitempty"`

	// LastSyncOutcome is the outcome of the last completed attempt.
	LastSyncOutcome string `json:"last_sync_outcome,omitempty"`

	// LastSyncSHA is the upstream SHA of the last successful sync.
	LastSyncSHA string `json:"last_sync_sha,omitempty"`

	// CurrentAttempt holds the in-progress attempt (nil when idle/paused/failed).
	CurrentAttempt *CurrentAttempt `json:"current_attempt"`

	// PausedUntil is the RFC3339 timestamp at which the pause expires.
	// Empty when not paused.
	PausedUntil string `json:"paused_until,omitempty"`

	// PauseReason is the operator-provided reason for the pause.
	PauseReason string `json:"pause_reason,omitempty"`

	// ConsecutiveFailures is the count of consecutive failed attempts.
	// Resets to 0 on success. Triggers circuit breaker at threshold.
	ConsecutiveFailures int `json:"consecutive_failures"`

	// Attempts is the bounded history of sync attempts (FIFO, max 30).
	Attempts []SyncAttempt `json:"attempts,omitempty"`
}

// DefaultSyncStateMetadata returns the initial state for a newly-enabled rig.
func DefaultSyncStateMetadata(rig, upstreamRemote, upstreamBranch, targetBranch string) SyncStateMetadata {
	return SyncStateMetadata{
		SchemaVersion:  StateBeadSchemaVersion,
		Rig:            rig,
		State:          StateIdle,
		UpstreamRemote: upstreamRemote,
		UpstreamBranch: upstreamBranch,
		TargetBranch:   targetBranch,
	}
}

// MarshalMetadata serializes a SyncStateMetadata to JSON bytes suitable
// for Issue.Metadata.
func (s SyncStateMetadata) MarshalMetadata() (json.RawMessage, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshaling upstream sync state: %w", err)
	}
	return raw, nil
}

// UnmarshalSyncState parses Issue.Metadata bytes into a SyncStateMetadata.
// Empty/null metadata yields a zero value (callers should use
// DefaultSyncStateMetadata for initialization).
func UnmarshalSyncState(raw json.RawMessage) (SyncStateMetadata, error) {
	if len(raw) == 0 {
		return SyncStateMetadata{}, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" || trimmed == "" {
		return SyncStateMetadata{}, nil
	}
	var s SyncStateMetadata
	if err := json.Unmarshal(raw, &s); err != nil {
		return SyncStateMetadata{}, fmt.Errorf("unmarshaling upstream sync state: %w", err)
	}
	return s, nil
}

// StateBeadID returns the well-known bead ID for a rig's upstream sync
// state bead: "<rig-prefix>-upstream-sync-state".
// For gastown_upstream (prefix "gu"), this is "gu-upstream-sync-state".
func StateBeadID(rigPrefix string) string {
	return rigPrefix + "-upstream-sync-state"
}

// StateBeadTitle returns the human-readable title for the state bead.
func StateBeadTitle(rig string) string {
	return fmt.Sprintf("Upstream sync state (%s)", rig)
}

// StateBeadLabel is the label for upstream sync state beads.
const StateBeadLabel = "gt:upstream-sync-state"

// StatusSummary is the external-facing status for `gt upstream status`.
type StatusSummary struct {
	// Rig is the rig name.
	Rig string `json:"rig"`

	// State is the current state machine state as a human-readable string.
	State string `json:"state"`

	// Behind is the number of commits the fork is behind upstream.
	// Computed at status time, not stored in the bead.
	Behind int `json:"behind"`

	// LastSyncAt is when the last successful sync completed.
	LastSyncAt string `json:"last_sync_at,omitempty"`

	// LastSyncOutcome is the outcome of the last attempt.
	LastSyncOutcome string `json:"last_sync_outcome,omitempty"`

	// Paused indicates whether sync is currently paused.
	Paused bool `json:"paused"`

	// PauseReason is the reason for the pause (if paused).
	PauseReason string `json:"pause_reason,omitempty"`

	// ConsecutiveFailures is the current failure count.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`

	// UpstreamRemote is the configured upstream remote.
	UpstreamRemote string `json:"upstream_remote"`

	// UpstreamBranch is the configured upstream branch.
	UpstreamBranch string `json:"upstream_branch"`

	// TargetBranch is the configured target branch.
	TargetBranch string `json:"target_branch"`

	// Enabled is whether upstream sync is configured for this rig.
	Enabled bool `json:"enabled"`
}

// ToStatusSummary projects a SyncStateMetadata into the external-facing
// status shape used by `gt upstream status`. The Behind field is not
// populated here (requires git ops); callers must fill it separately.
func (s SyncStateMetadata) ToStatusSummary() StatusSummary {
	paused := s.State == StatePaused || s.PausedUntil != ""

	// Derive a human-friendly state string.
	state := string(s.State)
	if paused && s.State != StatePaused {
		state = "paused"
	}

	return StatusSummary{
		Rig:                 s.Rig,
		State:               state,
		LastSyncAt:          s.LastSyncAt,
		LastSyncOutcome:     s.LastSyncOutcome,
		Paused:              paused,
		PauseReason:         s.PauseReason,
		ConsecutiveFailures: s.ConsecutiveFailures,
		UpstreamRemote:      s.UpstreamRemote,
		UpstreamBranch:      s.UpstreamBranch,
		TargetBranch:        s.TargetBranch,
		Enabled:             true,
	}
}

// FormatLastSync returns a human-readable "X ago" string for the last
// sync timestamp. Returns "never" if no sync has occurred.
func FormatLastSync(lastSyncAt string) string {
	if lastSyncAt == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339, lastSyncAt)
	if err != nil {
		return lastSyncAt // fallback to raw string
	}
	dur := time.Since(t)
	switch {
	case dur < time.Minute:
		return "just now"
	case dur < time.Hour:
		return fmt.Sprintf("%dm ago", int(dur.Minutes()))
	case dur < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(dur.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(dur.Hours()/24))
	}
}

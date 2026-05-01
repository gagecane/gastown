// Package deacon provides the Deacon agent infrastructure.
package deacon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

// StaleSpawningConfig holds configurable parameters for stale spawning-agent
// detection.
//
// Agent beads transition to agent_state=spawning when a polecat (or other
// agent) is being brought up — the sandbox is being created and the tmux
// session is not yet live. Under normal conditions the agent finishes spawning
// in well under a minute. If the spawn aborts (setup script fails, parent
// shell dies, rig gets renamed or removed mid-spawn), nothing transitions the
// bead out of spawning — it accumulates indefinitely. See gu-iabm.
//
// This sweeper is the town-level watchdog: it runs from the Deacon, which has
// visibility across every rig, so it also catches beads orphaned by rig
// renames or removals (the per-rig witness can't see those because the rig's
// polecats/ directory is gone).
type StaleSpawningConfig struct {
	// MaxAge is how long an agent bead may stay in agent_state=spawning before
	// being considered stuck. Defaults to 1 hour per gu-iabm AC.
	MaxAge time.Duration `json:"max_age"`
	// DryRun, if true, reports what would be done without making changes.
	DryRun bool `json:"dry_run"`
}

// DefaultStaleSpawningConfig returns the default stale spawning config.
func DefaultStaleSpawningConfig() *StaleSpawningConfig {
	return &StaleSpawningConfig{
		MaxAge: 1 * time.Hour,
		DryRun: false,
	}
}

// spawningBead is a minimal representation of an agent bead pulled from
// `bd list --label=gt:agent --json`. Only fields consulted by this sweeper
// are parsed; everything else is ignored.
type spawningBead struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Assignee    string    `json:"assignee"`
	Description string    `json:"description"`
	Labels      []string  `json:"labels"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// StaleSpawningResult describes the outcome for a single stuck-spawning bead.
type StaleSpawningResult struct {
	BeadID       string `json:"bead_id"`
	Title        string `json:"title"`
	Assignee     string `json:"assignee"`
	Age          string `json:"age"`
	SessionAlive bool   `json:"session_alive"`
	Closed       bool   `json:"closed"`
	Error        string `json:"error,omitempty"`
}

// StaleSpawningScanResult aggregates the results of a scan.
type StaleSpawningScanResult struct {
	ScannedAt     time.Time              `json:"scanned_at"`
	TotalSpawning int                    `json:"total_spawning"`
	StaleCount    int                    `json:"stale_count"`
	Closed        int                    `json:"closed"`
	Results       []*StaleSpawningResult `json:"results"`
}

// ScanStaleSpawning finds agent beads stuck in agent_state=spawning and
// closes them when the underlying session is confirmed dead. A bead is
// considered stuck when BOTH of the following hold:
//
//  1. The bead has been in spawning state for longer than cfg.MaxAge
//     (using UpdatedAt as the spawn-start proxy, since that is when the
//     description was last written — typically at polecat creation).
//  2. No live tmux session exists for the bead's identity.
//
// The two-factor gate avoids killing agents that are merely slow to spawn
// (the first factor) and agents whose session is actually alive but whose
// state field hasn't been rewritten yet (the second factor).
//
// Beads with no recognizable tmux session name (unusual assignees, town-level
// agents that encode rig=null, etc.) are still reported but not acted on —
// we can't confirm death, so we leave them for a human or a more specific
// sweeper (e.g. gt doctor --fix stale-agent-beads).
//
// Returns the scan result; a non-nil error indicates the scan itself could
// not be completed (e.g. bd CLI unavailable).
func ScanStaleSpawning(townRoot string, cfg *StaleSpawningConfig) (*StaleSpawningScanResult, error) {
	if cfg == nil {
		cfg = DefaultStaleSpawningConfig()
	}

	result := &StaleSpawningScanResult{
		ScannedAt: time.Now().UTC(),
		Results:   make([]*StaleSpawningResult, 0),
	}

	candidates, err := listSpawningAgentBeads(townRoot)
	if err != nil {
		return nil, fmt.Errorf("listing spawning agent beads: %w", err)
	}
	result.TotalSpawning = len(candidates)

	threshold := time.Now().Add(-cfg.MaxAge)
	t := tmux.NewTmux()

	for _, bead := range candidates {
		// Fast age gate first so we don't thrash tmux for fresh spawns.
		if !bead.UpdatedAt.Before(threshold) {
			continue
		}

		r := &StaleSpawningResult{
			BeadID:   bead.ID,
			Title:    bead.Title,
			Assignee: bead.Assignee,
			Age:      time.Since(bead.UpdatedAt).Round(time.Minute).String(),
		}

		// Try to check session liveness. If we can't resolve a session name
		// (e.g. town-level agent with rig=null, or exotic assignee format),
		// treat this as non-stale: we'd rather leave an ambiguous bead alone
		// than close a mayor/deacon by mistake.
		sessionName := beadToSessionName(&bead)
		if sessionName == "" {
			result.StaleCount++
			r.Error = "cannot resolve session name (cannot verify death)"
			result.Results = append(result.Results, r)
			continue
		}

		alive, _ := t.HasSession(sessionName)
		r.SessionAlive = alive

		if alive {
			// The bead hasn't been updated in a while, but the session is
			// alive. Someone else (witness, polecat itself) is responsible
			// for rewriting the state. Skip.
			continue
		}

		result.StaleCount++

		if cfg.DryRun {
			result.Results = append(result.Results, r)
			continue
		}

		if err := closeSpawningBead(townRoot, bead.ID, cfg.MaxAge); err != nil {
			r.Error = err.Error()
		} else {
			r.Closed = true
			result.Closed++
		}
		result.Results = append(result.Results, r)
	}

	return result, nil
}

// listSpawningAgentBeads returns agent beads whose description carries
// agent_state: spawning.
//
// Implementation note: bd has no structured filter on agent_state, so we
// list all open agent beads and parse the description. Agent beads are
// relatively few (dozens, not thousands) so parsing every description
// per-scan is acceptable.
func listSpawningAgentBeads(townRoot string) ([]spawningBead, error) {
	cmd := exec.Command("bd", "list",
		"--label=gt:agent",
		"--include-infra",
		"--status=open,in_progress,blocked,hooked,pinned",
		"--json",
		"--flat",
		"--limit=0",
	)
	cmd.Dir = townRoot
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		// Treat "no issues" as empty, not an error.
		if strings.Contains(string(output), "no issues found") {
			return nil, nil
		}
		return nil, err
	}

	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if trimmed[0] != '[' && trimmed[0] != '{' {
		// Unexpected non-JSON payload (e.g. "(no issues found)").
		return nil, nil
	}

	var all []spawningBead
	if err := json.Unmarshal(output, &all); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	spawning := make([]spawningBead, 0, len(all))
	for _, b := range all {
		if isSpawningState(b.Description) {
			spawning = append(spawning, b)
		}
	}
	return spawning, nil
}

// isSpawningState reports whether a description carries agent_state: spawning.
// Uses the same parser as the rest of the codebase so we stay aligned with
// ResolveAgentState semantics.
func isSpawningState(description string) bool {
	fields := beads.ParseAgentFields(description)
	if fields == nil {
		return false
	}
	return fields.AgentState == string(beads.AgentStateSpawning)
}

// beadToSessionName resolves a spawning agent bead to its tmux session name.
// It prefers the assignee (which is the canonical agent address used by the
// dispatcher and witness), falling back to the description's role_type+rig
// fields so orphans without assignees can still be matched.
//
// Returns an empty string when the bead does not describe a tmux-backed agent
// (e.g. town-level agent with rig=null: we can't close mayor/deacon via this
// path and shouldn't try).
func beadToSessionName(bead *spawningBead) string {
	if name := assigneeToSessionName(bead.Assignee); name != "" {
		return name
	}
	fields := beads.ParseAgentFields(bead.Description)
	if fields == nil || fields.Rig == "" || fields.RoleType == "" {
		return ""
	}

	prefix := session.PrefixFor(fields.Rig)
	// Reconstruct the expected session name by role. Only polecats and crew
	// encode a worker name in the bead ID; witness/refinery are singletons
	// and their session name is deterministic from the prefix.
	switch fields.RoleType {
	case "witness":
		return session.WitnessSessionName(prefix)
	case "refinery":
		return session.RefinerySessionName(prefix)
	case "polecat":
		if name := workerNameFromBeadID(bead.ID, "polecat"); name != "" {
			return session.PolecatSessionName(prefix, name)
		}
	case "crew":
		if name := workerNameFromBeadID(bead.ID, "crew"); name != "" {
			return session.CrewSessionName(prefix, name)
		}
	}
	return ""
}

// workerNameFromBeadID extracts the worker name from an agent bead ID.
// Handles both the collapsed form (prefix-role-name when prefix == rig)
// and the expanded form (prefix-rig-role-name).
//
// Returns empty string if the ID does not contain a role marker, because
// falling through to a default session name would risk hitting an unrelated
// agent.
func workerNameFromBeadID(id, role string) string {
	marker := "-" + role + "-"
	idx := strings.Index(id, marker)
	if idx < 0 {
		return ""
	}
	return id[idx+len(marker):]
}

// closeSpawningBead closes a stuck spawning agent bead with a descriptive
// reason.
//
// Uses `bd close --reason` directly rather than `bd update --status=closed`
// so the close shows up in audit trails with our diagnostic. We mirror the
// `bd` invocation style used by the rest of this package for consistency.
func closeSpawningBead(townRoot, beadID string, maxAge time.Duration) error {
	reason := fmt.Sprintf(
		"spawn-failed: agent bead stuck in spawning state for >%s with no live session (gu-iabm)",
		maxAge.Round(time.Minute),
	)
	cmd := exec.Command("bd", "close", beadID, "--reason", reason)
	cmd.Dir = townRoot
	util.SetDetachedProcessGroup(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

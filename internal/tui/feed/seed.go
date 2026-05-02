package feed

// This file implements "agent seeding" — pre-populating the activity tree
// from agent beads at startup so every known agent appears in the tree,
// not just those that have emitted a recent event (see gu-dupd).
//
// Without seeding, m.rigs is populated exclusively by addEventLocked, which
// means a polecat with an agent bead and worktree but no recent event in the
// retained window (maxEventHistory = 1000) doesn't appear in the tree at all.
// That diverges from the `gt status` mental model where every registered
// agent is always visible, just marked as idle.
//
// Events that arrive later decorate the seeded agents (setting LastEvent,
// LastUpdate, Status) rather than creating new entries.

import (
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

// AgentBeadSource is the minimal beads client surface needed for seeding.
// Accepting an interface (rather than a concrete *beads.Beads) keeps the
// feed package testable without standing up Dolt.
type AgentBeadSource interface {
	ListAgentBeads() (map[string]*beads.Issue, error)
}

// SeedAgents populates m.rigs with placeholder Agent entries for every
// agent bead in the supplied sources. Town-level beads (mayor, deacon) are
// ignored because the activity tree is keyed by rig — there is no "town"
// rig bucket.
//
// townSrc may be nil (tree seeding works without town-level sources).
// rigSrcs is keyed by rig name and provides agent beads for that rig. Agent
// IDs with a rig component that doesn't match the map key are skipped; the
// map key is treated as authoritative so we never conjure a fake rig.
//
// SeedAgents is idempotent: agents already in m.rigs keep their LastEvent
// and Status. This lets callers seed at startup (before events arrive) or
// re-seed on a schedule without clobbering live event data.
func (m *Model) SeedAgents(townSrc AgentBeadSource, rigSrcs map[string]AgentBeadSource) {
	// Collect seed entries outside the lock. ListAgentBeads can be slow
	// (it shells out to bd list), and holding m.mu during those queries
	// would block the event loop and View().
	type seeded struct {
		rig   string
		actor string
		role  string
	}
	var entries []seeded

	// Town-level beads are skipped for tree seeding (no rig bucket), but
	// collecting them keeps the door open if a future change adds a
	// synthetic "town" rig or surfaces mayor/deacon elsewhere.
	_ = townSrc

	for rigName, src := range rigSrcs {
		if src == nil {
			continue
		}
		agentBeads, err := src.ListAgentBeads()
		if err != nil {
			continue
		}
		for id := range agentBeads {
			beadRig, role, name, ok := beads.ParseAgentBeadID(id)
			if !ok {
				continue
			}
			// Skip town-level agents — they don't belong in any rig bucket.
			if beadRig == "" {
				continue
			}
			// Defensive: only seed agents whose parsed rig matches the map
			// key. Mismatches indicate either a mis-indexed source or a
			// cross-rig bead that shouldn't be surfaced here.
			if beadRig != rigName {
				continue
			}
			actor := buildActor(rigName, role, name)
			if actor == "" {
				continue
			}
			entries = append(entries, seeded{
				rig:   rigName,
				actor: actor,
				role:  role,
			})
		}
	}

	if len(entries) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range entries {
		r, ok := m.rigs[e.rig]
		if !ok {
			r = &Rig{
				Name:     e.rig,
				Agents:   make(map[string]*Agent),
				Expanded: true,
			}
			m.rigs[e.rig] = r
		}
		// Preserve existing agents — they already carry live event data
		// that we must not clobber.
		if _, exists := r.Agents[e.actor]; exists {
			continue
		}
		r.Agents[e.actor] = &Agent{
			ID:     e.actor,
			Name:   e.actor,
			Role:   e.role,
			Rig:    e.rig,
			Status: AgentStatusIdle,
			// LastEvent stays nil and LastUpdate stays zero. renderAgent
			// shows the agent with no activity string and no indicator —
			// isAgentActive returns false because Status != working. Once
			// an event arrives for this actor, addEventLocked decorates
			// the existing Agent in place.
		}
	}

	m.updateViewContentLocked()
}

// buildActor reconstructs the event-actor key format used in addEventLocked:
//   - polecat: "<rig>/polecats/<name>"
//   - crew:    "<rig>/crew/<name>"
//   - singletons (witness, refinery): "<rig>/<role>"
//
// Returns "" when the role/name combination doesn't map to a tree-visible
// actor (for example, an unknown role or a named role missing its name).
// Keeping this private means the convention is owned by one place; changing
// it means updating this function and addEventLocked together.
func buildActor(rig, role, name string) string {
	switch role {
	case constants.RolePolecat:
		if name == "" {
			return ""
		}
		return rig + "/polecats/" + name
	case constants.RoleCrew:
		if name == "" {
			return ""
		}
		return rig + "/" + constants.RoleCrew + "/" + name
	case constants.RoleWitness, constants.RoleRefinery:
		return rig + "/" + role
	default:
		return ""
	}
}

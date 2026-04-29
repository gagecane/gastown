package mail

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
)

// isGroupAddress returns true if the address is a @group address.
// Group addresses start with @ and resolve to multiple recipients.
func isGroupAddress(address string) bool {
	return strings.HasPrefix(address, "@")
}

// GroupType represents the type of group address.
type GroupType string

const (
	GroupTypeRig      GroupType = "rig"      // @rig/<rigname> - all agents in a rig
	GroupTypeTown     GroupType = "town"     // @town - all town-level agents
	GroupTypeRole     GroupType = "role"     // @witnesses, @dogs, etc. - all agents of a role
	GroupTypeRigRole  GroupType = "rig-role" // @crew/<rigname>, @polecats/<rigname> - role in a rig
	GroupTypeOverseer GroupType = "overseer" // @overseer - human operator
)

// ParsedGroup represents a parsed @group address.
type ParsedGroup struct {
	Type     GroupType
	RoleType string // witness, crew, polecat, dog, etc.
	Rig      string // rig name for rig-scoped groups
	Original string // original @group string
}

// parseGroupAddress parses a @group address into its components.
// Returns nil if the address is not a valid group address.
//
// Supported patterns:
//   - @rig/<rigname>: All agents in a rig
//   - @town: All town-level agents (mayor, deacon)
//   - @witnesses: All witnesses across rigs
//   - @crew/<rigname>: Crew workers in a specific rig
//   - @polecats/<rigname>: Polecats in a specific rig
//   - @dogs: All Deacon dogs
//   - @overseer: Human operator (special case)
func parseGroupAddress(address string) *ParsedGroup {
	if !isGroupAddress(address) {
		return nil
	}

	// Remove @ prefix
	group := strings.TrimPrefix(address, "@")

	// Special cases that don't require parsing
	switch group {
	case "overseer":
		return &ParsedGroup{Type: GroupTypeOverseer, Original: address}
	case "town":
		return &ParsedGroup{Type: GroupTypeTown, Original: address}
	case "witnesses":
		return &ParsedGroup{Type: GroupTypeRole, RoleType: constants.RoleWitness, Original: address}
	case "dogs":
		return &ParsedGroup{Type: GroupTypeRole, RoleType: "dog", Original: address}
	case "refineries":
		return &ParsedGroup{Type: GroupTypeRole, RoleType: constants.RoleRefinery, Original: address}
	case "deacons":
		return &ParsedGroup{Type: GroupTypeRole, RoleType: constants.RoleDeacon, Original: address}
	}

	// Parse patterns with slashes: @rig/<name>, @crew/<rig>, @polecats/<rig>
	parts := strings.SplitN(group, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil // Invalid format
	}

	prefix, qualifier := parts[0], parts[1]

	switch prefix {
	case "rig":
		return &ParsedGroup{Type: GroupTypeRig, Rig: qualifier, Original: address}
	case constants.RoleCrew:
		return &ParsedGroup{Type: GroupTypeRigRole, RoleType: constants.RoleCrew, Rig: qualifier, Original: address}
	case "polecats":
		return &ParsedGroup{Type: GroupTypeRigRole, RoleType: constants.RolePolecat, Rig: qualifier, Original: address}
	default:
		return nil // Unknown group type
	}
}

// ResolveGroupAddress resolves a @group address to individual recipient addresses.
// Returns the list of resolved addresses and any error.
// This is the public entry point for group resolution.
func (r *Router) ResolveGroupAddress(address string) ([]string, error) {
	group := parseGroupAddress(address)
	if group == nil {
		return nil, fmt.Errorf("invalid group address: %s", address)
	}
	return r.resolveGroup(group)
}

// resolveGroup resolves a @group address to individual recipient addresses.
// Returns the list of resolved addresses and any error.
func (r *Router) resolveGroup(group *ParsedGroup) ([]string, error) {
	if group == nil {
		return nil, errors.New("nil group")
	}

	switch group.Type {
	case GroupTypeOverseer:
		return r.resolveOverseer()
	case GroupTypeTown:
		return r.resolveTownAgents()
	case GroupTypeRole:
		return r.resolveAgentsByRole(group.RoleType, "")
	case GroupTypeRig:
		return r.resolveAgentsByRig(group.Rig)
	case GroupTypeRigRole:
		return r.resolveAgentsByRole(group.RoleType, group.Rig)
	default:
		return nil, fmt.Errorf("unknown group type: %s", group.Type)
	}
}

// resolveOverseer resolves @overseer to the human operator's address.
// Loads the overseer config and returns "overseer" as the address.
func (r *Router) resolveOverseer() ([]string, error) {
	if r.townRoot == "" {
		return nil, errors.New("town root not set, cannot resolve @overseer")
	}

	// Load overseer config to verify it exists
	configPath := config.OverseerConfigPath(r.townRoot)
	_, err := config.LoadOverseerConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolving @overseer: %w", err)
	}

	// Return the overseer address
	return []string{"overseer"}, nil
}

// resolveTownAgents resolves @town to all town-level agents (mayor, deacon).
func (r *Router) resolveTownAgents() ([]string, error) {
	// Town-level agents have rig=null in their description
	agents := r.queryAgents("rig: null")

	var addresses []string
	for _, agent := range agents {
		if addr := agentBeadToAddress(agent); addr != "" {
			addresses = append(addresses, addr)
		}
	}

	return addresses, nil
}

// resolveAgentsByRole resolves agents by their role_type.
// If rig is non-empty, also filters by rig.
func (r *Router) resolveAgentsByRole(roleType, rig string) ([]string, error) {
	// Build query filter
	query := "role_type: " + roleType
	agents := r.queryAgents(query)

	var addresses []string
	for _, agent := range agents {
		// Filter by rig if specified
		if rig != "" {
			// Check if agent's description contains matching rig
			if !strings.Contains(agent.Description, "rig: "+rig) {
				continue
			}
		}
		if addr := agentBeadToAddress(agent); addr != "" {
			addresses = append(addresses, addr)
		}
	}

	return addresses, nil
}

// resolveAgentsByRig resolves @rig/<rigname> to all agents in that rig.
func (r *Router) resolveAgentsByRig(rig string) ([]string, error) {
	// Query for agents with matching rig in description
	query := "rig: " + rig
	agents := r.queryAgents(query)

	var addresses []string
	for _, agent := range agents {
		if addr := agentBeadToAddress(agent); addr != "" {
			addresses = append(addresses, addr)
		}
	}

	return addresses, nil
}

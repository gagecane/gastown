package mail

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

// agentBead represents an agent bead as returned by bd list --label=gt:agent.
type agentBead struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	CreatedBy   string   `json:"created_by"`
	Type        string   `json:"issue_type"`
	Labels      []string `json:"labels"`
}

// agentBeadToAddress converts an agent bead to a mail address.
// Handles multiple ID formats:
//   - hq-mayor → mayor/
//   - hq-deacon → deacon/
//   - gt-gastown-crew-max → gastown/max (legacy)
//   - ppf-pyspark_pipeline_framework-polecat-Toast → pyspark_pipeline_framework/Toast (rig prefix)
func agentBeadToAddress(bead *agentBead) string {
	if bead == nil {
		return ""
	}

	id := bead.ID

	// Handle hq- prefixed IDs (town-level format)
	if strings.HasPrefix(id, "hq-") {
		// Well-known town-level agents
		if id == "hq-mayor" {
			return "mayor/"
		}
		if id == "hq-deacon" {
			return "deacon/"
		}

		// For other hq- agents, fall back to description parsing
		return parseAgentAddressFromDescription(bead.Description)
	}

	// Handle gt- prefixed IDs (legacy format)
	// Also handle rig-prefixed IDs (e.g., ppf-) by extracting rig from description
	var rest string
	if strings.HasPrefix(id, "gt-") {
		rest = strings.TrimPrefix(id, "gt-")
	} else {
		// For rig-prefixed IDs, extract rig and role from description
		return parseRigAgentAddress(bead)
	}

	// Agent bead IDs include the role explicitly: gt-<rig>-<role>[-<name>]
	// Scan from right for known role markers to handle hyphenated rig names.
	parts := strings.Split(rest, "-")

	if len(parts) == 1 {
		// Town-level: gt-mayor, gt-deacon
		return parts[0] + "/"
	}

	// Scan from right for known role markers
	for i := len(parts) - 1; i >= 1; i-- {
		switch parts[i] {
		case constants.RoleWitness, constants.RoleRefinery:
			// Singleton role: rig is everything before the role
			rig := strings.Join(parts[:i], "-")
			return rig + "/" + parts[i]
		case constants.RoleCrew, constants.RolePolecat:
			// Named role: rig is before role, name is after (skip role in address)
			rig := strings.Join(parts[:i], "-")
			if i+1 < len(parts) {
				name := strings.Join(parts[i+1:], "-")
				return rig + "/" + name
			}
			return rig + "/"
		case "dog":
			// Town-level named: gt-dog-alpha
			if i+1 < len(parts) {
				name := strings.Join(parts[i+1:], "-")
				return "dog/" + name
			}
			return "dog/"
		}
	}

	// Fallback: assume first part is rig, rest is role/name
	if len(parts) == 2 {
		return parts[0] + "/" + parts[1]
	}
	return ""
}

// parseRigAgentAddress extracts address from a rig-prefixed agent bead.
// ID format: <prefix>-<rig>-<role>[-<name>]
// Examples:
//   - ppf-pyspark_pipeline_framework-witness → pyspark_pipeline_framework/witness
//   - ppf-pyspark_pipeline_framework-polecat-Toast → pyspark_pipeline_framework/Toast
//   - bd-beads-crew-beavis → beads/beavis
func parseRigAgentAddress(bead *agentBead) string {
	// Parse rig and role_type from description
	var roleType, rig string
	for _, line := range strings.Split(bead.Description, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "role_type:") {
			roleType = strings.TrimSpace(strings.TrimPrefix(line, "role_type:"))
		} else if strings.HasPrefix(line, "rig:") {
			rig = strings.TrimSpace(strings.TrimPrefix(line, "rig:"))
		}
	}

	if rig == "" || rig == "null" || roleType == "" || roleType == "null" {
		// Fallback: parse from bead ID by scanning for known role markers.
		// ID format: <prefix>-<rig>-<role>[-<name>]
		// Known rig-level roles: crew, polecat, witness, refinery
		return parseRigAgentAddressFromID(bead.ID)
	}

	// For singleton roles (witness, refinery), address is rig/role
	if roleType == constants.RoleWitness || roleType == constants.RoleRefinery {
		return rig + "/" + roleType
	}

	// For named roles (crew, polecat), extract name from ID
	// ID pattern: <prefix>-<rig>-<role>-<name>
	// Find the role in the ID and take everything after it as the name
	id := bead.ID
	roleMarker := "-" + roleType + "-"
	if idx := strings.Index(id, roleMarker); idx >= 0 {
		name := id[idx+len(roleMarker):]
		if name != "" {
			return rig + "/" + name
		}
	}

	// Fallback: return rig/roleType (may not be correct for all cases)
	return rig + "/" + roleType
}

// parseRigAgentAddressFromID extracts a mail address from a rig-prefixed bead ID
// when the description metadata is missing. Scans for known role markers in the ID
// to determine the rig name and agent name.
//
// ID format: <prefix>-<rig>-<role>[-<name>]
//
// Singleton roles (witness, refinery) must NOT have a name segment — IDs like
// "bd-beads-witness-extra" are malformed and return "".
//
// Keep role lists in sync with beads.RigLevelRoles and beads.NamedRoles.
func parseRigAgentAddressFromID(id string) string {
	// Singleton roles: no name segment allowed
	singletonRoles := []string{constants.RoleWitness, constants.RoleRefinery}
	// Named roles: require a name segment
	namedRoles := []string{constants.RoleCrew, constants.RolePolecat}

	for _, role := range namedRoles {
		marker := "-" + role + "-"
		if idx := strings.Index(id, marker); idx >= 0 {
			// Everything between prefix- and -role- is the rig name.
			// The prefix ends at the first hyphen: <prefix>-<rig>-...
			// But prefix could be multi-char (bd, gt, ppf), so we find
			// the rig as the substring between the first hyphen and the role marker.
			firstHyphen := strings.Index(id, "-")
			if firstHyphen < 0 || firstHyphen >= idx {
				continue
			}
			rig := id[firstHyphen+1 : idx]
			if rig == "" {
				continue
			}
			name := id[idx+len(marker):]
			if name != "" {
				// Named role (crew, polecat): address is rig/name
				return rig + "/" + name
			}
			// crew/polecat without a name — malformed, skip
			continue
		}
	}

	for _, role := range singletonRoles {
		// Singleton roles match only at end of ID: <prefix>-<rig>-<role>
		// Reject if a name segment follows (e.g. -witness-extra is malformed).
		marker := "-" + role + "-"
		if strings.Contains(id, marker) {
			// Has a name segment after the role — malformed singleton
			continue
		}

		suffix := "-" + role
		if strings.HasSuffix(id, suffix) {
			// Find rig between first hyphen and the suffix
			firstHyphen := strings.Index(id, "-")
			if firstHyphen < 0 {
				continue
			}
			suffixStart := len(id) - len(suffix)
			if firstHyphen >= suffixStart {
				continue
			}
			rig := id[firstHyphen+1 : suffixStart]
			if rig == "" {
				continue
			}
			return rig + "/" + role
		}
	}

	return ""
}

// parseAgentAddressFromDescription extracts agent address from description metadata.
// Looks for "location: X" first (explicit address), then falls back to
// "role_type: X" and "rig: Y" patterns in the description.
func parseAgentAddressFromDescription(desc string) string {
	var roleType, rig, location string

	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "location:") {
			location = strings.TrimSpace(strings.TrimPrefix(line, "location:"))
		} else if strings.HasPrefix(line, "role_type:") {
			roleType = strings.TrimSpace(strings.TrimPrefix(line, "role_type:"))
		} else if strings.HasPrefix(line, "rig:") {
			rig = strings.TrimSpace(strings.TrimPrefix(line, "rig:"))
		}
	}

	// Explicit location takes priority (used by dogs and other agents
	// whose address can't be derived from role_type + rig alone)
	if location != "" && location != "null" {
		return location
	}

	// Handle null values from description
	if rig == "null" || rig == "" {
		rig = ""
	}
	if roleType == "null" || roleType == "" {
		return ""
	}

	// Town-level agents (no rig)
	if rig == "" {
		return roleType + "/"
	}

	// Rig-level agents: rig/name (role_type is the agent name for crew/polecat)
	return rig + "/" + roleType
}

// queryAgents queries agent beads using bd list with description filtering.
// Searches both town-level and rig-level beads to find all agents.
func (r *Router) queryAgents(descContains string) []*agentBead {
	var allAgents []*agentBead

	// Query town-level beads
	townBeadsDir := r.resolveBeadsDir()
	townAgents, err := r.queryAgentsInDir(townBeadsDir, descContains)
	if err != nil {
		// Don't fail yet - rig beads might still have results
		townAgents = nil
	}
	allAgents = append(allAgents, townAgents...)

	// Also query rig-level beads via routes.jsonl
	if r.townRoot != "" {
		routesDir := filepath.Join(r.townRoot, ".beads")
		routes, routeErr := beads.LoadRoutes(routesDir)
		if routeErr == nil {
			for _, route := range routes {
				// Skip hq- routes (town-level, already queried)
				if strings.HasPrefix(route.Prefix, "hq-") {
					continue
				}
				rigBeadsDir := filepath.Join(r.townRoot, route.Path, ".beads")
				rigAgents, rigErr := r.queryAgentsInDir(rigBeadsDir, descContains)
				if rigErr != nil {
					continue // Skip rigs with errors
				}
				allAgents = append(allAgents, rigAgents...)
			}
		}
	}

	// Deduplicate by ID
	seen := make(map[string]bool)
	var unique []*agentBead
	for _, agent := range allAgents {
		if !seen[agent.ID] {
			seen[agent.ID] = true
			unique = append(unique, agent)
		}
	}

	return unique
}

// queryAgentsInDir queries agent beads in a specific beads directory with optional description filtering.
// Queries both the issues and wisps tables, merging results.
func (r *Router) queryAgentsInDir(beadsDir, descContains string) ([]*agentBead, error) {
	args := []string{"list", "--label=gt:agent", "--include-infra", "--json", "--flat", "--limit=0"}

	if descContains != "" {
		args = append(args, "--desc-contains="+descContains)
	}

	ctx, cancel := bdReadCtx()
	defer cancel()

	// Query issues table (backward compat during migration)
	stdout, issuesErr := runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)

	// Also query wisps table for migrated agent beads (best-effort)
	wispCtx, wispCancel := bdReadCtx()
	defer wispCancel()
	wispOut, _ := runBdCommand(wispCtx, []string{"mol", "wisp", "list", "--json"}, filepath.Dir(beadsDir), beadsDir)

	// Merge results: collect agent beads from both sources
	seenIDs := make(map[string]bool)
	var agents []*agentBead

	// Parse wisps first (primary source after migration)
	if len(wispOut) > 0 {
		var wispAgents []*agentBead
		if json.Unmarshal(wispOut, &wispAgents) == nil {
			for _, agent := range wispAgents {
				if isAgentBeadEntry(agent) {
					seenIDs[agent.ID] = true
					agents = append(agents, agent)
				}
			}
		}
	}

	// Then issues (backward compat, skip duplicates)
	if len(stdout) > 0 {
		var issueAgents []*agentBead
		if json.Unmarshal(stdout, &issueAgents) == nil {
			for _, agent := range issueAgents {
				if !seenIDs[agent.ID] {
					agents = append(agents, agent)
				}
			}
		}
	} else if issuesErr != nil && len(agents) == 0 {
		return nil, fmt.Errorf("querying agents in %s: %w", beadsDir, issuesErr)
	}

	// Filter for active agents (closed/deleted agents are inactive)
	var active []*agentBead
	for _, agent := range agents {
		if agent.Status == "open" || agent.Status == "in_progress" || agent.Status == "hooked" || agent.Status == "pinned" {
			active = append(active, agent)
		}
	}

	return active, nil
}

// isAgentBeadEntry checks if an agentBead entry is an actual agent bead.
func isAgentBeadEntry(a *agentBead) bool {
	if a.Type == "agent" {
		return true
	}
	for _, l := range a.Labels {
		if l == "gt:agent" {
			return true
		}
	}
	return false
}

// queryAgentsFromDir queries agent beads from a specific beads directory.
func (r *Router) queryAgentsFromDir(beadsDir string) ([]*agentBead, error) {
	return r.queryAgentsInDir(beadsDir, "")
}

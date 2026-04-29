package mail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

// validateRecipient checks that the recipient identity corresponds to an existing agent.
// Returns an error if the recipient is invalid or doesn't exist.
// Queries agents from town-level beads AND all rig-level beads via routes.jsonl.
func (r *Router) validateRecipient(identity string) error {
	// Overseer is the human operator, not an agent bead
	if identity == "overseer" {
		return nil
	}

	// Well-known town-level singletons always valid
	switch identity {
	case "mayor", "mayor/", "deacon", "deacon/":
		return nil
	}

	// Well-known rig-level singletons (rig/witness, rig/refinery) always
	// valid — these agents are ephemeral and may not have an active session,
	// but mail queues for the next session that starts.
	parts := strings.SplitN(identity, "/", 3)
	if len(parts) == 2 {
		switch parts[1] {
		case "witness", "refinery":
			return nil
		}
	}

	// Query agents from town-level beads
	agents := r.queryAgents("")

	for _, agent := range agents {
		if agentBeadToAddress(agent) == identity {
			return nil // Found matching agent
		}
	}

	// Query agents from rig-level beads via routes.jsonl
	var routeQueryErr error
	if r.townRoot != "" {
		townBeadsDir := filepath.Join(r.townRoot, ".beads")
		routes, err := beads.LoadRoutes(townBeadsDir)
		if err == nil {
			var queryErrors []string
			for _, route := range routes {
				// Skip hq- routes (town-level, already queried)
				if strings.HasPrefix(route.Prefix, "hq-") {
					continue
				}
				rigBeadsDir := filepath.Join(r.townRoot, route.Path, ".beads")
				rigAgents, err := r.queryAgentsFromDir(rigBeadsDir)
				if err != nil {
					queryErrors = append(queryErrors, fmt.Sprintf("%s: %v", route.Path, err))
					continue
				}
				for _, agent := range rigAgents {
					if agentBeadToAddress(agent) == identity {
						return nil // Found matching agent
					}
				}
			}
			if len(queryErrors) > 0 {
				routeQueryErr = fmt.Errorf("no agent found (query errors: %s)", strings.Join(queryErrors, "; "))
			}
		}
	}

	// Fall back to workspace directory validation. Agent beads may be missing
	// (e.g., Dolt DB reset) even though the agent's workspace directory exists.
	if r.townRoot != "" && r.validateAgentWorkspace(identity) {
		return nil
	}

	if routeQueryErr != nil {
		return routeQueryErr
	}

	return fmt.Errorf("no agent found")
}

// validateAgentWorkspace checks if an agent's workspace directory exists on disk.
// Used as a fallback when the agent isn't found in the bead registry.
func (r *Router) validateAgentWorkspace(identity string) bool {
	parts := strings.Split(identity, "/")

	switch len(parts) {
	case 1:
		// Town-level singleton: "mayor", "deacon"
		name := strings.TrimSuffix(parts[0], "/")
		return dirExists(filepath.Join(r.townRoot, name))
	case 2:
		rig, name := parts[0], parts[1]
		// Singleton role: gastown/witness, gastown/refinery
		if dirExists(filepath.Join(r.townRoot, rig, name)) {
			return true
		}
		// Named role (identity normalized away crew/polecats): check both
		for _, role := range []string{"crew", "polecats"} {
			if dirExists(filepath.Join(r.townRoot, rig, role, name)) {
				return true
			}
		}
	case 3:
		// Explicit role paths: rig/crew/<name> or rig/polecats/<name>
		if parts[1] == "crew" || parts[1] == "polecats" {
			return dirExists(filepath.Join(r.townRoot, parts[0], parts[1], parts[2]))
		}
		// Dog addresses: deacon/dogs/<name>
		if dirExists(filepath.Join(r.townRoot, parts[0], parts[1], parts[2])) {
			return true
		}
	}

	return false
}

// dirExists returns true if the path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resolveCrewShorthand expands "crew/name" or "polecats/name" shorthand addresses
// to fully-qualified "rig/name" form by scanning the town filesystem.
//
// When gt agents displays crew workers, it shows them as "crew/bob" (without rig).
// This function enables "gt mail send crew/bob" to work by finding the rig.
//
// Returns the normalized identity if exactly one rig contains the crew member,
// or the original identity unchanged if zero or multiple rigs match (to let
// validation fail with an informative error).
func (r *Router) resolveCrewShorthand(identity string) string {
	if r.townRoot == "" {
		return identity
	}

	parts := strings.Split(identity, "/")
	if len(parts) != 2 {
		return identity
	}

	roleDir, name := parts[0], parts[1]
	// Only handle crew and polecats shorthand (not real rig names)
	if roleDir != constants.RoleCrew && roleDir != "polecats" {
		return identity
	}

	// Check if "crew" or "polecats" is actually a real rig directory
	if fi, err := os.Stat(filepath.Join(r.townRoot, roleDir)); err == nil && fi.IsDir() {
		// It's a real rig, not a shorthand - let normal validation handle it
		return identity
	}

	// Scan rig directories for a crew/polecats member with this name
	entries, err := os.ReadDir(r.townRoot)
	if err != nil {
		return identity
	}

	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rig := entry.Name()
		agentDir := filepath.Join(r.townRoot, rig, roleDir, name)
		if fi, err2 := os.Stat(agentDir); err2 == nil && fi.IsDir() {
			matches = append(matches, rig+"/"+name)
		}
	}

	if len(matches) == 1 {
		return matches[0] // Unambiguous: expand to rig/name
	}

	return identity // Ambiguous or not found: let validation handle it
}

package cmd

import "strings"

// IsCrewTarget checks if target is a crew target pattern.
// Returns the rig name, crew member name, and true if it's a crew target.
// Crew workers, like dogs, are persistent agents that don't consume polecat
// capacity slots — they bypass the scheduler and resolve via direct dispatch.
//
// Patterns:
//   - "<rig>/crew/<name>" -> (rig, name, true) - specific crew member
//   - "crew" -> ("", "", true) - crew in current rig (resolved from env)
func IsCrewTarget(target string) (rigName string, crewName string, isCrew bool) {
	lower := strings.ToLower(target)

	// Bare "crew" shorthand (e.g., "gt sling gt-abc crew")
	if lower == "crew" {
		return "", "", true
	}

	// Path form: "<rig>/crew/<name>"
	parts := strings.Split(target, "/")
	if len(parts) == 3 && strings.ToLower(parts[1]) == "crew" {
		rig := parts[0]
		name := parts[2]
		if rig != "" && name != "" {
			return rig, name, true
		}
	}

	return "", "", false
}

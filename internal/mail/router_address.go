package mail

import (
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
)

// Address type helpers. Mail addresses use several prefix schemes in addition
// to the canonical rig/name identity form. These helpers detect and parse each
// scheme so the router can dispatch correctly.

// isListAddress returns true if the address uses list:name syntax.
func isListAddress(address string) bool {
	return strings.HasPrefix(address, "list:")
}

// parseListName extracts the list name from a list:name address.
func parseListName(address string) string {
	return strings.TrimPrefix(address, "list:")
}

// isQueueAddress returns true if the address uses queue:name syntax.
func isQueueAddress(address string) bool {
	return strings.HasPrefix(address, "queue:")
}

// parseQueueName extracts the queue name from a queue:name address.
func parseQueueName(address string) string {
	return strings.TrimPrefix(address, "queue:")
}

// isAnnounceAddress returns true if the address uses announce:name syntax.
func isAnnounceAddress(address string) bool {
	return strings.HasPrefix(address, "announce:")
}

// parseAnnounceName extracts the announce channel name from an announce:name address.
func parseAnnounceName(address string) string {
	return strings.TrimPrefix(address, "announce:")
}

// isChannelAddress returns true if the address uses channel:name syntax (beads-native channels).
func isChannelAddress(address string) bool {
	return strings.HasPrefix(address, "channel:")
}

// parseChannelName extracts the channel name from a channel:name address.
func parseChannelName(address string) string {
	return strings.TrimPrefix(address, "channel:")
}

// isTownLevelAddress returns true if the address is for a town-level agent or the overseer.
func isTownLevelAddress(address string) bool {
	addr := strings.TrimSuffix(address, "/")
	return addr == constants.RoleMayor || addr == constants.RoleDeacon || addr == "overseer"
}

// isSelfMail returns true if sender and recipient are the same identity.
// Uses AddressToIdentity for canonical normalization (handles crew/, polecats/ paths).
func isSelfMail(from, to string) bool {
	return AddressToIdentity(from) == AddressToIdentity(to)
}

// addressToAgentBeadID converts a mail address to an agent bead ID for DND lookup.
// Returns empty string if the address cannot be converted.
func addressToAgentBeadID(address string) string {
	switch {
	case address == "overseer":
		return "" // Overseer is a human, no agent bead
	case strings.HasPrefix(address, constants.RoleMayor):
		return session.MayorSessionName()
	case strings.HasPrefix(address, constants.RoleDeacon):
		return session.DeaconSessionName()
	}

	parts := strings.SplitN(address, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return ""
	}

	rig := parts[0]
	target := parts[1]

	rigPrefix := session.PrefixFor(rig)

	switch {
	case target == constants.RoleWitness:
		return session.WitnessSessionName(rigPrefix)
	case target == constants.RoleRefinery:
		return session.RefinerySessionName(rigPrefix)
	case strings.HasPrefix(target, "crew/"):
		crewName := strings.TrimPrefix(target, "crew/")
		return session.CrewSessionName(rigPrefix, crewName)
	case strings.HasPrefix(target, "polecats/"):
		pcName := strings.TrimPrefix(target, "polecats/")
		return session.PolecatSessionName(rigPrefix, pcName)
	default:
		return session.PolecatSessionName(rigPrefix, target)
	}
}

// AddressToSessionIDs converts a mail address to possible tmux session IDs.
// Returns multiple candidates since the canonical address format (rig/name)
// doesn't distinguish between crew workers (gt-rig-crew-name) and polecats
// (gt-rig-name). The caller should try each and use the one that exists.
//
// This supersedes the approach in PR #896 which only handled slash-to-dash
// conversion but didn't address the crew/polecat ambiguity.
func AddressToSessionIDs(address string) []string {
	// Overseer address: "overseer" (human operator)
	if address == "overseer" {
		return []string{session.OverseerSessionName()}
	}

	// Mayor address: "mayor/" or "mayor"
	if strings.HasPrefix(address, constants.RoleMayor) {
		return []string{session.MayorSessionName()}
	}

	// Deacon address: "deacon/" or "deacon"
	if strings.HasPrefix(address, constants.RoleDeacon) {
		return []string{session.DeaconSessionName()}
	}

	// Rig-based address: "rig/target" or "rig/crew/name" or "rig/polecats/name"
	parts := strings.SplitN(address, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil
	}

	rig := parts[0]
	target := parts[1]
	rigPrefix := session.PrefixFor(rig)

	// If target already has crew/ or polecats/ prefix, use it directly
	// e.g., "gastown/crew/holden" → "gt-crew-holden"
	if strings.HasPrefix(target, "crew/") {
		crewName := strings.TrimPrefix(target, "crew/")
		return []string{session.CrewSessionName(rigPrefix, crewName)}
	}
	if strings.HasPrefix(target, "polecats/") {
		polecatName := strings.TrimPrefix(target, "polecats/")
		return []string{session.PolecatSessionName(rigPrefix, polecatName)}
	}

	// Special cases that don't need crew variant
	if target == constants.RoleWitness {
		return []string{session.WitnessSessionName(rigPrefix)}
	}
	if target == constants.RoleRefinery {
		return []string{session.RefinerySessionName(rigPrefix)}
	}

	// For normalized addresses like "gastown/holden", try both:
	// 1. Crew format: gt-crew-holden
	// 2. Polecat format: gt-holden
	// Return crew first since crew workers are more commonly missed.
	return []string{
		session.CrewSessionName(rigPrefix, target),    // <prefix>-crew-name
		session.PolecatSessionName(rigPrefix, target), // <prefix>-name
	}
}

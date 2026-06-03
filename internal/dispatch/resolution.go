package dispatch

import (
	"encoding/json"
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
)

// VerifyBeadIDMatch returns an error if the resolved bead ID does not match the
// requested ID when the requested ID looks like a full prefixed ID. This is a
// defensive guard against bd's partial-ID resolver falling through to substring
// matching (gu-yphj): when e.g. "gt-74f" exists (OPEN) but "gt-74fjf" also
// exists (CLOSED), bd show "gt-74f" can return "gt-74fjf" because the exact
// filter matches neither locally and the substring fallback picks the longer
// closed bead. That misrouting blocks dispatch of the legitimate bead.
//
// We consider the input a "full" ID if it has a valid prefix (as detected by
// beads.ExtractPrefix). Partial IDs without a prefix (e.g. "74f") are allowed
// to resolve loosely — that is the documented bd show contract. Full IDs must
// match exactly or we surface a clear error pointing at the collision.
func VerifyBeadIDMatch(requestedID, resolvedID string) error {
	// Partial IDs without a recognized prefix are allowed to resolve loosely.
	if beads.ExtractPrefix(requestedID) == "" {
		return nil
	}
	if resolvedID == "" {
		// bd show returned no ID field (older bd or partial JSON) — can't verify,
		// be permissive rather than break existing callers.
		return nil
	}
	if resolvedID == requestedID {
		return nil
	}
	return fmt.Errorf("bead '%s' not found: bd show resolved to a different bead '%s' (prefix collision — use the exact ID)", requestedID, resolvedID)
}

// ParseBeadInfo decodes the JSON output of `bd show --json` into a BeadInfo.
// bd show --json returns an array (issue + dependents); the first element is the
// requested bead. The resolved ID is verified against the requested ID via
// VerifyBeadIDMatch to guard against prefix-collision misrouting (gu-yphj).
func ParseBeadInfo(beadID string, out []byte) (*BeadInfo, error) {
	if len(out) == 0 {
		return nil, fmt.Errorf("bead '%s' not found", beadID)
	}
	// bd show --json returns an array (issue + dependents), take first element.
	var infos []BeadInfo
	if err := json.Unmarshal(out, &infos); err != nil {
		return nil, fmt.Errorf("parsing bead info: %w", err)
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("bead '%s' not found", beadID)
	}
	if err := VerifyBeadIDMatch(beadID, infos[0].ID); err != nil {
		return nil, err
	}
	return &infos[0], nil
}

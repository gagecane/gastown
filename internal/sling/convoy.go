package sling

import (
	"crypto/rand"
	"encoding/base32"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// ConvoyInfo holds convoy details for an issue's tracking convoy.
type ConvoyInfo struct {
	ID            string // Convoy bead ID (e.g., "hq-cv-abc")
	Owned         bool   // true if convoy has gt:owned label
	MergeStrategy string // "direct", "mr", "local", or "" (default = mr)
	BaseBranch    string // Named relay/base branch the convoy's polecats cut from (gs-9ct #1)
}

// IsOwnedDirect returns true if the convoy is owned with direct merge strategy.
// This is the key check for skipping witness/refinery merge pipeline.
func (c *ConvoyInfo) IsOwnedDirect() bool {
	return c != nil && c.Owned && c.MergeStrategy == "direct"
}

// MergeFromFields extracts the merge strategy from a convoy description using
// the typed ConvoyFields accessor. Returns the strategy string ("direct",
// "mr", "local") or empty string if not set.
func MergeFromFields(description string) string {
	fields := beads.ParseConvoyFields(&beads.Issue{Description: description})
	if fields == nil {
		return ""
	}
	return fields.Merge
}

// BaseFromFields extracts the relay/base branch from a convoy description using
// the typed ConvoyFields accessor. Returns "" if unset. Mirrors MergeFromFields
// (gs-9ct #1).
func BaseFromFields(description string) string {
	fields := beads.ParseConvoyFields(&beads.Issue{Description: description})
	if fields == nil {
		return ""
	}
	return fields.BaseBranch
}

// GenerateShortID generates a short random ID (5 lowercase chars), used to mint
// convoy bead IDs (hq-cv-<id>).
func GenerateShortID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.EncodeToString(b)[:5])
}

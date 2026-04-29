package cmd

import (
	"crypto/rand"
	"io"
	"strings"

	"github.com/steveyegge/gastown/internal/session"
)

var convoyIDEntropy io.Reader = rand.Reader

// generateShortID generates a collision-resistant convoy ID suffix using base36.
// 5 chars of base36 gives ~60M possible values (36^5 = 60,466,176).
// Birthday paradox: ~1% collision at ~1,100 IDs — safe for convoy volumes. (#2063)
func generateShortID() string {
	return generateShortIDFromReader(convoyIDEntropy)
}

func generateShortIDFromReader(r io.Reader) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 5)
	_, _ = io.ReadFull(r, b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// looksLikeIssueID checks if a string looks like a beads issue ID.
// Issue IDs have the format: prefix-id (e.g., gt-abc, bd-xyz, hq-123).
func looksLikeIssueID(s string) bool {
	// Check registry prefixes and legacy fallbacks via centralized helper
	if session.HasKnownPrefix(s) {
		return true
	}
	// Pattern check: 2-3 lowercase letters followed by hyphen.
	// Covers unregistered short rig prefixes (e.g., nx, rpk).
	// Longer prefixes (4+ chars like nrpk) are caught by HasKnownPrefix
	// via the registry — no need to heuristic-match them here.
	hyphenIdx := strings.Index(s, "-")
	if hyphenIdx >= 2 && hyphenIdx <= 3 && len(s) > hyphenIdx+1 {
		prefix := s[:hyphenIdx]
		for _, c := range prefix {
			if c < 'a' || c > 'z' {
				return false
			}
		}
		return true
	}
	return false
}

// isValidBeadID checks that a string is safe for SQL interpolation in dep queries.
// Bead IDs contain only alphanumeric chars, hyphens, dots, and underscores.
func isValidBeadID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}

// Complexity gate for autonomous merge conflict resolution.
//
// Phase 4 (gu-g5gh). The security design (cv-2s6tq/security.md §"Option B")
// recommends only resolving "simple" conflicts autonomously and escalating
// complex or security-sensitive ones to a human. This file is the pure
// policy that decides which bucket a given conflict falls into.
//
// The evaluator is intentionally side-effect free: it consumes a list of
// conflicted files (and optional hunk counts) plus the configured
// restricted-path list, and returns a ComplexityVerdict. All git/IO
// happens at the call sites — we want this testable in isolation and
// easy to reason about in security review.
//
// Default thresholds come from the security design recommendation:
//   - ≤3 files conflicted
//   - ≤10 conflict hunks total
//   - no conflicts in: internal/auth/, internal/secrets/, *.sh, Makefile,
//     go.mod, go.sum, .github/, scripts/
//
// Operators can override the path list via UpstreamSyncConfig.RestrictedPaths.
//
// Design context: .designs/cv-2s6tq/security.md §"Option B".
package upstreamsync

import (
	"path/filepath"
	"strings"
)

// ComplexityClass is the bucket a conflict falls into after evaluation.
type ComplexityClass string

const (
	// ComplexityResolvable — conflict is simple enough to dispatch a
	// polecat for autonomous resolution.
	ComplexityResolvable ComplexityClass = "resolvable"

	// ComplexityRestrictedEscalate — at least one conflict touches a
	// security-sensitive path; must escalate to human review even if
	// the file/hunk count is small.
	ComplexityRestrictedEscalate ComplexityClass = "restricted-escalate"

	// ComplexityTooComplexEscalate — conflict exceeds the file or hunk
	// threshold; not safe for autonomous resolution.
	ComplexityTooComplexEscalate ComplexityClass = "too-complex-escalate"
)

// ComplexityVerdict is the output of EvaluateComplexity. Reason carries
// the human-readable explanation for the chosen class — operators see
// it on the state bead and in `gt upstream history`.
type ComplexityVerdict struct {
	// Class is the bucket the conflict falls into.
	Class ComplexityClass

	// Reason is a short stable string describing why this class fired.
	// Empty when Class == Resolvable.
	Reason string

	// RestrictedFiles lists conflicted files that matched a restricted
	// path pattern. Populated even when Class != RestrictedEscalate so
	// observability dashboards can flag near-misses.
	RestrictedFiles []string

	// FileCount is the total number of conflicted files evaluated.
	FileCount int

	// HunkCount is the total number of conflict hunks evaluated. May be
	// 0 if the caller did not supply per-file hunk counts.
	HunkCount int
}

// ComplexityPolicy tunes the evaluator. The zero value uses the
// security-design defaults; callers usually pass the configured policy
// once and reuse it.
type ComplexityPolicy struct {
	// MaxFiles is the file-count ceiling for autonomous resolution.
	// Default: 3 (security-design recommendation).
	MaxFiles int

	// MaxHunks is the hunk-count ceiling for autonomous resolution.
	// Default: 10 (security-design recommendation).
	// HunkCount 0 from a caller that did not parse hunks is treated as
	// "skip hunk check" — file count alone governs.
	MaxHunks int

	// RestrictedPaths is the list of glob patterns that, when matched,
	// force escalation regardless of file/hunk count. Patterns are
	// matched against forward-slash paths via filepath.Match plus
	// directory-prefix and basename heuristics; see matchesRestricted.
	//
	// Defaults from DefaultRestrictedPaths if nil/empty.
	RestrictedPaths []string
}

// DefaultRestrictedPaths is the security-design recommended set of
// path patterns where autonomous conflict resolution is forbidden.
// Operators can extend this via UpstreamSyncConfig.RestrictedPaths.
//
// The list is ordered most-specific first; matching short-circuits on
// the first hit so logging is deterministic.
func DefaultRestrictedPaths() []string {
	return []string{
		"internal/auth/",
		"internal/secrets/",
		".github/",
		"scripts/",
		"go.mod",
		"go.sum",
		"Makefile",
		"*.sh",
	}
}

// DefaultComplexityPolicy returns the evaluator's default tuning.
func DefaultComplexityPolicy() ComplexityPolicy {
	return ComplexityPolicy{
		MaxFiles:        3,
		MaxHunks:        10,
		RestrictedPaths: DefaultRestrictedPaths(),
	}
}

// resolveDefaults fills in zero-valued fields with security-design
// defaults so callers can pass a partially-zero policy and get sensible
// behavior.
func (p ComplexityPolicy) resolveDefaults() ComplexityPolicy {
	out := p
	if out.MaxFiles <= 0 {
		out.MaxFiles = 3
	}
	if out.MaxHunks <= 0 {
		out.MaxHunks = 10
	}
	if len(out.RestrictedPaths) == 0 {
		out.RestrictedPaths = DefaultRestrictedPaths()
	}
	return out
}

// EvaluateComplexity classifies a conflict given the conflicted file
// list, total hunk count (0 = unknown), and policy.
//
// Evaluation order matters for security: restricted-path check fires
// FIRST, before file/hunk thresholds. This ensures that even a single
// conflicted file in a restricted path triggers escalation regardless
// of whether the overall conflict is "small."
//
// Empty conflictedFiles returns Resolvable with FileCount=0 — the
// caller should not normally invoke EvaluateComplexity for clean merges,
// but the helper degrades gracefully rather than panicking.
func EvaluateComplexity(conflictedFiles []string, totalHunks int, policy ComplexityPolicy) ComplexityVerdict {
	policy = policy.resolveDefaults()

	verdict := ComplexityVerdict{
		FileCount: len(conflictedFiles),
		HunkCount: totalHunks,
	}

	// Check 1: restricted paths (security-first ordering).
	var restricted []string
	for _, f := range conflictedFiles {
		if matchesRestricted(f, policy.RestrictedPaths) {
			restricted = append(restricted, f)
		}
	}
	verdict.RestrictedFiles = restricted

	if len(restricted) > 0 {
		verdict.Class = ComplexityRestrictedEscalate
		verdict.Reason = formatRestrictedReason(restricted)
		return verdict
	}

	// Check 2: file count ceiling.
	if verdict.FileCount > policy.MaxFiles {
		verdict.Class = ComplexityTooComplexEscalate
		verdict.Reason = formatFileCountReason(verdict.FileCount, policy.MaxFiles)
		return verdict
	}

	// Check 3: hunk count ceiling. totalHunks == 0 means "unknown" —
	// callers that didn't parse hunks fall back to file-count only.
	if totalHunks > 0 && totalHunks > policy.MaxHunks {
		verdict.Class = ComplexityTooComplexEscalate
		verdict.Reason = formatHunkCountReason(totalHunks, policy.MaxHunks)
		return verdict
	}

	verdict.Class = ComplexityResolvable
	return verdict
}

// matchesRestricted reports whether path matches any pattern in the
// restricted list. Three matching modes are supported, in order:
//
//  1. Directory prefix: pattern ending in "/" matches if path starts
//     with the pattern (e.g., "internal/auth/" matches
//     "internal/auth/foo.go").
//  2. Glob: filepath.Match against both the full path and the basename
//     (e.g., "*.sh" matches "scripts/build.sh" via basename).
//  3. Exact match: pattern with no wildcard and no trailing slash must
//     match path or path basename exactly (e.g., "go.mod").
//
// Path is normalized to forward slashes before matching so Windows-style
// separators in upstream files don't smuggle past the gate.
func matchesRestricted(path string, patterns []string) bool {
	if path == "" {
		return false
	}
	norm := filepath.ToSlash(path)
	base := filepath.Base(norm)

	for _, pat := range patterns {
		if pat == "" {
			continue
		}
		p := filepath.ToSlash(pat)

		// Directory prefix.
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(norm, p) {
				return true
			}
			continue
		}

		// Glob (any wildcard char) — try full path then basename.
		if strings.ContainsAny(p, "*?[") {
			if ok, _ := filepath.Match(p, norm); ok {
				return true
			}
			if ok, _ := filepath.Match(p, base); ok {
				return true
			}
			continue
		}

		// Exact match against full path or basename.
		if norm == p || base == p {
			return true
		}
	}
	return false
}

// formatRestrictedReason returns a stable human-readable string.
// Restricted file lists are bounded in length to keep state-bead
// metadata small; we show up to 3 names then a count.
func formatRestrictedReason(files []string) string {
	const max = 3
	if len(files) <= max {
		return "restricted-path conflict in " + strings.Join(files, ", ")
	}
	head := strings.Join(files[:max], ", ")
	rest := len(files) - max
	if rest == 1 {
		return "restricted-path conflict in " + head + " and 1 more"
	}
	return "restricted-path conflict in " + head + " and " + itoa(rest) + " more"
}

func formatFileCountReason(got, max int) string {
	return "too many conflicted files: " + itoa(got) + " > " + itoa(max)
}

func formatHunkCountReason(got, max int) string {
	return "too many conflict hunks: " + itoa(got) + " > " + itoa(max)
}

// itoa is a tiny int→string helper to avoid pulling in fmt for two
// numbers (keeps this file's allocation footprint trivial).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

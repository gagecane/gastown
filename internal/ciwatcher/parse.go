package ciwatcher

import "regexp"

// beadIDPattern matches a Gas Town bead ID in `(prefix-suffix)` form.
// Prefix is one to six lowercase letters, suffix is at least one alphanum or
// dot/dash to allow subtask IDs (e.g. gu-aei.1, hq-leg-quf7s). This is the
// same shape that the refinery's parseBranchName accepts; we keep it tight
// rather than matching arbitrary parens to avoid grabbing things like
// "(see #1234)".
var beadIDPattern = regexp.MustCompile(`\(([a-z]{1,6}-[a-z0-9][a-z0-9.\-]*)\)`)

// ExtractBeadID returns the bead ID embedded in a commit subject, or empty
// string when none is present. Gas Town's commit convention places the
// responsible bead in trailing parens, e.g.:
//
//	feat(witness): add foo bar (gu-7f0v)
//	docs(design): add UX analysis for heartbeat liveness scheme (gu-leg-xtwu2)
//
// When multiple matches appear (e.g. a fix commit referencing both the bug
// and a related bead), we return the LAST occurrence — the trailing parens
// are by convention the bead that landed the change. Earlier matches are
// usually contextual references inside the subject line.
func ExtractBeadID(subject string) string {
	matches := beadIDPattern.FindAllStringSubmatch(subject, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

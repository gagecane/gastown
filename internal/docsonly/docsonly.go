// Package docsonly classifies a changed-file set as documentation-only.
//
// The code-quality audit formula (mol-polecat-work code-quality legs) fans out
// ~7 legs per cycle, each committing a single report under .quality/**.md.
// Without this classification both `gt done` and the refinery run the full
// lint/vet/`go test ./...` gate suite on those docs-only changes — ~14
// redundant full-suite runs per cycle that clog the polecat pool and the
// single-threaded refinery for zero code risk (gs-2c9). Go gates (build, vet,
// test, gofmt, golangci-lint) only inspect .go sources, so a diff that touches
// only docs makes every gate a no-op; callers use IsDocsOnly to skip them.
package docsonly

import "strings"

// IsDocsOnly reports whether every path in files is documentation: a Markdown
// file (*.md, case-insensitive) or any file under a .quality/ directory.
//
// Returns false for an empty set — "no changed files" is not a positive
// docs-only signal and must never unlock a gate skip.
func IsDocsOnly(files []string) bool {
	matched := 0
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !isDocPath(f) {
			return false
		}
		matched++
	}
	return matched > 0
}

// isDocPath reports whether a single path is a documentation file under the
// gs-2c9 heuristic: a Markdown file, or anything inside a .quality/ directory
// (at repo root or nested).
func isDocPath(f string) bool {
	if strings.HasSuffix(strings.ToLower(f), ".md") {
		return true
	}
	if strings.HasPrefix(f, ".quality/") || strings.Contains(f, "/.quality/") {
		return true
	}
	return false
}

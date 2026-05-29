// Package polecat: stash policy.
//
// Polecat reaper auto-drop heuristic for environmental-only stashes (gu-6ctd).
//
// Background: when a polecat finishes work, build tooling (Brazil, vitest,
// vendor-package builds) often writes paths the polecat reflexively adds to
// .gitignore. If the polecat dies with a stash holding only those drift
// changes, the reaper today flags the polecat NEEDS_RECOVERY and the worktree
// becomes unreusable until a Mayor manually inspects and drops the stash.
//
// This package classifies a stash as "environmental-only" when every changed
// path matches a town-level allowlist of build-output and IDE-state patterns.
// Callers (the polecat reaper / witness reconcile path) can then auto-drop
// the stash safely. If any path falls outside the allowlist, the stash is
// treated as real WIP and the existing recovery path runs unchanged.
package polecat

import (
	"path"
	"strings"
)

// EnvironmentalStashAllowlist enumerates the path patterns whose changes are
// always safe to discard from a stash. Patterns are evaluated against forward-
// slash-normalized paths.
//
// Two pattern shapes are supported:
//
//   - Exact match (e.g. ".gitignore", "package-lock.json"): the full path must
//     equal the pattern, OR the path's last segment must equal the pattern (so
//     `apps/web/.gitignore` matches `.gitignore`).
//   - Directory prefix (e.g. "build/", "node_modules/"): the path must contain
//     the pattern as a path component prefix.
//
// Keep this conservative — additions here trade auto-recovery throughput for
// the risk of silently dropping work. Anything that can plausibly contain
// non-environmental edits MUST stay off the allowlist.
var EnvironmentalStashAllowlist = []string{
	".gitignore",       // any change is environmental drift
	"package-lock.json", // npm rebuild output
	"yarn.lock",        // yarn rebuild output
	"pnpm-lock.yaml",   // pnpm rebuild output
	"build/",           // brazil build symlink target
	"dist/",            // generic build output
	".runtime/",        // gas town runtime state
	".claude/",         // claude code session state
	"__pycache__/",     // python bytecode
	".pytest_cache/",   // pytest cache
	".mypy_cache/",     // mypy cache
	"node_modules/",    // js dependency cache
	".vscode/",         // editor state
	".idea/",           // jetbrains editor state
}

// IsEnvironmentalPath reports whether the given path matches any pattern in
// the EnvironmentalStashAllowlist. The path is normalized to forward slashes
// before evaluation. Empty paths return false.
func IsEnvironmentalPath(p string) bool {
	return matchEnvironmentalPath(p, EnvironmentalStashAllowlist)
}

// IsEnvironmentalOnlyStash reports whether every path in `paths` is environmental.
// An empty paths slice returns false — a stash with no changed paths is anomalous
// (e.g. lookup failed) and should fall through to the normal recovery path rather
// than be auto-dropped on no evidence.
func IsEnvironmentalOnlyStash(paths []string) bool {
	return IsEnvironmentalOnlyStashWithAllowlist(paths, EnvironmentalStashAllowlist)
}

// IsEnvironmentalOnlyStashWithAllowlist is the testable variant that takes the
// allowlist explicitly. Callers should normally use IsEnvironmentalOnlyStash.
func IsEnvironmentalOnlyStashWithAllowlist(paths []string, allowlist []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if !matchEnvironmentalPath(p, allowlist) {
			return false
		}
	}
	return true
}

// matchEnvironmentalPath returns true when `p` matches any pattern in `allowlist`.
//
// Behavior:
//   - Empty paths return false.
//   - Paths are normalized: backslashes converted to forward slashes, leading
//     "./" stripped.
//   - Patterns ending in "/" match if the path has the pattern as a path
//     prefix or contains it as a "/<pattern>" segment.
//   - Otherwise the pattern matches if the full path equals the pattern OR the
//     last path segment equals the pattern. The latter is critical for nested
//     packages: `apps/web/.gitignore` should match the `.gitignore` pattern.
func matchEnvironmentalPath(p string, allowlist []string) bool {
	if p == "" {
		return false
	}
	norm := strings.ReplaceAll(p, "\\", "/")
	norm = strings.TrimPrefix(norm, "./")
	if norm == "" {
		return false
	}

	last := path.Base(norm)
	for _, pattern := range allowlist {
		if pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "/") {
			// Directory prefix: match when path is `<pattern>...` or `.../<pattern>...`.
			if strings.HasPrefix(norm, pattern) || strings.Contains(norm, "/"+pattern) {
				return true
			}
			continue
		}
		// Exact-or-basename match.
		if norm == pattern || last == pattern {
			return true
		}
	}
	return false
}

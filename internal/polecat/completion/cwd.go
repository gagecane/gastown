package completion

import (
	"os"
	"path/filepath"
	"strings"
)

// statExists reports whether a path exists on disk. Swappable in tests so the
// cwd-resolution logic can be exercised without a real filesystem layout.
var statExists = func(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ResolveWorktreeCwd normalizes the working directory `gt done` operates on
// (gu-nid89.12.1 extraction from runDone). It handles two distinct cases that
// previously sat inline as ~40 lines of nested filesystem probing:
//
//  1. cwd is NOT a polecat worktree (shell-alias / Claude-Code CWD reset):
//     `cd ~/gt && gt`, or Claude Code resetting the shell CWD to mayor/rig,
//     leaves cwd pointing at the town root or a sibling path rather than the
//     polecat's clone. Reconstruct the real worktree from GT_POLECAT (or
//     GT_CREW) + rig, preferring the nested rig clone, then the bare clone.
//
//  2. cwd IS a polecat worktree but a subdirectory of it (e.g. beads-ide/
//     inside the repo): beads.ResolveBeadsDir only looks at cwd/.beads, not
//     parent dirs, so walk up to the git repo root before use — stopping at
//     the filesystem root or once we leave the polecats area.
//
// envPolecat is GT_POLECAT and envCrew is GT_CREW (pass "" when unset); they
// are read up front by the caller so this function stays pure modulo the
// filesystem stats and is unit-testable in isolation. When cwd is unavailable
// (deleted worktree) it is returned unchanged.
//
// Mirrors the lines previously inlined at done.go:461–498.
func ResolveWorktreeCwd(cwd string, cwdAvailable bool, townRoot, rigName, envPolecat, envCrew string) string {
	if !cwdAvailable {
		return cwd
	}

	cwdIsPolecatWorktree := strings.Contains(cwd, "/polecats/")

	if !cwdIsPolecatWorktree {
		if envPolecat != "" && rigName != "" {
			polecatClone := filepath.Join(townRoot, rigName, "polecats", envPolecat, rigName)
			if statExists(polecatClone) {
				return polecatClone
			}
			polecatClone = filepath.Join(townRoot, rigName, "polecats", envPolecat)
			if statExists(filepath.Join(polecatClone, ".git")) {
				return polecatClone
			}
		} else if envCrew != "" && rigName != "" {
			crewClone := filepath.Join(townRoot, rigName, "crew", envCrew)
			if statExists(crewClone) {
				return crewClone
			}
		}
		return cwd
	}

	// Subdirectory normalization: walk up to the git repo root, but never leave
	// the polecats area.
	candidate := cwd
	for {
		if statExists(filepath.Join(candidate, ".git")) {
			return candidate
		}
		parent := filepath.Dir(candidate)
		if parent == candidate || !strings.Contains(parent, "/polecats/") {
			return cwd // hit filesystem root or left polecats area
		}
		candidate = parent
	}
}

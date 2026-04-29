package witness

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// isRegisteredPolecatWorktree reports whether the expected polecat worktree
// paths under <townRoot>/<rigName>/polecats/<polecatName>/ appear as
// registered worktrees in the rig's main git repository.
//
// This is a belt-and-suspenders guard against false-positive orphan detection.
// A plain os.Stat on the polecat directory can fail to match the true on-disk
// state for several reasons:
//
//   - The polecats directory or any ancestor is a symlink whose target lives
//     outside the expected prefix (e.g. the rig lives at
//     /workplace/user/Rig/... but the canonical git path is
//     /home/user/gt/rig/...).
//   - os.Stat returns an error other than ErrNotExist that the caller
//     incorrectly treats as "missing" (not the case today, but defensive).
//   - Filesystem layout uses bind mounts or overlay mounts that change the
//     effective path relative to townRoot.
//
// In all of these cases the polecat's worktree is perfectly healthy from
// git's perspective: `git worktree list` reports it as a live worktree. By
// consulting git's view of the world (and resolving symlinks on both sides
// before comparison), we avoid resetting beads whose polecat is actually
// alive and well.
//
// Returns true if a matching registered worktree is found. Returns false
// for any failure (git missing, realpath failing, no match) — callers MUST
// continue with their existing existence checks and treat a false result
// as "inconclusive, fall back to prior logic", not as proof of absence.
func isRegisteredPolecatWorktree(townRoot, rigName, polecatName string) bool {
	if townRoot == "" || rigName == "" || polecatName == "" {
		return false
	}

	// Candidate on-disk worktree locations. Both the new layout
	// (polecats/<name>/<rigname>/) and the legacy flat layout
	// (polecats/<name>/) are considered legitimate.
	candidates := []string{
		filepath.Join(townRoot, rigName, constants.DirPolecats, polecatName, rigName),
		filepath.Join(townRoot, rigName, constants.DirPolecats, polecatName),
	}

	// Resolve symlinks for each candidate; keep the original path as a
	// fallback so that non-existent candidates are still considered when
	// git happens to know about a matching absolute path.
	resolvedCandidates := make(map[string]struct{}, len(candidates)*2)
	for _, c := range candidates {
		resolvedCandidates[filepath.Clean(c)] = struct{}{}
		if real, err := filepath.EvalSymlinks(c); err == nil {
			resolvedCandidates[filepath.Clean(real)] = struct{}{}
		}
	}

	// Query git for its registered worktrees. We query from the rig's
	// mayor/rig clone because that is the main working tree from which
	// all polecat worktrees are created (see internal/polecat/session_manager.go).
	mayorRig := filepath.Join(townRoot, rigName, constants.DirMayor, constants.DirRig)
	paths, err := gitWorktreePaths(mayorRig)
	if err != nil || len(paths) == 0 {
		return false
	}

	for _, p := range paths {
		clean := filepath.Clean(p)
		if _, ok := resolvedCandidates[clean]; ok {
			return true
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			if _, ok := resolvedCandidates[filepath.Clean(real)]; ok {
				return true
			}
		}
	}

	return false
}

// gitWorktreePaths returns the list of worktree paths registered in the
// repository at repoPath. It shells out to `git worktree list --porcelain`
// rather than using internal/git.Git to avoid a package dependency cycle
// and to keep the parsing narrow: we only need the worktree paths, not
// branches or commits.
//
// gitWorktreePathsRunner is a package-level variable so tests can stub the
// command execution.
var gitWorktreePathsRunner = runGitWorktreeList

func gitWorktreePaths(repoPath string) ([]string, error) {
	return gitWorktreePathsRunner(repoPath)
}

func runGitWorktreeList(repoPath string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		// Porcelain format: each worktree stanza begins with `worktree <path>`
		// followed by HEAD/branch/bare lines and a blank separator.
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimPrefix(line, "worktree "))
		}
	}
	return paths, nil
}

package completion

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
)

// ResolveRigName derives the rig name for a gt done invocation (gs-bn1
// extraction from runDone). The cwd-relative path is the primary source —
// findCurrentRig can't be used because it calls os.Getwd(), which fails on a
// deleted worktree, so we derive the rig from the already-resolved cwd instead.
//
// envRig is GT_RIG (pass "" when unset). When set it WINS over the cwd-derived
// name: Claude Code resets the shell cwd (e.g. to mayor/rig), which makes the
// cwd-derived rig wrong (e.g. "mayor" instead of "vets"). GT_RIG is injected
// reliably for polecats via session env.
//
// Returns an error when neither source yields a rig name (the working
// directory may be deleted and GT_RIG unset).
//
// Mirrors the lines previously inlined at done.go:342–363.
func ResolveRigName(townRoot, cwd, envRig string) (string, error) {
	var rigName string
	if cwd != "" {
		relPath, err := filepath.Rel(townRoot, cwd)
		if err == nil {
			parts := strings.Split(relPath, string(filepath.Separator))
			if len(parts) > 0 && parts[0] != "" && parts[0] != "." {
				rigName = parts[0]
			}
		}
	}
	// Prefer GT_RIG over cwd-derived rig name when available.
	if envRig != "" {
		rigName = envRig
	}
	if rigName == "" {
		return "", fmt.Errorf("cannot determine current rig (working directory may be deleted)")
	}
	return rigName, nil
}

// ResolveBranch determines the polecat's working branch for gt done, applying
// the env-var fallbacks and the detached-HEAD guard (gs-bn1 extraction from
// runDone). envBranch is GT_BRANCH and envPolecat is GT_POLECAT (pass "" when
// unset); both are read up front by the caller so this function stays pure
// modulo the git queries and is unit-testable in isolation.
//
// Resolution order:
//   - When the working directory is gone (!cwdAvailable), GT_BRANCH is the only
//     source. The mayor-clone fallback used for git ops in that case sits on
//     main/master, so calling g.CurrentBranch() would misreport the branch —
//     hence GT_BRANCH or a hard error.
//   - Otherwise g.CurrentBranch(), falling back to polecat/<GT_POLECAT> when
//     git can't report a branch.
//
// gu-ge1s detached-HEAD guard: CurrentBranch() returns the literal "HEAD" in
// detached state. Left unguarded, "HEAD" flows downstream and produces
// refs/heads/HEAD pollution on origin and MR beads the refinery can't process.
// We salvage a named branch from GT_BRANCH or GT_POLECAT when possible, else
// fail with an actionable message. A final belt-and-suspenders check refuses to
// return the literal "HEAD" no matter how it was assigned.
//
// Mirrors the lines previously inlined at done.go:422–486.
func ResolveBranch(g *git.Git, cwdAvailable bool, envBranch, envPolecat string) (string, error) {
	// Get current branch - try env var first if cwd is gone.
	var branch string
	if !cwdAvailable {
		branch = envBranch
	}
	// CRITICAL: Only call g.CurrentBranch() when the cwd-based git is in use.
	// When cwdAvailable is false the caller falls back to the mayor clone, which
	// is on main/master — NOT the polecat branch — so CurrentBranch() there
	// would incorrectly return main/master.
	if branch == "" {
		if !cwdAvailable {
			// No GT_BRANCH and using the mayor clone — can't determine branch.
			// Session stays alive (persistent polecat model); Witness recovers.
			return "", fmt.Errorf("cannot determine branch: GT_BRANCH not set and working directory unavailable")
		}
		var err error
		branch, err = g.CurrentBranch()
		if err != nil {
			// Last resort: extract from polecat name (polecat/<name>-<suffix>).
			if envPolecat != "" {
				branch = fmt.Sprintf("polecat/%s", envPolecat)
				style.PrintWarning("could not get branch from git, using fallback: %s", branch)
			} else {
				return "", fmt.Errorf("getting current branch: %w", err)
			}
		}
	}

	// gu-ge1s: Detached-HEAD guard. Prefer a salvageable polecat branch;
	// otherwise fail with an actionable message. GT_BRANCH wins here because it
	// records the branch the polecat was provisioned on even if a later checkout
	// detached HEAD.
	if branch == "" || branch == "HEAD" {
		if cwdAvailable {
			if detached, detErr := g.IsDetachedHEAD(); detErr == nil && detached {
				if envBranch != "" && envBranch != "HEAD" {
					style.PrintWarning("HEAD is detached; using GT_BRANCH=%s from environment", envBranch)
					branch = envBranch
				} else if envPolecat != "" {
					fallback := fmt.Sprintf("polecat/%s", envPolecat)
					style.PrintWarning("HEAD is detached and GT_BRANCH is unset; falling back to %s (no-op push if branch missing)", fallback)
					branch = fallback
				} else {
					return "", fmt.Errorf("cannot submit from detached HEAD: no named branch to push\n" +
						"Create a branch first (git checkout -b <name>) or run `gt polecat nuke` to terminate this worktree")
				}
			}
		}
	}
	// Belt-and-suspenders: never propagate the literal "HEAD" past this point,
	// even if some fallback above accidentally assigned it.
	if branch == "HEAD" {
		return "", fmt.Errorf("refusing to proceed with branch=%q (detached HEAD); create a named branch or run `gt polecat nuke`", branch)
	}
	return branch, nil
}

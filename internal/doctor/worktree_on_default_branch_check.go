package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// WorktreeOnDefaultBranchCheck detects registered worktrees whose HEAD is
// pointing at the rig's default branch (e.g. main, mainline, master) from a
// location where that is NOT expected.
//
// Background / motivation: See gu-f35z / gt-ef38s. On 2026-04-27 a
// deacon/alpha/talontriage worktree had its HEAD left pointing at
// refs/heads/mainline. Git's default receive.denyCurrentBranch=refuse
// caused every polecat push to mainline to be rejected for hours, until
// a human manually edited the worktree HEAD ref file. This check is the
// detection layer so the incident is caught early the next time a
// worktree ends up on the default branch outside of refinery/rig/.
//
// Allowed locations (ignored by this check):
//   - <rig>/refinery/rig/   - refinery legitimately sits on the default
//     branch; that's how it merges polecat branches.
//   - <rig>/mayor/rig/      - mayor is a full clone (not a bare-repo
//     worktree) and is intentionally on the default branch. It won't
//     appear in the bare repo's worktree list, but we guard it by path
//     prefix anyway in case the repo topology changes.
//
// Any other worktree (crew/, polecats/, deacon/dogs/, ad-hoc manual
// worktrees, etc.) with HEAD on the default branch is a warning — it
// will block refinery/mayor pushes to that branch.
type WorktreeOnDefaultBranchCheck struct {
	BaseCheck
}

// NewWorktreeOnDefaultBranchCheck creates a new worktree-on-default-branch check.
func NewWorktreeOnDefaultBranchCheck() *WorktreeOnDefaultBranchCheck {
	return &WorktreeOnDefaultBranchCheck{
		BaseCheck: BaseCheck{
			CheckName:        "worktree-on-default-branch",
			CheckDescription: "Detect worktrees on the rig default branch (blocks pushes)",
			CheckCategory:    CategoryCleanup,
		},
	}
}

// Run scans all rigs' bare repos for worktrees checked out to the default
// branch outside refinery/rig/ (or mayor/rig/ by path).
func (c *WorktreeOnDefaultBranchCheck) Run(ctx *CheckContext) *CheckResult {
	var offenders []string
	var scanned int

	entries, err := os.ReadDir(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Could not scan town root",
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "mayor" || name == "deacon" || strings.HasPrefix(name, ".") {
			continue
		}

		rigPath := filepath.Join(ctx.TownRoot, name)
		bareRepoPath := filepath.Join(rigPath, ".repo.git")
		if info, err := os.Stat(bareRepoPath); err != nil || !info.IsDir() {
			continue
		}

		// Resolve the rig's default branch.
		// Prefer rig config, fall back to git remote HEAD detection, then "main".
		defaultBranch := resolveRigDefaultBranch(rigPath)
		if defaultBranch == "" {
			continue
		}

		bareGit := git.NewGitWithDir(bareRepoPath, "")
		worktrees, err := bareGit.WorktreeList()
		if err != nil {
			continue
		}
		scanned++

		for _, wt := range worktrees {
			if wt.Branch != defaultBranch {
				continue
			}
			// Ignore the bare repo itself (it has no working tree but
			// sometimes shows up with a HEAD branch).
			if wt.Path == bareRepoPath || wt.Path == "" {
				continue
			}
			if isAllowedDefaultBranchWorktree(rigPath, wt.Path) {
				continue
			}
			offenders = append(offenders, fmt.Sprintf(
				"%s: worktree at %s is on branch %q (will block pushes)",
				name, wt.Path, defaultBranch))
		}
	}

	if len(offenders) == 0 {
		if scanned == 0 {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusOK,
				Message: "No rigs with bare repos found",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("Scanned %d rig(s); no worktrees blocking default-branch pushes", scanned),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d worktree(s) on rig default branch (will block pushes)", len(offenders)),
		Details: offenders,
		FixHint: "Either remove the worktree (git worktree remove <path>) or detach its HEAD (git -C <path> checkout --detach).",
	}
}

// isAllowedDefaultBranchWorktree returns true if the given worktree path is
// an intentional consumer of the default branch (refinery/rig or mayor/rig).
func isAllowedDefaultBranchWorktree(rigPath, worktreePath string) bool {
	// Normalize paths for comparison. Using EvalSymlinks would be nicer but
	// is unnecessarily expensive here; string prefix matches are fine because
	// these paths are controlled by gt itself.
	wt := filepath.Clean(worktreePath)
	allowed := []string{
		filepath.Clean(filepath.Join(rigPath, "refinery", "rig")),
		filepath.Clean(filepath.Join(rigPath, "mayor", "rig")),
	}
	for _, a := range allowed {
		if wt == a {
			return true
		}
	}
	return false
}

// resolveRigDefaultBranch returns the rig's default branch, trying:
//  1. Rig config default_branch
//  2. Git origin/HEAD detection on the bare repo
//  3. "" if nothing resolves (caller should skip the rig)
func resolveRigDefaultBranch(rigPath string) string {
	if cfg, err := rig.LoadRigConfig(rigPath); err == nil && cfg.DefaultBranch != "" {
		return cfg.DefaultBranch
	}
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if branch := detectDefaultBranchFromGit(bareRepoPath); branch != "" {
		return branch
	}
	return ""
}

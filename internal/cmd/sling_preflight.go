package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
	rigpkg "github.com/steveyegge/gastown/internal/rig"
)

// preflightRigSpawn validates that a rig is configured well enough to spawn a
// polecat worktree, BEFORE executeSling performs any irreversible side effects
// (burning stale molecules, creating the auto-convoy, hooking the bead). It
// surfaces the two failure modes that previously only manifested as a cryptic
// "Failed to sling: exit status 1" deep in the worktree-creation path (gu-9uvl6,
// refiled from gc-ab6ujy):
//
//  1. Missing config.json — the rig has no root config.json, so default_branch
//     and the beads prefix can't be resolved.
//  2. Bad base branch — the configured/derived base branch does not exist as
//     origin/<branch> in the shared bare repo, so `git worktree add` fails. When
//     the branch is simply not yet fetched into .repo.git (a sync gap on a
//     non-default convoy base branch — gu-6vg2a), this self-heals by fetching it
//     from origin; it only fails when the branch is genuinely absent on origin.
//
// baseBranch is the effective base branch already resolved by the caller (may be
// empty, in which case the rig's configured/derived default branch is used). It
// may carry an "origin/" prefix, which is normalized here.
//
// The check is best-effort: if the bare repo does not exist yet (a rig that has
// never been cloned), branch validation is skipped — the spawn path creates it,
// and the worktree-creation error remains the backstop. Only conditions we can
// prove are fatal return an error.
func preflightRigSpawn(townRoot, rigName, baseBranch string) error {
	if townRoot == "" || rigName == "" {
		return nil
	}
	rigPath := filepath.Join(townRoot, rigName)

	// 1. config.json must exist. Without it, default_branch and the beads prefix
	// fall back to ambiguous defaults and the rig is effectively unconfigured.
	configPath := filepath.Join(rigPath, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return fmt.Errorf("rig %q is missing config.json (%s)\n"+
			"The rig is registered but unconfigured, so default_branch and the beads prefix cannot be resolved.\n"+
			"Fix it with: gt doctor --fix\n"+
			"(or copy config.json from a working rig and set git_url / default_branch / beads.prefix)",
			rigName, configPath)
	}

	// Resolve the base branch to validate. An explicit caller-supplied base wins;
	// otherwise use the rig's configured/derived default branch.
	branch := strings.TrimPrefix(baseBranch, "origin/")
	if branch == "" {
		branch = resolveRigDefaultBranchForSling(rigPath)
	}
	if branch == "" {
		// Nothing concrete to validate against — let the spawn path's own
		// fallback ("main") and worktree-creation error handle it.
		return nil
	}

	// 2. The base branch must exist as origin/<branch> in the shared bare repo.
	// Skip when there is no bare repo yet (never-cloned rig): the spawn path
	// creates it, and the worktree-creation error is the backstop.
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if info, err := os.Stat(bareRepoPath); err != nil || !info.IsDir() {
		return nil
	}
	bareGit := git.NewGitWithDir(bareRepoPath, "")
	ref := "refs/remotes/origin/" + branch
	exists, err := bareGit.RefExists(ref)
	if err != nil {
		// Can't determine — don't block dispatch on a transient git error; the
		// worktree-creation path re-checks and fails closed if truly broken.
		return nil
	}
	if exists {
		return nil
	}

	// Sync gap (gu-6vg2a): Gas Town topology has the crew workspace clone and the
	// polecat bare repo (.repo.git) sharing one remote. A non-default convoy base
	// branch is pushed to the shared remote by the crew, but .repo.git has not
	// fetched it — so the local origin/<branch> ref is missing here even though
	// the branch exists on origin. Attempt to fetch it into the tracking ref
	// before failing; only error if the branch is genuinely absent on the remote.
	if fetchErr := bareGit.FetchRemoteBranch("origin", branch); fetchErr == nil {
		if exists, err := bareGit.RefExists(ref); err == nil && exists {
			return nil
		}
	}

	suggestion := suggestBaseBranches(bareGit)
	msg := fmt.Sprintf("rig %q base branch %q does not exist as origin/%s in the bare repo (%s), "+
		"and could not be fetched from origin.\n"+
		"Polecat worktree creation would fail with a cryptic git error.",
		rigName, branch, branch, bareRepoPath)
	if suggestion != "" {
		msg += "\nAvailable branches: " + suggestion
	}
	msg += fmt.Sprintf("\nFix the default_branch in %s/config.json (gt doctor flags this), "+
		"or create the branch on the remote.", rigPath)
	return fmt.Errorf("%s", msg)
}

// resolveRigDefaultBranchForSling returns the rig's configured default_branch,
// falling back to git origin/HEAD detection on the bare repo. Returns "" when
// nothing resolves (caller skips branch validation).
//
// This mirrors doctor.resolveRigDefaultBranch but lives in the cmd package to
// avoid a cmd -> doctor dependency.
func resolveRigDefaultBranchForSling(rigPath string) string {
	if cfg, err := rigpkg.LoadRigConfig(rigPath); err == nil && cfg.DefaultBranch != "" {
		return cfg.DefaultBranch
	}
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if info, err := os.Stat(bareRepoPath); err == nil && info.IsDir() {
		return git.NewGitWithDir(bareRepoPath, "").RemoteDefaultBranch()
	}
	return ""
}

// suggestBaseBranches lists the remote tracking branches in the bare repo as a
// short, comma-joined suggestion string for error messages. Returns "" when no
// branches can be listed. Caps the list so the message stays readable.
func suggestBaseBranches(bareGit *git.Git) string {
	out, err := bareGit.ForEachRef("refs/remotes/origin/*", "%(refname:short)")
	if err != nil || strings.TrimSpace(out) == "" {
		return ""
	}
	var branches []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		// for-each-ref short form yields "origin/<branch>"; strip the remote and
		// skip the symbolic origin/HEAD pointer.
		name = strings.TrimPrefix(name, "origin/")
		if name == "" || name == "HEAD" {
			continue
		}
		branches = append(branches, name)
	}
	if len(branches) == 0 {
		return ""
	}
	sort.Strings(branches)
	const maxList = 10
	truncated := false
	if len(branches) > maxList {
		branches = branches[:maxList]
		truncated = true
	}
	joined := strings.Join(branches, ", ")
	if truncated {
		joined += ", …"
	}
	return joined
}

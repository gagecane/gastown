// Package polecat provides polecat lifecycle management.
package polecat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/templates"
)

// AutoSaveAbandonedWIP commits any uncommitted work in a polecat worktree before
// the session is terminated. This is the safety net extracted from gt done's
// auto-commit logic (done.go:509-619), now callable from any session-kill path:
//   - daemon.killIdlePolecat (reaper)
//   - witness.RestartPolecatSession
//   - polecat.killExistingPolecatSession
//   - polecat.ReuseIdlePolecat (second-chance before ResetHard)
//
// Returns:
//   - (false, "", nil) if the worktree is clean or only has runtime artifacts
//   - (true, sha, nil) if work was saved; sha is the new commit SHA
//   - (false, "", err) if an error occurred that prevented saving
//
// The helper does NOT push. Pushing is the caller's responsibility (caller knows
// whether origin is reachable, branch protection rules, etc.).
//
// Guards (matching gt done safety net):
//   - Detached HEAD: refuses to commit (would orphan work)
//   - Default branch (main/master/mainline): refuses to commit (would bypass merge queue)
//   - Unmerged conflicts: refuses to commit (needs manual resolution)
func AutoSaveAbandonedWIP(worktreePath, branch, reason string) (saved bool, sha string, err error) {
	if worktreePath == "" {
		return false, "", fmt.Errorf("worktreePath is required")
	}

	// Validate worktree exists
	if _, err := os.Stat(worktreePath); err != nil {
		return false, "", fmt.Errorf("worktree not found: %w", err)
	}

	g := git.NewGit(worktreePath)

	// Check if there's any uncommitted work
	workStatus, err := g.CheckUncommittedWork()
	if err != nil {
		return false, "", fmt.Errorf("checking uncommitted work: %w", err)
	}

	// Nothing to save: worktree is clean or only has runtime artifacts
	if !workStatus.HasUncommittedChanges || workStatus.CleanExcludingRuntime() {
		return false, "", nil
	}

	// GUARD: Detached HEAD (gu-h5pr)
	// A commit on detached HEAD produces an orphaned object: no branch ref advances,
	// so the work would be lost after gc. Refuse and let caller handle manually.
	detached, detErr := g.IsDetachedHEAD()
	if detErr != nil {
		return false, "", fmt.Errorf("checking detached HEAD: %w", detErr)
	}
	if detached {
		return false, "", fmt.Errorf("autosave refused: HEAD is detached (would orphan work)")
	}

	// GUARD: Default branch (gu-cfb)
	// Auto-committing on main/master/mainline would bypass the merge queue and
	// potentially land unrelated artifacts directly on origin/main.
	if isDefaultBranchForAutosave(branch) {
		return false, "", fmt.Errorf("autosave refused: branch %q is a protected default branch", branch)
	}

	// GUARD: Unmerged conflicts
	// Cannot auto-commit with merge conflicts; needs manual resolution.
	if len(workStatus.UnmergedFiles) > 0 {
		return false, "", fmt.Errorf("autosave refused: unmerged conflicts present: %s",
			strings.Join(workStatus.UnmergedFiles, ", "))
	}

	// Stage all changes
	if err := g.Add("-A"); err != nil {
		return false, "", fmt.Errorf("git add -A failed: %w", err)
	}

	// Unstage Gas Town overlay files that git add -A picked up (gt-p35)
	_ = g.ResetFiles("CLAUDE.local.md")

	// Only unstage CLAUDE.md if it contains the overlay marker
	claudeMDPath := filepath.Join(worktreePath, "CLAUDE.md")
	if claudeData, readErr := os.ReadFile(claudeMDPath); readErr == nil {
		if strings.Contains(string(claudeData), templates.PolecatLifecycleMarker) {
			_ = g.ResetFiles("CLAUDE.md")
		}
	}

	// Unstage runtime/ephemeral artifacts using the centralized git policy
	for _, path := range workStatus.RuntimeArtifactPaths() {
		_ = g.ResetFiles(path)
	}

	// Unstage deletions of tracked files (gt-pvx safety)
	// A safety-net auto-commit should preserve work (additions + modifications),
	// never destroy it (deletions).
	if stagedDeletions, delErr := g.StagedDeletions(); delErr == nil && len(stagedDeletions) > 0 {
		_ = g.ResetFiles(stagedDeletions...)
	}

	// Handle stashes: auto-pop if the stash parent matches HEAD (fresh stash from
	// the current session). If stale (parent != HEAD), leave it for manual handling.
	if stashEntries, stashErr := g.StashListForBranch(); stashErr == nil && len(stashEntries) > 0 {
		for _, entry := range stashEntries {
			stale, _, _, staleErr := g.IsStashStale(entry.Ref)
			if staleErr != nil || stale {
				// Stale or error checking — skip this stash
				continue
			}
			// Fresh stash — attempt to pop it into the working tree
			if popErr := g.StashPop(entry.Ref); popErr != nil {
				// Pop failed (likely conflicts) — leave it alone
				break
			}
			// Re-stage the popped changes
			_ = g.Add("-A")
			// Re-unstage runtime artifacts and deletions after the pop
			if ws, wsErr := g.CheckUncommittedWork(); wsErr == nil {
				for _, path := range ws.RuntimeArtifactPaths() {
					_ = g.ResetFiles(path)
				}
			}
			if dels, delErr := g.StagedDeletions(); delErr == nil && len(dels) > 0 {
				_ = g.ResetFiles(dels...)
			}
			// Only pop one stash per autosave; multiple stashes are unusual and
			// likely need manual review.
			break
		}
	}

	// Re-check if there's anything staged to commit.
	// After unstaging runtime artifacts and deletions, there may be nothing left.
	hasStagedChanges, stageErr := g.HasStagedChanges()
	if stageErr != nil {
		return false, "", fmt.Errorf("checking staged changes: %w", stageErr)
	}
	if !hasStagedChanges {
		// Everything we unstaged left nothing real — no commit needed
		return false, "", nil
	}

	// Build commit message
	commitMsg := fmt.Sprintf("fix(autosave): reaper-saved abandoned WIP (%s)\n\nReason: %s\nWorktree: %s\nBranch: %s",
		reason, reason, worktreePath, branch)

	// Commit
	if err := g.Commit(commitMsg); err != nil {
		return false, "", fmt.Errorf("git commit failed: %w", err)
	}

	// Get the new commit SHA
	newSHA, err := g.HeadSHA()
	if err != nil {
		// Commit succeeded but we can't get SHA — still report success
		return true, "", nil
	}

	return true, newSHA, nil
}

// isDefaultBranchForAutosave returns true if the branch name is a protected
// default branch that should not receive auto-commits.
func isDefaultBranchForAutosave(branch string) bool {
	switch branch {
	case "main", "master", "mainline":
		return true
	default:
		return false
	}
}

// WorktreePath returns the path to a polecat's git worktree given the town root,
// rig name, and polecat name. This is a standalone function that doesn't require
// a Manager instance, for use by the daemon reaper and other callers outside the
// polecat package.
//
// Structure: polecats/<name>/<rigname>/ (new) or polecats/<name>/ (legacy).
func WorktreePath(townRoot, rigName, polecatName string) string {
	// New structure: <townRoot>/<rigName>/polecats/<polecatName>/<rigName>/
	newPath := filepath.Join(townRoot, rigName, "polecats", polecatName, rigName)
	if info, err := os.Stat(newPath); err == nil && info.IsDir() {
		return newPath
	}

	// Old structure: <townRoot>/<rigName>/polecats/<polecatName>/
	oldPath := filepath.Join(townRoot, rigName, "polecats", polecatName)
	if info, err := os.Stat(oldPath); err == nil && info.IsDir() {
		// Check if this is actually a git worktree (has .git file or dir)
		gitPath := filepath.Join(oldPath, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return oldPath
		}
	}

	// Default to new structure
	return newPath
}

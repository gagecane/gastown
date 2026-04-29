package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
)

// BareRepoExistsCheck verifies that .repo.git exists when worktrees depend on it.
// Worktrees (refinery/rig, polecats) created from the shared bare repo have .git files
// pointing to .repo.git/worktrees/<name>. If .repo.git is missing (deleted, moved, or
// never created), all those worktrees break with "fatal: not a git repository".
type BareRepoExistsCheck struct {
	FixableCheck
	brokenWorktrees []string // worktree paths with broken .repo.git references
	pushURLMismatch bool     // config.json push_url differs from .repo.git push URL
}

// NewBareRepoExistsCheck creates a new bare repo exists check.
func NewBareRepoExistsCheck() *BareRepoExistsCheck {
	return &BareRepoExistsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "bare-repo-exists",
				CheckDescription: "Verify .repo.git exists when worktrees depend on it",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if .repo.git exists when worktrees reference it.
func (c *BareRepoExistsCheck) Run(ctx *CheckContext) *CheckResult {
	if ctx.RigName == "" {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No rig specified (skipping bare repo check)",
			Category: c.Category(),
		}
	}

	rigPath := ctx.RigPath()
	bareRepoPath := filepath.Join(rigPath, ".repo.git")

	// Reset mutable state to avoid stale values if check is reused across rigs.
	c.brokenWorktrees = nil
	c.pushURLMismatch = false
	worktreeDirs := c.findWorktreeDirs(rigPath, ctx.RigName)

	for _, wtDir := range worktreeDirs {
		gitFile := filepath.Join(wtDir, ".git")
		info, err := os.Stat(gitFile)
		if err != nil {
			continue // no .git entry, skip
		}

		// Only check .git files (worktrees), not .git directories (regular clones)
		if info.IsDir() {
			continue
		}

		content, err := os.ReadFile(gitFile)
		if err != nil {
			continue
		}

		line := strings.TrimSpace(string(content))
		if !strings.HasPrefix(line, "gitdir: ") {
			continue
		}

		gitdir := strings.TrimPrefix(line, "gitdir: ")
		// Check if this worktree references .repo.git
		if strings.Contains(gitdir, ".repo.git") {
			// Resolve the target path
			targetPath := gitdir
			if !filepath.IsAbs(targetPath) {
				targetPath = filepath.Join(wtDir, targetPath)
			}

			// Check if the target exists
			if _, err := os.Stat(targetPath); os.IsNotExist(err) {
				relPath, _ := filepath.Rel(rigPath, wtDir)
				if relPath == "" {
					relPath = wtDir
				}
				c.brokenWorktrees = append(c.brokenWorktrees, relPath)
			}
		}
	}

	// If .repo.git exists, also verify push URL matches config.json
	if _, err := os.Stat(bareRepoPath); err == nil {
		c.pushURLMismatch = false
		var configWarning string // track config read issues for combined reporting

		// Read push_url from config.json directly (not via config.RigEntry/loadRig).
		// The doctor check reads on-disk config independently of the loaded town.json state.
		// If push_url field semantics change in config.RigEntry, update this struct to match.
		configPath := filepath.Join(rigPath, "config.json")
		cfgData, readErr := os.ReadFile(configPath)
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				// config.json exists but unreadable — warn about permissions
				if len(c.brokenWorktrees) == 0 {
					return &CheckResult{
						Name:     c.Name(),
						Status:   StatusWarning,
						Message:  "Shared bare repo exists but config.json is unreadable",
						Details:  []string{readErr.Error()},
						FixHint:  "Check file permissions on " + configPath,
						Category: c.Category(),
					}
				}
				configWarning = "config.json unreadable: " + readErr.Error()
			}
			// config.json missing — skip push URL validation (bare repo can exist without config)
		} else {
			var cfg struct {
				PushURL string `json:"push_url,omitempty"`
			}
			if jsonErr := json.Unmarshal(cfgData, &cfg); jsonErr != nil {
				// config.json is malformed — cannot validate push URL
				if len(c.brokenWorktrees) == 0 {
					return &CheckResult{
						Name:     c.Name(),
						Status:   StatusWarning,
						Message:  "Shared bare repo exists but config.json is malformed",
						Details:  []string{jsonErr.Error()},
						FixHint:  "Check config.json syntax in " + configPath,
						Category: c.Category(),
					}
				}
				configWarning = "config.json malformed: " + jsonErr.Error()
			} else {
				cfgPushURL := strings.TrimSpace(cfg.PushURL)
				// Get actual push and fetch URLs from .repo.git using git wrapper
				bareGit := git.NewGitWithDir(bareRepoPath, "")
				actualPush, pushErr := bareGit.GetPushURL("origin")
				actualFetch, fetchErr := bareGit.RemoteURL("origin")
				if pushErr != nil || fetchErr != nil {
					// Cannot query remote config — report warning
					if len(c.brokenWorktrees) == 0 {
						details := []string{}
						if pushErr != nil {
							details = append(details, "push URL query failed: "+pushErr.Error())
						}
						if fetchErr != nil {
							details = append(details, "fetch URL query failed: "+fetchErr.Error())
						}
						return &CheckResult{
							Name:     c.Name(),
							Status:   StatusWarning,
							Message:  "Cannot validate push URL — git remote query failed",
							Details:  details,
							FixHint:  "Check .repo.git remote configuration for " + ctx.RigName,
							Category: c.Category(),
						}
					}
					configWarning = fmt.Sprintf("git remote query failed (push: %v, fetch: %v)", pushErr, fetchErr)
				} else {
					actualFetch = strings.TrimSpace(actualFetch)
					if cfgPushURL != "" {
						// Config specifies a push URL — it should match actual
						if actualPush != cfgPushURL {
							c.pushURLMismatch = true
						}
					} else {
						// Config has no push URL — this may be a legacy config that
						// predates the push_url feature. Don't flag a mismatch; the
						// existing git push URL (if any) may be intentionally set.
						// RegisterRig will auto-detect and sync to config.json on next run.
					}
				}
			}
		}

		// Return based on the combination of conditions.
		// All paths return from here — no fall-through to the "missing .repo.git" block.
		if len(c.brokenWorktrees) == 0 && !c.pushURLMismatch {
			return &CheckResult{
				Name:     c.Name(),
				Status:   StatusOK,
				Message:  "Shared bare repo exists and worktrees are valid",
				Category: c.Category(),
			}
		}
		if c.pushURLMismatch && len(c.brokenWorktrees) == 0 {
			return &CheckResult{
				Name:     c.Name(),
				Status:   StatusWarning,
				Message:  "Shared bare repo push URL does not match config.json",
				Details:  []string{"Note: manual config.json edits require 'gt rig add <name> --adopt' to propagate to town.json"},
				FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to update push URL",
				Category: c.Category(),
			}
		}
		if c.pushURLMismatch {
			// Both push URL mismatch and broken worktrees
			details := []string{fmt.Sprintf("Push URL mismatch and %d broken worktree(s)", len(c.brokenWorktrees))}
			if configWarning != "" {
				details = append(details, configWarning)
			}
			details = append(details, c.brokenWorktrees...)
			return &CheckResult{
				Name:     c.Name(),
				Status:   StatusError,
				Message:  fmt.Sprintf("Push URL mismatch and %d broken worktree(s)", len(c.brokenWorktrees)),
				Details:  details,
				FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to repair",
				Category: c.Category(),
			}
		}
		// Broken worktrees only (.repo.git exists but worktree refs are stale)
		details := []string{fmt.Sprintf("Bare repo exists at %s but %d worktree(s) have broken references", bareRepoPath, len(c.brokenWorktrees))}
		if configWarning != "" {
			details = append(details, configWarning)
		}
		details = append(details, c.brokenWorktrees...)
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusError,
			Message:  fmt.Sprintf("%d worktree(s) have broken references in .repo.git", len(c.brokenWorktrees)),
			Details:  details,
			FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to recreate worktree entries",
			Category: c.Category(),
		}
	}

	// .repo.git missing but no worktrees depend on it
	if len(c.brokenWorktrees) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No worktrees depend on .repo.git",
			Category: c.Category(),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d worktree(s) reference missing .repo.git", len(c.brokenWorktrees)),
		Details: append(
			[]string{"Missing: " + bareRepoPath},
			c.brokenWorktrees...,
		),
		FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to recreate .repo.git from remote",
		Category: c.Category(),
	}
}

// Fix recreates the .repo.git bare repo from the rig's git_url and re-registers
// existing worktrees. Also fixes push URL mismatches on existing repos.
func (c *BareRepoExistsCheck) Fix(ctx *CheckContext) error {
	if ctx.RigName == "" {
		return nil
	}

	rigPath := ctx.RigPath()
	bareRepoPath := filepath.Join(rigPath, ".repo.git")

	// Fix push URL mismatch on existing .repo.git.
	// Only apply if .repo.git exists — if missing, recreation below sets the correct push URL.
	// Note: config.json is parsed inline here (not via config.RigEntry) because the doctor
	// check needs to read the on-disk config independently of the loaded town.json state.
	if c.pushURLMismatch {
		if _, statErr := os.Stat(bareRepoPath); statErr == nil {
			configPath := filepath.Join(rigPath, "config.json")
			cfgData, readErr := os.ReadFile(configPath)
			if readErr != nil {
				return fmt.Errorf("cannot read config.json to fix push URL: %w", readErr)
			}
			var cfg struct {
				PushURL string `json:"push_url,omitempty"`
			}
			if jsonErr := json.Unmarshal(cfgData, &cfg); jsonErr != nil {
				return fmt.Errorf("cannot parse config.json to fix push URL: %w", jsonErr)
			}
			cfgPushURL := strings.TrimSpace(cfg.PushURL)
			bareGit := git.NewGitWithDir(bareRepoPath, "")
			if cfgPushURL != "" {
				if err := bareGit.ConfigurePushURL("origin", cfgPushURL); err != nil {
					return fmt.Errorf("updating push URL on .repo.git: %w", err)
				}
			} else {
				// Config has no push URL — this may be a legacy config that
				// predates the push_url feature. Don't clear; the existing
				// git push URL (if any) may be intentionally set.
			}
		}
	}

	if len(c.brokenWorktrees) == 0 {
		// No worktrees to fix — push URL mismatch (if any) already handled above
		return nil
	}

	// Recreate .repo.git if it's missing; skip clone if it already exists
	if _, err := os.Stat(bareRepoPath); err != nil {
		// Read git_url from config.json
		configPath := filepath.Join(rigPath, "config.json")
		data, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf("cannot read config.json to get git_url: %w", err)
		}

		var cfg struct {
			GitURL  string `json:"git_url"`
			PushURL string `json:"push_url,omitempty"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("cannot parse config.json: %w", err)
		}
		if cfg.GitURL == "" {
			return fmt.Errorf("config.json has no git_url, cannot recreate .repo.git")
		}

		// Clone bare repo (shallow, single-branch for efficiency on repos with many branches)
		cmd := exec.Command("git", "clone", "--bare", "--single-branch", "--depth", "1", cfg.GitURL, bareRepoPath)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("cloning bare repo: %s", strings.TrimSpace(stderr.String()))
		}

		// Configure refspec so worktrees can fetch origin/* refs.
		// Skip full fetch — the shallow single-branch clone already has the default branch.
		stderr.Reset()
		configCmd := exec.Command("git", "-C", bareRepoPath, "config",
			"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
		configCmd.Stderr = &stderr
		if err := configCmd.Run(); err != nil {
			return fmt.Errorf("configuring refspec: %s", strings.TrimSpace(stderr.String()))
		}

		// Restore push URL if configured (for read-only upstream repos)
		if cfg.PushURL != "" {
			stderr.Reset()
			pushURLCmd := exec.Command("git", "-C", bareRepoPath, "remote", "set-url", "--push", "origin", cfg.PushURL)
			pushURLCmd.Stderr = &stderr
			if err := pushURLCmd.Run(); err != nil {
				return fmt.Errorf("configuring push URL: %s", strings.TrimSpace(stderr.String()))
			}
		}
	}

	// Re-register broken worktrees so the bare repo knows about them.
	// Git worktrees are tracked in .repo.git/worktrees/<name>/ with a gitdir file
	// pointing back to the worktree. We need to create these metadata entries.
	for _, relPath := range c.brokenWorktrees {
		wtPath := filepath.Join(rigPath, relPath)
		gitFile := filepath.Join(wtPath, ".git")

		content, err := os.ReadFile(gitFile)
		if err != nil {
			continue
		}

		line := strings.TrimSpace(string(content))
		if !strings.HasPrefix(line, "gitdir: ") {
			continue
		}

		gitdir := strings.TrimPrefix(line, "gitdir: ")
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(wtPath, gitdir)
		}
		gitdir = filepath.Clean(gitdir)

		// Extract worktree name from path (e.g., .repo.git/worktrees/rig -> "rig")
		worktreeName := filepath.Base(gitdir)

		// Create worktree metadata directory
		wtMetaDir := filepath.Join(bareRepoPath, "worktrees", worktreeName)
		if err := os.MkdirAll(wtMetaDir, 0755); err != nil {
			continue
		}

		// Write gitdir file (points back to the worktree's .git file)
		gitdirFile := filepath.Join(wtMetaDir, "gitdir")
		if err := os.WriteFile(gitdirFile, []byte(wtPath+"/.git\n"), 0644); err != nil {
			continue
		}

		// Detect which branch the worktree was on by looking at HEAD in the worktree.
		// If we can't determine, default to HEAD.
		headContent := "ref: refs/heads/main\n"
		// Try to read the old HEAD from the worktree's git metadata
		oldHeadPath := filepath.Join(gitdir, "HEAD")
		if oldHead, err := os.ReadFile(oldHeadPath); err == nil {
			headContent = string(oldHead)
		}

		headFile := filepath.Join(wtMetaDir, "HEAD")
		if err := os.WriteFile(headFile, []byte(headContent), 0644); err != nil {
			continue
		}
	}

	return nil
}

// findWorktreeDirs returns paths to directories that may be git worktrees within a rig.
// Checks refinery/rig and all polecat worktree directories.
func (c *BareRepoExistsCheck) findWorktreeDirs(rigPath, rigName string) []string {
	var dirs []string

	// refinery/rig
	refineryRig := filepath.Join(rigPath, "refinery", "rig")
	if _, err := os.Stat(refineryRig); err == nil {
		dirs = append(dirs, refineryRig)
	}

	// witness/rig
	witnessRig := filepath.Join(rigPath, "witness", "rig")
	if _, err := os.Stat(witnessRig); err == nil {
		dirs = append(dirs, witnessRig)
	}

	// polecats/<name>/<rigname>/
	polecatsDir := filepath.Join(rigPath, "polecats")
	if entries, err := os.ReadDir(polecatsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			// New structure: polecats/<name>/<rigname>/
			newPath := filepath.Join(polecatsDir, entry.Name(), rigName)
			if _, err := os.Stat(newPath); err == nil {
				dirs = append(dirs, newPath)
			}
			// Old structure: polecats/<name>/
			oldPath := filepath.Join(polecatsDir, entry.Name())
			if oldPath != newPath {
				if _, err := os.Stat(filepath.Join(oldPath, ".git")); err == nil {
					dirs = append(dirs, oldPath)
				}
			}
		}
	}

	return dirs
}

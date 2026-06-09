package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

const (
	// defaultBranchSyncInterval is the patrol cadence — how often each target
	// is re-checked for divergence. Branch sync is maintenance, not latency
	// sensitive: a long-lived planning branch that falls a day behind its
	// source is fine; one that falls weeks behind re-accumulates the
	// diverged-trunk tangle this patrol exists to prevent (gs-auhe).
	defaultBranchSyncInterval = 24 * time.Hour

	// branchSyncGitTimeout bounds individual plumbing git commands (fetch,
	// rev-list, worktree add/remove).
	branchSyncGitTimeout = 60 * time.Second

	// branchSyncMergeTimeout bounds the merge itself.
	branchSyncMergeTimeout = 120 * time.Second

	// branchSyncPushTimeout bounds the push of a clean merge to the target.
	branchSyncPushTimeout = 120 * time.Second

	// branchSyncLabel tags every conflict bead this patrol files, so the
	// per-target dedup query can find an already-open report.
	branchSyncLabel = "branch-sync"
)

// BranchSyncConfig holds configuration for the branch_sync patrol.
//
// branch_sync is a generic "run this merge recipe every N" primitive: it keeps
// one or more long-lived branches merged from their source branch so they do
// not re-accumulate divergence. The first consumer is lia_bac's gagecane/gt
// branch (kept merged from main), but any rig/branch can opt in via a target.
//
// These are REAL customer repos, so the patrol holds hard safety invariants:
// it never touches or pushes the source branch, never force-pushes, never
// leaves a dirty worktree, and on any conflict it aborts and files+slings a
// bead rather than guessing at a resolution.
type BranchSyncConfig struct {
	// Enabled controls whether branch sync runs. Opt-in (default disabled).
	Enabled bool `json:"enabled"`

	// IntervalStr is the patrol cadence, as a string (e.g., "24h").
	IntervalStr string `json:"interval,omitempty"`

	// Targets is the list of branches to keep synced.
	Targets []BranchSyncTarget `json:"targets,omitempty"`
}

// BranchSyncTarget describes one long-lived branch to keep merged from a source.
type BranchSyncTarget struct {
	// Rig is the rig whose bare repo (<town>/<rig>/.repo.git) holds the
	// branches. Required.
	Rig string `json:"rig"`

	// Branch is the long-lived target branch to keep merged, e.g. "gagecane/gt".
	// Required. This is the only branch the patrol ever pushes.
	Branch string `json:"branch"`

	// From is the source branch to merge in. A leading "origin/" is optional
	// and stripped. Default: "main".
	From string `json:"from,omitempty"`

	// Crew is the sling target for conflict resolution, e.g.
	// "lia_bac/crew/gagecane". When empty, a conflict bead is filed but not
	// slung (a human picks it up from the rig backlog).
	Crew string `json:"crew,omitempty"`
}

// resolvedFrom returns the source branch name with any leading "origin/"
// stripped, defaulting to "main".
func (t BranchSyncTarget) resolvedFrom() string {
	from := strings.TrimSpace(t.From)
	if from == "" {
		return "main"
	}
	return strings.TrimPrefix(from, "origin/")
}

// branchSyncInterval returns the configured patrol cadence, or the default (24h).
func branchSyncInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.BranchSync != nil {
		if config.Patrols.BranchSync.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.BranchSync.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultBranchSyncInterval
}

// syncBranches runs one branch_sync cycle over every configured target.
// Each target is independent: an error on one does not stop the others.
func (d *Daemon) syncBranches() {
	if !d.isPatrolActive("branch_sync") {
		return
	}

	cfg := d.patrolConfig.Patrols.BranchSync
	if cfg == nil || len(cfg.Targets) == 0 {
		d.logger.Printf("branch_sync: enabled but no targets configured, nothing to do")
		return
	}

	d.logger.Printf("branch_sync: starting patrol cycle (%d target(s))", len(cfg.Targets))

	var synced, upToDate, conflicts, failed int
	for _, target := range cfg.Targets {
		outcome, err := d.syncOneBranch(target)
		switch {
		case err != nil:
			failed++
			d.logger.Printf("branch_sync: %s/%s: error: %v", target.Rig, target.Branch, err)
		case outcome == branchSyncConflict:
			conflicts++
		case outcome == branchSyncMerged:
			synced++
		default:
			upToDate++
		}
	}

	d.logger.Printf("branch_sync: cycle complete — merged=%d up_to_date=%d conflicts=%d failed=%d",
		synced, upToDate, conflicts, failed)
}

// branchSyncOutcome enumerates the non-error results of syncing one target.
type branchSyncOutcome int

const (
	branchSyncUpToDate branchSyncOutcome = iota
	branchSyncMerged
	branchSyncConflict
)

// syncOneBranch keeps a single target branch merged from its source.
//
// It operates entirely in a throwaway detached worktree off the rig's bare
// repo, so the rig's real working directories are never touched. The only
// write to a shared ref is an explicit push of the target branch on a clean
// merge — the source branch is never pushed and --force is never used.
func (d *Daemon) syncOneBranch(target BranchSyncTarget) (branchSyncOutcome, error) {
	if strings.TrimSpace(target.Rig) == "" || strings.TrimSpace(target.Branch) == "" {
		return branchSyncUpToDate, fmt.Errorf("invalid target: rig and branch are required")
	}

	src := target.resolvedFrom()
	dst := target.Branch
	if src == dst {
		return branchSyncUpToDate, fmt.Errorf("source and target branch are identical (%q)", dst)
	}

	rigPath := filepath.Join(d.config.TownRoot, target.Rig)
	bareRepo := filepath.Join(rigPath, ".repo.git")
	if _, err := os.Stat(bareRepo); err != nil {
		return branchSyncUpToDate, fmt.Errorf("bare repo not found at %s: %w", bareRepo, err)
	}

	// Fetch both refs so the comparison and worktree see current origin state.
	// Fetching explicit branches (not a blanket fetch) keeps the work bounded.
	if out, err := d.runBareGit(bareRepo, "fetch", "origin", src, dst); err != nil {
		return branchSyncUpToDate, fmt.Errorf("git fetch origin %s %s: %v (%s)", src, dst, err, out)
	}

	// behind = commits on origin/src not yet on origin/dst.
	behind, err := d.branchBehindCount(bareRepo, dst, src)
	if err != nil {
		return branchSyncUpToDate, err
	}
	if behind == 0 {
		d.logger.Printf("branch_sync: %s/%s: up to date with origin/%s", target.Rig, dst, src)
		return branchSyncUpToDate, nil
	}

	d.logger.Printf("branch_sync: %s/%s: %d commit(s) behind origin/%s — attempting merge", target.Rig, dst, behind, src)

	// Throwaway detached worktree at the current target tip. --detach avoids
	// creating/clobbering a local branch in the bare repo; we push by explicit
	// refspec from the detached HEAD instead.
	worktreePath := filepath.Join(rigPath, ".branch-sync-worktree")
	if err := cleanupStaleWorktree(bareRepo, worktreePath); err != nil {
		return branchSyncUpToDate, err
	}
	if out, err := d.runBareGit(bareRepo, "worktree", "add", "--detach", worktreePath, "origin/"+dst); err != nil {
		return branchSyncUpToDate, fmt.Errorf("git worktree add: %v (%s)", err, out)
	}
	// Always remove the worktree — registered removal plus an unconditional
	// RemoveAll mop-up, mirroring main_branch_test (gu-dob2f). Guarantees the
	// hard "never leave a dirty worktree" invariant even if we crash mid-merge.
	defer func() {
		if _, err := d.runBareGit(bareRepo, "worktree", "remove", "--force", worktreePath); err != nil {
			d.logger.Printf("branch_sync: %s/%s: warning: worktree cleanup failed: %v", target.Rig, dst, err)
		}
		if err := os.RemoveAll(worktreePath); err != nil {
			d.logger.Printf("branch_sync: %s/%s: warning: worktree dir removal failed: %v", target.Rig, dst, err)
		}
	}()

	// Attempt the merge in the worktree.
	mergeOut, mergeErr := d.runWorktreeGit(worktreePath, branchSyncMergeTimeout,
		"merge", "--no-edit", "origin/"+src)
	if mergeErr != nil {
		// Capture conflicting files BEFORE aborting, then abort so the worktree
		// is clean for removal. Any merge failure is treated as "needs a human":
		// we never attempt an auto-resolution.
		conflictFiles := d.unmergedFiles(worktreePath)
		if _, abortErr := d.runWorktreeGit(worktreePath, branchSyncGitTimeout, "merge", "--abort"); abortErr != nil {
			d.logger.Printf("branch_sync: %s/%s: warning: merge --abort failed: %v", target.Rig, dst, abortErr)
		}
		d.logger.Printf("branch_sync: %s/%s: merge conflict (%d file(s)) — filing+slinging for resolution",
			target.Rig, dst, len(conflictFiles))
		d.fileConflictBead(target, src, behind, conflictFiles, strings.TrimSpace(mergeOut))
		return branchSyncConflict, nil
	}

	// Clean merge — push ONLY the target branch via explicit refspec. This is
	// the single shared-ref write the patrol performs; main is never pushed and
	// --force is never used.
	if out, err := d.runWorktreeGit(worktreePath, branchSyncPushTimeout,
		"push", "origin", "HEAD:refs/heads/"+dst); err != nil {
		return branchSyncUpToDate, fmt.Errorf("git push origin HEAD:refs/heads/%s: %v (%s)", dst, err, out)
	}

	d.logger.Printf("branch_sync: %s/%s: merged origin/%s (%d commit(s)) and pushed", target.Rig, dst, src, behind)
	return branchSyncMerged, nil
}

// branchBehindCount returns how many commits origin/src has that origin/dst
// does not (i.e. how far the target branch is behind the source).
func (d *Daemon) branchBehindCount(bareRepo, dst, src string) (int, error) {
	out, err := d.runBareGit(bareRepo,
		"rev-list", "--count", "origin/"+dst+"..origin/"+src)
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count origin/%s..origin/%s: %v (%s)", dst, src, err, out)
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return 0, fmt.Errorf("parsing behind count %q: %w", strings.TrimSpace(out), convErr)
	}
	return n, nil
}

// unmergedFiles returns the list of conflicting (unmerged) paths in a worktree
// with an in-progress merge. Best-effort: returns nil on any error.
func (d *Daemon) unmergedFiles(worktreePath string) []string {
	out, err := d.runWorktreeGit(worktreePath, branchSyncGitTimeout,
		"diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files
}

// fileConflictBead files a bead describing a merge conflict and slings it to the
// target's crew. The bead is deduped by a per-target signature label so a
// recurring conflict bumps nothing instead of filing a fresh bead every cycle.
func (d *Daemon) fileConflictBead(target BranchSyncTarget, src string, behind int, conflictFiles []string, mergeOutput string) {
	rigDir := filepath.Join(d.config.TownRoot, target.Rig)
	sigLabel := branchSyncSignatureLabel(target, src)

	if d.branchSyncBeadExists(rigDir, sigLabel) {
		d.logger.Printf("branch_sync: %s/%s: open conflict bead already exists (%s), not re-filing",
			target.Rig, target.Branch, sigLabel)
		return
	}

	title := fmt.Sprintf("branch-sync conflict: %s cannot auto-merge %s (%d behind)",
		target.Branch, src, behind)

	var body strings.Builder
	fmt.Fprintf(&body, "The branch-sync patrol could not cleanly merge `origin/%s` into `%s`.\n\n", src, target.Branch)
	fmt.Fprintf(&body, "- Rig: %s\n", target.Rig)
	fmt.Fprintf(&body, "- Target branch: %s\n", target.Branch)
	fmt.Fprintf(&body, "- Source branch: origin/%s\n", src)
	fmt.Fprintf(&body, "- Behind by: %d commit(s)\n", behind)
	if len(conflictFiles) > 0 {
		body.WriteString("\nConflicting files:\n")
		for _, f := range conflictFiles {
			fmt.Fprintf(&body, "- %s\n", f)
		}
	}
	if mergeOutput != "" {
		fmt.Fprintf(&body, "\nMerge output:\n```\n%s\n```\n", mergeOutput)
	}
	body.WriteString("\nResolve the conflict on a worktree of the target branch, then push. " +
		"The patrol aborted the merge cleanly — no partial state was left behind.")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"create",
		"--title="+title,
		"--description="+body.String(),
		"--type=task",
		"--priority=1",
		"--label="+branchSyncLabel+","+sigLabel,
		"--silent",
	)
	cmd.Dir = rigDir
	cmd.Env = append(os.Environ(), "BD_ACTOR=branch-sync")
	util.SetDetachedProcessGroup(cmd)

	out, err := cmd.Output()
	if err != nil {
		d.logger.Printf("branch_sync: %s/%s: failed to file conflict bead: %v", target.Rig, target.Branch, err)
		return
	}
	beadID := strings.TrimSpace(string(out))
	if beadID == "" {
		d.logger.Printf("branch_sync: %s/%s: filed conflict bead but got empty ID", target.Rig, target.Branch)
		return
	}
	d.logger.Printf("branch_sync: %s/%s: filed conflict bead %s", target.Rig, target.Branch, beadID)

	if strings.TrimSpace(target.Crew) == "" {
		d.logger.Printf("branch_sync: %s/%s: no crew configured, bead %s left in rig backlog", target.Rig, target.Branch, beadID)
		return
	}
	d.slingConflictBead(beadID, target.Crew)
}

// slingConflictBead slings a filed conflict bead to the configured crew target.
func (d *Daemon) slingConflictBead(beadID, crew string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.gtPath, "sling", beadID, crew) //nolint:gosec // G204: args constructed internally
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "BD_ACTOR=branch-sync")
	util.SetDetachedProcessGroup(cmd)

	if out, err := cmd.CombinedOutput(); err != nil {
		d.logger.Printf("branch_sync: failed to sling %s to %s: %v (%s)", beadID, crew, err, strings.TrimSpace(string(out)))
		return
	}
	d.logger.Printf("branch_sync: slung conflict bead %s to %s", beadID, crew)
}

// branchSyncBeadExists reports whether an open branch-sync conflict bead with
// the given per-target signature label already exists in the rig.
func (d *Daemon) branchSyncBeadExists(rigDir, sigLabel string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"list",
		"--label="+branchSyncLabel+","+sigLabel,
		"--status=open",
		"--json",
	)
	cmd.Dir = rigDir
	cmd.Env = os.Environ()
	util.SetDetachedProcessGroup(cmd)

	out, err := cmd.Output()
	if err != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(out))
	// bd emits "[]" (or empty) when nothing matches.
	return trimmed != "" && trimmed != "[]" && trimmed != "null"
}

// branchSyncSignatureLabel builds a stable, label-safe dedup signature for a
// (rig, target branch, source branch) tuple. Slashes (common in branch names
// like "gagecane/gt") are replaced with "-" so the value is a valid label.
func branchSyncSignatureLabel(target BranchSyncTarget, src string) string {
	sanitize := func(s string) string {
		return strings.ReplaceAll(s, "/", "-")
	}
	return fmt.Sprintf("branch-sync:%s:%s-from-%s", sanitize(target.Rig), sanitize(target.Branch), sanitize(src))
}

// runBareGit runs a git plumbing command against a bare repo (cmd.Dir = bareRepo)
// and returns combined output. Used for fetch/rev-list/worktree management, all
// of which are bounded by branchSyncGitTimeout.
func (d *Daemon) runBareGit(bareRepo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), branchSyncGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: args constructed internally
	cmd.Dir = bareRepo
	cmd.Env = gitChildEnv()
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// runWorktreeGit runs a git command inside a checked-out worktree
// (cmd.Dir = worktreePath) and returns combined output.
func (d *Daemon) runWorktreeGit(worktreePath string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: args constructed internally
	cmd.Dir = worktreePath
	cmd.Env = gitChildEnv()
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

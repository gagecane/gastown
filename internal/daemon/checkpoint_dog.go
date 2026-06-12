package daemon

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultCheckpointDogInterval = 10 * time.Minute
)

// CheckpointDogConfig holds configuration for the checkpoint_dog patrol.
type CheckpointDogConfig struct {
	// Enabled controls whether the checkpoint dog runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "10m").
	IntervalStr string `json:"interval,omitempty"`
}

// checkpointDogInterval returns the configured interval, or the default (10m).
func checkpointDogInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.CheckpointDog != nil {
		if config.Patrols.CheckpointDog.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.CheckpointDog.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultCheckpointDogInterval
}

// runtimeExcludeDirs are directories to unstage after git add -A.
// These contain runtime/ephemeral data that should not be checkpointed.
//
// node_modules is listed WITHOUT a trailing slash on purpose. In casc_webapp
// polecat worktrees node_modules is not gitignored and frequently appears as a
// gitlink or symlink (not a real directory). A trailing-slash pathspec
// (node_modules/) does NOT unstage a symlink entry, so the auto-WIP checkpoint
// committed a node_modules-only gitlink — pure junk that later tripped the
// DIRTY recovery predicate. The no-slash form `node_modules` unstages all three
// forms (real dir, gitlink, symlink). (gu-lxrbv.)
var runtimeExcludeDirs = []string{
	".claude/",
	".beads/",
	".runtime/",
	"__pycache__/",
	"node_modules",
}

// runCheckpointDog auto-commits WIP changes in active polecat worktrees.
// This protects against data loss when sessions crash or hit context limits.
//
// ## ZFC Exemption
// The checkpoint dog executes git operations directly (same pattern as
// compactor_dog's SQL operations). The daemon pours a molecule for
// observability, then runs git commands via exec.Command.
func (d *Daemon) runCheckpointDog() {
	if !d.isPatrolActive("checkpoint_dog") {
		return
	}

	d.logger.Printf("checkpoint_dog: starting cycle")

	mol := d.pourDogMolecule(constants.MolDogCheckpoint, nil)
	defer mol.close()

	rigs := d.getKnownRigs()
	totalScanned := 0
	totalCheckpointed := 0

	for _, rigName := range rigs {
		scanned, checkpointed := d.checkpointRigPolecats(rigName)
		totalScanned += scanned
		totalCheckpointed += checkpointed
	}

	mol.closeStep("scan")
	mol.closeStep("checkpoint")

	d.logger.Printf("checkpoint_dog: cycle complete — scanned %d worktrees, checkpointed %d",
		totalScanned, totalCheckpointed)
	mol.closeStep("report")
}

// checkpointRigPolecats checkpoints dirty polecat worktrees in a single rig.
// Returns (scanned, checkpointed) counts.
func (d *Daemon) checkpointRigPolecats(rigName string) (int, int) {
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		return 0, 0
	}

	scanned := 0
	checkpointed := 0

	for _, polecatName := range polecats {
		scanned++

		// Check if tmux session is alive — only checkpoint active sessions.
		// Dead sessions can't benefit from checkpoints.
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
		alive, err := d.tmux.HasSession(sessionName)
		if err != nil {
			d.logger.Printf("checkpoint_dog: error checking session %s: %v", sessionName, err)
			continue
		}
		if !alive {
			continue
		}

		// Polecat layout: prefer <polecatsDir>/<name>/<rigName>/ (the new
		// nested layout where the outer <name>/ dir is a container with
		// per-polecat scaffolding and the inner dir is the actual git
		// worktree). Fall back to <polecatsDir>/<name>/ for the legacy
		// flat layout still supported by polecat.Manager. Both candidates
		// must contain `.git` — never fall back to a parent dir, since
		// the original bug here was exactly that: an empty <name>/
		// container caused git to walk up to the top-level workspace's
		// .git and commit "WIP: checkpoint (auto)" on the workspace's
		// branch (usually main) instead of the polecat's branch.
		// (gt-checkpoint-workdir fix.)
		workDir := resolveCheckpointWorkDir(polecatsDir, polecatName, rigName)
		if workDir == "" {
			continue // Neither layout has a usable .git — skip silently.
		}
		if d.checkpointWorktree(workDir, rigName, polecatName) {
			checkpointed++
		}
	}

	return scanned, checkpointed
}

// checkpointWorktree snapshots uncommitted work in a single worktree to a
// namespaced backup ref WITHOUT touching the worktree's branch tip, index, or
// working tree. Returns true if a new backup snapshot was created.
//
// gu-weo4x: checkpoint_dog used to `git add -A && git commit` directly on the
// polecat's branch, leaving a `WIP: checkpoint (auto)` commit as the branch
// tip. When a session ended abnormally that WIP commit looked like a real
// deliverable from the outside — the refinery could squash-merge it onto
// mainline, or the merge queue would pause "awaiting direction" for hours.
// The fix snapshots the same content (additions + modifications, runtime dirs
// and tracked-file deletions excluded) into refs/backup/<rig>/<polecat>/<tree>
// via a TEMPORARY index, so the branch tip stays exactly where the agent left
// it and the work is still fully recoverable from the backup ref.
func (d *Daemon) checkpointWorktree(workDir, rigName, polecatName string) bool {
	// Check git status — a clean worktree has nothing to back up.
	statusOut, err := runGitCmd(workDir, "status", "--porcelain")
	if err != nil {
		d.logger.Printf("checkpoint_dog: git status failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}
	if strings.TrimSpace(statusOut) == "" {
		return false // Clean worktree
	}

	ref, created, err := snapshotToBackupRef(workDir, rigName, polecatName)
	if err != nil {
		d.logger.Printf("checkpoint_dog: backup snapshot failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}
	if !created {
		// Either nothing survived the exclusion filter (only runtime/ephemeral
		// churn) or an identical snapshot already exists — no new backup needed.
		d.logger.Printf("checkpoint_dog: nothing new to back up in %s/%s", rigName, polecatName)
		return false
	}

	d.logger.Printf("checkpoint_dog: backed up WIP in %s/%s to %s (branch tip untouched)",
		rigName, polecatName, ref)
	return true
}

// snapshotToBackupRef captures the worktree's uncommitted work into a backup
// ref using a temporary index, leaving the real index, working tree, and
// branch tip untouched. It returns the ref name, whether a new snapshot was
// created, and any error.
//
// Content selection mirrors the historical checkpoint_dog behavior:
//   - stage everything (git add -A) into a TEMP index seeded from HEAD,
//   - unstage runtime/ephemeral directories (runtimeExcludeDirs),
//   - unstage deletions of tracked files (preserve work, never record deletes).
//
// The resulting tree is committed with commit-tree (parented on HEAD when one
// exists) and planted at refs/backup/<rig>/<polecat>/<treeSHA>. Naming the ref
// by the tree SHA makes the operation idempotent: if the worktree content has
// not changed since the last cycle, the same ref already points at that tree
// and we skip the write (created=false). When the snapshot tree equals HEAD's
// tree (only excluded churn was present) there is nothing to back up.
func snapshotToBackupRef(workDir, rigName, polecatName string) (string, bool, error) {
	// Temp index file: git writes the staging area here instead of .git/index,
	// so the worktree's real index is never disturbed. Created in the OS temp
	// dir (git does not require the index to live inside the repo).
	tmpIndex, err := os.CreateTemp("", "gt-checkpoint-index-*")
	if err != nil {
		return "", false, fmt.Errorf("create temp index: %w", err)
	}
	tmpPath := tmpIndex.Name()
	_ = tmpIndex.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	env := []string{"GIT_INDEX_FILE=" + tmpPath}

	// Resolve HEAD (may be absent in a brand-new repo with no commits).
	headSHA, headErr := runGitCmd(workDir, "rev-parse", "--verify", "HEAD")
	hasHead := headErr == nil && headSHA != ""

	// Seed the temp index from HEAD so `git add -A` produces a diff relative to
	// the last commit (matching the old commit-based behavior). Without a HEAD,
	// start from an empty index.
	if hasHead {
		if _, err := runGitCmdEnv(workDir, env, "read-tree", "HEAD"); err != nil {
			return "", false, fmt.Errorf("read-tree HEAD into temp index: %w", err)
		}
	} else {
		if _, err := runGitCmdEnv(workDir, env, "read-tree", "--empty"); err != nil {
			return "", false, fmt.Errorf("read-tree --empty into temp index: %w", err)
		}
	}

	// Stage everything into the temp index.
	if _, err := runGitCmdEnv(workDir, env, "add", "-A"); err != nil {
		return "", false, fmt.Errorf("git add -A (temp index): %w", err)
	}

	// Unstage runtime/ephemeral directories (safe even if absent).
	for _, dir := range runtimeExcludeDirs {
		_, _ = runGitCmdEnv(workDir, env, "reset", "--", dir)
	}

	// Unstage deletions of tracked files — a checkpoint preserves work
	// (additions + modifications), never records deletions of tracked files.
	if delOut, err := runGitCmdEnv(workDir, env, "diff", "--cached", "--name-only", "--diff-filter=D"); err == nil {
		if dels := strings.TrimSpace(delOut); dels != "" {
			for _, f := range strings.Split(dels, "\n") {
				if f != "" {
					_, _ = runGitCmdEnv(workDir, env, "reset", "--", f)
				}
			}
		}
	}

	// Write the staged tree.
	tree, err := runGitCmdEnv(workDir, env, "write-tree")
	if err != nil {
		return "", false, fmt.Errorf("write-tree (temp index): %w", err)
	}
	tree = strings.TrimSpace(tree)
	if tree == "" {
		return "", false, fmt.Errorf("write-tree returned empty tree")
	}

	// If the snapshot tree equals HEAD's tree, only excluded churn was present
	// — there is nothing new to back up.
	if hasHead {
		if headTree, err := runGitCmd(workDir, "rev-parse", "--verify", "HEAD^{tree}"); err == nil {
			if strings.TrimSpace(headTree) == tree {
				return "", false, nil
			}
		}
	}

	ref := fmt.Sprintf("refs/backup/%s/%s/%s",
		sanitizeBackupRefComponent(rigName),
		sanitizeBackupRefComponent(polecatName),
		tree)

	// Idempotency: if a backup ref for this exact tree already exists, the
	// content is unchanged since the last cycle — skip the redundant write.
	if existing, err := runGitCmd(workDir, "rev-parse", "--verify", "--quiet", ref); err == nil && strings.TrimSpace(existing) != "" {
		return ref, false, nil
	}

	// Commit the tree (parented on HEAD when available) so the snapshot is a
	// reachable commit object, then plant it at the backup ref.
	commitArgs := []string{"commit-tree", tree, "-m", "WIP: checkpoint (auto)"}
	if hasHead {
		commitArgs = append(commitArgs, "-p", headSHA)
	}
	commit, err := runGitCmd(workDir, commitArgs...)
	if err != nil {
		return "", false, fmt.Errorf("commit-tree: %w", err)
	}
	commit = strings.TrimSpace(commit)

	if _, err := runGitCmd(workDir, "update-ref", "-m", "checkpoint_dog autosave", ref, commit); err != nil {
		return "", false, fmt.Errorf("update-ref %s: %w", ref, err)
	}

	return ref, true, nil
}

// sanitizeBackupRefComponent maps an arbitrary string to a form safe to use as
// one segment of a git ref path: anything outside [A-Za-z0-9._-] becomes "-",
// and leading/trailing dots/dashes are trimmed (git refuses or special-cases
// them). Returns "unknown" for inputs that sanitize to empty so the ref path
// never contains an empty segment.
func sanitizeBackupRefComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return "unknown"
	}
	return out
}

// isGitWorktree reports whether the given directory is the root of a git
// worktree (has its own `.git` file or directory). Used to guard checkpoint
// commits against the "wrong-dir" failure mode where git operations in a
// non-worktree directory walk up the filesystem tree and commit on the
// parent workspace's branch.
func isGitWorktree(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// resolveCheckpointWorkDir picks the actual git-worktree directory for a
// polecat, supporting both the new nested layout (polecats/<name>/<rigName>/)
// and the legacy flat layout (polecats/<name>/) that polecat.Manager still
// recognizes for backward compatibility. Returns "" if neither candidate is
// a git worktree, in which case the caller MUST skip the polecat — never
// fall back to a parent directory, since git would walk up to the top-level
// workspace's .git and commit on the wrong branch (this is the bug this
// helper exists to prevent).
func resolveCheckpointWorkDir(polecatsDir, polecatName, rigName string) string {
	nested := filepath.Join(polecatsDir, polecatName, rigName)
	if isGitWorktree(nested) {
		return nested
	}
	flat := filepath.Join(polecatsDir, polecatName)
	if isGitWorktree(flat) {
		return flat
	}
	return ""
}

// runGitCmd executes a git command in the given directory and returns stdout.
func runGitCmd(workDir string, args ...string) (string, error) {
	return runGitCmdEnv(workDir, nil, args...)
}

// runGitCmdEnv is runGitCmd with extra environment variables appended to the
// process environment. Used by the backup-ref snapshot path to point git at a
// temporary index via GIT_INDEX_FILE so the worktree's real index is never
// touched. A nil/empty extraEnv inherits the parent environment unchanged.
func runGitCmdEnv(workDir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	util.SetDetachedProcessGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s: %s", err, errMsg)
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}

package daemon

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/util"
)

// guardDaemonCommit is a defensive pre-flight check for daemon-initiated
// git commits. It refuses to commit in any worktree whose current branch
// already exists at origin — preventing future regressions where a new
// daemon subsystem inadvertently commits to a shared branch on a rig
// root or on a polecat branch that has already been submitted to the
// merge queue.
//
// Semantics:
//   - On a named branch that exists at origin: return an error (refuse).
//   - On a named branch that does NOT exist at origin (e.g. a fresh
//     polecat branch before `gt done`): permit the commit.
//   - On detached HEAD: permit the commit (no branch to corrupt).
//   - If the branch-existence check cannot be performed (e.g. no origin
//     remote, git error): fail CLOSED — refuse the commit. A daemon
//     silently committing because we couldn't check is worse than a
//     daemon refusing to commit.
//
// Callers should log the refusal and skip the affected worktree. This
// guard is deliberately conservative: the cost of a skipped checkpoint
// is a single lost auto-save; the cost of a daemon commit on a shared
// branch is a merge-queue disruption or hours of confusion chasing a
// "WIP: checkpoint (auto)" commit that appeared out of nowhere.
func guardDaemonCommit(workDir string) error {
	branch, err := currentBranch(workDir)
	if err != nil {
		return fmt.Errorf("guardDaemonCommit: cannot determine current branch in %s: %w", workDir, err)
	}

	// Detached HEAD — no named branch to protect. Daemon may proceed.
	if branch == "" {
		return nil
	}

	exists, err := branchExistsAtOrigin(workDir, branch)
	if err != nil {
		// Fail closed: if we can't tell, refuse.
		return fmt.Errorf("guardDaemonCommit: cannot verify whether branch %q exists at origin in %s: %w",
			branch, workDir, err)
	}

	if exists {
		return fmt.Errorf("guardDaemonCommit: refusing to commit in %s: branch %q exists at origin "+
			"(daemon commits on shared branches cause merge-queue disruption)", workDir, branch)
	}

	return nil
}

// currentBranch returns the named branch of the worktree, or "" if HEAD
// is detached. An error is returned only on git invocation failure.
func currentBranch(workDir string) (string, error) {
	out, err := runGitOutput(workDir, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// branchExistsAtOrigin reports whether refs/heads/<branch> exists on the
// 'origin' remote. Uses 'git ls-remote' so the answer reflects the live
// remote state, not a possibly stale local tracking ref.
//
// Returns an error only if the remote check fails (no origin remote,
// network error, git invocation error). A missing ref is (false, nil).
func branchExistsAtOrigin(workDir, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}

	// 'git ls-remote --exit-code origin refs/heads/<branch>' prints the
	// ref if it exists (exit 0) or exits 2 if it doesn't. Any other
	// exit code is a real error (no origin remote, network failure).
	out, err := runGitOutput(workDir, "ls-remote", "--exit-code", "origin", "refs/heads/"+branch)
	if err != nil {
		// Distinguish "ref not found" (exit 2) from "remote error" (other).
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			return false, nil
		}
		return false, err
	}

	// Output is "<sha>\trefs/heads/<branch>" on success.
	return strings.TrimSpace(out) != "", nil
}

// runGitOutput runs a git command in workDir and returns its stdout. Used by
// the commit guard; kept separate from checkpoint_dog's runGitCmd to avoid
// import cycles during testing and to make the guard self-contained.
func runGitOutput(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	util.SetDetachedProcessGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%w: %s", err, errMsg)
		}
		return "", err
	}
	return stdout.String(), nil
}

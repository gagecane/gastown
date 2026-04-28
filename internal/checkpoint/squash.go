package checkpoint

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/util"
)

// WIPCommitPrefix is the commit message prefix used by checkpoint_dog auto-commits.
const WIPCommitPrefix = "WIP: checkpoint (auto)"

// CountWIPCommits returns the number of WIP checkpoint commits between
// the merge-base of baseRef and HEAD.
func CountWIPCommits(workDir, baseRef string) (int, error) {
	mergeBase, err := gitOutput(workDir, "merge-base", baseRef, "HEAD")
	if err != nil {
		return 0, fmt.Errorf("finding merge-base: %w", err)
	}

	// List commit subjects from merge-base..HEAD
	logOut, err := gitOutput(workDir, "log", "--format=%s", mergeBase+"..HEAD")
	if err != nil {
		return 0, fmt.Errorf("listing commits: %w", err)
	}

	if logOut == "" {
		return 0, nil
	}

	count := 0
	for _, line := range strings.Split(logOut, "\n") {
		if strings.HasPrefix(line, WIPCommitPrefix) {
			count++
		}
	}
	return count, nil
}

// SquashWIPCommits collapses all commits from merge-base..HEAD into a single
// commit, preserving non-WIP commit messages in the body. Returns the number
// of WIP commits that were squashed.
//
// This is safe because Refinery squash-merges polecat branches anyway —
// individual commit history on polecat branches is not preserved.
func SquashWIPCommits(workDir, baseRef string) (int, error) {
	mergeBase, err := gitOutput(workDir, "merge-base", baseRef, "HEAD")
	if err != nil {
		return 0, fmt.Errorf("finding merge-base: %w", err)
	}

	// List commit subjects from merge-base..HEAD
	logOut, err := gitOutput(workDir, "log", "--format=%s", mergeBase+"..HEAD")
	if err != nil {
		return 0, fmt.Errorf("listing commits: %w", err)
	}

	if logOut == "" {
		return 0, nil // No commits to squash
	}

	subjects := strings.Split(logOut, "\n")

	// Count WIP commits
	wipCount := 0
	var nonWIPSubjects []string
	for _, subj := range subjects {
		if strings.HasPrefix(subj, WIPCommitPrefix) {
			wipCount++
		} else if subj != "" {
			nonWIPSubjects = append(nonWIPSubjects, subj)
		}
	}

	if wipCount == 0 {
		return 0, nil // No WIP commits to squash
	}

	// Soft-reset to merge-base (preserves all changes as staged)
	if _, err := gitOutput(workDir, "reset", "--soft", mergeBase); err != nil {
		return 0, fmt.Errorf("soft reset: %w", err)
	}

	// Build combined commit message
	var msg strings.Builder
	if len(nonWIPSubjects) > 0 {
		// Use first non-WIP subject as the title
		msg.WriteString(nonWIPSubjects[0])
		if len(nonWIPSubjects) > 1 {
			msg.WriteString("\n")
			for _, subj := range nonWIPSubjects[1:] {
				msg.WriteString("\n- ")
				msg.WriteString(subj)
			}
		}
	} else {
		// All commits were WIP — use a generic message
		msg.WriteString("squashed WIP checkpoint commits")
	}

	// Commit with combined message
	if _, err := gitOutput(workDir, "commit", "-m", msg.String()); err != nil {
		return 0, fmt.Errorf("squash commit: %w", err)
	}

	return wipCount, nil
}

// BestCommitMessage returns the full commit message (%B) of the most
// recent non-WIP commit on `branch` relative to `baseRef`, i.e. the most
// recent commit in merge-base(baseRef, branch)..branch whose subject does
// NOT start with WIPCommitPrefix.
//
// If no non-WIP commit exists in that range, it returns ("", nil) so the
// caller can fall back to its own default message.
//
// Intended use: when squash-merging a polecat branch into mainline, the
// refinery copies the branch-tip commit message onto the squash commit.
// If the tip happens to be a `WIP: checkpoint (auto)` commit produced by
// checkpoint_dog (e.g. the polecat crashed before committing real work,
// or ran `gt done` on a branch whose tip was a WIP), the merge commit
// message ends up as "WIP: checkpoint (auto)" on mainline — the gu-zd2
// incident. Walking back past WIP tips selects a clean conventional
// commit message instead.
//
// This does NOT rewrite history — it only picks a better message. The
// squash-merge itself still captures the full branch diff.
func BestCommitMessage(workDir, branch, baseRef string) (string, error) {
	mergeBase, err := gitOutput(workDir, "merge-base", baseRef, branch)
	if err != nil {
		return "", fmt.Errorf("finding merge-base: %w", err)
	}

	// List commits in mergeBase..branch, newest-first, with a NUL record
	// separator so multi-line commit bodies can be parsed unambiguously.
	// Format: "<subject>\n<body>" per commit, records separated by \x00.
	rev := mergeBase + ".." + branch
	logOut, err := gitOutput(workDir, "log", "--format=%B%x00", rev)
	if err != nil {
		return "", fmt.Errorf("listing commits: %w", err)
	}
	if logOut == "" {
		return "", nil
	}

	// Split on NUL. `git log` emits the records in reverse-chronological
	// order (newest first), which is exactly what we want — the first
	// non-WIP record we encounter is the newest non-WIP commit.
	for _, rec := range strings.Split(logOut, "\x00") {
		msg := strings.TrimSpace(rec)
		if msg == "" {
			continue
		}
		// The subject is the first line.
		subj := msg
		if idx := strings.IndexByte(msg, '\n'); idx != -1 {
			subj = msg[:idx]
		}
		if !strings.HasPrefix(subj, WIPCommitPrefix) {
			return msg, nil
		}
	}

	// All commits in the range were WIP checkpoints.
	return "", nil
}

// gitOutput runs a git command and returns trimmed stdout.
func gitOutput(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	util.SetDetachedProcessGroup(cmd)

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return "", fmt.Errorf("%s: %s", err, stderr)
			}
		}
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}

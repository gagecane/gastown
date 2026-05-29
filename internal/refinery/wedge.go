// Package refinery — wedge.go
//
// Detection and recovery for the "reaped temp upstream" wedge.
//
// Symptom (gu-xn2z incident, 2026-05-29 ~20:30):
//
// The refinery role agent uses a `temp` branch as its working ref while
// rebasing each MR onto the resolved merge target (see refinery role
// template: `git checkout -b temp polecat/<worker>`). After a successful
// merge, the refinery's post-merge cleanup deletes the polecat branch
// from origin (DeleteRemoteBranch) — but git, looking up `branch.temp.merge`
// in config, still believes `temp` tracks `origin/polecat/<worker>`.
// The next cycle's `git status` reports:
//
//	On branch temp
//	Your branch is based on 'origin/polecat/...', but the upstream is gone.
//
// Operations that depend on `@{u}` (rebase, pull, push without explicit
// refspec) then fail or behave unexpectedly. Refinery wedges, MRs back up,
// and the wedge survives session restarts because it's persisted in
// .git/config.
//
// Recovery: detect "temp branch with reaped upstream" and:
//  1. Unset the upstream pointer on `temp`.
//  2. Reset the worktree to origin/<defaultBranch>.
//
// Bead: gu-hlie. Parent incident: gu-xn2z. Sibling: gu-rh0g (Pattern B
// push-failure guard, shipped 91361f99).
package refinery

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// WedgeStatus describes the worktree's wedge state for a refinery rig.
type WedgeStatus struct {
	// WorktreePath is the absolute path to the worktree that was inspected.
	WorktreePath string

	// Exists reports whether the worktree directory exists. If false, no
	// wedge is possible (Manager.Start auto-repairs missing worktrees from
	// the bare repo before this check would even run).
	Exists bool

	// CurrentBranch is the worktree's current branch (or "HEAD" when detached).
	CurrentBranch string

	// HasTempBranch reports whether a local branch named "temp" exists in
	// the worktree. The refinery role template explicitly creates and
	// deletes `temp` once per merge cycle.
	HasTempBranch bool

	// TempUpstream is the configured upstream of `temp` (e.g.
	// "refs/heads/polecat/fury/gu-b3ek--mprcyssb"). Empty when temp has no
	// upstream configured or no temp branch exists.
	TempUpstream string

	// TempUpstreamGone reports whether `temp` has an upstream configured
	// AND that upstream branch is missing on origin (the canonical "wedge"
	// state). When true, the next refinery cycle is at risk.
	TempUpstreamGone bool

	// Reason is a human-readable explanation of the status.
	Reason string
}

// Wedged reports whether the worktree is in the wedge state.
func (s WedgeStatus) Wedged() bool {
	return s.TempUpstreamGone
}

// DetectWedge inspects a refinery worktree and reports any post-merge
// upstream-gone wedge. The check is read-only; no git mutations occur.
//
// The worktree is the path Manager.Start uses for the refinery agent
// (typically <rig>/refinery/rig). For rigs that fall back to mayor/rig
// (legacy / standard-clone layout), pass that path instead.
func DetectWedge(worktree string) (WedgeStatus, error) {
	st := WedgeStatus{WorktreePath: worktree}

	if _, err := os.Stat(worktree); err != nil {
		if os.IsNotExist(err) {
			st.Reason = "worktree directory does not exist"
			return st, nil
		}
		return st, fmt.Errorf("stat worktree: %w", err)
	}
	st.Exists = true

	g := git.NewGit(worktree)
	if !g.IsRepo() {
		st.Reason = "worktree is not a git repository"
		return st, nil
	}

	branch, err := g.CurrentBranch()
	if err != nil {
		return st, fmt.Errorf("read current branch: %w", err)
	}
	st.CurrentBranch = strings.TrimSpace(branch)

	hasTemp, err := g.BranchExists("temp")
	if err != nil {
		return st, fmt.Errorf("check temp branch: %w", err)
	}
	st.HasTempBranch = hasTemp
	if !hasTemp {
		st.Reason = "no temp branch — clean state"
		return st, nil
	}

	// Look up the configured upstream of temp directly via config to avoid
	// rev-parse @{u} ambiguity on detached / unborn states. branch.temp.merge
	// holds the symbolic ref name when set.
	upstream, _ := g.ConfigGet("branch.temp.merge")
	upstream = strings.TrimSpace(upstream)
	st.TempUpstream = upstream
	if upstream == "" {
		st.Reason = "temp branch has no upstream — not wedged"
		return st, nil
	}

	// Strip refs/heads/ prefix to get the bare branch name for ls-remote.
	branchName := strings.TrimPrefix(upstream, "refs/heads/")
	exists, err := g.RemoteBranchExists("origin", branchName)
	if err != nil {
		// ls-remote failures are operational — not the wedge condition.
		// Surface as Reason but do not claim wedge.
		st.Reason = fmt.Sprintf("could not query origin for %s: %v", branchName, err)
		return st, nil
	}
	if exists {
		st.Reason = fmt.Sprintf("temp tracks %s, still present on origin", branchName)
		return st, nil
	}

	st.TempUpstreamGone = true
	st.Reason = fmt.Sprintf("WEDGED: temp tracks %s but origin has reaped it", branchName)
	return st, nil
}

// UnwedgeWorktree clears the reaped-upstream wedge by:
//  1. Switching off the temp branch (checkout default branch).
//  2. Force-deleting `temp` so any stale upstream config goes with it.
//  3. Resetting the worktree to origin/<defaultBranch>.
//
// Returns nil if the worktree was not wedged (idempotent no-op). Writes
// progress messages to out (use io.Discard for silent operation).
//
// Caller should provide the rig-resolved default branch (e.g. "main"). If
// defaultBranch is empty, the worktree's git default branch is used.
func UnwedgeWorktree(worktree, defaultBranch string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	st, err := DetectWedge(worktree)
	if err != nil {
		return fmt.Errorf("detect wedge: %w", err)
	}
	if !st.Wedged() {
		return nil
	}

	g := git.NewGit(worktree)
	if defaultBranch == "" {
		defaultBranch = g.RemoteDefaultBranch()
	}
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	_, _ = fmt.Fprintf(out, "[Refinery] Unwedging worktree %s (temp tracked reaped %s)\n",
		worktree, st.TempUpstream)

	// Move off temp so it can be deleted. Checkout the default branch; if
	// that fails (e.g. local branch is missing or behind), fall back to a
	// detached HEAD on origin/<default>.
	if err := g.Checkout(defaultBranch); err != nil {
		_, _ = fmt.Fprintf(out, "[Refinery] Warning: checkout %s failed (%v), detaching HEAD instead\n",
			defaultBranch, err)
		if detachErr := g.Checkout("origin/" + defaultBranch); detachErr != nil {
			return fmt.Errorf("checkout away from temp: %w (detach also failed: %v)", err, detachErr)
		}
	}

	// Force-delete temp. This drops the stale branch.<name>.{remote,merge}
	// config along with the ref.
	if err := g.DeleteBranch("temp", true); err != nil {
		return fmt.Errorf("delete temp branch: %w", err)
	}

	// Sync the default branch to origin so the next refinery cycle starts
	// from a clean baseline.
	if err := g.ResetHard("origin/" + defaultBranch); err != nil {
		_, _ = fmt.Fprintf(out, "[Refinery] Warning: reset --hard origin/%s failed: %v\n",
			defaultBranch, err)
	}

	_, _ = fmt.Fprintf(out, "[Refinery] Unwedged: temp deleted, worktree reset to origin/%s\n",
		defaultBranch)
	return nil
}

// RefineryWorktreePath returns the path Manager.Start uses for the
// refinery worktree on this rig. Mirrors the resolution logic in
// Manager.Start so callers (e.g. `gt refinery diagnose`) inspect the
// same directory the running refinery agent uses.
//
// Resolution order:
//  1. <rig>/refinery/rig if it exists.
//  2. <rig>/mayor/rig as fallback for rigs that use a standard .git layout
//     (no .repo.git bare repo) or where the refinery worktree was not
//     repaired.
func RefineryWorktreePath(r *rig.Rig) string {
	refineryRig := filepath.Join(r.Path, "refinery", "rig")
	if _, err := os.Stat(refineryRig); err == nil {
		return refineryRig
	}
	return filepath.Join(r.Path, "mayor", "rig")
}

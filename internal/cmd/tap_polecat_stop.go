package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/workspace"
)

var tapPolecatStopCmd = &cobra.Command{
	Use:   "polecat-stop-check",
	Short: "Auto-run gt done on session Stop if polecat has pending work",
	Long: `Safety net for the "idle polecat" problem: polecats that finish work
but forget to call gt done before the session ends.

This command is designed to run from a Claude Code Stop hook. It checks:
1. Whether this is a polecat session (GT_POLECAT env var)
2. Whether gt done has already run (heartbeat state is "exiting" or "idle")
3. Whether the polecat has commits on its branch

Behavior:
- Commits on a feature branch → run gt done (submits to merge queue)
- Zero commits, or still on main/master/HEAD → run gt done --status DEFERRED
  (closes the bead and releases the hook; prevents polecats from exiting
  silently with their bead stuck in HOOKED state — gu-rhdt / gt-s2r96)
- gt done already ran (heartbeat "exiting" or "idle") → exit silently
- Not a polecat / can't determine state → exit silently

Exit codes:
  0 - No action needed (not a polecat, already done, or gt done succeeded)
  1 - gt done was attempted but failed`,
	RunE:         runTapPolecatStop,
	SilenceUsage: true,
}

func init() {
	tapCmd.AddCommand(tapPolecatStopCmd)
}

func runTapPolecatStop(cmd *cobra.Command, args []string) error {
	// Only applies to polecats
	polecatName := os.Getenv("GT_POLECAT")
	if polecatName == "" {
		return nil // Not a polecat session — nothing to do
	}

	sessionName := os.Getenv("GT_SESSION")
	if sessionName == "" {
		return nil // No session tracking — can't check state
	}

	// Find town root for heartbeat check
	townRoot, _, _ := workspace.FindFromCwdWithFallback()
	if townRoot == "" {
		townRoot = os.Getenv("GT_TOWN_ROOT")
	}
	if townRoot == "" {
		return nil // Can't find workspace — exit quietly
	}

	// Check heartbeat state: if already "exiting" or "idle", gt done already ran
	hb := polecat.ReadSessionHeartbeat(townRoot, sessionName)
	if hb != nil {
		state := hb.EffectiveState()
		if state == polecat.HeartbeatExiting || state == polecat.HeartbeatIdle {
			return nil // gt done already ran or polecat is idle — nothing to do
		}
	}

	// Check if the polecat is on a feature branch with commits
	rigName := os.Getenv("GT_RIG")
	if rigName == "" {
		return nil
	}

	// Reconstruct polecat worktree path
	polecatDir := filepath.Join(townRoot, rigName, "polecats", polecatName)
	// Try the nested clone layout first (polecats/<name>/<rig>/)
	cloneDir := filepath.Join(polecatDir, rigName)
	if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err != nil {
		// Fall back to flat layout
		cloneDir = polecatDir
		if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err != nil {
			return nil // No git repo found — exit quietly
		}
	}

	// Check current branch
	branchCmd := exec.Command("git", "-C", cloneDir, "rev-parse", "--abbrev-ref", "HEAD")
	branchOut, err := branchCmd.Output()
	if err != nil {
		return nil // Can't determine branch — exit quietly
	}
	branch := strings.TrimSpace(string(branchOut))
	onDefaultBranch := branch == "main" || branch == "master" || branch == "HEAD"

	// Check for commits ahead of origin/main.
	// We still check even on the default branch, because we want a single
	// decision path: zero commits → DEFERRED, nonzero commits → normal done.
	aheadCmd := exec.Command("git", "-C", cloneDir, "rev-list", "--count", "origin/main..HEAD")
	aheadOut, aheadErr := aheadCmd.Output()
	if aheadErr != nil {
		return nil // Can't check — exit quietly (don't block session stop)
	}
	ahead := strings.TrimSpace(string(aheadOut))

	// Find gt binary path
	gtBin, execErr := os.Executable()
	if execErr != nil || gtBin == "" {
		gtBin = "gt"
	}

	if onDefaultBranch || ahead == "0" {
		// Zero-commit safety net: polecat sat at prompt without running gt done.
		// Auto-run `gt done --status DEFERRED` so the bead is closed and the
		// slot is freed, rather than letting the session exit silently with
		// the bead stuck in HOOKED state (gu-rhdt / gt-s2r96).
		reason := "on default branch"
		if !onDefaultBranch {
			reason = fmt.Sprintf("0 commits ahead of origin/main on %s", branch)
		}
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "⚠️  Polecat %s exited with no work to submit (%s)\n", polecatName, reason)
		fmt.Fprintf(os.Stderr, "   Auto-running gt done --status DEFERRED to release the hook...\n")
		fmt.Fprintf(os.Stderr, "\n")

		deferredCmd := exec.Command(gtBin, "done", "--status", "DEFERRED")
		deferredCmd.Dir = cloneDir
		deferredCmd.Stdout = os.Stdout
		deferredCmd.Stderr = os.Stderr
		deferredCmd.Env = os.Environ()
		if err := deferredCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Auto gt done --status DEFERRED failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "   Witness will handle cleanup.\n")
			// Don't return error — don't block session stop
		}
		return nil
	}

	// Polecat has pending work! Run gt done as a safety net.
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "⚠️  Polecat %s has %s unpushed commit(s) on branch %s\n", polecatName, ahead, branch)
	fmt.Fprintf(os.Stderr, "   Auto-running gt done as safety net...\n")
	fmt.Fprintf(os.Stderr, "\n")

	// Run gt done in the polecat's worktree context
	doneCmd := exec.Command(gtBin, "done")
	doneCmd.Dir = cloneDir
	doneCmd.Stdout = os.Stdout
	doneCmd.Stderr = os.Stderr
	// Inherit environment (GT_POLECAT, GT_RIG, etc. are already set)
	doneCmd.Env = os.Environ()

	if err := doneCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Auto gt done failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "   Witness will handle cleanup.\n")
		// Don't return error — don't block session stop
		return nil
	}

	return nil
}

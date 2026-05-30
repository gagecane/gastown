package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	pruneBranchesDryRun  bool
	pruneBranchesPattern string
)

var pruneBranchesCmd = &cobra.Command{
	Use:     "prune-branches",
	GroupID: GroupWork,
	Short:   "Remove stale local polecat tracking branches",
	Long: `Remove local branches that were created when tracking remote polecat branches.

When polecats push branches to origin, other clones create local tracking
branches via git fetch. After the remote branch is deleted (post-merge),
git fetch --prune removes the remote tracking ref but the local branch
persists indefinitely.

This command finds and removes local branches matching the pattern (default:
polecat/*) that are either:
  - Fully merged to the repository's default branch
  - Have no corresponding remote tracking branch (remote was deleted)

Safety: Uses git branch -d (not -D) so only fully-merged branches are deleted.
Never deletes the current branch or the default branch.

Examples:
  gt prune-branches              # Clean up stale polecat branches
  gt prune-branches --dry-run    # Show what would be deleted
  gt prune-branches --pattern "feature/*"  # Custom pattern`,
	RunE: runPruneBranches,
}

func init() {
	pruneBranchesCmd.Flags().BoolVar(&pruneBranchesDryRun, "dry-run", false, "Show what would be deleted without deleting")
	pruneBranchesCmd.Flags().StringVar(&pruneBranchesPattern, "pattern", "polecat/*", "Branch name pattern to match")

	rootCmd.AddCommand(pruneBranchesCmd)
}

func runPruneBranches(cmd *cobra.Command, args []string) error {
	g := gitpkg.NewGit(".")
	if !g.IsRepo() {
		return fmt.Errorf("not a git repository")
	}

	// Resolve the rig's default branch for accurate user-facing messages
	// (gu-hg3t: don't hardcode "main" — rigs may default to "master" or other).
	defaultBranch := g.RemoteDefaultBranch()
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	// Run fetch --prune first to clean up stale remote tracking refs
	if err := g.FetchPrune("origin"); err != nil {
		// Non-fatal: we can still prune based on current state
		fmt.Printf("%s Warning: git fetch --prune failed: %v\n", style.Warning.Render("⚠"), err)
	}

	// Load the rig's configured default_branch so merge-detection is correct for
	// non-main integration branches (e.g. gagecane/gt). Falls back to remote
	// default branch when rig config is unavailable. (hq-dlksi)
	var baseBranch string
	if townRoot, wsErr := workspace.FindFromCwd(); wsErr == nil {
		cwd, _ := filepath.Abs(".")
		if relPath, relErr := filepath.Rel(townRoot, cwd); relErr == nil {
			parts := strings.Split(relPath, string(filepath.Separator))
			if len(parts) > 0 && parts[0] != "" && parts[0] != "." {
				rigName := parts[0]
				if rigCfg, cfgErr := rig.LoadRigConfig(filepath.Join(townRoot, rigName)); cfgErr == nil {
					baseBranch = rigCfg.DefaultBranch
				}
			}
		}
	}

	pruned, err := g.PruneStaleBranches(pruneBranchesPattern, pruneBranchesDryRun, baseBranch)
	if err != nil {
		return fmt.Errorf("pruning branches: %w", err)
	}

	if len(pruned) == 0 {
		fmt.Printf("%s No stale branches found matching %q\n", style.Bold.Render("✓"), pruneBranchesPattern)
		return nil
	}

	if pruneBranchesDryRun {
		fmt.Printf("%s Would prune %d branch(es):\n\n", style.Warning.Render("⚠"), len(pruned))
	} else {
		fmt.Printf("%s Pruned %d branch(es):\n\n", style.Bold.Render("✓"), len(pruned))
	}

	for _, b := range pruned {
		reasonStr := ""
		switch b.Reason {
		case "merged":
			reasonStr = fmt.Sprintf("merged to %s", defaultBranch)
		case "no-remote":
			reasonStr = "remote branch deleted"
		case "no-remote-merged":
			reasonStr = fmt.Sprintf("remote deleted, merged to %s", defaultBranch)
		}
		fmt.Printf("  %s %s (%s)\n",
			style.Dim.Render("•"),
			b.Name,
			style.Dim.Render(reasonStr))
	}
	fmt.Println()

	return nil
}

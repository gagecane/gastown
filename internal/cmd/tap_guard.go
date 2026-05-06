package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var tapGuardCmd = &cobra.Command{
	Use:   "guard",
	Short: "Block forbidden operations (PreToolUse hook)",
	Long: `Block forbidden operations via Claude Code PreToolUse hooks.

Guard commands exit with code 2 to BLOCK tool execution when a policy
is violated. They're called before the tool runs, preventing the
forbidden operation entirely.

Available guards:
  pr-workflow        - Block PR creation and feature branches
  bd-init            - Block bd init in wrong directories
  mol-patrol         - Block mol patrol from agent contexts
  dangerous-command  - Block rm -rf, force push, hard reset, git clean

External guards (standalone scripts, not compiled into gt):
  context-budget   - scripts/guards/context-budget-guard.sh

Example hook configuration:
  {
    "PreToolUse": [{
      "matcher": "Bash(gh pr create*)",
      "hooks": [{"command": "gt tap guard pr-workflow"}]
    }]
  }`,
}

var tapGuardPRWorkflowCmd = &cobra.Command{
	Use:   "pr-workflow",
	Short: "Block PR creation and feature branches",
	Long: `Block PR workflow operations in Gas Town.

Gas Town workers push directly to main. PRs add friction that breaks
the autonomous execution model (GUPP principle).

This guard blocks:
  - gh pr create
  - git checkout -b (feature branches)
  - git switch -c (feature branches)

Exit codes:
  0 - Operation allowed (not in Gas Town agent context, not maintainer origin)
  2 - Operation BLOCKED (in agent context OR maintainer origin)

The guard blocks in two scenarios:
  1. Running as a Gas Town agent (crew, polecat, witness, etc.)
  2. Origin remote is steveyegge/gastown (maintainer should push directly)

Humans running outside Gas Town with a fork origin can still use PRs.`,
	RunE: runTapGuardPRWorkflow,
}

func init() {
	tapCmd.AddCommand(tapGuardCmd)
	tapGuardCmd.AddCommand(tapGuardPRWorkflowCmd)
}

func runTapGuardPRWorkflow(cmd *cobra.Command, args []string) error {
	// Allow Refinery to run branch/push operations (its job is to rebase polecat
	// branches via `git checkout -b` and push the result to main).
	if os.Getenv("GT_REFINERY") != "" {
		return nil
	}

	// Read hook input from stdin (Claude Code / kiro-cli protocol)
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil // fail open
	}

	command := extractCommandFromHookInput(input)
	if command == "" {
		return nil // no command to inspect — allow
	}

	// Check if this command matches a forbidden PR/branch pattern
	if reason := matchesPRWorkflowPattern(command); reason != "" {
		// Only block if we're in agent context OR maintainer origin
		if isGasTownAgentContext() {
			printPRWorkflowBlock(reason)
			return NewSilentExit(2)
		}
		if isMaintainerOrigin() {
			printMaintainerBlock()
			return NewSilentExit(2)
		}
	}

	return nil
}

// prWorkflowPatterns defines commands that are forbidden in PR workflow.
var prWorkflowPatterns = []struct {
	match func(string) bool
	reason string
}{
	{matchesGhPrCreate, "gh pr create — Gas Town workers push directly to main"},
	{matchesGitNewBranch, "feature branch creation — Gas Town workers push directly to main"},
}

// matchesPRWorkflowPattern returns a reason string if the command matches a
// forbidden PR workflow pattern, or empty string if allowed.
func matchesPRWorkflowPattern(command string) string {
	lower := strings.ToLower(command)
	for _, p := range prWorkflowPatterns {
		if p.match(lower) {
			return p.reason
		}
	}
	return ""
}

// matchesGhPrCreate detects "gh pr create" commands.
func matchesGhPrCreate(lower string) bool {
	fields := strings.Fields(lower)
	for i := 0; i+2 < len(fields); i++ {
		if fields[i] == "gh" && fields[i+1] == "pr" && fields[i+2] == "create" {
			return true
		}
	}
	return false
}

// matchesGitNewBranch detects "git checkout -b" and "git switch -c" (new branch creation).
// Does NOT block "git checkout main" or "git switch main".
func matchesGitNewBranch(lower string) bool {
	fields := strings.Fields(lower)
	for i := 0; i+2 < len(fields); i++ {
		if fields[i] == "git" && fields[i+1] == "checkout" && fields[i+2] == "-b" {
			return true
		}
		if fields[i] == "git" && fields[i+1] == "switch" && fields[i+2] == "-c" {
			return true
		}
	}
	return false
}

func printPRWorkflowBlock(reason string) {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(os.Stderr, "║  ❌ PR WORKFLOW BLOCKED                                          ║")
	fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════════╣")
	fmt.Fprintf(os.Stderr,  "║  %-63s ║\n", truncateStr(reason, 63))
	fmt.Fprintln(os.Stderr, "║                                                                  ║")
	fmt.Fprintln(os.Stderr, "║  Instead:  git add . && git commit && git push origin main       ║")
	fmt.Fprintln(os.Stderr, "║  See: ~/gt/docs/PRIMING.md (GUPP principle)                      ║")
	fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr, "")
}

func printMaintainerBlock() {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(os.Stderr, "║  ❌ PR BLOCKED - MAINTAINER ORIGIN                               ║")
	fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════════╣")
	fmt.Fprintln(os.Stderr, "║  Your origin is steveyegge/gastown - push directly to main.     ║")
	fmt.Fprintln(os.Stderr, "║  PRs are for external contributors, not maintainers.            ║")
	fmt.Fprintln(os.Stderr, "║                                                                  ║")
	fmt.Fprintln(os.Stderr, "║  Instead:  git push origin main                                 ║")
	fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr, "")
}

// isGasTownAgentContext returns true if we're running as a Gas Town managed agent.
func isGasTownAgentContext() bool {
	// Check environment variables set by Gas Town session management
	envVars := []string{
		"GT_POLECAT",
		"GT_CREW",
		"GT_WITNESS",
		"GT_REFINERY",
		"GT_MAYOR",
		"GT_DEACON",
	}
	for _, env := range envVars {
		if os.Getenv(env) != "" {
			return true
		}
	}

	// Also check if we're in a crew or polecat worktree by path
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}

	agentPaths := []string{"/crew/", "/polecats/"}
	for _, path := range agentPaths {
		if strings.Contains(cwd, path) {
			return true
		}
	}

	return false
}

// isMaintainerOrigin returns true if the origin remote points to the maintainer's repo.
// This prevents the maintainer from accidentally creating PRs in their own repo.
func isMaintainerOrigin() bool {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	url := strings.TrimSpace(string(output))
	// Match both HTTPS and SSH URL formats:
	// - https://github.com/steveyegge/gastown.git
	// - git@github.com:steveyegge/gastown.git
	return strings.Contains(url, "steveyegge/gastown")
}

// auto-test-pr CLI: check-magic-phrase verb.
//
// Phase 2 task 20 (gu-hqe16). This verb is the machine-callable entry
// point for mol-pr-feedback-patrol's magic-phrase scanning step. The
// patrol passes each comment body through this command; if the exact
// token is present, the command writes a 7-day pause to the rig's
// state bead and exits 0 with a confirmation message. If not present,
// it exits 0 silently (no match is not an error).
//
// Design context: .designs/auto-test-pr/synthesis.md §D9
package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/autotestpr"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/workspace"
)

// CLI flag bindings for check-magic-phrase.
var (
	autoTestPRMagicPhraseBody string
	autoTestPRMagicPhraseRig  string
)

var autoTestPRCheckMagicPhraseCmd = &cobra.Command{
	Use:   "check-magic-phrase",
	Short: "Check a comment body for the reviewer pause magic phrase (D9)",
	Long: `Check whether a comment body contains the exact magic phrase
token that triggers a 7-day rig pause.

If the phrase is found, a 7-day pause is written to the rig's state
bead and a confirmation is printed. If not found, the command exits
silently with status 0 (no match is not an error).

This command is designed for machine callers (mol-pr-feedback-patrol).
Human operators should use ` + "`gt auto-test-pr pause --rig=<rig>`" + ` instead.

The magic phrase is: ` + "`" + autotestpr.MagicPhrase + "`" + `

Examples:

  gt auto-test-pr check-magic-phrase --body="gt auto-test-pr: pause-rig-7d" --rig=gastown_upstream
  echo "$COMMENT_BODY" | gt auto-test-pr check-magic-phrase --body="$(cat)" --rig=gastown_upstream`,
	Args:          cobra.NoArgs,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRCheckMagicPhrase,
}

func init() {
	autoTestPRCheckMagicPhraseCmd.Flags().StringVar(&autoTestPRMagicPhraseBody, "body", "",
		"Comment body text to scan for the magic phrase (required)")
	autoTestPRCheckMagicPhraseCmd.Flags().StringVar(&autoTestPRMagicPhraseRig, "rig", "",
		"Target rig to pause if the magic phrase is found (required)")

	autoTestPRCmd.AddCommand(autoTestPRCheckMagicPhraseCmd)
}

func runAutoTestPRCheckMagicPhrase(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	if autoTestPRMagicPhraseBody == "" {
		fmt.Fprintln(stderr, "gt auto-test-pr check-magic-phrase: --body is required")
		return NewSilentExit(2)
	}
	if autoTestPRMagicPhraseRig == "" {
		fmt.Fprintln(stderr, "gt auto-test-pr check-magic-phrase: --rig is required")
		return NewSilentExit(2)
	}

	if !autotestpr.ContainsMagicPhrase(autoTestPRMagicPhraseBody) {
		// No match — silent exit. This is the common path: most comments
		// don't contain the magic phrase.
		return nil
	}

	// Magic phrase detected — write the 7-day rig pause.
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("locating town root: %w", err)
	}

	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
	now := nowFn()

	if err := autotestpr.ApplyMagicPhrasePause(bd, autoTestPRMagicPhraseRig, now); err != nil {
		return fmt.Errorf("applying magic-phrase pause to rig %s: %w", autoTestPRMagicPhraseRig, err)
	}

	pauseUntil := now.Add(autotestpr.MagicPhrasePauseDuration)
	fmt.Fprintf(stdout, "✓ magic phrase detected — rig %s paused until %s (7d)\n",
		autoTestPRMagicPhraseRig, pauseUntil.UTC().Format(time.RFC3339))
	return nil
}

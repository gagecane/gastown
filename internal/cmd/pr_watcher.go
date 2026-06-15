// Package cmd: pr_watcher.go implements the `gt pr-watcher` command tree for
// the GitHub PR reviewer-comment → autonomous-action poller (gc-nfoah7).
//
// `gt pr-watcher poll`   — one-shot poll-and-react. Suitable cron / deacon /
//
//	plugin target. Fetches unresolved review comments on the
//	rig's open PRs, triages each, and either auto-dispatches
//	(mechanical) or gates for human confirmation (judgment).
//
// `gt pr-watcher status` — print how many comments have been actioned for a rig
//
//	(the size of the actioned-comments ledger).
//
// Modeled on `gt ci-watcher`; see internal/prwatcher/doc.go.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/prwatcher"
	"github.com/steveyegge/gastown/internal/workspace"
)

// pr-watcher command flags.
var (
	prWatcherRig  string
	prWatcherJSON bool
)

var prWatcherCmd = &cobra.Command{
	Use:     "pr-watcher",
	GroupID: GroupServices,
	Short:   "GitHub PR reviewer-comment poller (turns review comments into Gas Town work)",
	Long: `GitHub PR reviewer-comment → autonomous-action poller.

For Gas Town instances that push via pull requests, a human reviewer leaves
review comments on a PR and someone has to read each one and dispatch the fix.
The pr-watcher closes that gap. On each poll it:

  1. Fetches UNRESOLVED review comments on the rig's open PRs (via the gh CLI).
  2. Dedups against an on-disk ledger so a comment is actioned at most once.
  3. Triages each fresh comment (heuristic, gate-by-default):
     - Mechanical (typo, gofmt, rename, formatting) → create a bead and sling a
       fresh polecat to fix and re-push.
     - Judgment (everything else) → create a bead labeled needs-human-triage
       and mail the mayor for confirmation; NOT auto-dispatched.
  4. Posts an ack reply on the PR so the reviewer sees it was picked up.

This command does not run as a daemon; invoke 'poll' from a plugin or patrol on
the desired cadence (every few minutes is typical).`,
	RunE: requireSubcommand,
}

var prWatcherPollCmd = &cobra.Command{
	Use:   "poll",
	Short: "Run one poll cycle: fetch unresolved PR comments, triage, dispatch",
	Long: `Fetch unresolved review comments on the rig's open PRs and action each
fresh one. Idempotent across polls: an actioned comment is recorded in
<townRoot>/.runtime/pr-watcher-actioned-<rig> and skipped on later invocations.
Safe to invoke from cron/plugin at any cadence.`,
	RunE: runPRWatcherPoll,
}

var prWatcherStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show how many PR comments have been actioned for a rig",
	RunE:  runPRWatcherStatus,
}

func init() {
	for _, c := range []*cobra.Command{prWatcherPollCmd, prWatcherStatusCmd} {
		c.Flags().StringVar(&prWatcherRig, "rig", "", "Rig name (defaults to inferring from cwd)")
		c.Flags().BoolVar(&prWatcherJSON, "json", false, "Emit summary as JSON")
	}
	prWatcherCmd.AddCommand(prWatcherPollCmd)
	prWatcherCmd.AddCommand(prWatcherStatusCmd)
	rootCmd.AddCommand(prWatcherCmd)
}

// resolvePRWatcherContext figures out (townRoot, rigName, rigDir). --rig
// overrides cwd inference. Reuses inferRigFromCwd, shared with ci-watcher.
func resolvePRWatcherContext() (townRoot, rigName, rigDir string, err error) {
	townRoot, err = workspace.FindFromCwdOrError()
	if err != nil {
		return "", "", "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rigName = prWatcherRig
	if rigName == "" {
		rigName, err = inferRigFromCwd(townRoot)
		if err != nil {
			return "", "", "", fmt.Errorf("could not infer rig (use --rig <name>): %w", err)
		}
	}
	rigDir = filepath.Join(townRoot, rigName)
	if _, statErr := os.Stat(rigDir); statErr != nil {
		return "", "", "", fmt.Errorf("rig %q not found at %s", rigName, rigDir)
	}
	return townRoot, rigName, rigDir, nil
}

func runPRWatcherPoll(cmd *cobra.Command, args []string) error {
	townRoot, rigName, rigDir, err := resolvePRWatcherContext()
	if err != nil {
		return err
	}

	// Anchor the gh fetcher/replier at the rig's repo clone so they pick up the
	// right git remote. Same layout fallback the ci-watcher uses.
	repoDir := filepath.Join(rigDir, "refinery", "rig")
	if _, statErr := os.Stat(repoDir); statErr != nil {
		repoDir = filepath.Join(rigDir, "mayor", "rig")
	}

	fetcher := prwatcher.NewGHCommentFetcher(repoDir)
	replier := prwatcher.NewGHReplier(repoDir)
	dispatcher := prwatcher.NewGTDispatcher(rigDir)
	mailer := prwatcher.NewMailAdapter(rigDir)

	cfg := prwatcher.Config{TownRoot: townRoot, Rig: rigName}
	w := prwatcher.NewWatcher(cfg, fetcher, dispatcher, replier, mailer, nil, cmd.OutOrStdout())

	// Cap the poll at a generous wall-clock budget so a hung gh CLI doesn't
	// stall a cron invocation forever.
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()

	res, err := w.Process(ctx)
	if err != nil {
		return fmt.Errorf("prwatcher poll: %w", err)
	}

	if prWatcherJSON {
		out := struct {
			Rig                 string `json:"rig"`
			CommentsConsidered  int    `json:"comments_considered"`
			CommentsActioned    int    `json:"comments_actioned"`
			Mechanical          int    `json:"mechanical"`
			Gated               int    `json:"gated"`
			ColdStartSuppressed int    `json:"cold_start_suppressed"`
			Skipped             bool   `json:"skipped"`
			SkipReason          string `json:"skip_reason,omitempty"`
		}{rigName, res.CommentsConsidered, res.CommentsActioned, res.Mechanical, res.Gated, res.ColdStartSuppressed, res.Skipped, res.SkipReason}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	if res.Skipped {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "prwatcher: rig=%s skipped (%s)\n", rigName, res.SkipReason)
		return nil
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(),
		"prwatcher: rig=%s considered=%d actioned=%d mechanical=%d gated=%d cold_start_suppressed=%d\n",
		rigName, res.CommentsConsidered, res.CommentsActioned, res.Mechanical, res.Gated, res.ColdStartSuppressed,
	)
	return nil
}

func runPRWatcherStatus(cmd *cobra.Command, args []string) error {
	townRoot, rigName, _, err := resolvePRWatcherContext()
	if err != nil {
		return err
	}

	actioned, err := prwatcher.LoadActioned(townRoot, rigName)
	if err != nil {
		return fmt.Errorf("loading actioned ledger: %w", err)
	}

	if prWatcherJSON {
		payload := map[string]any{
			"rig":      rigName,
			"actioned": actioned.Len(),
			"fresh":    actioned.Fresh(),
			"path":     prwatcher.ActionedPath(townRoot, rigName),
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}

	if actioned.Fresh() {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rig=%s: no comments actioned yet (cold start)\n", rigName)
		return nil
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rig=%s: %d comment(s) actioned\n", rigName, actioned.Len())
	return nil
}

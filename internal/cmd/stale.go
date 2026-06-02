package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/version"
	"github.com/steveyegge/gastown/internal/workspace"
)

var staleJSON bool
var staleQuiet bool

var staleCmd = &cobra.Command{
	Use:     "stale",
	GroupID: GroupDiag,
	Short:   "Check if the gt binary is stale",
	Long: `Check if the gt binary was built from an older commit than the current repo HEAD.

This command compares the commit hash embedded in the binary at build time
with the current HEAD of the gastown repository.

Examples:
  gt stale              # Human-readable output
  gt stale --json       # Machine-readable JSON output
  gt stale --quiet      # Exit code only (0=stale, 1=fresh)

Exit codes:
  0 - Binary is stale (needs rebuild)
  1 - Binary is fresh (up to date)
  2 - Error (could not determine staleness)`,
	RunE: runStale,
}

func init() {
	staleCmd.Flags().BoolVar(&staleJSON, "json", false, "Output as JSON")
	staleCmd.Flags().BoolVarP(&staleQuiet, "quiet", "q", false, "Exit code only (0=stale, 1=fresh)")
	rootCmd.AddCommand(staleCmd)
}

// StaleOutput represents the JSON output structure.
type StaleOutput struct {
	Stale         bool   `json:"stale"`
	Forward       bool   `json:"forward"`
	OnMainBranch  bool   `json:"on_main_branch"`
	SafeToRebuild bool   `json:"safe_to_rebuild"`
	BinaryCommit  string `json:"binary_commit"`
	RepoCommit    string `json:"repo_commit"`
	CommitsBehind int    `json:"commits_behind,omitempty"`
	Error         string `json:"error,omitempty"`

	// Daemon-staleness fields (gu-qx6rn): a distinct signal comparing the
	// commit the RUNNING daemon process was built from against the commit of
	// the on-disk binary. When the on-disk binary is upgraded but the
	// long-lived daemon has not restarted, the daemon keeps executing
	// hours-old in-memory code while the on-disk-vs-repo check above reads
	// "fresh". This surfaces that gap independently.
	DaemonRunning      bool   `json:"daemon_running"`
	DaemonStale        bool   `json:"daemon_stale"`
	DaemonNeedsRestart bool   `json:"daemon_needs_restart"`
	DaemonCommit       string `json:"daemon_commit,omitempty"`
}

// daemonStaleInfo holds the result of comparing the running daemon's commit to
// the on-disk binary's commit.
type daemonStaleInfo struct {
	running      bool   // daemon process is alive
	stale        bool   // running daemon commit != on-disk binary commit
	needsRestart bool   // stale AND we have both commits to be confident
	daemonCommit string // commit the running daemon recorded at startup
}

// checkDaemonStale compares the running daemon's recorded build commit against
// the on-disk binary's commit. It is best-effort and non-fatal: if the town
// root can't be found, the daemon isn't running, or the daemon recorded no
// commit (dev build / pre-upgrade daemon), it reports not-stale rather than
// erroring. binaryCommit is the on-disk binary's commit (may be empty for dev
// builds).
func checkDaemonStale(binaryCommit string) daemonStaleInfo {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return daemonStaleInfo{}
	}

	running, _, err := daemon.IsRunning(townRoot)
	if err != nil || !running {
		return daemonStaleInfo{}
	}

	state, err := daemon.LoadState(townRoot)
	if err != nil {
		return daemonStaleInfo{running: true}
	}

	return compareDaemonCommit(state.Commit, binaryCommit)
}

// compareDaemonCommit is the pure comparison core of checkDaemonStale: given the
// commit the running daemon recorded at startup and the on-disk binary's commit,
// it decides whether the daemon is running stale code. Separated out so the
// commit-comparison logic is unit-testable without a live daemon. Assumes the
// daemon is running (callers only reach here when it is).
func compareDaemonCommit(daemonCommit, binaryCommit string) daemonStaleInfo {
	info := daemonStaleInfo{running: true, daemonCommit: daemonCommit}

	// If either commit is unknown we cannot compare. This happens for dev
	// builds (no ldflag commit) or a daemon that started before this field
	// existed. Report running-but-not-stale; the operator's restart will
	// populate state.Commit going forward.
	if daemonCommit == "" || binaryCommit == "" {
		return info
	}

	if !version.CommitsMatch(daemonCommit, binaryCommit) {
		info.stale = true
		info.needsRestart = true
	}
	return info
}

func runStale(cmd *cobra.Command, args []string) error {
	// Find the gastown repo
	repoRoot, err := version.GetRepoRoot()
	if err != nil {
		if staleQuiet {
			return NewSilentExit(2)
		}
		if staleJSON {
			return outputStaleJSON(StaleOutput{Error: err.Error()})
		}
		return fmt.Errorf("cannot find gastown repo: %w", err)
	}

	// Check staleness
	info := version.CheckStaleBinary(repoRoot)

	// Handle errors
	if info.Error != nil {
		if staleQuiet {
			return NewSilentExit(2)
		}
		if staleJSON {
			return outputStaleJSON(StaleOutput{Error: info.Error.Error()})
		}
		return fmt.Errorf("staleness check failed: %w", info.Error)
	}

	// Quiet mode: just exit with appropriate code. Exit status reflects the
	// on-disk binary-vs-repo staleness (the original contract that rebuild-gt
	// and other consumers depend on). The daemon-vs-binary signal is surfaced
	// in --json and text output, not the exit code.
	if staleQuiet {
		if info.IsStale {
			return NewSilentExit(0)
		}
		return NewSilentExit(1)
	}

	// Distinct signal (gu-qx6rn): is the RUNNING daemon executing the on-disk
	// binary's commit, or stale in-memory code from before an upgrade?
	daemonInfo := checkDaemonStale(info.BinaryCommit)

	// Build output
	// SafeToRebuild requires: stale + forward-only + on main branch
	safeToRebuild := info.IsStale && info.IsForward && info.OnMainBranch
	output := StaleOutput{
		Stale:              info.IsStale,
		Forward:            info.IsForward,
		OnMainBranch:       info.OnMainBranch,
		SafeToRebuild:      safeToRebuild,
		BinaryCommit:       info.BinaryCommit,
		RepoCommit:         info.RepoCommit,
		CommitsBehind:      info.CommitsBehind,
		DaemonRunning:      daemonInfo.running,
		DaemonStale:        daemonInfo.stale,
		DaemonNeedsRestart: daemonInfo.needsRestart,
		DaemonCommit:       daemonInfo.daemonCommit,
	}

	if staleJSON {
		return outputStaleJSON(output)
	}

	return outputStaleText(output)
}

func outputStaleJSON(output StaleOutput) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

func outputStaleText(output StaleOutput) error {
	if output.Stale {
		fmt.Printf("%s Binary is stale\n", style.Warning.Render("⚠"))
		fmt.Printf("  Binary: %s\n", version.ShortCommit(output.BinaryCommit))
		fmt.Printf("  Repo:   %s\n", version.ShortCommit(output.RepoCommit))
		if output.CommitsBehind > 0 {
			fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("(%d commits behind)", output.CommitsBehind)))
		}
		if !output.Forward {
			fmt.Printf("  %s repo HEAD is NOT a descendant of binary commit (diverged or older)\n", style.Error.Render("✗"))
		}
		if !output.OnMainBranch {
			fmt.Printf("  %s repo is not on main branch\n", style.Warning.Render("⚠"))
		}
		if output.SafeToRebuild {
			fmt.Printf("\n  Safe to rebuild: run 'make build && make install'\n")
		} else {
			fmt.Printf("\n  %s NOT safe for automated rebuild (forward=%v, main=%v)\n",
				style.Error.Render("✗"), output.Forward, output.OnMainBranch)
		}
	} else {
		fmt.Printf("%s Binary is fresh\n", style.Success.Render("✓"))
		fmt.Printf("  Commit: %s\n", version.ShortCommit(output.BinaryCommit))
	}

	// Daemon-staleness signal (gu-qx6rn): independent of the on-disk-vs-repo
	// check above. A daemon running stale in-memory code can sit behind an
	// upgraded on-disk binary even when the binary itself is "fresh".
	if output.DaemonNeedsRestart {
		fmt.Printf("\n%s DAEMON RESTART PENDING: running %s, on-disk %s\n",
			style.Error.Render("✗"),
			version.ShortCommit(output.DaemonCommit),
			version.ShortCommit(output.BinaryCommit))
		fmt.Printf("  The running daemon is executing older in-memory code than the on-disk binary.\n")
		fmt.Printf("  Restart to deploy daemon-resident fixes: %s\n",
			style.Dim.Render("gt daemon stop && gt daemon start"))
	}
	return nil
}

package cmd

// `gt status` command definition. This file is intentionally small: the
// heavy lifting lives in sibling files in the same package:
//
//   status_types.go    – data structures (TownStatus, RigStatus, …)
//   status_gather.go   – gatherStatus: collect data in parallel
//   status_render.go   – text/JSON output
//   status_discover.go – discover agents, hooks, MQ
//   status_runtime.go  – /proc inspection for agent runtimes
//   status_watch.go    – --watch mode
//
// The file was split from a single 1958-line status.go (gu-3io).

import (
	"os"

	"github.com/spf13/cobra"
)

var (
	statusJSON     bool
	statusFast     bool
	statusWatch    bool
	statusInterval int
	statusVerbose  bool
)

var statusCmd = &cobra.Command{
	Use:         "status",
	Aliases:     []string{"stat"},
	GroupID:     GroupDiag,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Show overall town status",
	Long: `Display the current status of the Gas Town workspace.

Shows town name, registered rigs, polecats, and witness status.

Use --fast to skip mail lookups for faster execution.
Use --watch to continuously refresh status at regular intervals.`,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Output as JSON")
	statusCmd.Flags().BoolVar(&statusFast, "fast", false, "Skip mail lookups for faster execution")
	statusCmd.Flags().BoolVarP(&statusWatch, "watch", "w", false, "Watch mode: refresh status continuously")
	statusCmd.Flags().IntVarP(&statusInterval, "interval", "n", 2, "Refresh interval in seconds")
	statusCmd.Flags().BoolVarP(&statusVerbose, "verbose", "v", false, "Show detailed multi-line output per agent")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	if statusWatch {
		return runStatusWatch(cmd, args)
	}
	return runStatusOnce(cmd, args)
}

func runStatusOnce(_ *cobra.Command, _ []string) error {
	status, err := gatherStatus()
	if err != nil {
		return err
	}
	if statusJSON {
		return outputStatusJSON(status)
	}
	return outputStatusText(os.Stdout, status)
}

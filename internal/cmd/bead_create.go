package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/workspace"
)

var beadCreateCmd = &cobra.Command{
	Use:   "create [title] [flags]",
	Short: "Create a bead, routing it to the rig database its assignee owns",
	Long: `Create a bead with rig-aware database routing.

The raw 'bd create' picks its target database from the current directory, so
running it from the town root (or any non-rig cwd) writes the bead to the town
(hq) database even when --assignee names a rig agent. The assignee is just a
label to bd; it does not change which database receives the bead. Rig-scoped
beads created that way become invisible to the owning rig's agents.

'gt bead create' fixes this by choosing the target database from the work's
owner, not the cwd:

  1. An explicit --repo <rig> alias takes precedence.
  2. Otherwise the rig is inferred from --assignee (<rig>/<role>/<name>).

The bead is then written to that rig's database regardless of cwd. Town-level
assignees (mayor, deacon) and beads with no resolvable rig fall through to the
cwd database, matching bd's native behavior.

All other 'bd create' flags are passed through unchanged.

Examples:
  gt bead create "Fix flaky test" -a gastown_upstream/crew/canewiw --type bug
  gt bead create "Town policy" -a mayor/        # town (hq) database
  gt bead create "Explicit" --repo gastown_upstream --type bug`,
	DisableFlagParsing: true, // Pass all flags through to bd create
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBeadCreate(cmd, args)
	},
}

func init() {
	beadCmd.AddCommand(beadCreateCmd)
}

func runBeadCreate(cmd *cobra.Command, args []string) error {
	if helped, err := checkHelpFlag(cmd, args); helped || err != nil {
		return err
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("cannot determine town root for rig routing: %w", err)
	}

	// Determine the target rig: an explicit --repo alias wins; otherwise infer
	// from the assignee. Both name the rig that owns the work, independent of cwd.
	repo := flagValueFromArgs(args, "--repo")
	assignee := flagValueFromArgs(args, "--assignee", "-a")

	targetRig := repo
	routedVia := "--repo"
	if targetRig == "" {
		targetRig = beads.RigFromAssignee(assignee)
		routedVia = "--assignee"
	}

	beadsDir := ""
	if targetRig != "" {
		if dir, ok := beads.ResolveRepoAliasBeadsDir(townRoot, targetRig); ok {
			beadsDir = dir
			// Strip a resolved --repo: bd's own --repo handling opens the
			// embedded store (which fails on CGO_ENABLED=0 builds) and would
			// override our BEADS_DIR pin. Once we've resolved it to a database,
			// bd must route purely via the pinned BEADS_DIR.
			if repo != "" {
				args = stripFlagFromArgs(args, "--repo")
			}
		} else if repo != "" {
			// An explicit --repo that we cannot resolve is a hard error: bd would
			// silently create the bead in the cwd database and orphan it.
			return fmt.Errorf("cannot route bead: unknown repo/rig alias %q", repo)
		} else {
			// An assignee whose rig we cannot resolve (e.g. a typo or a non-rig
			// address) falls through to bd's cwd-based routing with a warning.
			fmt.Fprintf(os.Stderr,
				"⚠ gt bead create: assignee %q names rig %q but it has no routes.jsonl entry; using cwd database\n",
				assignee, targetRig)
		}
	}

	return execBdCreate(args, beadsDir, routedVia, targetRig)
}

// stripFlagFromArgs removes a single value-taking flag and its value from a
// passthrough argv, handling both "--flag value" and "--flag=value" forms and
// leaving everything after the "--" sentinel untouched.
func stripFlagFromArgs(args []string, name string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if arg == name {
			i++ // skip the value too (if any)
			continue
		}
		if strings.HasPrefix(arg, name+"=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// flagValueFromArgs returns the value of the first matching flag in a
// passthrough argv. It understands both "--flag value" and "--flag=value"
// forms and stops at the "--" sentinel. Returns "" if absent.
func flagValueFromArgs(args []string, names ...string) string {
	match := func(arg string) bool {
		for _, n := range names {
			if arg == n {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if match(arg) {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		for _, n := range names {
			if strings.HasPrefix(arg, n+"=") {
				return strings.TrimPrefix(arg, n+"=")
			}
		}
	}
	return ""
}

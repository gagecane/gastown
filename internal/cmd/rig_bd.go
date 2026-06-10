package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/workspace"
)

var rigBdCmd = &cobra.Command{
	Use:   "bd <rig> [bd args...]",
	Short: "Run a bd command against a named rig's database (cwd-independent)",
	Long: `Run an arbitrary bd command against a named rig's beads database.

Resolves the rig by NAME (not by current working directory), so it works from
anywhere in town — including the mayor session, where the shell's working
directory resets to the town root (the hq database) between commands.

The rig name is resolved to its .beads directory regardless of placement
convention (<rig>/mayor/rig/.beads or rig-root <rig>/.beads). The special
names "hq" and "town" target the town-level database.

This is the cwd-independent counterpart to prefix-routed commands like
'gt show <id>': use 'gt rig bd' when the bd command has no bead ID to route on
(e.g. ready, list, where) or must run against a specific rig.

Everything after the rig name is passed through to bd verbatim.

Examples:
  gt rig bd talon ready                 # 'bd ready' against the talon rig DB
  gt rig bd gastown_upstream list --status=open
  gt rig bd talon_cdk where             # show the resolved beads workspace
  gt rig bd hq list --status=hooked     # town-level (hq) database
  gt rig bd talon update ti-f9a --status=open`,
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: true, // Pass all flags through to bd verbatim.
	RunE:               runRigBd,
}

func init() {
	rigCmd.AddCommand(rigBdCmd)
}

func runRigBd(cmd *cobra.Command, args []string) error {
	if helped, err := checkHelpFlag(cmd, args); helped || err != nil {
		return err
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	beadsDir, bdArgs, err := resolveRigBd(townRoot, args)
	if err != nil {
		return err
	}

	return execRigBd(beadsDir, bdArgs)
}

// resolveRigBd validates the 'gt rig bd' positional args and resolves the named
// rig to its .beads directory using cwd-independent name routing. It returns the
// resolved beads directory and the bd args to pass through. Splitting this out
// from runRigBd keeps the resolution logic unit-testable without execing bd.
func resolveRigBd(townRoot string, args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("rig name required\n\nUsage: gt rig bd <rig> [bd args...]")
	}

	rigName := args[0]
	bdArgs := args[1:]
	if len(bdArgs) == 0 {
		return "", nil, fmt.Errorf("bd command required\n\nUsage: gt rig bd <rig> [bd args...]")
	}

	beadsDir, ok := beads.ResolveRepoAliasBeadsDir(townRoot, rigName)
	if !ok {
		return "", nil, fmt.Errorf("cannot resolve beads database for rig %q "+
			"(unknown rig or missing .beads workspace); known rigs come from routes.jsonl", rigName)
	}

	return beadsDir, bdArgs, nil
}

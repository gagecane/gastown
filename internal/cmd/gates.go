// Package cmd: `gt gates` — inspect/render the canonical gates.yaml.
//
// The bead this implements (gu-1wm3) describes four consumers of "what gates
// run before a change lands": pre-push, CI, refinery, and the polecat
// formula's gates_commands variable. Today they each maintain their own copy
// and drift; the parent bead's fix is one declarative gates.yaml read by all
// four.
//
// This command is the consumer-facing surface of that file. It exists for two
// concrete reasons:
//
//  1. The pre-push hook is bash. Parsing YAML in bash is a maintenance hazard
//     (sed/awk over multi-line values, no schema validation). Calling
//     `gt gates print --phase fast --shell` in the hook gives bash a
//     pre-validated, copy-pastable command stream.
//
//  2. CI manifest generators and the refinery formula resolver will read the
//     file via the gates package directly. The `list` subcommand here is the
//     human/audit interface — `gt gates list` is the equivalent of `make
//     verify-gates --dry-run`: "tell me what's about to run."
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/gastown/internal/gates"
)

var (
	gatesPhase   string
	gatesShell   bool
	gatesInclude string
)

var gatesCmd = &cobra.Command{
	Use:     "gates",
	GroupID: GroupDiag,
	Short:   "Inspect the canonical gates.yaml manifest",
	Long: `Inspect the canonical gates.yaml manifest at the repo root.

gates.yaml declares the gate commands run by pre-push, CI, the refinery
merge queue, and the polecat formula's gates_commands variable. See
internal/gates for the schema.

Subcommands:
  list   — print gates as a human-readable table
  print  — print gates as shell-runnable commands (used by pre-push)
`,
	RunE: requireSubcommand,
}

var gatesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List declared gates as a human-readable table",
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := loadGates()
		if err != nil {
			return err
		}
		filtered := filterGates(f.All(), gatesPhase, gatesInclude)
		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "%-6s  %-22s  %-22s  %s\n", "PHASE", "NAME", "TIER", "COMMAND")
		for _, g := range filtered {
			fmt.Fprintf(w, "%-6s  %-22s  %-22s  %s\n", g.Phase, g.Name, g.Tier, g.Command)
		}
		return nil
	},
}

var gatesPrintCmd = &cobra.Command{
	Use:   "print",
	Short: "Print declared gate commands (shell-runnable)",
	Long: `Print declared gate commands one per line, suitable for piping into bash.

Used by scripts/pre-push-check.sh to stay in sync with gates.yaml without
parsing YAML in bash. The output format with --shell is:

	# fast: build (required) — go build ./...
	go build ./...
	# fast: vet (required) — go vet ./...
	go vet ./...

The comments preserve the gate name and tier so the hook can render
human-friendly progress messages without re-parsing.

Without --shell, prints just the commands separated by newlines (formula
gates_commands format).
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := loadGates()
		if err != nil {
			return err
		}
		filtered := filterGates(f.All(), gatesPhase, gatesInclude)
		w := cmd.OutOrStdout()
		for _, g := range filtered {
			if gatesShell {
				note := g.Note
				if note == "" {
					note = g.Command
				}
				fmt.Fprintf(w, "# %s: %s (%s) — %s\n", g.Phase, g.Name, g.Tier, note)
			}
			fmt.Fprintln(w, g.Command)
		}
		return nil
	},
}

// loadGates resolves gates.yaml from the current working directory's repo
// root. We call os.Getwd here rather than threading a path through because
// every caller (the pre-push hook, manual `gt gates list`) operates against
// the repo they're standing in.
func loadGates() (*gates.File, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return gates.LoadFromRepo(cwd)
}

// filterGates applies the --phase and --include flags to the gate list.
// Empty filters mean "include everything"; we intentionally do not error on
// no matches because an empty phase is a legitimate query ("are there any
// ci-only gates?" → empty output is the right answer).
func filterGates(all []gates.PhasedGate, phase, include string) []gates.PhasedGate {
	out := make([]gates.PhasedGate, 0, len(all))
	wantTiers := parseInclude(include)
	for _, g := range all {
		if phase != "" && string(g.Phase) != phase {
			continue
		}
		if wantTiers != nil && !wantTiers[string(g.Tier)] {
			continue
		}
		out = append(out, g)
	}
	return out
}

// parseInclude parses a comma-separated --include flag (e.g. "required,ci-only")
// into a set. nil means "no filter set", which is distinct from an empty set.
func parseInclude(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	for _, t := range strings.Split(s, ",") {
		out[strings.TrimSpace(t)] = true
	}
	return out
}

func init() {
	gatesCmd.AddCommand(gatesListCmd)
	gatesCmd.AddCommand(gatesPrintCmd)

	for _, c := range []*cobra.Command{gatesListCmd, gatesPrintCmd} {
		c.Flags().StringVar(&gatesPhase, "phase", "", `filter by phase: "fast" or "slow" (default: all)`)
		c.Flags().StringVar(&gatesInclude, "include", "",
			`comma-separated tiers to include: required, required-if-installed, ci-only (default: all)`)
	}
	gatesPrintCmd.Flags().BoolVar(&gatesShell, "shell", false, "emit shell comments alongside commands")

	rootCmd.AddCommand(gatesCmd)
}

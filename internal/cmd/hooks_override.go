package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/hooks"
)

var hooksOverrideCmd = &cobra.Command{
	Use:   "override <target>",
	Short: "Edit overrides for a role or rig",
	Long: `Edit hook overrides for a specific role or rig+role combination.

Valid targets:
  Role-level:  crew, witness, refinery, polecats, mayor, deacon
  Rig+role:    gastown/crew, beads/witness, sky/polecats, etc.

Overrides are merged on top of the base config during sync.
Hooks with the same matcher replace the base hook entirely.

Override files are stored in ~/.gt/hooks-overrides/<target>.json.

Examples:
  gt hooks override crew              # Edit crew role overrides
  gt hooks override gastown/crew      # Edit gastown rig crew overrides
  gt hooks override mayor             # Edit mayor overrides
  gt hooks override crew --show       # Print current override config`,
	Args: cobra.ExactArgs(1),
	RunE: runHooksOverride,
}

var hooksOverrideShow bool

func init() {
	hooksCmd.AddCommand(hooksOverrideCmd)
	hooksOverrideCmd.Flags().BoolVar(&hooksOverrideShow, "show", false, "Print current override config to stdout")
}

func runHooksOverride(cmd *cobra.Command, args []string) error {
	normalized, ok := hooks.NormalizeTarget(args[0])
	if !ok {
		return fmt.Errorf("invalid target %q; valid targets are roles (crew, witness, refinery, polecats, mayor, deacon) or rig/role (gastown/crew, etc.)", args[0])
	}
	target := normalized

	cfg, err := hooks.LoadOverride(target)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("loading override config for %q: %w", target, err)
		}
		// File doesn't exist yet - create empty
		cfg = &hooks.HooksConfig{}
		if err := hooks.SaveOverride(target, cfg); err != nil {
			return fmt.Errorf("creating override config: %w", err)
		}
		fmt.Printf("Created empty override config for %s\n", target)
	}

	if hooksOverrideShow {
		data, err := hooks.MarshalConfig(cfg)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Open in editor (refuses under GT_ROLE — see gu-pkf3).
	path := hooks.OverridePath(target)
	suggestion := fmt.Sprintf("use `gt hooks override %s --show` to inspect, or edit the file directly: %s", target, path)
	if err := launchEditor(path, "gt hooks override", suggestion); err != nil {
		return err
	}

	// Validate after editing
	if _, err := hooks.LoadOverride(target); err != nil {
		return fmt.Errorf("warning: override config has errors after editing: %w", err)
	}

	fmt.Printf("Override config for %s updated. Run 'gt hooks sync' to propagate changes.\n", target)
	return nil
}

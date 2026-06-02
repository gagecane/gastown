package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestEstopRejectsArgs guards the footgun where `gt estop status` (no such
// subcommand) was parsed as an ignored positional arg and FIRED a town-wide
// e-stop. estopCmd must reject any positional args so typos/probes error out
// instead of freezing the town. Regression guard for the estop-status footgun
// (multiple town freezes 2026-06-02).
func TestEstopRejectsArgs(t *testing.T) {
	if estopCmd.Args == nil {
		t.Fatal("estopCmd.Args is nil — `gt estop status` would fire an e-stop instead of erroring (footgun)")
	}
	// cobra.NoArgs returns an error for any positional args.
	if err := estopCmd.Args(estopCmd, []string{"status"}); err == nil {
		t.Error("estopCmd accepted positional arg \"status\" — footgun: this fires an e-stop. Expected an error (cobra.NoArgs).")
	}
	// Sanity: zero args must be accepted (the legitimate `gt estop` invocation).
	if err := estopCmd.Args(estopCmd, []string{}); err != nil {
		t.Errorf("estopCmd rejected zero args (%v) — legitimate `gt estop` must still work", err)
	}
}

var _ = cobra.NoArgs // ensure cobra import is used even if the cmd wiring changes

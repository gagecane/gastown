package cmd

import (
	"strings"
	"testing"
)

// TestBeadRequiresSubcommand verifies that `gt bead` with no subcommand and
// with an unknown subcommand returns an error instead of printing help and
// exiting 0 (false success). See gu-8f5ad.
func TestBeadRequiresSubcommand(t *testing.T) {
	if beadCmd.RunE == nil {
		t.Fatal("beadCmd.RunE is nil; expected requireSubcommand wiring")
	}

	// No subcommand: should error.
	err := beadCmd.RunE(beadCmd, []string{})
	if err == nil {
		t.Fatal("beadCmd.RunE with no args returned no error")
	}
	if !strings.Contains(err.Error(), "requires a subcommand") {
		t.Errorf("beadCmd.RunE error = %v, want containing 'requires a subcommand'", err)
	}

	// Unknown subcommand: should error rather than silently succeed.
	err = beadCmd.RunE(beadCmd, []string{"doesnotexist"})
	if err == nil {
		t.Fatal("beadCmd.RunE with unknown subcommand returned no error")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("beadCmd.RunE error = %v, want containing 'unknown command'", err)
	}
}

// TestBeadSubcommandsRegistered verifies the expected subcommands remain wired.
func TestBeadSubcommandsRegistered(t *testing.T) {
	expected := []string{"move", "show", "read", "refile", "reset", "create"}

	got := make(map[string]bool)
	for _, sub := range beadCmd.Commands() {
		got[sub.Name()] = true
	}

	for _, name := range expected {
		if !got[name] {
			t.Errorf("beadCmd missing subcommand %q", name)
		}
	}
}

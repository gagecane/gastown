//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/steveyegge/gastown/internal/beads"
)

// execRigBd runs 'bd <args...>' with stdio passthrough on Windows, pinned to
// beadsDir. Pinning BEADS_DIR (rather than relying on cwd) is what makes the
// command cwd-independent. Read-only vs mutation env policy is selected from the
// bd args so cross-rig reads stay read-only and writes commit.
func execRigBd(beadsDir string, bdArgs []string) error {
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		return fmt.Errorf("bd not found in PATH: %w", err)
	}

	mode := beads.ReadOnlyPinned
	if !beads.ArgsAreReadOnly(bdArgs) {
		mode = beads.MutationPinned
	}
	env := beads.EnvForSubprocessMode(os.Environ(), beadsDir, mode)

	cmd := exec.Command(bdPath, bdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil // unreachable
}

//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
)

// execBdCreate runs 'bd create' with stdio passthrough on Windows, pinned to
// the resolved rig database when one was determined. When beadsDir is empty the
// command falls through to bd's native cwd-based routing.
func execBdCreate(args []string, beadsDir, routedVia, targetRig string) error {
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		return fmt.Errorf("bd not found in PATH: %w", err)
	}

	env := pinBeadsDirEnv(os.Environ(), beadsDir)
	if beadsDir != "" {
		fmt.Fprintf(os.Stderr, "→ routing bead to rig %q (via %s) database\n", targetRig, routedVia)
	}

	cmdArgs := append([]string{"create"}, args...)
	cmd := exec.Command(bdPath, cmdArgs...)
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

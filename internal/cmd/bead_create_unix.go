//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// execBdCreate replaces the current process with 'bd create', pinned to the
// resolved rig database when one was determined. When beadsDir is empty the
// command falls through to bd's native cwd-based routing.
func execBdCreate(args []string, beadsDir, routedVia, targetRig string) error {
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		return fmt.Errorf("bd not found in PATH: %w", err)
	}

	// pinBeadsDirEnv sets BEADS_DIR and strips inherited bd target selectors so
	// the resolved rig database is authoritative regardless of cwd. An empty
	// beadsDir leaves bd's native cwd-based routing in place.
	env := pinBeadsDirEnv(os.Environ(), beadsDir)
	if beadsDir != "" {
		fmt.Fprintf(os.Stderr, "→ routing bead to rig %q (via %s) database\n", targetRig, routedVia)
	}

	fullArgs := append([]string{"bd", "create"}, args...)
	return syscall.Exec(bdPath, fullArgs, env)
}

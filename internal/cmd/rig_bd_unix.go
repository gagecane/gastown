//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/steveyegge/gastown/internal/beads"
)

// execRigBd replaces the current process with 'bd <args...>' pinned to beadsDir.
// Pinning BEADS_DIR (rather than relying on cwd) is what makes the command
// cwd-independent: bd targets the resolved rig database regardless of where the
// caller's shell happens to be. Read-only vs mutation env policy is selected
// from the bd args so cross-rig reads stay read-only and writes commit.
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

	// argv[0] must be the program name for exec.
	fullArgs := append([]string{"bd"}, bdArgs...)

	return syscall.Exec(bdPath, fullArgs, env)
}

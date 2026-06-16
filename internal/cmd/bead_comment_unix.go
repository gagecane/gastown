//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/steveyegge/gastown/internal/beads"
)

// execBdComment replaces the current process with 'bd comment'.
// Resolves the correct rig directory from the bead's prefix via routes.jsonl
// so that rig-prefixed beads are commented in their rig database rather than
// only the town-level hq database, mirroring execBdShow.
func execBdComment(args []string) error {
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		return fmt.Errorf("bd not found in PATH: %w", err)
	}

	beadsDir := ""
	if beadID := extractBeadIDFromArgs(args); beadID != "" {
		if dir := resolveBeadDir(beadID); dir != "" && dir != "." {
			_ = os.Chdir(dir)
			beadsDir = beads.ResolveBeadsDir(dir)
		}
	}

	env := pinBeadsDirEnv(os.Environ(), beadsDir)

	// Build args: bd comment <all-args>
	// argv[0] must be the program name for exec.
	fullArgs := append([]string{"bd", "comment"}, args...)

	return syscall.Exec(bdPath, fullArgs, env)
}

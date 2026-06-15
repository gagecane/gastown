package rig

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/style"
)

// guardInstallTimeout bounds rig-guards/install.sh execution.
const guardInstallTimeout = 60 * time.Second

// RunGuardsInstall runs <rigPath>/rig-guards/install.sh against the given
// worktree (or clone) so the rig's internal-ref pre-push guard (lb-b403) is
// enforced from the FIRST push, rather than only after someone manually runs
// install.sh. The guard wires itself into the repo's shared bare-repo hooks
// directory; rig-guards is internal and never committed to the customer tree.
//
// Behavior:
//   - No-op (returns nil) when the rig has no rig-guards/install.sh — most rigs
//     don't ship guards, and that is not an error.
//   - Idempotent: install.sh is safe to re-run, so calling this on every rig
//     and worktree create is harmless.
//
// worktreePath is used as the working directory so install.sh resolves the
// shared hooks dir via `git rev-parse --git-common-dir` of a real worktree.
func RunGuardsInstall(rigPath, worktreePath string) error {
	script := filepath.Join(rigPath, "rig-guards", "install.sh")
	info, err := os.Stat(script)
	if err != nil {
		if os.IsNotExist(err) {
			// Rig ships no guards — nothing to install.
			return nil
		}
		return fmt.Errorf("stat rig-guards install.sh: %w", err)
	}

	// Skip non-executable scripts rather than failing provisioning.
	if info.Mode().Perm()&0111 == 0 {
		style.PrintWarning("rig-guards install.sh is not executable (chmod +x to enable); skipping")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), guardInstallTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, script)
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GT_WORKTREE_PATH=%s", worktreePath),
		fmt.Sprintf("GT_RIG_PATH=%s", rigPath),
	)

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("rig-guards install timed out after %s", guardInstallTimeout)
		}
		return err
	}
	return nil
}

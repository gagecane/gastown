package beads

import (
	"context"
	"os"
	"os/exec"

	"github.com/steveyegge/gastown/internal/util"
)

// SubprocessEnvMode describes how a bd subprocess should target Dolt and
// whether it may mutate state. New raw bd call sites should use this helper so
// target selection and side-effect suppression stay centralized.
type SubprocessEnvMode int

const (
	ReadOnlyRouting SubprocessEnvMode = iota
	MutationRouting
	ReadOnlyPinned
	MutationPinned
)

// Command builds a bd command with the shared Gas Town bd environment policy.
func Command(dir, fallbackBeadsDir string, mode SubprocessEnvMode, args ...string) *exec.Cmd {
	cmd := exec.Command("bd", args...) //nolint:gosec // G204: args are constructed internally
	ConfigureCommand(cmd, dir, fallbackBeadsDir, mode)
	return cmd
}

// CommandContext builds a context-bound bd command with the shared Gas Town bd
// environment policy.
func CommandContext(ctx context.Context, dir, fallbackBeadsDir string, mode SubprocessEnvMode, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bd", args...) //nolint:gosec // G204: args are constructed internally
	ConfigureCommand(cmd, dir, fallbackBeadsDir, mode)
	return cmd
}

// ConfigureCommand applies the shared bd subprocess policy to an existing
// command. This is for callers that need a custom bd path.
func ConfigureCommand(cmd *exec.Cmd, dir, fallbackBeadsDir string, mode SubprocessEnvMode) {
	cmd.Dir = dir
	cmd.Env = EnvForSubprocessMode(os.Environ(), fallbackBeadsDir, mode)
	util.SetDetachedProcessGroup(cmd)
}

func EnvForSubprocessMode(base []string, fallbackBeadsDir string, mode SubprocessEnvMode) []string {
	switch mode {
	case ReadOnlyRouting:
		return BuildReadOnlyRoutingBDEnv(base, fallbackBeadsDir)
	case MutationRouting:
		return BuildMutationRoutingBDEnv(base, fallbackBeadsDir)
	case ReadOnlyPinned:
		return BuildReadOnlyPinnedBDEnv(base, fallbackBeadsDir)
	case MutationPinned:
		return BuildMutationPinnedBDEnv(base, fallbackBeadsDir)
	default:
		return BuildMutationRoutingBDEnv(base, fallbackBeadsDir)
	}
}

func SubprocessModeForArgs(args []string) SubprocessEnvMode {
	if ArgsAreReadOnly(args) {
		return ReadOnlyRouting
	}
	return MutationRouting
}

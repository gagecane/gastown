package doctor

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/tmux"
)

// GlobalEnvAccessor abstracts tmux global environment reads/writes for testing.
type GlobalEnvAccessor interface {
	GetGlobalEnvironment(key string) (string, error)
	SetGlobalEnvironment(key, value string) error
}

// TmuxGlobalEnvCheck verifies that GT_TOWN_ROOT is set in the tmux global
// environment. This is needed for run-shell subprocesses (e.g., gt cycle
// next/prev) where CWD is $HOME and process env vars aren't available.
type TmuxGlobalEnvCheck struct {
	FixableCheck
	accessor GlobalEnvAccessor // nil means use real tmux
}

// NewTmuxGlobalEnvCheck creates a new tmux global env check.
func NewTmuxGlobalEnvCheck() *TmuxGlobalEnvCheck {
	return &TmuxGlobalEnvCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "tmux-global-env",
				CheckDescription: "Verify GT_TOWN_ROOT is set in tmux global environment",
				CheckCategory:    CategoryInfrastructure,
			},
		},
	}
}

// NewTmuxGlobalEnvCheckWithAccessor creates a check with a custom accessor (for testing).
func NewTmuxGlobalEnvCheckWithAccessor(accessor GlobalEnvAccessor) *TmuxGlobalEnvCheck {
	c := NewTmuxGlobalEnvCheck()
	c.accessor = accessor
	return c
}

// Run checks that GT_TOWN_ROOT is set correctly in the tmux global environment.
func (c *TmuxGlobalEnvCheck) Run(ctx *CheckContext) *CheckResult {
	accessor := c.accessor
	if accessor == nil {
		accessor = tmux.NewTmux()
	}

	val, err := accessor.GetGlobalEnvironment("GT_TOWN_ROOT")
	if err != nil {
		// No tmux server running — nothing to check or fix.
		if errors.Is(err, tmux.ErrNoServer) {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusOK,
				Message: "No tmux server running (nothing to check)",
			}
		}
		// Variable not set (tmux returns error for unknown vars) — warn.
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "GT_TOWN_ROOT not set in tmux global environment",
			Details: []string{
				"The daemon sets GT_TOWN_ROOT in tmux global env for run-shell subprocesses.",
				"Without it, prefix-based cycle groups (prefix+n/p) fail when CWD is $HOME.",
			},
			FixHint: "Run 'gt doctor --fix' to set GT_TOWN_ROOT in tmux global env",
		}
	}

	if !sameResolvedPath(val, ctx.TownRoot) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("GT_TOWN_ROOT mismatch in tmux global env: %q (expected %q)", val, ctx.TownRoot),
			Details: []string{
				"The daemon sets GT_TOWN_ROOT in tmux global env for run-shell subprocesses.",
				"Without it, prefix-based cycle groups (prefix+n/p) fail when CWD is $HOME.",
			},
			FixHint: "Run 'gt doctor --fix' to set GT_TOWN_ROOT in tmux global env",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("GT_TOWN_ROOT=%s in tmux global env", val),
	}
}

// Fix sets GT_TOWN_ROOT in the tmux global environment.
func (c *TmuxGlobalEnvCheck) Fix(ctx *CheckContext) error {
	accessor := c.accessor
	if accessor == nil {
		accessor = tmux.NewTmux()
	}
	return accessor.SetGlobalEnvironment("GT_TOWN_ROOT", ctx.TownRoot)
}

// sameResolvedPath returns true if a and b resolve to the same canonical
// filesystem path. This handles the case where GT_TOWN_ROOT was set from
// one path (e.g. /home/canewiw/gt, a symlink) and ctx.TownRoot was resolved
// from the other (e.g. /local/home/canewiw/gt, the symlink target). Both
// forms point to the same town — the raw string compare would flag them as
// a mismatch, producing a recurring false-positive warning.
//
// Falls back to raw string compare if symlink resolution fails on either side
// (e.g., the path no longer exists).
func sameResolvedPath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return ra == rb
}

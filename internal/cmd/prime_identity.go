package cmd

// Agent identity, locking, and bead ID resolution for `gt prime`.
//
// These helpers answer "who am I, and which bead/lock represents me?" They are
// used by the core prime flow (prime.go) to enforce single-owner identity for
// worker roles (polecat, crew) and to resolve the right agent bead for hook
// lookup (prime_work.go).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/style"
)

// getAgentIdentity returns the agent identity string for hook lookup.
func getAgentIdentity(ctx RoleContext) string {
	switch ctx.Role {
	case RoleCrew:
		return fmt.Sprintf("%s/crew/%s", ctx.Rig, ctx.Polecat)
	case RolePolecat:
		return fmt.Sprintf("%s/polecats/%s", ctx.Rig, ctx.Polecat)
	case RoleMayor:
		return "mayor"
	case RoleDeacon:
		return "deacon"
	case RoleBoot:
		return "boot"
	case RoleWitness:
		return fmt.Sprintf("%s/witness", ctx.Rig)
	case RoleRefinery:
		return fmt.Sprintf("%s/refinery", ctx.Rig)
	default:
		return ""
	}
}

// acquireIdentityLock checks and acquires the identity lock for worker roles.
// This prevents multiple agents from claiming the same worker identity.
// Returns an error if another agent already owns this identity.
func acquireIdentityLock(ctx RoleContext) error {
	// Only lock worker roles (polecat, crew)
	// Infrastructure roles (mayor, witness, refinery, deacon) are singletons
	// managed by tmux session names, so they don't need file-based locks
	if ctx.Role != RolePolecat && ctx.Role != RoleCrew {
		return nil
	}

	// Create lock for this worker directory
	l := lock.New(ctx.WorkDir)

	// Determine session ID from environment or context
	sessionID := os.Getenv("TMUX_PANE")
	if sessionID == "" {
		// Fall back to a descriptive identifier
		sessionID = fmt.Sprintf("%s/%s", ctx.Rig, ctx.Polecat)
	}

	// Try to acquire the lock
	if err := l.Acquire(sessionID); err != nil {
		if errors.Is(err, lock.ErrLocked) {
			// Another agent owns this identity
			fmt.Printf("\n%s\n\n", style.Bold.Render("⚠️  IDENTITY COLLISION DETECTED"))
			fmt.Printf("Another agent already claims this worker identity.\n\n")

			// Show lock details
			if info, readErr := l.Read(); readErr == nil {
				fmt.Printf("Lock holder:\n")
				fmt.Printf("  PID: %d\n", info.PID)
				fmt.Printf("  Session: %s\n", info.SessionID)
				fmt.Printf("  Acquired: %s\n", info.AcquiredAt.Format("2006-01-02 15:04:05"))
				fmt.Println()
			}

			fmt.Printf("To resolve:\n")
			fmt.Printf("  1. Find the other session and close it, OR\n")
			fmt.Printf("  2. Run: gt doctor --fix (cleans stale locks)\n")
			fmt.Printf("  3. If lock is stale: rm %s/.runtime/agent.lock\n", ctx.WorkDir)
			fmt.Println()

			return fmt.Errorf("cannot claim identity %s/%s: %w", ctx.Rig, ctx.Polecat, err)
		}
		return fmt.Errorf("acquiring identity lock: %w", err)
	}

	return nil
}

// getAgentBeadID returns the agent bead ID for the current role.
// Town-level agents (mayor, deacon) use hq- prefix; rig-scoped agents use the rig's prefix.
// Returns empty string for unknown roles.
func getAgentBeadID(ctx RoleContext) string {
	switch ctx.Role {
	case RoleMayor:
		return beads.MayorBeadIDTown()
	case RoleDeacon:
		return beads.DeaconBeadIDTown()
	case RoleBoot:
		// Boot uses deacon's bead since it's a deacon subprocess
		return beads.DeaconBeadIDTown()
	case RoleWitness:
		if ctx.Rig != "" {
			prefix := beads.GetPrefixForRig(ctx.TownRoot, ctx.Rig)
			return beads.WitnessBeadIDWithPrefix(prefix, ctx.Rig)
		}
		return ""
	case RoleRefinery:
		if ctx.Rig != "" {
			prefix := beads.GetPrefixForRig(ctx.TownRoot, ctx.Rig)
			return beads.RefineryBeadIDWithPrefix(prefix, ctx.Rig)
		}
		return ""
	case RolePolecat:
		if ctx.Rig != "" && ctx.Polecat != "" {
			prefix := beads.GetPrefixForRig(ctx.TownRoot, ctx.Rig)
			return beads.PolecatBeadIDWithPrefix(prefix, ctx.Rig, ctx.Polecat)
		}
		return ""
	case RoleCrew:
		if ctx.Rig != "" && ctx.Polecat != "" {
			prefix := beads.GetPrefixForRig(ctx.TownRoot, ctx.Rig)
			return beads.CrewBeadIDWithPrefix(prefix, ctx.Rig, ctx.Polecat)
		}
		return ""
	default:
		return ""
	}
}

// ensureBeadsRedirect ensures the .beads/redirect file exists for worktree-based roles.
// This handles cases where git clean or other operations delete the redirect file.
// Uses the shared SetupRedirect helper which handles both tracked and local beads.
func ensureBeadsRedirect(ctx RoleContext) {
	// Only applies to worktree-based roles that use shared beads
	if ctx.Role != RoleCrew && ctx.Role != RolePolecat && ctx.Role != RoleRefinery {
		return
	}

	// Check if redirect already exists
	redirectPath := filepath.Join(ctx.WorkDir, ".beads", "redirect")
	if _, err := os.Stat(redirectPath); err == nil {
		return // Redirect exists, nothing to do
	}

	// Use shared helper - silently ignore errors during prime
	_ = beads.SetupRedirect(ctx.TownRoot, ctx.WorkDir)
}

package cmd

// handoff_session.go — session/role resolution helpers used by the handoff
// command. Split out of handoff.go to keep each file focused (gu-a1q).
//
// The functions here translate between role names, paths, tmux session names,
// and working directories. They're pure resolution helpers with no session
// mutation — that lives in handoff_restart.go and handoff.go itself.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// getCurrentTmuxSession returns the current tmux session name.
func getCurrentTmuxSession() (string, error) {
	// Prefer GT_ROLE for session resolution. BuildCommand uses -L <town-socket>,
	// but the calling process may live on the default socket (e.g., Claude Code
	// spawned by tmux on the default server). In that case, display-message on
	// the town socket returns an arbitrary session (often hq-boot) instead of
	// the caller's actual session.
	if role := os.Getenv("GT_ROLE"); role != "" {
		resolved, err := resolveRoleToSession(role)
		if err == nil && resolved != "" {
			return resolved, nil
		}
		// Fall through to tmux detection if role resolution fails
	}

	// Use TMUX_PANE for targeted display-message to avoid returning an
	// arbitrary session when multiple sessions share the town socket.
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return "", fmt.Errorf("TMUX_PANE not set")
	}
	out, err := tmux.BuildCommand("display-message", "-t", pane, "-p", "#{session_name}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveRoleToSession converts a role name or path to a tmux session name.
// Accepts:
//   - Role shortcuts: "crew", "witness", "refinery", "mayor", "deacon"
//   - Full paths: "<rig>/crew/<name>", "<rig>/witness", "<rig>/refinery"
//   - Direct session names (passed through)
//
// For role shortcuts that need context (crew, witness, refinery), it auto-detects from environment.
func resolveRoleToSession(role string) (string, error) {
	// First, check if it's a path format (contains /)
	if strings.Contains(role, "/") {
		return resolvePathToSession(role)
	}

	switch strings.ToLower(role) {
	case constants.RoleMayor, "may":
		return getMayorSessionName(), nil

	case constants.RoleDeacon, "dea":
		return getDeaconSessionName(), nil

	case constants.RoleCrew:
		// Try to get rig and crew name from environment or cwd
		rig := os.Getenv("GT_RIG")
		crewName := os.Getenv("GT_CREW")
		if rig == "" || crewName == "" {
			// Try to detect from cwd
			detected, err := detectCrewFromCwd()
			if err == nil {
				rig = detected.rigName
				crewName = detected.crewName
			}
		}
		if rig == "" || crewName == "" {
			return "", fmt.Errorf("cannot determine crew identity - run from crew directory or specify GT_RIG/GT_CREW")
		}
		return session.CrewSessionName(session.PrefixFor(rig), crewName), nil

	case constants.RoleWitness, "wit":
		rig := os.Getenv("GT_RIG")
		if rig == "" {
			return "", fmt.Errorf("cannot determine rig - set GT_RIG or run from rig context")
		}
		return session.WitnessSessionName(session.PrefixFor(rig)), nil

	case constants.RoleRefinery, "ref":
		rig := os.Getenv("GT_RIG")
		if rig == "" {
			return "", fmt.Errorf("cannot determine rig - set GT_RIG or run from rig context")
		}
		return session.RefinerySessionName(session.PrefixFor(rig)), nil

	default:
		// Assume it's a direct session name (e.g., gt-gastown-crew-max)
		return role, nil
	}
}

// resolvePathToSession converts a path like "<rig>/crew/<name>" to a session name.
// Supported formats:
//   - <rig>/crew/<name> -> gt-<rig>-crew-<name>
//   - <rig>/witness -> gt-<rig>-witness
//   - <rig>/refinery -> gt-<rig>-refinery
//   - <rig>/polecats/<name> -> gt-<rig>-<name> (explicit polecat)
//   - <rig>/<name> -> gt-<rig>-<name> (polecat shorthand, if name isn't a known role)
func resolvePathToSession(path string) (string, error) {
	parts := strings.Split(path, "/")

	// Handle <rig>/crew/<name> format
	if len(parts) == 3 && parts[1] == constants.RoleCrew {
		rig := parts[0]
		name := parts[2]
		return session.CrewSessionName(session.PrefixFor(rig), name), nil
	}

	// Handle <rig>/polecats/<name> format (explicit polecat path)
	if len(parts) == 3 && parts[1] == "polecats" {
		rig := parts[0]
		name := strings.ToLower(parts[2]) // normalize polecat name
		return session.PolecatSessionName(session.PrefixFor(rig), name), nil
	}

	// Handle <rig>/<role-or-polecat> format
	if len(parts) == 2 {
		rig := parts[0]
		second := parts[1]
		secondLower := strings.ToLower(second)

		// Check for known roles first
		switch secondLower {
		case constants.RoleWitness:
			return session.WitnessSessionName(session.PrefixFor(rig)), nil
		case constants.RoleRefinery:
			return session.RefinerySessionName(session.PrefixFor(rig)), nil
		case constants.RoleCrew:
			// Just "<rig>/crew" without a name - need more info
			return "", fmt.Errorf("crew path requires name: %s/crew/<name>", rig)
		case "polecats":
			// Just "<rig>/polecats" without a name - need more info
			return "", fmt.Errorf("polecats path requires name: %s/polecats/<name>", rig)
		default:
			// Not a known role - check if it's a crew member before assuming polecat.
			// Crew members exist at <townRoot>/<rig>/crew/<name>.
			// This fixes: gt sling gt-375 gastown/max failing because max is crew, not polecat.
			townRoot := detectTownRootFromCwd()
			if townRoot != "" {
				crewPath := filepath.Join(townRoot, rig, "crew", second)
				if info, err := os.Stat(crewPath); err == nil && info.IsDir() {
					return session.CrewSessionName(session.PrefixFor(rig), second), nil
				}
			}
			// Not a crew member - treat as polecat name (e.g., gastown/nux)
			return session.PolecatSessionName(session.PrefixFor(rig), secondLower), nil
		}
	}

	return "", fmt.Errorf("cannot parse path '%s' - expected <rig>/<polecat>, <rig>/crew/<name>, <rig>/witness, or <rig>/refinery", path)
}

// sessionWorkDir returns the correct working directory for a session.
// This is the canonical home for each role type.
func sessionWorkDir(sessionName, townRoot string) (string, error) {
	// Get session names for comparison
	mayorSession := getMayorSessionName()
	deaconSession := getDeaconSessionName()

	bootSession := session.BootSessionName()

	switch {
	case sessionName == mayorSession:
		// Mayor runs from ~/gt/mayor/, not town root.
		// Tools use workspace.FindFromCwd() which walks UP to find town root.
		return townRoot + "/mayor", nil

	case sessionName == bootSession:
		// Boot watchdog runs from ~/gt/deacon/dogs/boot/, not ~/gt/deacon/.
		// Boot is ephemeral (fresh each daemon tick) with its own CLAUDE.md.
		return townRoot + "/deacon/dogs/boot", nil

	case sessionName == deaconSession:
		return townRoot + "/deacon", nil

	case strings.Contains(sessionName, "-crew-"):
		// gt-<rig>-crew-<name> -> <townRoot>/<rig>/crew/<name>
		rig, name, _, ok := parseCrewSessionName(sessionName)
		if !ok {
			return "", fmt.Errorf("cannot parse crew session name: %s", sessionName)
		}
		return fmt.Sprintf("%s/%s/crew/%s", townRoot, rig, name), nil

	default:
		// Parse session name to determine role and resolve paths
		identity, err := session.ParseSessionName(sessionName)
		if err != nil {
			return "", fmt.Errorf("unknown session type: %s (%w)", sessionName, err)
		}
		switch identity.Role {
		case session.RoleMayor:
			return townRoot + "/mayor", nil
		case session.RoleDeacon:
			return townRoot + "/deacon", nil
		case session.RoleOverseer:
			return townRoot + "/deacon", nil
		case session.RoleWitness:
			return fmt.Sprintf("%s/%s/witness", townRoot, identity.Rig), nil
		case session.RoleRefinery:
			return fmt.Sprintf("%s/%s/refinery/rig", townRoot, identity.Rig), nil
		case session.RolePolecat:
			return fmt.Sprintf("%s/%s/polecats/%s", townRoot, identity.Rig, identity.Name), nil
		case session.RoleDog:
			return fmt.Sprintf("%s/deacon/dogs/%s", townRoot, identity.Name), nil
		default:
			return "", fmt.Errorf("unknown session type: %s (role %s, try specifying role explicitly)", sessionName, identity.Role)
		}
	}
}

// sessionToGTRole converts a session name to a GT_ROLE value.
// Uses session.ParseSessionName for consistent parsing across the codebase.
func sessionToGTRole(sessionName string) string {
	identity, err := session.ParseSessionName(sessionName)
	if err != nil {
		return ""
	}
	return identity.GTRole()
}

// detectTownRootFromCwd walks up from the current directory to find the town root.
// Falls back to GT_TOWN_ROOT or GT_ROOT env vars if cwd detection fails (broken state recovery).
func detectTownRootFromCwd() string {
	// Use workspace.FindFromCwd which handles both primary (mayor/town.json)
	// and secondary (mayor/ directory) markers
	townRoot, err := workspace.FindFromCwd()
	if err == nil && townRoot != "" {
		return townRoot
	}

	// Fallback: try environment variables for town root
	// GT_TOWN_ROOT is set by shell integration, GT_ROOT is set by session manager
	// This enables handoff to work even when cwd detection fails due to
	// detached HEAD, wrong branch, deleted worktree, etc.
	for _, envName := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		if envRoot := os.Getenv(envName); envRoot != "" {
			// Verify it's actually a workspace
			if _, statErr := os.Stat(filepath.Join(envRoot, workspace.PrimaryMarker)); statErr == nil {
				return envRoot
			}
			// Try secondary marker too
			if info, statErr := os.Stat(filepath.Join(envRoot, workspace.SecondaryMarker)); statErr == nil && info.IsDir() {
				return envRoot
			}
		}
	}

	// Final fallback: read GT_TOWN_ROOT from tmux global environment.
	// This handles the run-shell case where CWD is $HOME and process env
	// vars aren't set — the daemon sets GT_TOWN_ROOT in tmux global env.
	if socket := tmux.SocketFromEnv(); socket != "" {
		t := tmux.NewTmuxWithSocket(socket)
		if envRoot, err := t.GetGlobalEnvironment("GT_TOWN_ROOT"); err == nil && envRoot != "" {
			if _, statErr := os.Stat(filepath.Join(envRoot, workspace.PrimaryMarker)); statErr == nil {
				return envRoot
			}
			if info, statErr := os.Stat(filepath.Join(envRoot, workspace.SecondaryMarker)); statErr == nil && info.IsDir() {
				return envRoot
			}
		}
	}

	return ""
}

// handoffRemoteSession respawns a different session and optionally switches to it.
func handoffRemoteSession(t *tmux.Tmux, targetSession, restartCmd string) error {
	// Check if target session exists
	exists, err := t.HasSession(targetSession)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session '%s' not found - is the agent running?", targetSession)
	}

	// Get the pane ID for the target session
	targetPane, err := getSessionPane(targetSession)
	if err != nil {
		return fmt.Errorf("getting target pane: %w", err)
	}

	fmt.Printf("%s Handing off %s...\n", style.Bold.Render("🤝"), targetSession)

	// Dry run mode
	if handoffDryRun {
		fmt.Printf("Would execute: tmux clear-history -t %s\n", targetPane)
		fmt.Printf("Would execute: tmux respawn-pane -k -t %s %s\n", targetPane, restartCmd)
		if handoffWatch {
			fmt.Printf("Would execute: tmux switch-client -t %s\n", targetSession)
		}
		return nil
	}

	// Set remain-on-exit so the pane survives process death during handoff.
	// Without this, killing processes causes tmux to destroy the pane before
	// we can respawn it. This is essential for tmux session reuse.
	if err := t.SetRemainOnExit(targetPane, true); err != nil {
		style.PrintWarning("could not set remain-on-exit: %v", err)
	}

	// Kill all processes in the pane before respawning to prevent orphan leaks
	// RespawnPane's -k flag only sends SIGHUP which Claude/Node may ignore
	if err := t.KillPaneProcesses(targetPane); err != nil {
		// Non-fatal but log the warning
		style.PrintWarning("could not kill pane processes: %v", err)
	}

	// Clear scrollback history before respawn (resets copy-mode from [0/N] to [0/0])
	if err := t.ClearHistory(targetPane); err != nil {
		// Non-fatal - continue with respawn even if clear fails
		style.PrintWarning("could not clear history: %v", err)
	}

	// Respawn the remote session's pane, handling deleted working directories
	respawnErr := func() error {
		paneWorkDir, _ := t.GetPaneWorkDir(targetSession)
		if paneWorkDir != "" {
			if _, statErr := os.Stat(paneWorkDir); statErr != nil {
				if townRoot := detectTownRootFromCwd(); townRoot != "" {
					style.PrintWarning("pane working directory deleted, using town root")
					return t.RespawnPaneWithWorkDir(targetPane, townRoot, restartCmd)
				}
			}
		}
		return t.RespawnPane(targetPane, restartCmd)
	}()
	if respawnErr != nil {
		return fmt.Errorf("respawning pane: %w", respawnErr)
	}

	// If --watch, switch to that session
	if handoffWatch {
		fmt.Printf("Switching to %s...\n", targetSession)
		// Use tmux switch-client to move our view to the target session
		if err := tmux.BuildCommand("switch-client", "-t", targetSession).Run(); err != nil {
			// Non-fatal - they can manually switch
			fmt.Printf("Note: Could not auto-switch (use: tmux switch-client -t %s)\n", targetSession)
		}
	}

	return nil
}

// getSessionPane returns the pane identifier for a session's main pane.
func getSessionPane(sessionName string) (string, error) {
	// Get the pane ID for the first pane in the session
	out, err := tmux.BuildCommand("list-panes", "-t", sessionName, "-F", "#{pane_id}").Output()
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("no panes found in session")
	}
	return lines[0], nil
}

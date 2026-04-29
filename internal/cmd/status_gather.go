package cmd

// Gather status data for the `gt status` command. Collects tmux sessions,
// dolt/daemon/acp state, rig metadata, agent beads, hook beads, mail
// counts, etc. in parallel and assembles them into a TownStatus. Pure
// data collection — rendering lives in status_render.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/crew"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/mayor"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

func gatherStatus() (TownStatus, error) {
	// Find town root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return TownStatus{}, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load town config
	townConfigPath := constants.MayorTownPath(townRoot)
	townConfig, err := config.LoadTownConfig(townConfigPath)
	if err != nil {
		// Try to continue without config
		townConfig = &config.TownConfig{Name: filepath.Base(townRoot)}
	}

	// Load rigs config
	rigsConfigPath := constants.MayorRigsPath(townRoot)
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		// Empty config if file doesn't exist
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	// Load town settings for agent display info
	townSettings, _ := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))

	// Create rig manager
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	// Create tmux instance for runtime checks
	t := tmux.NewTmux()

	// Pre-fetch all tmux sessions and verify agent liveness for O(1) lookup.
	// A Gas Town session is only considered "running" if the agent process is
	// alive inside it, not merely if the tmux session exists. This prevents
	// zombie sessions (tmux alive, agent dead) from showing as running.
	// See: gt-bd6i3
	allSessions := make(map[string]bool)
	if sessions, err := t.ListSessions(); err == nil {
		var sessionMu sync.Mutex
		var sessionWg sync.WaitGroup
		for _, s := range sessions {
			if session.IsKnownSession(s) {
				sessionWg.Add(1)
				go func(name string) {
					defer sessionWg.Done()
					alive := t.IsAgentAlive(name)
					sessionMu.Lock()
					allSessions[name] = alive
					sessionMu.Unlock()
				}(s)
			} else {
				allSessions[s] = true
			}
		}
		sessionWg.Wait()
	}

	// Discover rigs
	rigs, err := mgr.DiscoverRigs()
	if err != nil {
		return TownStatus{}, fmt.Errorf("discovering rigs: %w", err)
	}

	// Pre-fetch agent beads across all rig-specific beads DBs.
	// In --fast mode, parallelize these fetches for better performance.
	allAgentBeads := make(map[string]*beads.Issue)
	allHookBeads := make(map[string]*beads.Issue)
	var beadsMu sync.Mutex // Protects allAgentBeads and allHookBeads

	// Helper to safely merge beads into the shared maps
	mergeAgentBeads := func(beadsMap map[string]*beads.Issue) {
		beadsMu.Lock()
		for id, issue := range beadsMap {
			allAgentBeads[id] = issue
		}
		beadsMu.Unlock()
	}
	mergeHookBeads := func(beadsMap map[string]*beads.Issue) {
		beadsMu.Lock()
		for id, issue := range beadsMap {
			allHookBeads[id] = issue
		}
		beadsMu.Unlock()
	}

	var beadsWg sync.WaitGroup

	// Fetch town-level agent beads (Mayor, Deacon) from town beads
	townBeadsPath := beads.GetTownBeadsPath(townRoot)
	beadsWg.Add(1)
	go func() {
		defer beadsWg.Done()
		townBeadsClient := beads.New(townBeadsPath)
		townAgentBeads, _ := townBeadsClient.ListAgentBeads()
		mergeAgentBeads(townAgentBeads)

		// Fetch hook beads from town beads
		var townHookIDs []string
		for _, issue := range townAgentBeads {
			hookID := issue.HookBead
			if hookID == "" {
				fields := beads.ParseAgentFields(issue.Description)
				if fields != nil {
					hookID = fields.HookBead
				}
			}
			if hookID != "" {
				townHookIDs = append(townHookIDs, hookID)
			}
		}
		if len(townHookIDs) > 0 {
			townHookBeads, _ := townBeadsClient.ShowMultiple(townHookIDs)
			mergeHookBeads(townHookBeads)
		}
	}()

	// Fetch rig-level agent beads in parallel
	for _, r := range rigs {
		beadsWg.Add(1)
		go func(r *rig.Rig) {
			defer beadsWg.Done()
			rigBeadsPath := filepath.Join(r.Path, "mayor", "rig")
			rigBeads := beads.New(rigBeadsPath)
			rigAgentBeads, _ := rigBeads.ListAgentBeads()
			if rigAgentBeads == nil {
				return
			}
			mergeAgentBeads(rigAgentBeads)

			var hookIDs []string
			for _, issue := range rigAgentBeads {
				// Use the HookBead field from the database column; fall back for legacy beads.
				hookID := issue.HookBead
				if hookID == "" {
					fields := beads.ParseAgentFields(issue.Description)
					if fields != nil {
						hookID = fields.HookBead
					}
				}
				if hookID != "" {
					hookIDs = append(hookIDs, hookID)
				}
			}

			if len(hookIDs) == 0 {
				return
			}
			hookBeads, _ := rigBeads.ShowMultiple(hookIDs)
			mergeHookBeads(hookBeads)
		}(r)
	}

	beadsWg.Wait()

	// Create mail router for inbox lookups
	mailRouter := mail.NewRouter(townRoot)

	// Load overseer config
	var overseerInfo *OverseerInfo
	if overseerConfig, err := config.LoadOrDetectOverseer(townRoot); err == nil && overseerConfig != nil {
		overseerInfo = &OverseerInfo{
			Name:     overseerConfig.Name,
			Email:    overseerConfig.Email,
			Username: overseerConfig.Username,
			Source:   overseerConfig.Source,
		}
		// Get overseer mail count (skip in --fast mode)
		if !statusFast {
			if mailbox, err := mailRouter.GetMailbox("overseer"); err == nil {
				_, unread, _ := mailbox.Count()
				overseerInfo.UnreadMail = unread
			}
		}
	}

	// Build status - parallel fetch global agents and rigs
	status := TownStatus{
		Name:     townConfig.Name,
		Location: townRoot,
		Overseer: overseerInfo,
		DND:      detectCurrentDNDStatus(townRoot),
		Rigs:     make([]RigStatus, len(rigs)),
	}

	// Daemon status
	if daemonRunning, daemonPid, err := daemon.IsRunning(townRoot); err == nil {
		status.Daemon = &ServiceInfo{Running: daemonRunning, PID: daemonPid}
	}

	// Dolt status
	doltCfg := doltserver.DefaultConfig(townRoot)
	if doltCfg.IsRemote() {
		status.Dolt = &DoltInfo{Remote: true, Port: doltCfg.Port}
	} else {
		doltRunning, doltPid, _ := doltserver.IsRunning(townRoot)
		port := doltCfg.Port
		if doltRunning {
			// Read the actual port from state — doltCfg.Port comes from
			// DefaultConfig which reads GT_DOLT_PORT from the shell env,
			// but gt status is typically run without that env var set.
			if state, err := doltserver.LoadState(townRoot); err == nil && state.Port > 0 {
				port = state.Port
			}
		}
		doltInfo := &DoltInfo{
			Running: doltRunning,
			PID:     doltPid,
			Port:    port,
			DataDir: doltCfg.DataDir,
		}
		// Check if port is held by another town's Dolt
		if !doltRunning {
			if conflictPid, conflictDir := doltserver.CheckPortConflict(townRoot); conflictPid > 0 {
				doltInfo.PortConflict = true
				doltInfo.ConflictOwner = conflictDir
			}
		}
		status.Dolt = doltInfo
	}

	// Tmux status
	socket := tmux.GetDefaultSocket()
	socketLabel := "default"
	if socket != "" {
		socketLabel = socket
	}
	tmuxInfo := &TmuxInfo{
		Socket:       socketLabel,
		SessionCount: len(allSessions),
		Running:      len(allSessions) > 0,
	}
	// Resolve socket path: /tmp/tmux-<UID>/<socket>
	tmuxInfo.SocketPath = filepath.Join(tmux.SocketDir(), socketLabel)
	if _, err := os.Stat(tmuxInfo.SocketPath); err == nil {
		tmuxInfo.Running = true
		tmuxInfo.PID = tmux.NewTmux().ServerPID()
	}
	status.Tmux = tmuxInfo

	// ACP status
	if mayor.IsACPActive(townRoot) {
		acpPid, _ := mayor.GetACPPid(townRoot)
		status.ACP = &ServiceInfo{Running: true, PID: acpPid}
	}

	var wg sync.WaitGroup

	// Fetch global agents in parallel with rig discovery
	wg.Add(1)
	go func() {
		defer wg.Done()
		status.Agents = discoverGlobalAgents(townRoot, allSessions, allAgentBeads, allHookBeads, mailRouter, statusFast)
	}()

	// Process all rigs in parallel
	rigActiveHooks := make([]int, len(rigs)) // Track hooks per rig for thread safety
	for i, r := range rigs {
		wg.Add(1)
		go func(idx int, r *rig.Rig) {
			defer wg.Done()

			rs := RigStatus{
				Name:         r.Name,
				Polecats:     r.Polecats,
				PolecatCount: len(r.Polecats),
				HasWitness:   r.HasWitness,
				HasRefinery:  r.HasRefinery,
			}

			// Count crew workers
			crewGit := git.NewGit(r.Path)
			crewMgr := crew.NewManager(r, crewGit)
			if workers, err := crewMgr.List(); err == nil {
				for _, w := range workers {
					rs.Crews = append(rs.Crews, w.Name)
				}
				rs.CrewCount = len(workers)
			}

			// Run hooks, agents, and MQ discovery concurrently within this rig.
			// Each was previously sequential; now they overlap since they use
			// independent bd/beads calls.
			var rigWg sync.WaitGroup

			// Discover hooks for all agents in this rig
			// In --fast mode, skip expensive handoff bead lookups. Hook info comes from
			// preloaded agent beads via discoverRigAgents instead.
			if !statusFast {
				rigWg.Add(1)
				go func() {
					defer rigWg.Done()
					rs.Hooks = discoverRigHooks(r, rs.Crews)
				}()
			}

			// Get MQ summary if rig has a refinery
			// Skip in --fast mode to avoid expensive bd queries
			if !statusFast {
				rigWg.Add(1)
				go func() {
					defer rigWg.Done()
					rs.MQ = getMQSummary(r)
				}()
			}

			// Discover runtime state for all agents in this rig
			// (uses preloaded maps, so it's fast — but run concurrently with hooks/MQ)
			rigWg.Add(1)
			go func() {
				defer rigWg.Done()
				rs.Agents = discoverRigAgents(allSessions, r, rs.Crews, allAgentBeads, allHookBeads, mailRouter, statusFast)
			}()

			rigWg.Wait()

			activeHooks := 0
			for _, hook := range rs.Hooks {
				if hook.HasWork {
					activeHooks++
				}
			}
			rigActiveHooks[idx] = activeHooks

			status.Rigs[idx] = rs
		}(i, r)
	}

	wg.Wait()

	// Enrich agents with runtime info — inspect actual running processes
	for i := range status.Agents {
		a := &status.Agents[i]
		alias, info := resolveAgentDisplay(townRoot, townSettings, a.Role, a.Session, a.Running)
		a.AgentAlias = alias
		a.AgentInfo = info
	}
	for i := range status.Rigs {
		for j := range status.Rigs[i].Agents {
			a := &status.Rigs[i].Agents[j]
			alias, info := resolveAgentDisplay(townRoot, townSettings, a.Role, a.Session, a.Running)
			a.AgentAlias = alias
			a.AgentInfo = info
		}
	}

	// Aggregate summary (after parallel work completes)
	for i, rs := range status.Rigs {
		status.Summary.PolecatCount += rs.PolecatCount
		status.Summary.CrewCount += rs.CrewCount
		status.Summary.ActiveHooks += rigActiveHooks[i]
		if rs.HasWitness {
			status.Summary.WitnessCount++
		}
		if rs.HasRefinery {
			status.Summary.RefineryCount++
		}
	}
	status.Summary.RigCount = len(rigs)

	return status, nil
}

package cmd

// Agent discovery for the `gt status` command. Builds AgentRuntime
// entries for town-level agents (Mayor, Deacon) and rig agents (Witness,
// Refinery, Polecats, Crew) using preloaded tmux session and bead maps.
// Also provides helpers for discovering hook (pinned work) attachments
// and summarizing the merge queue.

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/mayor"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
)

// discoverRigHooks finds all hook attachments for agents in a rig.
// It fetches all pinned handoff beads in a single bd call, then resolves
// each agent's hook in-memory. This replaces the previous N+1 pattern where
// each agent triggered a separate bd subprocess.
func discoverRigHooks(r *rig.Rig, crews []string) []AgentHookInfo {
	var hooks []AgentHookInfo

	// Create beads instance for the rig
	b := beads.New(r.Path)

	// Batch-fetch all handoff beads in one bd call
	allHandoffs, err := b.FindAllHandoffBeads()
	if err != nil {
		// On error, return empty hooks for all agents rather than failing
		allHandoffs = make(map[string]*beads.Issue)
	}

	// Check polecats
	for _, name := range r.Polecats {
		hooks = append(hooks, resolveHookFromMap(allHandoffs, name, r.Name+"/"+name, constants.RolePolecat))
	}

	// Check crew workers
	for _, name := range crews {
		hooks = append(hooks, resolveHookFromMap(allHandoffs, name, r.Name+"/crew/"+name, constants.RoleCrew))
	}

	// Check witness
	if r.HasWitness {
		hooks = append(hooks, resolveHookFromMap(allHandoffs, constants.RoleWitness, r.Name+"/witness", constants.RoleWitness))
	}

	// Check refinery
	if r.HasRefinery {
		hooks = append(hooks, resolveHookFromMap(allHandoffs, constants.RoleRefinery, r.Name+"/refinery", constants.RoleRefinery))
	}

	return hooks
}

// resolveHookFromMap builds an AgentHookInfo from a pre-fetched map of handoff beads.
// This is the in-memory equivalent of getAgentHook, avoiding per-agent bd subprocess calls.
func resolveHookFromMap(allHandoffs map[string]*beads.Issue, role, agentAddress, roleType string) AgentHookInfo {
	hook := AgentHookInfo{
		Agent: agentAddress,
		Role:  roleType,
	}

	handoff, ok := allHandoffs[role]
	if !ok || handoff == nil {
		return hook
	}

	attachment := beads.ParseAttachmentFields(handoff)
	if attachment != nil && attachment.AttachedMolecule != "" {
		hook.HasWork = true
		hook.Molecule = attachment.AttachedMolecule
		hook.Title = handoff.Title
	} else if handoff.Description != "" {
		hook.HasWork = true
		hook.Title = handoff.Title
	}

	return hook
}

// discoverGlobalAgents checks runtime state for town-level agents (Mayor, Deacon).
// Uses parallel fetching for performance. If skipMail is true, mail lookups are skipped.
// allSessions is a preloaded map of tmux sessions for O(1) lookup.
// allAgentBeads is a preloaded map of agent beads for O(1) lookup.
// allHookBeads is a preloaded map of hook beads for O(1) lookup.
func discoverGlobalAgents(townRoot string, allSessions map[string]bool, allAgentBeads map[string]*beads.Issue, allHookBeads map[string]*beads.Issue, mailRouter *mail.Router, skipMail bool) []AgentRuntime {
	// Get session names dynamically
	mayorSession := getMayorSessionName()
	deaconSession := getDeaconSessionName()

	// Define agents to discover
	// Note: Mayor and Deacon are town-level agents with hq- prefix bead IDs
	agentDefs := []struct {
		name    string
		address string
		session string
		role    string
		beadID  string
	}{
		{constants.RoleMayor, constants.RoleMayor + "/", mayorSession, "coordinator", beads.MayorBeadIDTown()},
		{constants.RoleDeacon, constants.RoleDeacon + "/", deaconSession, "health-check", beads.DeaconBeadIDTown()},
	}

	agents := make([]AgentRuntime, len(agentDefs))
	var wg sync.WaitGroup

	for i, def := range agentDefs {
		wg.Add(1)
		go func(idx int, d struct {
			name    string
			address string
			session string
			role    string
			beadID  string
		}) {
			defer wg.Done()

			agent := AgentRuntime{
				Name:    d.name,
				Address: d.address,
				Session: d.session,
				Role:    d.role,
			}

			// Check tmux session from preloaded map (O(1))
			agent.Running = allSessions[d.session]

			// Check for ACP session (for Mayor)
			if d.name == "mayor" {
				if mayor.IsACPActive(townRoot) {
					agent.ACP = true
					agent.Running = true
				}
			}

			// Look up agent bead from preloaded map (O(1))
			if issue, ok := allAgentBeads[d.beadID]; ok {
				// Prefer database columns over description parsing
				// HookBead column is authoritative (cleared by unsling)
				agent.HookBead = issue.HookBead
				agent.State = beads.ResolveAgentState(issue.Description, issue.AgentState)
				if agent.HookBead != "" {
					agent.HasWork = true
					// Get hook title from preloaded map
					if pinnedIssue, ok := allHookBeads[agent.HookBead]; ok {
						agent.WorkTitle = pinnedIssue.Title
					}
				}
				// Parse description fields for notification level
				if fields := beads.ParseAgentFields(issue.Description); fields != nil {
					agent.NotificationLevel = fields.NotificationLevel
				}
			}

			// Get mail info (skip if --fast)
			if !skipMail {
				populateMailInfo(&agent, mailRouter)
			}

			agents[idx] = agent
		}(i, def)
	}

	wg.Wait()
	return agents
}

// populateMailInfo fetches unread mail count and first subject for an agent
func populateMailInfo(agent *AgentRuntime, router *mail.Router) {
	if router == nil {
		return
	}
	mailbox, err := router.GetMailbox(agent.Address)
	if err != nil {
		return
	}
	_, unread, _ := mailbox.Count()
	agent.UnreadMail = unread
	if unread > 0 {
		if messages, err := mailbox.ListUnread(); err == nil && len(messages) > 0 {
			agent.FirstSubject = messages[0].Subject
		}
	}
}

// detectCurrentDNDStatus returns DND status for the currently resolved role context.
// Returns nil when role context cannot be determined (e.g. outside agent context).
func detectCurrentDNDStatus(townRoot string) *DNDInfo {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return nil
	}

	ctx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}
	agentBeadID := getAgentBeadID(ctx)
	if agentBeadID == "" {
		return nil
	}

	bd := beads.New(townRoot)
	level, err := bd.GetAgentNotificationLevel(agentBeadID)
	if err != nil || level == "" {
		level = beads.NotifyNormal
	}

	return &DNDInfo{
		Enabled: level == beads.NotifyMuted,
		Level:   level,
		Agent:   agentBeadID,
	}
}

// discoverRigAgents checks runtime state for all agents in a rig.
// Uses parallel fetching for performance. If skipMail is true, mail lookups are skipped.
// allSessions is a preloaded map of tmux sessions for O(1) lookup.
// allAgentBeads is a preloaded map of agent beads for O(1) lookup.
// allHookBeads is a preloaded map of hook beads for O(1) lookup.
func discoverRigAgents(allSessions map[string]bool, r *rig.Rig, crews []string, allAgentBeads map[string]*beads.Issue, allHookBeads map[string]*beads.Issue, mailRouter *mail.Router, skipMail bool) []AgentRuntime {
	// Build list of all agents to discover
	var defs []agentDef
	townRoot := filepath.Dir(r.Path)
	prefix := beads.GetPrefixForRig(townRoot, r.Name)

	// Witness
	if r.HasWitness {
		defs = append(defs, agentDef{
			name:    constants.RoleWitness,
			address: r.Name + "/witness",
			session: witnessSessionName(r.Name),
			role:    constants.RoleWitness,
			beadID:  beads.WitnessBeadIDWithPrefix(prefix, r.Name),
		})
	}

	// Refinery
	if r.HasRefinery {
		defs = append(defs, agentDef{
			name:    constants.RoleRefinery,
			address: r.Name + "/refinery",
			session: session.RefinerySessionName(session.PrefixFor(r.Name)),
			role:    constants.RoleRefinery,
			beadID:  beads.RefineryBeadIDWithPrefix(prefix, r.Name),
		})
	}

	// Polecats
	for _, name := range r.Polecats {
		defs = append(defs, agentDef{
			name:    name,
			address: r.Name + "/" + name,
			session: session.PolecatSessionName(session.PrefixFor(r.Name), name),
			role:    constants.RolePolecat,
			beadID:  beads.PolecatBeadIDWithPrefix(prefix, r.Name, name),
		})
	}

	// Crew
	for _, name := range crews {
		defs = append(defs, agentDef{
			name:    name,
			address: r.Name + "/crew/" + name,
			session: crewSessionName(r.Name, name),
			role:    constants.RoleCrew,
			beadID:  beads.CrewBeadIDWithPrefix(prefix, r.Name, name),
		})
	}

	if len(defs) == 0 {
		return nil
	}

	// Fetch all agents in parallel
	agents := make([]AgentRuntime, len(defs))
	var wg sync.WaitGroup

	for i, def := range defs {
		wg.Add(1)
		go func(idx int, d agentDef) {
			defer wg.Done()

			agent := AgentRuntime{
				Name:    d.name,
				Address: d.address,
				Session: d.session,
				Role:    d.role,
			}

			// Check tmux session from preloaded map (O(1))
			agent.Running = allSessions[d.session]

			// Look up agent bead from preloaded map (O(1))
			if issue, ok := allAgentBeads[d.beadID]; ok {
				// Prefer database columns over description parsing
				// HookBead column is authoritative (cleared by unsling)
				agent.HookBead = issue.HookBead
				agent.State = beads.ResolveAgentState(issue.Description, issue.AgentState)
				if agent.HookBead != "" {
					agent.HasWork = true
					// Get hook title from preloaded map
					if pinnedIssue, ok := allHookBeads[agent.HookBead]; ok {
						agent.WorkTitle = pinnedIssue.Title
					}
				}
				// Parse description fields for notification level
				if fields := beads.ParseAgentFields(issue.Description); fields != nil {
					agent.NotificationLevel = fields.NotificationLevel
				}
			}

			// Get mail info (skip if --fast)
			if !skipMail {
				populateMailInfo(&agent, mailRouter)
			}

			agents[idx] = agent
		}(i, def)
	}

	wg.Wait()
	return agents
}

// getMQSummary queries beads for merge-request issues and returns a summary.
// Uses a single bd call to fetch all non-closed merge-requests, then splits
// open vs in_progress in memory. Previously used two separate bd calls.
// Returns nil if the rig has no refinery or no MQ issues.
func getMQSummary(r *rig.Rig) *MQSummary {
	if !r.HasRefinery {
		return nil
	}

	// Create beads instance for the rig
	b := beads.New(r.BeadsPath())

	// Single query for all non-closed merge-request issues.
	// Status "all" fetches everything; we filter open/in_progress in memory.
	opts := beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "all",
		Priority: -1, // No priority filter
	}
	allMRs, err := b.List(opts)
	if err != nil {
		return nil
	}

	// Split by status in memory
	pending := 0
	blocked := 0
	inProgress := 0
	for _, mr := range allMRs {
		switch mr.Status {
		case "open":
			if len(mr.BlockedBy) > 0 || mr.BlockedByCount > 0 {
				blocked++
			} else {
				pending++
			}
		case "in_progress":
			inProgress++
		}
		// closed/other statuses are ignored
	}

	// Determine queue state
	state := "idle"
	if inProgress > 0 {
		state = "processing"
	} else if pending > 0 {
		state = "idle" // Has work but not processing yet
	} else if blocked > 0 {
		state = "blocked" // Only blocked items, nothing processable
	}

	// Determine queue health
	health := "empty"
	total := pending + inProgress + blocked
	if total > 0 {
		health = "healthy"
		// Check for potential issues
		if pending > 10 && inProgress == 0 {
			// Large queue but nothing processing - may be stuck
			health = "stale"
		}
	}

	// Only return summary if there's something to show
	if pending == 0 && inProgress == 0 && blocked == 0 {
		return nil
	}

	return &MQSummary{
		Pending:  pending,
		InFlight: inProgress,
		Blocked:  blocked,
		State:    state,
		Health:   health,
	}
}

// getAgentHook retrieves hook status for a specific agent.
func getAgentHook(b *beads.Beads, role, agentAddress, roleType string) AgentHookInfo {
	hook := AgentHookInfo{
		Agent: agentAddress,
		Role:  roleType,
	}

	// Find handoff bead for this role
	handoff, err := b.FindHandoffBead(role)
	if err != nil || handoff == nil {
		return hook
	}

	// Check for attachment
	attachment := beads.ParseAttachmentFields(handoff)
	if attachment != nil && attachment.AttachedMolecule != "" {
		hook.HasWork = true
		hook.Molecule = attachment.AttachedMolecule
		hook.Title = handoff.Title
	} else if handoff.Description != "" {
		// Has content but no molecule - still has work
		hook.HasWork = true
		hook.Title = handoff.Title
	}

	return hook
}

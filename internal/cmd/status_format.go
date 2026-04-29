package cmd

// Formatters for individual pieces rendered by `gt status`: single agent
// lines (compact + verbose detail), merge-queue summaries, hook info,
// and small string helpers. status_render.go uses these to compose the
// overall TownStatus output.

import (
	"fmt"
	"io"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/style"
)

// renderAgentDetails renders full agent bead details
func renderAgentDetails(w io.Writer, agent AgentRuntime, indent string, hooks []AgentHookInfo, townRoot string) { //nolint:unparam // indent kept for future customization
	// Line 1: Agent bead ID + status
	// Per gt-zecmc: derive status from tmux (observable reality), not bead state.
	// "Discover, don't track" - agent liveness is observable from tmux session.
	sessionExists := agent.Running

	var statusStr string
	var stateInfo string

	if sessionExists {
		statusStr = style.Success.Render("running")
	} else {
		statusStr = style.Error.Render("stopped")
	}

	// Show non-observable states that represent intentional agent decisions.
	// These can't be discovered from tmux and are legitimately recorded in beads.
	beadState := agent.State
	switch beadState {
	case "stuck":
		// Agent escalated - needs help
		stateInfo = style.Warning.Render(" [stuck]")
	case "awaiting-gate":
		// Agent waiting for external trigger (phase gate)
		stateInfo = style.Dim.Render(" [awaiting-gate]")
	case "muted", "paused", "degraded":
		// Other intentional non-observable states
		stateInfo = style.Dim.Render(fmt.Sprintf(" [%s]", beadState))
		// Ignore observable states: "running", "idle", "dead", "done", "stopped", ""
		// These should be derived from tmux, not bead.
	}

	// Build agent bead ID using canonical naming: prefix-rig-role-name
	agentBeadID := "gt-" + agent.Name
	if agent.Address != "" && agent.Address != agent.Name {
		// Use address for full path agents like gastown/crew/joe → gt-gastown-crew-joe
		addr := strings.TrimSuffix(agent.Address, "/") // Remove trailing slash for global agents
		parts := strings.Split(addr, "/")
		if len(parts) == 1 {
			// Global agent: mayor/, deacon/ → hq-mayor, hq-deacon
			agentBeadID = beads.AgentBeadIDWithPrefix(beads.TownBeadsPrefix, "", parts[0], "")
		} else if len(parts) >= 2 {
			rig := parts[0]
			prefix := beads.GetPrefixForRig(townRoot, rig)
			if parts[1] == constants.RoleCrew && len(parts) >= 3 {
				agentBeadID = beads.CrewBeadIDWithPrefix(prefix, rig, parts[2])
			} else if parts[1] == constants.RoleWitness {
				agentBeadID = beads.WitnessBeadIDWithPrefix(prefix, rig)
			} else if parts[1] == constants.RoleRefinery {
				agentBeadID = beads.RefineryBeadIDWithPrefix(prefix, rig)
			} else if len(parts) == 2 {
				// polecat: rig/name
				agentBeadID = beads.PolecatBeadIDWithPrefix(prefix, rig, parts[1])
			}
		}
	}

	fmt.Fprintf(w, "%s%s %s%s\n", indent, style.Dim.Render(agentBeadID), statusStr, stateInfo)

	// Line 2: Agent runtime info
	if agent.AgentInfo != "" {
		fmt.Printf("%s  agent: %s\n", indent, agent.AgentInfo)
	}

	// Line 3: Hook bead (pinned work)
	hookStr := style.Dim.Render("(none)")
	hookBead := agent.HookBead
	hookTitle := agent.WorkTitle

	// Fall back to hooks array if agent bead doesn't have hook info
	if hookBead == "" && hooks != nil {
		for _, h := range hooks {
			if h.Agent == agent.Address && h.HasWork {
				hookBead = h.Molecule
				hookTitle = h.Title
				break
			}
		}
	}

	if hookBead != "" {
		if hookTitle != "" {
			hookStr = fmt.Sprintf("%s → %s", hookBead, truncateWithEllipsis(hookTitle, 40))
		} else {
			hookStr = hookBead
		}
	} else if hookTitle != "" {
		// Has title but no molecule ID
		hookStr = truncateWithEllipsis(hookTitle, 50)
	}

	fmt.Fprintf(w, "%s  hook: %s\n", indent, hookStr)

	// Line 4: Notification mode (DND)
	if agent.NotificationLevel == beads.NotifyMuted {
		fmt.Fprintf(w, "%s  notify: 🔕 muted (DND)\n", indent)
	}

	// Line 5: Mail (if any unread)
	if agent.UnreadMail > 0 {
		mailStr := fmt.Sprintf("📬 %d unread", agent.UnreadMail)
		if agent.FirstSubject != "" {
			mailStr = fmt.Sprintf("📬 %d unread → %s", agent.UnreadMail, truncateWithEllipsis(agent.FirstSubject, 35))
		}
		fmt.Fprintf(w, "%s  mail: %s\n", indent, mailStr)
	}
}

// formatMQSummary formats the MQ status for verbose display
func formatMQSummary(mq *MQSummary) string {
	if mq == nil {
		return ""
	}
	mqParts := []string{}
	if mq.Pending > 0 {
		mqParts = append(mqParts, fmt.Sprintf("%d pending", mq.Pending))
	}
	if mq.InFlight > 0 {
		mqParts = append(mqParts, style.Warning.Render(fmt.Sprintf("%d in-flight", mq.InFlight)))
	}
	if mq.Blocked > 0 {
		mqParts = append(mqParts, style.Dim.Render(fmt.Sprintf("%d blocked", mq.Blocked)))
	}
	if len(mqParts) == 0 {
		return ""
	}
	// Add state indicator
	stateIcon := "○" // idle
	switch mq.State {
	case "processing":
		stateIcon = style.Success.Render("●")
	case "blocked":
		stateIcon = style.Error.Render("○")
	}
	// Add health warning if stale
	healthSuffix := ""
	if mq.Health == "stale" {
		healthSuffix = style.Error.Render(" [stale]")
	}
	return fmt.Sprintf("%s %s%s", stateIcon, strings.Join(mqParts, ", "), healthSuffix)
}

// formatMQSummaryCompact formats MQ status for compact single-line display
func formatMQSummaryCompact(mq *MQSummary) string {
	if mq == nil {
		return ""
	}
	// Very compact: "MQ:12" or "MQ:12 [stale]"
	total := mq.Pending + mq.InFlight + mq.Blocked
	if total == 0 {
		return ""
	}
	healthSuffix := ""
	if mq.Health == "stale" {
		healthSuffix = style.Error.Render("[stale]")
	}
	return fmt.Sprintf("MQ:%d%s", total, healthSuffix)
}

// renderAgentCompactWithSuffix renders a single-line agent status with an extra suffix
func renderAgentCompactWithSuffix(w io.Writer, agent AgentRuntime, indent string, hooks []AgentHookInfo, _ string, suffix string) {
	// Build status indicator (gt-zecmc: use tmux state, not bead state)
	statusIndicator := buildStatusIndicator(agent)

	// Get hook info
	hookBead := agent.HookBead
	hookTitle := agent.WorkTitle
	if hookBead == "" && hooks != nil {
		for _, h := range hooks {
			if h.Agent == agent.Address && h.HasWork {
				hookBead = h.Molecule
				hookTitle = h.Title
				break
			}
		}
	}

	// Build hook suffix
	hookSuffix := ""
	if hookBead != "" {
		if hookTitle != "" {
			hookSuffix = style.Dim.Render(" → ") + truncateWithEllipsis(hookTitle, 30)
		} else {
			hookSuffix = style.Dim.Render(" → ") + hookBead
		}
	} else if hookTitle != "" {
		hookSuffix = style.Dim.Render(" → ") + truncateWithEllipsis(hookTitle, 30)
	}

	// Mail indicator
	mailSuffix := ""
	if agent.UnreadMail > 0 {
		mailSuffix = fmt.Sprintf(" 📬%d", agent.UnreadMail)
	}

	// Agent runtime info
	agentSuffix := ""
	if agent.AgentInfo != "" {
		agentSuffix = " " + style.Dim.Render("["+agent.AgentInfo+"]")
	}

	// Print single line: name + status + agent-info + hook + mail + suffix
	fmt.Fprintf(w, "%s%-12s %s%s%s%s%s\n", indent, agent.Name, statusIndicator, agentSuffix, hookSuffix, mailSuffix, suffix)
}

// renderAgentCompact renders a single-line agent status
func renderAgentCompact(w io.Writer, agent AgentRuntime, indent string, hooks []AgentHookInfo, _ string) {
	// Build status indicator (gt-zecmc: use tmux state, not bead state)
	statusIndicator := buildStatusIndicator(agent)

	// Get hook info
	hookBead := agent.HookBead
	hookTitle := agent.WorkTitle
	if hookBead == "" && hooks != nil {
		for _, h := range hooks {
			if h.Agent == agent.Address && h.HasWork {
				hookBead = h.Molecule
				hookTitle = h.Title
				break
			}
		}
	}

	// Build hook suffix
	hookSuffix := ""
	if hookBead != "" {
		if hookTitle != "" {
			hookSuffix = style.Dim.Render(" → ") + truncateWithEllipsis(hookTitle, 30)
		} else {
			hookSuffix = style.Dim.Render(" → ") + hookBead
		}
	} else if hookTitle != "" {
		hookSuffix = style.Dim.Render(" → ") + truncateWithEllipsis(hookTitle, 30)
	}

	// Mail indicator
	mailSuffix := ""
	if agent.UnreadMail > 0 {
		mailSuffix = fmt.Sprintf(" 📬%d", agent.UnreadMail)
	}

	// Agent runtime info
	agentSuffix := ""
	if agent.AgentInfo != "" {
		agentSuffix = " " + style.Dim.Render("["+agent.AgentInfo+"]")
	}

	// Print single line: name + status + agent-info + hook + mail
	fmt.Fprintf(w, "%s%-12s %s%s%s%s\n", indent, agent.Name, statusIndicator, agentSuffix, hookSuffix, mailSuffix)
}

// buildStatusIndicator creates the visual status indicator for an agent.
// Per gt-zecmc: uses tmux state (observable reality), not bead state.
// Non-observable states (stuck, awaiting-gate, muted, etc.) are shown as suffixes.
func buildStatusIndicator(agent AgentRuntime) string {
	sessionExists := agent.Running

	// Base indicator from tmux state or ACP state
	var indicator string
	if sessionExists {
		indicator = style.Success.Render("●")
	} else {
		indicator = style.Error.Render("○")
	}

	// Add mode info if ACP
	if agent.ACP {
		indicator += style.Dim.Render(" acp")
	}

	// Add non-observable state suffix if present
	beadState := agent.State
	switch beadState {
	case "stuck":
		indicator += style.Warning.Render(" stuck")
	case "awaiting-gate":
		indicator += style.Dim.Render(" gate")
	case "muted", "paused", "degraded":
		indicator += style.Dim.Render(" " + beadState)
		// Ignore observable states: running, idle, dead, done, stopped, ""
	}

	if agent.NotificationLevel == beads.NotifyMuted {
		indicator += style.Dim.Render(" 🔕")
	}

	return indicator
}

// formatHookInfo formats the hook bead and title for display
func formatHookInfo(hookBead, title string, maxLen int) string {
	if hookBead == "" {
		return ""
	}
	if title == "" {
		return fmt.Sprintf(" → %s", hookBead)
	}
	title = truncateWithEllipsis(title, maxLen)
	return fmt.Sprintf(" → %s", title)
}

// truncateWithEllipsis shortens a string to maxLen, adding "..." if truncated
func truncateWithEllipsis(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen < 4 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// capitalizeFirst capitalizes the first letter of a string
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:]
}

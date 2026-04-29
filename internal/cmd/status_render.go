package cmd

// Top-level rendering for the `gt status` command. `outputStatusJSON`
// emits JSON; `outputStatusText` composes the full styled text output
// by walking the TownStatus tree and delegating per-item formatting to
// helpers in status_format.go.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/style"
)

func outputStatusJSON(status TownStatus) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(status)
}

func outputStatusText(w io.Writer, status TownStatus) error {
	// Header
	fmt.Fprintf(w, "%s %s\n", style.Bold.Render("Town:"), status.Name)
	fmt.Fprintf(w, "%s\n\n", style.Dim.Render(status.Location))

	// E-stop banner (if active)
	addEstopToStatus(status.Location)

	// Overseer info
	if status.Overseer != nil {
		overseerDisplay := status.Overseer.Name
		if status.Overseer.Email != "" {
			overseerDisplay = fmt.Sprintf("%s <%s>", status.Overseer.Name, status.Overseer.Email)
		} else if status.Overseer.Username != "" && status.Overseer.Username != status.Overseer.Name {
			overseerDisplay = fmt.Sprintf("%s (@%s)", status.Overseer.Name, status.Overseer.Username)
		}
		fmt.Fprintf(w, "👤 %s %s\n", style.Bold.Render("Overseer:"), overseerDisplay)
		if status.Overseer.UnreadMail > 0 {
			fmt.Fprintf(w, "   📬 %d unread\n", status.Overseer.UnreadMail)
		}
		fmt.Fprintln(w)
	}

	// Current agent notification mode (DND)
	if status.DND != nil {
		icon := "🔔"
		state := "off"
		desc := "notifications normal"
		if status.DND.Enabled {
			icon = "🔕"
			state = "on"
			desc = "notifications muted"
		}
		fmt.Fprintf(w, "%s %s %s", icon, style.Bold.Render("DND:"), style.Bold.Render(state))
		if status.DND.Agent != "" {
			fmt.Fprintf(w, " %s", style.Dim.Render("("+status.DND.Agent+")"))
		}
		fmt.Fprintf(w, "\n   %s\n\n", style.Dim.Render(desc))
	}

	// Infrastructure services
	if status.Daemon != nil || status.Dolt != nil || status.Tmux != nil {
		fmt.Fprintf(w, "%s ", style.Bold.Render("Services:"))
		var parts []string
		if status.Daemon != nil {
			if status.Daemon.Running {
				parts = append(parts, fmt.Sprintf("daemon %s", style.Dim.Render(fmt.Sprintf("(PID %d)", status.Daemon.PID))))
			} else {
				parts = append(parts, fmt.Sprintf("daemon %s", style.Dim.Render("(stopped)")))
			}
		}
		if status.Dolt != nil {
			if status.Dolt.Remote {
				parts = append(parts, fmt.Sprintf("dolt %s", style.Dim.Render(fmt.Sprintf("(remote :%d)", status.Dolt.Port))))
			} else if status.Dolt.Running {
				dataDir := status.Dolt.DataDir
				if home, err := os.UserHomeDir(); err == nil {
					dataDir = strings.Replace(dataDir, home, "~", 1)
				}
				parts = append(parts, fmt.Sprintf("dolt %s", style.Dim.Render(fmt.Sprintf("(PID %d, :%d, %s)", status.Dolt.PID, status.Dolt.Port, dataDir))))
			} else if status.Dolt.PortConflict {
				parts = append(parts, fmt.Sprintf("dolt %s", style.Bold.Render(fmt.Sprintf("(stopped, :%d ⚠ port used by %s)", status.Dolt.Port, status.Dolt.ConflictOwner))))
			} else {
				parts = append(parts, fmt.Sprintf("dolt %s", style.Dim.Render(fmt.Sprintf("(stopped, :%d)", status.Dolt.Port))))
			}
		}
		if status.Tmux != nil {
			if status.Tmux.Running {
				parts = append(parts, fmt.Sprintf("tmux %s", style.Dim.Render(fmt.Sprintf("(-L %s, PID %d, %d sessions, %s)", status.Tmux.Socket, status.Tmux.PID, status.Tmux.SessionCount, status.Tmux.SocketPath))))
			} else {
				parts = append(parts, fmt.Sprintf("tmux %s", style.Dim.Render(fmt.Sprintf("(-L %s, no server)", status.Tmux.Socket))))
			}
		}
		if status.ACP != nil {
			if status.ACP.Running {
				parts = append(parts, fmt.Sprintf("acp %s", style.Dim.Render(fmt.Sprintf("(PID %d)", status.ACP.PID))))
			} else {
				parts = append(parts, fmt.Sprintf("acp %s", style.Dim.Render("(stopped)")))
			}
		}
		fmt.Fprintf(w, "%s\n", strings.Join(parts, "  "))
		fmt.Fprintln(w)
	}

	// Role icons - uses centralized emojis from constants package
	roleIcons := map[string]string{
		constants.RoleMayor:    constants.EmojiMayor,
		constants.RoleDeacon:   constants.EmojiDeacon,
		constants.RoleWitness:  constants.EmojiWitness,
		constants.RoleRefinery: constants.EmojiRefinery,
		constants.RoleCrew:     constants.EmojiCrew,
		constants.RolePolecat:  constants.EmojiPolecat,
		// Legacy names for backwards compatibility
		"coordinator":  constants.EmojiMayor,
		"health-check": constants.EmojiDeacon,
	}

	// Global Agents (Mayor, Deacon)
	for _, agent := range status.Agents {
		icon := roleIcons[agent.Role]
		if icon == "" {
			icon = roleIcons[agent.Name]
		}
		if statusVerbose {
			fmt.Fprintf(w, "%s %s\n", icon, style.Bold.Render(capitalizeFirst(agent.Name)))
			renderAgentDetails(w, agent, "   ", nil, status.Location)
			fmt.Fprintln(w)
		} else {
			// Compact: icon + name on one line
			renderAgentCompact(w, agent, icon+" ", nil, status.Location)
		}
	}
	if !statusVerbose && len(status.Agents) > 0 {
		fmt.Fprintln(w)
	}

	if len(status.Rigs) == 0 {
		fmt.Fprintf(w, "%s\n", style.Dim.Render("No rigs registered. Use 'gt rig add' to add one."))
		return nil
	}

	// Rigs
	for _, r := range status.Rigs {
		// Rig header with separator
		fmt.Fprintf(w, "─── %s ───────────────────────────────────────────\n\n", style.Bold.Render(r.Name+"/"))

		// Group agents by role
		var witnesses, refineries, crews, polecats []AgentRuntime
		for _, agent := range r.Agents {
			switch agent.Role {
			case constants.RoleWitness:
				witnesses = append(witnesses, agent)
			case constants.RoleRefinery:
				refineries = append(refineries, agent)
			case constants.RoleCrew:
				crews = append(crews, agent)
			case constants.RolePolecat:
				polecats = append(polecats, agent)
			}
		}

		// Witness
		if len(witnesses) > 0 {
			if statusVerbose {
				fmt.Fprintf(w, "%s %s\n", roleIcons[constants.RoleWitness], style.Bold.Render("Witness"))
				for _, agent := range witnesses {
					renderAgentDetails(w, agent, "   ", r.Hooks, status.Location)
				}
				fmt.Fprintln(w)
			} else {
				for _, agent := range witnesses {
					renderAgentCompact(w, agent, roleIcons[constants.RoleWitness]+" ", r.Hooks, status.Location)
				}
			}
		}

		// Refinery
		if len(refineries) > 0 {
			if statusVerbose {
				fmt.Fprintf(w, "%s %s\n", roleIcons[constants.RoleRefinery], style.Bold.Render("Refinery"))
				for _, agent := range refineries {
					renderAgentDetails(w, agent, "   ", r.Hooks, status.Location)
				}
				// MQ summary (shown under refinery)
				if r.MQ != nil {
					mqStr := formatMQSummary(r.MQ)
					if mqStr != "" {
						fmt.Fprintf(w, "   MQ: %s\n", mqStr)
					}
				}
				fmt.Fprintln(w)
			} else {
				for _, agent := range refineries {
					// Compact: include MQ on same line if present
					mqSuffix := ""
					if r.MQ != nil {
						mqStr := formatMQSummaryCompact(r.MQ)
						if mqStr != "" {
							mqSuffix = "  " + mqStr
						}
					}
					renderAgentCompactWithSuffix(w, agent, roleIcons[constants.RoleRefinery]+" ", r.Hooks, status.Location, mqSuffix)
				}
			}
		}

		// Crew
		if len(crews) > 0 {
			if statusVerbose {
				fmt.Fprintf(w, "%s %s (%d)\n", roleIcons[constants.RoleCrew], style.Bold.Render("Crew"), len(crews))
				for _, agent := range crews {
					renderAgentDetails(w, agent, "   ", r.Hooks, status.Location)
				}
				fmt.Fprintln(w)
			} else {
				fmt.Fprintf(w, "%s %s (%d)\n", roleIcons[constants.RoleCrew], style.Bold.Render("Crew"), len(crews))
				for _, agent := range crews {
					renderAgentCompact(w, agent, "   ", r.Hooks, status.Location)
				}
			}
		}

		// Polecats
		if len(polecats) > 0 {
			if statusVerbose {
				fmt.Fprintf(w, "%s %s (%d)\n", roleIcons[constants.RolePolecat], style.Bold.Render("Polecats"), len(polecats))
				for _, agent := range polecats {
					renderAgentDetails(w, agent, "   ", r.Hooks, status.Location)
				}
				fmt.Fprintln(w)
			} else {
				fmt.Fprintf(w, "%s %s (%d)\n", roleIcons[constants.RolePolecat], style.Bold.Render("Polecats"), len(polecats))
				for _, agent := range polecats {
					renderAgentCompact(w, agent, "   ", r.Hooks, status.Location)
				}
			}
		}

		// No agents
		if len(witnesses) == 0 && len(refineries) == 0 && len(crews) == 0 && len(polecats) == 0 {
			fmt.Fprintf(w, "   %s\n", style.Dim.Render("(no agents)"))
		}
		fmt.Fprintln(w)
	}

	return nil
}

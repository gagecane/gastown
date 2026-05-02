package feed

import (
	"fmt"
	"io"

	"github.com/steveyegge/gastown/internal/beads"
)

// PrintProblemsOptions controls filtering and output for PrintProblems.
type PrintProblemsOptions struct {
	// Rig, if non-empty, restricts output to agents in that rig.
	Rig string
	// All emits all agents (problems, working, idle, zombie).
	// Default (false) emits only agents that need attention.
	All bool
}

// PrintProblems runs the StuckDetector over agent beads and emits one
// structured text line per problem agent to w.
//
// This is the plain-mode counterpart to the TUI problems view. It's designed
// for pipelines and agents (e.g. Witness) that cannot run an interactive TUI
// but still want to surface stuck / GUPP-violation agents.
//
// Output format (columns separated by single spaces, one agent per line):
//
//	<state> <symbol> <duration> <bead-id> <rig> <role>/<name> -- <hint>
//
// Fields:
//   - state:    short state label (gupp, stalled, working, idle, zombie)
//   - symbol:   single unicode glyph matching the TUI (🔥, ⚠, ●, ○, 💀)
//   - duration: human-readable idle duration (<1m, 15m, 2h30m)
//   - bead-id:  current hooked bead ID (or "-" if none)
//   - rig:      rig name (or "-" if empty)
//   - role/name: role qualified with agent name
//   - hint:     human-readable action suggestion
//
// By default, only agents needing attention (gupp / stalled / zombie) are
// emitted. Pass All=true to emit every agent.
func PrintProblems(bd *beads.Beads, w io.Writer, opts PrintProblemsOptions) error {
	detector := NewStuckDetector(bd)
	return printProblemsFromDetector(detector, w, opts)
}

// printProblemsFromDetector is the core implementation, factored out so tests
// can inject a StuckDetector built from a mock HealthDataSource.
func printProblemsFromDetector(detector *StuckDetector, w io.Writer, opts PrintProblemsOptions) error {
	agents, err := detector.CheckAll()
	if err != nil {
		return fmt.Errorf("checking agent health: %w", err)
	}

	emitted := 0
	for _, agent := range agents {
		if opts.Rig != "" && agent.Rig != opts.Rig {
			continue
		}
		if !opts.All && !agent.State.NeedsAttention() {
			continue
		}
		if _, err := fmt.Fprintln(w, formatProblemLine(agent)); err != nil {
			return err
		}
		emitted++
	}

	if emitted == 0 {
		// Explicit "no problems" signal so callers can distinguish "healthy"
		// from "detector never ran".
		if _, err := fmt.Fprintln(w, "# no problem agents detected"); err != nil {
			return err
		}
	}

	return nil
}

// formatProblemLine renders a single ProblemAgent as a structured text line.
// Keep the format stable — external tools may grep/parse this output.
func formatProblemLine(agent *ProblemAgent) string {
	beadID := agent.CurrentBeadID
	if beadID == "" {
		beadID = "-"
	}
	rig := agent.Rig
	if rig == "" {
		rig = "-"
	}
	target := agent.Role
	if agent.Name != "" && agent.Name != agent.Role {
		target = agent.Role + "/" + agent.Name
	}
	hint := agent.ActionHint
	if hint == "" {
		hint = "-"
	}
	return fmt.Sprintf("%-7s %s %-6s %-12s %-15s %-25s -- %s",
		agent.State.String(),
		agent.State.Symbol(),
		agent.DurationDisplay(),
		beadID,
		rig,
		target,
		hint,
	)
}

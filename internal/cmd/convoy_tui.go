package cmd

import (
	"encoding/json"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/steveyegge/gastown/internal/tui/convoy"
)

// runConvoyTUI launches the interactive convoy TUI.
func runConvoyTUI() error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	m := convoy.New(townBeads)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// resolveConvoyNumber converts a numeric shortcut (1, 2, 3...) to a convoy ID.
// Numbers correspond to the order shown in 'gt convoy list'.
func resolveConvoyNumber(townBeads string, n int) (string, error) {
	// Get convoy list (same query as runConvoyList)
	out, err := runBdJSON(townBeads, "list", "--type=convoy", "--json")
	if err != nil {
		return "", fmt.Errorf("listing convoys: %w", err)
	}

	var convoys []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &convoys); err != nil {
		return "", fmt.Errorf("parsing convoy list: %w", err)
	}

	if n < 1 || n > len(convoys) {
		return "", fmt.Errorf("convoy %d not found (have %d convoys)", n, len(convoys))
	}

	return convoys[n-1].ID, nil
}

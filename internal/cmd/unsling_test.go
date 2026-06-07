package cmd

import (
	"strings"
	"testing"
)

// TestFormatNotActiveSlotMsg verifies the assignee-but-not-active-slot
// diagnostic (gu-u7num): the message must name the bead, the agent, the slot
// state, and the re-route command — not read like a syntax error.
func TestFormatNotActiveSlotMsg(t *testing.T) {
	const (
		bead  = "gu-wfs-y6wg6"
		agent = "gastown_upstream/crew/scheduler"
	)

	t.Run("active slot holds another bead", func(t *testing.T) {
		msg := formatNotActiveSlotMsg(bead, agent, "gu-c7eww")
		for _, want := range []string{bead, agent, "active hook slot holds gu-c7eww", "gt sling " + bead + " <new-target> --force --no-convoy"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message missing %q\ngot:\n%s", want, msg)
			}
		}
	})

	t.Run("active slot empty", func(t *testing.T) {
		msg := formatNotActiveSlotMsg(bead, agent, "")
		if !strings.Contains(msg, "active hook slot is empty") {
			t.Errorf("expected empty-slot phrasing, got:\n%s", msg)
		}
		if strings.Contains(msg, "holds") {
			t.Errorf("empty slot message should not mention a held bead, got:\n%s", msg)
		}
	})
}

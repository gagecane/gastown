package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/session"
)

// TestResolveTargetAgentIDNoPane verifies that resolveTargetAgentID resolves a
// target spec to its agent ID without requiring a live tmux pane. This is the
// detach path for a dead polecat holding a valid bead's hook (gu-wmqey): the
// older resolveTargetAgent path failed on getSessionPane when the session was
// dead, leaving the bead permanently stuck on the hook.
func TestResolveTargetAgentIDNoPane(t *testing.T) {
	originalRegistry := session.DefaultRegistry()
	t.Cleanup(func() { session.SetDefaultRegistry(originalRegistry) })

	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("cadk", "casc_cdk")
	session.SetDefaultRegistry(reg)

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"polecat path", "casc_cdk/polecats/furiosa", "casc_cdk/polecats/furiosa"},
		{"polecat shorthand", "gastown/nux", "gastown/polecats/nux"},
		{"witness", "gastown/witness", "gastown/witness"},
		{"refinery", "casc_cdk/refinery", "casc_cdk/refinery"},
		{"crew", "gastown/crew/max", "gastown/crew/max"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTargetAgentID(tt.target)
			if err != nil {
				t.Fatalf("resolveTargetAgentID(%q): %v", tt.target, err)
			}
			if got != tt.want {
				t.Fatalf("resolveTargetAgentID(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

package cmd

import "testing"

// TestResolveRigPrefix verifies the rig name to prefix conversion.
func TestResolveRigPrefix(t *testing.T) {
	tests := []struct {
		rigName string
		want    string
	}{
		{"gastown_upstream", "gu"},
		{"zack_dev", "zd"},
		{"myrig", "my"},
		{"a", "a"},
		{"ab", "ab"},
		{"one_two_three", "ot"},
	}

	for _, tt := range tests {
		got := resolveRigPrefix(tt.rigName)
		if got != tt.want {
			t.Errorf("resolveRigPrefix(%q) = %q, want %q", tt.rigName, got, tt.want)
		}
	}
}

// TestFormatCadence verifies cadence formatting.
func TestFormatCadence(t *testing.T) {
	tests := []struct {
		minutes int
		want    string
	}{
		{30, "30m"},
		{60, "1h"},
		{90, "1h30m"},
		{360, "6h"},
		{1440, "24h"},
	}

	for _, tt := range tests {
		got := formatCadence(tt.minutes)
		if got != tt.want {
			t.Errorf("formatCadence(%d) = %q, want %q", tt.minutes, got, tt.want)
		}
	}
}

// TestStateIcon verifies state icon mapping.
func TestStateIcon(t *testing.T) {
	tests := []struct {
		state string
		want  string
	}{
		{"idle", "✓"},
		{"synced", "✓"},
		{"checking", "⟳"},
		{"syncing", "⟳"},
		{"gating", "⟳"},
		{"pushing", "⟳"},
		{"resolving", "⚡"},
		{"failed", "✗"},
		{"paused", "⏸"},
		{"unknown", "?"},
	}

	for _, tt := range tests {
		got := stateIcon(tt.state)
		if got != tt.want {
			t.Errorf("stateIcon(%q) = %q, want %q", tt.state, got, tt.want)
		}
	}
}

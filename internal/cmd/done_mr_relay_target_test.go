package cmd

import "testing"

// TestMRRelayTargetOverride pins the gs-dus fix: a relay leg run as plain
// `gt done` (no --target) on a non-FF path must target its convoy/stamped relay
// base, not the rig default. The MR-target ladder previously consulted only
// formula_vars and fell through to the rig default (gagecane/gt), producing a
// modify/delete conflict and a reopen/resling churn loop.
//
// The injected resolver stands in for effectiveBaseBranch so the decision is
// exercised without bd/Dolt I/O.
func TestMRRelayTargetOverride(t *testing.T) {
	const def = "gagecane/gt"
	const relay = "proto/v3-build"

	// resolver returns relay for the relay leg, "" otherwise.
	relayResolver := func(beadID, explicit string) string {
		if beadID == "lb-wcdw.12" {
			return relay
		}
		return ""
	}

	tests := []struct {
		name           string
		explicitTarget bool
		currentTarget  string
		issueID        string
		resolve        func(string, string) string
		want           string
	}{
		{
			name:          "relay leg, no explicit target, still default -> override to relay base",
			currentTarget: def, issueID: "lb-wcdw.12", resolve: relayResolver,
			want: relay,
		},
		{
			name:           "explicit --target wins -> no override",
			explicitTarget: true, currentTarget: relay, issueID: "lb-wcdw.12", resolve: relayResolver,
			want: "",
		},
		{
			name:          "formula_vars already moved target off default -> no override",
			currentTarget: relay, issueID: "lb-wcdw.12", resolve: relayResolver,
			want: "",
		},
		{
			name:          "non-relay bead -> resolver returns empty -> no override",
			currentTarget: def, issueID: "lb-plain-1", resolve: relayResolver,
			want: "",
		},
		{
			name:          "resolver returns the default branch -> no override",
			currentTarget: def, issueID: "lb-wcdw.12",
			resolve: func(string, string) string { return def },
			want:    "",
		},
		{
			name:          "empty issueID -> no override (resolver not consulted)",
			currentTarget: def, issueID: "",
			resolve: func(string, string) string { t.Fatal("resolver must not be called for empty issueID"); return "" },
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mrRelayTargetOverride(tt.explicitTarget, tt.currentTarget, def, tt.issueID, tt.resolve)
			if got != tt.want {
				t.Errorf("mrRelayTargetOverride() = %q, want %q", got, tt.want)
			}
		})
	}
}

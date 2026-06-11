package cmd

import (
	"strings"
	"testing"
)

// TestGuardDoltLifecycleRole verifies that refinery agents are blocked from
// Dolt lifecycle commands while other roles and human/operator contexts pass
// through. See gu-k00y0 / hq:gc-vkwkfr — refineries must escalate, never
// restart the shared Dolt data plane.
func TestGuardDoltLifecycleRole(t *testing.T) {
	tests := []struct {
		name      string
		gtRole    string // value to set for GT_ROLE ("" = unset)
		wantBlock bool
	}{
		{"refinery compound role blocked", "gastown_upstream/refinery", true},
		{"refinery double-slash blocked", "gamestore//refinery", true},
		{"no role (human/operator) allowed", "", false},
		{"polecat allowed", "gastown_upstream/polecats/dust", false},
		{"witness allowed", "gastown_upstream/witness", false},
		{"mayor allowed", "mayor", false},
		{"deacon allowed", "deacon", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.gtRole == "" {
				t.Setenv(EnvGTRole, "")
			} else {
				t.Setenv(EnvGTRole, tt.gtRole)
			}

			err := guardDoltLifecycleRole("restart")
			if tt.wantBlock && err == nil {
				t.Fatalf("GT_ROLE=%q: expected guard to block, got nil error", tt.gtRole)
			}
			if !tt.wantBlock && err != nil {
				t.Fatalf("GT_ROLE=%q: expected guard to allow, got error: %v", tt.gtRole, err)
			}
		})
	}
}

// TestGuardDoltLifecycleRole_ActionAndGuidance verifies the refusal message
// names the attempted action and points the refinery at the escalation path.
func TestGuardDoltLifecycleRole_ActionAndGuidance(t *testing.T) {
	t.Setenv(EnvGTRole, "gastown_upstream/refinery")

	for _, action := range []string{"start", "stop", "restart"} {
		err := guardDoltLifecycleRole(action)
		if err == nil {
			t.Fatalf("action %q: expected error, got nil", action)
		}
		msg := err.Error()
		if !strings.Contains(msg, action) {
			t.Errorf("action %q: error message does not mention the action: %s", action, msg)
		}
		if !strings.Contains(msg, "gt escalate") {
			t.Errorf("action %q: error message does not point at escalation: %s", action, msg)
		}
	}
}

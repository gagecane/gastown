package tmux

import "testing"

// TestShouldSkipEscapeForAgent covers the auto-detection of agents whose CLI
// treats Escape as "cancel in-flight generation" rather than a vim mode exit.
// NudgeSessionWithOpts uses this decision to skip step 5 (Escape) and step 6
// (the 600ms readline wait) of the delivery protocol.
//
// Per gu-flq9: any GT_AGENT containing "kiro" (case-insensitive) must map to
// true. Per hq-isz: "copilot" continues to map to true. Unknown/empty values
// must map to false so the Escape step is preserved for agents that need it
// (e.g., Claude in vim-INSERT mode).
func TestShouldSkipEscapeForAgent(t *testing.T) {
	tests := []struct {
		name     string
		agent    string
		expected bool
	}{
		// Preserved: Copilot detection (hq-isz).
		{"copilot lowercase", "copilot", true},
		{"copilot uppercase", "COPILOT", true},
		{"copilot mixed case", "CoPiLoT", true},
		{"copilot with whitespace", "  copilot  ", true},

		// New: Kiro detection (gu-flq9).
		{"kiro-cli canonical", "kiro-cli", true},
		{"kiro bare", "kiro", true},
		{"kiro uppercase", "KIRO-CLI", true},
		{"kiro mixed case", "Kiro-CLI", true},
		{"kiro with whitespace", "  kiro-cli  ", true},
		{"kiro suffix variant", "kiro-next", true},
		{"kiro embedded", "my-kiro-build", true},

		// Preserved: Escape is required for these agents.
		{"claude", "claude", false},
		{"claude-code", "claude-code", false},
		{"gemini", "gemini", false},
		{"cursor", "cursor", false},
		{"empty string", "", false},
		{"whitespace only", "   ", false},
		{"unknown agent", "some-new-agent", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := shouldSkipEscapeForAgent(tc.agent)
			if got != tc.expected {
				t.Errorf("shouldSkipEscapeForAgent(%q) = %v; want %v", tc.agent, got, tc.expected)
			}
		})
	}
}

// TestAgentSupportsRewindMode covers the gating decision for the Rewind-mode
// pane-capture probes in the nudge protocol (steps 0 and 6.5). Only Claude
// Code implements the double-Escape Rewind UI, so every other agent should
// skip the ~15ms pane-content check that can never match.
//
// Per gu-yx80: any GT_AGENT containing "claude" (case-insensitive) maps to
// true (check Rewind). All other values — including agents that skip Escape
// entirely (kiro-cli, copilot), the empty string, and unknown agents — map
// to false so the wasted checks are elided.
func TestAgentSupportsRewindMode(t *testing.T) {
	tests := []struct {
		name     string
		agent    string
		expected bool
	}{
		// Claude variants: Rewind mode check applies.
		{"claude bare", "claude", true},
		{"claude-code canonical", "claude-code", true},
		{"claude uppercase", "CLAUDE", true},
		{"claude-code uppercase", "CLAUDE-CODE", true},
		{"claude mixed case", "Claude-Code", true},
		{"claude with whitespace", "  claude-code  ", true},
		{"claude suffix variant", "claude-next", true},
		{"claude embedded", "my-claude-build", true},

		// Non-Claude agents: Rewind check is pure overhead.
		{"kiro-cli", "kiro-cli", false},
		{"kiro bare", "kiro", false},
		{"copilot", "copilot", false},
		{"gemini", "gemini", false},
		{"cursor", "cursor", false},
		{"empty string", "", false},
		{"whitespace only", "   ", false},
		{"unknown agent", "some-new-agent", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := agentSupportsRewindMode(tc.agent)
			if got != tc.expected {
				t.Errorf("agentSupportsRewindMode(%q) = %v; want %v", tc.agent, got, tc.expected)
			}
		})
	}
}

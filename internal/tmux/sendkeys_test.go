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

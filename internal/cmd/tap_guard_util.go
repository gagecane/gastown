package cmd

import "encoding/json"

// extractCommandFromHookInput extracts the bash command from Claude Code / kiro-cli
// PreToolUse hook input JSON. The payload format is:
//
//	{"tool_name":"Bash","tool_input":{"command":"..."}}
//
// Returns empty string if input is empty, malformed, or has no command field.
func extractCommandFromHookInput(input []byte) string {
	if len(input) == 0 {
		return ""
	}
	var hookInput struct {
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(input, &hookInput); err != nil {
		return ""
	}
	return hookInput.ToolInput.Command
}

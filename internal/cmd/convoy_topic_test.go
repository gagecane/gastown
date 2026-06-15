package cmd

import "testing"

// TestFormulaSeedTopic verifies that the run's seed problem statement is
// extracted from --set vars in priority order, so concurrent runs of the same
// formula get distinguishable convoy titles (gs-h1q4).
func TestFormulaSeedTopic(t *testing.T) {
	tests := []struct {
		name    string
		setVars map[string]interface{}
		want    string
	}{
		{"none", map[string]interface{}{}, ""},
		{"problem", map[string]interface{}{"problem": "notification levels"}, "notification levels"},
		{"issue fallback", map[string]interface{}{"issue": "gs-h1q4"}, "gs-h1q4"},
		{"problem beats issue", map[string]interface{}{"issue": "gs-h1q4", "problem": "the idea"}, "the idea"},
		{"trims whitespace", map[string]interface{}{"problem": "  spaced  "}, "spaced"},
		{"empty string skipped", map[string]interface{}{"problem": "   ", "idea": "real"}, "real"},
		{"non-string ignored", map[string]interface{}{"problem": 42}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formulaSeedTopic(tt.setVars); got != tt.want {
				t.Errorf("formulaSeedTopic() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestConvoyTopicFromFields verifies that the seed topic echoed onto a convoy
// description is recoverable for display in `gt convoy status` (gs-h1q4).
func TestConvoyTopicFromFields(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        string
	}{
		{"none", "Formula convoy: design\n\nLegs: 6\nRig: gastown", ""},
		{"leading topic", "Topic: notification levels\n\nFormula convoy: design", "notification levels"},
		{"topic among fields", "Workflow: x\nTopic: the idea\nSteps: 3", "the idea"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convoyTopicFromFields(tt.description); got != tt.want {
				t.Errorf("convoyTopicFromFields() = %q, want %q", got, tt.want)
			}
		})
	}
}

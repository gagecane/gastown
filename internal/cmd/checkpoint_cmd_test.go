package cmd

import "testing"

// TestResolveCheckpointMolecule is a regression test for gu-d6q99: the
// auto-detected step title was discarded because a second WithMolecule call
// unconditionally passed an empty title. The resolver must preserve the
// detected title while honoring explicit molecule/step overrides.
func TestResolveCheckpointMolecule(t *testing.T) {
	tests := []struct {
		name                         string
		overrideMol, overrideStep    string
		detMol, detStep, detTitle    string
		wantMol, wantStep, wantTitle string
	}{
		{
			name:      "detected title is preserved (regression)",
			detMol:    "mol-abc",
			detStep:   "step-1",
			detTitle:  "Build the thing",
			wantMol:   "mol-abc",
			wantStep:  "step-1",
			wantTitle: "Build the thing",
		},
		{
			name:        "explicit molecule override keeps detected step title",
			overrideMol: "mol-override",
			detMol:      "mol-abc",
			detStep:     "step-1",
			detTitle:    "Build the thing",
			wantMol:     "mol-override",
			wantStep:    "step-1",
			wantTitle:   "Build the thing",
		},
		{
			name:         "explicit step override keeps detected molecule and title",
			overrideStep: "step-override",
			detMol:       "mol-abc",
			detStep:      "step-1",
			detTitle:     "Build the thing",
			wantMol:      "mol-abc",
			wantStep:     "step-override",
			wantTitle:    "Build the thing",
		},
		{
			name:         "both overrides, no detection",
			overrideMol:  "mol-x",
			overrideStep: "step-x",
			wantMol:      "mol-x",
			wantStep:     "step-x",
			wantTitle:    "",
		},
		{
			name: "nothing detected or overridden",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mol, step, title := resolveCheckpointMolecule(
				tt.overrideMol, tt.overrideStep, tt.detMol, tt.detStep, tt.detTitle)
			if mol != tt.wantMol {
				t.Errorf("mol = %q, want %q", mol, tt.wantMol)
			}
			if step != tt.wantStep {
				t.Errorf("step = %q, want %q", step, tt.wantStep)
			}
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
		})
	}
}

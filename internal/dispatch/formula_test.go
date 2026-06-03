package dispatch

import "testing"

func TestWorkflowStepTargetFromDescription(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        string
	}{
		{name: "no metadata", description: "Body only", want: ""},
		{name: "mayor", description: "workflow_target: mayor\n\nBody", want: "mayor"},
		{name: "rig alias", description: "workflow_target: rig\n\nBody", want: "gastown"},
		{name: "empty value falls back to targetRig", description: "workflow_target:\n\nBody", want: "gastown"},
		{name: "path target", description: "workflow_target: gastown/crew/alex\n\nBody", want: "gastown/crew/alex"},
		{name: "case-insensitive key", description: "Workflow_Target: mayor", want: "mayor"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WorkflowStepTargetFromDescription(tt.description, "gastown"); got != tt.want {
				t.Fatalf("WorkflowStepTargetFromDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureFormulaRequiredVars(t *testing.T) {
	requiredKeys := []string{
		"base_branch", "setup_command", "typecheck_command",
		"lint_command", "test_command", "build_command", "gates_commands",
	}

	t.Run("non-polecat formula is unchanged", func(t *testing.T) {
		in := []string{"issue=gt-abc"}
		got := EnsureFormulaRequiredVars("some-other-formula", in)
		if len(got) != 1 || got[0] != "issue=gt-abc" {
			t.Fatalf("EnsureFormulaRequiredVars(non-polecat) = %v, want unchanged", got)
		}
	})

	t.Run("mol-polecat-work gets all required defaults appended", func(t *testing.T) {
		got := EnsureFormulaRequiredVars("mol-polecat-work", []string{"issue=gt-abc"})
		seen := varKeys(got)
		for _, k := range requiredKeys {
			if !seen[k] {
				t.Errorf("EnsureFormulaRequiredVars(mol-polecat-work) missing %s= default; got %v", k, got)
			}
		}
		if !seen["issue"] {
			t.Errorf("EnsureFormulaRequiredVars dropped existing issue var; got %v", got)
		}
	})

	t.Run("polecat-work alias also gets defaults", func(t *testing.T) {
		got := EnsureFormulaRequiredVars("polecat-work", nil)
		seen := varKeys(got)
		for _, k := range requiredKeys {
			if !seen[k] {
				t.Errorf("EnsureFormulaRequiredVars(polecat-work) missing %s=; got %v", k, got)
			}
		}
	})

	t.Run("existing var is not overwritten with default", func(t *testing.T) {
		got := EnsureFormulaRequiredVars("mol-polecat-work", []string{"base_branch=release"})
		count := 0
		for _, v := range got {
			if v == "base_branch=release" {
				count++
			}
			if v == "base_branch=main" {
				t.Errorf("EnsureFormulaRequiredVars overwrote base_branch with default; got %v", got)
			}
		}
		if count != 1 {
			t.Errorf("EnsureFormulaRequiredVars duplicated base_branch; got %v", got)
		}
	})
}

func varKeys(vars []string) map[string]bool {
	seen := make(map[string]bool, len(vars))
	for _, v := range vars {
		if eq := indexByte(v, '='); eq > 0 {
			seen[v[:eq]] = true
		}
	}
	return seen
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

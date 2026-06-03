package dispatch

import "strings"

// workflowTargetField is the description metadata key that names the target
// rig/agent for a workflow step. Duplicated as a package-local constant so this
// package does not depend on internal/cmd; cmd keeps its own copy (formula.go).
const workflowTargetField = "workflow_target"

// WorkflowStepTargetFromDescription extracts the workflow step target from a
// bead description's "workflow_target:" metadata line. Returns targetRig when
// the value is empty or the literal "rig" alias, the explicit target otherwise,
// and "" when no metadata line is present.
func WorkflowStepTargetFromDescription(description, targetRig string) string {
	for _, line := range strings.Split(description, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(key), workflowTargetField) {
			continue
		}
		target := strings.TrimSpace(value)
		if target == "" || target == "rig" {
			return targetRig
		}
		return target
	}
	return ""
}

// EnsureFormulaRequiredVars appends missing required vars for formulas that
// enforce strict var presence on direct bond paths. Currently only
// mol-polecat-work (and its polecat-work alias) has strict required vars; for
// any other formula the input vars are returned unchanged.
func EnsureFormulaRequiredVars(formulaName string, vars []string) []string {
	// Currently only mol-polecat-work has strict required vars on bond.
	if formulaName != "mol-polecat-work" && formulaName != "polecat-work" {
		return vars
	}

	seen := make(map[string]bool, len(vars))
	for _, variable := range vars {
		if eq := strings.Index(variable, "="); eq > 0 {
			seen[variable[:eq]] = true
		}
	}

	requiredDefaults := []struct {
		Key   string
		Value string
	}{
		{"base_branch", "main"},
		{"setup_command", ""},
		{"typecheck_command", ""},
		{"lint_command", ""},
		{"test_command", ""},
		{"build_command", ""},
		{"gates_commands", ""},
	}
	for _, item := range requiredDefaults {
		if !seen[item.Key] {
			vars = append(vars, item.Key+"="+item.Value)
		}
	}
	return vars
}

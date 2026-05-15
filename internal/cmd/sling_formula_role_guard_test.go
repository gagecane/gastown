package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// TestValidatePatrolFormulaTarget exercises the role-pinning guard added for
// gs-i6u: patrol formulas may only be slung to their owning role. A deacon
// patrol slung at a polecat or rig was the failure that motivated this guard.
func TestValidatePatrolFormulaTarget(t *testing.T) {
	cases := []struct {
		name        string
		formula     string
		target      string
		wantErr     bool
		errContains string
	}{
		// Patrol formulas slung at their owning role: allow.
		{"deacon patrol to deacon", constants.MolDeaconPatrol, "deacon", false, ""},
		{"deacon patrol to deacon/ (trailing slash)", constants.MolDeaconPatrol, "deacon/", false, ""},
		{"witness patrol to gastown/witness", constants.MolWitnessPatrol, "gastown/witness", false, ""},
		{"refinery patrol to gastown/refinery", constants.MolRefineryPatrol, "gastown/refinery", false, ""},

		// Patrol formulas mis-targeted: reject. This is the gs-i6u scenario.
		{"deacon patrol to polecat", constants.MolDeaconPatrol, "gastown/polecats/furiosa", true, "mol-deacon-patrol"},
		{"deacon patrol to witness", constants.MolDeaconPatrol, "gastown/witness", true, "require deacon"},
		{"deacon patrol to refinery", constants.MolDeaconPatrol, "gastown/refinery", true, "require deacon"},
		{"witness patrol to polecat", constants.MolWitnessPatrol, "gastown/polecats/nux", true, "mol-witness-patrol"},
		{"witness patrol to deacon", constants.MolWitnessPatrol, "deacon", true, "require witness"},
		{"refinery patrol to polecat", constants.MolRefineryPatrol, "gastown/polecats/nux", true, "mol-refinery-patrol"},
		{"refinery patrol to deacon", constants.MolRefineryPatrol, "deacon", true, "require refinery"},

		// Non-patrol formulas: unconstrained.
		{"polecat work to polecat", "mol-polecat-work", "gastown/polecats/nux", false, ""},
		{"polecat work to deacon", "mol-polecat-work", "deacon", false, ""},
		{"user formula to polecat", "mol-evolve", "gastown/polecats/nux", false, ""},
		{"dog formula to dog", "mol-dog-reaper", "deacon/dogs/alpha", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePatrolFormulaTarget(tc.formula, tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validatePatrolFormulaTarget(%q, %q) = nil, want error", tc.formula, tc.target)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("error = %q, want substring %q", err, tc.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("validatePatrolFormulaTarget(%q, %q) = %v, want nil", tc.formula, tc.target, err)
			}
		})
	}
}

// TestPatrolFormulaRequiredRole confirms the mapping is exhaustive for the
// patrol formulas listed in constants.PatrolFormulas() — adding a new patrol
// formula without updating the guard would let it through unchecked.
func TestPatrolFormulaRequiredRole(t *testing.T) {
	for _, formula := range constants.PatrolFormulas() {
		role := patrolFormulaRequiredRole(formula)
		if role == RoleUnknown {
			t.Errorf("patrolFormulaRequiredRole(%q) = RoleUnknown; new patrol formula needs a role mapping in sling_formula.go", formula)
		}
	}
}

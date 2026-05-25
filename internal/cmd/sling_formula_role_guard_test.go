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

// TestValidateFormulaRoleTarget exercises the generalized role guard (gu-0h3f)
// that checks both patrol-formula name mapping AND the formula's required_role
// TOML field. This prevents formulas requiring specific capabilities (like
// gt sling) from being dispatched to roles that lack them.
func TestValidateFormulaRoleTarget(t *testing.T) {
	// Save original and restore after test.
	origFn := loadFormulaRequiredRoleFn
	defer func() { loadFormulaRequiredRoleFn = origFn }()

	cases := []struct {
		name         string
		formula      string
		target       string
		stubRole     Role // What loadFormulaRequiredRoleFn returns
		wantErr      bool
		errContains  string
	}{
		// Formula with required_role=dog slung to a polecat: reject.
		{
			name:        "dog formula to polecat",
			formula:     "mol-session-gc",
			target:      "gastown/polecats/chrome",
			stubRole:    RoleDog,
			wantErr:     true,
			errContains: "required_role=dog",
		},
		// Formula with required_role=dog slung to a dog: allow.
		{
			name:     "dog formula to dog",
			formula:  "mol-session-gc",
			target:   "deacon/dogs/alpha",
			stubRole: RoleDog,
			wantErr:  false,
		},
		// Formula with required_role=deacon slung to a polecat: reject.
		{
			name:        "deacon formula to polecat",
			formula:     "mol-custom-deacon",
			target:      "gastown/polecats/nux",
			stubRole:    RoleDeacon,
			wantErr:     true,
			errContains: "required_role=deacon",
		},
		// Formula with required_role=deacon slung to deacon: allow.
		{
			name:     "deacon formula to deacon",
			formula:  "mol-custom-deacon",
			target:   "deacon",
			stubRole: RoleDeacon,
			wantErr:  false,
		},
		// Formula with no required_role (RoleUnknown): allow anywhere.
		{
			name:     "unconstrained formula to polecat",
			formula:  "mol-polecat-work",
			target:   "gastown/polecats/nux",
			stubRole: RoleUnknown,
			wantErr:  false,
		},
		// Formula with no required_role slung to deacon: allow.
		{
			name:     "unconstrained formula to deacon",
			formula:  "mol-polecat-work",
			target:   "deacon",
			stubRole: RoleUnknown,
			wantErr:  false,
		},
		// Patrol formulas still work through the generalized function.
		{
			name:        "patrol guard still active via generalized func",
			formula:     constants.MolDeaconPatrol,
			target:      "gastown/polecats/furiosa",
			stubRole:    RoleUnknown, // patrol guard fires before required_role check
			wantErr:     true,
			errContains: "mol-deacon-patrol",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loadFormulaRequiredRoleFn = func(_ string) Role { return tc.stubRole }
			err := validateFormulaRoleTarget(tc.formula, tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateFormulaRoleTarget(%q, %q) = nil, want error", tc.formula, tc.target)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("error = %q, want substring %q", err, tc.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateFormulaRoleTarget(%q, %q) = %v, want nil", tc.formula, tc.target, err)
			}
		})
	}
}

// TestLoadFormulaRequiredRole verifies that embedded formulas with required_role
// are correctly parsed. Tests the real function (not a stub).
func TestLoadFormulaRequiredRole(t *testing.T) {
	cases := []struct {
		formula  string
		wantRole Role
	}{
		// mol-session-gc declares required_role = "dog"
		{"mol-session-gc", RoleDog},
		// mol-polecat-work has no required_role
		{"mol-polecat-work", RoleUnknown},
		// Nonexistent formula returns unknown
		{"mol-nonexistent-formula-xyz", RoleUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.formula, func(t *testing.T) {
			got := loadFormulaRequiredRole(tc.formula)
			if got != tc.wantRole {
				t.Errorf("loadFormulaRequiredRole(%q) = %q, want %q", tc.formula, got, tc.wantRole)
			}
		})
	}
}

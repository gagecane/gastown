package cmd

import (
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// TestInferRoleFromPath verifies role inference for managed settings dirs,
// including deacon/dogs/<name> which must resolve to the fleet "dog" role
// (see 2026-06-05 OOM post-mortem — dogs must get AIM plugins disabled).
func TestInferRoleFromPath(t *testing.T) {
	root := filepath.Join("home", "user", "gt")
	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"witness", filepath.Join(root, "rig1", "witness"), constants.RoleWitness},
		{"refinery", filepath.Join(root, "rig1", "refinery"), constants.RoleRefinery},
		{"crew", filepath.Join(root, "rig1", "crew"), constants.RoleCrew},
		{"polecats", filepath.Join(root, "rig1", "polecats"), constants.RolePolecat},
		{"mayor", filepath.Join(root, "mayor"), constants.RoleMayor},
		{"deacon", filepath.Join(root, "deacon"), constants.RoleDeacon},
		{"boot", filepath.Join(root, "deacon", "dogs", "boot"), constants.RoleBoot},
		{"dog-charlie", filepath.Join(root, "deacon", "dogs", "charlie"), constants.RoleDog},
		{"dog-delta", filepath.Join(root, "deacon", "dogs", "delta"), constants.RoleDog},
		{"unknown", filepath.Join(root, "rig1", "something"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferRoleFromPath(tc.dir); got != tc.want {
				t.Errorf("inferRoleFromPath(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
}

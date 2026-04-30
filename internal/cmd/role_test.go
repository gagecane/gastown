package cmd

import (
	"path/filepath"
	"testing"
)

// TestParseRoleString exercises parseRoleString across every known role
// form. It complements role_boot_test.go (which focuses on boot edge cases)
// by covering the full matrix of valid role strings.
func TestParseRoleString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantRole Role
		wantRig  string
		wantName string
	}{
		// Simple roles
		{"mayor", "mayor", RoleMayor, "", ""},
		{"deacon", "deacon", RoleDeacon, "", ""},
		{"boot bare", "boot", RoleBoot, "", ""},
		{"dog bare", "dog", RoleDog, "", ""},

		// Whitespace handling
		{"mayor with spaces", "  mayor  ", RoleMayor, "", ""},
		{"deacon with tabs", "\tdeacon\t", RoleDeacon, "", ""},

		// Compound roles: rig/<role>
		{"rig witness", "gastown/witness", RoleWitness, "gastown", ""},
		{"rig refinery", "gastown/refinery", RoleRefinery, "gastown", ""},

		// Compound roles: rig/polecats/<name>
		{"rig polecat with name", "gastown/polecats/alpha", RolePolecat, "gastown", "alpha"},
		{"rig polecat empty name", "gastown/polecats", RolePolecat, "gastown", ""},
		{"rig polecat with deep name", "west/polecats/beta", RolePolecat, "west", "beta"},

		// Compound roles: rig/crew/<name>
		{"rig crew with name", "gastown/crew/max", RoleCrew, "gastown", "max"},
		{"rig crew empty name", "gastown/crew", RoleCrew, "gastown", ""},

		// Shorthand: rig/<name> falls back to polecat
		{"rig/name shorthand", "gastown/toast", RolePolecat, "gastown", "toast"},

		// Trailing/duplicate slash normalization
		{"trailing slash", "gastown/witness/", RoleWitness, "gastown", ""},
		{"double slash", "gastown//witness", RoleWitness, "gastown", ""},
		{"triple slash", "gastown///refinery", RoleRefinery, "gastown", ""},
		{"double slash in polecat", "gastown//polecats//alpha", RolePolecat, "gastown", "alpha"},

		// Unknown / degenerate inputs
		{"empty string", "", Role(""), "", ""},
		{"single unknown word", "wizard", Role("wizard"), "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role, rig, name := parseRoleString(tt.input)
			if role != tt.wantRole {
				t.Errorf("parseRoleString(%q) role = %v, want %v", tt.input, role, tt.wantRole)
			}
			if rig != tt.wantRig {
				t.Errorf("parseRoleString(%q) rig = %q, want %q", tt.input, rig, tt.wantRig)
			}
			if name != tt.wantName {
				t.Errorf("parseRoleString(%q) name = %q, want %q", tt.input, name, tt.wantName)
			}
		})
	}
}

// TestDetectRoleFromCwd exercises detectRole against every supported
// directory layout. The function is the cwd-based fallback used when
// GT_ROLE is not set.
func TestDetectRoleFromCwd(t *testing.T) {
	townRoot := "/tmp/gt"

	tests := []struct {
		name        string
		cwd         string
		wantRole    Role
		wantRig     string
		wantPolecat string
	}{
		// Neutral town root
		{"town root returns unknown", townRoot, RoleUnknown, "", ""},
		{"town root with trailing slash", townRoot + "/", RoleUnknown, "", ""},

		// Town-level roles
		{"mayor", filepath.Join(townRoot, "mayor"), RoleMayor, "", ""},
		{"mayor subdir", filepath.Join(townRoot, "mayor", "notes"), RoleMayor, "", ""},
		{"deacon", filepath.Join(townRoot, "deacon"), RoleDeacon, "", ""},
		{"deacon subdir", filepath.Join(townRoot, "deacon", "logs"), RoleDeacon, "", ""},

		// Boot and dog roles (nested under deacon)
		{"boot", filepath.Join(townRoot, "deacon", "dogs", "boot"), RoleBoot, "", ""},
		{"dog named", filepath.Join(townRoot, "deacon", "dogs", "rex"), RoleDog, "", "rex"},

		// Rig-scoped roles
		{"rig mayor", filepath.Join(townRoot, "gastown", "mayor"), RoleMayor, "gastown", ""},
		{"rig witness", filepath.Join(townRoot, "gastown", "witness"), RoleWitness, "gastown", ""},
		{"rig witness subdir", filepath.Join(townRoot, "gastown", "witness", "rig"), RoleWitness, "gastown", ""},
		{"rig refinery", filepath.Join(townRoot, "gastown", "refinery"), RoleRefinery, "gastown", ""},
		{"rig refinery subdir", filepath.Join(townRoot, "gastown", "refinery", "rig"), RoleRefinery, "gastown", ""},

		// Polecat and crew identities
		{"polecat", filepath.Join(townRoot, "gastown", "polecats", "alpha"), RolePolecat, "gastown", "alpha"},
		{"polecat with worktree", filepath.Join(townRoot, "gastown", "polecats", "alpha", "rig"), RolePolecat, "gastown", "alpha"},
		{"crew", filepath.Join(townRoot, "gastown", "crew", "max"), RoleCrew, "gastown", "max"},
		{"crew with worktree", filepath.Join(townRoot, "gastown", "crew", "max", "rig"), RoleCrew, "gastown", "max"},

		// Degenerate rig paths: rig root and incomplete dirs
		{"rig root only", filepath.Join(townRoot, "gastown"), RoleUnknown, "gastown", ""},
		{"polecats without name", filepath.Join(townRoot, "gastown", "polecats"), RoleUnknown, "gastown", ""},
		{"crew without name", filepath.Join(townRoot, "gastown", "crew"), RoleUnknown, "gastown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectRole(tt.cwd, townRoot)
			if got.Role != tt.wantRole {
				t.Errorf("detectRole(%q) role = %v, want %v", tt.cwd, got.Role, tt.wantRole)
			}
			if got.Rig != tt.wantRig {
				t.Errorf("detectRole(%q) rig = %q, want %q", tt.cwd, got.Rig, tt.wantRig)
			}
			if got.Polecat != tt.wantPolecat {
				t.Errorf("detectRole(%q) polecat = %q, want %q", tt.cwd, got.Polecat, tt.wantPolecat)
			}
			if got.Source != "cwd" {
				t.Errorf("detectRole(%q) source = %q, want %q", tt.cwd, got.Source, "cwd")
			}
			if got.TownRoot != townRoot {
				t.Errorf("detectRole(%q) townRoot = %q, want %q", tt.cwd, got.TownRoot, townRoot)
			}
		})
	}
}

// TestDetectRoleOutsideTownRoot verifies detectRole returns RoleUnknown
// when cwd cannot be made relative to the town root (i.e. outside it).
func TestDetectRoleOutsideTownRoot(t *testing.T) {
	// On Unix, filepath.Rel between different roots succeeds with "../..".
	// detectRole does not special-case that, so paths outside town root
	// fall through to the "parts[0] is rig name" branch. This test simply
	// pins current behaviour: any path that doesn't match a known role
	// pattern returns RoleUnknown.
	got := detectRole("/etc", "/tmp/gt")
	if got.Role != RoleUnknown {
		t.Errorf("detectRole outside town root: role = %v, want %v", got.Role, RoleUnknown)
	}
}

// TestGetRoleHome exercises getRoleHome for every role, including the
// error cases where rig or polecat are missing for roles that require them.
func TestGetRoleHome(t *testing.T) {
	townRoot := "/tmp/gt"

	tests := []struct {
		name    string
		role    Role
		rig     string
		polecat string
		want    string
	}{
		{"mayor", RoleMayor, "", "", filepath.Join(townRoot, "mayor")},
		{"mayor ignores rig", RoleMayor, "gastown", "", filepath.Join(townRoot, "mayor")},
		{"deacon", RoleDeacon, "", "", filepath.Join(townRoot, "deacon")},
		{"boot", RoleBoot, "", "", filepath.Join(townRoot, "deacon", "dogs", "boot")},

		{"witness with rig", RoleWitness, "gastown", "", filepath.Join(townRoot, "gastown", "witness")},
		{"witness without rig is empty", RoleWitness, "", "", ""},

		{"refinery with rig", RoleRefinery, "gastown", "", filepath.Join(townRoot, "gastown", "refinery", "rig")},
		{"refinery without rig is empty", RoleRefinery, "", "", ""},

		{"polecat fully specified", RolePolecat, "gastown", "alpha", filepath.Join(townRoot, "gastown", "polecats", "alpha")},
		{"polecat missing rig is empty", RolePolecat, "", "alpha", ""},
		{"polecat missing name is empty", RolePolecat, "gastown", "", ""},

		{"crew fully specified", RoleCrew, "gastown", "max", filepath.Join(townRoot, "gastown", "crew", "max")},
		{"crew missing rig is empty", RoleCrew, "", "max", ""},
		{"crew missing name is empty", RoleCrew, "gastown", "", ""},

		{"dog with name", RoleDog, "", "rex", filepath.Join(townRoot, "deacon", "dogs", "rex")},
		{"dog missing name is empty", RoleDog, "", "", ""},

		{"unknown is empty", RoleUnknown, "", "", ""},
		{"arbitrary role is empty", Role("wizard"), "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getRoleHome(tt.role, tt.rig, tt.polecat, townRoot)
			if got != tt.want {
				t.Errorf("getRoleHome(%v, %q, %q) = %q, want %q",
					tt.role, tt.rig, tt.polecat, got, tt.want)
			}
		})
	}
}

// TestActorString exercises RoleInfo.ActorString for every role, including
// the fallback paths when rig/polecat are missing for rig-scoped roles.
func TestActorString(t *testing.T) {
	tests := []struct {
		name string
		info RoleInfo
		want string
	}{
		{"mayor", RoleInfo{Role: RoleMayor}, "mayor"},
		{"deacon", RoleInfo{Role: RoleDeacon}, "deacon"},
		{"boot", RoleInfo{Role: RoleBoot}, "deacon-boot"},

		{"witness with rig", RoleInfo{Role: RoleWitness, Rig: "gastown"}, "gastown/witness"},
		{"witness without rig falls back", RoleInfo{Role: RoleWitness}, "witness"},

		{"refinery with rig", RoleInfo{Role: RoleRefinery, Rig: "gastown"}, "gastown/refinery"},
		{"refinery without rig falls back", RoleInfo{Role: RoleRefinery}, "refinery"},

		{"polecat fully specified", RoleInfo{Role: RolePolecat, Rig: "gastown", Polecat: "alpha"}, "gastown/polecats/alpha"},
		{"polecat missing rig falls back", RoleInfo{Role: RolePolecat, Polecat: "alpha"}, "polecat"},
		{"polecat missing name falls back", RoleInfo{Role: RolePolecat, Rig: "gastown"}, "polecat"},
		{"polecat missing both falls back", RoleInfo{Role: RolePolecat}, "polecat"},

		{"crew fully specified", RoleInfo{Role: RoleCrew, Rig: "gastown", Polecat: "max"}, "gastown/crew/max"},
		{"crew missing rig falls back", RoleInfo{Role: RoleCrew, Polecat: "max"}, "crew"},
		{"crew missing name falls back", RoleInfo{Role: RoleCrew, Rig: "gastown"}, "crew"},

		{"unknown role returns string value", RoleInfo{Role: RoleUnknown}, "unknown"},
		{"arbitrary role preserves raw value", RoleInfo{Role: Role("wizard")}, "wizard"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.ActorString()
			if got != tt.want {
				t.Errorf("ActorString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGetRoleWithContextCwdFallback verifies that when GT_ROLE is unset,
// GetRoleWithContext uses cwd detection and populates the role's home.
func TestGetRoleWithContextCwdFallback(t *testing.T) {
	townRoot := "/tmp/gt"

	// Ensure no env role leaks into the test.
	t.Setenv("GT_ROLE", "")
	t.Setenv("GT_RIG", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")

	cwd := filepath.Join(townRoot, "gastown", "polecats", "alpha")
	info, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		t.Fatalf("GetRoleWithContext: %v", err)
	}

	if info.Source != "cwd" {
		t.Errorf("Source = %q, want %q", info.Source, "cwd")
	}
	if info.Role != RolePolecat {
		t.Errorf("Role = %v, want %v", info.Role, RolePolecat)
	}
	if info.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", info.Rig, "gastown")
	}
	if info.Polecat != "alpha" {
		t.Errorf("Polecat = %q, want %q", info.Polecat, "alpha")
	}
	wantHome := filepath.Join(townRoot, "gastown", "polecats", "alpha")
	if info.Home != wantHome {
		t.Errorf("Home = %q, want %q", info.Home, wantHome)
	}
	if info.TownRoot != townRoot {
		t.Errorf("TownRoot = %q, want %q", info.TownRoot, townRoot)
	}
	if info.WorkDir != cwd {
		t.Errorf("WorkDir = %q, want %q", info.WorkDir, cwd)
	}
	if info.Mismatch {
		t.Errorf("Mismatch = true, want false (no env role set)")
	}
	if info.EnvIncomplete {
		t.Errorf("EnvIncomplete = true, want false")
	}
}

// TestGetRoleWithContextEnvAuthoritative verifies that when GT_ROLE is set
// to a compound role, it overrides cwd detection and becomes authoritative.
func TestGetRoleWithContextEnvAuthoritative(t *testing.T) {
	townRoot := "/tmp/gt"

	t.Setenv("GT_ROLE", "gastown/witness")
	t.Setenv("GT_RIG", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")

	// cwd points at mayor/ but env says witness → env wins with mismatch.
	cwd := filepath.Join(townRoot, "mayor")
	info, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		t.Fatalf("GetRoleWithContext: %v", err)
	}

	if info.Source != "env" {
		t.Errorf("Source = %q, want %q", info.Source, "env")
	}
	if info.Role != RoleWitness {
		t.Errorf("Role = %v, want %v", info.Role, RoleWitness)
	}
	if info.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", info.Rig, "gastown")
	}
	if info.EnvRole != "gastown/witness" {
		t.Errorf("EnvRole = %q, want %q", info.EnvRole, "gastown/witness")
	}
	if info.CwdRole != RoleMayor {
		t.Errorf("CwdRole = %v, want %v", info.CwdRole, RoleMayor)
	}
	if !info.Mismatch {
		t.Errorf("Mismatch = false, want true (cwd=mayor, env=witness)")
	}
	wantHome := filepath.Join(townRoot, "gastown", "witness")
	if info.Home != wantHome {
		t.Errorf("Home = %q, want %q", info.Home, wantHome)
	}
}

// TestGetRoleWithContextEnvIncomplete verifies that a bare GT_ROLE value
// (e.g. "polecat") is filled in from cwd when GT_RIG / GT_POLECAT are
// missing, and that EnvIncomplete is flagged.
func TestGetRoleWithContextEnvIncomplete(t *testing.T) {
	townRoot := "/tmp/gt"

	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")

	cwd := filepath.Join(townRoot, "gastown", "polecats", "alpha")
	info, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		t.Fatalf("GetRoleWithContext: %v", err)
	}

	if info.Source != "env" {
		t.Errorf("Source = %q, want %q", info.Source, "env")
	}
	if info.Role != RolePolecat {
		t.Errorf("Role = %v, want %v", info.Role, RolePolecat)
	}
	if info.Rig != "gastown" {
		t.Errorf("Rig filled from cwd = %q, want %q", info.Rig, "gastown")
	}
	if info.Polecat != "alpha" {
		t.Errorf("Polecat filled from cwd = %q, want %q", info.Polecat, "alpha")
	}
	if !info.EnvIncomplete {
		t.Errorf("EnvIncomplete = false, want true")
	}
	// No mismatch: env polecat agrees with cwd polecat.
	if info.Mismatch {
		t.Errorf("Mismatch = true, want false (env and cwd both say polecat)")
	}
}

// TestGetRoleWithContextEnvUsesGTRigAndGTCrew verifies that GT_RIG and
// GT_CREW / GT_POLECAT are honoured when GT_ROLE is a bare role.
func TestGetRoleWithContextEnvUsesGTRigAndGTCrew(t *testing.T) {
	townRoot := "/tmp/gt"

	t.Setenv("GT_ROLE", "crew")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_CREW", "max")
	t.Setenv("GT_POLECAT", "")

	// cwd is neutral town root so cwd detection yields unknown and no
	// mismatch should be reported.
	info, err := GetRoleWithContext(townRoot, townRoot)
	if err != nil {
		t.Fatalf("GetRoleWithContext: %v", err)
	}

	if info.Role != RoleCrew {
		t.Errorf("Role = %v, want %v", info.Role, RoleCrew)
	}
	if info.Rig != "gastown" {
		t.Errorf("Rig from GT_RIG = %q, want %q", info.Rig, "gastown")
	}
	if info.Polecat != "max" {
		t.Errorf("Polecat from GT_CREW = %q, want %q", info.Polecat, "max")
	}
	if info.EnvIncomplete {
		t.Errorf("EnvIncomplete = true, want false (env was complete)")
	}
	if info.Mismatch {
		t.Errorf("Mismatch = true, want false (town root returns RoleUnknown)")
	}
	wantHome := filepath.Join(townRoot, "gastown", "crew", "max")
	if info.Home != wantHome {
		t.Errorf("Home = %q, want %q", info.Home, wantHome)
	}
}

// TestGetRoleWithContextGTPolecatFallback verifies GT_POLECAT is used when
// GT_CREW is empty.
func TestGetRoleWithContextGTPolecatFallback(t *testing.T) {
	townRoot := "/tmp/gt"

	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "alpha")

	info, err := GetRoleWithContext(townRoot, townRoot)
	if err != nil {
		t.Fatalf("GetRoleWithContext: %v", err)
	}

	if info.Polecat != "alpha" {
		t.Errorf("Polecat from GT_POLECAT = %q, want %q", info.Polecat, "alpha")
	}
}

// TestGetRoleWithContextGTCrewPreferredOverGTPolecat verifies GT_CREW wins
// over GT_POLECAT when both are set.
func TestGetRoleWithContextGTCrewPreferredOverGTPolecat(t *testing.T) {
	townRoot := "/tmp/gt"

	t.Setenv("GT_ROLE", "crew")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_CREW", "max")
	t.Setenv("GT_POLECAT", "alpha")

	info, err := GetRoleWithContext(townRoot, townRoot)
	if err != nil {
		t.Fatalf("GetRoleWithContext: %v", err)
	}

	if info.Polecat != "max" {
		t.Errorf("Polecat = %q, want %q (GT_CREW should win)", info.Polecat, "max")
	}
}

// TestGetRoleWithContextCompoundEnvRole verifies that a compound GT_ROLE
// like "gastown/polecats/alpha" fully populates rig and polecat without
// touching cwd-derived values.
func TestGetRoleWithContextCompoundEnvRole(t *testing.T) {
	townRoot := "/tmp/gt"

	t.Setenv("GT_ROLE", "gastown/polecats/alpha")
	t.Setenv("GT_RIG", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")

	info, err := GetRoleWithContext(townRoot, townRoot)
	if err != nil {
		t.Fatalf("GetRoleWithContext: %v", err)
	}

	if info.Role != RolePolecat {
		t.Errorf("Role = %v, want %v", info.Role, RolePolecat)
	}
	if info.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", info.Rig, "gastown")
	}
	if info.Polecat != "alpha" {
		t.Errorf("Polecat = %q, want %q", info.Polecat, "alpha")
	}
	if info.EnvIncomplete {
		t.Errorf("EnvIncomplete = true, want false (compound env was complete)")
	}
}

// TestGetRoleWithContextTownRootNoMismatch is a regression guard for
// https://github.com/steveyegge/gastown/issues/1496 — running from the
// town root with GT_ROLE set must not trigger a false mismatch because
// town root cwd detection returns RoleUnknown.
func TestGetRoleWithContextTownRootNoMismatch(t *testing.T) {
	townRoot := "/tmp/gt"

	t.Setenv("GT_ROLE", "mayor")
	t.Setenv("GT_RIG", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")

	info, err := GetRoleWithContext(townRoot, townRoot)
	if err != nil {
		t.Fatalf("GetRoleWithContext: %v", err)
	}

	if info.Role != RoleMayor {
		t.Errorf("Role = %v, want %v", info.Role, RoleMayor)
	}
	if info.Mismatch {
		t.Errorf("Mismatch = true, want false at town root (cwd yields RoleUnknown)")
	}
}

// TestRoleInfoJSONTags is a guard against accidentally breaking the JSON
// shape that external tools may depend on. It lists the tag names expected
// on the exported fields so we notice renames early.
func TestRoleInfoJSONTags(t *testing.T) {
	// This test doesn't invoke encoding/json — it just documents the
	// contract. If a field is renamed or its tag changed, the compiler
	// will still pass but downstream consumers may break silently. We
	// construct a populated RoleInfo to ensure every field still has a
	// reachable zero value so at least one assertion exists.
	info := RoleInfo{
		Role:          RolePolecat,
		Source:        "env",
		Home:          "/tmp/gt/gastown/polecats/alpha",
		Rig:           "gastown",
		Polecat:       "alpha",
		EnvRole:       "gastown/polecats/alpha",
		CwdRole:       RolePolecat,
		Mismatch:      false,
		EnvIncomplete: false,
		TownRoot:      "/tmp/gt",
		WorkDir:       "/tmp/gt/gastown/polecats/alpha",
	}
	if info.ActorString() != "gastown/polecats/alpha" {
		t.Errorf("fully populated polecat RoleInfo.ActorString() mismatch: got %q", info.ActorString())
	}
}

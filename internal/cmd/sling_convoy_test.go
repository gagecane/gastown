package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestConvoyTracksBeadExactMatch verifies that convoyTracksBead finds a bead
// when the dep query returns the raw beadID.
func TestConvoyTracksBeadExactMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return a tracked dep with raw beadID
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"gt-abc123"}]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if !convoyTracksBead(beadsDir, "hq-cv-test1", "gt-abc123") {
		t.Error("convoyTracksBead should return true for exact match")
	}
}

// TestConvoyTracksBeadExternalRef verifies that convoyTracksBead finds a bead
// when the dep query returns an external-formatted reference.
func TestConvoyTracksBeadExternalRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return a tracked dep with external:prefix:beadID format
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"external:gt-abc:gt-abc123"}]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if !convoyTracksBead(beadsDir, "hq-cv-test2", "gt-abc123") {
		t.Error("convoyTracksBead should return true for external ref match")
	}
}

// TestConvoyTracksBeadNoMatch verifies that convoyTracksBead returns false
// when the convoy tracks a different bead.
func TestConvoyTracksBeadNoMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return a tracked dep with a different beadID
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"gt-other456"}]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if convoyTracksBead(beadsDir, "hq-cv-test3", "gt-abc123") {
		t.Error("convoyTracksBead should return false when bead not tracked")
	}
}

// TestConvoyTracksBeadEmptyDeps verifies that convoyTracksBead returns false
// when the convoy has no tracked deps.
func TestConvoyTracksBeadEmptyDeps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return empty array
	bdScript := `#!/bin/sh
echo '[]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if convoyTracksBead(beadsDir, "hq-cv-test4", "gt-abc123") {
		t.Error("convoyTracksBead should return false for empty deps")
	}
}

// TestConvoyTracksBeadMultipleDeps verifies that convoyTracksBead finds the
// target bead among multiple tracked deps.
func TestConvoyTracksBeadMultipleDeps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// Stub bd sql to return multiple tracked deps, one of which matches
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"gt-other1"},{"depends_on_id":"external:gt-abc:gt-abc123"},{"depends_on_id":"gt-other2"}]'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if !convoyTracksBead(beadsDir, "hq-cv-test5", "gt-abc123") {
		t.Error("convoyTracksBead should return true when bead found among multiple deps")
	}
}

// TestBdDepListRawIDsValidation verifies that bdDepListRawIDs rejects
// invalid bead IDs to prevent SQL injection.
func TestBdDepListRawIDsValidation(t *testing.T) {
	_, err := bdDepListRawIDs("/tmp", "'; DROP TABLE deps; --", "down", "tracks")
	if err == nil {
		t.Error("bdDepListRawIDs should reject SQL injection attempts")
	}

	_, err = bdDepListRawIDs("/tmp", "valid-id", "down", "'; DROP TABLE deps; --")
	if err == nil {
		t.Error("bdDepListRawIDs should reject SQL injection in depType")
	}
}

// TestConvoyBaseFromFields verifies the relay base branch is parsed from a
// convoy description (gs-9ct #1).
func TestConvoyBaseFromFields(t *testing.T) {
	cases := []struct {
		name string
		desc string
		want string
	}{
		{"named relay base", "owner: mayor/\nbase_branch: proto/v3-build\nmerge: local\n", "proto/v3-build"},
		{"no base", "owner: mayor/\nmerge: mr\n", ""},
		{"empty desc", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := convoyBaseFromFields(tc.desc); got != tc.want {
				t.Errorf("convoyBaseFromFields = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUpsertVar verifies that a relay leg's base_branch replaces the rig-default
// seed in place rather than appending a duplicate the first-match
// extractFormulaVar reader would shadow (gs-n6h).
func TestUpsertVar(t *testing.T) {
	// Relay override: rig default seeded first, then upsert with the relay base.
	// The stored vars must hold exactly ONE base_branch, set to the relay base,
	// so extractFormulaVar (first-match) reads the relay base, not the default.
	vars := []string{"base_branch=main", "lint_command=golangci-lint run"}
	vars = upsertVar(vars, "base_branch", "proto/v3-build")
	joined := strings.Join(vars, "\n")
	if got := extractFormulaVar(joined, "base_branch"); got != "proto/v3-build" {
		t.Errorf("upsert must replace in place: extractFormulaVar = %q, want proto/v3-build\nvars: %q", got, joined)
	}
	if n := strings.Count(joined, "base_branch="); n != 1 {
		t.Errorf("upsert must not duplicate the key: found %d base_branch= entries in %q", n, joined)
	}
	// Position preserved (replaced in place, not moved to the end).
	if vars[0] != "base_branch=proto/v3-build" {
		t.Errorf("upsert must preserve position: vars[0] = %q", vars[0])
	}

	// Absent key: appended.
	vars = upsertVar([]string{"lint_command=x"}, "base_branch", "feat/foo")
	if got := extractFormulaVar(strings.Join(vars, "\n"), "base_branch"); got != "feat/foo" {
		t.Errorf("upsert must append a missing key: got %q", got)
	}
}

// TestEffectiveBaseBranch_ExplicitWins verifies the short-circuit paths of
// effectiveBaseBranch that do NOT require a convoy lookup: an explicit base
// always wins, and an empty beadID returns the explicit value unchanged
// (gs-9ct #1). The inheritance path (empty explicit + tracking convoy) is
// covered by integration coverage since it hits bd/Dolt.
func TestEffectiveBaseBranch_ExplicitWins(t *testing.T) {
	if got := effectiveBaseBranch("gt-abc", "feat/explicit"); got != "feat/explicit" {
		t.Errorf("explicit base must win: got %q", got)
	}
	if got := effectiveBaseBranch("", ""); got != "" {
		t.Errorf("empty beadID must return explicit unchanged: got %q", got)
	}
}

// TestRigDefaultBranchForBead_Fallbacks verifies the rig-default resolver used
// to distinguish a genuine relay base from the formula's base_branch=<default>
// var (gs-n6h). Unresolvable prefix/rig/config all fall back to "main".
func TestRigDefaultBranchForBead_Fallbacks(t *testing.T) {
	tmp := t.TempDir()
	if got := rigDefaultBranchForBead(tmp, "noprefix"); got != "main" {
		t.Errorf("missing prefix must fall back to main: got %q", got)
	}
	if got := rigDefaultBranchForBead(tmp, "unknownrig-abc"); got != "main" {
		t.Errorf("unresolvable rig must fall back to main: got %q", got)
	}
}

package formula

import (
	"errors"
	"testing"
)

// TestValidateFormulaName verifies path-traversal and unsafe names are rejected
// while ordinary formula names pass. Guards Finding 4 (gu-hpnjo) — formula names
// are joined into filesystem paths, so "../" sequences must not escape the
// formulas directory.
func TestValidateFormulaName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"mol-polecat-work", false},
		{"shiny", false},
		{"code-review", false},
		{"mol-polecat-work.formula.toml", false},
		{"", true},
		{".", true},
		{"..", true},
		{"/", true},
		{"foo/bar", true},
		{"foo\\bar", true},
		{"../../../../etc/passwd", true},
		{"../etc/passwd", true},
		{"a..b", true},
		{"-rf", true},
		{"-foo", true},
	}

	for _, tc := range tests {
		err := validateFormulaName(tc.name)
		if tc.wantErr && err == nil {
			t.Errorf("validateFormulaName(%q): expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("validateFormulaName(%q): unexpected error: %v", tc.name, err)
		}
		if tc.wantErr && err != nil && !errors.Is(err, ErrInvalidName) {
			t.Errorf("validateFormulaName(%q): expected ErrInvalidName, got %v", tc.name, err)
		}
	}
}

// TestResolveFormulaContentRejectsTraversal verifies the public entry points
// reject traversal before touching the filesystem.
func TestResolveFormulaContentRejectsTraversal(t *testing.T) {
	if _, err := ResolveFormulaContent("../../../../etc/passwd", t.TempDir(), "rig"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("ResolveFormulaContent: expected ErrInvalidName for traversal, got %v", err)
	}
	if _, err := GetEmbeddedFormulaContent("../../../../etc/passwd"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("GetEmbeddedFormulaContent: expected ErrInvalidName for traversal, got %v", err)
	}
	if _, err := LoadFormulaOverlay("../../../../etc/passwd", t.TempDir(), "rig"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("LoadFormulaOverlay: expected ErrInvalidName for traversal, got %v", err)
	}
}

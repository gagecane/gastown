package upstreamsync

import (
	"reflect"
	"testing"
)

func TestEvaluateComplexity_Resolvable(t *testing.T) {
	v := EvaluateComplexity([]string{"internal/cmd/foo.go"}, 2, DefaultComplexityPolicy())
	if v.Class != ComplexityResolvable {
		t.Errorf("Class = %q, want %q", v.Class, ComplexityResolvable)
	}
	if v.Reason != "" {
		t.Errorf("Reason should be empty for resolvable, got %q", v.Reason)
	}
	if v.FileCount != 1 {
		t.Errorf("FileCount = %d, want 1", v.FileCount)
	}
	if v.HunkCount != 2 {
		t.Errorf("HunkCount = %d, want 2", v.HunkCount)
	}
	if len(v.RestrictedFiles) != 0 {
		t.Errorf("RestrictedFiles should be empty, got %v", v.RestrictedFiles)
	}
}

func TestEvaluateComplexity_TooManyFiles(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go", "d.go"}
	v := EvaluateComplexity(files, 0, DefaultComplexityPolicy())
	if v.Class != ComplexityTooComplexEscalate {
		t.Errorf("Class = %q, want %q", v.Class, ComplexityTooComplexEscalate)
	}
	if v.Reason == "" {
		t.Errorf("Reason should not be empty")
	}
}

func TestEvaluateComplexity_TooManyHunks(t *testing.T) {
	v := EvaluateComplexity([]string{"a.go"}, 11, DefaultComplexityPolicy())
	if v.Class != ComplexityTooComplexEscalate {
		t.Errorf("Class = %q, want %q", v.Class, ComplexityTooComplexEscalate)
	}
}

func TestEvaluateComplexity_HunkUnknown(t *testing.T) {
	// totalHunks=0 means "unknown" — file-count only governs.
	v := EvaluateComplexity([]string{"a.go"}, 0, DefaultComplexityPolicy())
	if v.Class != ComplexityResolvable {
		t.Errorf("Class = %q, want %q", v.Class, ComplexityResolvable)
	}
}

func TestEvaluateComplexity_RestrictedFiresFirst(t *testing.T) {
	// One restricted file beats hunk/file thresholds (even at 0 hunks).
	v := EvaluateComplexity([]string{"go.mod"}, 1, DefaultComplexityPolicy())
	if v.Class != ComplexityRestrictedEscalate {
		t.Errorf("Class = %q, want %q", v.Class, ComplexityRestrictedEscalate)
	}
	if !containsString(v.RestrictedFiles, "go.mod") {
		t.Errorf("RestrictedFiles = %v, want to include go.mod", v.RestrictedFiles)
	}
}

func TestEvaluateComplexity_RestrictedBeatsTooComplex(t *testing.T) {
	// Even if file count exceeds the cap, a restricted-path conflict
	// must surface as RestrictedEscalate so security comes first.
	files := []string{"a.go", "b.go", "c.go", "d.go", "internal/auth/foo.go"}
	v := EvaluateComplexity(files, 0, DefaultComplexityPolicy())
	if v.Class != ComplexityRestrictedEscalate {
		t.Errorf("Class = %q, want %q (restricted should win over too-complex)", v.Class, ComplexityRestrictedEscalate)
	}
	if !containsString(v.RestrictedFiles, "internal/auth/foo.go") {
		t.Errorf("RestrictedFiles = %v, missing internal/auth/foo.go", v.RestrictedFiles)
	}
}

func TestEvaluateComplexity_Empty(t *testing.T) {
	v := EvaluateComplexity(nil, 0, DefaultComplexityPolicy())
	if v.Class != ComplexityResolvable {
		t.Errorf("empty conflict list should classify as resolvable, got %q", v.Class)
	}
	if v.FileCount != 0 {
		t.Errorf("FileCount = %d, want 0", v.FileCount)
	}
}

func TestEvaluateComplexity_DefaultPolicy_ZeroValue(t *testing.T) {
	// Zero-value policy fills defaults.
	v := EvaluateComplexity([]string{"go.sum"}, 0, ComplexityPolicy{})
	if v.Class != ComplexityRestrictedEscalate {
		t.Errorf("zero-value policy should restrict go.sum, got %q", v.Class)
	}
}

func TestEvaluateComplexity_CustomRestricted(t *testing.T) {
	policy := ComplexityPolicy{
		MaxFiles:        3,
		MaxHunks:        10,
		RestrictedPaths: []string{"custom/path/"},
	}
	v := EvaluateComplexity([]string{"custom/path/secret.go"}, 1, policy)
	if v.Class != ComplexityRestrictedEscalate {
		t.Errorf("custom restricted dir should fire, got %q", v.Class)
	}
	// Default-restricted path should NOT match when a custom list is set.
	v2 := EvaluateComplexity([]string{"go.mod"}, 1, policy)
	if v2.Class != ComplexityResolvable {
		t.Errorf("non-restricted file should pass with custom list, got %q", v2.Class)
	}
}

func TestMatchesRestricted_DirectoryPrefix(t *testing.T) {
	patterns := []string{"internal/auth/"}
	cases := map[string]bool{
		"internal/auth/handler.go":      true,
		"internal/auth/sub/dir/file.go": true,
		"internal/auth":                 false, // not a directory match (no trailing slash)
		"cmd/internal/auth/handler.go":  false, // prefix must match from start
		"other/file.go":                 false,
	}
	for path, want := range cases {
		if got := matchesRestricted(path, patterns); got != want {
			t.Errorf("matchesRestricted(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestMatchesRestricted_Glob(t *testing.T) {
	patterns := []string{"*.sh"}
	cases := map[string]bool{
		"build.sh":         true,
		"scripts/build.sh": true, // matches via basename
		"foo.go":           false,
		"foo.sh.bak":       false,
	}
	for path, want := range cases {
		if got := matchesRestricted(path, patterns); got != want {
			t.Errorf("matchesRestricted(%q) glob = %v, want %v", path, got, want)
		}
	}
}

func TestMatchesRestricted_Exact(t *testing.T) {
	patterns := []string{"go.mod", "Makefile"}
	cases := map[string]bool{
		"go.mod":           true,
		"path/to/go.mod":   true, // basename match
		"go.modules":       false,
		"Makefile":         true,
		"path/to/Makefile": true,
		"my.Makefile":      false,
	}
	for path, want := range cases {
		if got := matchesRestricted(path, patterns); got != want {
			t.Errorf("matchesRestricted(%q) exact = %v, want %v", path, got, want)
		}
	}
}

func TestMatchesRestricted_EmptyInputs(t *testing.T) {
	if matchesRestricted("", []string{"go.mod"}) {
		t.Errorf("empty path should never match")
	}
	if matchesRestricted("go.mod", nil) {
		t.Errorf("nil patterns should never match")
	}
	if matchesRestricted("go.mod", []string{""}) {
		t.Errorf("empty pattern in slice should be skipped")
	}
}

func TestDefaultRestrictedPaths_Stable(t *testing.T) {
	// Two calls return equal slices (defensive against mutation).
	a := DefaultRestrictedPaths()
	b := DefaultRestrictedPaths()
	if !reflect.DeepEqual(a, b) {
		t.Errorf("DefaultRestrictedPaths not stable: %v vs %v", a, b)
	}
	// Sanity: critical entries are present.
	required := []string{"go.mod", "go.sum", "*.sh", "Makefile", "internal/auth/", "internal/secrets/"}
	for _, r := range required {
		if !containsString(a, r) {
			t.Errorf("DefaultRestrictedPaths missing %q (got %v)", r, a)
		}
	}
}

func TestComplexityPolicy_ResolveDefaults(t *testing.T) {
	p := ComplexityPolicy{}.resolveDefaults()
	if p.MaxFiles != 3 {
		t.Errorf("MaxFiles default = %d, want 3", p.MaxFiles)
	}
	if p.MaxHunks != 10 {
		t.Errorf("MaxHunks default = %d, want 10", p.MaxHunks)
	}
	if len(p.RestrictedPaths) == 0 {
		t.Errorf("RestrictedPaths default should be non-empty")
	}
}

func TestFormatRestrictedReason(t *testing.T) {
	cases := []struct {
		files []string
		want  string
	}{
		{[]string{"go.mod"}, "restricted-path conflict in go.mod"},
		{[]string{"a", "b"}, "restricted-path conflict in a, b"},
		{[]string{"a", "b", "c"}, "restricted-path conflict in a, b, c"},
		{[]string{"a", "b", "c", "d"}, "restricted-path conflict in a, b, c and 1 more"},
		{[]string{"a", "b", "c", "d", "e"}, "restricted-path conflict in a, b, c and 2 more"},
	}
	for _, tt := range cases {
		got := formatRestrictedReason(tt.files)
		if got != tt.want {
			t.Errorf("formatRestrictedReason(%v) = %q, want %q", tt.files, got, tt.want)
		}
	}
}

// containsString is a small slice helper used by complexity tests.
// Named to avoid collision with the string-substring contains helper
// in transitions_test.go.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

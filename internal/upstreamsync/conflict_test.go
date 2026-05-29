package upstreamsync

import (
	"strings"
	"testing"
)

func TestParseMergeTreeOutput_Clean(t *testing.T) {
	// First (and only) line is the merged tree SHA — no conflicts.
	out := "abc1234def5678abc1234def5678abc1234def56\n"
	r := parseMergeTreeOutput(out)
	if !r.IsClean() {
		t.Errorf("expected clean report, got %+v", r)
	}
}

func TestParseMergeTreeOutput_Conflicts(t *testing.T) {
	out := strings.Join([]string{
		"abc1234def5678abc1234def5678abc1234def56",
		"internal/cmd/foo.go",
		"internal/cmd/bar.go",
		"",
	}, "\n")
	r := parseMergeTreeOutput(out)
	if r.IsClean() {
		t.Fatalf("expected conflicts, got clean")
	}
	want := []string{"internal/cmd/foo.go", "internal/cmd/bar.go"}
	if len(r.Files) != len(want) {
		t.Fatalf("Files = %v, want %v", r.Files, want)
	}
	for i, w := range want {
		if r.Files[i] != w {
			t.Errorf("Files[%d] = %q, want %q", i, r.Files[i], w)
		}
	}
	if r.HunkCount != 0 {
		t.Errorf("HunkCount = %d, want 0 (--name-only does not expose hunks)", r.HunkCount)
	}
}

func TestParseMergeTreeOutput_Empty(t *testing.T) {
	r := parseMergeTreeOutput("")
	if !r.IsClean() {
		t.Errorf("empty output should be clean, got %+v", r)
	}
}

func TestParseLegacyMergeTreeOutput_Conflicts(t *testing.T) {
	// Synthetic legacy merge-tree output. The path lines look like:
	//   "their" SP "<mode>" SP "<sha>" SP "<path>"
	// and conflict hunks are introduced by "+<<<<<<<".
	out := `added in both
  our    100644 1111111111111111111111111111111111111111 internal/cmd/foo.go
  their  100644 2222222222222222222222222222222222222222 internal/cmd/foo.go
@@ -1,3 +1,5 @@
+<<<<<<< .our
+ours
+=======
+theirs
+>>>>>>> .their
added in remote
  their  100644 3333333333333333333333333333333333333333 docs/notes.md
@@ -1,1 +1,3 @@
+<<<<<<< .our
+stuff
+=======
+other
+>>>>>>> .their
`
	r := parseLegacyMergeTreeOutput(out)
	if len(r.Files) != 2 {
		t.Errorf("Files = %v, want 2 entries", r.Files)
	}
	if r.HunkCount != 2 {
		t.Errorf("HunkCount = %d, want 2", r.HunkCount)
	}
}

func TestParseLegacyMergeTreeOutput_Dedup(t *testing.T) {
	// Same file appearing on both sides should be reported once.
	out := `added in both
  our    100644 1111111111111111111111111111111111111111 internal/cmd/foo.go
  their  100644 2222222222222222222222222222222222222222 internal/cmd/foo.go
+<<<<<<< .our
+a
+=======
+b
+>>>>>>> .their
`
	r := parseLegacyMergeTreeOutput(out)
	if len(r.Files) != 1 {
		t.Errorf("expected dedup, got Files=%v", r.Files)
	}
	if r.Files[0] != "internal/cmd/foo.go" {
		t.Errorf("Files[0] = %q, want internal/cmd/foo.go", r.Files[0])
	}
}

func TestExtractLegacyPath(t *testing.T) {
	cases := map[string]string{
		"their  100644 1111111111111111111111111111111111111111 internal/cmd/foo.go": "internal/cmd/foo.go",
		"our    100644 abcd 1234 path/with spaces.go":                                "spaces.go", // last field
		"base   100644 abcd path/no/space.go":                                        "path/no/space.go",
		"":                                                                           "",
		"junk line":                                                                  "",
		"their notamode sha foo":                                                     "",
		// mode wrong length:
		"their 100 abcd foo.go": "",
		// mode not all digits:
		"their 10064a abcd foo.go": "",
	}
	for line, want := range cases {
		if got := extractLegacyPath(line); got != want {
			t.Errorf("extractLegacyPath(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestCountConflictMarkers(t *testing.T) {
	body := `package x
<<<<<<< HEAD
ours
=======
theirs
>>>>>>> upstream/main
ok
<<<<<<< HEAD
ours2
=======
theirs2
>>>>>>> upstream/main
`
	if got := countConflictMarkers(body); got != 2 {
		t.Errorf("countConflictMarkers = %d, want 2", got)
	}
}

func TestCountConflictMarkers_None(t *testing.T) {
	body := `package x

func Hello() {}
`
	if got := countConflictMarkers(body); got != 0 {
		t.Errorf("countConflictMarkers (clean file) = %d, want 0", got)
	}
}

func TestCountConflictMarkers_Suffix(t *testing.T) {
	// "<<<<<<<" must be at the start of a line; embedded occurrences
	// don't count.
	body := `// this is not a marker: <<<<<<< embedded
ok
`
	if got := countConflictMarkers(body); got != 0 {
		t.Errorf("countConflictMarkers (embedded) = %d, want 0", got)
	}
}

func TestConflictReport_IsClean(t *testing.T) {
	if !(ConflictReport{}.IsClean()) {
		t.Errorf("zero-value report should be clean")
	}
	if (ConflictReport{Files: []string{"x"}}).IsClean() {
		t.Errorf("non-empty Files should NOT be clean")
	}
}

func TestDetectConflicts_EmptyArgs(t *testing.T) {
	if _, err := DetectConflicts("", "a", "b"); err == nil {
		t.Errorf("expected error for empty gitDir")
	}
	if _, err := DetectConflicts("/tmp", "", "b"); err == nil {
		t.Errorf("expected error for empty head ref")
	}
	if _, err := DetectConflicts("/tmp", "a", ""); err == nil {
		t.Errorf("expected error for empty upstream ref")
	}
}

func TestListUnmergedPaths_EmptyGitDir(t *testing.T) {
	if _, err := ListUnmergedPaths(""); err == nil {
		t.Errorf("expected error for empty gitDir")
	}
}

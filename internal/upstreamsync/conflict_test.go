package upstreamsync

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestParseMergeTreeOutput_StopsAtInfoSection guards the real modern
// `git merge-tree --write-tree --name-only` conflict layout: the OID, the
// conflicted file list, a blank-line separator, then an informational
// section ("Auto-merging …", "CONFLICT …"). The parser must stop at the
// blank line so the info lines are not counted as conflicted files.
func TestParseMergeTreeOutput_StopsAtInfoSection(t *testing.T) {
	out := strings.Join([]string{
		"63b005e726555b4c257484795cde9545c07e49dd",
		"go.mod",
		"internal/cmd/done.go",
		"",
		"Auto-merging go.mod",
		"CONFLICT (content): Merge conflict in go.mod",
		"Auto-merging internal/cmd/done.go",
		"CONFLICT (content): Merge conflict in internal/cmd/done.go",
	}, "\n")
	r := parseMergeTreeOutput(out)
	want := []string{"go.mod", "internal/cmd/done.go"}
	if len(r.Files) != len(want) {
		t.Fatalf("Files = %v, want exactly %v (info section must be ignored)", r.Files, want)
	}
	for i, w := range want {
		if r.Files[i] != w {
			t.Errorf("Files[%d] = %q, want %q", i, r.Files[i], w)
		}
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

// runGit runs a git command in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(out)))
	}
}

// TestDetectConflicts_RealConflict is the end-to-end regression guard for
// the exit-code bug: `git merge-tree --write-tree` exits 1 when the merge
// conflicts, which the old `if err == nil` guard mistook for command
// failure — falling through to a legacy call that always reported clean.
// This builds a genuinely conflicting two-branch repo and asserts the
// conflict is detected, not silently swallowed.
func TestDetectConflicts_RealConflict(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "checkout", "-q", "-b", "base")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-q", "-m", "base")

	// Branch A diverges.
	runGit(t, dir, "checkout", "-q", "-b", "ours")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-q", "-am", "ours")

	// Branch B diverges with a conflicting edit to the same line.
	runGit(t, dir, "checkout", "-q", "base")
	runGit(t, dir, "checkout", "-q", "-b", "theirs")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-q", "-am", "theirs")

	report, err := DetectConflicts(dir, "ours", "theirs")
	if err != nil {
		t.Fatalf("DetectConflicts returned error: %v", err)
	}
	if report.IsClean() {
		t.Fatalf("expected conflict on f.txt, got clean report (exit-1 swallowed?)")
	}
	if len(report.Files) != 1 || report.Files[0] != "f.txt" {
		t.Errorf("Files = %v, want [f.txt]", report.Files)
	}
}

// TestDetectConflicts_RealClean verifies the clean (exit 0) path: a merge
// with no overlapping edits must report clean.
func TestDetectConflicts_RealClean(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "checkout", "-q", "-b", "base")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-q", "-m", "base")

	runGit(t, dir, "checkout", "-q", "-b", "ours")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-q", "-m", "add a")

	runGit(t, dir, "checkout", "-q", "base")
	runGit(t, dir, "checkout", "-q", "-b", "theirs")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "b.txt")
	runGit(t, dir, "commit", "-q", "-m", "add b")

	report, err := DetectConflicts(dir, "ours", "theirs")
	if err != nil {
		t.Fatalf("DetectConflicts returned error: %v", err)
	}
	if !report.IsClean() {
		t.Fatalf("expected clean merge, got conflicts: %v", report.Files)
	}
}

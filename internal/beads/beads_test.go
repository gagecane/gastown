package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestNew verifies the constructor.
func TestNew(t *testing.T) {
	b := New("/some/path")
	if b == nil {
		t.Fatal("New returned nil")
	}
	if b.workDir != "/some/path" {
		t.Errorf("workDir = %q, want /some/path", b.workDir)
	}
}

// TestListOptions verifies ListOptions defaults.
func TestListOptions(t *testing.T) {
	opts := ListOptions{
		Status:   "open",
		Label:    "gt:task",
		Priority: 1,
	}
	if opts.Status != "open" {
		t.Errorf("Status = %q, want open", opts.Status)
	}
}

// TestListOptionsEphemeral verifies that Ephemeral flag is preserved.
func TestListOptionsEphemeral(t *testing.T) {
	opts := ListOptions{
		Label:     "gt:merge-request",
		Status:    "open",
		Priority:  -1,
		Ephemeral: true,
	}
	if !opts.Ephemeral {
		t.Error("Ephemeral should be true")
	}
}

// TestCreateOptions verifies CreateOptions fields.
func TestCreateOptions(t *testing.T) {
	opts := CreateOptions{
		Title:       "Test issue",
		Labels:      []string{"gt:task"},
		Priority:    2,
		Description: "A test description",
		Parent:      "gt-abc",
	}
	if opts.Title != "Test issue" {
		t.Errorf("Title = %q, want 'Test issue'", opts.Title)
	}
	if opts.Parent != "gt-abc" {
		t.Errorf("Parent = %q, want gt-abc", opts.Parent)
	}
}

// TestCreateOptionsRig verifies the Rig field targets the correct rig database (gt-7y7).
// When a polecat works on a cross-rig bead (e.g., hq-xxx), gt done must explicitly
// set Rig on CreateOptions so the MR bead lands in the polecat's rig database,
// not the town-level database where the source bead lives.
func TestCreateOptionsRig(t *testing.T) {
	opts := CreateOptions{
		Title:     "Merge: hq-abc",
		Labels:    []string{"gt:merge-request"},
		Ephemeral: true,
		Rig:       "gastown",
	}
	if opts.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", opts.Rig, "gastown")
	}

	// Zero value: Rig is empty string (no --repo flag passed).
	var empty CreateOptions
	if empty.Rig != "" {
		t.Errorf("zero-value Rig = %q, want empty string", empty.Rig)
	}
}

// TestIsFlagLikeTitle verifies flag-like title detection (gt-e0kx5).
func TestIsFlagLikeTitle(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		// Flag-like (should be rejected)
		{"--help", true},
		{"--json", true},
		{"--verbose", true},
		{"-h", true},
		{"-v", true},
		{"--dry-run", true},
		{"--type=task", true},

		// Normal titles (should be allowed)
		{"Fix bug in parser", false},
		{"Add --help flag handling", false},
		{"Fix --help flag parsing", false},
		{"", false},
		{"hello", false},
		{"- list item", false}, // single dash with space is fine (markdown)
	}

	for _, tt := range tests {
		got := IsFlagLikeTitle(tt.title)
		if got != tt.want {
			t.Errorf("IsFlagLikeTitle(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

func TestBdSupportsAllowStale_ReprobesWhenBinaryPathChanges(t *testing.T) {
	bdAllowStaleMu.Lock()
	prevPath := bdAllowStalePath
	prevResult := bdAllowStaleResult
	bdAllowStaleMu.Unlock()
	ResetBdAllowStaleCacheForTest()
	t.Cleanup(func() {
		bdAllowStaleMu.Lock()
		bdAllowStalePath = prevPath
		bdAllowStaleResult = prevResult
		bdAllowStaleMu.Unlock()
	})

	supportingDir := t.TempDir()
	nonSupportingDir := t.TempDir()
	writeAllowStaleBDStub(t, supportingDir, true)
	writeAllowStaleBDStub(t, nonSupportingDir, false)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", supportingDir+string(os.PathListSeparator)+origPath)
	if !BdSupportsAllowStale() {
		t.Fatal("expected first stub to support --allow-stale")
	}

	t.Setenv("PATH", nonSupportingDir+string(os.PathListSeparator)+origPath)
	if BdSupportsAllowStale() {
		t.Fatal("expected second stub to be re-probed and report no --allow-stale support")
	}
}

// writeAllowStaleBDStub creates a mock bd binary in dir.
//
// The detection function (BdSupportsAllowStaleWithEnv) ignores the exit code
// and checks output for "unknown flag" (matching real bd v0.60+ behavior where
// unknown flags exit 0 but print an error to stderr). The stubs must match:
//   - Supporting: exit 0, no output
//   - Non-supporting: exit 0, print "unknown flag" to stderr
func writeAllowStaleBDStub(t *testing.T, dir string, supportsAllowStale bool) {
	t.Helper()

	// bd v0.60+ exits 0 even on unknown flags, printing the error to stderr.
	// Detection now checks output for "unknown flag" rather than exit code.
	var scriptPath, script string
	if runtime.GOOS == "windows" {
		scriptPath = filepath.Join(dir, "bd.bat")
		if supportsAllowStale {
			script = `@echo off
setlocal enableextensions
if "%1"=="--allow-stale" exit /b 0
exit /b 0
`
		} else {
			script = `@echo off
setlocal enableextensions
if "%1"=="--allow-stale" (
  echo Error: unknown flag: --allow-stale 1>&2
  exit /b 0
)
exit /b 0
`
		}
	} else {
		scriptPath = filepath.Join(dir, "bd")
		if supportsAllowStale {
			script = `#!/bin/sh
exit 0
`
		} else {
			script = `#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  echo "Error: unknown flag: --allow-stale" >&2
fi
exit 0
`
		}
	}

	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
}

// TestUpdateOptions verifies UpdateOptions pointer fields.
func TestUpdateOptions(t *testing.T) {
	status := "in_progress"
	priority := 1
	opts := UpdateOptions{
		Status:   &status,
		Priority: &priority,
	}
	if *opts.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress", *opts.Status)
	}
	if *opts.Priority != 1 {
		t.Errorf("Priority = %d, want 1", *opts.Priority)
	}
}

// TestIsBeadsRepo tests repository detection.
func TestIsBeadsRepo(t *testing.T) {
	// Test with a non-beads directory
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	b := New(tmpDir)
	// Should return false since there's no .beads directory
	if b.IsBeadsRepo() {
		t.Error("IsBeadsRepo returned true for non-beads directory")
	}
}

// TestWrapError tests error wrapping.
// ZFC: Only test ErrNotFound detection. ErrNotARepo and ErrSyncConflict
// were removed as per ZFC - agents should handle those errors directly.
func TestWrapError(t *testing.T) {
	b := New("/test")

	tests := []struct {
		stderr  string
		wantErr error
		wantNil bool
	}{
		{"Issue not found: gt-xyz", ErrNotFound, false},
		{"gt-xyz not found", ErrNotFound, false},
	}

	for _, tt := range tests {
		err := b.wrapError(nil, tt.stderr, []string{"test"})
		if tt.wantNil {
			if err != nil {
				t.Errorf("wrapError(%q) = %v, want nil", tt.stderr, err)
			}
		} else {
			if err != tt.wantErr {
				t.Errorf("wrapError(%q) = %v, want %v", tt.stderr, err, tt.wantErr)
			}
		}
	}
}

// TestNormalizeBugTitle tests title normalization for duplicate detection.
func TestNormalizeBugTitle(t *testing.T) {
	tests := []struct {
		a, b string
		want bool // should they match?
	}{
		// Exact match after normalization
		{"test_foo fails", "test_foo fails", true},
		{"Test_foo Fails", "test_foo fails", true},
		{" test_foo fails ", "test_foo fails", true},

		// Common prefix stripping
		{"Pre-existing failure: test_foo fails", "test_foo fails", true},
		{"Pre-existing failure: test_foo fails", "Pre-existing: test_foo fails", true},
		{"Test failure: test_foo fails", "test_foo fails", true},

		// Different failures should NOT match
		{"test_foo fails", "test_bar fails", false},
		{"lint error in main.go", "test_foo fails", false},
	}

	for _, tt := range tests {
		na := normalizeBugTitle(tt.a)
		nb := normalizeBugTitle(tt.b)
		got := na == nb
		if got != tt.want {
			t.Errorf("normalizeBugTitle(%q) == normalizeBugTitle(%q): got %v, want %v (normalized: %q vs %q)",
				tt.a, tt.b, got, tt.want, na, nb)
		}
	}
}

// TestSearchOptions verifies SearchOptions fields.
func TestSearchOptions(t *testing.T) {
	opts := SearchOptions{
		Query:  "test failure",
		Status: "open",
		Label:  "gt:bug",
		Limit:  5,
	}
	if opts.Query != "test failure" {
		t.Errorf("Query = %q, want 'test failure'", opts.Query)
	}
	if opts.Status != "open" {
		t.Errorf("Status = %q, want 'open'", opts.Status)
	}
	if opts.Label != "gt:bug" {
		t.Errorf("Label = %q, want 'gt:bug'", opts.Label)
	}
}

// Integration test that runs against real bd if available
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Find a beads repo (use current directory if it has .beads)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".beads")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("no .beads directory found in path")
		}
		dir = parent
	}

	// Resolve the actual beads directory (following redirect if present)
	// In multi-worktree setups, worktrees have .beads/redirect pointing to
	// the canonical beads location (e.g., mayor/rig/.beads)
	beadsDir := ResolveBeadsDir(dir)
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		t.Skip("no dolt database found")
	}

	b := New(dir)

	// Test List
	t.Run("List", func(t *testing.T) {
		issues, err := b.List(ListOptions{Status: "open"})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		t.Logf("Found %d open issues", len(issues))
	})

	// Test Ready
	t.Run("Ready", func(t *testing.T) {
		issues, err := b.Ready()
		if err != nil {
			t.Fatalf("Ready failed: %v", err)
		}
		t.Logf("Found %d ready issues", len(issues))
	})

	// Test Blocked
	t.Run("Blocked", func(t *testing.T) {
		issues, err := b.Blocked()
		if err != nil {
			t.Fatalf("Blocked failed: %v", err)
		}
		t.Logf("Found %d blocked issues", len(issues))
	})

	// Test Show (if we have issues)
	t.Run("Show", func(t *testing.T) {
		issues, err := b.List(ListOptions{})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(issues) == 0 {
			t.Skip("no issues to show")
		}

		issue, err := b.Show(issues[0].ID)
		if err != nil {
			t.Fatalf("Show(%s) failed: %v", issues[0].ID, err)
		}
		t.Logf("Showed issue: %s - %s", issue.ID, issue.Title)
	})
}

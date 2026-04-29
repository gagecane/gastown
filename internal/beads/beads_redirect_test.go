package beads

// Tests for beads_redirect.go helpers that are not covered by
// TestResolveBeadsDir / TestSetupRedirect in beads_test.go. Those two
// top-level tests already cover the high-level flows via SetupRedirect +
// ResolveBeadsDir end-to-end; this file fills gaps for the unexported
// helpers (rigHasOwnDB, cleanBeadsRuntimeFiles) and IsLocalBeadsDir, plus
// additional edge cases (redirect chains, .beads-suffix stripping, circular
// redirect auto-removal) that were missing.

import (
	"os"
	"path/filepath"
	"testing"
)

// --- ResolveBeadsDir edge cases not covered by TestResolveBeadsDir ---

// TestResolveBeadsDir_DotBeadsStripped exercises the "if base is .beads,
// strip it" branch that lets callers pass either the work directory or the
// .beads directory itself.
func TestResolveBeadsDir_DotBeadsStripped(t *testing.T) {
	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := ResolveBeadsDir(beadsDir)
	if got != beadsDir {
		t.Errorf("ResolveBeadsDir(%q) = %q, want %q", beadsDir, got, beadsDir)
	}
}

// TestResolveBeadsDir_CircularRedirect_RemovesFile validates the auto-remove
// behavior: a redirect file that points back to its own parent must be
// removed so we do not keep emitting warnings forever.
func TestResolveBeadsDir_CircularRedirect_RemovesFile(t *testing.T) {
	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	redirectFile := filepath.Join(beadsDir, "redirect")
	// Points at itself via a relative path.
	if err := os.WriteFile(redirectFile, []byte(".beads\n"), 0600); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	got := ResolveBeadsDir(workDir)
	if got != beadsDir {
		t.Errorf("ResolveBeadsDir with circular redirect = %q, want %q", got, beadsDir)
	}
	if _, err := os.Stat(redirectFile); !os.IsNotExist(err) {
		t.Errorf("circular redirect file was not auto-removed: err = %v", err)
	}
}

// TestResolveBeadsDir_RedirectChain follows a chain of redirects. The depth
// budget is 3, so a two-hop chain should resolve successfully.
func TestResolveBeadsDir_RedirectChain(t *testing.T) {
	root := t.TempDir()

	finalBeads := filepath.Join(root, "final", ".beads")
	midBeads := filepath.Join(root, "mid", ".beads")
	startBeads := filepath.Join(root, "start", ".beads")
	for _, d := range []string{finalBeads, midBeads, startBeads} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}

	if err := os.WriteFile(filepath.Join(startBeads, "redirect"), []byte("../mid/.beads\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(midBeads, "redirect"), []byte("../final/.beads\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := ResolveBeadsDir(filepath.Join(root, "start"))
	if filepath.Clean(got) != filepath.Clean(finalBeads) {
		t.Errorf("ResolveBeadsDir = %q, want %q", got, finalBeads)
	}
}

// --- IsLocalBeadsDir ---

func TestIsLocalBeadsDir(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "worktree")
	if err := os.MkdirAll(filepath.Join(cwd, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	otherBeads := filepath.Join(root, "other", ".beads")
	if err := os.MkdirAll(otherBeads, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tests := []struct {
		name         string
		resolvedPath string
		want         bool
	}{
		{"local beads", filepath.Join(cwd, ".beads"), true},
		{"foreign beads", otherBeads, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsLocalBeadsDir(cwd, tt.resolvedPath)
			if got != tt.want {
				t.Errorf("IsLocalBeadsDir(%q, %q) = %v, want %v", cwd, tt.resolvedPath, got, tt.want)
			}
		})
	}
}

// --- cleanBeadsRuntimeFiles ---

// TestCleanBeadsRuntimeFiles_RemovesRuntimePreservesTracked validates that
// the cleaner strips runtime-only files (daemon.log, metadata.json, mq/,
// etc.) but keeps tracked files (README.md, formulas/, config.yaml).
func TestCleanBeadsRuntimeFiles_RemovesRuntimePreservesTracked(t *testing.T) {
	beadsDir := t.TempDir()

	runtimeFiles := []string{
		"daemon.lock", "daemon.log", "daemon.pid", "bd.sock",
		"last-touched", "metadata.json",
		".local_version",
		"redirect",
	}
	for _, f := range runtimeFiles {
		if err := os.WriteFile(filepath.Join(beadsDir, f), []byte("x"), 0600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(beadsDir, "mq"), 0755); err != nil {
		t.Fatalf("mkdir mq: %v", err)
	}

	tracked := []string{"README.md", "config.yaml", ".gitignore"}
	for _, f := range tracked {
		if err := os.WriteFile(filepath.Join(beadsDir, f), []byte("keep"), 0600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(beadsDir, "formulas"), 0755); err != nil {
		t.Fatalf("mkdir formulas: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "formulas", "example.yaml"), []byte("formula"), 0600); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	if err := cleanBeadsRuntimeFiles(beadsDir); err != nil {
		t.Fatalf("cleanBeadsRuntimeFiles: %v", err)
	}

	for _, f := range runtimeFiles {
		if _, err := os.Stat(filepath.Join(beadsDir, f)); !os.IsNotExist(err) {
			t.Errorf("runtime file %s still exists", f)
		}
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "mq")); !os.IsNotExist(err) {
		t.Error("mq directory still exists")
	}
	for _, f := range tracked {
		if _, err := os.Stat(filepath.Join(beadsDir, f)); err != nil {
			t.Errorf("tracked file %s was deleted: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "formulas", "example.yaml")); err != nil {
		t.Errorf("formula file was deleted: %v", err)
	}
}

// TestCleanBeadsRuntimeFiles_MissingDir is a no-op when the directory does
// not exist.
func TestCleanBeadsRuntimeFiles_MissingDir(t *testing.T) {
	if err := cleanBeadsRuntimeFiles("/nonexistent/missing/path"); err != nil {
		t.Errorf("cleanBeadsRuntimeFiles on missing dir = %v, want nil", err)
	}
}

// --- ComputeRedirectTarget (direct) ---

// TestComputeRedirectTarget_MayorRejected refuses to create a redirect for a
// worktree at mayor/rig (the canonical beads location).
func TestComputeRedirectTarget_MayorRejected(t *testing.T) {
	town := t.TempDir()
	mayorRig := filepath.Join(town, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := ComputeRedirectTarget(town, mayorRig); err == nil {
		t.Error("expected error for mayor/rig worktree, got nil")
	}
}

// TestComputeRedirectTarget_NestedMayorRejected refuses when the worktree is
// inside a rig's mayor/.
func TestComputeRedirectTarget_NestedMayorRejected(t *testing.T) {
	town := t.TempDir()
	nested := filepath.Join(town, "somerig", "mayor", "rig")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := ComputeRedirectTarget(town, nested); err == nil {
		t.Error("expected error for rig/mayor/rig worktree, got nil")
	}
}

// TestComputeRedirectTarget_InvalidPath rejects shallow worktree paths.
func TestComputeRedirectTarget_InvalidPath(t *testing.T) {
	town := t.TempDir()
	if _, err := ComputeRedirectTarget(town, town); err == nil {
		t.Error("expected error for shallow path, got nil")
	}
}

// TestComputeRedirectTarget_NoBeadsAnywhere returns an error when no beads
// can be found at town level, rig level, or mayor/rig.
func TestComputeRedirectTarget_NoBeadsAnywhere(t *testing.T) {
	town := t.TempDir()
	worktree := filepath.Join(town, "myrig", "crew", "alice")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := ComputeRedirectTarget(town, worktree); err == nil {
		t.Error("expected error when no beads exists, got nil")
	}
}

// --- rigHasOwnDB ---

func TestRigHasOwnDB_NoMetadata(t *testing.T) {
	dir := t.TempDir()
	if rigHasOwnDB(dir) {
		t.Error("rigHasOwnDB returned true for dir with no metadata")
	}
}

func TestRigHasOwnDB_MalformedMetadata(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("not json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rigHasOwnDB(dir) {
		t.Error("rigHasOwnDB returned true for malformed metadata")
	}
}

func TestRigHasOwnDB_WithDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"),
		[]byte(`{"dolt_database": "mydb"}`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !rigHasOwnDB(dir) {
		t.Error("rigHasOwnDB returned false when dolt_database was set")
	}
}

func TestRigHasOwnDB_EmptyDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"),
		[]byte(`{"dolt_database": ""}`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rigHasOwnDB(dir) {
		t.Error("rigHasOwnDB returned true for empty dolt_database")
	}
}

package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAutoDropEnvironmentalStashes_DropsEnvOnlyAndKeepsRealWIP simulates the
// gu-6ctd scenario: a polecat ends with two stashes — one is .gitignore +
// package-lock drift, the other contains a real source-file edit. The
// auto-drop must remove the environmental stash and leave the real WIP in
// place for the existing recovery path to handle.
func TestAutoDropEnvironmentalStashes_DropsEnvOnlyAndKeepsRealWIP(t *testing.T) {
	repo := initStashRepo(t)

	// Stash 1: real WIP — modify a tracked source file.
	writeTestFile(t, filepath.Join(repo, "main.go"), "package main\n// real WIP\n")
	runGitCmd(t, repo, "stash", "push", "-m", "real WIP")

	// Stash 2 (newer = stash@{0}): environmental drift.
	writeTestFile(t, filepath.Join(repo, ".gitignore"), "build/\n")
	writeTestFile(t, filepath.Join(repo, "package-lock.json"), "{}\n")
	runGitCmd(t, repo, "add", ".gitignore", "package-lock.json")
	runGitCmd(t, repo, "stash", "push", "-m", "env drift")

	dropped, diags := autoDropEnvironmentalStashes(repo)
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1 (env stash only); diagnostics=%v", dropped, diags)
	}

	// Confirm exactly one stash remains and it's the real WIP one.
	state, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.StashCount != 1 {
		t.Fatalf("StashCount after auto-drop = %d, want 1 (real WIP must remain)", state.StashCount)
	}

	out := combinedDiagnostics(diags)
	if !strings.Contains(out, "auto_dropped_stash") {
		t.Errorf("expected 'auto_dropped_stash' diagnostic, got: %s", out)
	}
	if !strings.Contains(out, ".gitignore") {
		t.Errorf("expected dropped-stash diagnostic to list .gitignore, got: %s", out)
	}
}

// TestAutoDropEnvironmentalStashes_AllEnvDropsAll covers the cheedo case:
// two stacked stashes both containing only environmental drift. Both should
// be dropped and the worktree should be reusable.
func TestAutoDropEnvironmentalStashes_AllEnvDropsAll(t *testing.T) {
	repo := initStashRepo(t)

	// Track .gitignore in the base commit so subsequent stashes show modifications
	// via `git stash show --name-only` (untracked-only stashes do not appear there).
	writeTestFile(t, filepath.Join(repo, ".gitignore"), "")
	runGitCmd(t, repo, "add", ".gitignore")
	runGitCmd(t, repo, "commit", "-m", "track gitignore")

	// Two consecutive environmental-only stashes (modifications to .gitignore).
	writeTestFile(t, filepath.Join(repo, ".gitignore"), "first\n")
	runGitCmd(t, repo, "stash", "push", "-m", "first env")

	writeTestFile(t, filepath.Join(repo, ".gitignore"), "second\n")
	runGitCmd(t, repo, "stash", "push", "-m", "second env")

	dropped, diags := autoDropEnvironmentalStashes(repo)
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2; diagnostics=%v", dropped, diags)
	}
	state, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.StashCount != 0 {
		t.Errorf("StashCount after dropping all env stashes = %d, want 0", state.StashCount)
	}
}

// TestAutoDropEnvironmentalStashes_KeepsRealWIPAlone confirms a stash
// containing real source code is never dropped, and the count returned is 0.
func TestAutoDropEnvironmentalStashes_KeepsRealWIPAlone(t *testing.T) {
	repo := initStashRepo(t)

	writeTestFile(t, filepath.Join(repo, "main.go"), "package main\nfunc Touch() {}\n")
	runGitCmd(t, repo, "stash", "push", "-m", "real WIP")

	dropped, _ := autoDropEnvironmentalStashes(repo)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0 (real WIP must be preserved)", dropped)
	}
	state, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.StashCount != 1 {
		t.Errorf("StashCount = %d, want 1 (real WIP must remain)", state.StashCount)
	}
}

// TestAutoDropEnvironmentalStashes_NoStashes verifies the empty case is a
// no-op with no diagnostics.
func TestAutoDropEnvironmentalStashes_NoStashes(t *testing.T) {
	repo := initStashRepo(t)
	dropped, diags := autoDropEnvironmentalStashes(repo)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	if len(diags) != 0 {
		t.Errorf("diagnostics = %v, want none", diags)
	}
}

// TestAutoDropEnvironmentalStashes_StaleStashSkipped confirms the staleness
// guard: a stash whose base commit is no longer current HEAD is left alone
// even if its files look environmental.
func TestAutoDropEnvironmentalStashes_StaleStashSkipped(t *testing.T) {
	repo := initStashRepo(t)

	// Create an environmental stash, then advance HEAD past it.
	writeTestFile(t, filepath.Join(repo, ".gitignore"), "drift\n")
	runGitCmd(t, repo, "stash", "push", "-u", "-m", "stale env")

	// Add a new commit so HEAD moves past the stash's parent.
	writeTestFile(t, filepath.Join(repo, "post.txt"), "post-stash commit\n")
	runGitCmd(t, repo, "add", "post.txt")
	runGitCmd(t, repo, "commit", "-m", "advance HEAD")

	dropped, diags := autoDropEnvironmentalStashes(repo)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0 (stale stash must NOT be auto-dropped)", dropped)
	}
	if !strings.Contains(combinedDiagnostics(diags), "stale") {
		t.Errorf("expected 'stale' diagnostic, got: %v", diags)
	}
	state, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.StashCount != 1 {
		t.Errorf("StashCount = %d, want 1 (stale stash must remain)", state.StashCount)
	}
}

// initStashRepo creates a single-commit repo on `main` ready for stash tests.
func initStashRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitCmd(t, "", "init", repo)
	runGitCmd(t, repo, "config", "user.email", "test@example.com")
	runGitCmd(t, repo, "config", "user.name", "Test User")
	writeTestFile(t, filepath.Join(repo, "main.go"), "package main\n")
	runGitCmd(t, repo, "add", "main.go")
	runGitCmd(t, repo, "commit", "-m", "initial")
	runGitCmd(t, repo, "branch", "-M", "main")
	return repo
}

func combinedDiagnostics(diags []string) string {
	return strings.Join(diags, "\n")
}

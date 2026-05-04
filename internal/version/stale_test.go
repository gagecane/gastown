package version

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShortCommit(t *testing.T) {
	tests := []struct {
		name   string
		hash   string
		expect string
	}{
		{"full SHA", "abcdef1234567890abcdef1234567890abcdef12", "abcdef123456"},
		{"exactly 12", "abcdef123456", "abcdef123456"},
		{"short hash", "abcdef", "abcdef"},
		{"empty", "", ""},
		{"13 chars", "abcdef1234567", "abcdef123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShortCommit(tt.hash)
			if got != tt.expect {
				t.Errorf("ShortCommit(%q) = %q, want %q", tt.hash, got, tt.expect)
			}
		})
	}
}

func TestCommitsMatch(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"identical full", "abcdef1234567890", "abcdef1234567890", true},
		{"prefix match short-long", "abcdef1234567", "abcdef1234567890abcd", true},
		{"prefix match long-short", "abcdef1234567890abcd", "abcdef1234567", true},
		{"no match", "abcdef1234567", "1234567abcdef", false},
		{"too short a", "abc", "abcdef1234567", false},
		{"too short b", "abcdef1234567", "abc", false},
		{"both too short", "abc", "abc", false},
		{"exactly 7 chars match", "abcdefg", "abcdefg", true},
		{"exactly 7 chars no match", "abcdefg", "abcdefh", false},
		{"6 chars too short", "abcdef", "abcdef", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commitsMatch(tt.a, tt.b)
			if got != tt.expect {
				t.Errorf("commitsMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.expect)
			}
		})
	}
}

func TestSetCommit(t *testing.T) {
	original := Commit
	defer func() { Commit = original }()

	SetCommit("abc123def456")
	if Commit != "abc123def456" {
		t.Errorf("SetCommit did not set Commit; got %q", Commit)
	}
}

func TestIsBuildBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{"main", true},
		{"master", true},
		{"carry/operational", true},
		{"carry/staging", true},
		{"carry/", true},
		{"fix/something", false},
		{"feat/new-thing", false},
		{"develop", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			if got := isBuildBranch(tt.branch); got != tt.want {
				t.Errorf("isBuildBranch(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}

func TestCheckStaleBinary_NoCommit(t *testing.T) {
	original := Commit
	defer func() { Commit = original }()

	Commit = ""
	// Force resolveCommitHash to return empty by clearing Commit
	// (vcs.revision from build info may still be set, so this test
	// verifies the error path when no commit is available)
	info := CheckStaleBinary(t.TempDir())
	if info == nil {
		t.Fatal("CheckStaleBinary returned nil")
	}
	// Either we get an error (no commit) or we get a valid result from build info
	// Both are acceptable outcomes
	if info.BinaryCommit == "" && info.Error == nil {
		t.Error("expected error when binary commit is empty")
	}
}

// TestGastownRepoCandidates_OrderPrefersUpstream verifies that the candidate
// expansion prefers the fork name ("gastown_upstream") over the vanilla name
// ("gastown"). This is important because a town that has both directories
// (common during/after fork migration) must pick the active fork, not the
// stale vanilla clone. (gu-1rae)
func TestGastownRepoCandidates_OrderPrefersUpstream(t *testing.T) {
	candidates := gastownRepoCandidates("/fake/root")
	if len(candidates) == 0 {
		t.Fatal("gastownRepoCandidates returned empty slice")
	}

	// First upstream candidate must appear before any vanilla gastown candidate.
	firstUpstream := -1
	firstVanilla := -1
	for i, c := range candidates {
		if firstUpstream < 0 && strings.Contains(c, "/gastown_upstream") {
			firstUpstream = i
		}
		if firstVanilla < 0 && strings.Contains(c, "/gastown") && !strings.Contains(c, "/gastown_upstream") {
			firstVanilla = i
		}
	}
	if firstUpstream < 0 {
		t.Errorf("gastown_upstream missing from candidates: %v", candidates)
	}
	if firstVanilla < 0 {
		t.Errorf("gastown missing from candidates: %v", candidates)
	}
	if firstUpstream >= 0 && firstVanilla >= 0 && firstUpstream > firstVanilla {
		t.Errorf("expected gastown_upstream before gastown; candidates=%v", candidates)
	}
}

// TestGastownRepoCandidates_IncludesRigRootAndMayorRig verifies that both the
// bare rig directory (e.g. ".../gastown_upstream") and the mayor/rig clone
// inside it (".../gastown_upstream/mayor/rig") are checked, matching the two
// common source-of-truth layouts. (gu-1rae)
func TestGastownRepoCandidates_IncludesRigRootAndMayorRig(t *testing.T) {
	candidates := gastownRepoCandidates("/fake/root")
	want := map[string]bool{
		"/fake/root/gastown_upstream":           false,
		"/fake/root/gastown_upstream/mayor/rig": false,
		"/fake/root/gastown":                    false,
		"/fake/root/gastown/mayor/rig":          false,
	}
	for _, c := range candidates {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("missing candidate %q; got %v", path, candidates)
		}
	}
}

// TestGetRepoRoot_DiscoversGastownUpstream verifies GetRepoRoot locates a
// gastown_upstream rig when GT_ROOT points to a town that has the fork but
// no vanilla gastown directory. This is the exact scenario from gu-1rae.
func TestGetRepoRoot_DiscoversGastownUpstream(t *testing.T) {
	townRoot := t.TempDir()
	rigRoot := filepath.Join(townRoot, "gastown_upstream", "mayor", "rig")
	cmdDir := filepath.Join(rigRoot, "cmd", "gt")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("GT_ROOT", townRoot)
	// Clear HOME to avoid accidentally hitting a real ~/gt clone on the test host.
	t.Setenv("HOME", t.TempDir())

	got, err := GetRepoRoot()
	if err != nil {
		t.Fatalf("GetRepoRoot: %v", err)
	}
	if got != rigRoot {
		t.Errorf("GetRepoRoot() = %q, want %q", got, rigRoot)
	}
}

// TestGetRepoRoot_PrefersUpstreamOverVanilla verifies that when both
// directories exist in the same town, the fork wins. This guards against
// regressions where a stale ~/gt/gastown clone hangs around after a fork
// migration and is silently preferred. (gu-1rae)
func TestGetRepoRoot_PrefersUpstreamOverVanilla(t *testing.T) {
	townRoot := t.TempDir()

	// Create both candidate sources.
	for _, name := range []string{"gastown_upstream", "gastown"} {
		cmdDir := filepath.Join(townRoot, name, "mayor", "rig", "cmd", "gt")
		if err := os.MkdirAll(cmdDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	t.Setenv("GT_ROOT", townRoot)
	t.Setenv("HOME", t.TempDir())

	got, err := GetRepoRoot()
	if err != nil {
		t.Fatalf("GetRepoRoot: %v", err)
	}
	want := filepath.Join(townRoot, "gastown_upstream", "mayor", "rig")
	if got != want {
		t.Errorf("GetRepoRoot() = %q, want %q (should prefer fork)", got, want)
	}
}

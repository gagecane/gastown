package completion

import (
	"path/filepath"
	"testing"
)

// withStatExists swaps the package-level statExists probe for the duration of a
// test, restoring it afterward. The fake treats every path in `exists` as
// present and all others as absent.
func withStatExists(t *testing.T, exists map[string]bool) {
	t.Helper()
	orig := statExists
	statExists = func(path string) bool { return exists[path] }
	t.Cleanup(func() { statExists = orig })
}

func TestResolveWorktreeCwd(t *testing.T) {
	const town = "/town"
	const rig = "vets"

	tests := []struct {
		name         string
		cwd          string
		cwdAvailable bool
		envPolecat   string
		envCrew      string
		exists       map[string]bool
		want         string
	}{
		{
			name:         "cwd unavailable returns unchanged",
			cwd:          "/anything",
			cwdAvailable: false,
			want:         "/anything",
		},
		{
			name:         "already a polecat worktree root with .git stays put",
			cwd:          "/town/vets/polecats/rust/vets",
			cwdAvailable: true,
			exists:       map[string]bool{"/town/vets/polecats/rust/vets/.git": true},
			want:         "/town/vets/polecats/rust/vets",
		},
		{
			name:         "polecat subdirectory walks up to repo root",
			cwd:          "/town/vets/polecats/rust/vets/beads-ide",
			cwdAvailable: true,
			exists:       map[string]bool{"/town/vets/polecats/rust/vets/.git": true},
			want:         "/town/vets/polecats/rust/vets",
		},
		{
			name:         "polecat subdir with no .git before leaving polecats stays unchanged",
			cwd:          "/town/vets/polecats/rust/vets/sub",
			cwdAvailable: true,
			exists:       map[string]bool{},
			want:         "/town/vets/polecats/rust/vets/sub",
		},
		{
			name:         "non-worktree reconstructs nested rig clone from GT_POLECAT",
			cwd:          town, // town root, no /polecats/
			cwdAvailable: true,
			envPolecat:   "rust",
			exists:       map[string]bool{filepath.Join(town, rig, "polecats", "rust", rig): true},
			want:         filepath.Join(town, rig, "polecats", "rust", rig),
		},
		{
			name:         "non-worktree falls back to bare clone when nested rig clone absent",
			cwd:          town,
			cwdAvailable: true,
			envPolecat:   "rust",
			exists: map[string]bool{
				filepath.Join(town, rig, "polecats", "rust", ".git"): true,
			},
			want: filepath.Join(town, rig, "polecats", "rust"),
		},
		{
			name:         "non-worktree reconstructs crew clone from GT_CREW",
			cwd:          town,
			cwdAvailable: true,
			envCrew:      "scribe",
			exists:       map[string]bool{filepath.Join(town, rig, "crew", "scribe"): true},
			want:         filepath.Join(town, rig, "crew", "scribe"),
		},
		{
			name:         "non-worktree with no matching clone returns cwd unchanged",
			cwd:          town,
			cwdAvailable: true,
			envPolecat:   "rust",
			exists:       map[string]bool{},
			want:         town,
		},
		{
			name:         "non-worktree with no env vars returns cwd unchanged",
			cwd:          town,
			cwdAvailable: true,
			exists:       map[string]bool{},
			want:         town,
		},
		{
			name:         "polecat env preferred over crew env",
			cwd:          town,
			cwdAvailable: true,
			envPolecat:   "rust",
			envCrew:      "scribe",
			exists: map[string]bool{
				filepath.Join(town, rig, "polecats", "rust", rig): true,
				filepath.Join(town, rig, "crew", "scribe"):        true,
			},
			want: filepath.Join(town, rig, "polecats", "rust", rig),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStatExists(t, tt.exists)
			got := ResolveWorktreeCwd(tt.cwd, tt.cwdAvailable, town, rig, tt.envPolecat, tt.envCrew)
			if got != tt.want {
				t.Errorf("ResolveWorktreeCwd() = %q, want %q", got, tt.want)
			}
		})
	}
}

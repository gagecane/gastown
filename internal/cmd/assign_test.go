package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveAssignBeadsDir verifies that 'gt assign' routes the created bead
// to the rig database the assignee owns (not the cwd/hq database), and that an
// unroutable rig falls through to the town database with resolved=false. This
// guards against the silent wrong-DB bug where a rig-scoped bead landed in hq
// and became invisible to the owning rig's agents.
func TestResolveAssignBeadsDir(t *testing.T) {
	townRoot := t.TempDir()

	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// The rig's .beads directory must physically exist for the route alias to
	// resolve (validRepoAliasBeadsDir stats it).
	rigBeadsDir := filepath.Join(townRoot, "gastown_upstream", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	routesContent := `{"prefix": "gu-", "path": "gastown_upstream"}
{"prefix": "hq-", "path": "."}
`
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routesContent), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		rigName      string
		wantDir      string
		wantResolved bool
	}{
		{
			name:         "known rig routes to rig database",
			rigName:      "gastown_upstream",
			wantDir:      rigBeadsDir,
			wantResolved: true,
		},
		{
			name:         "unknown rig falls through to town database",
			rigName:      "nonexistent",
			wantDir:      townBeadsDir,
			wantResolved: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotDir, gotResolved := resolveAssignBeadsDir(townRoot, tc.rigName)
			if gotResolved != tc.wantResolved {
				t.Errorf("resolveAssignBeadsDir(%q, %q) resolved = %v, want %v",
					townRoot, tc.rigName, gotResolved, tc.wantResolved)
			}
			if gotDir != tc.wantDir {
				t.Errorf("resolveAssignBeadsDir(%q, %q) dir = %q, want %q",
					townRoot, tc.rigName, gotDir, tc.wantDir)
			}
		})
	}
}

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRigBdTestTown lays out a minimal town with two placement conventions:
// a canonical rig at <rig>/mayor/rig/.beads and a rig-root rig at <rig>/.beads.
// This mirrors the inconsistency the bead (gu-ecstl) calls out.
func writeRigBdTestTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()

	townBeads := filepath.Join(townRoot, ".beads")
	canonicalBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	rigRootBeads := filepath.Join(townRoot, "talon", ".beads")

	for _, dir := range []string{townBeads, canonicalBeads, rigRootBeads} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	routes := `{"prefix":"hq-","path":"."}` + "\n" +
		`{"prefix":"gt-","path":"gastown/mayor/rig"}` + "\n" +
		`{"prefix":"ti-","path":"talon"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalBeads, "metadata.json"), []byte(`{"dolt_database":"gastown"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigRootBeads, "metadata.json"), []byte(`{"dolt_database":"talon"}`), 0644); err != nil {
		t.Fatal(err)
	}

	return townRoot
}

func TestResolveRigBd(t *testing.T) {
	townRoot := writeRigBdTestTown(t)

	canonicalBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	rigRootBeads := filepath.Join(townRoot, "talon", ".beads")
	townBeads := filepath.Join(townRoot, ".beads")

	tests := []struct {
		name         string
		args         []string
		wantBeadsDir string
		wantBdArgs   []string
		wantErr      string
	}{
		{
			name:         "canonical mayor/rig placement",
			args:         []string{"gastown", "ready"},
			wantBeadsDir: canonicalBeads,
			wantBdArgs:   []string{"ready"},
		},
		{
			name:         "rig-root placement resolves the same way",
			args:         []string{"talon", "list", "--status=open"},
			wantBeadsDir: rigRootBeads,
			wantBdArgs:   []string{"list", "--status=open"},
		},
		{
			name:         "hq alias targets town database",
			args:         []string{"hq", "list", "--status=hooked"},
			wantBeadsDir: townBeads,
			wantBdArgs:   []string{"list", "--status=hooked"},
		},
		{
			name:    "missing rig name",
			args:    nil,
			wantErr: "rig name required",
		},
		{
			name:    "missing bd command",
			args:    []string{"talon"},
			wantErr: "bd command required",
		},
		{
			name:    "unknown rig",
			args:    []string{"nosuchrig", "ready"},
			wantErr: "cannot resolve beads database",
		},
		{
			name:    "path-like rig name rejected",
			args:    []string{"talon/mayor/rig", "ready"},
			wantErr: "cannot resolve beads database",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, bdArgs, err := resolveRigBd(townRoot, tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if beadsDir != tc.wantBeadsDir {
				t.Fatalf("beadsDir = %q, want %q", beadsDir, tc.wantBeadsDir)
			}
			if strings.Join(bdArgs, " ") != strings.Join(tc.wantBdArgs, " ") {
				t.Fatalf("bdArgs = %v, want %v", bdArgs, tc.wantBdArgs)
			}
		})
	}
}

package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newRoutedTownForCreate builds a temp town whose routes.jsonl maps the "tt-"
// prefix to <rig>/mayor/rig and creates that canonical rig beads directory.
func newRoutedTownForCreate(t *testing.T, rigName string) (townRoot, canonicalBeadsDir string) {
	t.Helper()
	townRoot = t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	routes := "{\"prefix\":\"tt-\",\"path\":\"" + rigName + "/mayor/rig\"}\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	canonicalBeadsDir = filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if err := os.MkdirAll(canonicalBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir canonical beads: %v", err)
	}
	return townRoot, canonicalBeadsDir
}

func TestResolveBareCreateBeadsDir(t *testing.T) {
	townRoot, canonical := newRoutedTownForCreate(t, "myrig")

	t.Run("bare create from rig worktree pins canonical rig DB", func(t *testing.T) {
		cwd := filepath.Join(townRoot, "myrig", "polecats", "rictus")
		dir, rig, ok := resolveBareCreateBeadsDir(townRoot, cwd, "", "")
		if !ok {
			t.Fatal("expected ok=true for bare create inside a rig")
		}
		if dir != canonical {
			t.Errorf("beadsDir = %q, want %q", dir, canonical)
		}
		if rig != "myrig" {
			t.Errorf("rig = %q, want %q", rig, "myrig")
		}
	})

	t.Run("explicit repo leaves cwd routing untouched", func(t *testing.T) {
		cwd := filepath.Join(townRoot, "myrig", "polecats", "rictus")
		if _, _, ok := resolveBareCreateBeadsDir(townRoot, cwd, "myrig", ""); ok {
			t.Error("expected ok=false when --repo is set")
		}
	})

	t.Run("explicit assignee leaves cwd routing untouched", func(t *testing.T) {
		cwd := filepath.Join(townRoot, "myrig", "polecats", "rictus")
		if _, _, ok := resolveBareCreateBeadsDir(townRoot, cwd, "", "mayor/"); ok {
			t.Error("expected ok=false when --assignee is set (incl. town-level)")
		}
	})

	t.Run("town root is not a rig", func(t *testing.T) {
		if _, _, ok := resolveBareCreateBeadsDir(townRoot, townRoot, "", ""); ok {
			t.Error("expected ok=false at town root")
		}
	})

	t.Run("cwd outside town", func(t *testing.T) {
		if _, _, ok := resolveBareCreateBeadsDir(townRoot, t.TempDir(), "", ""); ok {
			t.Error("expected ok=false for cwd outside town")
		}
	})

	t.Run("unknown rig has no canonical DB", func(t *testing.T) {
		cwd := filepath.Join(townRoot, "otherrig", "crew", "x")
		if _, _, ok := resolveBareCreateBeadsDir(townRoot, cwd, "", ""); ok {
			t.Error("expected ok=false for a rig with no route")
		}
	})
}

func TestStripFlagFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		flag string
		want []string
	}{
		{
			name: "space form",
			args: []string{"title", "--repo", "rig", "--type", "bug"},
			flag: "--repo",
			want: []string{"title", "--type", "bug"},
		},
		{
			name: "equals form",
			args: []string{"title", "--repo=rig", "--type", "bug"},
			flag: "--repo",
			want: []string{"title", "--type", "bug"},
		},
		{
			name: "absent",
			args: []string{"title", "--type", "bug"},
			flag: "--repo",
			want: []string{"title", "--type", "bug"},
		},
		{
			name: "leaves sentinel tail intact",
			args: []string{"--repo", "rig", "--", "--repo", "keep"},
			flag: "--repo",
			want: []string{"--", "--repo", "keep"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripFlagFromArgs(tc.args, tc.flag); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("stripFlagFromArgs(%v, %q) = %v, want %v", tc.args, tc.flag, got, tc.want)
			}
		})
	}
}

func TestFlagValueFromArgs(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		names []string
		want  string
	}{
		{
			name:  "space form",
			args:  []string{"title", "--assignee", "rig/crew/x", "--type", "bug"},
			names: []string{"--assignee", "-a"},
			want:  "rig/crew/x",
		},
		{
			name:  "equals form",
			args:  []string{"title", "--assignee=rig/crew/x"},
			names: []string{"--assignee", "-a"},
			want:  "rig/crew/x",
		},
		{
			name:  "short alias",
			args:  []string{"-a", "rig/crew/x"},
			names: []string{"--assignee", "-a"},
			want:  "rig/crew/x",
		},
		{
			name:  "repo flag",
			args:  []string{"title", "--repo", "gastown_upstream"},
			names: []string{"--repo"},
			want:  "gastown_upstream",
		},
		{
			name:  "absent",
			args:  []string{"title", "--type", "bug"},
			names: []string{"--assignee", "-a"},
			want:  "",
		},
		{
			name:  "stops at sentinel",
			args:  []string{"--", "--assignee", "rig/crew/x"},
			names: []string{"--assignee", "-a"},
			want:  "",
		},
		{
			name:  "flag with no value at end",
			args:  []string{"title", "--assignee"},
			names: []string{"--assignee", "-a"},
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := flagValueFromArgs(tc.args, tc.names...); got != tc.want {
				t.Errorf("flagValueFromArgs(%v, %v) = %q, want %q", tc.args, tc.names, got, tc.want)
			}
		})
	}
}

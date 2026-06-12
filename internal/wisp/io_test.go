package wisp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Test creating directory in existing root
	dir, err := EnsureDir(tmpDir)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}

	expectedDir := filepath.Join(tmpDir, WispDir)
	if dir != expectedDir {
		t.Errorf("EnsureDir() = %q, want %q", dir, expectedDir)
	}

	// Verify directory exists
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Failed to stat directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("EnsureDir() should create a directory")
	}

	// Test calling again (should be idempotent)
	dir2, err := EnsureDir(tmpDir)
	if err != nil {
		t.Fatalf("EnsureDir() second call error = %v", err)
	}
	if dir2 != dir {
		t.Errorf("EnsureDir() second call = %q, want %q", dir2, dir)
	}
}

func TestEnsureDir_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not reliable on Windows")
	}

	tmpDir := t.TempDir()

	dir, err := EnsureDir(tmpDir)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Failed to stat directory: %v", err)
	}

	// Check directory permissions are 0755
	expectedPerm := os.FileMode(0755)
	if info.Mode().Perm() != expectedPerm {
		t.Errorf("Directory permissions = %v, want %v", info.Mode().Perm(), expectedPerm)
	}
}

func TestWispPath(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		filename string
		want     string
	}{
		{
			name:     "basic path",
			root:     "/path/to/root",
			filename: "bead.json",
			want:     "/path/to/root/.beads/bead.json",
		},
		{
			name:     "nested filename",
			root:     "/path/to/root",
			filename: "subdir/bead.json",
			want:     "/path/to/root/.beads/subdir/bead.json",
		},
		{
			name:     "empty filename",
			root:     "/path/to/root",
			filename: "",
			want:     "/path/to/root/.beads",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WispPath(tt.root, tt.filename)
			if filepath.ToSlash(got) != tt.want {
				t.Errorf("WispPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWispPath_WithWispDir(t *testing.T) {
	// Verify WispPath uses WispDir constant
	root := "/test/root"
	filename := "test.json"

	expected := filepath.Join(root, WispDir, filename)
	got := WispPath(root, filename)

	if got != expected {
		t.Errorf("WispPath() = %q, want %q (using WispDir=%q)", got, expected, WispDir)
	}
}

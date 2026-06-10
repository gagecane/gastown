package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSizeHuman(t *testing.T) {
	dir := t.TempDir()

	// Empty directory
	got := dirSizeHuman(dir)
	if got != "0 B" {
		t.Errorf("empty dir: got %q, want %q", got, "0 B")
	}

	// Write a 1024-byte file
	data := make([]byte, 1024)
	if err := os.WriteFile(filepath.Join(dir, "file.dat"), data, 0644); err != nil {
		t.Fatal(err)
	}
	got = dirSizeHuman(dir)
	if got != "1.0 KB" {
		t.Errorf("1KB file: got %q, want %q", got, "1.0 KB")
	}

	// Add a subdirectory with another file
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	data2 := make([]byte, 512)
	if err := os.WriteFile(filepath.Join(subDir, "nested.dat"), data2, 0644); err != nil {
		t.Fatal(err)
	}
	got = dirSizeHuman(dir)
	if got != "1.5 KB" {
		t.Errorf("1.5KB total: got %q, want %q", got, "1.5 KB")
	}
}

func TestDirSizeHuman_NonexistentDir(t *testing.T) {
	got := dirSizeHuman("/nonexistent/path/that/does/not/exist")
	if got != "0 B" {
		t.Errorf("nonexistent dir: got %q, want %q", got, "0 B")
	}
}

// TestDoltSQLCmd_Flags ensures the -q/--query and -r/--result-format flags
// are registered on `gt dolt sql` so non-interactive scripted queries work
// (gu-86sy2).
func TestDoltSQLCmd_Flags(t *testing.T) {
	q := doltSQLCmd.Flags().Lookup("query")
	if q == nil {
		t.Fatal("expected --query flag to be registered on doltSQLCmd")
	}
	if q.Shorthand != "q" {
		t.Errorf("--query shorthand = %q, want %q", q.Shorthand, "q")
	}

	r := doltSQLCmd.Flags().Lookup("result-format")
	if r == nil {
		t.Fatal("expected --result-format flag to be registered on doltSQLCmd")
	}
	if r.Shorthand != "r" {
		t.Errorf("--result-format shorthand = %q, want %q", r.Shorthand, "r")
	}
}

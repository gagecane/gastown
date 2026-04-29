package beads

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDatabaseNameFromMetadata covers the happy path, missing files, malformed
// JSON, and missing fields.
func TestDatabaseNameFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		omitFile bool
		want     string
	}{
		{
			name:     "valid metadata with database",
			contents: `{"dolt_database": "gastown_db"}`,
			want:     "gastown_db",
		},
		{
			name:     "valid metadata without database field",
			contents: `{"other_key": "value"}`,
			want:     "",
		},
		{
			name:     "empty JSON object",
			contents: `{}`,
			want:     "",
		},
		{
			name:     "malformed JSON returns empty",
			contents: `{not valid json`,
			want:     "",
		},
		{
			name:     "empty dolt_database value",
			contents: `{"dolt_database": ""}`,
			want:     "",
		},
		{
			name:     "metadata with extra fields",
			contents: `{"dolt_database": "mydb", "version": 1, "nested": {"key": "val"}}`,
			want:     "mydb",
		},
		{
			name:     "missing metadata file returns empty",
			omitFile: true,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tt.omitFile {
				path := filepath.Join(dir, "metadata.json")
				if err := os.WriteFile(path, []byte(tt.contents), 0600); err != nil {
					t.Fatalf("write metadata: %v", err)
				}
			}

			got := DatabaseNameFromMetadata(dir)
			if got != tt.want {
				t.Errorf("DatabaseNameFromMetadata(%q) = %q, want %q", dir, got, tt.want)
			}
		})
	}
}

// TestDatabaseNameFromMetadata_MissingDir ensures a non-existent directory
// returns empty without panicking.
func TestDatabaseNameFromMetadata_MissingDir(t *testing.T) {
	got := DatabaseNameFromMetadata("/nonexistent/path/that/does/not/exist")
	if got != "" {
		t.Errorf("DatabaseNameFromMetadata on missing dir = %q, want empty", got)
	}
}

// TestDatabaseEnv covers the env-var formatting wrapper over
// DatabaseNameFromMetadata.
func TestDatabaseEnv(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		omitFile bool
		want     string
	}{
		{
			name:     "valid database produces env string",
			contents: `{"dolt_database": "gastown_db"}`,
			want:     "BEADS_DOLT_SERVER_DATABASE=gastown_db",
		},
		{
			name:     "missing field returns empty string",
			contents: `{"other": "val"}`,
			want:     "",
		},
		{
			name:     "empty database value returns empty string",
			contents: `{"dolt_database": ""}`,
			want:     "",
		},
		{
			name:     "malformed JSON returns empty string",
			contents: `garbage`,
			want:     "",
		},
		{
			name:     "missing metadata file returns empty string",
			omitFile: true,
			want:     "",
		},
		{
			name:     "database with special characters",
			contents: `{"dolt_database": "db-with_chars.v1"}`,
			want:     "BEADS_DOLT_SERVER_DATABASE=db-with_chars.v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tt.omitFile {
				path := filepath.Join(dir, "metadata.json")
				if err := os.WriteFile(path, []byte(tt.contents), 0600); err != nil {
					t.Fatalf("write metadata: %v", err)
				}
			}

			got := DatabaseEnv(dir)
			if got != tt.want {
				t.Errorf("DatabaseEnv(%q) = %q, want %q", dir, got, tt.want)
			}
		})
	}
}

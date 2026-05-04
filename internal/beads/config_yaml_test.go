package beads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureConfigYAMLIfMissing_DoesNotOverwriteExisting(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: keep\nissue-prefix: keep\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAMLIfMissing(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAMLIfMissing: %v", err)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if string(after) != original {
		t.Fatalf("config.yaml changed:\n got: %q\nwant: %q", string(after), original)
	}
}

func TestEnsureConfigYAMLFromMetadataIfMissing_UsesMetadataPrefix(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"hq","issue_prefix":"foo"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	if err := EnsureConfigYAMLFromMetadataIfMissing(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAMLFromMetadataIfMissing: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: foo\n") {
		t.Fatalf("config.yaml missing metadata prefix: %q", got)
	}
	if !strings.Contains(got, "issue-prefix: foo\n") {
		t.Fatalf("config.yaml missing metadata issue-prefix: %q", got)
	}
}

func TestConfigDefaultsFromMetadata_FallsBackToDoltDatabase(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"hq-custom"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	prefix := ConfigDefaultsFromMetadata(beadsDir, "hq")
	if prefix != "hq-custom" {
		t.Fatalf("prefix = %q, want %q", prefix, "hq-custom")
	}
}

func TestConfigDefaultsFromMetadata_StripsLegacyBeadsPrefixFromDoltDatabase(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_hq"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	prefix := ConfigDefaultsFromMetadata(beadsDir, "fallback")
	if prefix != "hq" {
		t.Fatalf("prefix = %q, want %q", prefix, "hq")
	}
}

func TestEnsureConfigYAMLFromMetadataIfMissing_StripsLegacyBeadsPrefixFromDoltDatabase(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_hq"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	if err := EnsureConfigYAMLFromMetadataIfMissing(beadsDir, "fallback"); err != nil {
		t.Fatalf("EnsureConfigYAMLFromMetadataIfMissing: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: hq\n") {
		t.Fatalf("config.yaml missing normalized prefix: %q", got)
	}
	if !strings.Contains(got, "issue-prefix: hq\n") {
		t.Fatalf("config.yaml missing normalized issue-prefix: %q", got)
	}
}

func TestEnsureConfigYAML_IncludesDoltAutoCommitDefault(t *testing.T) {
	beadsDir := t.TempDir()

	if err := EnsureConfigYAML(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "dolt.auto-commit: \"on\"\n") {
		t.Fatalf("config.yaml missing dolt.auto-commit default: %q", got)
	}
}

func TestEnsureConfigYAML_AddsDoltAutoCommitWhenMissing(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	// Existing config without auto-commit key
	original := "prefix: hq\nissue-prefix: hq\ndolt.idle-timeout: \"0\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAML(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "dolt.auto-commit: \"on\"\n") {
		t.Fatalf("config.yaml missing dolt.auto-commit default: %q", got)
	}
}

func TestEnsureConfigYAML_PreservesUserSetAutoCommitOff(t *testing.T) {
	// If a user has explicitly set dolt.auto-commit: "off", we must not
	// clobber it when running EnsureConfigYAML (e.g., via gt doctor fix).
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: hq\nissue-prefix: hq\ndolt.idle-timeout: \"0\"\ndolt.auto-commit: \"off\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAML(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "dolt.auto-commit: \"off\"\n") {
		t.Fatalf("config.yaml should preserve user-set dolt.auto-commit: \"off\", got: %q", got)
	}
	if strings.Contains(got, "dolt.auto-commit: \"on\"") {
		t.Fatalf("config.yaml should not overwrite user-set off with on: %q", got)
	}
}

func TestEnsureDoltAutoCommitDefault_CreatesWhenMissing(t *testing.T) {
	beadsDir := t.TempDir()

	if err := EnsureDoltAutoCommitDefault(beadsDir); err != nil {
		t.Fatalf("EnsureDoltAutoCommitDefault: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "dolt.auto-commit: \"on\"\n") {
		t.Fatalf("config.yaml missing dolt.auto-commit default: %q", got)
	}
	// Must not seed prefix/issue-prefix — callers that want those use EnsureConfigYAML.
	if strings.Contains(got, "prefix:") {
		t.Fatalf("EnsureDoltAutoCommitDefault should not write prefix keys: %q", got)
	}
}

func TestEnsureDoltAutoCommitDefault_AddsKeyWhenMissing(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: hq\nissue-prefix: hq\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureDoltAutoCommitDefault(beadsDir); err != nil {
		t.Fatalf("EnsureDoltAutoCommitDefault: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: hq\n") {
		t.Fatalf("prefix line must be preserved: %q", got)
	}
	if !strings.Contains(got, "issue-prefix: hq\n") {
		t.Fatalf("issue-prefix line must be preserved: %q", got)
	}
	if !strings.Contains(got, "dolt.auto-commit: \"on\"\n") {
		t.Fatalf("auto-commit key must be added: %q", got)
	}
}

func TestEnsureDoltAutoCommitDefault_PreservesExistingValue(t *testing.T) {
	// An existing auto-commit setting (even "off") must not be touched.
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: hq\ndolt.auto-commit: \"off\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureDoltAutoCommitDefault(beadsDir); err != nil {
		t.Fatalf("EnsureDoltAutoCommitDefault: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if got != original {
		t.Fatalf("config.yaml was modified when auto-commit already set:\n got: %q\nwant: %q", got, original)
	}
}

func TestEnsureDoltAutoCommitDefault_DoesNotDuplicateOnRepeatedCalls(t *testing.T) {
	beadsDir := t.TempDir()

	if err := EnsureDoltAutoCommitDefault(beadsDir); err != nil {
		t.Fatalf("first EnsureDoltAutoCommitDefault: %v", err)
	}
	if err := EnsureDoltAutoCommitDefault(beadsDir); err != nil {
		t.Fatalf("second EnsureDoltAutoCommitDefault: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	count := strings.Count(got, "dolt.auto-commit:")
	if count != 1 {
		t.Fatalf("dolt.auto-commit appears %d times, want 1: %q", count, got)
	}
}

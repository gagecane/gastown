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
	if !strings.Contains(got, "export.auto: \"false\"\n") {
		t.Fatalf("config.yaml missing export.auto default: %q", got)
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

	changed, err := EnsureDoltAutoCommitDefault(beadsDir)
	if err != nil {
		t.Fatalf("EnsureDoltAutoCommitDefault: %v", err)
	}
	if !changed {
		t.Fatalf("changed=false on first write; expected changed=true (gu-b7h5 contract: caller can drive a follow-up commit only when this signals)")
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

	changed, err := EnsureDoltAutoCommitDefault(beadsDir)
	if err != nil {
		t.Fatalf("EnsureDoltAutoCommitDefault: %v", err)
	}
	if !changed {
		t.Fatalf("changed=false when key was appended; expected changed=true")
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

	changed, err := EnsureDoltAutoCommitDefault(beadsDir)
	if err != nil {
		t.Fatalf("EnsureDoltAutoCommitDefault: %v", err)
	}
	if changed {
		t.Fatalf("changed=true when auto-commit was already set; expected changed=false (gu-b7h5: no follow-up commit must fire when the file was not actually modified)")
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

	changed, err := EnsureDoltAutoCommitDefault(beadsDir)
	if err != nil {
		t.Fatalf("first EnsureDoltAutoCommitDefault: %v", err)
	}
	if !changed {
		t.Fatalf("first call: expected changed=true")
	}
	changed, err = EnsureDoltAutoCommitDefault(beadsDir)
	if err != nil {
		t.Fatalf("second EnsureDoltAutoCommitDefault: %v", err)
	}
	if changed {
		t.Fatalf("second call: expected changed=false (idempotent — gu-b7h5 contract)")
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

func TestEnsureConfigYAML_DisablesAutoExport(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: old\nissue-prefix: old\ndolt.idle-timeout: \"30\"\nexport.auto: true\nsync.mode: dolt-native\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAML(beadsDir, "gt"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"prefix: gt\n",
		"issue-prefix: gt\n",
		"dolt.idle-timeout: \"0\"\n",
		"export.auto: \"false\"\n",
		"sync.mode: dolt-native\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config.yaml missing %q after repair:\n%s", want, got)
		}
	}
}

func TestEnsureConfigYAML_IncludesDoltAutoStartFalseDefault(t *testing.T) {
	beadsDir := t.TempDir()

	if err := EnsureConfigYAML(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(data), "dolt.auto-start: \"false\"\n") {
		t.Fatalf("config.yaml missing dolt.auto-start default: %q", string(data))
	}
}

func TestEnsureConfigYAML_AddsDoltAutoStartWhenMissing(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
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
	if !strings.Contains(string(data), "dolt.auto-start: \"false\"\n") {
		t.Fatalf("config.yaml missing dolt.auto-start default: %q", string(data))
	}
}

func TestEnsureConfigYAML_ForcesDoltAutoStartTrueToFalse(t *testing.T) {
	// dolt.auto-start is a town-safety key: a rig-local imposter on the shared
	// port takes down every other rig's bd. Unlike auto-commit, a user-set
	// "true" MUST be normalized to "false". See gu-hvw2a.
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: hq\nissue-prefix: hq\ndolt.auto-start: \"true\"\n"
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
	if !strings.Contains(got, "dolt.auto-start: \"false\"\n") {
		t.Fatalf("config.yaml should force dolt.auto-start to false: %q", got)
	}
	if strings.Contains(got, "dolt.auto-start: \"true\"") {
		t.Fatalf("config.yaml must not retain dolt.auto-start: \"true\": %q", got)
	}
	if strings.Count(got, "dolt.auto-start:") != 1 {
		t.Fatalf("dolt.auto-start should appear exactly once: %q", got)
	}
}

func TestConfigDisablesAutoStartHelper(t *testing.T) {
	// Guards the doctor check's detector against quoting/comment variations.
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"double quoted false", "dolt.auto-start: \"false\"\n", true},
		{"single quoted false", "dolt.auto-start: 'false'\n", true},
		{"bare false", "dolt.auto-start: false\n", true},
		{"true", "dolt.auto-start: \"true\"\n", false},
		{"missing", "prefix: hq\n", false},
		{"comment only", "# dolt.auto-start: false\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mirror the doctor check's detector logic locally to keep this test
			// in the beads package (where ensureConfigYAML lives).
			got := false
			for _, line := range strings.Split(strings.ReplaceAll(tt.content, "\r\n", "\n"), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				if strings.HasPrefix(trimmed, "dolt.auto-start:") {
					value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "dolt.auto-start:")), `"'`)
					got = strings.EqualFold(value, "false")
					break
				}
			}
			if got != tt.want {
				t.Fatalf("detector(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestConfigYAMLDisablesAutoExport(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"double quoted false", "export.auto: \"false\"\n", true},
		{"single quoted false", "export.auto: 'false'\n", true},
		{"bare false", "export.auto: false\n", true},
		{"true", "export.auto: true\n", false},
		{"missing", "prefix: hq\n", false},
		{"comment only", "# export.auto: false\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConfigYAMLDisablesAutoExport(tt.content); got != tt.want {
				t.Fatalf("ConfigYAMLDisablesAutoExport() = %v, want %v", got, tt.want)
			}
		})
	}
}

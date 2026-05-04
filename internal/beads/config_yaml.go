package beads

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// EnsureConfigYAML ensures config.yaml has both prefix keys set for the given
// beads namespace. Existing non-prefix settings are preserved.
func EnsureConfigYAML(beadsDir, prefix string) error {
	return ensureConfigYAML(beadsDir, prefix, false)
}

// EnsureConfigYAMLIfMissing creates config.yaml with the required defaults when
// it is missing. Existing files are left untouched.
func EnsureConfigYAMLIfMissing(beadsDir, prefix string) error {
	return ensureConfigYAML(beadsDir, prefix, true)
}

// EnsureConfigYAMLFromMetadataIfMissing creates config.yaml when missing using
// metadata-derived defaults for prefix when available.
func EnsureConfigYAMLFromMetadataIfMissing(beadsDir, fallbackPrefix string) error {
	prefix := ConfigDefaultsFromMetadata(beadsDir, fallbackPrefix)
	return ensureConfigYAML(beadsDir, prefix, true)
}

// ConfigDefaultsFromMetadata derives config.yaml defaults from metadata.json.
// Falls back to fallbackPrefix when fields are absent.
func ConfigDefaultsFromMetadata(beadsDir, fallbackPrefix string) string {
	prefix := strings.TrimSpace(strings.TrimSuffix(fallbackPrefix, "-"))
	if prefix == "" {
		prefix = fallbackPrefix
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return prefix
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		return prefix
	}

	if derived := firstString(meta, "issue_prefix", "issue-prefix", "prefix"); derived != "" {
		prefix = strings.TrimSpace(strings.TrimSuffix(derived, "-"))
	} else if doltDB := firstString(meta, "dolt_database"); doltDB != "" {
		prefix = normalizeDoltDatabasePrefix(doltDB)
	}

	return prefix
}

func firstString(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}

func normalizeDoltDatabasePrefix(dbName string) string {
	name := strings.TrimSpace(strings.TrimSuffix(dbName, "-"))
	if strings.HasPrefix(name, "beads_") {
		trimmed := strings.TrimPrefix(name, "beads_")
		if trimmed != "" {
			return trimmed
		}
	}
	return name
}

func ensureConfigYAML(beadsDir, prefix string, onlyIfMissing bool) error {
	configPath := filepath.Join(beadsDir, "config.yaml")
	wantPrefix := "prefix: " + prefix
	wantIssuePrefix := "issue-prefix: " + prefix
	// Gas Town rigs should disable idle-monitor to use centralized Dolt server
	wantIdleTimeout := "dolt.idle-timeout: \"0\""
	// Gas Town rigs default dolt.auto-commit=on so that ephemeral MR beads
	// created by `gt done` (and other Dolt writes) are actually committed
	// and visible to other sessions (refineries, witnesses). With
	// auto-commit=off (bd's historical default), writes sit in the working
	// set and are silently lost across sessions. See gt-2o9eg / gu-8nbc.
	wantAutoCommit := "dolt.auto-commit: \"on\""

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		// New config: include all Gas Town defaults
		content := wantPrefix + "\n" + wantIssuePrefix + "\n" + wantIdleTimeout + "\n" + wantAutoCommit + "\n"
		return os.WriteFile(configPath, []byte(content), 0644)
	}
	if err != nil {
		return err
	}
	if onlyIfMissing {
		return nil
	}

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	foundPrefix := false
	foundIssuePrefix := false
	foundIdleTimeout := false
	foundAutoCommit := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "prefix:") {
			lines[i] = wantPrefix
			foundPrefix = true
			continue
		}
		if strings.HasPrefix(trimmed, "issue-prefix:") {
			lines[i] = wantIssuePrefix
			foundIssuePrefix = true
			continue
		}
		if strings.HasPrefix(trimmed, "dolt.idle-timeout:") {
			lines[i] = wantIdleTimeout
			foundIdleTimeout = true
			continue
		}
		// dolt.auto-commit: "add if missing" semantics — respect any
		// user-set value ("on" or "off"), only insert when absent.
		if strings.HasPrefix(trimmed, "dolt.auto-commit:") {
			foundAutoCommit = true
			continue
		}
	}

	if !foundPrefix {
		lines = append(lines, wantPrefix)
	}
	if !foundIssuePrefix {
		lines = append(lines, wantIssuePrefix)
	}
	if !foundIdleTimeout {
		lines = append(lines, wantIdleTimeout)
	}
	if !foundAutoCommit {
		lines = append(lines, wantAutoCommit)
	}

	newContent := strings.Join(lines, "\n")
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	if newContent == content {
		return nil
	}

	return os.WriteFile(configPath, []byte(newContent), 0644)
}

// EnsureDoltAutoCommitDefault ensures config.yaml at beadsDir has a
// dolt.auto-commit setting, adding it with value "on" when absent. Existing
// values (whether "on" or "off") are preserved. The file is created with
// just the auto-commit key if it does not exist.
//
// Use this when you want to set the Gas Town auto-commit default without
// touching unrelated config (notably: without normalizing prefix or
// issue-prefix). This is what `gt rig add` calls on HQ's .beads/config.yaml
// so it doesn't retroactively rewrite HQ's prefix.
func EnsureDoltAutoCommitDefault(beadsDir string) error {
	configPath := filepath.Join(beadsDir, "config.yaml")
	wantAutoCommit := "dolt.auto-commit: \"on\""

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		// No config.yaml at all — create one with just the auto-commit key.
		// We intentionally do not seed prefix/issue-prefix here; callers that
		// want full defaults should use EnsureConfigYAML.
		return os.WriteFile(configPath, []byte(wantAutoCommit+"\n"), 0644)
	}
	if err != nil {
		return err
	}

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "dolt.auto-commit:") {
			// Already set (to any value) — do not touch.
			return nil
		}
	}

	// Append the auto-commit key, ensuring a trailing newline.
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += wantAutoCommit + "\n"
	return os.WriteFile(configPath, []byte(content), 0644)
}

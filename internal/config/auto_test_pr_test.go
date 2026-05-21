// Tests for the auto_test_pr block of RigSettings (Phase 0 task 1).
//
// Acceptance criteria from gu-qqpsq (synthesis Round 3 fix #7):
//
//	a. absent auto_test_pr block → returns disabled config with default
//	   cadence/skip_dirs.
//	b. well-formed block → returns parsed auto_test_pr.* keys.
//	c. malformed JSON or unknown language value → returns typed error
//	   (not a panic).

package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeRigSettingsFile is a small test helper that writes a JSON document
// to a temp settings/config.json and returns its path. Kept local to this
// file to avoid coupling auto-test-pr tests to other test fixtures.
func writeRigSettingsFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return path
}

func TestLoadRigSettings_AutoTestPRAbsent_ReturnsDisabledWithDefaults(t *testing.T) {
	t.Parallel()

	// Acceptance (a): a settings JSON with no auto_test_pr block must load
	// successfully and surface "disabled" + the documented defaults via the
	// nil-safe accessors. We deliberately keep a known-good MergeQueue block
	// so we don't exercise other validators by accident.
	body := `{
	  "type": "rig-settings",
	  "version": 1,
	  "merge_queue": { "enabled": true }
	}`
	path := writeRigSettingsFile(t, body)

	settings, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: unexpected error: %v", err)
	}
	if settings == nil {
		t.Fatal("LoadRigSettings: expected non-nil settings")
	}

	if settings.AutoTestPR != nil {
		t.Errorf("expected nil AutoTestPR (block absent), got %+v", settings.AutoTestPR)
	}

	atpr := settings.GetAutoTestPR()
	if atpr.IsEnabled() {
		t.Error("IsEnabled() = true; want false when block is absent")
	}
	if got, want := atpr.GetCadenceDays(), DefaultAutoTestPRCadenceDays; got != want {
		t.Errorf("GetCadenceDays() = %d; want default %d", got, want)
	}
	if got, want := atpr.GetConventionsPath(), DefaultAutoTestPRConventionsPath; got != want {
		t.Errorf("GetConventionsPath() = %q; want default %q", got, want)
	}
	if got := atpr.GetSkipDirs(); len(got) != 0 {
		t.Errorf("GetSkipDirs() = %v; want empty slice", got)
	}
	// GetSkipDirs MUST NOT return nil so callers can range without guarding.
	if atpr.GetSkipDirs() == nil {
		t.Error("GetSkipDirs() returned nil; want non-nil empty slice")
	}
}

func TestLoadRigSettings_AutoTestPRWellFormed_ParsesAllKeys(t *testing.T) {
	t.Parallel()

	// Acceptance (b): every documented auto_test_pr key round-trips cleanly
	// through the loader. We verify the *exact* parsed value, not just the
	// accessor's defaulted output, so a regression that swaps the JSON tag
	// (e.g. "cadenceDays" vs "cadence_days") is caught.
	body := `{
	  "type": "rig-settings",
	  "version": 1,
	  "auto_test_pr": {
	    "enabled": true,
	    "language": "go",
	    "cadence_days": 14,
	    "conventions_path": "docs/test-conventions.md",
	    "skip_dirs": ["vendor", "internal/generated"]
	  }
	}`
	path := writeRigSettingsFile(t, body)

	settings, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: unexpected error: %v", err)
	}
	atpr := settings.GetAutoTestPR()
	if atpr == nil {
		t.Fatal("expected non-nil AutoTestPR block")
	}
	if !atpr.IsEnabled() {
		t.Error("IsEnabled() = false; want true")
	}
	if got, want := atpr.Language, "go"; got != want {
		t.Errorf("Language = %q; want %q", got, want)
	}
	if got, want := atpr.GetCadenceDays(), 14; got != want {
		t.Errorf("GetCadenceDays() = %d; want %d", got, want)
	}
	if got, want := atpr.GetConventionsPath(), "docs/test-conventions.md"; got != want {
		t.Errorf("GetConventionsPath() = %q; want %q", got, want)
	}
	if got, want := atpr.GetSkipDirs(), []string{"vendor", "internal/generated"}; !reflect.DeepEqual(got, want) {
		t.Errorf("GetSkipDirs() = %v; want %v", got, want)
	}
}

func TestLoadRigSettings_AutoTestPRMalformedJSON_ReturnsTypedError(t *testing.T) {
	t.Parallel()

	// Acceptance (c) part 1: malformed JSON must surface as the standard
	// json.Unmarshal error path — never a panic. We don't assert on the
	// error string, only that it returned an error and didn't crash.
	body := `{
	  "type": "rig-settings",
	  "version": 1,
	  "auto_test_pr": { "enabled": true, "language": "go", }
	}`
	path := writeRigSettingsFile(t, body)

	_, err := LoadRigSettings(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	// A bad JSON document yields a *json.SyntaxError. Pinning to that
	// concrete type guards against a future "swallow and default" rewrite
	// that would silently drop the block.
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("error type = %T; want *json.SyntaxError. err = %v", err, err)
	}
}

func TestLoadRigSettings_AutoTestPRUnknownLanguage_ReturnsTypedError(t *testing.T) {
	t.Parallel()

	// Acceptance (c) part 2: a syntactically-valid block with a language
	// outside the v1 allow-list must surface ErrInvalidAutoTestPRLanguage so
	// `gt auto-test-pr enable` can pattern-match and route the operator to
	// the v2 follow-up bead. Pinning the sentinel ensures the CLI's typed
	// matching keeps working as the validator evolves.
	body := `{
	  "type": "rig-settings",
	  "version": 1,
	  "auto_test_pr": { "enabled": true, "language": "rust" }
	}`
	path := writeRigSettingsFile(t, body)

	_, err := LoadRigSettings(path)
	if err == nil {
		t.Fatal("expected error for unknown language, got nil")
	}
	if !errors.Is(err, ErrInvalidAutoTestPRLanguage) {
		t.Errorf("errors.Is(err, ErrInvalidAutoTestPRLanguage) = false; err = %v", err)
	}
}

func TestLoadRigSettings_AutoTestPRNegativeCadence_ReturnsTypedError(t *testing.T) {
	t.Parallel()

	// Negative cadence is the other "actively wrong" value the validator
	// catches. Zero is intentionally allowed (interpreted as "use default")
	// — see AutoTestPRConfig.GetCadenceDays — so we don't assert on it here.
	body := `{
	  "type": "rig-settings",
	  "version": 1,
	  "auto_test_pr": { "enabled": false, "cadence_days": -1 }
	}`
	path := writeRigSettingsFile(t, body)

	_, err := LoadRigSettings(path)
	if err == nil {
		t.Fatal("expected error for negative cadence, got nil")
	}
	if !errors.Is(err, ErrInvalidAutoTestPRCadence) {
		t.Errorf("errors.Is(err, ErrInvalidAutoTestPRCadence) = false; err = %v", err)
	}
}

func TestLoadRigSettings_AutoTestPRDisabledBlockShipsCleanly(t *testing.T) {
	t.Parallel()

	// "Shape ships, opt-in deferred": a block with enabled=false and no
	// language should validate, so the per-rig settings JSON can carry the
	// stanza ahead of the operator-driven `gt auto-test-pr enable` flip.
	body := `{
	  "type": "rig-settings",
	  "version": 1,
	  "auto_test_pr": { "enabled": false }
	}`
	path := writeRigSettingsFile(t, body)

	settings, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: unexpected error: %v", err)
	}
	if settings.AutoTestPR == nil {
		t.Fatal("expected non-nil AutoTestPR block")
	}
	if settings.AutoTestPR.IsEnabled() {
		t.Error("IsEnabled() = true; want false")
	}
}

func TestAutoTestPRConfigAccessors_NilSafe(t *testing.T) {
	t.Parallel()

	// All accessors must be nil-safe so callers can use them on a freshly-
	// loaded RigSettings whose AutoTestPR field happens to be nil without
	// guarding every call site.
	var nilCfg *AutoTestPRConfig
	if nilCfg.IsEnabled() {
		t.Error("(*AutoTestPRConfig)(nil).IsEnabled() = true; want false")
	}
	if got, want := nilCfg.GetCadenceDays(), DefaultAutoTestPRCadenceDays; got != want {
		t.Errorf("(*AutoTestPRConfig)(nil).GetCadenceDays() = %d; want %d", got, want)
	}
	if got, want := nilCfg.GetConventionsPath(), DefaultAutoTestPRConventionsPath; got != want {
		t.Errorf("(*AutoTestPRConfig)(nil).GetConventionsPath() = %q; want %q", got, want)
	}
	if got := nilCfg.GetSkipDirs(); got == nil || len(got) != 0 {
		t.Errorf("(*AutoTestPRConfig)(nil).GetSkipDirs() = %v; want non-nil empty slice", got)
	}

	// And RigSettings.GetAutoTestPR is nil-safe in both directions.
	var nilSettings *RigSettings
	if got := nilSettings.GetAutoTestPR(); got != nil {
		t.Errorf("(*RigSettings)(nil).GetAutoTestPR() = %+v; want nil", got)
	}
}

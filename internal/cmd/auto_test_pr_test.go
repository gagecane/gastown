package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/autotestpr"
	"github.com/steveyegge/gastown/internal/config"
)

// TestAutoTestPRShowTemplate_PrintsEmbeddedTemplate verifies that
// `gt auto-test-pr show-template` writes the embedded conventions
// template to stdout verbatim and produces no stderr output.
func TestAutoTestPRShowTemplate_PrintsEmbeddedTemplate(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd := autoTestPRShowTemplateCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	if err := runAutoTestPRShowTemplate(cmd, nil); err != nil {
		t.Fatalf("runAutoTestPRShowTemplate: %v", err)
	}
	if got, want := stdout.String(), autotestpr.ConventionsTemplate(); got != want {
		t.Errorf("stdout mismatch:\n got len=%d\nwant len=%d", len(got), len(want))
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}
}

// TestAutoTestPREnable_EmitTemplate verifies that
// `gt auto-test-pr enable --emit-template` writes the embedded
// conventions template to stdout — same content as show-template, so
// the two verbs share a single source of truth. The Phase 0 task 2d
// stub path must keep working even after task 2a wires the full
// enable surface around it.
func TestAutoTestPREnable_EmitTemplate(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd := autoTestPREnableCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	prev := autoTestPREmitTemplate
	autoTestPREmitTemplate = true
	defer func() { autoTestPREmitTemplate = prev }()

	if err := runAutoTestPREnable(cmd, nil); err != nil {
		t.Fatalf("runAutoTestPREnable: %v", err)
	}
	if got, want := stdout.String(), autotestpr.ConventionsTemplate(); got != want {
		t.Errorf("stdout mismatch:\n got len=%d\nwant len=%d", len(got), len(want))
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}
}

// TestAutoTestPREnable_MissingRigFlag_ExitsNonZero asserts the
// validation error path when the operator forgets --rig. The error
// message must mention --rig so they know which flag is missing.
func TestAutoTestPREnable_MissingRigFlag_ExitsNonZero(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd := autoTestPREnableCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	prev := autoTestPREmitTemplate
	autoTestPREmitTemplate = false
	defer func() { autoTestPREmitTemplate = prev }()
	prevRig := autoTestPREnableRig
	autoTestPREnableRig = ""
	defer func() { autoTestPREnableRig = prevRig }()

	err := runAutoTestPREnable(cmd, nil)
	if err == nil {
		t.Fatal("expected error from runAutoTestPREnable without --rig")
	}
	code, ok := IsSilentExit(err)
	if !ok {
		t.Fatalf("expected SilentExit error, got %T: %v", err, err)
	}
	if code != 2 {
		t.Errorf("SilentExit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--rig is required") {
		t.Errorf("stderr should mention --rig is required, got: %q", stderr.String())
	}
}

// TestAutoTestPREnable_MissingLanguageFlag_ExitsNonZero is the
// matching guard for --language. We assert separately from the
// missing-rig case because the two checks happen in different
// branches and we want both to remain greppable in the error string.
func TestAutoTestPREnable_MissingLanguageFlag_ExitsNonZero(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd := autoTestPREnableCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	prev := autoTestPREmitTemplate
	autoTestPREmitTemplate = false
	defer func() { autoTestPREmitTemplate = prev }()
	prevRig := autoTestPREnableRig
	autoTestPREnableRig = "gastown_upstream"
	defer func() { autoTestPREnableRig = prevRig }()
	prevLang := autoTestPREnableLanguage
	autoTestPREnableLanguage = ""
	defer func() { autoTestPREnableLanguage = prevLang }()

	err := runAutoTestPREnable(cmd, nil)
	if err == nil {
		t.Fatal("expected error from runAutoTestPREnable without --language")
	}
	code, ok := IsSilentExit(err)
	if !ok {
		t.Fatalf("expected SilentExit error, got %T: %v", err, err)
	}
	if code != 2 {
		t.Errorf("SilentExit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--language is required") {
		t.Errorf("stderr should mention --language is required, got: %q", stderr.String())
	}
}

// TestAutoTestPREnable_LanguageNotAllowed_ExitsNonZero validates the
// v1 language allow-list. A non-go language must be rejected with a
// pointer to the v2 follow-up bead so the operator has somewhere to
// go next.
func TestAutoTestPREnable_LanguageNotAllowed_ExitsNonZero(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd := autoTestPREnableCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	prev := autoTestPREmitTemplate
	autoTestPREmitTemplate = false
	defer func() { autoTestPREmitTemplate = prev }()
	prevRig := autoTestPREnableRig
	autoTestPREnableRig = "gastown_upstream"
	defer func() { autoTestPREnableRig = prevRig }()
	prevLang := autoTestPREnableLanguage
	autoTestPREnableLanguage = "rust"
	defer func() { autoTestPREnableLanguage = prevLang }()

	err := runAutoTestPREnable(cmd, nil)
	if err == nil {
		t.Fatal("expected error from runAutoTestPREnable with --language=rust")
	}
	code, ok := IsSilentExit(err)
	if !ok {
		t.Fatalf("expected SilentExit error, got %T: %v", err, err)
	}
	if code != 2 {
		t.Errorf("SilentExit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "v1 allow-list") {
		t.Errorf("stderr should mention v1 allow-list, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), autoTestPRV2FollowUpBead) {
		t.Errorf("stderr should mention v2 follow-up bead %q, got: %q",
			autoTestPRV2FollowUpBead, stderr.String())
	}
}

// TestAutoTestPREnable_RigNotAllowed_ExitsNonZero validates the v1
// pilot rig allow-list. The error message must include the v2
// follow-up bead pointer for the same reason as the language test.
func TestAutoTestPREnable_RigNotAllowed_ExitsNonZero(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd := autoTestPREnableCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	prev := autoTestPREmitTemplate
	autoTestPREmitTemplate = false
	defer func() { autoTestPREmitTemplate = prev }()
	prevRig := autoTestPREnableRig
	autoTestPREnableRig = "some_other_rig"
	defer func() { autoTestPREnableRig = prevRig }()
	prevLang := autoTestPREnableLanguage
	autoTestPREnableLanguage = "go"
	defer func() { autoTestPREnableLanguage = prevLang }()

	err := runAutoTestPREnable(cmd, nil)
	if err == nil {
		t.Fatal("expected error from runAutoTestPREnable with --rig=some_other_rig")
	}
	code, ok := IsSilentExit(err)
	if !ok {
		t.Fatalf("expected SilentExit error, got %T: %v", err, err)
	}
	if code != 2 {
		t.Errorf("SilentExit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "v1 pilot allow-list") {
		t.Errorf("stderr should mention v1 pilot allow-list, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), autoTestPRV2FollowUpBead) {
		t.Errorf("stderr should mention v2 follow-up bead %q, got: %q",
			autoTestPRV2FollowUpBead, stderr.String())
	}
}

// TestAutoTestPRDisable_MissingRigFlag_ExitsNonZero is the
// validation guard for `disable`. We accept a much narrower flag
// surface here than `enable` (no language, no allow-list) because
// disable just flips a flag — semantics should be obvious from the
// rig name alone.
func TestAutoTestPRDisable_MissingRigFlag_ExitsNonZero(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd := autoTestPRDisableCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	prev := autoTestPRDisableRig
	autoTestPRDisableRig = ""
	defer func() { autoTestPRDisableRig = prev }()

	err := runAutoTestPRDisable(cmd, nil)
	if err == nil {
		t.Fatal("expected error from runAutoTestPRDisable without --rig")
	}
	code, ok := IsSilentExit(err)
	if !ok {
		t.Fatalf("expected SilentExit error, got %T: %v", err, err)
	}
	if code != 2 {
		t.Errorf("SilentExit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--rig is required") {
		t.Errorf("stderr should mention --rig is required, got: %q", stderr.String())
	}
}

// TestAutoTestPREnable_AndShowTemplate_AgreeOnContent is the explicit
// cross-check that the two CLI verbs emit byte-identical bytes via
// the shared --emit-template path. If a future refactor accidentally
// splits the source of truth, this test goes red before the
// divergence ships.
func TestAutoTestPREnable_AndShowTemplate_AgreeOnContent(t *testing.T) {
	prev := autoTestPREmitTemplate
	autoTestPREmitTemplate = true
	defer func() { autoTestPREmitTemplate = prev }()

	enableOut := &bytes.Buffer{}
	autoTestPREnableCmd.SetOut(enableOut)
	autoTestPREnableCmd.SetErr(&bytes.Buffer{})
	defer autoTestPREnableCmd.SetOut(nil)
	defer autoTestPREnableCmd.SetErr(nil)
	if err := runAutoTestPREnable(autoTestPREnableCmd, nil); err != nil {
		t.Fatalf("enable: %v", err)
	}

	showOut := &bytes.Buffer{}
	autoTestPRShowTemplateCmd.SetOut(showOut)
	autoTestPRShowTemplateCmd.SetErr(&bytes.Buffer{})
	defer autoTestPRShowTemplateCmd.SetOut(nil)
	defer autoTestPRShowTemplateCmd.SetErr(nil)
	if err := runAutoTestPRShowTemplate(autoTestPRShowTemplateCmd, nil); err != nil {
		t.Fatalf("show-template: %v", err)
	}

	if enableOut.String() != showOut.String() {
		t.Errorf("enable --emit-template and show-template diverged:\n"+
			"enable len=%d\nshow   len=%d", enableOut.Len(), showOut.Len())
	}
}

// TestWriteAutoTestPREnabled_RoundTrips covers the settings-JSON
// writer in isolation: enable from defaults, disable, re-enable
// preserves the language. We test through the helper rather than the
// full CLI because the CLI requires a real town root + bead and we
// want to exercise the JSON shape directly.
func TestWriteAutoTestPREnabled_RoundTrips(t *testing.T) {
	t.Parallel()

	rigPath := t.TempDir()
	settingsPath := filepath.Join(rigPath, "settings", "config.json")

	// First call: writes the block from scratch with language=go.
	if err := writeAutoTestPREnabled(rigPath, "go", true); err != nil {
		t.Fatalf("first enable: %v", err)
	}

	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadRigSettings: %v", err)
	}
	if settings.AutoTestPR == nil {
		t.Fatal("AutoTestPR block must exist after enable")
	}
	if !settings.AutoTestPR.Enabled {
		t.Error("Enabled = false; want true after enable")
	}
	if got, want := settings.AutoTestPR.Language, "go"; got != want {
		t.Errorf("Language = %q; want %q", got, want)
	}

	// Disable: language must be preserved (operator did not pass it
	// on the disable path).
	if err := writeAutoTestPREnabled(rigPath, "", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	settings, err = config.LoadRigSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadRigSettings after disable: %v", err)
	}
	if settings.AutoTestPR == nil {
		t.Fatal("AutoTestPR block must persist through disable")
	}
	if settings.AutoTestPR.Enabled {
		t.Error("Enabled = true; want false after disable")
	}
	if got, want := settings.AutoTestPR.Language, "go"; got != want {
		t.Errorf("Language not preserved across disable: got %q; want %q", got, want)
	}

	// Re-enable: still preserves language (it's the same value being
	// passed back in but we verify the writer doesn't blank-out the
	// existing block on the way through).
	if err := writeAutoTestPREnabled(rigPath, "go", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	settings, err = config.LoadRigSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadRigSettings after re-enable: %v", err)
	}
	if !settings.AutoTestPR.Enabled {
		t.Error("Enabled = false; want true after re-enable")
	}

	// Sanity: settings file is well-formed JSON. We re-read raw
	// because LoadRigSettings already validates structure; this
	// catches any disk-format weirdness.
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Errorf("settings file is not valid JSON: %v", err)
	}
}

// TestIsAutoTestPRSupportedLanguage_AgreesWithConfigAllowList is a
// small drift detector. The CLI predicate and the config allow-list
// must agree — if the config allow-list grows but the CLI predicate
// doesn't, operators will see "language not in v1 allow-list" while
// staring at a successful settings JSON load.
func TestIsAutoTestPRSupportedLanguage_AgreesWithConfigAllowList(t *testing.T) {
	t.Parallel()

	for _, lang := range config.AutoTestPRSupportedLanguages {
		if !isAutoTestPRSupportedLanguage(lang) {
			t.Errorf("isAutoTestPRSupportedLanguage(%q) = false; config allow-list disagrees",
				lang)
		}
	}
	if isAutoTestPRSupportedLanguage("rust") {
		t.Error("isAutoTestPRSupportedLanguage(rust) = true; expected false in v1")
	}
	if isAutoTestPRSupportedLanguage("") {
		t.Error("isAutoTestPRSupportedLanguage(empty) = true; expected false")
	}
}

// TestIsAutoTestPRPilotRig_GastownUpstreamOnly documents the v1
// pilot allow-list as exactly {"gastown_upstream"}. If a Phase 1
// rollout adds a second rig, this test goes red and the operator is
// reminded to update both the allow-list and the matching error
// messages.
func TestIsAutoTestPRPilotRig_GastownUpstreamOnly(t *testing.T) {
	t.Parallel()

	if !isAutoTestPRPilotRig("gastown_upstream") {
		t.Error("isAutoTestPRPilotRig(gastown_upstream) = false; v1 pilot rig must be allowed")
	}
	if isAutoTestPRPilotRig("casc_crud") {
		t.Error("isAutoTestPRPilotRig(casc_crud) = true; expected false in v1")
	}
	if isAutoTestPRPilotRig("") {
		t.Error("isAutoTestPRPilotRig(empty) = true; expected false")
	}
}

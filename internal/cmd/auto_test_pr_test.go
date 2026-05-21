package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/autotestpr"
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
// the two verbs share a single source of truth.
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

// TestAutoTestPREnable_NoFlag_ExitsNonZero verifies that
// `gt auto-test-pr enable` without --emit-template returns a SilentExit
// with code 2 and prints a helpful pointer to --emit-template on
// stderr. This is the v1 stub behavior until Phase 0 task 2a wires the
// full enable surface.
func TestAutoTestPREnable_NoFlag_ExitsNonZero(t *testing.T) {
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

	err := runAutoTestPREnable(cmd, nil)
	if err == nil {
		t.Fatal("expected error from runAutoTestPREnable without --emit-template")
	}
	code, ok := IsSilentExit(err)
	if !ok {
		t.Fatalf("expected SilentExit error, got %T: %v", err, err)
	}
	if code != 2 {
		t.Errorf("SilentExit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--emit-template") {
		t.Errorf("stderr should mention --emit-template, got: %q", stderr.String())
	}
}

// TestAutoTestPREnable_AndShowTemplate_AgreeOnContent is the explicit
// cross-check that the two CLI verbs emit byte-identical bytes. If a
// future refactor accidentally splits the source of truth, this test
// goes red before the divergence ships.
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

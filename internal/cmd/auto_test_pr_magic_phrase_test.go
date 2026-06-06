// Tests for the check-magic-phrase CLI verb (Phase 2 task 20: gu-hqe16).
//
// These tests cover the flag-validation surface and the ContainsMagicPhrase
// integration path. The actual parsing logic is exhaustively tested in
// internal/autotestpr/magic_phrase_test.go; here we verify the CLI glue:
// flags, error messages, and the no-match → silent-exit path.
package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func resetAutoTestPRMagicPhraseFlags(t *testing.T) {
	t.Helper()
	autoTestPRMagicPhraseBody = ""
	autoTestPRMagicPhraseRig = ""
}

func TestCheckMagicPhraseRequiresBody(t *testing.T) {
	resetAutoTestPRMagicPhraseFlags(t)
	autoTestPRMagicPhraseRig = "gastown_upstream"

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRCheckMagicPhraseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRCheckMagicPhrase(cmd, nil)
	if err == nil {
		t.Fatal("expected error when --body is missing")
	}
	if !strings.Contains(stderr.String(), "--body is required") {
		t.Errorf("stderr = %q, want mention of --body", stderr.String())
	}
}

func TestCheckMagicPhraseRequiresRig(t *testing.T) {
	resetAutoTestPRMagicPhraseFlags(t)
	autoTestPRMagicPhraseBody = "some text"

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRCheckMagicPhraseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRCheckMagicPhrase(cmd, nil)
	if err == nil {
		t.Fatal("expected error when --rig is missing")
	}
	if !strings.Contains(stderr.String(), "--rig is required") {
		t.Errorf("stderr = %q, want mention of --rig", stderr.String())
	}
}

func TestCheckMagicPhraseNoMatch(t *testing.T) {
	resetAutoTestPRMagicPhraseFlags(t)
	autoTestPRMagicPhraseBody = "This is a regular review comment, nothing special"
	autoTestPRMagicPhraseRig = "gastown_upstream"

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRCheckMagicPhraseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRCheckMagicPhrase(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error for no-match: %v", err)
	}
	// No-match should produce no output (silent exit).
	if stdout.String() != "" {
		t.Errorf("stdout should be empty for no-match, got %q", stdout.String())
	}
}

func TestCheckMagicPhraseNearMiss(t *testing.T) {
	resetAutoTestPRMagicPhraseFlags(t)
	// Near miss: embedded in longer line
	autoTestPRMagicPhraseBody = "Please run gt auto-test-pr: pause-rig-7d now"
	autoTestPRMagicPhraseRig = "gastown_upstream"

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRCheckMagicPhraseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRCheckMagicPhrase(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error for near-miss: %v", err)
	}
	if stdout.String() != "" {
		t.Errorf("stdout should be empty for near-miss, got %q", stdout.String())
	}
}

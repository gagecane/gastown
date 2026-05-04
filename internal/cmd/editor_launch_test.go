package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestLaunchEditor_RefusesUnderGTRole(t *testing.T) {
	orig, hadOrig := os.LookupEnv("GT_ROLE")
	t.Cleanup(func() {
		if hadOrig {
			_ = os.Setenv("GT_ROLE", orig)
		} else {
			_ = os.Unsetenv("GT_ROLE")
		}
	})

	if err := os.Setenv("GT_ROLE", "polecat"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	// Path and suggestion chosen so that if the function fails to guard
	// and actually tries to exec vi, the test hangs — making a regression
	// loudly visible.
	err := launchEditor("/tmp/does-not-matter-agent-guarded", "gt test edit",
		"hint: test suggestion")
	if err == nil {
		t.Fatal("expected refusal error under GT_ROLE=polecat, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"gt test edit",
		"GT_ROLE",
		"hint: test suggestion",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got:\n%s", want, msg)
		}
	}
}

func TestLaunchEditor_NoopSuggestionFallback(t *testing.T) {
	orig, hadOrig := os.LookupEnv("GT_ROLE")
	t.Cleanup(func() {
		if hadOrig {
			_ = os.Setenv("GT_ROLE", orig)
		} else {
			_ = os.Unsetenv("GT_ROLE")
		}
	})

	if err := os.Setenv("GT_ROLE", "refinery"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	err := launchEditor("/tmp/fallback-path", "gt other", "")
	if err == nil {
		t.Fatal("expected refusal error, got nil")
	}
	if !strings.Contains(err.Error(), "/tmp/fallback-path") {
		t.Errorf("fallback hint should include path; got:\n%s", err.Error())
	}
}

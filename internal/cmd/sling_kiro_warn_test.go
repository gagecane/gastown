package cmd

import (
	"strings"
	"testing"
)

// TestWarnIfKiroPolecatTarget_FiresOnKiroOverride covers the --agent=kiro path,
// which short-circuits any config.ResolveAgentConfig lookup.
func TestWarnIfKiroPolecatTarget_FiresOnKiroOverride(t *testing.T) {
	tmp := t.TempDir()
	out := captureStderr(t, func() {
		warnIfKiroPolecatTarget(tmp, "somerig", "kiro")
	})
	if !strings.Contains(out, "kiro-polecat warning") {
		t.Errorf("expected warning banner in stderr, got %q", out)
	}
	if !strings.Contains(out, "gu-ronb") {
		t.Errorf("expected gu-ronb reference in warning, got %q", out)
	}
	if !strings.Contains(out, "somerig") {
		t.Errorf("expected rig name in warning, got %q", out)
	}
}

// TestWarnIfKiroPolecatTarget_SilentOnNonKiroOverride confirms that an explicit
// non-kiro --agent override suppresses the warning even if the rig's default
// agent is kiro.
func TestWarnIfKiroPolecatTarget_SilentOnNonKiroOverride(t *testing.T) {
	tmp := t.TempDir()
	for _, agent := range []string{"claude", "gemini", "codex", "copilot"} {
		out := captureStderr(t, func() {
			warnIfKiroPolecatTarget(tmp, "somerig", agent)
		})
		if out != "" {
			t.Errorf("agent=%q: expected no warning, got %q", agent, out)
		}
	}
}

// TestWarnIfKiroPolecatTarget_SilentWithEmptyInputs guards against spurious
// output when townRoot or rigName are empty.
func TestWarnIfKiroPolecatTarget_SilentWithEmptyInputs(t *testing.T) {
	cases := []struct{ townRoot, rigName, agent string }{
		{"", "rig", ""},
		{"/tmp", "", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		out := captureStderr(t, func() {
			warnIfKiroPolecatTarget(tc.townRoot, tc.rigName, tc.agent)
		})
		if out != "" {
			t.Errorf("townRoot=%q rig=%q agent=%q: expected no warning, got %q",
				tc.townRoot, tc.rigName, tc.agent, out)
		}
	}
}

// TestWarnIfKiroPolecatTarget_CaseInsensitive confirms "KIRO" / " Kiro "
// overrides also trigger the warning (defensive against env-derived casing).
func TestWarnIfKiroPolecatTarget_CaseInsensitive(t *testing.T) {
	tmp := t.TempDir()
	for _, agent := range []string{"KIRO", " Kiro ", "kIrO"} {
		out := captureStderr(t, func() {
			warnIfKiroPolecatTarget(tmp, "somerig", agent)
		})
		if !strings.Contains(out, "kiro-polecat warning") {
			t.Errorf("agent=%q: expected warning banner, got %q", agent, out)
		}
	}
}

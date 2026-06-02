package daemon

import (
	"testing"
)

// TestJoinLabels_Empty covers the zero-arg path so the empty
// `--label=` form (which `bd create` would reject) doesn't sneak past.
func TestJoinLabels_Empty(t *testing.T) {
	got := joinLabels(nil)
	if got != "" {
		t.Errorf("joinLabels(nil) = %q, want empty", got)
	}
}

func TestJoinLabels_Single(t *testing.T) {
	got := joinLabels([]string{"gt:task"})
	if got != "gt:task" {
		t.Errorf("joinLabels(single) = %q, want gt:task", got)
	}
}

func TestJoinLabels_Multiple(t *testing.T) {
	got := joinLabels([]string{"gt:task", "gt:auto-test-pr", "sev:1"})
	want := "gt:task,gt:auto-test-pr,sev:1"
	if got != want {
		t.Errorf("joinLabels = %q, want %q", got, want)
	}
}

// TestShortRevertSHA covers the title/log truncation used by
// fileMainCIBreakRevertTask. Mirrors the autotestpr-package shortSHA
// behavior but lives in daemon to keep the wire file self-contained.
func TestShortRevertSHA(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"abc123def4567890", "abc123def456"}, // > 12
		{"abc123def456", "abc123def456"},     // exactly 12
		{"abcd", "abcd"},                     // < 12
		{"", ""},                             // empty
	}
	for _, tt := range tests {
		got := shortRevertSHA(tt.in)
		if got != tt.want {
			t.Errorf("shortRevertSHA(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestInitMainCIBreakHandler_PatrolDisabledIsNoop verifies that
// initMainCIBreakHandler does NOT register a handler when the
// main_ci_break patrol is inactive — events keep going to
// noopMainCIBreakHandler. Guards against accidental
// "always-register" regressions that would have the SEV-1 chain fire
// even when operators have explicitly disabled the patrol via
// daemon.json.
func TestInitMainCIBreakHandler_PatrolDisabledIsNoop(t *testing.T) {
	d := &Daemon{
		// patrolConfig with main_ci_break disabled (default-zero).
		patrolConfig: &DaemonPatrolConfig{Patrols: &PatrolsConfig{}},
	}
	d.initMainCIBreakHandler()
	if d.mainCIBreakHandler != nil {
		t.Errorf("handler should not be registered when patrol disabled")
	}
}

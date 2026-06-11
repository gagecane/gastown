package daemon

import (
	"strings"
	"testing"
)

// TestDogWispArgsRootOnly is the regression guard for gu-stcak: dog molecule
// pours MUST pass --root-only so `bd mol wisp` does not materialize child
// step-wisps that dogMol.close() never closes (they would leak open until the
// 24h reaper purge, accumulating faster than they age out).
func TestDogWispArgsRootOnly(t *testing.T) {
	args := dogWispArgs("mol-dog-doctor", map[string]string{"port": "3307"})

	if len(args) < 3 || args[0] != "mol" || args[1] != "wisp" || args[2] != "mol-dog-doctor" {
		t.Fatalf("dogWispArgs: unexpected prefix %v", args)
	}

	hasRootOnly := false
	for _, a := range args {
		if a == "--root-only" {
			hasRootOnly = true
		}
	}
	if !hasRootOnly {
		t.Errorf("dogWispArgs missing --root-only flag; got %v", args)
	}

	// Vars must still be forwarded.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--var port=3307") {
		t.Errorf("dogWispArgs dropped --var; got %v", args)
	}
}

// TestLoadFormulaSteps verifies that dog formula step IDs are loaded from the
// formula file (embedded tier), since `bd mol wisp` no longer materializes
// child step-wisps (root-only wisp model). This is the regression guard for
// the "0 steps" / "unknown step (known: [])" treadmill (gu-861mn).
func TestLoadFormulaSteps(t *testing.T) {
	tests := []struct {
		formula string
		want    []string
	}{
		{"mol-dog-checkpoint", []string{"scan", "checkpoint", "report"}},
		{"mol-dog-reaper", []string{"scan", "reap", "purge", "flush-wisps", "auto-close", "report"}},
		{"mol-dog-doctor", []string{"probe", "inspect", "report"}},
		{"mol-dog-backup", []string{"sync", "offsite", "report"}},
	}

	for _, tt := range tests {
		t.Run(tt.formula, func(t *testing.T) {
			dm := &dogMol{
				steps:  make(map[string]bool),
				logger: &recordingLogger{},
			}
			dm.loadFormulaSteps(tt.formula)

			if len(dm.steps) == 0 {
				t.Fatalf("loadFormulaSteps(%q) found 0 steps — regression of the 0-step pour bug", tt.formula)
			}
			for _, id := range tt.want {
				if !dm.steps[id] {
					t.Errorf("step %q missing from loaded steps %v", id, dm.knownSteps())
				}
			}
		})
	}
}

func TestParseWispID(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantID string
	}{
		{
			name:   "standard wisp output",
			input:  "✓ Spawned wisp: gt-wisp-abc123 — Reap stale wisps",
			wantID: "gt-wisp-abc123",
		},
		{
			name:   "wisp ID with ANSI codes",
			input:  "\033[32m✓\033[0m Spawned wisp: \033[1mgt-wisp-xyz789\033[0m — Title",
			wantID: "gt-wisp-xyz789",
		},
		{
			name:   "empty output",
			input:  "",
			wantID: "",
		},
		{
			name:   "no wisp ID in output",
			input:  "Error: something went wrong",
			wantID: "",
		},
		{
			name:   "wisp ID at end of line",
			input:  "Created gt-wisp-def456",
			wantID: "gt-wisp-def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWispID(tt.input)
			if got != tt.wantID {
				t.Errorf("parseWispID(%q) = %q, want %q", tt.input, got, tt.wantID)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ANSI", "hello", "hello"},
		{"color code", "\033[32mgreen\033[0m", "green"},
		{"bold", "\033[1mbold\033[0m", "bold"},
		{"multiple codes", "\033[32m✓\033[0m \033[1mtext\033[0m", "✓ text"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(tt.input)
			if got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDogMolGracefulDegradation(t *testing.T) {
	// A dogMol with empty rootID should be a no-op for all operations.
	dm := &dogMol{
		rootID: "",
		steps:  make(map[string]bool),
	}

	// These should not panic or error — graceful degradation.
	dm.closeStep("scan")
	dm.failStep("scan", "test failure")
	dm.close()
}

func TestDogMolUnknownStepWarns(t *testing.T) {
	// closeStep/failStep validate the step ID against the formula's declared
	// steps and warn (but do not panic) on drift.
	rec := &recordingLogger{}
	dm := &dogMol{
		rootID:  "gt-wisp-test",
		formula: "mol-dog-test",
		steps:   map[string]bool{"scan": true},
		logger:  rec,
	}

	// Known step: no warning.
	dm.closeStep("scan")
	if rec.count() != 0 {
		t.Errorf("known step should not warn, got %d log lines", rec.count())
	}

	// Unknown step: warns.
	dm.closeStep("nonexistent")
	if rec.count() != 1 {
		t.Errorf("unknown step should warn once, got %d log lines", rec.count())
	}

	// failStep on a known step logs the failure reason.
	rec.reset()
	dm.failStep("scan", "boom")
	if rec.count() != 1 {
		t.Errorf("failStep on known step should log once, got %d log lines", rec.count())
	}
}

// recordingLogger captures Printf calls for assertions.
type recordingLogger struct {
	lines []string
}

func (r *recordingLogger) Printf(format string, args ...interface{}) {
	r.lines = append(r.lines, format)
}

func (r *recordingLogger) count() int { return len(r.lines) }
func (r *recordingLogger) reset()     { r.lines = nil }

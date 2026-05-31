package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestClassifyMemory exercises every band of the memory-check classifier
// (gu-ayam3). The defaults mirror the patrol formula thresholds in
// mol-deacon-patrol.formula.toml so this test doubles as a contract for
// what the deacon will report.
func TestClassifyMemory(t *testing.T) {
	const (
		warn     = 10.0
		high     = 5.0
		critical = 2.0
	)

	cases := []struct {
		name        string
		avail       float64
		doEscalate  bool
		dryRun      bool
		wantSev     string
		wantAction  string
		wantExit2   bool
	}{
		{"unknown when zero", 0, true, false, "unknown", "skipped", false},
		{"unknown when negative", -1, true, false, "unknown", "skipped", false},
		{"healthy above warn", 32.0, true, false, "ok", "none", false},
		{"warn just below 10GB", 9.5, true, false, "warn", "logged", false},
		{"warn at boundary 10GB", 10.0, true, false, "ok", "none", false},
		{"high band 4GB", 4.0, true, false, "high", "escalated", true},
		{"high dry-run does not escalate", 4.0, true, true, "high", "would-escalate", true},
		{"high no-escalate flag", 4.0, false, false, "high", "would-escalate", true},
		{"critical band 1GB escalates", 1.0, true, false, "critical", "escalated", true},
		{"critical takes priority over high", 1.5, true, false, "critical", "escalated", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, exit2 := classifyMemory(tc.avail, warn, high, critical, tc.doEscalate, tc.dryRun)
			if r.Severity != tc.wantSev {
				t.Errorf("Severity = %q, want %q (avail=%.2f)", r.Severity, tc.wantSev, tc.avail)
			}
			if r.Action != tc.wantAction {
				t.Errorf("Action = %q, want %q (avail=%.2f)", r.Action, tc.wantAction, tc.avail)
			}
			if exit2 != tc.wantExit2 {
				t.Errorf("exit2 = %v, want %v (avail=%.2f)", exit2, tc.wantExit2, tc.avail)
			}
			if r.MemAvailableGB != tc.avail {
				t.Errorf("MemAvailableGB = %v, want %v", r.MemAvailableGB, tc.avail)
			}
		})
	}
}

// TestClassifyMemoryDisabledThresholds verifies that a 0 threshold disables
// that tier (matching the daemon pressure-check convention). This lets
// operators turn off, say, the WARN tier without having to set a sentinel.
func TestClassifyMemoryDisabledThresholds(t *testing.T) {
	// All thresholds disabled → never alerts even at 0.5GB available.
	r, exit2 := classifyMemory(0.5, 0, 0, 0, true, false)
	if r.Severity != "ok" {
		t.Errorf("with all thresholds disabled, severity = %q, want ok", r.Severity)
	}
	if exit2 {
		t.Error("with all thresholds disabled, want exit2=false, got true")
	}

	// Only critical configured → 4GB does not match (above critical) so OK.
	r, _ = classifyMemory(4.0, 0, 0, 2.0, true, false)
	if r.Severity != "ok" {
		t.Errorf("only critical configured at 2GB, 4GB available → severity %q, want ok", r.Severity)
	}

	// Only critical configured, 1GB available → critical fires.
	r, exit2 = classifyMemory(1.0, 0, 0, 2.0, true, false)
	if r.Severity != "critical" {
		t.Errorf("only critical configured, 1GB available → severity %q, want critical", r.Severity)
	}
	if !exit2 {
		t.Error("critical band must trigger exit2")
	}
}

// TestThresholdFor verifies the helper picks the right threshold for the
// escalation message. Important because we put the threshold in the human-
// readable description and a wrong value would mislead the on-call.
func TestThresholdFor(t *testing.T) {
	r := memoryCheckResult{Severity: "critical", HighGB: 5, CriticalGB: 2}
	if got := thresholdFor(r); got != 2 {
		t.Errorf("critical thresholdFor = %v, want 2", got)
	}
	r = memoryCheckResult{Severity: "high", HighGB: 5, CriticalGB: 2}
	if got := thresholdFor(r); got != 5 {
		t.Errorf("high thresholdFor = %v, want 5", got)
	}
}

// TestDeaconMemoryCheckRegistered ensures the command is wired into deaconCmd.
// Pairs with TestDeaconSubcommandsRegistered in deacon_test.go but isolates
// the failure to this file when memory-check specifically is missing.
func TestDeaconMemoryCheckRegistered(t *testing.T) {
	var found *cobra.Command
	for _, sub := range deaconCmd.Commands() {
		if sub.Name() == "memory-check" {
			found = sub
			break
		}
	}
	if found == nil {
		t.Fatal("deaconCmd missing memory-check subcommand")
	}

	// Sanity-check the alias too — patrol formula uses the canonical name,
	// but operators reach for the short form.
	wantAliases := map[string]bool{"mem-check": false, "memcheck": false}
	for _, a := range found.Aliases {
		if _, ok := wantAliases[a]; ok {
			wantAliases[a] = true
		}
	}
	for name, ok := range wantAliases {
		if !ok {
			t.Errorf("memory-check missing alias %q", name)
		}
	}

	// Flags we promise in the Long help.
	for _, name := range []string{"warn", "high", "critical", "json", "escalate", "dry-run"} {
		if found.Flag(name) == nil {
			t.Errorf("memory-check missing --%s flag", name)
		}
	}
}

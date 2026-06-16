package daemon

import (
	"fmt"
	"testing"
)

func TestIsAgentSession(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"hq-mayor", true},
		{"rig-witness", true},
		{"rig-refinery", true},
		{"rig-polecat-abc", true},
		{"hq-deacon", true},
		{"hq-boot", true},
		{"rig-dog-fido", true},
		{"my-personal-session", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isAgentSession(tt.name); got != tt.want {
			t.Errorf("isAgentSession(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLoadAverage1_DoesNotPanic(t *testing.T) {
	load := loadAverage1()
	if load < 0 {
		t.Errorf("load average should be >= 0, got %f", load)
	}
}

func TestAvailableMemoryGB_DoesNotPanic(t *testing.T) {
	mem := availableMemoryGB()
	if mem < 0 {
		t.Errorf("available memory should be >= 0, got %f", mem)
	}
}

// ramDerivedSessionCeiling converts a RAM budget into a session count and
// disables itself when either knob is zero. See gu-tawx0.
func TestRAMDerivedSessionCeiling(t *testing.T) {
	total := totalMemoryGB()
	if total <= 0 {
		t.Skip("total memory unavailable on this platform")
	}

	// fraction or per-session of 0 disables the ceiling.
	if got := ramDerivedSessionCeiling(0, 1.0); got != 0 {
		t.Errorf("zero fraction: got %d, want 0 (disabled)", got)
	}
	if got := ramDerivedSessionCeiling(0.6, 0); got != 0 {
		t.Errorf("zero per-session: got %d, want 0 (disabled)", got)
	}

	// A realistic config yields floor(total * fraction / per-session).
	want := int((total * 0.6) / 1.0)
	if want < 1 {
		want = 1
	}
	if got := ramDerivedSessionCeiling(0.6, 1.0); got != want {
		t.Errorf("ceiling: got %d, want %d (total=%.1fGB)", got, want, total)
	}

	// An aggressive config never drops below 1 — the town must admit something.
	if got := ramDerivedSessionCeiling(0.0001, 100000); got != 1 {
		t.Errorf("tiny budget: got %d, want 1 (floor clamp)", got)
	}
}

// checkPressure enforces the RAM-derived session ceiling against ALL agent
// roles and admits below it. See gu-tawx0.
func TestCheckPressure_RAMDerivedSessionCeiling(t *testing.T) {
	if totalMemoryGB() <= 0 {
		t.Skip("total memory unavailable on this platform")
	}

	// Per-session = total memory so the RAM-derived ceiling is exactly 1
	// (fraction 1.0 * total / total). Isolate by disabling the reactive memory
	// budget and the static cap.
	total := totalMemoryGB()
	d := newGateDaemon(t, fmt.Sprintf(
		`{"pressure_mem_budget_fraction":0,"pressure_max_sessions":0,"pressure_session_ceiling_fraction":1.0,"pressure_session_mem_gb":%f}`,
		total))

	// One session already live: at the ceiling → defer, for every role.
	d.countAgentSessionsFn = func() int { return 1 }
	for _, role := range []string{"witness", "refinery", "crew", "polecat", "dog", "boot"} {
		if p := d.checkPressure(role); p.OK {
			t.Errorf("role %s: expected deferral at session ceiling, got OK", role)
		}
	}

	// Zero sessions live: below the ceiling → admit.
	d.countAgentSessionsFn = func() int { return 0 }
	if p := d.checkPressure("polecat"); !p.OK {
		t.Errorf("expected admission below ceiling, got deferral: %s", p.Reason)
	}
}

// effectiveSessionCeiling binds to the smaller of the static cap and the
// RAM-derived ceiling, ignoring whichever is disabled.
func TestEffectiveSessionCeiling(t *testing.T) {
	tests := []struct {
		name        string
		maxSessions int
		ramCeiling  int
		want        int
	}{
		{"both disabled", 0, 0, 0},
		{"only static", 10, 0, 10},
		{"only ram", 0, 75, 75},
		{"static binds (lower)", 50, 75, 50},
		{"ram binds (lower)", 100, 75, 75},
		{"equal", 60, 60, 60},
	}
	for _, tt := range tests {
		if got := effectiveSessionCeiling(tt.maxSessions, tt.ramCeiling); got != tt.want {
			t.Errorf("%s: effectiveSessionCeiling(%d, %d) = %d, want %d",
				tt.name, tt.maxSessions, tt.ramCeiling, got, tt.want)
		}
	}
}

package daemon

import (
	"testing"
)

func TestMainCIBreakInterval_Default(t *testing.T) {
	got := mainCIBreakInterval(nil)
	if got != defaultMainCIBreakInterval {
		t.Errorf("mainCIBreakInterval(nil) = %v, want %v", got, defaultMainCIBreakInterval)
	}
}

func TestMainCIBreakInterval_Configured(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainCIBreak: &MainCIBreakConfig{
				Enabled:     true,
				IntervalStr: "30s",
			},
		},
	}
	got := mainCIBreakInterval(config)
	if got.Seconds() != 30 {
		t.Errorf("mainCIBreakInterval(30s) = %v, want 30s", got)
	}
}

func TestMainCIBreakInterval_InvalidFallsBack(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainCIBreak: &MainCIBreakConfig{
				Enabled:     true,
				IntervalStr: "not-a-duration",
			},
		},
	}
	got := mainCIBreakInterval(config)
	if got != defaultMainCIBreakInterval {
		t.Errorf("mainCIBreakInterval(invalid) = %v, want %v", got, defaultMainCIBreakInterval)
	}
}

func TestMainCIBreakFingerprint_Stable(t *testing.T) {
	fp1 := mainCIBreakFingerprint("esc-1", "gastown_upstream", "abc123")
	fp2 := mainCIBreakFingerprint("esc-1", "gastown_upstream", "abc123")
	if fp1 != fp2 {
		t.Errorf("fingerprint not stable: %q != %q", fp1, fp2)
	}
	if len(fp1) != 12 {
		t.Errorf("fingerprint length = %d, want 12", len(fp1))
	}
}

func TestMainCIBreakFingerprint_Varies(t *testing.T) {
	fp1 := mainCIBreakFingerprint("esc-1", "rig-a", "abc123")
	fp2 := mainCIBreakFingerprint("esc-1", "rig-b", "abc123")
	fp3 := mainCIBreakFingerprint("esc-2", "rig-a", "abc123")
	if fp1 == fp2 {
		t.Error("fingerprint should differ across rigs")
	}
	if fp1 == fp3 {
		t.Error("fingerprint should differ across escalation IDs")
	}
}

func TestMinInt(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{3, 5, 3},
		{5, 3, 3},
		{0, 0, 0},
		{-1, 1, -1},
	}
	for _, tt := range tests {
		got := minInt(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("minInt(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestMainCIBreakEvent_Fields verifies the event struct has all required fields.
func TestMainCIBreakEvent_Fields(t *testing.T) {
	ev := MainCIBreakEvent{
		RigName:      "gastown_upstream",
		CommitSHA:    "abc123def456",
		PreviousSHA:  "prev789",
		MRBeadID:     "gt-mr42",
		EscalationID: "esc-001",
		Body:         "full escalation text",
	}

	if ev.RigName != "gastown_upstream" {
		t.Errorf("RigName = %q", ev.RigName)
	}
	if ev.CommitSHA != "abc123def456" {
		t.Errorf("CommitSHA = %q", ev.CommitSHA)
	}
	if ev.PreviousSHA != "prev789" {
		t.Errorf("PreviousSHA = %q", ev.PreviousSHA)
	}
	if ev.MRBeadID != "gt-mr42" {
		t.Errorf("MRBeadID = %q", ev.MRBeadID)
	}
	if ev.EscalationID != "esc-001" {
		t.Errorf("EscalationID = %q", ev.EscalationID)
	}
	if ev.Body != "full escalation text" {
		t.Errorf("Body = %q", ev.Body)
	}
}

// TestMainCIBreakConfig_JSON verifies config deserialization.
func TestMainCIBreakConfig_JSON(t *testing.T) {
	cfg := &MainCIBreakConfig{
		Enabled:     true,
		IntervalStr: "45s",
	}
	if !cfg.Enabled {
		t.Error("expected Enabled = true")
	}
	if cfg.IntervalStr != "45s" {
		t.Errorf("IntervalStr = %q, want 45s", cfg.IntervalStr)
	}
}

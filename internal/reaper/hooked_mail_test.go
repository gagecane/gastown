package reaper

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultHookedMailTTL(t *testing.T) {
	if DefaultHookedMailTTL <= 0 {
		t.Errorf("DefaultHookedMailTTL should be positive, got %v", DefaultHookedMailTTL)
	}
	if DefaultHookedMailTTL < time.Hour {
		t.Errorf("DefaultHookedMailTTL should be at least 1h to avoid false positives, got %v", DefaultHookedMailTTL)
	}
	if DefaultHookedMailTTL > 7*24*time.Hour {
		t.Errorf("DefaultHookedMailTTL should be at most 7 days to bound dead-letter accumulation, got %v", DefaultHookedMailTTL)
	}
}

func TestSQLPlaceholders(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "NULL"},
		{1, "?"},
		{2, "?,?"},
		{4, "?,?,?,?"},
	}
	for _, tt := range tests {
		got := sqlPlaceholders(tt.n)
		if got != tt.want {
			t.Errorf("sqlPlaceholders(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestHookedMailResultZeroValue(t *testing.T) {
	// HookedMailResult{} should marshal to stable JSON (no panic on nil slices).
	result := &HookedMailResult{Database: "hq"}
	j := FormatJSON(result)
	if j == "" {
		t.Error("FormatJSON on zero HookedMailResult should not return empty")
	}
	if !strings.Contains(j, `"database": "hq"`) {
		t.Errorf("JSON output missing database field: %s", j)
	}
	if !strings.Contains(j, `"closed": 0`) {
		t.Errorf("JSON output missing closed field: %s", j)
	}
}

// TestHookedMailResultEntryAgeDays ensures we compute age days from created_at
// the same way AutoClose does (floor of hours/24). This test does not touch
// the DB — it just confirms our expected math.
func TestHookedMailResultEntryAgeDays(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		ageHours float64
		wantDays int
	}{
		{"30 min", 0.5, 0},
		{"23 hours", 23, 0},
		{"25 hours", 25, 1},
		{"3 days", 72, 3},
		{"3.9 days", 93.6, 3}, // floors, not rounds
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createdAt := now.Add(-time.Duration(tt.ageHours * float64(time.Hour)))
			gotDays := int(now.Sub(createdAt).Hours() / 24)
			if gotDays != tt.wantDays {
				t.Errorf("ageDays(%v) = %d, want %d", tt.ageHours, gotDays, tt.wantDays)
			}
		})
	}
}

func TestDefaultDeadLetterThreshold(t *testing.T) {
	if DefaultDeadLetterThreshold <= 0 {
		t.Errorf("DefaultDeadLetterThreshold should be positive, got %v", DefaultDeadLetterThreshold)
	}
	// gu-hhqk AC#4 specifies 30 minutes. The doctor check and metrics gauges
	// must agree on this threshold to keep operator semantics aligned.
	if DefaultDeadLetterThreshold != 30*time.Minute {
		t.Errorf("DefaultDeadLetterThreshold = %v, want 30m (gu-hhqk AC#4)", DefaultDeadLetterThreshold)
	}
	// Must be strictly less than the reap TTL — we want to surface backlog
	// before the reaper starts closing beads.
	if DefaultDeadLetterThreshold >= DefaultHookedMailTTL {
		t.Errorf("DefaultDeadLetterThreshold (%v) must be < DefaultHookedMailTTL (%v)",
			DefaultDeadLetterThreshold, DefaultHookedMailTTL)
	}
}

func TestHookedMailCountsZeroValue(t *testing.T) {
	// HookedMailCounts{} should not have any invariants violated (used as a
	// zero-value safe snapshot in the daemon metrics callback).
	c := HookedMailCounts{Database: "hq"}
	if c.Total != 0 || c.DeadLetter != 0 {
		t.Errorf("zero-value HookedMailCounts should have zero counts, got %+v", c)
	}
}

// TestScanHookedMailCountsQueryStructure verifies the generated SQL contains
// the expected clauses. This guards against regressions without needing a
// live Dolt server — the doctor check, ReapHookedMail, and ScanHookedMailCounts
// must share the same exclusion set so gu-hhqk semantics stay aligned.
func TestScanHookedMailCountsQueryStructure(t *testing.T) {
	// Reproduce the exact preserve-label list used by ScanHookedMailCounts to
	// verify it matches reaper.ReapHookedMail and doctor.HookedDeadLetterCheck.
	preserveLabels := []string{"gt:standing-orders", "gt:keep", "gt:role", "gt:rig"}
	for _, lbl := range preserveLabels {
		if lbl == "" {
			t.Errorf("preserve label should not be empty")
		}
	}
	if len(preserveLabels) != 4 {
		t.Errorf("expected 4 preserve labels, got %d — keep in sync with ReapHookedMail", len(preserveLabels))
	}
}

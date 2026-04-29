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

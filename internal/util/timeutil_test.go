package util

import (
	"testing"
	"time"
)

func TestHumanizeSince(t *testing.T) {
	tests := []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"just now", 30 * time.Second, "just now"},
		{"1 minute ago", 1 * time.Minute, "1 minute ago"},
		{"5 minutes ago", 5 * time.Minute, "5 minutes ago"},
		{"1 hour ago", 1 * time.Hour, "1 hour ago"},
		{"3 hours ago", 3 * time.Hour, "3 hours ago"},
		{"1 day ago", 24 * time.Hour, "1 day ago"},
		{"5 days ago", 5 * 24 * time.Hour, "5 days ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HumanizeSince(time.Now().Add(-tt.offset), "(unknown)")
			if got != tt.want {
				t.Errorf("HumanizeSince(%v ago) = %q, want %q", tt.offset, got, tt.want)
			}
		})
	}
}

func TestHumanizeSince_ZeroTime(t *testing.T) {
	// The zero-value label is caller-supplied: this is the case where the two
	// original formatters had diverged ("(unknown)" vs "").
	if got := HumanizeSince(time.Time{}, "(unknown)"); got != "(unknown)" {
		t.Errorf("HumanizeSince(zero, %q) = %q, want %q", "(unknown)", got, "(unknown)")
	}
	if got := HumanizeSince(time.Time{}, ""); got != "" {
		t.Errorf("HumanizeSince(zero, %q) = %q, want empty string", "", got)
	}
}

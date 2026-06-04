package plugin

import (
	"testing"
	"time"
)

func TestParseCron_Errors(t *testing.T) {
	cases := []string{
		"",                  // empty
		"* * * *",           // 4 fields
		"* * * * * *",       // 6 fields
		"60 * * * *",        // minute out of range
		"* 24 * * *",        // hour out of range
		"* * 0 * *",         // day-of-month below min
		"* * 32 * *",        // day-of-month above max
		"* * * 13 *",        // month out of range
		"* * * * 8",         // day-of-week above max (0-7)
		"*/0 * * * *",       // zero step
		"5-1 * * * *",       // inverted range
		"abc * * * *",       // non-numeric
		"1,,2 * * * *",      // empty list term
	}
	for _, expr := range cases {
		if _, err := parseCron(expr); err == nil {
			t.Errorf("parseCron(%q) = nil error, want error", expr)
		}
	}
}

func TestParseCron_Matches(t *testing.T) {
	// 2026-06-04 is a Thursday (weekday 4).
	cases := []struct {
		expr  string
		t     time.Time
		match bool
	}{
		{"* * * * *", time.Date(2026, 6, 4, 12, 30, 0, 0, time.UTC), true},
		{"30 12 * * *", time.Date(2026, 6, 4, 12, 30, 0, 0, time.UTC), true},
		{"30 12 * * *", time.Date(2026, 6, 4, 12, 31, 0, 0, time.UTC), false},
		{"0 12 * * *", time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC), true},
		{"*/15 * * * *", time.Date(2026, 6, 4, 9, 45, 0, 0, time.UTC), true},
		{"*/15 * * * *", time.Date(2026, 6, 4, 9, 46, 0, 0, time.UTC), false},
		{"0 9-17 * * *", time.Date(2026, 6, 4, 13, 0, 0, 0, time.UTC), true},
		{"0 9-17 * * *", time.Date(2026, 6, 4, 18, 0, 0, 0, time.UTC), false},
		{"0 0 1,15 * *", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), true},
		{"0 0 1,15 * *", time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC), false},
		// Day-of-week: Thursday = 4. Sunday accepted as both 0 and 7.
		{"0 12 * * 4", time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC), true},
		{"0 12 * * thu", time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC), true},
		{"0 12 * * 1", time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC), false},
		// Sunday-as-7 normalizes to 0. 2026-06-07 is a Sunday.
		{"0 12 * * 7", time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC), true},
		{"0 12 * * 0", time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC), true},
		// Month names.
		{"0 0 4 jun *", time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC), true},
		{"0 0 4 jul *", time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC), false},
		// DOM and DOW both restricted → OR semantics. June 4 is Thursday(4),
		// not the 1st, but the DOW matches so it fires.
		{"0 12 1 * 4", time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC), true},
		// Neither DOM(1) nor DOW(mon=1) matches June 4 → no fire.
		{"0 12 1 * 1", time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC), false},
		// Stepped value from a start: "5/10" in minutes → 5,15,25,...
		{"5/10 * * * *", time.Date(2026, 6, 4, 9, 25, 0, 0, time.UTC), true},
		{"5/10 * * * *", time.Date(2026, 6, 4, 9, 20, 0, 0, time.UTC), false},
	}
	for _, c := range cases {
		s, err := parseCron(c.expr)
		if err != nil {
			t.Fatalf("parseCron(%q) error: %v", c.expr, err)
		}
		if got := s.matches(c.t); got != c.match {
			t.Errorf("parseCron(%q).matches(%v) = %v, want %v", c.expr, c.t, got, c.match)
		}
	}
}

func TestCronSchedule_Next(t *testing.T) {
	s, err := parseCron("0 12 * * *")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, 6, 4, 13, 0, 0, 0, time.UTC)
	next := s.Next(after)
	want := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestCronSchedule_Prev(t *testing.T) {
	s, err := parseCron("0 12 * * *")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 6, 4, 13, 0, 0, 0, time.UTC)
	prev := s.Prev(at)
	want := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	if !prev.Equal(want) {
		t.Errorf("Prev = %v, want %v", prev, want)
	}

	// Before today's fire → yesterday's fire.
	at2 := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC)
	prev2 := s.Prev(at2)
	want2 := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	if !prev2.Equal(want2) {
		t.Errorf("Prev = %v, want %v", prev2, want2)
	}
}

func TestCronSchedule_ImpossibleSchedule(t *testing.T) {
	// Feb 30 never occurs.
	s, err := parseCron("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	if !s.Prev(at).IsZero() {
		t.Error("Prev for Feb 30 should be zero (never)")
	}
	if !s.Next(at).IsZero() {
		t.Error("Next for Feb 30 should be zero (never)")
	}
}

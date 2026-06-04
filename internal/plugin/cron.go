package plugin

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSchedule is a parsed standard 5-field cron expression
// (minute hour day-of-month month day-of-week). It is intentionally
// self-contained: the gastown build runs offline (the public Go module
// proxy is unreachable), so a third-party cron library cannot be vendored.
// The supported syntax is the common standard cron subset:
//
//   - "*"            any value
//   - "5"            a single value
//   - "1-5"          an inclusive range
//   - "1,3,5"        a comma-separated list
//   - "*/15"         a step over the whole range
//   - "1-20/5"       a step over a range
//   - "5/10"         a step from a value to the field maximum
//   - JAN..DEC / SUN..SAT  three-letter names for month and day-of-week
//
// Day-of-month and day-of-week follow standard cron semantics: when BOTH
// are restricted (neither is "*") a time matches if EITHER field matches;
// when one is "*" only the other constrains the match.
type cronSchedule struct {
	minute  map[int]bool
	hour    map[int]bool
	dom     map[int]bool
	month   map[int]bool
	dow     map[int]bool
	domStar bool
	dowStar bool
}

var cronMonthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var cronDowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// parseCron parses a standard 5-field cron expression. It returns an error
// for any field that is malformed or out of range so a bad plugin.md schedule
// surfaces as a dispatch log line rather than silently never firing.
func parseCron(expr string) (*cronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression %q: expected 5 fields, got %d", expr, len(fields))
	}

	minute, _, err := parseCronField(fields[0], 0, 59, nil)
	if err != nil {
		return nil, fmt.Errorf("minute field: %w", err)
	}
	hour, _, err := parseCronField(fields[1], 0, 23, nil)
	if err != nil {
		return nil, fmt.Errorf("hour field: %w", err)
	}
	dom, domStar, err := parseCronField(fields[2], 1, 31, nil)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field: %w", err)
	}
	month, _, err := parseCronField(fields[3], 1, 12, cronMonthNames)
	if err != nil {
		return nil, fmt.Errorf("month field: %w", err)
	}
	dow, dowStar, err := parseCronField(fields[4], 0, 7, cronDowNames)
	if err != nil {
		return nil, fmt.Errorf("day-of-week field: %w", err)
	}
	// Normalize Sunday-as-7 to 0 so weekday lookups (time.Weekday) match.
	if dow[7] {
		dow[0] = true
		delete(dow, 7)
	}

	return &cronSchedule{
		minute:  minute,
		hour:    hour,
		dom:     dom,
		month:   month,
		dow:     dow,
		domStar: domStar,
		dowStar: dowStar,
	}, nil
}

// parseCronField parses a single comma-separated cron field into the set of
// matching integers. It returns isStar=true only for a literal "*" or "?"
// (NOT "*/N"), because the day-of-month / day-of-week OR rule keys off the
// wildcard, and a stepped wildcard is a restricted set.
func parseCronField(field string, min, max int, names map[string]int) (map[int]bool, bool, error) {
	set := make(map[int]bool)
	isStar := false

	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return nil, false, fmt.Errorf("empty term in %q", field)
		}

		rangePart := part
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			rangePart = part[:i]
			stepStr := part[i+1:]
			s, err := strconv.Atoi(stepStr)
			if err != nil || s < 1 {
				return nil, false, fmt.Errorf("invalid step %q", part)
			}
			step = s
		}

		var lo, hi int
		switch {
		case rangePart == "*" || rangePart == "?":
			lo, hi = min, max
			if part == "*" || part == "?" {
				isStar = true
			}
		case strings.Contains(rangePart, "-"):
			bounds := strings.SplitN(rangePart, "-", 2)
			var err error
			if lo, err = cronAtoi(bounds[0], names); err != nil {
				return nil, false, fmt.Errorf("invalid range start %q: %w", part, err)
			}
			if hi, err = cronAtoi(bounds[1], names); err != nil {
				return nil, false, fmt.Errorf("invalid range end %q: %w", part, err)
			}
		default:
			v, err := cronAtoi(rangePart, names)
			if err != nil {
				return nil, false, fmt.Errorf("invalid value %q: %w", part, err)
			}
			lo = v
			// A single value with a step (e.g. "5/10") runs from the value
			// to the field maximum; a bare value matches only itself.
			if step > 1 || strings.Contains(part, "/") {
				hi = max
			} else {
				hi = v
			}
		}

		if lo < min || hi > max || lo > hi {
			return nil, false, fmt.Errorf("value out of range [%d-%d] in %q", min, max, part)
		}
		for i := lo; i <= hi; i += step {
			set[i] = true
		}
	}

	return set, isStar, nil
}

// cronAtoi parses a cron numeric token, accepting three-letter names when a
// name map is supplied (case-insensitive).
func cronAtoi(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	return strconv.Atoi(s)
}

// matches reports whether time t (at minute granularity) satisfies the schedule.
func (s *cronSchedule) matches(t time.Time) bool {
	if !s.minute[t.Minute()] || !s.hour[t.Hour()] || !s.month[int(t.Month())] {
		return false
	}
	domMatch := s.dom[t.Day()]
	dowMatch := s.dow[int(t.Weekday())]
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowMatch
	case s.dowStar:
		return domMatch
	default:
		// Both restricted: standard cron matches on EITHER.
		return domMatch || dowMatch
	}
}

// Next returns the earliest time strictly after `after` (truncated to the
// minute) that matches the schedule. It returns the zero time if no match
// occurs within five years — which only happens for an impossible schedule
// such as Feb 30 — so callers can treat a zero result as "never".
func (s *cronSchedule) Next(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.AddDate(5, 0, 0)
	for t.Before(limit) {
		if s.matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// Prev returns the latest time at or before `at` (truncated to the minute)
// that matches the schedule — i.e. the most recent scheduled fire as of `at`.
// It returns the zero time if no match occurs within the preceding five years
// (an impossible schedule), so callers can treat a zero result as "never".
func (s *cronSchedule) Prev(at time.Time) time.Time {
	t := at.Truncate(time.Minute)
	limit := at.AddDate(-5, 0, 0)
	for t.After(limit) {
		if s.matches(t) {
			return t
		}
		t = t.Add(-time.Minute)
	}
	return time.Time{}
}
